// Package postgres implements the grades repositories on PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/edutrak/backend/internal/grades/domain"
	"github.com/edutrak/backend/internal/grades/ports"
	"github.com/edutrak/backend/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Scores travel as text ("89.99") in both directions, so no float is ever involved.
func scanScore(text string) (domain.Score, error) {
	s, err := domain.ParseScore(text)
	if err != nil {
		return domain.Score{}, fmt.Errorf("stored score %q is invalid: %w", text, err)
	}
	return s, nil
}

type Repository struct{ db *database.DB }

func NewRepository(db *database.DB) *Repository { return &Repository{db: db} }

const gradeColumns = `id, subject_enrollment_id, subject_unit_id, score::text, recorded_by, recorded_at, updated_at`

func scanGrade(row pgx.Row) (*domain.Grade, error) {
	var g domain.Grade
	var score string
	if err := row.Scan(&g.ID, &g.SubjectEnrollmentID, &g.SubjectUnitID, &score, &g.RecordedBy, &g.RecordedAt, &g.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrGradeNotFound
		}
		return nil, err
	}
	s, err := scanScore(score)
	if err != nil {
		return nil, err
	}
	g.Score = s
	return &g, nil
}

func (r *Repository) Create(ctx context.Context, g *domain.Grade) error {
	err := r.db.Q(ctx).QueryRow(ctx,
		`INSERT INTO grades (id, subject_enrollment_id, subject_unit_id, score, recorded_by)
		 VALUES ($1, $2, $3, $4::text::numeric, $5) RETURNING recorded_at, updated_at`,
		g.ID, g.SubjectEnrollmentID, g.SubjectUnitID, g.Score.String(), g.RecordedBy).Scan(&g.RecordedAt, &g.UpdatedAt)
	if name, dup := database.IsUniqueViolation(err); dup && name == "grades_subject_enrollment_id_subject_unit_id_key" {
		return domain.ErrGradeExists
	}
	if err != nil {
		return fmt.Errorf("insert grade: %w", err)
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, id uuid.UUID) (*domain.Grade, error) {
	return scanGrade(r.db.Q(ctx).QueryRow(ctx, `SELECT `+gradeColumns+` FROM grades WHERE id = $1`, id))
}

func (r *Repository) GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.Grade, error) {
	return scanGrade(r.db.Q(ctx).QueryRow(ctx, `SELECT `+gradeColumns+` FROM grades WHERE id = $1 FOR UPDATE`, id))
}

func (r *Repository) UpdateScore(ctx context.Context, id uuid.UUID, s domain.Score) (*domain.Grade, error) {
	return scanGrade(r.db.Q(ctx).QueryRow(ctx,
		`UPDATE grades SET score = $2::text::numeric, updated_at = now() WHERE id = $1 RETURNING `+gradeColumns, id, s.String()))
}

// unitGrade builds a UnitGrade from a LEFT JOINed grade (all three nil when pending).
func unitGrade(u domain.Unit, gradeID *uuid.UUID, score *string, updatedAt *time.Time) (domain.UnitGrade, error) {
	ug := domain.UnitGrade{Unit: u}
	if gradeID == nil || score == nil {
		return ug, nil
	}
	s, err := scanScore(*score)
	if err != nil {
		return ug, err
	}
	ug.GradeID, ug.Score, ug.UpdatedAt = gradeID, &s, updatedAt
	return ug, nil
}

