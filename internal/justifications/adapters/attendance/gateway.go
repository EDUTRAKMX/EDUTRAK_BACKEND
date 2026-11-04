// Package attendance adapts the Attendance module to the port Justifications needs.
// It is the only code through which a justification touches attendance records, and
// it goes through Attendance's own use cases (never its tables).
package attendance

import (
	"context"
	"errors"

	attendanceapp "github.com/edutrak/backend/internal/attendance/application"
	attendancedomain "github.com/edutrak/backend/internal/attendance/domain"
	"github.com/edutrak/backend/internal/justifications/domain"
	"github.com/edutrak/backend/internal/justifications/ports"
	"github.com/google/uuid"
)

type Gateway struct{ svc *attendanceapp.Service }

func NewGateway(svc *attendanceapp.Service) *Gateway { return &Gateway{svc: svc} }

func (g *Gateway) Absence(ctx context.Context, recordID uuid.UUID, lock bool) (*ports.Absence, error) {
	a, err := g.svc.AbsenceForJustification(ctx, recordID, lock)
	if errors.Is(err, attendancedomain.ErrRecordNotFound) {
		return nil, domain.ErrRecordNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ports.Absence{
		RecordID: a.RecordID, Status: string(a.Status), StudentID: a.StudentID, EnrollmentStatus: a.EnrollmentStatus,
		AssignmentID: a.AssignmentID, TeacherID: a.TeacherID, GroupTutorID: a.GroupTutorID, TermStatus: a.TermStatus,
	}, nil
}

func (g *Gateway) MarkJustified(ctx context.Context, recordID uuid.UUID) error {
	err := g.svc.JustifyAbsence(ctx, recordID)
	if errors.Is(err, attendancedomain.ErrAbsenceNotEligible) {
		return domain.ErrAbsenceNotEligible
	}
	return err
}
