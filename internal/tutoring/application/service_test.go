package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/tutoring/application"
	"github.com/edutrak/backend/internal/tutoring/domain"
	"github.com/edutrak/backend/internal/tutoring/ports"
	"github.com/google/uuid"
)

var (
	now       = time.Date(2026, 3, 10, 18, 30, 0, 0, time.UTC)
	termStart = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
)

type env struct {
	svc                       *application.Service
	repo                      *fakeRepo
	audit                     *fakeAudit
	ov                        *fakeOverviews
	tutorID, otherTutorID     uuid.UUID
	student, otherStudent     uuid.UUID
	group, otherGroup         uuid.UUID
	tutor, otherTutor         iddomain.Principal
	director, admin, teacher  iddomain.Principal
	studentP, teacherAndTutor iddomain.Principal
}

func principal(roles ...iddomain.Role) iddomain.Principal {
	return iddomain.Principal{UserID: uuid.New(), SessionID: uuid.New(), Roles: roles}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{repo: newRepo(), audit: &fakeAudit{}, ov: &fakeOverviews{}}
	e.tutor, e.otherTutor = principal(iddomain.RoleTutor), principal(iddomain.RoleTutor)
	e.director, e.admin = principal(iddomain.RoleDirector), principal(iddomain.RoleAdmin)
	e.teacher, e.studentP = principal(iddomain.RoleTeacher), principal(iddomain.RoleStudent)
	e.teacherAndTutor = principal(iddomain.RoleTeacher, iddomain.RoleTutor)
	e.tutorID, e.otherTutorID = e.tutor.UserID, e.otherTutor.UserID
	e.student, e.otherStudent, e.group, e.otherGroup = uuid.New(), uuid.New(), uuid.New(), uuid.New()

	mk := func(student, group uuid.UUID, tutor uuid.UUID, status, term string) domain.Enrollment {
		tid := tutor
		return domain.Enrollment{StudentID: student, GroupID: group, GroupCode: "2A", GroupTutorID: &tid, TermStatus: term,
			TermStart: termStart, EnrollmentStatus: status}
	}
	e.repo.addEnrollment(mk(e.student, e.group, e.tutorID, "ACTIVE", "active"))
	e.repo.addEnrollment(mk(e.otherStudent, e.otherGroup, e.otherTutorID, "ACTIVE", "active"))
	e.svc = application.NewService(application.Deps{
		Repo: e.repo, Overviews: e.ov, Audit: e.audit, Tx: fakeTx{e.repo}, Clock: fixedClock(now), NewID: uuid.New,
	})
	return e
}

func (e *env) create(p iddomain.Principal, student, group uuid.UUID, note string) (*domain.Entry, error) {
	return e.svc.CreateFollowUp(context.Background(), p, application.CreateInput{StudentID: student, GroupID: group, Note: note})
}

func is(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestCreateFollowUpByTheGroupTutor(t *testing.T) {
	e := newEnv(t)
	got, err := e.create(e.tutor, e.student, e.group, "  Acordamos revisar Matematicas  ")
	if err != nil {
		t.Fatal(err)
	}
	if got.TutorID != e.tutorID || got.Note != "Acordamos revisar Matematicas" || got.Date.Format(domain.DateLayout) != "2026-03-10" {
		t.Fatalf("unexpected follow-up: %+v", got)
	}
	if len(e.audit.events) != 1 || e.audit.events[0].Action != "tutoring.followup_created" || *e.audit.events[0].ActorUserID != e.tutorID {
		t.Fatalf("audit = %+v", e.audit.events)
	}
	// The audit event carries ids and the date, never the text of the note.
	for k, v := range e.audit.events[0].Metadata {
		if s, ok := v.(string); ok && strings.Contains(s, "Matematicas") {
			t.Fatalf("audit metadata %q leaks note content", k)
		}
	}
}

func TestCreateFollowUpAuthorizationMatrix(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name    string
		p       iddomain.Principal
		student uuid.UUID
		group   uuid.UUID
		want    error
	}{
		{"tutor of ANOTHER group", e.otherTutor, e.student, e.group, domain.ErrForbidden},
		{"tutor writing for a student of another group (not enrolled in the chosen group)", e.tutor, e.otherStudent, e.group, domain.ErrNotEnrolled},
		{"tutor naming the other group", e.tutor, e.otherStudent, e.otherGroup, domain.ErrForbidden},
		{"director supervises, does not write", e.director, e.student, e.group, domain.ErrForbidden},
		{"teacher", e.teacher, e.student, e.group, domain.ErrForbidden},
		{"student", e.studentP, e.student, e.group, domain.ErrForbidden},
		{"admin", e.admin, e.student, e.group, domain.ErrForbidden},
		{"teacher without a tutoring relation, even with the TUTOR role", e.teacherAndTutor, e.student, e.group, domain.ErrForbidden},
		{"nonexistent student", e.tutor, uuid.New(), e.group, domain.ErrBodyStudentNotFound},
		{"nonexistent group", e.tutor, e.student, uuid.New(), domain.ErrBodyGroupNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.create(c.p, c.student, c.group, "nota")
			is(t, err, c.want)
		})
	}
	if len(e.repo.followUps) != 0 || len(e.audit.events) != 0 {
		t.Fatalf("a rejected request left data behind: %d rows, %d events", len(e.repo.followUps), len(e.audit.events))
	}
}

