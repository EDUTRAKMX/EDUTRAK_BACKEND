package application_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/edutrak/backend/internal/analytics/application"
	"github.com/edutrak/backend/internal/analytics/domain"
	"github.com/edutrak/backend/internal/analytics/ports"
	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

// --- fakes ------------------------------------------------------------------------------------------

type fakeFacts struct {
	groups   map[uuid.UUID]domain.GroupInfo
	students map[uuid.UUID][]domain.StudentFacts // by group
	exists   map[uuid.UUID]bool
	fail     error
	loads    int
}

func (f *fakeFacts) Group(_ context.Context, id uuid.UUID, _ bool) (*domain.GroupInfo, error) {
	g, ok := f.groups[id]
	if !ok {
		return nil, domain.ErrGroupNotFound
	}
	return &g, nil
}
func (f *fakeFacts) StudentExists(_ context.Context, id uuid.UUID) (bool, error) {
	return f.exists[id], nil
}
func (f *fakeFacts) Enrollment(_ context.Context, s, g uuid.UUID) (*domain.EnrollmentRef, error) {
	for _, st := range f.students[g] {
		if st.StudentID == s {
			return &domain.EnrollmentRef{GroupInfo: f.groups[g], StudentID: s, Status: st.EnrollmentStatus}, nil
		}
	}
	return nil, domain.ErrContextNotFound
}
func (f *fakeFacts) Enrollments(_ context.Context, s uuid.UUID, sc ports.Scope) ([]domain.EnrollmentRef, error) {
	var out []domain.EnrollmentRef
	for gid, list := range f.students {
		for _, st := range list {
			g := f.groups[gid]
			if st.StudentID == s && (sc.All || (sc.TutorID != nil && g.TutorID != nil && *g.TutorID == *sc.TutorID)) {
				out = append(out, domain.EnrollmentRef{GroupInfo: g, StudentID: s, Status: st.EnrollmentStatus})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TermStart.After(out[j].TermStart) })
	return out, nil
}
func (f *fakeFacts) Students(_ context.Context, g uuid.UUID, student *uuid.UUID) ([]domain.StudentFacts, error) {
	f.loads++
	if f.fail != nil {
		return nil, f.fail
	}
	var out []domain.StudentFacts
	for _, st := range f.students[g] {
		if (student == nil && st.EnrollmentStatus == "ACTIVE") || (student != nil && st.StudentID == *student) {
			out = append(out, st)
		}
	}
	return out, nil
}
func (f *fakeFacts) Overview(_ context.Context, sc ports.Scope, _ ports.OverviewFilter) ([]domain.GroupOverview, error) {
	var out []domain.GroupOverview
	for _, g := range f.groups {
		if sc.All || (sc.TutorID != nil && g.TutorID != nil && *g.TutorID == *sc.TutorID) {
			out = append(out, domain.GroupOverview{Group: g})
		}
	}
	return out, nil
}

type fakeAlerts struct {
	rows    []domain.StoredAlert
	creates int
	locks   int
	groupOf map[uuid.UUID]domain.GroupInfo
}

func (a *fakeAlerts) LockGroup(context.Context, uuid.UUID) error { a.locks++; return nil }
func (a *fakeAlerts) Unresolved(_ context.Context, g uuid.UUID, s *uuid.UUID) ([]domain.StoredAlert, error) {
	var out []domain.StoredAlert
	for _, r := range a.rows {
		if r.GroupID == g && r.Status != domain.AlertResolved && (s == nil || r.StudentID == *s) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (a *fakeAlerts) Create(_ context.Context, n ports.NewAlert) error {
	for _, r := range a.rows { // same guarantee as the partial unique index
		if r.Status != domain.AlertResolved && r.StudentID == n.Candidate.StudentID && r.Rule == n.Candidate.Rule && r.Key == n.Candidate.Key {
			return errors.New("duplicate alert")
		}
	}
	a.creates++
	a.rows = append(a.rows, domain.StoredAlert{ID: n.ID, StudentID: n.Candidate.StudentID, GroupID: n.GroupID, SubjectEnrollmentID: n.Candidate.SubjectEnrollmentID,
		Rule: n.Candidate.Rule, Key: n.Candidate.Key, Message: n.Candidate.Message, Evidence: n.Evidence, Status: domain.AlertOpen, CreatedAt: n.Now, UpdatedAt: n.Now, EvaluatedAt: n.Now})
	return nil
}
func (a *fakeAlerts) idx(id uuid.UUID) int {
	for i, r := range a.rows {
		if r.ID == id {
			return i
		}
	}
	return -1
}
func (a *fakeAlerts) Update(_ context.Context, id uuid.UUID, m string, e []byte, now time.Time) error {
	i := a.idx(id)
	a.rows[i].Message, a.rows[i].Evidence, a.rows[i].UpdatedAt, a.rows[i].EvaluatedAt = m, e, now, now
	return nil
}
func (a *fakeAlerts) Touch(_ context.Context, ids []uuid.UUID, now time.Time) error {
	for _, id := range ids {
		a.rows[a.idx(id)].EvaluatedAt = now
	}
	return nil
}
func (a *fakeAlerts) Resolve(_ context.Context, ids []uuid.UUID, now time.Time) error {
	for _, id := range ids {
		i := a.idx(id)
		a.rows[i].Status, a.rows[i].ResolvedAt = domain.AlertResolved, &now
	}
	return nil
}
func (a *fakeAlerts) view(r domain.StoredAlert) domain.AlertView {
	g := a.groupOf[r.GroupID]
	return domain.AlertView{StoredAlert: r, GroupCode: g.Code, GroupTutorID: g.TutorID}
}
func (a *fakeAlerts) Get(_ context.Context, id uuid.UUID) (*domain.AlertView, error) {
	if i := a.idx(id); i >= 0 {
		v := a.view(a.rows[i])
		return &v, nil
	}
	return nil, domain.ErrAlertNotFound
}
func (a *fakeAlerts) List(_ context.Context, sc ports.Scope, f ports.AlertFilter, p ports.Page) ([]domain.AlertView, int, error) {
	var out []domain.AlertView
	for _, r := range a.rows {
		v := a.view(r)
		if !(sc.All || (sc.TutorID != nil && v.GroupTutorID != nil && *v.GroupTutorID == *sc.TutorID)) {
			continue
		}
		if f.StudentID != nil && *f.StudentID != r.StudentID || f.GroupID != nil && *f.GroupID != r.GroupID {
			continue
		}
		if (f.Status == "" && r.Status == domain.AlertResolved) || (f.Status != "" && f.Status != "ALL" && string(r.Status) != f.Status) {
			continue
		}
		out = append(out, v)
	}
	return out, len(out), nil
}
func (a *fakeAlerts) OpenCounts(_ context.Context, g uuid.UUID) (map[domain.Rule]int, error) {
	m := map[domain.Rule]int{}
	for _, r := range a.rows {
		if r.GroupID == g && r.Status != domain.AlertResolved {
			m[r.Rule]++
		}
	}
	return m, nil
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

type fakeTx struct{ alerts *fakeAlerts }

func (t fakeTx) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	saved := append([]domain.StoredAlert(nil), t.alerts.rows...)
	if err := fn(ctx); err != nil {
		t.alerts.rows = saved
		return err
	}
	return nil
}

type fixedClock struct{ t time.Time }

func (c *fixedClock) Now() time.Time { return c.t }

// --- environment ---------------------------------------------------------------------------------------

type env struct {
	svc                      *application.Service
	facts                    *fakeFacts
	alerts                   *fakeAlerts
	audit                    *fakeAudit
	clock                    *fixedClock
	tutor, otherTutor        iddomain.Principal
	director, admin, teacher iddomain.Principal
	student                  iddomain.Principal
	group, otherGroup        uuid.UUID
	s1, s2                   uuid.UUID
	unit                     domain.UnitFact
}

func principal(roles ...iddomain.Role) iddomain.Principal {
	return iddomain.Principal{UserID: uuid.New(), SessionID: uuid.New(), Roles: roles}
}

func h32(v int32) *int32 { return &v }

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{facts: &fakeFacts{groups: map[uuid.UUID]domain.GroupInfo{}, students: map[uuid.UUID][]domain.StudentFacts{}, exists: map[uuid.UUID]bool{}},
		alerts: &fakeAlerts{groupOf: map[uuid.UUID]domain.GroupInfo{}}, audit: &fakeAudit{}, clock: &fixedClock{time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)}}
	e.tutor, e.otherTutor = principal(iddomain.RoleTutor), principal(iddomain.RoleTutor)
	e.director, e.admin, e.teacher, e.student = principal(iddomain.RoleDirector), principal(iddomain.RoleAdmin), principal(iddomain.RoleTeacher), principal(iddomain.RoleStudent)
	e.group, e.otherGroup, e.s1, e.s2 = uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tid, oid := e.tutor.UserID, e.otherTutor.UserID
	mkGroup := func(id uuid.UUID, tutor *uuid.UUID, code string) {
		g := domain.GroupInfo{ID: id, Code: code, TutorID: tutor, TermName: "2026-1", TermStatus: "active", TermStart: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)}
		e.facts.groups[id], e.alerts.groupOf[id] = g, g
	}
	mkGroup(e.group, &tid, "2A")
	mkGroup(e.otherGroup, &oid, "2B")
	e.unit = domain.UnitFact{UnitID: uuid.New(), Number: 2, Name: "Calculo"}
	subject := domain.SubjectFacts{SubjectEnrollmentID: uuid.New(), AssignmentID: uuid.New(), SubjectCode: "MAT1", SubjectName: "Matematicas I", Attempt: 1, Status: "ACTIVE",
		Units: []domain.UnitFact{e.unit}}
	e.facts.students[e.group] = []domain.StudentFacts{
		{StudentID: e.s1, Number: "40000001", EnrollmentStatus: "ACTIVE", Subjects: []domain.SubjectFacts{subject}},
		{StudentID: e.s2, Number: "40000002", EnrollmentStatus: "ACTIVE"},
	}
	e.facts.students[e.otherGroup] = []domain.StudentFacts{{StudentID: uuid.New(), EnrollmentStatus: "ACTIVE"}}
	e.facts.exists[e.s1], e.facts.exists[e.s2] = true, true
	e.svc = application.NewService(application.Deps{Facts: e.facts, Alerts: e.alerts, Audit: e.audit, Tx: fakeTx{e.alerts}, Clock: e.clock, NewID: uuid.New})
	return e
}

