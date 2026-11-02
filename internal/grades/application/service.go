// Package application holds the grades use cases. Every method receives the
// authenticated principal and decides on role + relationship to the resource;
// the teacher's identity is never read from the request body.
package application

import (
	"context"

	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	"github.com/edutrak/backend/internal/grades/domain"
	"github.com/edutrak/backend/internal/grades/ports"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

type Deps struct {
	Grades   ports.Repository
	Academic ports.Academic
	Audit    ports.AuditRecorder
	Tx       ports.Transactor
	NewID    func() uuid.UUID
}

type Service struct{ Deps }

func NewService(d Deps) *Service { return &Service{d} }

func (s *Service) audit(ctx context.Context, p iddomain.Principal, action string, entityID uuid.UUID, meta map[string]any) error {
	actor := p.UserID
	return s.Audit.Record(ctx, auditdomain.Event{
		ActorUserID: &actor, Action: action, EntityType: "grade", EntityID: entityID.String(), Metadata: meta,
	})
}

// requireOwningTeacher allows writes only to the teacher of the enrollment's
// teaching assignment. DIRECTOR, TUTOR, STUDENT and ADMIN never write grades.
func requireOwningTeacher(p iddomain.Principal, ref *domain.SubjectEnrollmentRef) error {
	if p.HasRole(iddomain.RoleTeacher) && ref.TeacherID == p.UserID {
		return nil
	}
	return domain.ErrForbidden
}

type CaptureGradeInput struct {
	SubjectEnrollmentID uuid.UUID
	SubjectUnitID       uuid.UUID
	Score               string // decimal text, e.g. "89.99"
}

// CaptureGrade records the first grade of a unit for a student attempt.
func (s *Service) CaptureGrade(ctx context.Context, p iddomain.Principal, in CaptureGradeInput) (*domain.Grade, error) {
	if !p.HasRole(iddomain.RoleTeacher) {
		return nil, domain.ErrForbidden
	}
	score, err := domain.ParseScore(in.Score)
	if err != nil {
		return nil, err
	}
	g := &domain.Grade{
		ID: s.NewID(), SubjectEnrollmentID: in.SubjectEnrollmentID, SubjectUnitID: in.SubjectUnitID,
		Score: score, RecordedBy: p.UserID,
	}
	err = s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		ref, err := s.Academic.EnrollmentRef(ctx, in.SubjectEnrollmentID, true)
		if err != nil {
			return err
		}
		if err := requireOwningTeacher(p, ref); err != nil {
			return err
		}
		unit, err := s.Academic.Unit(ctx, in.SubjectUnitID)
		if err != nil {
			return err
		}
		if unit.SubjectID != ref.SubjectID {
			return domain.ErrUnitNotInSubject
		}
		if err := ref.RequireWritable(); err != nil {
			return err
		}
		if err := s.Grades.Create(ctx, g); err != nil {
			return err
		}
		return s.audit(ctx, p, "grades.created", g.ID, map[string]any{
			"subject_enrollment_id": g.SubjectEnrollmentID.String(), "subject_unit_id": g.SubjectUnitID.String(),
			"score": score.String(), "category": string(score.Category()),
		})
	})
	if err != nil {
		return nil, err
	}
	return g, nil
}

