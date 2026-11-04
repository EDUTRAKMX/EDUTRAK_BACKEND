// Package postgres implements the tutoring repository and the academic overview reader on
// PostgreSQL. Reads of Academic, Grades, Attendance and Justifications tables are
// read-only joins; nothing from those modules is copied into Tutoring.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/edutrak/backend/internal/platform/database"
	"github.com/edutrak/backend/internal/tutoring/domain"
	"github.com/edutrak/backend/internal/tutoring/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Repository struct{ db *database.DB }

func NewRepository(db *database.DB) *Repository { return &Repository{db: db} }

const enrollmentSelect = `SELECT u.id, u.institutional_number, u.first_name, u.last_name,
		g.id, g.code, g.level, g.tutor_id, c.code, c.name, t.id, t.name, t.status, t.start_date, t.end_date,
		e.status,
		(SELECT count(*) FROM tutoring_followups f WHERE f.student_id = e.student_id AND f.group_id = e.group_id),
		(SELECT max(f.followup_date) FROM tutoring_followups f WHERE f.student_id = e.student_id AND f.group_id = e.group_id)
	FROM enrollments e
	JOIN users u ON u.id = e.student_id
	JOIN groups g ON g.id = e.group_id
	JOIN careers c ON c.id = g.career_id
	JOIN academic_terms t ON t.id = g.academic_term_id`

func scanEnrollment(row interface{ Scan(...any) error }) (domain.Enrollment, error) {
	var e domain.Enrollment
	err := row.Scan(&e.StudentID, &e.StudentNumber, &e.StudentFirst, &e.StudentLast,
		&e.GroupID, &e.GroupCode, &e.Level, &e.GroupTutorID, &e.CareerCode, &e.CareerName,
		&e.TermID, &e.TermName, &e.TermStatus, &e.TermStart, &e.TermEnd,
		&e.EnrollmentStatus, &e.FollowUpCount, &e.LastFollowUpDate)
	return e, err
}

const studentRole = `EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = $1 AND r.code = 'STUDENT')`

func (r *Repository) StudentExists(ctx context.Context, studentID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.Q(ctx).QueryRow(ctx, `SELECT `+studentRole, studentID).Scan(&ok)
	return ok, err
}