// setScore sets the captured score of s1's only unit (nil = pending).
func (e *env) setScore(h *int32) {
	st := e.facts.students[e.group][0]
	st.Subjects = append([]domain.SubjectFacts(nil), st.Subjects...)
	st.Subjects[0].Units = []domain.UnitFact{{UnitID: e.unit.UnitID, Number: 2, Name: "Calculo", Hundredths: h}}
	e.facts.students[e.group][0] = st
}

func is(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// --- tests -----------------------------------------------------------------------------------------------

func TestAuthorizationMatrix(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for name, p := range map[string]iddomain.Principal{"admin": e.admin, "teacher": e.teacher, "student": e.student} {
		if _, err := e.svc.GetStudentAnalytics(ctx, p, e.s1, nil); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: student analytics = %v", name, err)
		}
		if _, err := e.svc.GetGroupAnalytics(ctx, p, e.group); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: group analytics = %v", name, err)
		}
		if _, err := e.svc.Overview(ctx, p, ports.OverviewFilter{}); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: overview = %v", name, err)
		}
		if _, _, err := e.svc.ListAlerts(ctx, p, ports.AlertFilter{}, ports.Page{}); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: alerts = %v", name, err)
		}
		if _, err := e.svc.RecalculateAlerts(ctx, p, e.group, nil); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: recalculate = %v", name, err)
		}
	}
	// A student never recalculates, and a tutor of another group neither reads nor recalculates.
	_, err := e.svc.GetGroupAnalytics(ctx, e.otherTutor, e.group)
	is(t, err, domain.ErrForbidden)
	_, err = e.svc.GetStudentAnalytics(ctx, e.otherTutor, e.s1, nil)
	is(t, err, domain.ErrForbidden)
	g := e.group
	_, err = e.svc.GetStudentAnalytics(ctx, e.otherTutor, e.s1, &g)
	is(t, err, domain.ErrForbidden)
	_, err = e.svc.RecalculateAlerts(ctx, e.otherTutor, e.group, nil)
	is(t, err, domain.ErrForbidden)
	if e.alerts.creates != 0 || len(e.audit.events) != 0 {
		t.Fatal("a refused request must not write anything")
	}
	// Allowed: the tutor of the group and the director.
	for name, p := range map[string]iddomain.Principal{"tutor": e.tutor, "director": e.director} {
		if _, err := e.svc.GetStudentAnalytics(ctx, p, e.s1, nil); err != nil {
			t.Errorf("%s: student analytics: %v", name, err)
		}
		if _, err := e.svc.GetGroupAnalytics(ctx, p, e.group); err != nil {
			t.Errorf("%s: group analytics: %v", name, err)
		}
	}
	// A TEACHER+TUTOR user is authorized by the tutoring relationship, not by the roles added up.
	both := iddomain.Principal{UserID: uuid.New(), Roles: []iddomain.Role{iddomain.RoleTeacher, iddomain.RoleTutor}}
	_, err = e.svc.GetGroupAnalytics(ctx, both, e.group)
	is(t, err, domain.ErrForbidden)
}

