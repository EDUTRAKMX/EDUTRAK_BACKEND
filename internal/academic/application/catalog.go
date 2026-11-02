package application

import (
	"context"

	"github.com/edutrak/backend/internal/academic/domain"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

// Catalog reads are open to every authenticated account: careers, subjects,
// units and curricula are institutional reference data, not student records.

func (s *Service) ListCareers(ctx context.Context) ([]domain.Career, error) {
	return s.Catalog.ListCareers(ctx)
}

func (s *Service) GetCareer(ctx context.Context, id uuid.UUID) (*domain.Career, error) {
	return s.Catalog.GetCareer(ctx, id)
}

func (s *Service) ListSubjects(ctx context.Context) ([]domain.Subject, error) {
	return s.Catalog.ListSubjects(ctx)
}

func (s *Service) GetSubject(ctx context.Context, id uuid.UUID) (*domain.Subject, error) {
	return s.Catalog.GetSubject(ctx, id)
}

func (s *Service) ListUnits(ctx context.Context, subjectID uuid.UUID) ([]domain.SubjectUnit, error) {
	if _, err := s.Catalog.GetSubject(ctx, subjectID); err != nil {
		return nil, err
	}
	return s.Catalog.ListUnits(ctx, subjectID)
}

func (s *Service) ListCurriculum(ctx context.Context, careerID uuid.UUID, level *int16) ([]domain.CurriculumEntry, error) {
	if _, err := s.Catalog.GetCareer(ctx, careerID); err != nil {
		return nil, err
	}
	return s.Catalog.ListCurriculum(ctx, careerID, level)
}

type CreateCareerInput struct{ Code, Name string }

func (s *Service) CreateCareer(ctx context.Context, p iddomain.Principal, in CreateCareerInput) (*domain.Career, error) {
	if err := requireDirector(p); err != nil {
		return nil, err
	}
	code, err := domain.NewCode(in.Code)
	if err != nil {
		return nil, err
	}
	name, err := domain.NewCareerName(in.Name)
	if err != nil {
		return nil, err
	}
	c := &domain.Career{ID: s.NewID(), Code: code, Name: name, IsActive: true}
	if err := s.Catalog.CreateCareer(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

type CreateSubjectInput struct{ Code, Name string }

func (s *Service) CreateSubject(ctx context.Context, p iddomain.Principal, in CreateSubjectInput) (*domain.Subject, error) {
	if err := requireDirector(p); err != nil {
		return nil, err
	}
	code, err := domain.NewCode(in.Code)
	if err != nil {
		return nil, err
	}
	name, err := domain.NewSubjectName(in.Name)
	if err != nil {
		return nil, err
	}
	sub := &domain.Subject{ID: s.NewID(), Code: code, Name: name, IsActive: true}
	if err := s.Catalog.CreateSubject(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

type CreateUnitInput struct {
	Number int
	Name   string
}

func (s *Service) CreateUnit(ctx context.Context, p iddomain.Principal, subjectID uuid.UUID, in CreateUnitInput) (*domain.SubjectUnit, error) {
	if err := requireDirector(p); err != nil {
		return nil, err
	}
	if err := domain.ValidateUnitNumber(in.Number); err != nil {
		return nil, err
	}
	name, err := domain.NewSubjectName(in.Name)
	if err != nil {
		return nil, err
	}
	if _, err := s.Catalog.GetSubject(ctx, subjectID); err != nil {
		return nil, err
	}
	u := &domain.SubjectUnit{ID: s.NewID(), SubjectID: subjectID, Number: int16(in.Number), Name: name}
	if err := s.Catalog.CreateUnit(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

type AddCurriculumInput struct {
	SubjectID uuid.UUID
	Level     int
}

func (s *Service) AddCurriculum(ctx context.Context, p iddomain.Principal, careerID uuid.UUID, in AddCurriculumInput) (*domain.CurriculumEntry, error) {
	if err := requireDirector(p); err != nil {
		return nil, err
	}
	if err := domain.ValidateLevel(in.Level); err != nil {
		return nil, err
	}
	career, err := s.Catalog.GetCareer(ctx, careerID)
	if err != nil {
		return nil, err
	}
	if !career.IsActive {
		return nil, domain.ErrCareerInactive
	}
	subject, err := s.Catalog.GetSubject(ctx, in.SubjectID)
	if err != nil {
		return nil, asRef(err)
	}
	if !subject.IsActive {
		return nil, domain.ErrSubjectInactive
	}
	e := domain.CurriculumEntry{CareerID: careerID, SubjectID: subject.ID, Level: int16(in.Level), SubjectCode: subject.Code, SubjectName: subject.Name}
	if err := s.Catalog.AddCurriculum(ctx, e); err != nil {
		return nil, err
	}
	return &e, nil
}
