package application

import (
	"context"
	"time"

	"github.com/edutrak/backend/internal/academic/domain"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

type CreateTermInput struct {
	Name      string
	StartDate time.Time
	EndDate   time.Time
}

func (s *Service) CreateTerm(ctx context.Context, p iddomain.Principal, in CreateTermInput) (*domain.Term, error) {
	if err := requireDirector(p); err != nil {
		return nil, err
	}
	t, err := domain.NewTerm(s.NewID(), in.Name, in.StartDate, in.EndDate)
	if err != nil {
		return nil, err
	}
	err = s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.Terms.Create(ctx, t); err != nil {
			return err
		}
		return s.audit(ctx, p, "academic.term_created", "academic_term", t.ID, map[string]any{"name": t.Name})
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Service) ListTerms(ctx context.Context, p iddomain.Principal) ([]domain.Term, error) {
	if err := requireDirector(p); err != nil {
		return nil, err
	}
	return s.Terms.List(ctx)
}

func (s *Service) GetTerm(ctx context.Context, p iddomain.Principal, id uuid.UUID) (*domain.Term, error) {
	if err := requireDirector(p); err != nil {
		return nil, err
	}
	return s.Terms.Get(ctx, id)
}

func (s *Service) ActivateTerm(ctx context.Context, p iddomain.Principal, id uuid.UUID) (*domain.Term, error) {
	return s.changeTermStatus(ctx, p, id, domain.TermActive, "academic.term_activated")
}

func (s *Service) CloseTerm(ctx context.Context, p iddomain.Principal, id uuid.UUID) (*domain.Term, error) {
	return s.changeTermStatus(ctx, p, id, domain.TermClosed, "academic.term_closed")
}

func (s *Service) changeTermStatus(ctx context.Context, p iddomain.Principal, id uuid.UUID, target domain.TermStatus, action string) (*domain.Term, error) {
	if err := requireDirector(p); err != nil {
		return nil, err
	}
	var out *domain.Term
	err := s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		t, err := s.Terms.Get(ctx, id)
		if err != nil {
			return err
		}
		next, err := t.Transition(target)
		if err != nil {
			return err
		}
		changed, err := s.Terms.UpdateStatus(ctx, id, t.Status, next)
		if err != nil {
			return err
		}
		if !changed { // someone else moved the term between our read and write
			return domain.ErrInvalidTermTransition
		}
		prev := t.Status
		t.Status = next
		out = t
		return s.audit(ctx, p, action, "academic_term", t.ID, map[string]any{"from": string(prev), "to": string(next)})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
