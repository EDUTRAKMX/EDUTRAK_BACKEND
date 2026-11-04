package application

import (
	"context"

	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/tutoring/domain"
	"github.com/edutrak/backend/internal/tutoring/ports"
	"github.com/google/uuid"
)

// Students lists the enrollments (one row per student and group) the caller supervises:
// TUTOR the groups they tutor now, DIRECTOR all. ACTIVE enrollments unless a status is given.
func (s *Service) Students(ctx context.Context, p iddomain.Principal, f ports.StudentFilter) ([]domain.Enrollment, error) {
	scope, err := scopeOf(p)
	if err != nil {
		return nil, err
	}
	switch f.Status {
	case "":
		f.Status = "ACTIVE"
	case "ACTIVE", "WITHDRAWN":
	default:
		return nil, domain.ErrInvalidFilter
	}
	return s.Repo.Students(ctx, scope, f)
}

// resolve authorizes the caller over one student and returns the enrollment to use: the
// given group, or (without one) the most recent enrollment the caller may see. A student
// the caller has no relationship with is 403; a nonexistent one is 404.
func (s *Service) resolve(ctx context.Context, p iddomain.Principal, scope ports.Scope, studentID uuid.UUID, groupID *uuid.UUID) (*domain.Enrollment, error) {
	if groupID != nil {
		e, err := s.Repo.Context(ctx, studentID, *groupID, false)
		if err != nil {
			return nil, readContextErr(err)
		}
		if !canAccess(p, e.GroupTutorID) {
			return nil, domain.ErrForbidden
		}
		return e, nil
	}
	ok, err := s.Repo.StudentExists(ctx, studentID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrStudentNotFound
	}
	list, err := s.Repo.Enrollments(ctx, studentID, scope)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, domain.ErrForbidden
	}
	return &list[0], nil
}

// Overview is the academic picture of ONE student in ONE group (never mixing groups or
// periods). Without groupID the most recent enrollment the caller may see is used; the
// response always states which group and period it describes.
func (s *Service) Overview(ctx context.Context, p iddomain.Principal, studentID uuid.UUID, groupID *uuid.UUID) (*domain.Overview, error) {
	scope, err := scopeOf(p)
	if err != nil {
		return nil, err
	}
	e, err := s.resolve(ctx, p, scope, studentID, groupID)
	if err != nil {
		return nil, err
	}
	subjects, err := s.Overviews.Subjects(ctx, studentID, e.GroupID)
	if err != nil {
		return nil, err
	}
	if subjects == nil {
		subjects = []domain.SubjectOverview{}
	}
	return &domain.Overview{Enrollment: *e, Subjects: subjects}, nil
}

// StudentHistory lists the follow-ups of one student that the caller may read: those of
// groups the caller tutors NOW (whoever wrote them) or, for DIRECTOR, all. Newest first.
func (s *Service) StudentHistory(ctx context.Context, p iddomain.Principal, studentID uuid.UUID, groupID *uuid.UUID, page ports.Page) ([]domain.Entry, int, error) {
	scope, err := scopeOf(p)
	if err != nil {
		return nil, 0, err
	}
	if page, err = checkPage(page); err != nil {
		return nil, 0, err
	}
	if _, err := s.resolve(ctx, p, scope, studentID, groupID); err != nil {
		return nil, 0, err
	}
	return s.Repo.List(ctx, scope, ports.FollowUpFilter{StudentID: &studentID, GroupID: groupID}, page)
}

// ListFollowUps lists every follow-up in the caller's scope, with optional filters.
func (s *Service) ListFollowUps(ctx context.Context, p iddomain.Principal, f ports.FollowUpFilter, page ports.Page) ([]domain.Entry, int, error) {
	scope, err := scopeOf(p)
	if err != nil {
		return nil, 0, err
	}
	if page, err = checkPage(page); err != nil {
		return nil, 0, err
	}
	return s.Repo.List(ctx, scope, f, page)
}

// GetFollowUp returns one follow-up if the caller supervises its group now.
func (s *Service) GetFollowUp(ctx context.Context, p iddomain.Principal, id uuid.UUID) (*domain.Entry, error) {
	if _, err := scopeOf(p); err != nil {
		return nil, err
	}
	e, err := s.Repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !canAccess(p, e.GroupTutorID) {
		return nil, domain.ErrForbidden
	}
	return e, nil
}
