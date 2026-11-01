package application

import (
	"context"

	"github.com/edutrak/backend/internal/academic/domain"
	"github.com/edutrak/backend/internal/academic/ports"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

type CreateAssignmentInput struct {
	SubjectID uuid.UUID
	TeacherID uuid.UUID
}

// CreateAssignment assigns a subject of the group's curriculum to a teacher.
func (s *Service) CreateAssignment(ctx context.Context, p iddomain.Principal, groupID uuid.UUID, in CreateAssignmentInput) (*domain.TeachingAssignment, error) {
	if err := requireDirector(p); err != nil {
		return nil, err
	}
	a := &domain.TeachingAssignment{ID: s.NewID(), GroupID: groupID, SubjectID: in.SubjectID, TeacherID: in.TeacherID}
	err := s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		g, err := s.Groups.Get(ctx, groupID)
		if err != nil {
			return err
		}
		if _, err := s.openTerm(ctx, g.TermID); err != nil {
			return err
		}
		subject, err := s.Catalog.GetSubject(ctx, in.SubjectID)
		if err != nil {
			return asRef(err)
		}
		if !subject.IsActive {
			return domain.ErrSubjectInactive
		}
		inPlan, err := s.Catalog.InCurriculum(ctx, g.CareerID, in.SubjectID, g.Level)
		if err != nil {
			return err
		}
		if !inPlan {
			return domain.ErrNotInCurriculum
		}
		if err := s.requireUser(ctx, in.TeacherID, domain.RoleTeacher, domain.ErrInvalidTeacher); err != nil {
			return err
		}
		if err := s.Assignments.Create(ctx, a); err != nil {
			return err
		}
		return s.audit(ctx, p, "academic.teaching_assignment_created", "teaching_assignment", a.ID, map[string]any{
			"group_id": groupID.String(), "subject_id": in.SubjectID.String(), "teacher_id": in.TeacherID.String(),
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Assignments.Get(ctx, a.ID)
}

// ChangeTeacher replaces the teacher of an assignment.
func (s *Service) ChangeTeacher(ctx context.Context, p iddomain.Principal, id, teacherID uuid.UUID) (*domain.TeachingAssignment, error) {
	if err := requireDirector(p); err != nil {
		return nil, err
	}
	err := s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		a, err := s.Assignments.GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if _, err := s.openTerm(ctx, a.TermID); err != nil {
			return err
		}
		if err := s.requireUser(ctx, teacherID, domain.RoleTeacher, domain.ErrInvalidTeacher); err != nil {
			return err
		}
		if err := s.Assignments.SetTeacher(ctx, id, teacherID); err != nil {
			return err
		}
		return s.audit(ctx, p, "academic.teacher_changed", "teaching_assignment", id, map[string]any{
			"from": a.TeacherID.String(), "to": teacherID.String(),
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Assignments.Get(ctx, id)
}

// authorizeAssignment allows a DIRECTOR, the assignment's teacher, the tutor of
// its group, or a student with a subject enrollment in it.
func (s *Service) authorizeAssignment(ctx context.Context, p iddomain.Principal, a *domain.TeachingAssignment) error {
	if p.HasRole(iddomain.RoleDirector) {
		return nil
	}
	if p.HasRole(iddomain.RoleTeacher) && a.TeacherID == p.UserID {
		return nil
	}
	if p.HasRole(iddomain.RoleTutor) && a.GroupTutorID != nil && *a.GroupTutorID == p.UserID {
		return nil
	}
	if p.HasRole(iddomain.RoleStudent) {
		ok, err := s.Assignments.HasSubjectEnrollment(ctx, a.ID, p.UserID)
		if err != nil || ok {
			return err
		}
	}
	return domain.ErrForbidden
}

// loadAssignment fetches an assignment and applies authorizeAssignment.
func (s *Service) loadAssignment(ctx context.Context, p iddomain.Principal, id uuid.UUID) (*domain.TeachingAssignment, error) {
	if err := requireAcademicRole(p); err != nil {
		return nil, err
	}
	a, err := s.Assignments.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeAssignment(ctx, p, a); err != nil {
		return nil, err
	}
	return a, nil
}

func (s *Service) GetAssignment(ctx context.Context, p iddomain.Principal, id uuid.UUID) (*domain.TeachingAssignment, error) {
	return s.loadAssignment(ctx, p, id)
}

// ListAssignments returns the assignments the caller is related to, optionally
// restricted to one group (which the caller must be allowed to see).
func (s *Service) ListAssignments(ctx context.Context, p iddomain.Principal, groupID *uuid.UUID) ([]domain.TeachingAssignment, error) {
	if err := requireAcademicRole(p); err != nil {
		return nil, err
	}
	if groupID != nil {
		if _, err := s.GetGroup(ctx, p, *groupID); err != nil {
			return nil, err
		}
	}
	return s.Assignments.List(ctx, scopeOf(p), ports.AssignmentFilter{GroupID: groupID})
}

// ListAssignmentStudents lists who is enrolled in an assignment. Only the
// DIRECTOR and the assignment's own teacher may see it.
func (s *Service) ListAssignmentStudents(ctx context.Context, p iddomain.Principal, id uuid.UUID) ([]domain.SubjectEnrollment, error) {
	if err := requireAcademicRole(p); err != nil {
		return nil, err
	}
	a, err := s.Assignments.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !p.HasRole(iddomain.RoleDirector) && !(p.HasRole(iddomain.RoleTeacher) && a.TeacherID == p.UserID) {
		return nil, domain.ErrForbidden
	}
	return s.SubjectEnrollments.ListByAssignment(ctx, id)
}

type CreateScheduleInput struct {
	DayOfWeek int
	StartTime string
	EndTime   string
	Classroom string
}

func (s *Service) CreateSchedule(ctx context.Context, p iddomain.Principal, assignmentID uuid.UUID, in CreateScheduleInput) (*domain.Schedule, error) {
	if err := requireDirector(p); err != nil {
		return nil, err
	}
	sch, err := domain.NewSchedule(s.NewID(), assignmentID, in.DayOfWeek, in.StartTime, in.EndTime, in.Classroom)
	if err != nil {
		return nil, err
	}
	err = s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		a, err := s.Assignments.GetForUpdate(ctx, assignmentID) // serializes concurrent inserts for this assignment
		if err != nil {
			return err
		}
		if _, err := s.openTerm(ctx, a.TermID); err != nil {
			return err
		}
		existing, err := s.Assignments.ListSchedules(ctx, assignmentID)
		if err != nil {
			return err
		}
		for _, e := range existing {
			if sch.Overlaps(e) {
				return domain.ErrScheduleOverlap
			}
		}
		return s.Assignments.CreateSchedule(ctx, sch)
	})
	if err != nil {
		return nil, err
	}
	return sch, nil
}

func (s *Service) ListAssignmentSchedules(ctx context.Context, p iddomain.Principal, assignmentID uuid.UUID) ([]domain.Schedule, error) {
	if _, err := s.loadAssignment(ctx, p, assignmentID); err != nil {
		return nil, err
	}
	return s.Assignments.ListSchedules(ctx, assignmentID)
}

// ListSchedules is the caller's own timetable: every slot of the assignments
// the caller teaches, tutors, or is subject-enrolled in (all for a DIRECTOR).
func (s *Service) ListSchedules(ctx context.Context, p iddomain.Principal, groupID *uuid.UUID) ([]domain.ScheduleEntry, error) {
	if err := requireAcademicRole(p); err != nil {
		return nil, err
	}
	return s.Assignments.ListScheduleEntries(ctx, scopeOf(p), ports.AssignmentFilter{GroupID: groupID})
}
