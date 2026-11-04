package application

import (
	"context"
	"io"

	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/justifications/domain"
	"github.com/edutrak/backend/internal/justifications/ports"
	"github.com/google/uuid"
)

// List returns the requests the caller is related to, filtered in the backend:
// STUDENT their own · TUTOR students of groups they tutor · TEACHER absences of
// assignments they teach · DIRECTOR all · ADMIN none (403). Roles add up.
func (s *Service) List(ctx context.Context, p iddomain.Principal, status string) ([]domain.Case, error) {
	if !hasAcademicRole(p) {
		return nil, domain.ErrForbidden
	}
	var filter *domain.Status
	if status != "" {
		st, err := domain.ParseStatus(status)
		if err != nil {
			return nil, err
		}
		filter = &st
	}
	scope := ports.ViewScope{All: p.HasRole(iddomain.RoleDirector)}
	id := p.UserID
	if p.HasRole(iddomain.RoleStudent) {
		scope.StudentID = &id
	}
	if p.HasRole(iddomain.RoleTeacher) {
		scope.TeacherID = &id
	}
	if p.HasRole(iddomain.RoleTutor) {
		scope.TutorID = &id
	}
	return s.Repo.List(ctx, scope, filter)
}

// Get returns one request with its evidence metadata and decision history.
func (s *Service) Get(ctx context.Context, p iddomain.Principal, id uuid.UUID) (*domain.Detail, error) {
	if !hasAcademicRole(p) {
		return nil, domain.ErrForbidden
	}
	d, err := s.Repo.Detail(ctx, id)
	if err != nil {
		return nil, err
	}
	if !canView(p, &d.Case) {
		return nil, domain.ErrForbidden
	}
	return d, nil
}

// OpenEvidence streams a file to someone entitled to see the request. The relationship is
// re-evaluated on EVERY call, so losing access (a new tutor, a reassigned teacher) is immediate.
func (s *Service) OpenEvidence(ctx context.Context, p iddomain.Principal, requestID, evidenceID uuid.UUID) (*domain.Evidence, io.ReadCloser, error) {
	if !hasAcademicRole(p) {
		return nil, nil, domain.ErrForbidden
	}
	c, err := s.Repo.GetCase(ctx, requestID, false)
	if err != nil {
		return nil, nil, err
	}
	if !canView(p, c) {
		return nil, nil, domain.ErrForbidden
	}
	ev, err := s.Repo.Evidence(ctx, requestID, evidenceID) // it must belong to THIS request
	if err != nil {
		return nil, nil, err
	}
	rc, err := s.Storage.Open(ctx, ev.StorageKey)
	if err != nil {
		return nil, nil, err // metadata without a file is an internal inconsistency: 500, details only in logs
	}
	return ev, rc, nil
}