func TestNotFound(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.svc.GetGroupAnalytics(ctx, e.tutor, uuid.New())
	is(t, err, domain.ErrGroupNotFound)
	_, err = e.svc.GetStudentAnalytics(ctx, e.tutor, uuid.New(), nil)
	is(t, err, domain.ErrStudentNotFound)
	g := uuid.New()
	_, err = e.svc.GetStudentAnalytics(ctx, e.tutor, e.s1, &g)
	is(t, err, domain.ErrContextNotFound)
	_, err = e.svc.GetAlert(ctx, e.tutor, uuid.New())
	is(t, err, domain.ErrAlertNotFound)
	_, err = e.svc.RecalculateAlerts(ctx, e.tutor, uuid.New(), nil)
	is(t, err, domain.ErrGroupNotFound)
	stranger := uuid.New()
	_, err = e.svc.RecalculateAlerts(ctx, e.tutor, e.group, &stranger)
	is(t, err, domain.ErrContextNotFound)
}

func TestTutorChangeTakesEffectImmediately(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.setScore(h32(7600))
	if _, err := e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil); err != nil {
		t.Fatal(err)
	}
	list, _, _ := e.svc.ListAlerts(ctx, e.tutor, ports.AlertFilter{}, ports.Page{})
	if len(list) != 1 {
		t.Fatalf("alerts = %d", len(list))
	}
	newTutor := e.otherTutor.UserID
	g := e.facts.groups[e.group]
	g.TutorID = &newTutor
	e.facts.groups[e.group], e.alerts.groupOf[e.group] = g, g

	_, err := e.svc.GetAlert(ctx, e.tutor, list[0].ID)
	is(t, err, domain.ErrForbidden)
	if l, total, err := e.svc.ListAlerts(ctx, e.tutor, ports.AlertFilter{}, ports.Page{}); err != nil || total != 0 || len(l) != 0 {
		t.Fatalf("old tutor still lists alerts: %v %d", err, total)
	}
	_, err = e.svc.GetGroupAnalytics(ctx, e.tutor, e.group)
	is(t, err, domain.ErrForbidden)
	_, err = e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil)
	is(t, err, domain.ErrForbidden)
	if _, err := e.svc.GetAlert(ctx, e.otherTutor, list[0].ID); err != nil {
		t.Fatalf("new tutor must read the alert: %v", err)
	}
}

