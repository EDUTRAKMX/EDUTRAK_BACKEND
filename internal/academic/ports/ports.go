// Package ports declares what the academic use cases need from the outside.
// Implementations live in adapters; nothing here mentions HTTP or SQL.
package ports

import (
	"context"

	"github.com/edutrak/backend/internal/academic/domain"
	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	"github.com/google/uuid"
)

// Scope tells a repository which rows the caller may see. It is built by the
// application layer from the authenticated principal, never from client input:
// the roles say which relationships apply, and UserID is the caller.
type Scope struct {
	UserID  uuid.UUID
	All     bool // DIRECTOR: every row
	Teacher bool // rows of teaching assignments the user teaches
	Tutor   bool // rows of groups the user tutors
	Student bool // rows of the user's own enrollments
}

type GroupFilter struct{ TermID *uuid.UUID }

type AssignmentFilter struct{ GroupID *uuid.UUID }

type SubjectEnrollmentFilter struct {
	TeachingAssignmentID *uuid.UUID
	StudentID            *uuid.UUID // honored only for Scope.All
}

// Catalog covers careers, subjects, units and the curriculum.
type Catalog interface {
	ListCareers(ctx context.Context) ([]domain.Career, error)
	GetCareer(ctx context.Context, id uuid.UUID) (*domain.Career, error) // domain.ErrCareerNotFound
	CreateCareer(ctx context.Context, c *domain.Career) error            // domain.ErrCareerCodeTaken

	ListSubjects(ctx context.Context) ([]domain.Subject, error)
	GetSubject(ctx context.Context, id uuid.UUID) (*domain.Subject, error) // domain.ErrSubjectNotFound
	CreateSubject(ctx context.Context, s *domain.Subject) error            // domain.ErrSubjectCodeTaken

	ListUnits(ctx context.Context, subjectID uuid.UUID) ([]domain.SubjectUnit, error)
	CreateUnit(ctx context.Context, u *domain.SubjectUnit) error // domain.ErrUnitNumberTaken

	ListCurriculum(ctx context.Context, careerID uuid.UUID, level *int16) ([]domain.CurriculumEntry, error)
	AddCurriculum(ctx context.Context, e domain.CurriculumEntry) error // domain.ErrCurriculumExists
	InCurriculum(ctx context.Context, careerID, subjectID uuid.UUID, level int16) (bool, error)
}

type Terms interface {
	Create(ctx context.Context, t *domain.Term) error // domain.ErrTermNameTaken
	List(ctx context.Context) ([]domain.Term, error)
	Get(ctx context.Context, id uuid.UUID) (*domain.Term, error) // domain.ErrTermNotFound
	// GetForShare reads the term holding a row lock until the transaction ends,
	// so a concurrent close cannot slip in between a check and a dependent write.
	GetForShare(ctx context.Context, id uuid.UUID) (*domain.Term, error)
	// UpdateStatus changes the status only if it is still `from`; false means it changed meanwhile.
	UpdateStatus(ctx context.Context, id uuid.UUID, from, to domain.TermStatus) (bool, error)
}

type Groups interface {
	Create(ctx context.Context, g *domain.Group) error // domain.ErrGroupCodeTaken
	Get(ctx context.Context, id uuid.UUID) (*domain.Group, error)
	List(ctx context.Context, s Scope, f GroupFilter) ([]domain.Group, error)
	SetTutor(ctx context.Context, id uuid.UUID, tutorID *uuid.UUID) error

	AddEnrollment(ctx context.Context, e *domain.Enrollment) error // domain.ErrAlreadyEnrolled
	ListEnrollments(ctx context.Context, groupID uuid.UUID) ([]domain.Enrollment, error)
	IsEnrolled(ctx context.Context, groupID, studentID uuid.UUID) (bool, error) // active enrollment
	TeachesInGroup(ctx context.Context, groupID, teacherID uuid.UUID) (bool, error)
}

type Assignments interface {
	Create(ctx context.Context, a *domain.TeachingAssignment) error // domain.ErrAssignmentExists
	Get(ctx context.Context, id uuid.UUID) (*domain.TeachingAssignment, error)
	// GetForUpdate locks the assignment row until the transaction ends.
	GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.TeachingAssignment, error)
	List(ctx context.Context, s Scope, f AssignmentFilter) ([]domain.TeachingAssignment, error)
	SetTeacher(ctx context.Context, id, teacherID uuid.UUID) error
	HasSubjectEnrollment(ctx context.Context, assignmentID, studentID uuid.UUID) (bool, error)

	CreateSchedule(ctx context.Context, s *domain.Schedule) error
	ListSchedules(ctx context.Context, assignmentID uuid.UUID) ([]domain.Schedule, error)
	ListScheduleEntries(ctx context.Context, s Scope, f AssignmentFilter) ([]domain.ScheduleEntry, error)
}

type SubjectEnrollments interface {
	Create(ctx context.Context, e *domain.SubjectEnrollment) error // domain.ErrSubjectEnrollExists
	// NextAttempt is 1 + the student's highest attempt in any assignment of the subject.
	NextAttempt(ctx context.Context, studentID, subjectID uuid.UUID) (int16, error)
	ExistsForAssignment(ctx context.Context, studentID, assignmentID uuid.UUID) (bool, error)
	// EnrollGroup creates the missing subject enrollments of every active
	// student of the assignment's group and returns how many it created.
	EnrollGroup(ctx context.Context, assignmentID uuid.UUID) (int, error)
	ListByAssignment(ctx context.Context, assignmentID uuid.UUID) ([]domain.SubjectEnrollment, error)
	List(ctx context.Context, s Scope, f SubjectEnrollmentFilter) ([]domain.SubjectEnrollment, error)
}

// UserDirectory is the read-only view of accounts academic needs to validate
// that a referenced user exists, is active and holds the expected role.
type UserDirectory interface {
	Find(ctx context.Context, id uuid.UUID) (*domain.UserRef, error) // domain.ErrUserNotFound
}

// AuditRecorder is satisfied by the audit module's recorder.
type AuditRecorder interface {
	Record(ctx context.Context, e auditdomain.Event) error
}

// Transactor runs fn atomically; repositories called with the given ctx join it.
type Transactor interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}
