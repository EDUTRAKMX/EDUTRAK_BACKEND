package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/edutrak/backend/internal/analytics/domain"
	"github.com/edutrak/backend/internal/analytics/ports"
	"github.com/edutrak/backend/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Alerts struct{ db *database.DB }

func NewAlerts(db *database.DB) *Alerts { return &Alerts{db: db} }

func (a *Alerts) LockGroup(ctx context.Context, groupID uuid.UUID) error {
	_, err := a.db.Q(ctx).Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('analytics:alerts:' || $1::text, 0))`, groupID.String())
	return err
}

const storedCols = `a.id, a.student_id, a.group_id, a.subject_enrollment_id, a.type, COALESCE(a.condition_key, ''), a.message,
	a.evidence, a.status, a.created_at, a.updated_at, a.evaluated_at, a.resolved_at`

func scanStored(row interface{ Scan(...any) error }, extra ...any) (domain.StoredAlert, error) {
	var s domain.StoredAlert
	var rule, status string
	dst := append([]any{&s.ID, &s.StudentID, &s.GroupID, &s.SubjectEnrollmentID, &rule, &s.Key, &s.Message,
		&s.Evidence, &status, &s.CreatedAt, &s.UpdatedAt, &s.EvaluatedAt, &s.ResolvedAt}, extra...)
	err := row.Scan(dst...)
	s.Rule, s.Status = domain.Rule(rule), domain.AlertStatus(status)
	return s, err
}

func (a *Alerts) Unresolved(ctx context.Context, groupID uuid.UUID, studentID *uuid.UUID) ([]domain.StoredAlert, error) {
	rows, err := a.db.Q(ctx).Query(ctx, `SELECT `+storedCols+` FROM alerts a
		WHERE a.group_id = $1 AND a.status <> 'RESOLVED' AND ($2::uuid IS NULL OR a.student_id = $2)
		ORDER BY a.student_id, a.type, a.condition_key FOR UPDATE`, groupID, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.StoredAlert{}
	for rows.Next() {
		s, err := scanStored(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (a *Alerts) Create(ctx context.Context, n ports.NewAlert) error {
	c := n.Candidate
	_, err := a.db.Q(ctx).Exec(ctx, `
		INSERT INTO alerts (id, student_id, subject_enrollment_id, type, message, evidence, status, group_id, condition_key, created_at, updated_at, evaluated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'OPEN', $7, $8, $9, $9, $9)`,
		n.ID, c.StudentID, c.SubjectEnrollmentID, string(c.Rule), c.Message, n.Evidence, n.GroupID, c.Key, n.Now)
	if err != nil {
		return fmt.Errorf("insert alert: %w", err)
	}
	return nil
}

func (a *Alerts) Update(ctx context.Context, id uuid.UUID, message string, evidence []byte, now time.Time) error {
	_, err := a.db.Q(ctx).Exec(ctx,
		`UPDATE alerts SET message = $2, evidence = $3, updated_at = $4, evaluated_at = $4 WHERE id = $1`, id, message, evidence, now)
	return err
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

func (a *Alerts) Touch(ctx context.Context, ids []uuid.UUID, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := a.db.Q(ctx).Exec(ctx, `UPDATE alerts SET evaluated_at = $2 WHERE id = ANY($1::uuid[])`, uuidStrings(ids), now)
	return err
}

func (a *Alerts) Resolve(ctx context.Context, ids []uuid.UUID, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := a.db.Q(ctx).Exec(ctx,
		`UPDATE alerts SET status = 'RESOLVED', resolved_at = $2, updated_at = $2, evaluated_at = $2 WHERE id = ANY($1::uuid[]) AND status <> 'RESOLVED'`,
		uuidStrings(ids), now)
	return err
}

const viewSelect = `SELECT ` + storedCols + `,
		stu.institutional_number, stu.first_name, stu.last_name, g.code, g.tutor_id, t.id, t.name,
		COALESCE(s.code, ''), COALESCE(s.name, '')
	FROM alerts a
	JOIN users stu ON stu.id = a.student_id
	JOIN groups g ON g.id = a.group_id
	JOIN academic_terms t ON t.id = g.academic_term_id
	LEFT JOIN subject_enrollments se ON se.id = a.subject_enrollment_id
	LEFT JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id
	LEFT JOIN subjects s ON s.id = ta.subject_id`

func scanView(row interface{ Scan(...any) error }) (domain.AlertView, error) {
	var v domain.AlertView
	var rule, status string
	err := row.Scan(&v.ID, &v.StudentID, &v.GroupID, &v.SubjectEnrollmentID, &rule, &v.Key, &v.Message,
		&v.Evidence, &status, &v.CreatedAt, &v.UpdatedAt, &v.EvaluatedAt, &v.ResolvedAt,
		&v.StudentNumber, &v.StudentFirst, &v.StudentLast, &v.GroupCode, &v.GroupTutorID, &v.TermID, &v.TermName, &v.SubjectCode, &v.SubjectName)
	v.Rule, v.Status = domain.Rule(rule), domain.AlertStatus(status)
	return v, err
}

func (a *Alerts) Get(ctx context.Context, id uuid.UUID) (*domain.AlertView, error) {
	v, err := scanView(a.db.Q(ctx).QueryRow(ctx, viewSelect+` WHERE a.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrAlertNotFound
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (a *Alerts) List(ctx context.Context, scope ports.Scope, f ports.AlertFilter, p ports.Page) ([]domain.AlertView, int, error) {
	const where = ` WHERE ($1 OR ($2::uuid IS NOT NULL AND g.tutor_id = $2))
		  AND ($3::uuid IS NULL OR a.student_id = $3) AND ($4::uuid IS NULL OR a.group_id = $4) AND ($5::uuid IS NULL OR g.academic_term_id = $5)
		  AND ($6 = '' OR a.type = $6)
		  AND (($7 = '' AND a.status <> 'RESOLVED') OR $7 = 'ALL' OR a.status = $7)`
	args := []any{scope.All, scope.TutorID, f.StudentID, f.GroupID, f.TermID, f.Rule, f.Status}
	var total int
	if err := a.db.Q(ctx).QueryRow(ctx,
		`SELECT count(*) FROM alerts a JOIN groups g ON g.id = a.group_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := a.db.Q(ctx).Query(ctx, viewSelect+where+` ORDER BY a.created_at DESC, a.id LIMIT $8 OFFSET $9`, append(args, p.Limit, p.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.AlertView{}
	for rows.Next() {
		v, err := scanView(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

func (a *Alerts) OpenCounts(ctx context.Context, groupID uuid.UUID) (map[domain.Rule]int, error) {
	rows, err := a.db.Q(ctx).Query(ctx, `SELECT type, count(*) FROM alerts WHERE group_id = $1 AND status <> 'RESOLVED' GROUP BY type`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[domain.Rule]int{}
	for rows.Next() {
		var r string
		var n int
		if err := rows.Scan(&r, &n); err != nil {
			return nil, err
		}
		out[domain.Rule(r)] = n
	}
	return out, rows.Err()
}