func (r *Repository) Context(ctx context.Context, studentID, groupID uuid.UUID, lock bool) (*domain.Enrollment, error) {
	sql := enrollmentSelect + ` WHERE e.student_id = $1 AND e.group_id = $2`
	if lock {
		sql += ` FOR SHARE OF g, t` // tutor_id and term status stay put until the transaction ends
	}
	e, err := scanEnrollment(r.db.Q(ctx).QueryRow(ctx, sql, studentID, groupID))
	if err == nil {
		return &e, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	// Tell the caller which reference was wrong.
	if ok, err := r.StudentExists(ctx, studentID); err != nil {
		return nil, err
	} else if !ok {
		return nil, domain.ErrBodyStudentNotFound
	}
	var groupOK bool
	if err := r.db.Q(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM groups WHERE id = $1)`, groupID).Scan(&groupOK); err != nil {
		return nil, err
	}
	if !groupOK {
		return nil, domain.ErrBodyGroupNotFound
	}
	return nil, domain.ErrNotEnrolled
}

const scopeClause = `($1 OR ($2::uuid IS NOT NULL AND g.tutor_id = $2))`

func (r *Repository) Enrollments(ctx context.Context, studentID uuid.UUID, scope ports.Scope) ([]domain.Enrollment, error) {
	rows, err := r.db.Q(ctx).Query(ctx, enrollmentSelect+`
		WHERE e.student_id = $3 AND `+scopeClause+`
		ORDER BY (e.status = 'ACTIVE') DESC, t.start_date DESC, e.enrolled_at DESC, g.id`,
		scope.All, scope.TutorID, studentID)
	if err != nil {
		return nil, err
	}
	return collectEnrollments(rows)
}

func (r *Repository) Students(ctx context.Context, scope ports.Scope, f ports.StudentFilter) ([]domain.Enrollment, error) {
	rows, err := r.db.Q(ctx).Query(ctx, enrollmentSelect+`
		WHERE `+scopeClause+` AND e.status = $3
		  AND ($4::uuid IS NULL OR t.id = $4) AND ($5::uuid IS NULL OR g.id = $5)
		ORDER BY t.start_date DESC, g.code, u.last_name, u.first_name, u.id`,
		scope.All, scope.TutorID, f.Status, f.TermID, f.GroupID)
	if err != nil {
		return nil, err
	}
	return collectEnrollments(rows)
}

func collectEnrollments(rows pgx.Rows) ([]domain.Enrollment, error) {
	defer rows.Close()
	out := []domain.Enrollment{}
	for rows.Next() {
		e, err := scanEnrollment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *Repository) Create(ctx context.Context, f *domain.FollowUp) error {
	err := r.db.Q(ctx).QueryRow(ctx,
		`INSERT INTO tutoring_followups (id, student_id, tutor_id, group_id, followup_date, note) VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING created_at`,
		f.ID, f.StudentID, f.TutorID, f.GroupID, f.Date, f.Note).Scan(&f.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert follow-up: %w", err)
	}
	return nil
}

// entrySelect LEFT JOINs the group because rows from before migration 0011 have no group.
const entrySelect = `SELECT f.id, f.student_id, f.tutor_id, COALESCE(f.group_id, '00000000-0000-0000-0000-000000000000'::uuid),
		f.followup_date, f.note, f.created_at,
		stu.institutional_number, stu.first_name, stu.last_name, au.first_name, au.last_name,
		COALESCE(g.code, ''), g.tutor_id, COALESCE(t.id, '00000000-0000-0000-0000-000000000000'::uuid), COALESCE(t.name, '')
	FROM tutoring_followups f
	JOIN users stu ON stu.id = f.student_id
	JOIN users au ON au.id = f.tutor_id
	LEFT JOIN groups g ON g.id = f.group_id
	LEFT JOIN academic_terms t ON t.id = g.academic_term_id`

func scanEntry(row interface{ Scan(...any) error }) (domain.Entry, error) {
	var e domain.Entry
	err := row.Scan(&e.ID, &e.StudentID, &e.TutorID, &e.GroupID, &e.Date, &e.Note, &e.CreatedAt,
		&e.StudentNumber, &e.StudentFirst, &e.StudentLast, &e.TutorFirst, &e.TutorLast,
		&e.GroupCode, &e.GroupTutorID, &e.TermID, &e.TermName)
	return e, err
}

func (r *Repository) Get(ctx context.Context, id uuid.UUID) (*domain.Entry, error) {
	e, err := scanEntry(r.db.Q(ctx).QueryRow(ctx, entrySelect+` WHERE f.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrFollowUpNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *Repository) List(ctx context.Context, scope ports.Scope, f ports.FollowUpFilter, p ports.Page) ([]domain.Entry, int, error) {
	const where = ` WHERE ` + scopeClause + `
		  AND ($3::uuid IS NULL OR f.student_id = $3) AND ($4::uuid IS NULL OR f.group_id = $4)
		  AND ($5::uuid IS NULL OR g.academic_term_id = $5)
		  AND ($6::date IS NULL OR f.followup_date >= $6) AND ($7::date IS NULL OR f.followup_date <= $7)`
	args := []any{scope.All, scope.TutorID, f.StudentID, f.GroupID, f.TermID, f.From, f.To}

	var total int
	if err := r.db.Q(ctx).QueryRow(ctx,
		`SELECT count(*) FROM tutoring_followups f LEFT JOIN groups g ON g.id = f.group_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.Q(ctx).Query(ctx, entrySelect+where+`
		ORDER BY f.followup_date DESC, f.created_at DESC, f.id LIMIT $8 OFFSET $9`, append(args, p.Limit, p.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}
