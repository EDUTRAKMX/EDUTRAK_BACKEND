package postgres

import (
	"context"

	"github.com/edutrak/backend/internal/academic/domain"
	"github.com/edutrak/backend/internal/academic/ports"
	"github.com/edutrak/backend/internal/platform/database"
	"github.com/google/uuid"
)

type TermRepository struct{ db *database.DB }

func NewTermRepository(db *database.DB) *TermRepository { return &TermRepository{db: db} }

const termColumns = `id, name, start_date, end_date, status`

func scanTerm(row interface{ Scan(...any) error }) (*domain.Term, error) {
	var t domain.Term
	var status string
	if err := row.Scan(&t.ID, &t.Name, &t.StartDate, &t.EndDate, &status); err != nil {
		return nil, notFoundOr(err, domain.ErrTermNotFound)
	}
	t.Status = domain.TermStatus(status)
	return &t, nil
}

func (r *TermRepository) Create(ctx context.Context, t *domain.Term) error {
	_, err := r.db.Q(ctx).Exec(ctx,
		`INSERT INTO academic_terms (id, name, start_date, end_date, status) VALUES ($1, $2, $3, $4, $5)`,
		t.ID, t.Name, t.StartDate, t.EndDate, string(t.Status))
	return uniqueErr("insert term", err, map[string]error{"academic_terms_name_key": domain.ErrTermNameTaken})
}

func (r *TermRepository) List(ctx context.Context) ([]domain.Term, error) {
	rows, err := r.db.Q(ctx).Query(ctx, `SELECT `+termColumns+` FROM academic_terms ORDER BY start_date DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Term{}
	for rows.Next() {
		t, err := scanTerm(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (r *TermRepository) Get(ctx context.Context, id uuid.UUID) (*domain.Term, error) {
	return scanTerm(r.db.Q(ctx).QueryRow(ctx, `SELECT `+termColumns+` FROM academic_terms WHERE id = $1`, id))
}

func (r *TermRepository) GetForShare(ctx context.Context, id uuid.UUID) (*domain.Term, error) {
	return scanTerm(r.db.Q(ctx).QueryRow(ctx, `SELECT `+termColumns+` FROM academic_terms WHERE id = $1 FOR SHARE`, id))
}

func (r *TermRepository) UpdateStatus(ctx context.Context, id uuid.UUID, from, to domain.TermStatus) (bool, error) {
	tag, err := r.db.Q(ctx).Exec(ctx, `UPDATE academic_terms SET status = $3 WHERE id = $1 AND status = $2`, id, string(from), string(to))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

type GroupRepository struct{ db *database.DB }

func NewGroupRepository(db *database.DB) *GroupRepository { return &GroupRepository{db: db} }

const groupSelect = `SELECT g.id, g.academic_term_id, g.career_id, g.tutor_id, g.code, g.level, t.name, c.code, c.name
	FROM groups g
	JOIN academic_terms t ON t.id = g.academic_term_id
	JOIN careers c ON c.id = g.career_id`

func scanGroup(row interface{ Scan(...any) error }) (*domain.Group, error) {
	var g domain.Group
	if err := row.Scan(&g.ID, &g.TermID, &g.CareerID, &g.TutorID, &g.Code, &g.Level, &g.TermName, &g.CareerCode, &g.CareerName); err != nil {
		return nil, notFoundOr(err, domain.ErrGroupNotFound)
	}
	return &g, nil
}

func (r *GroupRepository) Create(ctx context.Context, g *domain.Group) error {
	_, err := r.db.Q(ctx).Exec(ctx,
		`INSERT INTO groups (id, academic_term_id, career_id, tutor_id, code, level) VALUES ($1, $2, $3, $4, $5, $6)`,
		g.ID, g.TermID, g.CareerID, g.TutorID, g.Code, g.Level)
	return uniqueErr("insert group", err, map[string]error{"groups_academic_term_id_code_key": domain.ErrGroupCodeTaken})
}

func (r *GroupRepository) Get(ctx context.Context, id uuid.UUID) (*domain.Group, error) {
	return scanGroup(r.db.Q(ctx).QueryRow(ctx, groupSelect+` WHERE g.id = $1`, id))
}

// List applies the caller's scope in SQL: $1 user, $2 all, $3 teacher, $4 tutor, $5 student.
func (r *GroupRepository) List(ctx context.Context, s ports.Scope, f ports.GroupFilter) ([]domain.Group, error) {
	rows, err := r.db.Q(ctx).Query(ctx, groupSelect+`
		WHERE ($6::uuid IS NULL OR g.academic_term_id = $6)
		  AND ($2
		    OR ($3 AND EXISTS (SELECT 1 FROM teaching_assignments ta WHERE ta.group_id = g.id AND ta.teacher_id = $1))
		    OR ($4 AND g.tutor_id = $1)
		    OR ($5 AND EXISTS (SELECT 1 FROM enrollments e WHERE e.group_id = g.id AND e.student_id = $1 AND e.status = 'ACTIVE')))
		ORDER BY t.start_date DESC, g.level, g.code`,
		s.UserID, s.All, s.Teacher, s.Tutor, s.Student, f.TermID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Group{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

func (r *GroupRepository) SetTutor(ctx context.Context, id uuid.UUID, tutorID *uuid.UUID) error {
	tag, err := r.db.Q(ctx).Exec(ctx, `UPDATE groups SET tutor_id = $2 WHERE id = $1`, id, tutorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrGroupNotFound
	}
	return nil
}

func (r *GroupRepository) AddEnrollment(ctx context.Context, e *domain.Enrollment) error {
	err := r.db.Q(ctx).QueryRow(ctx,
		`INSERT INTO enrollments (id, group_id, student_id, status) VALUES ($1, $2, $3, $4) RETURNING enrolled_at`,
		e.ID, e.GroupID, e.StudentID, string(e.Status)).Scan(&e.EnrolledAt)
	return uniqueErr("insert enrollment", err, map[string]error{"enrollments_student_id_group_id_key": domain.ErrAlreadyEnrolled})
}

func (r *GroupRepository) ListEnrollments(ctx context.Context, groupID uuid.UUID) ([]domain.Enrollment, error) {
	rows, err := r.db.Q(ctx).Query(ctx,
		`SELECT e.id, e.group_id, e.student_id, e.status, e.enrolled_at, u.institutional_number, u.first_name, u.last_name
		   FROM enrollments e JOIN users u ON u.id = e.student_id
		  WHERE e.group_id = $1 ORDER BY u.last_name, u.first_name, u.institutional_number`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Enrollment{}
	for rows.Next() {
		var e domain.Enrollment
		var status string
		if err := rows.Scan(&e.ID, &e.GroupID, &e.StudentID, &status, &e.EnrolledAt,
			&e.Student.InstitutionalNumber, &e.Student.FirstName, &e.Student.LastName); err != nil {
			return nil, err
		}
		e.Status = domain.EnrollmentStatus(status)
		e.Student.ID = e.StudentID
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *GroupRepository) IsEnrolled(ctx context.Context, groupID, studentID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.Q(ctx).QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM enrollments WHERE group_id = $1 AND student_id = $2 AND status = 'ACTIVE')`,
		groupID, studentID).Scan(&ok)
	return ok, err
}

func (r *GroupRepository) TeachesInGroup(ctx context.Context, groupID, teacherID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.Q(ctx).QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM teaching_assignments WHERE group_id = $1 AND teacher_id = $2)`,
		groupID, teacherID).Scan(&ok)
	return ok, err
}
