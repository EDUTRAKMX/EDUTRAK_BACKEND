// Package ports declares what the grades use cases need from the outside.
package ports

import (
	"context"

	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	"github.com/edutrak/backend/internal/grades/domain"
	"github.com/google/uuid"
)

// ViewScope limits which student attempts a reader may see. It is built from
// the authenticated principal only.
type ViewScope struct {
	All       bool       // DIRECTOR, or a student reading their own record
	TeacherID *uuid.UUID // attempts of assignments taught by this user
	TutorID   *uuid.UUID // attempts of groups tutored by this user
}

// Repository persists grades and reads grade sheets (one query each, no N+1).
type Repository interface {
	Create(ctx context.Context, g *domain.Grade) error // domain.ErrGradeExists
	// GetForUpdate reads the grade locking its row until the transaction ends.
	GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.Grade, error) // domain.ErrGradeNotFound
	Get(ctx context.Context, id uuid.UUID) (*domain.Grade, error)          // domain.ErrGradeNotFound
	UpdateScore(ctx context.Context, id uuid.UUID, s domain.Score) (*domain.Grade, error)

	// ListStudentSheets returns every attempt of the student within scope, with
	// all units of each subject (pending units have no grade).
	ListStudentSheets(ctx context.Context, studentID uuid.UUID, scope ViewScope) ([]domain.StudentSheet, error)
	// AssignmentSheet returns the students x units table of an assignment.
	AssignmentSheet(ctx context.Context, assignmentID uuid.UUID) (*domain.AssignmentSheet, error) // domain.ErrAssignmentNotFound
}

// Academic is the read-only view of academic data grading depends on.
type Academic interface {
	// EnrollmentRef loads a subject enrollment with its assignment, group and
	// term. With lockTerm the term row is locked FOR SHARE until the transaction
	// ends, so a concurrent close cannot slip in between check and write.
	EnrollmentRef(ctx context.Context, id uuid.UUID, lockTerm bool) (*domain.SubjectEnrollmentRef, error) // domain.ErrEnrollmentNotFound
	Unit(ctx context.Context, id uuid.UUID) (*domain.Unit, error)                                         // domain.ErrUnitNotFound
	// AssignmentOwner returns the data needed to authorize reads of an assignment.
	AssignmentOwner(ctx context.Context, assignmentID uuid.UUID) (*AssignmentOwner, error) // domain.ErrAssignmentNotFound
	// StudentVisible reports whether the student exists and is related to the scope
	// (director: any student; teacher: has an attempt in their assignments;
	// tutor: is enrolled in a group they tutor).
	StudentVisible(ctx context.Context, studentID uuid.UUID, scope ViewScope) (bool, error)
	StudentExists(ctx context.Context, studentID uuid.UUID) (bool, error)
}

type AssignmentOwner struct {
	TeacherID    uuid.UUID
	GroupTutorID *uuid.UUID
}

type AuditRecorder interface {
	Record(ctx context.Context, e auditdomain.Event) error
}

type Transactor interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}