func TestCreateFollowUpValidation(t *testing.T) {
	e := newEnv(t)
	_, err := e.create(e.tutor, e.student, e.group, "   ")
	is(t, err, domain.ErrNoteRequired)
	_, err = e.create(e.tutor, e.student, e.group, strings.Repeat("x", domain.MaxNoteLength+1))
	is(t, err, domain.ErrNoteTooLong)

	future := now.AddDate(0, 0, 1)
	_, err = e.svc.CreateFollowUp(context.Background(), e.tutor, application.CreateInput{StudentID: e.student, GroupID: e.group, Date: &future, Note: "n"})
	is(t, err, domain.ErrDateInFuture)
	before := termStart.AddDate(0, 0, -1)
	_, err = e.svc.CreateFollowUp(context.Background(), e.tutor, application.CreateInput{StudentID: e.student, GroupID: e.group, Date: &before, Note: "n"})
	is(t, err, domain.ErrDateBeforeTerm)
	ok := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if _, err = e.svc.CreateFollowUp(context.Background(), e.tutor, application.CreateInput{StudentID: e.student, GroupID: e.group, Date: &ok, Note: "n"}); err != nil {
		t.Fatalf("valid past date rejected: %v", err)
	}
}

func TestTodayUsesTheInstitutionTimezone(t *testing.T) {
	e := newEnv(t)
	// 2026-03-11 02:00 UTC is still 2026-03-10 in Mexico City.
	loc, _ := time.LoadLocation("America/Mexico_City")
	e.svc = application.NewService(application.Deps{Repo: e.repo, Overviews: e.ov, Audit: e.audit, Tx: fakeTx{e.repo},
		Clock: fixedClock(time.Date(2026, 3, 11, 2, 0, 0, 0, time.UTC)), NewID: uuid.New, Location: loc})
	got, err := e.create(e.tutor, e.student, e.group, "n")
	if err != nil || got.Date.Format(domain.DateLayout) != "2026-03-10" {
		t.Fatalf("date = %v, err = %v", got, err)
	}
}

func TestCreateFollowUpRequiresActiveEnrollmentAndOpenTerm(t *testing.T) {
	e := newEnv(t)
	tid := e.tutorID
	withdrawn, closed := uuid.New(), uuid.New()
	e.repo.addEnrollment(domain.Enrollment{StudentID: withdrawn, GroupID: e.group, GroupTutorID: &tid, TermStatus: "active", TermStart: termStart, EnrollmentStatus: "WITHDRAWN"})
	e.repo.addEnrollment(domain.Enrollment{StudentID: closed, GroupID: e.group, GroupTutorID: &tid, TermStatus: "closed", TermStart: termStart, EnrollmentStatus: "ACTIVE"})
	_, err := e.create(e.tutor, withdrawn, e.group, "n")
	is(t, err, domain.ErrEnrollmentInactive)
	_, err = e.create(e.tutor, closed, e.group, "n")
	is(t, err, domain.ErrTermClosed)
}

