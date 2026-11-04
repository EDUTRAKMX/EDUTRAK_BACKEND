package application_test

import (
	"context"
	"errors"
	"sort"
	"time"

	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	"github.com/edutrak/backend/internal/tutoring/domain"
	"github.com/edutrak/backend/internal/tutoring/ports"
	"github.com/google/uuid"
)

type key struct{ student, group uuid.UUID }

// fakeRepo is an in-memory Repository. Its state can be snapshotted so fakeTx can roll back.
type fakeRepo struct {
	students    map[uuid.UUID]bool
	groups      map[uuid.UUID]bool
	enrollments map[key]domain.Enrollment
	followUps   []domain.FollowUp
	createCalls int
}

func newRepo() *fakeRepo {
	return &fakeRepo{students: map[uuid.UUID]bool{}, groups: map[uuid.UUID]bool{}, enrollments: map[key]domain.Enrollment{}}
}

func (r *fakeRepo) addEnrollment(e domain.Enrollment) {
	r.students[e.StudentID], r.groups[e.GroupID] = true, true
	r.enrollments[key{e.StudentID, e.GroupID}] = e
}

func (r *fakeRepo) Context(_ context.Context, s, g uuid.UUID, _ bool) (*domain.Enrollment, error) {
	if e, ok := r.enrollments[key{s, g}]; ok {
		return &e, nil
	}
	switch {
	case !r.students[s]:
		return nil, domain.ErrBodyStudentNotFound
	case !r.groups[g]:
		return nil, domain.ErrBodyGroupNotFound
	}
	return nil, domain.ErrNotEnrolled
}

func (r *fakeRepo) StudentExists(_ context.Context, s uuid.UUID) (bool, error) {
	return r.students[s], nil
}

func (r *fakeRepo) inScope(sc ports.Scope, e domain.Enrollment) bool {
	return sc.All || (sc.TutorID != nil && e.GroupTutorID != nil && *e.GroupTutorID == *sc.TutorID)
}

func (r *fakeRepo) Enrollments(_ context.Context, s uuid.UUID, sc ports.Scope) ([]domain.Enrollment, error) {
	var out []domain.Enrollment
	for k, e := range r.enrollments {
		if k.student == s && r.inScope(sc, e) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TermStart.After(out[j].TermStart) })
	return out, nil
}

func (r *fakeRepo) Students(_ context.Context, sc ports.Scope, f ports.StudentFilter) ([]domain.Enrollment, error) {
	out := []domain.Enrollment{}
	for _, e := range r.enrollments {
		if r.inScope(sc, e) && e.EnrollmentStatus == f.Status && (f.GroupID == nil || *f.GroupID == e.GroupID) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (r *fakeRepo) Create(_ context.Context, f *domain.FollowUp) error {
	r.createCalls++
	f.CreatedAt = time.Now()
	r.followUps = append(r.followUps, *f)
	return nil
}

func (r *fakeRepo) entry(f domain.FollowUp) domain.Entry {
	e := r.enrollments[key{f.StudentID, f.GroupID}]
	return domain.Entry{FollowUp: f, GroupCode: e.GroupCode, GroupTutorID: e.GroupTutorID}
}

func (r *fakeRepo) Get(_ context.Context, id uuid.UUID) (*domain.Entry, error) {
	for _, f := range r.followUps {
		if f.ID == id {
			e := r.entry(f)
			return &e, nil
		}
	}
	return nil, domain.ErrFollowUpNotFound
}

func (r *fakeRepo) List(_ context.Context, sc ports.Scope, f ports.FollowUpFilter, p ports.Page) ([]domain.Entry, int, error) {
	var all []domain.Entry
	for _, fu := range r.followUps {
		e := r.entry(fu)
		if !(sc.All || (sc.TutorID != nil && e.GroupTutorID != nil && *e.GroupTutorID == *sc.TutorID)) {
			continue
		}
		if f.StudentID != nil && *f.StudentID != fu.StudentID || f.GroupID != nil && *f.GroupID != fu.GroupID {
			continue
		}
		all = append(all, e)
	}
	total := len(all)
	if p.Offset >= len(all) {
		return []domain.Entry{}, total, nil
	}
	all = all[p.Offset:]
	if len(all) > p.Limit {
		all = all[:p.Limit]
	}
	return all, total, nil
}

type fakeOverviews struct{ calls int }

func (o *fakeOverviews) Subjects(context.Context, uuid.UUID, uuid.UUID) ([]domain.SubjectOverview, error) {
	o.calls++
	return []domain.SubjectOverview{{SubjectCode: "MAT1"}}, nil
}

type fakeAudit struct {
	events []auditdomain.Event
	fail   bool
}

func (a *fakeAudit) Record(_ context.Context, e auditdomain.Event) error {
	if a.fail {
		return errors.New("audit unavailable")
	}
	a.events = append(a.events, e)
	return nil
}

// fakeTx rolls the repository back when the function fails, like a database transaction.
type fakeTx struct{ repo *fakeRepo }

func (t fakeTx) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	saved := append([]domain.FollowUp(nil), t.repo.followUps...)
	if err := fn(ctx); err != nil {
		t.repo.followUps = saved
		return err
	}
	return nil
}

type fixedClock time.Time

func (c fixedClock) Now() time.Time { return time.Time(c) }