// UpdateGrade corrects an existing grade. Concurrent corrections of the same
// grade are serialized by a row lock; the last one wins and both are audited.
func (s *Service) UpdateGrade(ctx context.Context, p iddomain.Principal, id uuid.UUID, rawScore string) (*domain.Grade, error) {
	if !p.HasRole(iddomain.RoleTeacher) {
		return nil, domain.ErrForbidden
	}
	score, err := domain.ParseScore(rawScore)
	if err != nil {
		return nil, err
	}
	var out *domain.Grade
	err = s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		g, err := s.Grades.GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		ref, err := s.Academic.EnrollmentRef(ctx, g.SubjectEnrollmentID, true)
		if err != nil {
			return err
		}
		if err := requireOwningTeacher(p, ref); err != nil {
			return err
		}
		if err := ref.RequireWritable(); err != nil {
			return err
		}
		if g.Score == score { // nothing to correct: no write, no audit noise
			out = g
			return nil
		}
		updated, err := s.Grades.UpdateScore(ctx, id, score)
		if err != nil {
			return err
		}
		out = updated
		return s.audit(ctx, p, "grades.updated", id, map[string]any{
			"subject_enrollment_id": g.SubjectEnrollmentID.String(), "subject_unit_id": g.SubjectUnitID.String(),
			"from": g.Score.String(), "to": score.String(),
			"from_category": string(g.Score.Category()), "to_category": string(score.Category()),
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetGrade returns one grade to the student it belongs to, the teacher of its
// assignment, the tutor of its group, or a DIRECTOR.
func (s *Service) GetGrade(ctx context.Context, p iddomain.Principal, id uuid.UUID) (*domain.Grade, error) {
	if !p.HasAnyRole(iddomain.RoleDirector, iddomain.RoleTeacher, iddomain.RoleTutor, iddomain.RoleStudent) {
		return nil, domain.ErrForbidden
	}
	g, err := s.Grades.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	ref, err := s.Academic.EnrollmentRef(ctx, g.SubjectEnrollmentID, false)
	if err != nil {
		return nil, err
	}
	switch {
	case p.HasRole(iddomain.RoleDirector),
		p.HasRole(iddomain.RoleTeacher) && ref.TeacherID == p.UserID,
		p.HasRole(iddomain.RoleTutor) && ref.GroupTutorID != nil && *ref.GroupTutorID == p.UserID,
		p.HasRole(iddomain.RoleStudent) && ref.StudentID == p.UserID:
		return g, nil
	}
	return nil, domain.ErrForbidden
}

// ListStudentGrades returns every attempt of a student, with all units of each
// subject (pending units are listed without a score, never as zero).
//
//	STUDENT: only themselves · TEACHER: only attempts in their assignments ·
//	TUTOR: only attempts in groups they tutor · DIRECTOR: any student.
func (s *Service) ListStudentGrades(ctx context.Context, p iddomain.Principal, studentID uuid.UUID) ([]domain.StudentSheet, error) {
	director := p.HasRole(iddomain.RoleDirector)
	self := p.HasRole(iddomain.RoleStudent) && p.UserID == studentID
	scope := ports.ViewScope{All: director || self}
	if p.HasRole(iddomain.RoleTeacher) {
		id := p.UserID
		scope.TeacherID = &id
	}
	if p.HasRole(iddomain.RoleTutor) {
		id := p.UserID
		scope.TutorID = &id
	}
	if !scope.All && scope.TeacherID == nil && scope.TutorID == nil {
		return nil, domain.ErrForbidden // other students, ADMIN
	}
	var visible bool
	var err error
	switch {
	case self:
		visible = true
	case director:
		visible, err = s.Academic.StudentExists(ctx, studentID)
		if err == nil && !visible {
			return nil, domain.ErrStudentNotFound
		}
	default:
		visible, err = s.Academic.StudentVisible(ctx, studentID, scope)
	}
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, domain.ErrForbidden
	}
	return s.Grades.ListStudentSheets(ctx, studentID, scope)
}

// GetAssignmentGrades returns the students x units table of a teaching
// assignment to its teacher, the tutor of its group, or a DIRECTOR.
func (s *Service) GetAssignmentGrades(ctx context.Context, p iddomain.Principal, assignmentID uuid.UUID) (*domain.AssignmentSheet, error) {
	if !p.HasAnyRole(iddomain.RoleDirector, iddomain.RoleTeacher, iddomain.RoleTutor) {
		return nil, domain.ErrForbidden
	}
	owner, err := s.Academic.AssignmentOwner(ctx, assignmentID)
	if err != nil {
		return nil, err
	}
	switch {
	case p.HasRole(iddomain.RoleDirector),
		p.HasRole(iddomain.RoleTeacher) && owner.TeacherID == p.UserID,
		p.HasRole(iddomain.RoleTutor) && owner.GroupTutorID != nil && *owner.GroupTutorID == p.UserID:
		return s.Grades.AssignmentSheet(ctx, assignmentID)
	}
	return nil, domain.ErrForbidden
}
