package application

import (
	"context"

	"github.com/edutrak/backend/internal/academic/domain"
	"github.com/edutrak/backend/internal/academic/ports"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

type CreateSubjectEnrollmentInput struct {
	StudentID            uuid.UUID
	TeachingAssignmentID uuid.UUID
	// AttemptNumber is optional. When given it must equal the next attempt for
	// the student and subject (1 for a first time, 2 after a previous attempt...).
	AttemptNumber *int
}

// CreateSubjectEnrollment enrolls one student in a teaching assignment. The
// student must be actively enrolled in the assignment's own group.
func (s *Service) CreateSubjectEnrollment(ctx context.Context, p iddomain.Principal, in CreateSubjectEnrollmentInput) (*domain.SubjectEnrollment, error) {
	if err := requireDirector(p); err != nil {
		return nil, err
	}
	var out *domain.SubjectEnrollment
	err := s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		// Locking the assignment serializes every subject-enrollment write for it, so two
		// concurrent requests cannot both pass the duplicate check (or pick attempt 1 and 2).
		a, err := s.Assignments.GetForUpdate(ctx, in.TeachingAssignmentID)
		if err != nil {
			return asRef(err)
		}
		if _, err := s.openTerm(ctx, a.TermID); err != nil {
			return err
		}
		if err := s.requireUser(ctx, in.StudentID, domain.RoleStudent, domain.ErrInvalidStudent); err != nil {
			return err
		}
		inGroup, err := s.Groups.IsEnrolled(ctx, a.GroupID, in.StudentID)
		if err != nil {
			return err
		}
		if !inGroup {
			return domain.ErrStudentNotInGrp
		}
		exists, err := s.SubjectEnrollments.ExistsForAssignment(ctx, in.StudentID, a.ID)
		if err != nil {
			return err
		}
		if exists {
			return domain.ErrSubjectEnrollExists
		}
		next, err := s.SubjectEnrollments.NextAttempt(ctx, in.StudentID, a.SubjectID)
		if err != nil {
			return err
		}
		if in.AttemptNumber != nil && *in.AttemptNumber != int(next) {
			return domain.ErrInvalidAttempt
		}
		out = &domain.SubjectEnrollment{
			ID: s.NewID(), StudentID: in.StudentID, TeachingAssignmentID: a.ID,
			AttemptNumber: next, Status: domain.SubjectEnrollmentActive,
		}
		if err := s.SubjectEnrollments.Create(ctx, out); err != nil {
			return err
		}
		return s.audit(ctx, p, "academic.subject_enrollment_created", "subject_enrollment", out.ID, map[string]any{
			"student_id": in.StudentID.String(), "teaching_assignment_id": a.ID.String(), "attempt_number": next,
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// EnrollGroupInAssignment creates the missing subject enrollments for every
// active student of the assignment's group. It is idempotent.
func (s *Service) EnrollGroupInAssignment(ctx context.Context, p iddomain.Principal, assignmentID uuid.UUID) (int, error) {
	if err := requireDirector(p); err != nil {
		return 0, err
	}
	var created int
	err := s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		a, err := s.Assignments.GetForUpdate(ctx, assignmentID)
		if err != nil {
			return err
		}
		if _, err := s.openTerm(ctx, a.TermID); err != nil {
			return err
		}
		created, err = s.SubjectEnrollments.EnrollGroup(ctx, assignmentID)
		if err != nil {
			return err
		}
		return s.audit(ctx, p, "academic.subject_enrollments_bulk_created", "teaching_assignment", assignmentID, map[string]any{"created": created})
	})
	return created, err
}

// ListSubjectEnrollments returns the caller's view: everything for a DIRECTOR
// (optionally filtered), the enrollments of the assignments a teacher teaches,
// and a student's own. Tutors have no access through this endpoint.
func (s *Service) ListSubjectEnrollments(ctx context.Context, p iddomain.Principal, f ports.SubjectEnrollmentFilter) ([]domain.SubjectEnrollment, error) {
	if !p.HasAnyRole(iddomain.RoleDirector, iddomain.RoleTeacher, iddomain.RoleStudent) {
		return nil, domain.ErrForbidden
	}
	sc := scopeOf(p)
	sc.Tutor = false
	if !sc.All {
		f.StudentID = nil // a client-supplied student id never widens the scope
	}
	return s.SubjectEnrollments.List(ctx, sc, f)
}