func TestFailedAuditRollsBackTheFollowUp(t *testing.T) {
	e := newEnv(t)
	e.audit.fail = true
	if _, err := e.create(e.tutor, e.student, e.group, "nota"); err == nil {
		t.Fatal("create must fail when its audit event cannot be written")
	}
	if len(e.repo.followUps) != 0 {
		t.Fatalf("follow-up survived a failed audit (%d rows)", len(e.repo.followUps))
	}
}

func TestHistoryFollowsTheCurrentTutorOfTheGroup(t *testing.T) {
	e := newEnv(t)
	first, err := e.create(e.tutor, e.student, e.group, "primera")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if list, total, err := e.svc.StudentHistory(ctx, e.tutor, e.student, nil, ports.Page{}); err != nil || total != 1 || len(list) != 1 {
		t.Fatalf("author cannot read own history: %v %d", err, total)
	}

	// The group changes tutor: the previous one loses access, the new one inherits the history.
	for k, en := range e.repo.enrollments {
		if k.group == e.group {
			id := e.otherTutorID
			en.GroupTutorID = &id
			e.repo.enrollments[k] = en
		}
	}
	_, _, err = e.svc.StudentHistory(ctx, e.tutor, e.student, nil, ports.Page{})
	is(t, err, domain.ErrForbidden)
	_, err = e.svc.GetFollowUp(ctx, e.tutor, first.ID)
	is(t, err, domain.ErrForbidden)
	_, err = e.svc.Overview(ctx, e.tutor, e.student, nil)
	is(t, err, domain.ErrForbidden)
	_, err = e.create(e.tutor, e.student, e.group, "ya no soy su tutor")
	is(t, err, domain.ErrForbidden)

	list, total, err := e.svc.StudentHistory(ctx, e.otherTutor, e.student, nil, ports.Page{})
	if err != nil || total != 1 || list[0].TutorID != e.tutorID {
		t.Fatalf("new tutor must see the history with the ORIGINAL author: %v %+v", err, list)
	}
	if got, err := e.svc.GetFollowUp(ctx, e.otherTutor, first.ID); err != nil || got.TutorID != e.tutorID {
		t.Fatalf("detail: %v %+v", err, got)
	}
}

func TestReadAuthorization(t *testing.T) {
	e := newEnv(t)
	f, _ := e.create(e.tutor, e.student, e.group, "privada")
	ctx := context.Background()
	for name, p := range map[string]iddomain.Principal{"admin": e.admin, "teacher": e.teacher, "student": e.studentP} {
		if _, err := e.svc.Students(ctx, p, ports.StudentFilter{}); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: Students = %v", name, err)
		}
		if _, err := e.svc.Overview(ctx, p, e.student, nil); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: Overview = %v", name, err)
		}
		if _, _, err := e.svc.StudentHistory(ctx, p, e.student, nil, ports.Page{}); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: StudentHistory = %v", name, err)
		}
		if _, _, err := e.svc.ListFollowUps(ctx, p, ports.FollowUpFilter{}, ports.Page{}); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: ListFollowUps = %v", name, err)
		}
		if _, err := e.svc.GetFollowUp(ctx, p, f.ID); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: GetFollowUp = %v", name, err)
		}
	}
	// A tutor of another group cannot read it either, and does not even see it listed.
	_, err := e.svc.GetFollowUp(ctx, e.otherTutor, f.ID)
	is(t, err, domain.ErrForbidden)
	if list, total, err := e.svc.ListFollowUps(ctx, e.otherTutor, ports.FollowUpFilter{}, ports.Page{}); err != nil || total != 0 || len(list) != 0 {
		t.Fatalf("foreign tutor listing: %v %d", err, total)
	}
	_, _, err = e.svc.StudentHistory(ctx, e.otherTutor, e.student, nil, ports.Page{})
	is(t, err, domain.ErrForbidden)
	// Naming a group the caller does not tutor is 403, a made-up one 404.
	g := e.group
	_, _, err = e.svc.StudentHistory(ctx, e.otherTutor, e.student, &g, ports.Page{})
	is(t, err, domain.ErrForbidden)
	unknown := uuid.New()
	_, _, err = e.svc.StudentHistory(ctx, e.tutor, e.student, &unknown, ports.Page{})
	is(t, err, domain.ErrContextNotFound)
	_, _, err = e.svc.StudentHistory(ctx, e.tutor, uuid.New(), nil, ports.Page{})
	is(t, err, domain.ErrStudentNotFound)
	_, err = e.svc.GetFollowUp(ctx, e.tutor, uuid.New())
	is(t, err, domain.ErrFollowUpNotFound)
}

