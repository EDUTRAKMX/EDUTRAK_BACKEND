package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/edutrak/backend/internal/academic/domain"
	"github.com/edutrak/backend/internal/academic/ports"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/apperr"
	"github.com/google/uuid"
)

var ctx = context.Background()

// A "not found" for an entity referenced from a request body is reported as a
// 422 validation error (the URL was right, the payload was not).
var (
	refTerm       = apperr.New(apperr.KindValidation, "term_not_found", "")
	refCareer     = apperr.New(apperr.KindValidation, "career_not_found", "")
	refSubject    = apperr.New(apperr.KindValidation, "subject_not_found", "")
	refAssignment = apperr.New(apperr.KindValidation, "teaching_assignment_not_found", "")
)

func wantErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("err = %v, want %v", got, want)
	}
}

func day(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}

func actions(st *store) []string {
	out := make([]string, len(st.events))
	for i, e := range st.events {
		out[i] = e.Action
	}
	return out
}

// --- terms ---

func TestTermLifecycle(t *testing.T) {
	e := newEnv(t)
	term, err := e.svc.CreateTerm(ctx, e.director, CreateTermInput{Name: "2026-2", StartDate: day("2026-05-04"), EndDate: day("2026-08-28")})
	if err != nil || term.Status != domain.TermDraft {
		t.Fatalf("create: %v %+v", err, term)
	}
	if term, err = e.svc.ActivateTerm(ctx, e.director, term.ID); err != nil || term.Status != domain.TermActive {
		t.Fatalf("activate: %v %+v", err, term)
	}
	_, err = e.svc.ActivateTerm(ctx, e.director, term.ID)
	wantErr(t, err, domain.ErrInvalidTermTransition)
	if term, err = e.svc.CloseTerm(ctx, e.director, term.ID); err != nil || term.Status != domain.TermClosed {
		t.Fatalf("close: %v %+v", err, term)
	}
	_, err = e.svc.ActivateTerm(ctx, e.director, term.ID)
	wantErr(t, err, domain.ErrInvalidTermTransition)
	_, err = e.svc.CloseTerm(ctx, e.director, term.ID)
	wantErr(t, err, domain.ErrInvalidTermTransition)

	want := []string{"academic.term_created", "academic.term_activated", "academic.term_closed"}
	got := actions(e.st)
	if len(got) != len(want) {
		t.Fatalf("audit actions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("audit actions = %v, want %v", got, want)
		}
	}
}

func TestDraftCannotBeClosedAndSeveralTermsMayBeActive(t *testing.T) {
	e := newEnv(t)
	draft, _ := e.svc.CreateTerm(ctx, e.director, CreateTermInput{Name: "2026-2", StartDate: day("2026-05-04"), EndDate: day("2026-08-28")})
	_, err := e.svc.CloseTerm(ctx, e.director, draft.ID)
	wantErr(t, err, domain.ErrInvalidTermTransition)
	// e.term is already active; activating another must be allowed (no single-active rule).
	if _, err := e.svc.ActivateTerm(ctx, e.director, draft.ID); err != nil {
		t.Fatalf("a second active term must be allowed: %v", err)
	}
}

func TestTermValidationAndRaces(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.CreateTerm(ctx, e.director, CreateTermInput{Name: "x", StartDate: day("2026-05-04"), EndDate: day("2026-05-04")})
	wantErr(t, err, domain.ErrInvalidDates)
	_, err = e.svc.CreateTerm(ctx, e.director, CreateTermInput{Name: "", StartDate: day("2026-01-01"), EndDate: day("2026-02-01")})
	if err == nil {
		t.Fatal("blank name must be rejected")
	}
	e.st.statusRace = true // another request moved the term between our read and our write
	_, err = e.svc.CloseTerm(ctx, e.director, e.term.ID)
	wantErr(t, err, domain.ErrInvalidTermTransition)
	_, err = e.svc.CloseTerm(ctx, e.director, uuid.New())
	wantErr(t, err, domain.ErrTermNotFound)
}