func TestRecalculateIsIdempotentAndFollowsTheData(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// No data: nothing created, nothing audited.
	r, err := e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil)
	if err != nil || r.Created+r.Updated+r.Resolved != 0 || len(e.audit.events) != 0 {
		t.Fatalf("empty recalculation: %+v %v events=%d", r, err, len(e.audit.events))
	}

	e.setScore(h32(7600))
	r, err = e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil)
	if err != nil || r.Created != 1 || len(e.audit.events) != 1 || e.audit.events[0].Action != "analytics.alerts_recalculated" {
		t.Fatalf("first run: %+v %v", r, err)
	}
	first := e.alerts.rows[0]

	// Second run with the same data: no duplicate, no change, no audit event; only the evaluation time moves.
	e.clock.t = e.clock.t.Add(time.Hour)
	r, err = e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil)
	if err != nil || r.Created != 0 || r.Unchanged != 1 || len(e.alerts.rows) != 1 || len(e.audit.events) != 1 {
		t.Fatalf("second run: %+v %v rows=%d events=%d", r, err, len(e.alerts.rows), len(e.audit.events))
	}
	if !e.alerts.rows[0].EvaluatedAt.After(first.EvaluatedAt) || !e.alerts.rows[0].UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatal("evaluated_at must advance and updated_at must not")
	}

	// The teacher corrects NA -> SA: the alert resolves.
	e.setScore(h32(8200))
	r, _ = e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil)
	if r.Resolved != 1 || e.alerts.rows[0].Status != domain.AlertResolved || e.alerts.rows[0].ResolvedAt == nil || len(e.audit.events) != 2 {
		t.Fatalf("after correction: %+v %+v", r, e.alerts.rows[0])
	}
	// The condition returns: a NEW alert opens, the resolved one stays as history.
	e.setScore(h32(5000))
	r, _ = e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil)
	if r.Created != 1 || len(e.alerts.rows) != 2 {
		t.Fatalf("recurrence: %+v rows=%d", r, len(e.alerts.rows))
	}
	// The score changes from 50.00 to 60.00 (still NA): same alert, refreshed.
	id := e.alerts.rows[1].ID
	e.setScore(h32(6000))
	r, _ = e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil)
	if r.Updated != 1 || len(e.alerts.rows) != 2 || e.alerts.rows[1].ID != id || e.alerts.rows[1].Message == "" {
		t.Fatalf("update: %+v", r)
	}
}

