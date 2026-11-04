// Package ports declares what the attendance use cases need from the outside.
package ports

import (
	"context"
	"net/netip"
	"time"

	"github.com/edutrak/backend/internal/attendance/domain"
	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	"github.com/google/uuid"
)

// Lock selects the row lock taken when a session is read inside a transaction.
type Lock int

const (
	LockNone   Lock = iota
	LockShare       // check-in, manual capture, correction: many may run together
	LockUpdate      // QR rotation and closing: exclusive, so they serialize with check-ins
)

// ViewScope limits which student attempts a reader may see (built from the principal only).
type ViewScope struct {
	All       bool
	TeacherID *uuid.UUID
	TutorID   *uuid.UUID
}

type Repository interface {
	CreateSession(ctx context.Context, s *domain.Session) error                       // domain.ErrSessionAlreadyOpen
	GetSession(ctx context.Context, id uuid.UUID, lock Lock) (*domain.Session, error) // domain.ErrSessionNotFound
	SetQR(ctx context.Context, sessionID uuid.UUID, hash string, expiresAt time.Time) error
	// CloseSession marks the session CLOSED and clears its QR.
	CloseSession(ctx context.Context, sessionID uuid.UUID, at time.Time) error
	// MarkAbsentees creates an ABSENT record (method MANUAL, recorded by the closing
	// teacher) for every ACTIVE subject enrollment of the session's assignment that has
	// none yet, and returns how many it created. Existing records are never touched.
	MarkAbsentees(ctx context.Context, sessionID, assignmentID, by uuid.UUID, at time.Time) (int, error)

	CreateRecord(ctx context.Context, r *domain.Record) error                            // domain.ErrAlreadyRegistered
	GetRecord(ctx context.Context, id uuid.UUID, forUpdate bool) (*domain.Record, error) // domain.ErrRecordNotFound
	UpdateRecordStatus(ctx context.Context, id uuid.UUID, status domain.RecordStatus) (*domain.Record, error)

	SessionDetail(ctx context.Context, sessionID uuid.UUID) (*domain.SessionDetail, error)
	AssignmentOverview(ctx context.Context, assignmentID uuid.UUID) (*domain.AssignmentOverview, error)
	StudentAttendance(ctx context.Context, studentID uuid.UUID, scope ViewScope) ([]domain.StudentAttendance, error)
}

// Academic is the read-only view of academic data attendance depends on.
type Academic interface {
	// Assignment loads a teaching assignment with its group tutor and term. With
	// lockTerm the term row is locked FOR SHARE until the transaction ends.
	Assignment(ctx context.Context, id uuid.UUID, lockTerm bool) (*domain.AssignmentRef, error) // domain.ErrAssignmentNotFound
	ScheduleBelongs(ctx context.Context, scheduleID, assignmentID uuid.UUID) (bool, error)
	// StudentEnrollment finds the student's subject enrollment in the assignment
	// (domain.ErrNotEnrolled when there is none).
	StudentEnrollment(ctx context.Context, assignmentID, studentID uuid.UUID) (*domain.EnrollmentRef, error)
	// EnrollmentInAssignment loads a subject enrollment only if it belongs to the assignment
	// (domain.ErrBodyEnrollmentNotFound otherwise).
	EnrollmentInAssignment(ctx context.Context, enrollmentID, assignmentID uuid.UUID) (*domain.EnrollmentRef, error)
	StudentExists(ctx context.Context, studentID uuid.UUID) (bool, error)
	StudentVisible(ctx context.Context, studentID uuid.UUID, scope ViewScope) (bool, error)
}

// QRTokens creates and hashes temporary QR tokens. Only the hash is persisted.
type QRTokens interface {
	New() (token, hash string, err error)
	Hash(token string) string
}

// NetworkPolicy decides whether a request address may register attendance by QR.
type NetworkPolicy interface {
	Allows(ip netip.Addr) bool
}

type Clock interface{ Now() time.Time }

type AuditRecorder interface {
	Record(ctx context.Context, e auditdomain.Event) error
}

type Transactor interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}
