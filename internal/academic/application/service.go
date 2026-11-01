// Package application holds the academic use cases. Every method receives the
// authenticated principal and decides on role + relationship to the resource;
// ids of the acting teacher, tutor or student are never taken from the client.
package application

import (
	"context"

	"github.com/edutrak/backend/internal/academic/domain"
	"github.com/edutrak/backend/internal/academic/ports"
	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/apperr"
	"github.com/google/uuid"
)

type Deps struct {
	Catalog            ports.Catalog
	Terms              ports.Terms
	Groups             ports.Groups
	Assignments        ports.Assignments
	SubjectEnrollments ports.SubjectEnrollments
	Users              ports.UserDirectory
	Audit              ports.AuditRecorder
	Tx                 ports.Transactor
	NewID              func() uuid.UUID
}

type Service struct{ Deps }

func NewService(d Deps) *Service { return &Service{d} }

// --- authorization helpers -------------------------------------------------------

func requireDirector(p iddomain.Principal) error {
	if !p.HasRole(iddomain.RoleDirector) {
		return domain.ErrForbidden
	}
	return nil
}

// requireAcademicRole rejects accounts whose only roles are non-academic (ADMIN):
// administering accounts does not grant access to academic records.
func requireAcademicRole(p iddomain.Principal) error {
	if p.HasAnyRole(iddomain.RoleDirector, iddomain.RoleTeacher, iddomain.RoleTutor, iddomain.RoleStudent) {
		return nil
	}
	return domain.ErrForbidden
}

func scopeOf(p iddomain.Principal) ports.Scope {
	return ports.Scope{
		UserID:  p.UserID,
		All:     p.HasRole(iddomain.RoleDirector),
		Teacher: p.HasRole(iddomain.RoleTeacher),
		Tutor:   p.HasRole(iddomain.RoleTutor),
		Student: p.HasRole(iddomain.RoleStudent),
	}
}

// asRef turns "not found" of an entity referenced from a request body into a
// 422: the URL was fine, the payload pointed at something that does not exist.
func asRef(err error) error {
	if ae, ok := apperr.As(err); ok && ae.Kind == apperr.KindNotFound {
		return apperr.New(apperr.KindValidation, ae.Code, ae.Message)
	}
	return err
}

// requireUser checks that a referenced user exists, is active and holds role.
func (s *Service) requireUser(ctx context.Context, id uuid.UUID, role string, invalid error) error {
	u, err := s.Users.Find(ctx, id)
	if err != nil {
		return err
	}
	if !u.IsActive || !u.HasRole(role) {
		return invalid
	}
	return nil
}

// openTerm loads a term for a dependent write, locking it against a concurrent
// close, and rejects closed terms.
func (s *Service) openTerm(ctx context.Context, id uuid.UUID) (*domain.Term, error) {
	t, err := s.Terms.GetForShare(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := t.RequireOpen(); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Service) audit(ctx context.Context, p iddomain.Principal, action, entityType string, entityID uuid.UUID, meta map[string]any) error {
	actor := p.UserID
	return s.Audit.Record(ctx, auditdomain.Event{
		ActorUserID: &actor, Action: action, EntityType: entityType, EntityID: entityID.String(), Metadata: meta,
	})
}

func idString(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}
