package application

import (
	"context"

	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/justifications/domain"
	"github.com/edutrak/backend/internal/justifications/ports"
	"github.com/google/uuid"
)

type DecisionInput struct {
	Decision string // APPROVED | REJECTED
	Comment  string
}

// DecideAsTutor is the first review: only the tutor CURRENTLY assigned to the student's group.
func (s *Service) DecideAsTutor(ctx context.Context, p iddomain.Principal, id uuid.UUID, in DecisionInput) (*domain.Detail, error) {
	return s.decide(ctx, p, id, domain.StageTutor, in)
}

// DecideAsTeacher is the final review: only the teacher of the assignment, and only after the tutor approved.
func (s *Service) DecideAsTeacher(ctx context.Context, p iddomain.Principal, id uuid.UUID, in DecisionInput) (*domain.Detail, error) {
	return s.decide(ctx, p, id, domain.StageTeacher, in)
}

func stageRole(st domain.Stage) iddomain.Role {
	if st == domain.StageTutor {
		return iddomain.RoleTutor
	}
	return iddomain.RoleTeacher
}

func actionName(st domain.Stage, d domain.Decision) string {
	who := "tutor"
	if st == domain.StageTeacher {
		who = "teacher"
	}
	if d == domain.DecisionApproved {
		return "justifications." + who + "_approved"
	}
	return "justifications." + who + "_rejected"
}

// decide runs one review as a single transaction. Lock order, shared with every other
// writer of this flow and with Attendance's corrections, is always:
//
//  1. the attendance record row   (FOR UPDATE)
//  2. the justification row       (FOR UPDATE)
//
// so concurrent decisions, requests and corrections serialize instead of deadlocking.
// The request, its review, the attendance record (on final approval) and the audit
// rows commit together or not at all.
func (s *Service) decide(ctx context.Context, p iddomain.Principal, id uuid.UUID, stage domain.Stage, in DecisionInput) (*domain.Detail, error) {
	if !p.HasRole(stageRole(stage)) {
		return nil, domain.ErrForbidden
	}
	decision, err := domain.ParseDecision(in.Decision)
	if err != nil {
		return nil, err
	}
	review, err := domain.NewReview(s.NewID(), id, p.UserID, stage, decision, in.Comment)
	if err != nil {
		return nil, err
	}
	err = s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		peek, err := s.Repo.GetCase(ctx, id, false) // only to learn which attendance record to lock first
		if err != nil {
			return err
		}
		abs, err := s.Attendance.Absence(ctx, peek.AttendanceRecordID, true)
		if err != nil {
			return err
		}
		c, err := s.Repo.GetCase(ctx, id, true)
		if err != nil {
			return err
		}
		// Relationship FIRST (a stranger learns nothing about the request's state)...
		if !isReviewer(p, stage, abs) {
			return domain.ErrForbidden
		}
		// ...then the state machine...
		next, err := c.Status.Next(stage, decision)
		if err != nil {
			return err
		}
		// ...then the facts: the term is open, and an approval needs an absence that is STILL an absence.
		if abs.TermStatus == "closed" {
			return domain.ErrTermClosed
		}
		if decision == domain.DecisionApproved && abs.Status != "ABSENT" {
			return domain.ErrAbsenceNotEligible
		}
		changed, err := s.Repo.UpdateStatus(ctx, id, c.Status, next)
		if err != nil {
			return err
		}
		if !changed {
			return domain.ErrWrongStage // lost a race we did not lock against; fail safe
		}
		if err := s.Repo.AddReview(ctx, review); err != nil {
			return err
		}
		if err := s.audit(ctx, p, actionName(stage, decision), "justification_request", id, map[string]any{
			"attendance_record_id": abs.RecordID.String(), "stage": string(stage), "decision": string(decision),
			"from": string(c.Status), "to": string(next),
		}); err != nil {
			return err
		}
		if next == domain.StatusJustified {
			// The one and only place where an attendance record becomes JUSTIFIED.
			if err := s.Attendance.MarkJustified(ctx, abs.RecordID); err != nil {
				return err
			}
			return s.audit(ctx, p, "attendance.justified", "attendance_record", abs.RecordID, map[string]any{
				"justification_request_id": id.String(), "from": "ABSENT", "to": "JUSTIFIED",
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Repo.Detail(ctx, id)
}

// isReviewer checks the CURRENT relationship: the group's tutor / the assignment's teacher.
func isReviewer(p iddomain.Principal, stage domain.Stage, a *ports.Absence) bool {
	if stage == domain.StageTutor {
		return p.HasRole(iddomain.RoleTutor) && a.GroupTutorID != nil && *a.GroupTutorID == p.UserID
	}
	return p.HasRole(iddomain.RoleTeacher) && a.TeacherID == p.UserID
}
