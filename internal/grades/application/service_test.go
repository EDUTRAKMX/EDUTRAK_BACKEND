package application

import (
	"context"
	"errors"
	"testing"
	"time"

	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	"github.com/edutrak/backend/internal/grades/domain"
	"github.com/edutrak/backend/internal/grades/ports"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

var ctx = context.Background()

// --- fakes ---

type fakeRepo struct {
	grades      map[uuid.UUID]*domain.Grade
	lastScope   ports.ViewScope
	listCalls   int
	failCreate  error
	updateCalls int
}

func (f *fakeRepo) Create(_ context.Context, g *domain.Grade) error {
	if f.failCreate != nil {
		return f.failCreate
	}
	for _, o := range f.grades {
		if o.SubjectEnrollmentID == g.SubjectEnrollmentID && o.SubjectUnitID == g.SubjectUnitID {
			return domain.ErrGradeExists
		}
	}
	g.RecordedAt, g.UpdatedAt = time.Now(), time.Now()
	c := *g
	f.grades[g.ID] = &c
	return nil
}
func (f *fakeRepo) Get(_ context.Context, id uuid.UUID) (*domain.Grade, error) {
	if g, ok := f.grades[id]; ok {
		c := *g
		return &c, nil
	}
	return nil, domain.ErrGradeNotFound
}
func (f *fakeRepo) GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.Grade, error) {
	return f.Get(ctx, id)
}
func (f *fakeRepo) UpdateScore(_ context.Context, id uuid.UUID, s domain.Score) (*domain.Grade, error) {
	f.updateCalls++
	g := f.grades[id]
	g.Score, g.UpdatedAt = s, time.Now()
	c := *g
	return &c, nil
}
func (f *fakeRepo) ListStudentSheets(_ context.Context, _ uuid.UUID, scope ports.ViewScope) ([]domain.StudentSheet, error) {
	f.lastScope = scope
	f.listCalls++
	return nil, nil
}
func (f *fakeRepo) AssignmentSheet(context.Context, uuid.UUID) (*domain.AssignmentSheet, error) {
	f.listCalls++
	return &domain.AssignmentSheet{}, nil
}

type fakeAcademic struct {
	refs     map[uuid.UUID]*domain.SubjectEnrollmentRef
	units    map[uuid.UUID]*domain.Unit
	owners   map[uuid.UUID]*ports.AssignmentOwner
	students map[uuid.UUID]bool
	visible  bool
}

func (f *fakeAcademic) EnrollmentRef(_ context.Context, id uuid.UUID, _ bool) (*domain.SubjectEnrollmentRef, error) {
	if r, ok := f.refs[id]; ok {
		c := *r
		return &c, nil
	}
	return nil, domain.ErrEnrollmentNotFound
}
func (f *fakeAcademic) Unit(_ context.Context, id uuid.UUID) (*domain.Unit, error) {
	if u, ok := f.units[id]; ok {
		return u, nil
	}
	return nil, domain.ErrUnitNotFound
}
func (f *fakeAcademic) AssignmentOwner(_ context.Context, id uuid.UUID) (*ports.AssignmentOwner, error) {
	if o, ok := f.owners[id]; ok {
		return o, nil
	}
	return nil, domain.ErrAssignmentNotFound
}
func (f *fakeAcademic) StudentVisible(context.Context, uuid.UUID, ports.ViewScope) (bool, error) {
	return f.visible, nil
}
func (f *fakeAcademic) StudentExists(_ context.Context, id uuid.UUID) (bool, error) {
	return f.students[id], nil
}

type fakeAudit struct {
	events []auditdomain.Event
	fail   error
}

func (f *fakeAudit) Record(_ context.Context, e auditdomain.Event) error {
	if f.fail != nil {
		return f.fail
	}
	f.events = append(f.events, e)
	return nil
}

type fakeTx struct{ repo *fakeRepo }

// WithinTx emulates rollback by restoring the grades map when fn fails.
func (t fakeTx) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	snapshot := map[uuid.UUID]domain.Grade{}
	for id, g := range t.repo.grades {
		snapshot[id] = *g
	}
	if err := fn(ctx); err != nil {
		t.repo.grades = map[uuid.UUID]*domain.Grade{}
		for id, g := range snapshot {
			c := g
			t.repo.grades[id] = &c
		}
		return err
	}
	return nil
}

