// Package application holds the analytics use cases. Every method receives the authenticated
// principal and decides on role + CURRENT relationship (groups.tutor_id is read at the moment
// of each call, so a change of tutor takes effect immediately). Only TUTOR (own groups) and
// DIRECTOR use Analytics; ADMIN, TEACHER and STUDENT are refused.
package application

import (
	"context"

	"github.com/edutrak/backend/internal/analytics/domain"
	"github.com/edutrak/backend/internal/analytics/ports"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

const (
	DefaultLimit = 50
	MaxLimit     = 200
)

type Deps struct {
	Facts  ports.Facts
	Alerts ports.Alerts
	Audit  ports.AuditRecorder
	Tx     ports.Transactor
	Clock  ports.Clock
	NewID  func() uuid.UUID
	Rules  domain.Rules
}

type Service struct{ Deps }

func NewService(d Deps) *Service {
	if !d.Rules.Valid() {
		d.Rules = domain.DefaultRules()
	}
	return &Service{d}
}

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

func checkPage(p ports.Page) (ports.Page, error) {
	if p.Limit == 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit < 1 || p.Limit > MaxLimit || p.Offset < 0 {
		return p, domain.ErrInvalidPaging
	}
	return p, nil
}

// --- student ------------------------------------------------------------------------------------------

type StudentView struct {
	Enrollment domain.EnrollmentRef
	Analytics  domain.StudentAnalytics
	// Alerts are the PERSISTED unresolved alerts; they are as fresh as the last recalculation.
	// Signals inside Analytics are computed live from the current data.
	Alerts []domain.AlertView
}

// resolve authorizes the caller over one student and picks the enrollment: the given group, or
// (without one) the most recent enrollment the caller may see.
func (s *Service) resolve(ctx context.Context, p iddomain.Principal, scope ports.Scope, studentID uuid.UUID, groupID *uuid.UUID) (*domain.EnrollmentRef, error) {
	ok, err := s.Facts.StudentExists(ctx, studentID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrStudentNotFound
	}
	if groupID != nil {
		e, err := s.Facts.Enrollment(ctx, studentID, *groupID)
		if err != nil {
			return nil, err
		}
		if !canAccess(p, e.TutorID) {
			return nil, domain.ErrForbidden
		}
		return e, nil
	}
	list, err := s.Facts.Enrollments(ctx, studentID, scope)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, domain.ErrForbidden
	}
	return &list[0], nil
}

// GetStudentAnalytics returns the verifiable indicators of one student in ONE group/period.
func (s *Service) GetStudentAnalytics(ctx context.Context, p iddomain.Principal, studentID uuid.UUID, groupID *uuid.UUID) (*StudentView, error) {
	scope, err := scopeOf(p)
	if err != nil {
		return nil, err
	}
	e, err := s.resolve(ctx, p, scope, studentID, groupID)
	if err != nil {
		return nil, err
	}
	facts, err := s.Facts.Students(ctx, e.ID, &studentID)
	if err != nil {
		return nil, err
	}
	if len(facts) != 1 {
		return nil, domain.ErrContextNotFound
	}
	alerts, _, err := s.Alerts.List(ctx, scope, ports.AlertFilter{StudentID: &studentID, GroupID: &e.ID}, ports.Page{Limit: MaxLimit})
	if err != nil {
		return nil, err
	}
	return &StudentView{Enrollment: *e, Analytics: domain.Analyze(e.GroupInfo, facts[0], s.Rules), Alerts: alerts}, nil
}

// --- group --------------------------------------------------------------------------------------------

type GroupView struct {
	domain.GroupAnalytics
	OpenAlerts map[domain.Rule]int
}

func (s *Service) authorizeGroup(ctx context.Context, p iddomain.Principal, groupID uuid.UUID, lock bool) (*domain.GroupInfo, error) {
	if _, err := scopeOf(p); err != nil {
		return nil, err
	}
	g, err := s.Facts.Group(ctx, groupID, lock)
	if err != nil {
		return nil, err
	}
	if !canAccess(p, g.TutorID) {
		return nil, domain.ErrForbidden
	}
	return g, nil
}

// GetGroupAnalytics aggregates the ACTIVE students of one group.
func (s *Service) GetGroupAnalytics(ctx context.Context, p iddomain.Principal, groupID uuid.UUID) (*GroupView, error) {
	g, err := s.authorizeGroup(ctx, p, groupID, false)
	if err != nil {
		return nil, err
	}
	students, err := s.Facts.Students(ctx, groupID, nil)
	if err != nil {
		return nil, err
	}
	counts, err := s.Alerts.OpenCounts(ctx, groupID)
	if err != nil {
		return nil, err
	}
	return &GroupView{GroupAnalytics: domain.AnalyzeGroup(*g, students, s.Rules), OpenAlerts: counts}, nil
}

// --- overview -----------------------------------------------------------------------------------------

// Overview lists one aggregated line per group inside the caller's scope: a DIRECTOR sees every
// group (the model has no relation between a director and a career, so none is invented), a
// TUTOR only the groups they tutor now.
func (s *Service) Overview(ctx context.Context, p iddomain.Principal, f ports.OverviewFilter) ([]domain.GroupOverview, error) {
	scope, err := scopeOf(p)
	if err != nil {
		return nil, err
	}
	return s.Facts.Overview(ctx, scope, f)
}

// --- alerts (reads) -----------------------------------------------------------------------------------

func (s *Service) ListAlerts(ctx context.Context, p iddomain.Principal, f ports.AlertFilter, page ports.Page) ([]domain.AlertView, int, error) {
	scope, err := scopeOf(p)
	if err != nil {
		return nil, 0, err
	}
	if page, err = checkPage(page); err != nil {
		return nil, 0, err
	}
	switch f.Status {
	case "", "ALL", string(domain.AlertOpen), string(domain.AlertAcknowledged), string(domain.AlertResolved):
	default:
		return nil, 0, domain.ErrInvalidFilter
	}
	if f.Rule != "" && !domain.Rule(f.Rule).Valid() {
		return nil, 0, domain.ErrInvalidFilter
	}
	return s.Alerts.List(ctx, scope, f, page)
}

func (s *Service) GetAlert(ctx context.Context, p iddomain.Principal, id uuid.UUID) (*domain.AlertView, error) {
	if _, err := scopeOf(p); err != nil {
		return nil, err
	}
	a, err := s.Alerts.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !canAccess(p, a.GroupTutorID) {
		return nil, domain.ErrForbidden
	}
	return a, nil
}
