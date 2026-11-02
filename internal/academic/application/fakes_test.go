package application

import (
	"context"
	"testing"
	"time"

	"github.com/edutrak/backend/internal/academic/domain"
	"github.com/edutrak/backend/internal/academic/ports"
	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

// store is an in-memory implementation of every academic port. Listing methods
// that depend on SQL scoping just record the scope they were called with; the
// scoping itself is covered by the PostgreSQL integration tests.
type store struct {
	careers     map[uuid.UUID]*domain.Career
	subjects    map[uuid.UUID]*domain.Subject
	curriculum  map[[3]any]bool
	terms       map[uuid.UUID]*domain.Term
	groups      map[uuid.UUID]*domain.Group
	enrollments []*domain.Enrollment
	assignments map[uuid.UUID]*domain.TeachingAssignment
	schedules   []*domain.Schedule
	subjectEnr  []*domain.SubjectEnrollment
	users       map[uuid.UUID]*domain.UserRef
	events      []auditdomain.Event

	lastScope  ports.Scope
	lastSEFilt ports.SubjectEnrollmentFilter
	statusRace bool // makes UpdateStatus report a lost race
}

func newStore() *store {
	return &store{
		careers: map[uuid.UUID]*domain.Career{}, subjects: map[uuid.UUID]*domain.Subject{},
		curriculum: map[[3]any]bool{}, terms: map[uuid.UUID]*domain.Term{}, groups: map[uuid.UUID]*domain.Group{},
		assignments: map[uuid.UUID]*domain.TeachingAssignment{}, users: map[uuid.UUID]*domain.UserRef{},
	}
}

type (
	fCatalog     struct{ *store }
	fTerms       struct{ *store }
	fGroups      struct{ *store }
	fAssignments struct{ *store }
	fSubjectEnr  struct{ *store }
	fUsers       struct{ *store }
	fAudit       struct{ *store }
	fTx          struct{}
)

func (fTx) WithinTx(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

func (f fAudit) Record(_ context.Context, e auditdomain.Event) error {
	f.events = append(f.events, e)
	return nil
}

// --- catalog ---

func (f fCatalog) ListCareers(context.Context) ([]domain.Career, error) { return nil, nil }
func (f fCatalog) GetCareer(_ context.Context, id uuid.UUID) (*domain.Career, error) {
	if c, ok := f.careers[id]; ok {
		return c, nil
	}
	return nil, domain.ErrCareerNotFound
}
func (f fCatalog) CreateCareer(_ context.Context, c *domain.Career) error {
	for _, o := range f.careers {
		if o.Code == c.Code {
			return domain.ErrCareerCodeTaken
		}
	}
	f.careers[c.ID] = c
	return nil
}
func (f fCatalog) ListSubjects(context.Context) ([]domain.Subject, error) { return nil, nil }
func (f fCatalog) GetSubject(_ context.Context, id uuid.UUID) (*domain.Subject, error) {
	if s, ok := f.subjects[id]; ok {
		return s, nil
	}
	return nil, domain.ErrSubjectNotFound
}
func (f fCatalog) CreateSubject(_ context.Context, s *domain.Subject) error {
	f.subjects[s.ID] = s
	return nil
}
func (f fCatalog) ListUnits(context.Context, uuid.UUID) ([]domain.SubjectUnit, error) {
	return nil, nil
}
func (f fCatalog) CreateUnit(context.Context, *domain.SubjectUnit) error { return nil }
func (f fCatalog) ListCurriculum(context.Context, uuid.UUID, *int16) ([]domain.CurriculumEntry, error) {
	return nil, nil
}
func (f fCatalog) AddCurriculum(_ context.Context, e domain.CurriculumEntry) error {
	k := [3]any{e.CareerID, e.SubjectID, e.Level}
	if f.curriculum[k] {
		return domain.ErrCurriculumExists
	}
	f.curriculum[k] = true
	return nil
}
func (f fCatalog) InCurriculum(_ context.Context, c, s uuid.UUID, l int16) (bool, error) {
	return f.curriculum[[3]any{c, s, l}], nil
}

// --- terms ---

func (f fTerms) Create(_ context.Context, t *domain.Term) error { f.terms[t.ID] = t; return nil }
func (f fTerms) List(context.Context) ([]domain.Term, error)    { return nil, nil }
func (f fTerms) Get(_ context.Context, id uuid.UUID) (*domain.Term, error) {
	if t, ok := f.terms[id]; ok {
		c := *t // callers get a copy, like a database row
		return &c, nil
	}
	return nil, domain.ErrTermNotFound
}
func (f fTerms) GetForShare(ctx context.Context, id uuid.UUID) (*domain.Term, error) {
	return f.Get(ctx, id)
}
func (f fTerms) UpdateStatus(_ context.Context, id uuid.UUID, from, to domain.TermStatus) (bool, error) {
	t := f.terms[id]
	if f.statusRace || t.Status != from {
		return false, nil
	}
	t.Status = to
	return true, nil
}

// --- groups ---

func (f fGroups) Create(_ context.Context, g *domain.Group) error {
	for _, o := range f.groups {
		if o.TermID == g.TermID && o.Code == g.Code {
			return domain.ErrGroupCodeTaken
		}
	}
	f.groups[g.ID] = g
	return nil
}
func (f fGroups) Get(_ context.Context, id uuid.UUID) (*domain.Group, error) {
	if g, ok := f.groups[id]; ok {
		c := *g
		return &c, nil
	}
	return nil, domain.ErrGroupNotFound
}
func (f fGroups) List(_ context.Context, s ports.Scope, _ ports.GroupFilter) ([]domain.Group, error) {
	f.lastScope = s
	return nil, nil
}
func (f fGroups) SetTutor(_ context.Context, id uuid.UUID, tutor *uuid.UUID) error {
	f.groups[id].TutorID = tutor
	return nil
}
func (f fGroups) AddEnrollment(_ context.Context, e *domain.Enrollment) error {
	for _, o := range f.enrollments {
		if o.GroupID == e.GroupID && o.StudentID == e.StudentID {
			return domain.ErrAlreadyEnrolled
		}
	}
	f.enrollments = append(f.enrollments, e)
	return nil
}
func (f fGroups) ListEnrollments(_ context.Context, groupID uuid.UUID) ([]domain.Enrollment, error) {
	var out []domain.Enrollment
	for _, e := range f.enrollments {
		if e.GroupID == groupID {
			out = append(out, *e)
		}
	}
	return out, nil
}
func (f fGroups) IsEnrolled(_ context.Context, groupID, studentID uuid.UUID) (bool, error) {
	for _, e := range f.enrollments {
		if e.GroupID == groupID && e.StudentID == studentID && e.Status == domain.EnrollmentActive {
			return true, nil
		}
	}
	return false, nil
}
func (f fGroups) TeachesInGroup(_ context.Context, groupID, teacherID uuid.UUID) (bool, error) {
	for _, a := range f.assignments {
		if a.GroupID == groupID && a.TeacherID == teacherID {
			return true, nil
		}
	}
	return false, nil
}

// --- assignments ---

func (f fAssignments) Create(_ context.Context, a *domain.TeachingAssignment) error {
	for _, o := range f.assignments {
		if o.GroupID == a.GroupID && o.SubjectID == a.SubjectID {
			return domain.ErrAssignmentExists
		}
	}
	g := f.groups[a.GroupID]
	c := *a
	c.TermID, c.GroupTutorID = g.TermID, g.TutorID
	f.assignments[a.ID] = &c
	return nil
}
func (f fAssignments) Get(_ context.Context, id uuid.UUID) (*domain.TeachingAssignment, error) {
	if a, ok := f.assignments[id]; ok {
		c := *a
		c.GroupTutorID = f.groups[a.GroupID].TutorID
		return &c, nil
	}
	return nil, domain.ErrAssignmentNotFound
}
func (f fAssignments) GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.TeachingAssignment, error) {
	return f.Get(ctx, id)
}
func (f fAssignments) List(_ context.Context, s ports.Scope, _ ports.AssignmentFilter) ([]domain.TeachingAssignment, error) {
	f.lastScope = s
	return nil, nil
}
func (f fAssignments) SetTeacher(_ context.Context, id, teacher uuid.UUID) error {
	f.assignments[id].TeacherID = teacher
	return nil
}
func (f fAssignments) HasSubjectEnrollment(_ context.Context, assignmentID, studentID uuid.UUID) (bool, error) {
	for _, e := range f.subjectEnr {
		if e.TeachingAssignmentID == assignmentID && e.StudentID == studentID && e.Status == domain.SubjectEnrollmentActive {
			return true, nil
		}
	}
	return false, nil
}
func (f fAssignments) CreateSchedule(_ context.Context, s *domain.Schedule) error {
	f.schedules = append(f.schedules, s)
	return nil
}
func (f fAssignments) ListSchedules(_ context.Context, id uuid.UUID) ([]domain.Schedule, error) {
	var out []domain.Schedule
	for _, s := range f.schedules {
		if s.TeachingAssignmentID == id {
			out = append(out, *s)
		}
	}
	return out, nil
}
func (f fAssignments) ListScheduleEntries(_ context.Context, s ports.Scope, _ ports.AssignmentFilter) ([]domain.ScheduleEntry, error) {
	f.lastScope = s
	return nil, nil
}

// --- subject enrollments ---

func (f fSubjectEnr) Create(_ context.Context, e *domain.SubjectEnrollment) error {
	f.subjectEnr = append(f.subjectEnr, e)
	return nil
}
func (f fSubjectEnr) NextAttempt(_ context.Context, studentID, subjectID uuid.UUID) (int16, error) {
	var max int16
	for _, e := range f.subjectEnr {
		if e.StudentID == studentID && f.assignments[e.TeachingAssignmentID].SubjectID == subjectID && e.AttemptNumber > max {
			max = e.AttemptNumber
		}
	}
	return max + 1, nil
}
func (f fSubjectEnr) ExistsForAssignment(_ context.Context, studentID, assignmentID uuid.UUID) (bool, error) {
	for _, e := range f.subjectEnr {
		if e.StudentID == studentID && e.TeachingAssignmentID == assignmentID {
			return true, nil
		}
	}
	return false, nil
}
func (f fSubjectEnr) EnrollGroup(context.Context, uuid.UUID) (int, error) { return 3, nil }
func (f fSubjectEnr) ListByAssignment(_ context.Context, id uuid.UUID) ([]domain.SubjectEnrollment, error) {
	var out []domain.SubjectEnrollment
	for _, e := range f.subjectEnr {
		if e.TeachingAssignmentID == id {
			out = append(out, *e)
		}
	}
	return out, nil
}
func (f fSubjectEnr) List(_ context.Context, s ports.Scope, flt ports.SubjectEnrollmentFilter) ([]domain.SubjectEnrollment, error) {
	f.lastScope, f.lastSEFilt = s, flt
	return nil, nil
}

// --- users ---

func (f fUsers) Find(_ context.Context, id uuid.UUID) (*domain.UserRef, error) {
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return nil, domain.ErrUserNotFound
}

// --- test environment ---

type env struct {
	t   *testing.T
	st  *store
	svc *Service

	director, teacher, otherTeacher, tutor, otherTutor, student, otherStudent, admin iddomain.Principal

	term    *domain.Term
	career  *domain.Career
	subject *domain.Subject
	group   *domain.Group
}

func principal(id uuid.UUID, roles ...iddomain.Role) iddomain.Principal {
	return iddomain.Principal{UserID: id, SessionID: uuid.New(), Roles: roles}
}

// newEnv builds a service over in-memory ports with fictitious users, one
// active term, one career with one subject at level 2 and one group at level 2.
func newEnv(t *testing.T) *env {
	t.Helper()
	st := newStore()
	e := &env{t: t, st: st}
	add := func(role iddomain.Role) iddomain.Principal {
		id := uuid.New()
		st.users[id] = &domain.UserRef{ID: id, IsActive: true, Roles: []string{string(role)}}
		return principal(id, role)
	}
	e.director, e.teacher, e.otherTeacher = add(iddomain.RoleDirector), add(iddomain.RoleTeacher), add(iddomain.RoleTeacher)
	e.tutor, e.otherTutor = add(iddomain.RoleTutor), add(iddomain.RoleTutor)
	e.student, e.otherStudent, e.admin = add(iddomain.RoleStudent), add(iddomain.RoleStudent), add(iddomain.RoleAdmin)

	e.svc = NewService(Deps{
		Catalog: fCatalog{st}, Terms: fTerms{st}, Groups: fGroups{st}, Assignments: fAssignments{st},
		SubjectEnrollments: fSubjectEnr{st}, Users: fUsers{st}, Audit: fAudit{st}, Tx: fTx{}, NewID: uuid.New,
	})

	e.term = &domain.Term{ID: uuid.New(), Name: "2026-1", Status: domain.TermActive,
		StartDate: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)}
	st.terms[e.term.ID] = e.term
	e.career = &domain.Career{ID: uuid.New(), Code: "TIC", Name: "Tecnologias", IsActive: true}
	st.careers[e.career.ID] = e.career
	e.subject = &domain.Subject{ID: uuid.New(), Code: "MAT1", Name: "Matematicas", IsActive: true}
	st.subjects[e.subject.ID] = e.subject
	st.curriculum[[3]any{e.career.ID, e.subject.ID, int16(2)}] = true
	return e
}

// newGroup creates the standard group (level 2) through the service.
func (e *env) newGroup(tutor *iddomain.Principal) *domain.Group {
	e.t.Helper()
	in := CreateGroupInput{TermID: e.term.ID, CareerID: e.career.ID, Code: "2A", Level: 2}
	if tutor != nil {
		in.TutorID = &tutor.UserID
	}
	g, err := e.svc.CreateGroup(context.Background(), e.director, in)
	if err != nil {
		e.t.Fatalf("create group: %v", err)
	}
	e.group = g
	return g
}

// groupWithAssignment returns a group (tutor: e.tutor) taught by e.teacher.
func (e *env) groupWithAssignment() (*domain.Group, *domain.TeachingAssignment) {
	e.t.Helper()
	g := e.newGroup(&e.tutor)
	a, err := e.svc.CreateAssignment(context.Background(), e.director, g.ID, CreateAssignmentInput{SubjectID: e.subject.ID, TeacherID: e.teacher.UserID})
	if err != nil {
		e.t.Fatalf("create assignment: %v", err)
	}
	return g, a
}