// ListStudentSheets reads every attempt of a student in ONE query: attempts x
// units, with the grade LEFT JOINed so pending units come back without a score.
func (r *Repository) ListStudentSheets(ctx context.Context, studentID uuid.UUID, scope ports.ViewScope) ([]domain.StudentSheet, error) {
	rows, err := r.db.Q(ctx).Query(ctx, `
		SELECT se.id, ta.id, se.attempt_number, se.status, t.id, t.name, g.code, s.id, s.code, s.name,
		       u.id, u.subject_id, u.unit_number, u.name, gr.id, gr.score::text, gr.updated_at
		  FROM subject_enrollments se
		  JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id
		  JOIN groups g ON g.id = ta.group_id
		  JOIN academic_terms t ON t.id = g.academic_term_id
		  JOIN subjects s ON s.id = ta.subject_id
		  JOIN subject_units u ON u.subject_id = s.id
		  LEFT JOIN grades gr ON gr.subject_enrollment_id = se.id AND gr.subject_unit_id = u.id
		 WHERE se.student_id = $1
		   AND ($2 OR ($3::uuid IS NOT NULL AND ta.teacher_id = $3) OR ($4::uuid IS NOT NULL AND g.tutor_id = $4))
		 ORDER BY t.start_date DESC, s.name, se.attempt_number, se.id, u.unit_number`,
		studentID, scope.All, scope.TeacherID, scope.TutorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.StudentSheet{}
	for rows.Next() {
		var sh domain.StudentSheet
		var u domain.Unit
		var gradeID *uuid.UUID
		var score *string
		var updatedAt *time.Time
		if err := rows.Scan(&sh.SubjectEnrollmentID, &sh.AssignmentID, &sh.AttemptNumber, &sh.Status, &sh.TermID, &sh.TermName,
			&sh.GroupCode, &sh.SubjectID, &sh.SubjectCode, &sh.SubjectName,
			&u.ID, &u.SubjectID, &u.Number, &u.Name, &gradeID, &score, &updatedAt); err != nil {
			return nil, err
		}
		ug, err := unitGrade(u, gradeID, score, updatedAt)
		if err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].SubjectEnrollmentID == sh.SubjectEnrollmentID {
			out[n-1].Units = append(out[n-1].Units, ug)
			continue
		}
		sh.Units = []domain.UnitGrade{ug}
		out = append(out, sh)
	}
	return out, rows.Err()
}