type env struct {
	svc                                   *Service
	repo                                  *fakeRepo
	acad                                  *fakeAcademic
	audit                                 *fakeAudit
	teacher, otherTeacher, tutor, student iddomain.Principal
	otherStudent, director, admin         iddomain.Principal
	enrollment, unit, otherSubjectUnit    uuid.UUID
	assignment                            uuid.UUID
}

func principal(roles ...iddomain.Role) iddomain.Principal {
	return iddomain.Principal{UserID: uuid.New(), SessionID: uuid.New(), Roles: roles}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		repo:  &fakeRepo{grades: map[uuid.UUID]*domain.Grade{}},
		audit: &fakeAudit{},
		acad: &fakeAcademic{refs: map[uuid.UUID]*domain.SubjectEnrollmentRef{}, units: map[uuid.UUID]*domain.Unit{},
			owners: map[uuid.UUID]*ports.AssignmentOwner{}, students: map[uuid.UUID]bool{}},
		teacher: principal(iddomain.RoleTeacher), otherTeacher: principal(iddomain.RoleTeacher), tutor: principal(iddomain.RoleTutor),
		student: principal(iddomain.RoleStudent), otherStudent: principal(iddomain.RoleStudent),
		director: principal(iddomain.RoleDirector), admin: principal(iddomain.RoleAdmin),
		enrollment: uuid.New(), unit: uuid.New(), otherSubjectUnit: uuid.New(), assignment: uuid.New(),
	}
	subject := uuid.New()
	e.acad.refs[e.enrollment] = &domain.SubjectEnrollmentRef{
		ID: e.enrollment, StudentID: e.student.UserID, AssignmentID: e.assignment, TeacherID: e.teacher.UserID,
		GroupID: uuid.New(), GroupTutorID: &e.tutor.UserID, SubjectID: subject, TermID: uuid.New(),
		TermStatus: "active", Status: "ACTIVE", AttemptNumber: 1,
	}
	e.acad.units[e.unit] = &domain.Unit{ID: e.unit, SubjectID: subject, Number: 1, Name: "Unidad 1"}
	e.acad.units[e.otherSubjectUnit] = &domain.Unit{ID: e.otherSubjectUnit, SubjectID: uuid.New(), Number: 1, Name: "Otra materia"}
	e.acad.owners[e.assignment] = &ports.AssignmentOwner{TeacherID: e.teacher.UserID, GroupTutorID: &e.tutor.UserID}
	e.acad.students[e.student.UserID], e.acad.students[e.otherStudent.UserID] = true, true
	e.svc = NewService(Deps{Grades: e.repo, Academic: e.acad, Audit: e.audit, Tx: fakeTx{e.repo}, NewID: uuid.New})
	return e
}

func (e *env) capture(p iddomain.Principal, score string) (*domain.Grade, error) {
	return e.svc.CaptureGrade(ctx, p, CaptureGradeInput{SubjectEnrollmentID: e.enrollment, SubjectUnitID: e.unit, Score: score})
}

func wantErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("err = %v, want %v", got, want)
	}
}

// --- capture ---

func TestCaptureGradeByOwningTeacher(t *testing.T) {
	e := newEnv(t)
	g, err := e.capture(e.teacher, "89.99")
	if err != nil {
		t.Fatal(err)
	}
	if g.Score.String() != "89.99" || g.Score.Category() != domain.CategorySA || g.RecordedBy != e.teacher.UserID {
		t.Fatalf("grade = %+v", g)
	}
	if len(e.audit.events) != 1 {
		t.Fatalf("audit events = %d", len(e.audit.events))
	}
	ev := e.audit.events[0]
	if ev.Action != "grades.created" || ev.EntityType != "grade" || ev.EntityID != g.ID.String() || *ev.ActorUserID != e.teacher.UserID {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Metadata["score"] != "89.99" || ev.Metadata["category"] != "SA" {
		t.Fatalf("metadata = %v", ev.Metadata)
	}
}

func TestCaptureAcceptsZeroAndHundred(t *testing.T) {
	e := newEnv(t)
	g, err := e.capture(e.teacher, "0.00")
	if err != nil || g.Score.String() != "0.00" || g.Score.Category() != domain.CategoryNA {
		t.Fatalf("zero: %v %+v", err, g)
	}
	u2 := uuid.New()
	e.acad.units[u2] = &domain.Unit{ID: u2, SubjectID: e.acad.refs[e.enrollment].SubjectID, Number: 2}
	g, err = e.svc.CaptureGrade(ctx, e.teacher, CaptureGradeInput{SubjectEnrollmentID: e.enrollment, SubjectUnitID: u2, Score: "100"})
	if err != nil || g.Score.Category() != domain.CategoryAU {
		t.Fatalf("hundred: %v %+v", err, g)
	}
}

func TestCaptureRejectsInvalidScoresBeforeTouchingAnything(t *testing.T) {
	e := newEnv(t)
	for _, s := range []string{"-0.01", "100.01", "89.999", "", "abc", "1e2"} {
		if _, err := e.capture(e.teacher, s); err == nil {
			t.Errorf("score %q must be rejected", s)
		}
	}
	if len(e.repo.grades) != 0 || len(e.audit.events) != 0 {
		t.Fatal("nothing may be stored or audited for invalid scores")
	}
}

func TestCaptureAuthorization(t *testing.T) {
	e := newEnv(t)
	for name, p := range map[string]iddomain.Principal{
		"other teacher": e.otherTeacher, "tutor": e.tutor, "student": e.student, "director": e.director, "admin": e.admin,
	} {
		if _, err := e.capture(p, "90"); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: err = %v, want forbidden", name, err)
		}
	}
	if len(e.repo.grades) != 0 {
		t.Fatal("unauthorized attempts stored a grade")
	}
}