func TestTermsAreDirectorOnly(t *testing.T) {
	e := newEnv(t)
	for name, p := range map[string]iddomain.Principal{"admin": e.admin, "teacher": e.teacher, "tutor": e.tutor, "student": e.student} {
		if _, err := e.svc.CreateTerm(ctx, p, CreateTermInput{Name: "x", StartDate: day("2026-01-01"), EndDate: day("2026-02-01")}); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s create: %v", name, err)
		}
		if _, err := e.svc.ListTerms(ctx, p); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s list: %v", name, err)
		}
		if _, err := e.svc.GetTerm(ctx, p, e.term.ID); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s get: %v", name, err)
		}
		if _, err := e.svc.ActivateTerm(ctx, p, e.term.ID); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s activate: %v", name, err)
		}
		if _, err := e.svc.CloseTerm(ctx, p, e.term.ID); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s close: %v", name, err)
		}
	}
}

// --- groups ---

func TestCreateGroupValidation(t *testing.T) {
	e := newEnv(t)
	base := CreateGroupInput{TermID: e.term.ID, CareerID: e.career.ID, Code: "2A", Level: 2}
	with := func(f func(*CreateGroupInput)) CreateGroupInput { in := base; f(&in); return in }
	tutor := &e.tutor.UserID
	inactive := uuid.New()
	e.st.users[inactive] = &domain.UserRef{ID: inactive, IsActive: false, Roles: []string{"TUTOR"}}

	tests := []struct {
		name string
		in   CreateGroupInput
		want error
	}{
		{"level zero", with(func(i *CreateGroupInput) { i.Level = 0 }), domain.ErrInvalidLevel},
		{"negative level", with(func(i *CreateGroupInput) { i.Level = -3 }), domain.ErrInvalidLevel},
		{"unknown term", with(func(i *CreateGroupInput) { i.TermID = uuid.New() }), refTerm},
		{"unknown career", with(func(i *CreateGroupInput) { i.CareerID = uuid.New() }), refCareer},
		{"unknown tutor", with(func(i *CreateGroupInput) { u := uuid.New(); i.TutorID = &u }), domain.ErrUserNotFound},
		{"tutor is a teacher", with(func(i *CreateGroupInput) { i.TutorID = &e.teacher.UserID }), domain.ErrInvalidTutor},
		{"tutor is a student", with(func(i *CreateGroupInput) { i.TutorID = &e.student.UserID }), domain.ErrInvalidTutor},
		{"inactive tutor", with(func(i *CreateGroupInput) { i.TutorID = &inactive }), domain.ErrInvalidTutor},
		{"empty code", with(func(i *CreateGroupInput) { i.Code = " " }), nil},
		{"ok with tutor", with(func(i *CreateGroupInput) { i.TutorID = tutor }), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := e.svc.CreateGroup(ctx, e.director, tt.in)
			switch {
			case tt.want != nil:
				wantErr(t, err, tt.want)
			case tt.name == "empty code":
				if err == nil {
					t.Fatal("empty code must fail")
				}
			default:
				if err != nil || g.TutorID == nil || *g.TutorID != *tutor {
					t.Fatalf("err=%v group=%+v", err, g)
				}
			}
		})
	}
	// A rejected request must not leave a group behind.
	if len(e.st.groups) != 1 {
		t.Fatalf("groups stored = %d, want only the valid one", len(e.st.groups))
	}
}