// AssignmentSheet runs three queries regardless of class size: the assignment,
// its subject's units, and every student attempt with its grades.
func (r *Repository) AssignmentSheet(ctx context.Context, assignmentID uuid.UUID) (*domain.AssignmentSheet, error) {
	q := r.db.Q(ctx)
	var sh domain.AssignmentSheet
	err := q.QueryRow(ctx, `
		SELECT ta.id, g.id, g.code, s.id, s.code, s.name, t.id, t.status
		  FROM teaching_assignments ta
		  JOIN groups g ON g.id = ta.group_id
		  JOIN subjects s ON s.id = ta.subject_id
		  JOIN academic_terms t ON t.id = g.academic_term_id
		 WHERE ta.id = $1`, assignmentID).
		Scan(&sh.AssignmentID, &sh.GroupID, &sh.GroupCode, &sh.SubjectID, &sh.SubjectCode, &sh.SubjectName, &sh.TermID, &sh.TermStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrAssignmentNotFound
	}
	if err != nil {
		return nil, err
	}

	urows, err := q.Query(ctx, `SELECT id, subject_id, unit_number, name FROM subject_units WHERE subject_id = $1 ORDER BY unit_number`, sh.SubjectID)
	if err != nil {
		return nil, err
	}
	sh.Units = []domain.Unit{}
	for urows.Next() {
		var u domain.Unit
		if err := urows.Scan(&u.ID, &u.SubjectID, &u.Number, &u.Name); err != nil {
			urows.Close()
			return nil, err
		}
		sh.Units = append(sh.Units, u)
	}
	urows.Close()
	if err := urows.Err(); err != nil {
		return nil, err
	}

	rows, err := q.Query(ctx, `
		SELECT se.id, se.attempt_number, se.status, us.id, us.institutional_number, us.first_name, us.last_name,
		       gr.subject_unit_id, gr.id, gr.score::text, gr.updated_at
		  FROM subject_enrollments se
		  JOIN users us ON us.id = se.student_id
		  LEFT JOIN grades gr ON gr.subject_enrollment_id = se.id
		 WHERE se.teaching_assignment_id = $1
		 ORDER BY us.last_name, us.first_name, us.institutional_number, se.id`, assignmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type cell struct {
		id    uuid.UUID
		score string
		at    time.Time
	}
	cells := map[uuid.UUID]map[uuid.UUID]cell{} // enrollment -> unit -> grade
	sh.Rows = []domain.AssignmentRow{}
	for rows.Next() {
		var row domain.AssignmentRow
		var unitID, gradeID *uuid.UUID
		var score *string
		var at *time.Time
		if err := rows.Scan(&row.SubjectEnrollmentID, &row.AttemptNumber, &row.Status,
			&row.Student.ID, &row.Student.InstitutionalNumber, &row.Student.FirstName, &row.Student.LastName,
			&unitID, &gradeID, &score, &at); err != nil {
			return nil, err
		}
		if n := len(sh.Rows); n == 0 || sh.Rows[n-1].SubjectEnrollmentID != row.SubjectEnrollmentID {
			sh.Rows = append(sh.Rows, row)
			cells[row.SubjectEnrollmentID] = map[uuid.UUID]cell{}
		}
		if unitID != nil && gradeID != nil && score != nil && at != nil {
			cells[row.SubjectEnrollmentID][*unitID] = cell{*gradeID, *score, *at}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range sh.Rows {
		row := &sh.Rows[i]
		row.Units = make([]domain.UnitGrade, len(sh.Units))
		for j, u := range sh.Units {
			row.Units[j] = domain.UnitGrade{Unit: u}
			if c, ok := cells[row.SubjectEnrollmentID][u.ID]; ok {
				s, err := scanScore(c.score)
				if err != nil {
					return nil, err
				}
				id, at := c.id, c.at
				row.Units[j].GradeID, row.Units[j].Score, row.Units[j].UpdatedAt = &id, &s, &at
			}
		}
	}
	return &sh, nil
}

// AcademicReader answers grading's questions about academic data. It only reads.
type AcademicReader struct{ db *database.DB }

func NewAcademicReader(db *database.DB) *AcademicReader { return &AcademicReader{db: db} }

func (r *AcademicReader) EnrollmentRef(ctx context.Context, id uuid.UUID, lockTerm bool) (*domain.SubjectEnrollmentRef, error) {
	sql := `SELECT se.id, se.student_id, ta.id, ta.teacher_id, g.id, g.tutor_id, ta.subject_id, t.id, t.status, se.status, se.attempt_number
		  FROM subject_enrollments se
		  JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id
		  JOIN groups g ON g.id = ta.group_id
		  JOIN academic_terms t ON t.id = g.academic_term_id
		 WHERE se.id = $1`
	if lockTerm {
		sql += ` FOR SHARE OF t`
	}
	var ref domain.SubjectEnrollmentRef
	err := r.db.Q(ctx).QueryRow(ctx, sql, id).Scan(&ref.ID, &ref.StudentID, &ref.AssignmentID, &ref.TeacherID, &ref.GroupID,
		&ref.GroupTutorID, &ref.SubjectID, &ref.TermID, &ref.TermStatus, &ref.Status, &ref.AttemptNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrEnrollmentNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

func (r *AcademicReader) Unit(ctx context.Context, id uuid.UUID) (*domain.Unit, error) {
	var u domain.Unit
	err := r.db.Q(ctx).QueryRow(ctx, `SELECT id, subject_id, unit_number, name FROM subject_units WHERE id = $1`, id).
		Scan(&u.ID, &u.SubjectID, &u.Number, &u.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrUnitNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *AcademicReader) AssignmentOwner(ctx context.Context, assignmentID uuid.UUID) (*ports.AssignmentOwner, error) {
	var o ports.AssignmentOwner
	err := r.db.Q(ctx).QueryRow(ctx,
		`SELECT ta.teacher_id, g.tutor_id FROM teaching_assignments ta JOIN groups g ON g.id = ta.group_id WHERE ta.id = $1`, assignmentID).
		Scan(&o.TeacherID, &o.GroupTutorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrAssignmentNotFound
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

const isStudent = `EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = $1 AND r.code = 'STUDENT')`

func (r *AcademicReader) StudentExists(ctx context.Context, studentID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.Q(ctx).QueryRow(ctx, `SELECT `+isStudent, studentID).Scan(&ok)
	return ok, err
}

func (r *AcademicReader) StudentVisible(ctx context.Context, studentID uuid.UUID, scope ports.ViewScope) (bool, error) {
	var ok bool
	err := r.db.Q(ctx).QueryRow(ctx, `SELECT `+isStudent+` AND (
		   $2
		   OR ($3::uuid IS NOT NULL AND EXISTS (
		        SELECT 1 FROM subject_enrollments se JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id
		         WHERE se.student_id = $1 AND ta.teacher_id = $3))
		   OR ($4::uuid IS NOT NULL AND EXISTS (
		        SELECT 1 FROM enrollments e JOIN groups g ON g.id = e.group_id
		         WHERE e.student_id = $1 AND g.tutor_id = $4)))`,
		studentID, scope.All, scope.TeacherID, scope.TutorID).Scan(&ok)
	return ok, err
}