func TestCaptureValidatesRelations(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.CaptureGrade(ctx, e.teacher, CaptureGradeInput{SubjectEnrollmentID: uuid.New(), SubjectUnitID: e.unit, Score: "90"})
	wantErr(t, err, domain.ErrEnrollmentNotFound)
	_, err = e.svc.CaptureGrade(ctx, e.teacher, CaptureGradeInput{SubjectEnrollmentID: e.enrollment, SubjectUnitID: uuid.New(), Score: "90"})
	wantErr(t, err, domain.ErrUnitNotFound)
	_, err = e.svc.CaptureGrade(ctx, e.teacher, CaptureGradeInput{SubjectEnrollmentID: e.enrollment, SubjectUnitID: e.otherSubjectUnit, Score: "90"})
	wantErr(t, err, domain.ErrUnitNotInSubject)
}

func TestCaptureRespectsTermAndEnrollmentState(t *testing.T) {
	e := newEnv(t)
	e.acad.refs[e.enrollment].TermStatus = "closed"
	_, err := e.capture(e.teacher, "90")
	wantErr(t, err, domain.ErrTermClosed)
	e.acad.refs[e.enrollment].TermStatus = "active"
	e.acad.refs[e.enrollment].Status = "WITHDRAWN"
	_, err = e.capture(e.teacher, "90")
	wantErr(t, err, domain.ErrEnrollmentNotActive)
	if len(e.repo.grades) != 0 {
		t.Fatal("no grade may be stored")
	}
}

func TestDuplicateCaptureNeverOverwrites(t *testing.T) {
	e := newEnv(t)
	first, err := e.capture(e.teacher, "70")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.capture(e.teacher, "95")
	wantErr(t, err, domain.ErrGradeExists)
	if got := e.repo.grades[first.ID].Score.String(); got != "70.00" {
		t.Fatalf("score after duplicate POST = %s, must stay 70.00", got)
	}
	if len(e.audit.events) != 1 {
		t.Fatalf("audit events = %d, the rejected duplicate must not be audited", len(e.audit.events))
	}
}

func TestGradesArePerAttempt(t *testing.T) {
	e := newEnv(t)
	if _, err := e.capture(e.teacher, "50"); err != nil {
		t.Fatal(err)
	}
	// A second attempt of the same student is another subject enrollment (another assignment).
	second := uuid.New()
	ref := *e.acad.refs[e.enrollment]
	ref.ID, ref.AttemptNumber = second, 2
	e.acad.refs[second] = &ref
	g, err := e.svc.CaptureGrade(ctx, e.teacher, CaptureGradeInput{SubjectEnrollmentID: second, SubjectUnitID: e.unit, Score: "85"})
	if err != nil {
		t.Fatalf("same unit on a new attempt must be allowed: %v", err)
	}
	if g.SubjectEnrollmentID != second || len(e.repo.grades) != 2 {
		t.Fatalf("grades = %d", len(e.repo.grades))
	}
}