func TestCreateGroupRejectsClosedTermInactiveCareerAndDuplicateCode(t *testing.T) {
	e := newEnv(t)
	e.newGroup(nil)
	_, err := e.svc.CreateGroup(ctx, e.director, CreateGroupInput{TermID: e.term.ID, CareerID: e.career.ID, Code: "2a", Level: 2})
	wantErr(t, err, domain.ErrGroupCodeTaken) // codes are normalized to upper case

	e.career.IsActive = false
	_, err = e.svc.CreateGroup(ctx, e.director, CreateGroupInput{TermID: e.term.ID, CareerID: e.career.ID, Code: "3A", Level: 3})
	wantErr(t, err, domain.ErrCareerInactive)
	e.career.IsActive = true

	if _, err := e.svc.CloseTerm(ctx, e.director, e.term.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CreateGroup(ctx, e.director, CreateGroupInput{TermID: e.term.ID, CareerID: e.career.ID, Code: "4A", Level: 4})
	wantErr(t, err, domain.ErrTermClosed)
}

func TestSetGroupTutor(t *testing.T) {
	e := newEnv(t)
	g := e.newGroup(nil)
	got, err := e.svc.SetGroupTutor(ctx, e.director, g.ID, &e.tutor.UserID)
	if err != nil || got.TutorID == nil || *got.TutorID != e.tutor.UserID {
		t.Fatalf("assign: %v %+v", err, got)
	}
	if got, err = e.svc.SetGroupTutor(ctx, e.director, g.ID, &e.otherTutor.UserID); err != nil || *got.TutorID != e.otherTutor.UserID {
		t.Fatalf("change: %v %+v", err, got)
	}
	_, err = e.svc.SetGroupTutor(ctx, e.director, g.ID, &e.teacher.UserID)
	wantErr(t, err, domain.ErrInvalidTutor)
	if got, err = e.svc.SetGroupTutor(ctx, e.director, g.ID, nil); err != nil || got.TutorID != nil {
		t.Fatalf("remove: %v %+v", err, got)
	}
	_, err = e.svc.SetGroupTutor(ctx, e.tutor, g.ID, &e.tutor.UserID) // a tutor cannot self-assign
	wantErr(t, err, domain.ErrForbidden)
	_, err = e.svc.SetGroupTutor(ctx, e.director, uuid.New(), nil)
	wantErr(t, err, domain.ErrGroupNotFound)

	if _, err := e.svc.CloseTerm(ctx, e.director, e.term.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.SetGroupTutor(ctx, e.director, g.ID, &e.tutor.UserID)
	wantErr(t, err, domain.ErrTermClosed)
}

func TestGroupAccessDependsOnRelationship(t *testing.T) {
	e := newEnv(t)
	g, _ := e.groupWithAssignment()
	if _, err := e.svc.EnrollStudent(ctx, e.director, g.ID, e.student.UserID); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		p    iddomain.Principal
		want error
	}{
		{"director", e.director, nil},
		{"group tutor", e.tutor, nil},
		{"other tutor", e.otherTutor, domain.ErrForbidden},
		{"teacher of the group", e.teacher, nil},
		{"teacher of another group", e.otherTeacher, domain.ErrForbidden},
		{"enrolled student", e.student, nil},
		{"student of another group", e.otherStudent, domain.ErrForbidden},
		{"admin", e.admin, domain.ErrForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.svc.GetGroup(ctx, tt.p, g.ID)
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("GetGroup err = %v, want %v", err, tt.want)
			}
		})
	}
	_, err := e.svc.GetGroup(ctx, e.director, uuid.New())
	wantErr(t, err, domain.ErrGroupNotFound)
}

func TestWithdrawnStudentLosesGroupAccess(t *testing.T) {
	e := newEnv(t)
	g := e.newGroup(nil)
	enr, err := e.svc.EnrollStudent(ctx, e.director, g.ID, e.student.UserID)
	if err != nil {
		t.Fatal(err)
	}
	enr.Status = domain.EnrollmentWithdrawn
	_, err = e.svc.GetGroup(ctx, e.student, g.ID)
	wantErr(t, err, domain.ErrForbidden)
}

func TestGroupRosterIsForDirectorAndOwnTutorOnly(t *testing.T) {
	e := newEnv(t)
	g, _ := e.groupWithAssignment()
	if _, err := e.svc.EnrollStudent(ctx, e.director, g.ID, e.student.UserID); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]iddomain.Principal{"director": e.director, "own tutor": e.tutor} {
		list, err := e.svc.ListGroupEnrollments(ctx, p, g.ID)
		if err != nil || len(list) != 1 {
			t.Errorf("%s: %v %v", name, err, list)
		}
	}
	for name, p := range map[string]iddomain.Principal{"other tutor": e.otherTutor, "teacher": e.teacher, "student": e.student, "admin": e.admin} {
		if _, err := e.svc.ListGroupEnrollments(ctx, p, g.ID); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: err = %v, want forbidden", name, err)
		}
	}
}

func TestListGroupsBuildsScopeFromPrincipalOnly(t *testing.T) {
	e := newEnv(t)
	multi := principal(e.teacher.UserID, iddomain.RoleTeacher, iddomain.RoleTutor)
	if _, err := e.svc.ListGroups(ctx, multi, nil); err != nil {
		t.Fatal(err)
	}
	want := ports.Scope{UserID: e.teacher.UserID, Teacher: true, Tutor: true}
	if e.st.lastScope != want {
		t.Fatalf("scope = %+v, want %+v", e.st.lastScope, want)
	}
	if _, err := e.svc.ListGroups(ctx, e.admin, nil); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("admin err = %v", err)
	}
}