func TestAcknowledgedAlertsStayAcknowledged(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.setScore(h32(7600))
	_, _ = e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil)
	e.alerts.rows[0].Status = domain.AlertAcknowledged
	r, _ := e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil)
	if r.Created != 0 || len(e.alerts.rows) != 1 || e.alerts.rows[0].Status != domain.AlertAcknowledged {
		t.Fatalf("acknowledged alert was reopened or duplicated: %+v", r)
	}
}

func TestRecalculateOneStudentLeavesOthersAlone(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.setScore(h32(7600))
	_, _ = e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil)
	e.setScore(h32(9000)) // the NA is gone from the data...
	other := e.s2
	r, err := e.svc.RecalculateAlerts(ctx, e.tutor, e.group, &other)
	if err != nil || r.Resolved != 0 || e.alerts.rows[0].Status == domain.AlertResolved {
		t.Fatalf("recalculating s2 must not touch s1's alerts: %+v %v", r, err)
	}
	s1 := e.s1
	r, _ = e.svc.RecalculateAlerts(ctx, e.tutor, e.group, &s1)
	if r.Resolved != 1 {
		t.Fatalf("recalculating s1 must resolve its alert: %+v", r)
	}
}

func TestRecalculateWithdrawnStudentResolvesItsAlerts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.setScore(h32(7600))
	_, _ = e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil)
	e.facts.students[e.group][0].EnrollmentStatus = "WITHDRAWN"
	r, _ := e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil)
	if r.Resolved != 1 {
		t.Fatalf("a withdrawn student's alerts must resolve: %+v", r)
	}
}