func TestCaptureRollsBackWhenAuditFails(t *testing.T) {
	e := newEnv(t)
	e.audit.fail = errors.New("audit down")
	if _, err := e.capture(e.teacher, "90"); err == nil {
		t.Fatal("capture must fail when its audit cannot be written")
	}
	if len(e.repo.grades) != 0 {
		t.Fatal("the grade must be rolled back with the audit failure")
	}
}

// --- update ---

func TestUpdateGradeByOwningTeacher(t *testing.T) {
	e := newEnv(t)
	g, _ := e.capture(e.teacher, "79.99")
	u, err := e.svc.UpdateGrade(ctx, e.teacher, g.ID, "80.00")
	if err != nil {
		t.Fatal(err)
	}
	if u.Score.String() != "80.00" || u.Score.Category() != domain.CategorySA {
		t.Fatalf("updated = %+v", u)
	}
	if len(e.audit.events) != 2 || e.audit.events[1].Action != "grades.updated" {
		t.Fatalf("events = %+v", e.audit.events)
	}
	m := e.audit.events[1].Metadata
	if m["from"] != "79.99" || m["to"] != "80.00" || m["from_category"] != "NA" || m["to_category"] != "SA" {
		t.Fatalf("metadata = %v", m)
	}
}

func TestUpdateWithSameScoreIsANoOp(t *testing.T) {
	e := newEnv(t)
	g, _ := e.capture(e.teacher, "90")
	if _, err := e.svc.UpdateGrade(ctx, e.teacher, g.ID, "90.00"); err != nil {
		t.Fatal(err)
	}
	if e.repo.updateCalls != 0 || len(e.audit.events) != 1 {
		t.Fatalf("updates=%d events=%d, want no write and no extra audit", e.repo.updateCalls, len(e.audit.events))
	}
}

func TestUpdateAuthorizationAndState(t *testing.T) {
	e := newEnv(t)
	g, _ := e.capture(e.teacher, "60")
	for name, p := range map[string]iddomain.Principal{
		"other teacher": e.otherTeacher, "tutor": e.tutor, "student": e.student, "director": e.director, "admin": e.admin,
	} {
		if _, err := e.svc.UpdateGrade(ctx, p, g.ID, "100"); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: err = %v, want forbidden", name, err)
		}
	}
	if e.repo.grades[g.ID].Score.String() != "60.00" {
		t.Fatal("foreign update changed the score")
	}
	_, err := e.svc.UpdateGrade(ctx, e.teacher, uuid.New(), "90")
	wantErr(t, err, domain.ErrGradeNotFound)
	_, err = e.svc.UpdateGrade(ctx, e.teacher, g.ID, "100.01")
	wantErr(t, err, domain.ErrScoreRange)
	_, err = e.svc.UpdateGrade(ctx, e.teacher, g.ID, "70.555")
	wantErr(t, err, domain.ErrScorePrecision)
	e.acad.refs[e.enrollment].TermStatus = "closed"
	_, err = e.svc.UpdateGrade(ctx, e.teacher, g.ID, "70")
	wantErr(t, err, domain.ErrTermClosed)
	if e.repo.grades[g.ID].Score.String() != "60.00" {
		t.Fatal("closed term update changed the score")
	}
}

func TestReassignedTeacherLosesWriteAccess(t *testing.T) {
	e := newEnv(t)
	g, _ := e.capture(e.teacher, "60")
	e.acad.refs[e.enrollment].TeacherID = e.otherTeacher.UserID // Academic swapped the teacher
	_, err := e.svc.UpdateGrade(ctx, e.teacher, g.ID, "70")
	wantErr(t, err, domain.ErrForbidden)
	if _, err := e.svc.UpdateGrade(ctx, e.otherTeacher, g.ID, "70"); err != nil {
		t.Fatalf("new teacher must be able to correct: %v", err)
	}
}

func TestUpdateRollsBackWhenAuditFails(t *testing.T) {
	e := newEnv(t)
	g, _ := e.capture(e.teacher, "60")
	e.audit.fail = errors.New("audit down")
	if _, err := e.svc.UpdateGrade(ctx, e.teacher, g.ID, "99"); err == nil {
		t.Fatal("update must fail when its audit cannot be written")
	}
	if e.repo.grades[g.ID].Score.String() != "60.00" {
		t.Fatalf("score = %s, rollback expected", e.repo.grades[g.ID].Score)
	}
}

// --- reads ---

