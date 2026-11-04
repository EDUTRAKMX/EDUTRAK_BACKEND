// Package ports declares what the tutoring use cases need from the outside.
package ports

import (
	"context"
	"time"

	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	"github.com/edutrak/backend/internal/tutoring/domain"
	"github.com/google/uuid"
)

// Scope limits the groups a reader may see. It is built from the principal only.
type Scope struct {
	All     bool       // DIRECTOR
	TutorID *uuid.UUID // groups whose tutor_id is this user
}

type StudentFilter struct {
	TermID  *uuid.UUID
	GroupID *uuid.UUID
	Status  string // ACTIVE | WITHDRAWN
}

type FollowUpFilter struct {
	StudentID *uuid.UUID
	GroupID   *uuid.UUID
	TermID    *uuid.UUID
	From, To  *time.Time
}

type Page struct{ Limit, Offset int }

type Repository interface {
	// Context loads the enrollment of a student in a group with its academic context. With
	// lock the group and its term are held FOR SHARE until the transaction ends, so a
	// concurrent change of tutor or closing of the term cannot slip in between check and write.
	// Errors: ErrBodyStudentNotFound, ErrBodyGroupNotFound, ErrNotEnrolled.
	Context(ctx context.Context, studentID, groupID uuid.UUID, lock bool) (*domain.Enrollment, error)
	// Enrollments lists the enrollments of one student inside the scope, most recent term first.
	Enrollments(ctx context.Context, studentID uuid.UUID, scope Scope) ([]domain.Enrollment, error)
	// StudentExists is true when a user with that id exists.
	StudentExists(ctx context.Context, studentID uuid.UUID) (bool, error)
	// Students lists enrollments in scope with follow-up counters, in one query.
	Students(ctx context.Context, scope Scope, f StudentFilter) ([]domain.Enrollment, error)

	Create(ctx context.Context, f *domain.FollowUp) error
	Get(ctx context.Context, id uuid.UUID) (*domain.Entry, error) // domain.ErrFollowUpNotFound
	List(ctx context.Context, scope Scope, f FollowUpFilter, p Page) ([]domain.Entry, int, error)
}

// Overviews reads the academic situation of one student in one group from the Academic,
// Grades, Attendance and Justifications data (read-only; nothing is copied into Tutoring).
type Overviews interface {
	Subjects(ctx context.Context, studentID, groupID uuid.UUID) ([]domain.SubjectOverview, error)
}

type Clock interface{ Now() time.Time }

type AuditRecorder interface {
	Record(ctx context.Context, e auditdomain.Event) error
}

type Transactor interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}