func TestRecalculateFailuresRollBack(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.setScore(h32(7600))
	e.audit.fail = true
	if _, err := e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil); err == nil {
		t.Fatal("recalculation must fail when its audit event cannot be written")
	}
	if len(e.alerts.rows) != 0 {
		t.Fatalf("alerts survived a failed audit: %d", len(e.alerts.rows))
	}
	e.audit.fail = false
	e.facts.fail = errors.New("database unavailable")
	if _, err := e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil); err == nil || len(e.alerts.rows) != 0 {
		t.Fatalf("a failing reader must abort without changes: %v", err)
	}
	e.facts.fail = nil

	// A closed term freezes alerts.
	g := e.facts.groups[e.group]
	g.TermStatus = "closed"
	e.facts.groups[e.group] = g
	_, err := e.svc.RecalculateAlerts(ctx, e.tutor, e.group, nil)
	is(t, err, domain.ErrTermClosed)
	if e.alerts.locks == 0 {
		t.Fatal("evaluations must take the group lock")
	}
}

func TestReadsAreLiveAndDoNotWrite(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.setScore(h32(7600))
	v, err := e.svc.GetStudentAnalytics(ctx, e.tutor, e.s1, nil)
	if err != nil || len(v.Analytics.Signals) != 1 || len(v.Alerts) != 0 {
		t.Fatalf("live signals without persisted alerts: %v %+v", err, v)
	}
	if e.alerts.creates != 0 || len(e.audit.events) != 0 || e.alerts.locks != 0 {
		t.Fatal("reading must never recalculate or write")
	}
	gv, err := e.svc.GetGroupAnalytics(ctx, e.tutor, e.group)
	if err != nil || gv.Students != 2 || gv.Grades.NA != 1 {
		t.Fatalf("group: %v %+v", err, gv)
	}
}

func TestMultipleEnrollmentsDefaultToTheMostRecentAccessible(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tid := e.tutor.UserID
	older := uuid.New()
	g := domain.GroupInfo{ID: older, Code: "1A", TutorID: &tid, TermName: "2025-2", TermStatus: "closed", TermStart: time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC)}
	e.facts.groups[older], e.alerts.groupOf[older] = g, g
	e.facts.students[older] = []domain.StudentFacts{{StudentID: e.s1, EnrollmentStatus: "ACTIVE"}}
	v, err := e.svc.GetStudentAnalytics(ctx, e.tutor, e.s1, nil)
	if err != nil || v.Enrollment.ID != e.group {
		t.Fatalf("default context: %v %+v", err, v)
	}
	if v, err = e.svc.GetStudentAnalytics(ctx, e.tutor, e.s1, &older); err != nil || v.Enrollment.ID != older {
		t.Fatalf("explicit context: %v", err)
	}
}

func TestListAlertsValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, f := range []ports.AlertFilter{{Status: "bogus"}, {Rule: "RISK_SCORE"}} {
		if _, _, err := e.svc.ListAlerts(ctx, e.tutor, f, ports.Page{}); !errors.Is(err, domain.ErrInvalidFilter) {
			t.Errorf("%+v accepted: %v", f, err)
		}
	}
	for _, p := range []ports.Page{{Limit: 201}, {Limit: -1}, {Limit: 5, Offset: -1}} {
		if _, _, err := e.svc.ListAlerts(ctx, e.tutor, ports.AlertFilter{}, p); !errors.Is(err, domain.ErrInvalidPaging) {
			t.Errorf("%+v accepted: %v", p, err)
		}
	}
}

func TestOverviewIsScopedToTheCaller(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if l, err := e.svc.Overview(ctx, e.director, ports.OverviewFilter{}); err != nil || len(l) != 2 {
		t.Fatalf("director: %v %d", err, len(l))
	}
	if l, err := e.svc.Overview(ctx, e.tutor, ports.OverviewFilter{}); err != nil || len(l) != 1 || l[0].Group.ID != e.group {
		t.Fatalf("tutor: %v %d", err, len(l))
	}
}