// --- enrollments ---

func TestEnrollStudent(t *testing.T) {
	e := newEnv(t)
	g := e.newGroup(nil)
	if _, err := e.svc.EnrollStudent(ctx, e.director, g.ID, e.student.UserID); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.EnrollStudent(ctx, e.director, g.ID, e.student.UserID)
	wantErr(t, err, domain.ErrAlreadyEnrolled)
	_, err = e.svc.EnrollStudent(ctx, e.director, g.ID, e.teacher.UserID)
	wantErr(t, err, domain.ErrInvalidStudent)
	_, err = e.svc.EnrollStudent(ctx, e.director, g.ID, uuid.New())
	wantErr(t, err, domain.ErrUserNotFound)
	_, err = e.svc.EnrollStudent(ctx, e.director, uuid.New(), e.otherStudent.UserID)
	wantErr(t, err, domain.ErrGroupNotFound)
	_, err = e.svc.EnrollStudent(ctx, e.tutor, g.ID, e.otherStudent.UserID)
	wantErr(t, err, domain.ErrForbidden)

	// a deactivated student cannot be enrolled
	e.st.users[e.otherStudent.UserID].IsActive = false
	_, err = e.svc.EnrollStudent(ctx, e.director, g.ID, e.otherStudent.UserID)
	wantErr(t, err, domain.ErrInvalidStudent)
	e.st.users[e.otherStudent.UserID].IsActive = true

	if _, err := e.svc.CloseTerm(ctx, e.director, e.term.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.EnrollStudent(ctx, e.director, g.ID, e.otherStudent.UserID)
	wantErr(t, err, domain.ErrTermClosed)
}

// --- teaching assignments ---

func TestCreateAssignmentRules(t *testing.T) {
	e := newEnv(t)
	g := e.newGroup(nil)
	other := &domain.Subject{ID: uuid.New(), Code: "FIS1", Name: "Fisica", IsActive: true}
	e.st.subjects[other.ID] = other // exists, but is not in the career's plan
	retired := &domain.Subject{ID: uuid.New(), Code: "OLD1", Name: "Obsoleta", IsActive: false}
	e.st.subjects[retired.ID] = retired
	e.st.curriculum[[3]any{e.career.ID, retired.ID, int16(2)}] = true
	// in the plan, but at another level than the group's
	wrongLevel := &domain.Subject{ID: uuid.New(), Code: "QUI1", Name: "Quimica", IsActive: true}
	e.st.subjects[wrongLevel.ID] = wrongLevel
	e.st.curriculum[[3]any{e.career.ID, wrongLevel.ID, int16(5)}] = true

	tests := []struct {
		name  string
		actor iddomain.Principal
		group uuid.UUID
		in    CreateAssignmentInput
		want  error
	}{
		{"subject not in the plan", e.director, g.ID, CreateAssignmentInput{SubjectID: other.ID, TeacherID: e.teacher.UserID}, domain.ErrNotInCurriculum},
		{"subject at another level", e.director, g.ID, CreateAssignmentInput{SubjectID: wrongLevel.ID, TeacherID: e.teacher.UserID}, domain.ErrNotInCurriculum},
		{"inactive subject", e.director, g.ID, CreateAssignmentInput{SubjectID: retired.ID, TeacherID: e.teacher.UserID}, domain.ErrSubjectInactive},
		{"unknown subject", e.director, g.ID, CreateAssignmentInput{SubjectID: uuid.New(), TeacherID: e.teacher.UserID}, refSubject},
		{"teacher lacks role", e.director, g.ID, CreateAssignmentInput{SubjectID: e.subject.ID, TeacherID: e.tutor.UserID}, domain.ErrInvalidTeacher},
		{"student as teacher", e.director, g.ID, CreateAssignmentInput{SubjectID: e.subject.ID, TeacherID: e.student.UserID}, domain.ErrInvalidTeacher},
		{"unknown teacher", e.director, g.ID, CreateAssignmentInput{SubjectID: e.subject.ID, TeacherID: uuid.New()}, domain.ErrUserNotFound},
		{"unknown group", e.director, uuid.New(), CreateAssignmentInput{SubjectID: e.subject.ID, TeacherID: e.teacher.UserID}, domain.ErrGroupNotFound},
		{"teacher cannot assign", e.teacher, g.ID, CreateAssignmentInput{SubjectID: e.subject.ID, TeacherID: e.teacher.UserID}, domain.ErrForbidden},
		{"valid", e.director, g.ID, CreateAssignmentInput{SubjectID: e.subject.ID, TeacherID: e.teacher.UserID}, nil},
		{"duplicate subject in group", e.director, g.ID, CreateAssignmentInput{SubjectID: e.subject.ID, TeacherID: e.otherTeacher.UserID}, domain.ErrAssignmentExists},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.svc.CreateAssignment(ctx, tt.actor, tt.group, tt.in)
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	if len(e.st.assignments) != 1 {
		t.Fatalf("assignments stored = %d, want 1", len(e.st.assignments))
	}
	if _, err := e.svc.CloseTerm(ctx, e.director, e.term.ID); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.CreateAssignment(ctx, e.director, g.ID, CreateAssignmentInput{SubjectID: e.subject.ID, TeacherID: e.teacher.UserID})
	wantErr(t, err, domain.ErrTermClosed)
}

func TestChangeTeacher(t *testing.T) {
	e := newEnv(t)
	_, a := e.groupWithAssignment()
	got, err := e.svc.ChangeTeacher(ctx, e.director, a.ID, e.otherTeacher.UserID)
	if err != nil || got.TeacherID != e.otherTeacher.UserID {
		t.Fatalf("change: %v %+v", err, got)
	}
	_, err = e.svc.ChangeTeacher(ctx, e.director, a.ID, e.student.UserID)
	wantErr(t, err, domain.ErrInvalidTeacher)
	_, err = e.svc.ChangeTeacher(ctx, e.otherTeacher, a.ID, e.teacher.UserID) // the teacher cannot hand the class over
	wantErr(t, err, domain.ErrForbidden)
	_, err = e.svc.ChangeTeacher(ctx, e.director, uuid.New(), e.teacher.UserID)
	wantErr(t, err, domain.ErrAssignmentNotFound)
}

func TestAssignmentAccessDependsOnRelationship(t *testing.T) {
	e := newEnv(t)
	g, a := e.groupWithAssignment()
	if _, err := e.svc.EnrollStudent(ctx, e.director, g.ID, e.student.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateSubjectEnrollment(ctx, e.director, CreateSubjectEnrollmentInput{StudentID: e.student.UserID, TeachingAssignmentID: a.ID}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		p      iddomain.Principal
		get    bool // may read the assignment and its schedules
		roster bool // may list its students
	}{
		{"director", e.director, true, true},
		{"its teacher", e.teacher, true, true},
		{"another teacher", e.otherTeacher, false, false},
		{"tutor of the group", e.tutor, true, false},
		{"tutor of another group", e.otherTutor, false, false},
		{"subject-enrolled student", e.student, true, false},
		{"other student", e.otherStudent, false, false},
		{"admin", e.admin, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.svc.GetAssignment(ctx, tt.p, a.ID)
			if tt.get != (err == nil) || (!tt.get && !errors.Is(err, domain.ErrForbidden)) {
				t.Errorf("GetAssignment err = %v, want allowed=%v", err, tt.get)
			}
			_, err = e.svc.ListAssignmentSchedules(ctx, tt.p, a.ID)
			if tt.get != (err == nil) {
				t.Errorf("ListAssignmentSchedules err = %v, want allowed=%v", err, tt.get)
			}
			_, err = e.svc.ListAssignmentStudents(ctx, tt.p, a.ID)
			if tt.roster != (err == nil) || (!tt.roster && !errors.Is(err, domain.ErrForbidden)) {
				t.Errorf("ListAssignmentStudents err = %v, want allowed=%v", err, tt.roster)
			}
		})
	}
	_, err := e.svc.GetAssignment(ctx, e.director, uuid.New())
	wantErr(t, err, domain.ErrAssignmentNotFound)
}

func TestListAssignmentsOfAForeignGroupIsForbidden(t *testing.T) {
	e := newEnv(t)
	g, _ := e.groupWithAssignment()
	if _, err := e.svc.ListAssignments(ctx, e.otherTeacher, &g.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("other teacher err = %v", err)
	}
	if _, err := e.svc.ListAssignments(ctx, e.otherStudent, &g.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("other student err = %v", err)
	}
	if _, err := e.svc.ListAssignments(ctx, e.teacher, &g.ID); err != nil {
		t.Fatalf("own teacher err = %v", err)
	}
}

// --- schedules ---

func TestCreateSchedule(t *testing.T) {
	e := newEnv(t)
	_, a := e.groupWithAssignment()
	mk := func(day int, s, en string) CreateScheduleInput {
		return CreateScheduleInput{DayOfWeek: day, StartTime: s, EndTime: en, Classroom: "Aula 3"}
	}
	if _, err := e.svc.CreateSchedule(ctx, e.director, a.ID, mk(1, "08:00", "10:00")); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		actor iddomain.Principal
		id    uuid.UUID
		in    CreateScheduleInput
		want  error
	}{
		{"back to back is fine", e.director, a.ID, mk(1, "10:00", "11:00"), nil},
		{"same time other day", e.director, a.ID, mk(2, "08:00", "10:00"), nil},
		{"overlap", e.director, a.ID, mk(1, "09:00", "11:30"), domain.ErrScheduleOverlap},
		{"exact duplicate", e.director, a.ID, mk(1, "08:00", "10:00"), domain.ErrScheduleOverlap},
		{"start after end", e.director, a.ID, mk(3, "10:00", "09:00"), domain.ErrInvalidTimeRange},
		{"bad day", e.director, a.ID, mk(8, "08:00", "09:00"), domain.ErrInvalidDay},
		{"bad time", e.director, a.ID, mk(3, "8am", "09:00"), domain.ErrInvalidTime},
		{"unknown assignment", e.director, uuid.New(), mk(3, "08:00", "09:00"), domain.ErrAssignmentNotFound},
		{"teacher cannot create", e.teacher, a.ID, mk(3, "08:00", "09:00"), domain.ErrForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.svc.CreateSchedule(ctx, tt.actor, tt.id, tt.in)
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	if len(e.st.schedules) != 3 {
		t.Fatalf("schedules stored = %d, want 3", len(e.st.schedules))
	}
	if _, err := e.svc.CloseTerm(ctx, e.director, e.term.ID); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.CreateSchedule(ctx, e.director, a.ID, mk(5, "08:00", "09:00"))
	wantErr(t, err, domain.ErrTermClosed)
}

// --- subject enrollments ---

func TestCreateSubjectEnrollmentRules(t *testing.T) {
	e := newEnv(t)
	g, a := e.groupWithAssignment()
	if _, err := e.svc.EnrollStudent(ctx, e.director, g.ID, e.student.UserID); err != nil {
		t.Fatal(err)
	}
	one, two := 1, 2

	// a student of ANOTHER group
	g2, err := e.svc.CreateGroup(ctx, e.director, CreateGroupInput{TermID: e.term.ID, CareerID: e.career.ID, Code: "2B", Level: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.EnrollStudent(ctx, e.director, g2.ID, e.otherStudent.UserID); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		p    iddomain.Principal
		in   CreateSubjectEnrollmentInput
		want error
	}{
		{"student of another group", e.director, CreateSubjectEnrollmentInput{StudentID: e.otherStudent.UserID, TeachingAssignmentID: a.ID}, domain.ErrStudentNotInGrp},
		{"not a student", e.director, CreateSubjectEnrollmentInput{StudentID: e.teacher.UserID, TeachingAssignmentID: a.ID}, domain.ErrInvalidStudent},
		{"unknown student", e.director, CreateSubjectEnrollmentInput{StudentID: uuid.New(), TeachingAssignmentID: a.ID}, domain.ErrUserNotFound},
		{"unknown assignment", e.director, CreateSubjectEnrollmentInput{StudentID: e.student.UserID, TeachingAssignmentID: uuid.New()}, refAssignment},
		{"attempt 2 on a first enrollment", e.director, CreateSubjectEnrollmentInput{StudentID: e.student.UserID, TeachingAssignmentID: a.ID, AttemptNumber: &two}, domain.ErrInvalidAttempt},
		{"teacher cannot enroll", e.teacher, CreateSubjectEnrollmentInput{StudentID: e.student.UserID, TeachingAssignmentID: a.ID}, domain.ErrForbidden},
		{"valid, attempt defaulted", e.director, CreateSubjectEnrollmentInput{StudentID: e.student.UserID, TeachingAssignmentID: a.ID}, nil},
		{"duplicate", e.director, CreateSubjectEnrollmentInput{StudentID: e.student.UserID, TeachingAssignmentID: a.ID}, domain.ErrSubjectEnrollExists},
		{"duplicate with explicit attempt", e.director, CreateSubjectEnrollmentInput{StudentID: e.student.UserID, TeachingAssignmentID: a.ID, AttemptNumber: &one}, domain.ErrSubjectEnrollExists},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			se, err := e.svc.CreateSubjectEnrollment(ctx, tt.p, tt.in)
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if err == nil && (se.AttemptNumber != 1 || se.Status != domain.SubjectEnrollmentActive) {
				t.Fatalf("created %+v, want attempt 1 / ACTIVE", se)
			}
		})
	}
	if len(e.st.subjectEnr) != 1 {
		t.Fatalf("subject enrollments stored = %d, want 1", len(e.st.subjectEnr))
	}
}

func TestRetakeGetsNextAttemptNumber(t *testing.T) {
	e := newEnv(t)
	g, a := e.groupWithAssignment()
	if _, err := e.svc.EnrollStudent(ctx, e.director, g.ID, e.student.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateSubjectEnrollment(ctx, e.director, CreateSubjectEnrollmentInput{StudentID: e.student.UserID, TeachingAssignmentID: a.ID}); err != nil {
		t.Fatal(err)
	}
	// Next term the student retakes the same subject in a new group.
	term2 := &domain.Term{ID: uuid.New(), Name: "2026-2", Status: domain.TermActive, StartDate: day("2026-05-04"), EndDate: day("2026-08-28")}
	e.st.terms[term2.ID] = term2
	g2, err := e.svc.CreateGroup(ctx, e.director, CreateGroupInput{TermID: term2.ID, CareerID: e.career.ID, Code: "2A", Level: 2})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := e.svc.CreateAssignment(ctx, e.director, g2.ID, CreateAssignmentInput{SubjectID: e.subject.ID, TeacherID: e.teacher.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.EnrollStudent(ctx, e.director, g2.ID, e.student.UserID); err != nil {
		t.Fatal(err)
	}
	one, two := 1, 2
	_, err = e.svc.CreateSubjectEnrollment(ctx, e.director, CreateSubjectEnrollmentInput{StudentID: e.student.UserID, TeachingAssignmentID: a2.ID, AttemptNumber: &one})
	wantErr(t, err, domain.ErrInvalidAttempt)
	se, err := e.svc.CreateSubjectEnrollment(ctx, e.director, CreateSubjectEnrollmentInput{StudentID: e.student.UserID, TeachingAssignmentID: a2.ID, AttemptNumber: &two})
	if err != nil || se.AttemptNumber != 2 {
		t.Fatalf("retake: %v %+v", err, se)
	}
}

func TestSubjectEnrollmentsRejectedInClosedTerm(t *testing.T) {
	e := newEnv(t)
	g, a := e.groupWithAssignment()
	if _, err := e.svc.EnrollStudent(ctx, e.director, g.ID, e.student.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CloseTerm(ctx, e.director, e.term.ID); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.CreateSubjectEnrollment(ctx, e.director, CreateSubjectEnrollmentInput{StudentID: e.student.UserID, TeachingAssignmentID: a.ID})
	wantErr(t, err, domain.ErrTermClosed)
	_, err = e.svc.EnrollGroupInAssignment(ctx, e.director, a.ID)
	wantErr(t, err, domain.ErrTermClosed)
}

func TestEnrollGroupInAssignment(t *testing.T) {
	e := newEnv(t)
	_, a := e.groupWithAssignment()
	n, err := e.svc.EnrollGroupInAssignment(ctx, e.director, a.ID)
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	for name, p := range map[string]iddomain.Principal{"teacher": e.teacher, "tutor": e.tutor, "student": e.student, "admin": e.admin} {
		if _, err := e.svc.EnrollGroupInAssignment(ctx, p, a.ID); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	_, err = e.svc.EnrollGroupInAssignment(ctx, e.director, uuid.New())
	wantErr(t, err, domain.ErrAssignmentNotFound)
}

func TestListSubjectEnrollmentsNeverWidensScope(t *testing.T) {
	e := newEnv(t)
	victim := e.otherStudent.UserID
	filter := ports.SubjectEnrollmentFilter{StudentID: &victim}

	if _, err := e.svc.ListSubjectEnrollments(ctx, e.student, filter); err != nil {
		t.Fatal(err)
	}
	if e.st.lastSEFilt.StudentID != nil || !e.st.lastScope.Student || e.st.lastScope.UserID != e.student.UserID || e.st.lastScope.All {
		t.Fatalf("student call: scope=%+v filter=%+v; the client-supplied student id must be dropped", e.st.lastScope, e.st.lastSEFilt)
	}
	if _, err := e.svc.ListSubjectEnrollments(ctx, e.teacher, filter); err != nil {
		t.Fatal(err)
	}
	if e.st.lastSEFilt.StudentID != nil || !e.st.lastScope.Teacher || e.st.lastScope.All {
		t.Fatalf("teacher call: scope=%+v filter=%+v", e.st.lastScope, e.st.lastSEFilt)
	}
	if _, err := e.svc.ListSubjectEnrollments(ctx, e.director, filter); err != nil {
		t.Fatal(err)
	}
	if e.st.lastSEFilt.StudentID == nil || !e.st.lastScope.All {
		t.Fatalf("director call: scope=%+v filter=%+v", e.st.lastScope, e.st.lastSEFilt)
	}
	for name, p := range map[string]iddomain.Principal{"tutor": e.tutor, "admin": e.admin} {
		if _, err := e.svc.ListSubjectEnrollments(ctx, p, filter); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: err = %v, want forbidden", name, err)
		}
	}
}

// --- catalogs ---

func TestCatalogWritesAreDirectorOnlyAndValidated(t *testing.T) {
	e := newEnv(t)
	for name, p := range map[string]iddomain.Principal{"admin": e.admin, "teacher": e.teacher, "student": e.student} {
		if _, err := e.svc.CreateCareer(ctx, p, CreateCareerInput{Code: "X", Name: "X"}); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s CreateCareer: %v", name, err)
		}
		if _, err := e.svc.CreateSubject(ctx, p, CreateSubjectInput{Code: "X", Name: "X"}); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s CreateSubject: %v", name, err)
		}
		if _, err := e.svc.CreateUnit(ctx, p, e.subject.ID, CreateUnitInput{Number: 1, Name: "X"}); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s CreateUnit: %v", name, err)
		}
		if _, err := e.svc.AddCurriculum(ctx, p, e.career.ID, AddCurriculumInput{SubjectID: e.subject.ID, Level: 1}); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s AddCurriculum: %v", name, err)
		}
	}
	c, err := e.svc.CreateCareer(ctx, e.director, CreateCareerInput{Code: " ing-1 ", Name: " Ingenieria "})
	if err != nil || c.Code != "ING-1" || c.Name != "Ingenieria" || !c.IsActive {
		t.Fatalf("career: %v %+v", err, c)
	}
	_, err = e.svc.CreateCareer(ctx, e.director, CreateCareerInput{Code: "ING-1", Name: "Otra"})
	wantErr(t, err, domain.ErrCareerCodeTaken)

	_, err = e.svc.AddCurriculum(ctx, e.director, e.career.ID, AddCurriculumInput{SubjectID: e.subject.ID, Level: 0})
	wantErr(t, err, domain.ErrInvalidLevel)
	_, err = e.svc.AddCurriculum(ctx, e.director, e.career.ID, AddCurriculumInput{SubjectID: uuid.New(), Level: 1})
	wantErr(t, err, refSubject)
	_, err = e.svc.AddCurriculum(ctx, e.director, uuid.New(), AddCurriculumInput{SubjectID: e.subject.ID, Level: 1})
	wantErr(t, err, domain.ErrCareerNotFound)
	if _, err = e.svc.AddCurriculum(ctx, e.director, e.career.ID, AddCurriculumInput{SubjectID: e.subject.ID, Level: 4}); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.AddCurriculum(ctx, e.director, e.career.ID, AddCurriculumInput{SubjectID: e.subject.ID, Level: 4})
	wantErr(t, err, domain.ErrCurriculumExists)
	// the same subject can sit at a second level of the same career
	if _, err = e.svc.AddCurriculum(ctx, e.director, e.career.ID, AddCurriculumInput{SubjectID: e.subject.ID, Level: 5}); err != nil {
		t.Fatal(err)
	}
}
