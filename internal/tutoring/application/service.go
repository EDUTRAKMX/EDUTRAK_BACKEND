// Package application holds the tutoring use cases. Every method receives the
// authenticated principal and decides on role + CURRENT relationship: the tutor of a
// follow-up's group is read from groups.tutor_id at the moment of each operation, so a
// change of tutor takes effect immediately. The author of a follow-up never comes from
// the request, and note contents never reach logs or audit metadata.
package application

import (
	"context"
	"errors"
	"time"

	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/tutoring/domain"
	"github.com/edutrak/backend/internal/tutoring/ports"
	"github.com/google/uuid"
)

const (
	DefaultLimit = 50
	MaxLimit     = 200
)

type Deps struct {
	Repo      ports.Repository
	Overviews ports.Overviews
	Audit     ports.AuditRecorder
	Tx        ports.Transactor
	Clock     ports.Clock
	NewID     func() uuid.UUID
	// Location dates follow-ups: "today" is the calendar date in the institution timezone.
	Location *time.Location
}

type Service struct{ Deps }

func NewService(d Deps) *Service {
	if d.Location == nil {
		d.Location = time.UTC
	}
	return &Service{d}
}

// --- authorization helpers ------------------------------------------------------------------

// scopeOf: DIRECTOR sees every group, TUTOR the groups they tutor NOW. The role alone is
// never enough for a TUTOR: every query is further restricted by groups.tutor_id = user.
// Anyone else (ADMIN, TEACHER, STUDENT) has no tutoring access.
func scopeOf(p iddomain.Principal) (ports.Scope, error) {
	id := p.UserID
	switch {
	case p.HasRole(iddomain.RoleDirector):
		return ports.Scope{All: true}, nil
	case p.HasRole(iddomain.RoleTutor):
		return ports.Scope{TutorID: &id}, nil
	}
	return ports.Scope{}, domain.ErrForbidden
}

func canAccess(p iddomain.Principal, tutorID *uuid.UUID) bool {
	if p.HasRole(iddomain.RoleDirector) {
		return true
	}
	return p.HasRole(iddomain.RoleTutor) && tutorID != nil && *tutorID == p.UserID
}

// readContextErr turns the "body reference" errors of Repository.Context into 404s, for
// reads where the ids come from the path or the query string.
func readContextErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrBodyStudentNotFound):
		return domain.ErrStudentNotFound
	case errors.Is(err, domain.ErrBodyGroupNotFound), errors.Is(err, domain.ErrNotEnrolled):
		return domain.ErrContextNotFound
	}
	return err
}

func (s *Service) today() time.Time {
	n := s.Clock.Now().In(s.Location)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

func checkPage(p ports.Page) (ports.Page, error) {
	if p.Limit == 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit < 1 || p.Limit > MaxLimit || p.Offset < 0 {
		return p, domain.ErrInvalidPaging
	}
	return p, nil
}

// --- C. create ------------------------------------------------------------------------------------

type CreateInput struct {
	StudentID uuid.UUID
	GroupID   uuid.UUID
	Date      *time.Time // nil = today in the institution timezone
	Note      string
}

// CreateFollowUp records a new tutoring follow-up. Only the TUTOR of the student's group may
// write (DIRECTOR supervises but does not write). Inside ONE transaction the group and its term
// are locked FOR SHARE, the relationship is re-checked, and the row and its audit event are
// stored; if the audit fails nothing is saved.
func (s *Service) CreateFollowUp(ctx context.Context, p iddomain.Principal, in CreateInput) (*domain.Entry, error) {
	if !p.HasRole(iddomain.RoleTutor) {
		return nil, domain.ErrForbidden
	}
	note, err := domain.NewNote(in.Note)
	if err != nil {
		return nil, err
	}
	today := s.today()
	date := today
	if in.Date != nil {
		date = *in.Date
	}

	var out *domain.Entry
	err = s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		e, err := s.Repo.Context(ctx, in.StudentID, in.GroupID, true)
		if err != nil {
			return err
		}
		if !canAccess(p, e.GroupTutorID) {
			return domain.ErrForbidden
		}
		if err := e.RequireWritable(); err != nil {
			return err
		}
		if err := domain.CheckFollowUpDate(date, today, e.TermStart); err != nil {
			return err
		}
		f, err := domain.New(s.NewID(), in.StudentID, p.UserID, in.GroupID, date, note)
		if err != nil {
			return err
		}
		if err := s.Repo.Create(ctx, f); err != nil {
			return err
		}
		actor := p.UserID
		if err := s.Audit.Record(ctx, auditdomain.Event{
			ActorUserID: &actor, Action: "tutoring.followup_created", EntityType: "tutoring_followup", EntityID: f.ID.String(),
			Metadata: map[string]any{"student_id": f.StudentID.String(), "group_id": f.GroupID.String(), "followup_date": date.Format(domain.DateLayout)},
		}); err != nil {
			return err
		}
		out, err = s.Repo.Get(ctx, f.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
