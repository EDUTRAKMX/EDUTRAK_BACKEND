// Package postgres implements the academic repositories on PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/edutrak/backend/internal/academic/domain"
	"github.com/edutrak/backend/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// uniqueErr maps a unique violation to the domain error registered for its
// constraint; any other error is returned wrapped with op.
func uniqueErr(op string, err error, byConstraint map[string]error) error {
	if err == nil {
		return nil
	}
	if name, ok := database.IsUniqueViolation(err); ok {
		if mapped, known := byConstraint[name]; known {
			return mapped
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

func notFoundOr(err error, notFound error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound
	}
	return err
}

type CatalogRepository struct{ db *database.DB }

func NewCatalogRepository(db *database.DB) *CatalogRepository { return &CatalogRepository{db: db} }

func (r *CatalogRepository) ListCareers(ctx context.Context) ([]domain.Career, error) {
	rows, err := r.db.Q(ctx).Query(ctx, `SELECT id, code, name, is_active FROM careers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Career{}
	for rows.Next() {
		var c domain.Career
		if err := rows.Scan(&c.ID, &c.Code, &c.Name, &c.IsActive); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *CatalogRepository) GetCareer(ctx context.Context, id uuid.UUID) (*domain.Career, error) {
	var c domain.Career
	err := r.db.Q(ctx).QueryRow(ctx, `SELECT id, code, name, is_active FROM careers WHERE id = $1`, id).
		Scan(&c.ID, &c.Code, &c.Name, &c.IsActive)
	if err != nil {
		return nil, notFoundOr(err, domain.ErrCareerNotFound)
	}
	return &c, nil
}

func (r *CatalogRepository) CreateCareer(ctx context.Context, c *domain.Career) error {
	_, err := r.db.Q(ctx).Exec(ctx, `INSERT INTO careers (id, code, name, is_active) VALUES ($1, $2, $3, $4)`, c.ID, c.Code, c.Name, c.IsActive)
	return uniqueErr("insert career", err, map[string]error{"careers_code_key": domain.ErrCareerCodeTaken})
}

func (r *CatalogRepository) ListSubjects(ctx context.Context) ([]domain.Subject, error) {
	rows, err := r.db.Q(ctx).Query(ctx, `SELECT id, code, name, is_active FROM subjects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Subject{}
	for rows.Next() {
		var s domain.Subject
		if err := rows.Scan(&s.ID, &s.Code, &s.Name, &s.IsActive); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *CatalogRepository) GetSubject(ctx context.Context, id uuid.UUID) (*domain.Subject, error) {
	var s domain.Subject
	err := r.db.Q(ctx).QueryRow(ctx, `SELECT id, code, name, is_active FROM subjects WHERE id = $1`, id).
		Scan(&s.ID, &s.Code, &s.Name, &s.IsActive)
	if err != nil {
		return nil, notFoundOr(err, domain.ErrSubjectNotFound)
	}
	return &s, nil
}

func (r *CatalogRepository) CreateSubject(ctx context.Context, s *domain.Subject) error {
	_, err := r.db.Q(ctx).Exec(ctx, `INSERT INTO subjects (id, code, name, is_active) VALUES ($1, $2, $3, $4)`, s.ID, s.Code, s.Name, s.IsActive)
	return uniqueErr("insert subject", err, map[string]error{"subjects_code_key": domain.ErrSubjectCodeTaken})
}

func (r *CatalogRepository) ListUnits(ctx context.Context, subjectID uuid.UUID) ([]domain.SubjectUnit, error) {
	rows, err := r.db.Q(ctx).Query(ctx,
		`SELECT id, subject_id, unit_number, name FROM subject_units WHERE subject_id = $1 ORDER BY unit_number`, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.SubjectUnit{}
	for rows.Next() {
		var u domain.SubjectUnit
		if err := rows.Scan(&u.ID, &u.SubjectID, &u.Number, &u.Name); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *CatalogRepository) CreateUnit(ctx context.Context, u *domain.SubjectUnit) error {
	_, err := r.db.Q(ctx).Exec(ctx, `INSERT INTO subject_units (id, subject_id, unit_number, name) VALUES ($1, $2, $3, $4)`,
		u.ID, u.SubjectID, u.Number, u.Name)
	return uniqueErr("insert subject unit", err, map[string]error{"subject_units_subject_id_unit_number_key": domain.ErrUnitNumberTaken})
}

func (r *CatalogRepository) ListCurriculum(ctx context.Context, careerID uuid.UUID, level *int16) ([]domain.CurriculumEntry, error) {
	rows, err := r.db.Q(ctx).Query(ctx,
		`SELECT cs.career_id, cs.subject_id, cs.level, s.code, s.name
		   FROM curriculum_subjects cs JOIN subjects s ON s.id = cs.subject_id
		  WHERE cs.career_id = $1 AND ($2::smallint IS NULL OR cs.level = $2)
		  ORDER BY cs.level, s.name`, careerID, level)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CurriculumEntry{}
	for rows.Next() {
		var e domain.CurriculumEntry
		if err := rows.Scan(&e.CareerID, &e.SubjectID, &e.Level, &e.SubjectCode, &e.SubjectName); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *CatalogRepository) AddCurriculum(ctx context.Context, e domain.CurriculumEntry) error {
	_, err := r.db.Q(ctx).Exec(ctx, `INSERT INTO curriculum_subjects (career_id, subject_id, level) VALUES ($1, $2, $3)`,
		e.CareerID, e.SubjectID, e.Level)
	return uniqueErr("insert curriculum entry", err, map[string]error{"curriculum_subjects_pkey": domain.ErrCurriculumExists})
}

func (r *CatalogRepository) InCurriculum(ctx context.Context, careerID, subjectID uuid.UUID, level int16) (bool, error) {
	var ok bool
	err := r.db.Q(ctx).QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM curriculum_subjects WHERE career_id = $1 AND subject_id = $2 AND level = $3)`,
		careerID, subjectID, level).Scan(&ok)
	return ok, err
}

// UserDirectory reads accounts (read-only) so academic can validate roles.
type UserDirectory struct{ db *database.DB }

func NewUserDirectory(db *database.DB) *UserDirectory { return &UserDirectory{db: db} }

func (r *UserDirectory) Find(ctx context.Context, id uuid.UUID) (*domain.UserRef, error) {
	u := domain.UserRef{ID: id}
	err := r.db.Q(ctx).QueryRow(ctx,
		`SELECT u.is_active,
		        COALESCE((SELECT array_agg(r.code) FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = u.id), '{}')
		   FROM users u WHERE u.id = $1`, id).Scan(&u.IsActive, &u.Roles)
	if err != nil {
		return nil, notFoundOr(err, domain.ErrUserNotFound)
	}
	return &u, nil
}
