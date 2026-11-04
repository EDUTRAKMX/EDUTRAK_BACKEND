// Package ports declares what the justification use cases need from the outside.
package ports

import (
	"context"
	"io"

	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	"github.com/edutrak/backend/internal/justifications/domain"
	"github.com/google/uuid"
)

// ViewScope limits which requests a reader may list (built from the principal only).
type ViewScope struct {
	All       bool       // DIRECTOR
	StudentID *uuid.UUID // the student's own requests
	TeacherID *uuid.UUID // requests of absences in assignments taught by this user
	TutorID   *uuid.UUID // requests of students in groups tutored by this user
}

type Repository interface {
	// Create inserts a PENDING_TUTOR request; domain.ErrAlreadyRequested when the absence
	// already has an open or approved one (enforced by a partial unique index).
	Create(ctx context.Context, j *domain.Justification) error
	AddEvidence(ctx context.Context, e *domain.Evidence) error
	HasActive(ctx context.Context, recordID uuid.UUID) (bool, error)

	// GetCase reads a request with its academic context. With lock the request row is held
	// FOR UPDATE until the transaction ends. domain.ErrNotFound if missing.
	GetCase(ctx context.Context, id uuid.UUID, lock bool) (*domain.Case, error)
	// UpdateStatus moves the request only if it is still in `from`; false means it changed meanwhile.
	UpdateStatus(ctx context.Context, id uuid.UUID, from, to domain.Status) (bool, error)
	// AddReview appends to the decision history; domain.ErrWrongStage if that stage was already decided.
	AddReview(ctx context.Context, r *domain.Review) error

	Detail(ctx context.Context, id uuid.UUID) (*domain.Detail, error)
	List(ctx context.Context, scope ViewScope, status *domain.Status) ([]domain.Case, error)
	Evidence(ctx context.Context, requestID, evidenceID uuid.UUID) (*domain.Evidence, error) // domain.ErrEvidenceNotFound
}

// Absence is the attendance record a request refers to, as the justification flow needs it.
type Absence struct {
	RecordID         uuid.UUID
	Status           string // current attendance status
	StudentID        uuid.UUID
	EnrollmentStatus string
	AssignmentID     uuid.UUID
	TeacherID        uuid.UUID
	GroupTutorID     *uuid.UUID
	TermStatus       string
}

// Attendance is the ONLY door to attendance data. Writes go through the Attendance
// module's own use case, never through its tables.
type Attendance interface {
	// Absence loads the record; with lock its row is held FOR UPDATE until the transaction ends.
	Absence(ctx context.Context, recordID uuid.UUID, lock bool) (*Absence, error) // domain.ErrRecordNotFound
	// MarkJustified turns ABSENT into JUSTIFIED inside the caller's transaction;
	// domain.ErrAbsenceNotEligible if the record is no longer ABSENT.
	MarkJustified(ctx context.Context, recordID uuid.UUID) error
}

// EvidenceStorage keeps the bytes of evidence files in a PRIVATE place. Keys are generated
// by the application; implementations must refuse anything that is not a well-formed key.
type EvidenceStorage interface {
	// Put stores r under key and returns the number of bytes written. It must not overwrite.
	Put(ctx context.Context, key string, r io.Reader) (int64, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes the object; deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
}

type AuditRecorder interface {
	Record(ctx context.Context, e auditdomain.Event) error
}

type Transactor interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}
