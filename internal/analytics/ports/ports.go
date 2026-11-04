// Package ports declares what the analytics use cases need from the outside.
package ports

import (
	"context"
	"time"

	"github.com/edutrak/backend/internal/analytics/domain"
	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	"github.com/google/uuid"
)

// Scope limits the groups a reader may see. It is built from the principal only.
type Scope struct {
	All     bool       // DIRECTOR
	TutorID *uuid.UUID // groups whose tutor_id is this user
}

type Page struct{ Limit, Offset int }

type AlertFilter struct {
	StudentID *uuid.UUID
	GroupID   *uuid.UUID
	TermID    *uuid.UUID
	Rule      string
	Status    string // "" = unresolved (OPEN + ACKNOWLEDGED); OPEN | ACKNOWLEDGED | RESOLVED | ALL
}

type OverviewFilter struct {
	TermID   *uuid.UUID
	CareerID *uuid.UUID
}

// Facts reads the data the indicators are derived from. It is read-only and never copies
// anything into Analytics: Academic, Grades, Attendance, Justifications and Tutoring tables
// are the single source.
type Facts interface {
	// Group loads a group with its career and term. With lock the group and its term are held
	// FOR SHARE until the transaction ends. domain.ErrGroupNotFound if missing.
	Group(ctx context.Context, groupID uuid.UUID, lock bool) (*domain.GroupInfo, error)
	StudentExists(ctx context.Context, studentID uuid.UUID) (bool, error)
	// Enrollment returns the student's enrollment in the group (domain.ErrContextNotFound if none).
	Enrollment(ctx context.Context, studentID, groupID uuid.UUID) (*domain.EnrollmentRef, error)
	// Enrollments lists the student's enrollments inside the scope, most recent period first.
	Enrollments(ctx context.Context, studentID uuid.UUID, scope Scope) ([]domain.EnrollmentRef, error)
	// Students loads the facts of the group's ACTIVE students (or of one student of the group,
	// whatever the enrollment status) with a constant number of queries.
	Students(ctx context.Context, groupID uuid.UUID, studentID *uuid.UUID) ([]domain.StudentFacts, error)
	// Overview aggregates, in SQL, one line per group inside the scope.
	Overview(ctx context.Context, scope Scope, f OverviewFilter) ([]domain.GroupOverview, error)
}

type NewAlert struct {
	ID        uuid.UUID
	GroupID   uuid.UUID
	Candidate domain.Candidate
	Evidence  []byte
	Now       time.Time
}

type Alerts interface {
	// LockGroup serializes the evaluations of one group until the transaction ends.
	LockGroup(ctx context.Context, groupID uuid.UUID) error
	// Unresolved lists the OPEN and ACKNOWLEDGED alerts of a group (optionally of one student).
	Unresolved(ctx context.Context, groupID uuid.UUID, studentID *uuid.UUID) ([]domain.StoredAlert, error)
	Create(ctx context.Context, a NewAlert) error
	Update(ctx context.Context, id uuid.UUID, message string, evidence []byte, now time.Time) error
	// Touch records that the condition was confirmed again at now.
	Touch(ctx context.Context, ids []uuid.UUID, now time.Time) error
	Resolve(ctx context.Context, ids []uuid.UUID, now time.Time) error

	Get(ctx context.Context, id uuid.UUID) (*domain.AlertView, error) // domain.ErrAlertNotFound
	List(ctx context.Context, scope Scope, f AlertFilter, p Page) ([]domain.AlertView, int, error)
	// OpenCounts counts unresolved alerts by rule for a group.
	OpenCounts(ctx context.Context, groupID uuid.UUID) (map[domain.Rule]int, error)
}

type Clock interface{ Now() time.Time }

type AuditRecorder interface {
	Record(ctx context.Context, e auditdomain.Event) error
}

type Transactor interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}
