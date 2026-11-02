package application

import (
	"context"

	"github.com/edutrak/backend/internal/academic/domain"
	"github.com/edutrak/backend/internal/academic/ports"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

type CreateGroupInput struct {
	TermID   uuid.UUID
	CareerID uuid.UUID
	Code     string
	Level    int
	TutorID  *uuid.UUID
}

func (s *Service) CreateGroup(ctx context.Context, p iddomain.Principal, in CreateGroupInput) (*domain.Group, error) {
	if err := requireDirector(p); err != nil {
		return nil, err
	}
	code, err := domain.NewCode(in.Code)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateLevel(in.Level); err != nil {
		return nil, err
	}
	g := &domain.Group{ID: s.NewID(), TermID: in.TermID, CareerID: in.CareerID, TutorID: in.TutorID, Code: code, Level: int16(in.Level)}
	err = s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		if _, err := s.openTerm(ctx, in.TermID); err != nil {
			return asRef(err)
		}
		career, err := s.Catalog.GetCareer(ctx, in.CareerID)
		if err != nil {
			return asRef(err)
		}
		if !career.IsActive {
			return domain.ErrCareerInactive
		}
		if in.TutorID != nil {
			if err := s.requireUser(ctx, *in.TutorID, domain.RoleTutor, domain.ErrInvalidTutor); err != nil {
				return err
			}
		}
		if err := s.Groups.Create(ctx, g); err != nil {
			return err
		}
		return s.audit(ctx, p, "academic.group_created", "group", g.ID, map[string]any{
			"term_id": g.TermID.String(), "career_id": g.CareerID.String(), "code": g.Code, "level": g.Level, "tutor_id": idString(g.TutorID),
		})
	})
	if err != nil {
		return nil, err
	}
	// Re-read for the descriptive fields (term name, career code/name).
	return s.Groups.Get(ctx, g.ID)
}

// ListGroups returns the groups the caller is related to: all of them for a
// DIRECTOR; otherwise those the user teaches in, tutors, or is enrolled in.
func (s *Service) ListGroups(ctx context.Context, p iddomain.Principal, termID *uuid.UUID) ([]domain.Group, error) {
	if err := requireAcademicRole(p); err != nil {
		return nil, err
	}
	return s.Groups.List(ctx, scopeOf(p), ports.GroupFilter{TermID: termID})
}

// authorizeGroup allows a DIRECTOR, the group's tutor, a teacher with an
// assignment in the group, or a student actively enrolled in it.
func (s *Service) authorizeGroup(ctx context.Context, p iddomain.Principal, g *domain.Group) error {
	if p.HasRole(iddomain.RoleDirector) {
		return nil
	}
	if p.HasRole(iddomain.RoleTutor) && g.TutorID != nil && *g.TutorID == p.UserID {
		return nil
	}
	if p.HasRole(iddomain.RoleTeacher) {
		ok, err := s.Groups.TeachesInGroup(ctx, g.ID, p.UserID)
		if err != nil || ok {
			return err
		}
	}
	if p.HasRole(iddomain.RoleStudent) {
		ok, err := s.Groups.IsEnrolled(ctx, g.ID, p.UserID)
		if err != nil || ok {
			return err
		}
	}
	return domain.ErrForbidden
}

func (s *Service) GetGroup(ctx context.Context, p iddomain.Principal, id uuid.UUID) (*domain.Group, error) {
	if err := requireAcademicRole(p); err != nil {
		return nil, err
	}
	g, err := s.Groups.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeGroup(ctx, p, g); err != nil {
		return nil, err
	}
	return g, nil
}

// SetGroupTutor assigns, changes or (with nil) removes the tutor of a group.
func (s *Service) SetGroupTutor(ctx context.Context, p iddomain.Principal, groupID uuid.UUID, tutorID *uuid.UUID) (*domain.Group, error) {
	if err := requireDirector(p); err != nil {
		return nil, err
	}
	err := s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		g, err := s.Groups.Get(ctx, groupID)
		if err != nil {
			return err
		}
		if _, err := s.openTerm(ctx, g.TermID); err != nil {
			return err
		}
		if tutorID != nil {
			if err := s.requireUser(ctx, *tutorID, domain.RoleTutor, domain.ErrInvalidTutor); err != nil {
				return err
			}
		}
		if err := s.Groups.SetTutor(ctx, groupID, tutorID); err != nil {
			return err
		}
		return s.audit(ctx, p, "academic.group_tutor_changed", "group", groupID, map[string]any{
			"from": idString(g.TutorID), "to": idString(tutorID),
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Groups.Get(ctx, groupID)
}

// ListGroupEnrollments lists the students of a group; only a DIRECTOR or the
// group's own tutor may see it (teachers use their assignment's student list).
func (s *Service) ListGroupEnrollments(ctx context.Context, p iddomain.Principal, groupID uuid.UUID) ([]domain.Enrollment, error) {
	if err := requireAcademicRole(p); err != nil {
		return nil, err
	}
	g, err := s.Groups.Get(ctx, groupID)
	if err != nil {
		return nil, err
	}
	isTutor := p.HasRole(iddomain.RoleTutor) && g.TutorID != nil && *g.TutorID == p.UserID
	if !p.HasRole(iddomain.RoleDirector) && !isTutor {
		return nil, domain.ErrForbidden
	}
	return s.Groups.ListEnrollments(ctx, groupID)
}

func (s *Service) EnrollStudent(ctx context.Context, p iddomain.Principal, groupID, studentID uuid.UUID) (*domain.Enrollment, error) {
	if err := requireDirector(p); err != nil {
		return nil, err
	}
	var e *domain.Enrollment
	err := s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		g, err := s.Groups.Get(ctx, groupID)
		if err != nil {
			return err
		}
		if _, err := s.openTerm(ctx, g.TermID); err != nil {
			return err
		}
		if err := s.requireUser(ctx, studentID, domain.RoleStudent, domain.ErrInvalidStudent); err != nil {
			return err
		}
		e = &domain.Enrollment{ID: s.NewID(), GroupID: groupID, StudentID: studentID, Status: domain.EnrollmentActive}
		if err := s.Groups.AddEnrollment(ctx, e); err != nil {
			return err
		}
		return s.audit(ctx, p, "academic.student_enrolled", "enrollment", e.ID, map[string]any{
			"group_id": groupID.String(), "student_id": studentID.String(),
		})
	})
	if err != nil {
		return nil, err
	}
	return e, nil
}