func TestGetGradeAuthorizationByRelationship(t *testing.T) {
	e := newEnv(t)
	g, _ := e.capture(e.teacher, "88")
	allowed := map[string]iddomain.Principal{"teacher": e.teacher, "tutor": e.tutor, "student": e.student, "director": e.director}
	for name, p := range allowed {
		if _, err := e.svc.GetGrade(ctx, p, g.ID); err != nil {
			t.Errorf("%s should read the grade: %v", name, err)
		}
	}
	for name, p := range map[string]iddomain.Principal{
		"other teacher": e.otherTeacher, "other student": e.otherStudent, "other tutor": principal(iddomain.RoleTutor), "admin": e.admin,
	} {
		if _, err := e.svc.GetGrade(ctx, p, g.ID); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: err = %v, want forbidden", name, err)
		}
	}
	_, err := e.svc.GetGrade(ctx, e.director, uuid.New())
	wantErr(t, err, domain.ErrGradeNotFound)
}

func TestListStudentGradesScopes(t *testing.T) {
	e := newEnv(t)
	target := e.student.UserID

	if _, err := e.svc.ListStudentGrades(ctx, e.student, target); err != nil || !e.repo.lastScope.All {
		t.Fatalf("student reading self: %v scope=%+v", err, e.repo.lastScope)
	}
	if _, err := e.svc.ListStudentGrades(ctx, e.otherStudent, target); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("student reading another student: %v", err)
	}
	if _, err := e.svc.ListStudentGrades(ctx, e.admin, target); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("admin: %v", err)
	}
	if _, err := e.svc.ListStudentGrades(ctx, e.director, target); err != nil || !e.repo.lastScope.All {
		t.Fatalf("director: %v scope=%+v", err, e.repo.lastScope)
	}
	_, err := e.svc.ListStudentGrades(ctx, e.director, uuid.New())
	wantErr(t, err, domain.ErrStudentNotFound)

	e.acad.visible = true
	if _, err := e.svc.ListStudentGrades(ctx, e.teacher, target); err != nil {
		t.Fatal(err)
	}
	if e.repo.lastScope.All || e.repo.lastScope.TeacherID == nil || *e.repo.lastScope.TeacherID != e.teacher.UserID || e.repo.lastScope.TutorID != nil {
		t.Fatalf("teacher scope = %+v, must be limited to their assignments", e.repo.lastScope)
	}
	if _, err := e.svc.ListStudentGrades(ctx, e.tutor, target); err != nil {
		t.Fatal(err)
	}
	if e.repo.lastScope.All || e.repo.lastScope.TutorID == nil || e.repo.lastScope.TeacherID != nil {
		t.Fatalf("tutor scope = %+v, must be limited to tutored groups", e.repo.lastScope)
	}
	e.acad.visible = false // teacher/tutor without relation to this student
	calls := e.repo.listCalls
	for name, p := range map[string]iddomain.Principal{"teacher": e.otherTeacher, "tutor": principal(iddomain.RoleTutor)} {
		if _, err := e.svc.ListStudentGrades(ctx, p, target); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s without relation: %v", name, err)
		}
	}
	if e.repo.listCalls != calls {
		t.Fatal("forbidden callers must not reach the grade query")
	}
}

func TestMultiRoleUserGetsUnionOfScopesButNeverAll(t *testing.T) {
	e := newEnv(t)
	e.acad.visible = true
	both := iddomain.Principal{UserID: uuid.New(), Roles: []iddomain.Role{iddomain.RoleTeacher, iddomain.RoleTutor}}
	if _, err := e.svc.ListStudentGrades(ctx, both, e.student.UserID); err != nil {
		t.Fatal(err)
	}
	s := e.repo.lastScope
	if s.All || s.TeacherID == nil || s.TutorID == nil {
		t.Fatalf("scope = %+v", s)
	}
}

func TestGetAssignmentGradesAuthorization(t *testing.T) {
	e := newEnv(t)
	for name, p := range map[string]iddomain.Principal{"teacher": e.teacher, "tutor": e.tutor, "director": e.director} {
		if _, err := e.svc.GetAssignmentGrades(ctx, p, e.assignment); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, p := range map[string]iddomain.Principal{"other teacher": e.otherTeacher, "student": e.student, "admin": e.admin, "other tutor": principal(iddomain.RoleTutor)} {
		if _, err := e.svc.GetAssignmentGrades(ctx, p, e.assignment); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: err = %v, want forbidden", name, err)
		}
	}
	_, err := e.svc.GetAssignmentGrades(ctx, e.director, uuid.New())
	wantErr(t, err, domain.ErrAssignmentNotFound)
}