func TestDirectorReadsButNeverWrites(t *testing.T) {
	e := newEnv(t)
	f, _ := e.create(e.tutor, e.student, e.group, "nota")
	ctx := context.Background()
	if _, err := e.svc.GetFollowUp(ctx, e.director, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, total, err := e.svc.ListFollowUps(ctx, e.director, ports.FollowUpFilter{}, ports.Page{}); err != nil || total != 1 {
		t.Fatalf("director listing: %v %d", err, total)
	}
	if _, err := e.svc.Overview(ctx, e.director, e.student, nil); err != nil {
		t.Fatal(err)
	}
	if rows, err := e.svc.Students(ctx, e.director, ports.StudentFilter{}); err != nil || len(rows) != 2 {
		t.Fatalf("director students: %v %d", err, len(rows))
	}
	_, err := e.create(e.director, e.student, e.group, "x")
	is(t, err, domain.ErrForbidden)
}

func TestStudentsListIsScopedAndFiltered(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rows, err := e.svc.Students(ctx, e.tutor, ports.StudentFilter{})
	if err != nil || len(rows) != 1 || rows[0].StudentID != e.student {
		t.Fatalf("tutor students: %v %+v", err, rows)
	}
	if _, err := e.svc.Students(ctx, e.tutor, ports.StudentFilter{Status: "bogus"}); !errors.Is(err, domain.ErrInvalidFilter) {
		t.Fatalf("bogus status: %v", err)
	}
	if rows, _ := e.svc.Students(ctx, e.tutor, ports.StudentFilter{Status: "WITHDRAWN"}); len(rows) != 0 {
		t.Fatalf("withdrawn rows: %d", len(rows))
	}
}

func TestOverviewUsesTheChosenGroupOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ov, err := e.svc.Overview(ctx, e.tutor, e.student, nil)
	if err != nil || ov.GroupID != e.group || len(ov.Subjects) != 1 {
		t.Fatalf("overview: %v %+v", err, ov)
	}
	g := e.otherGroup
	_, err = e.svc.Overview(ctx, e.tutor, e.student, &g)
	is(t, err, domain.ErrContextNotFound) // the student is not enrolled in that group
	_, err = e.svc.Overview(ctx, e.tutor, e.otherStudent, nil)
	is(t, err, domain.ErrForbidden)
	if e.ov.calls != 1 {
		t.Fatalf("academic reader called %d times; forbidden requests must not read academic data", e.ov.calls)
	}
}

func TestPaging(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 3; i++ {
		if _, err := e.create(e.tutor, e.student, e.group, "n"); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	list, total, err := e.svc.StudentHistory(ctx, e.tutor, e.student, nil, ports.Page{Limit: 2})
	if err != nil || total != 3 || len(list) != 2 {
		t.Fatalf("page 1: %v %d %d", err, total, len(list))
	}
	list, _, _ = e.svc.StudentHistory(ctx, e.tutor, e.student, nil, ports.Page{Limit: 2, Offset: 2})
	if len(list) != 1 {
		t.Fatalf("page 2 len = %d", len(list))
	}
	for _, bad := range []ports.Page{{Limit: 201}, {Limit: -1}, {Limit: 5, Offset: -1}} {
		if _, _, err := e.svc.StudentHistory(ctx, e.tutor, e.student, nil, bad); !errors.Is(err, domain.ErrInvalidPaging) {
			t.Errorf("page %+v accepted: %v", bad, err)
		}
	}
}
