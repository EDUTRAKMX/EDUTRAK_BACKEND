// Package postgres implements the justification repository on PostgreSQL. It reads
// academic and attendance tables (read-only joins for context and scoping); the only
// write into attendance data goes through the Attendance module (see adapters/attendance).
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/edutrak/backend/internal/justifications/domain"
	"github.com/edutrak/backend/internal/justifications/ports"
	"github.com/edutrak/backend/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Repository struct{ db *database.DB }

func NewRepository(db *database.DB) *Repository { return &Repository{db: db} }

const caseSelect = `SELECT j.id, j.attendance_record_id, j.student_id, j.reason, j.status, j.created_at, j.updated_at,
		stu.institutional_number, stu.first_name, stu.last_name,
		r.status, ses.session_date, ta.id, ta.teacher_id, g.tutor_id, g.code, s.code, s.name, t.name, t.status,
		(SELECT count(*) FROM justification_evidence e WHERE e.justification_request_id = j.id)
	FROM justification_requests j
	JOIN users stu ON stu.id = j.student_id
	JOIN attendance_records r ON r.id = j.attendance_record_id
	JOIN attendance_sessions ses ON ses.id = r.attendance_session_id
	JOIN teaching_assignments ta ON ta.id = ses.teaching_assignment_id
	JOIN groups g ON g.id = ta.group_id
	JOIN subjects s ON s.id = ta.subject_id
	JOIN academic_terms t ON t.id = g.academic_term_id`

func scanCase(row interface{ Scan(...any) error }) (*domain.Case, error) {
	var c domain.Case
	var status string
	if err := row.Scan(&c.ID, &c.AttendanceRecordID, &c.StudentID, &c.Reason, &status, &c.CreatedAt, &c.UpdatedAt,
		&c.StudentNumber, &c.StudentFirst, &c.StudentLast,
		&c.RecordStatus, &c.SessionDate, &c.AssignmentID, &c.TeacherID, &c.GroupTutorID, &c.GroupCode, &c.SubjectCode, &c.SubjectName,
		&c.TermName, &c.TermStatus, &c.EvidenceCount); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	c.Status = domain.Status(status)
	return &c, nil
}

func (r *Repository) Create(ctx context.Context, j *domain.Justification) error {
	err := r.db.Q(ctx).QueryRow(ctx,
		`INSERT INTO justification_requests (id, attendance_record_id, student_id, reason, status) VALUES ($1, $2, $3, $4, $5)
		 RETURNING created_at, updated_at`,
		j.ID, j.AttendanceRecordID, j.StudentID, j.Reason, string(j.Status)).Scan(&j.CreatedAt, &j.UpdatedAt)
	if name, dup := database.IsUniqueViolation(err); dup && name == "justification_requests_active_per_record_idx" {
		return domain.ErrAlreadyRequested
	}
	if err != nil {
		return fmt.Errorf("insert justification: %w", err)
	}
	return nil
}

func (r *Repository) AddEvidence(ctx context.Context, e *domain.Evidence) error {
	err := r.db.Q(ctx).QueryRow(ctx,
		`INSERT INTO justification_evidence (id, justification_request_id, storage_key, original_filename, mime_type, size_bytes)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at`,
		e.ID, e.RequestID, e.StorageKey, e.OriginalFilename, e.MimeType, e.SizeBytes).Scan(&e.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert evidence: %w", err)
	}
	return nil
}

func (r *Repository) HasActive(ctx context.Context, recordID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.Q(ctx).QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM justification_requests WHERE attendance_record_id = $1
		                  AND status IN ('PENDING_TUTOR', 'PENDING_TEACHER', 'JUSTIFIED'))`, recordID).Scan(&ok)
	return ok, err
}

func (r *Repository) GetCase(ctx context.Context, id uuid.UUID, lock bool) (*domain.Case, error) {
	sql := caseSelect + ` WHERE j.id = $1`
	if lock {
		sql += ` FOR UPDATE OF j` // only the request row; joined rows are read
	}
	return scanCase(r.db.Q(ctx).QueryRow(ctx, sql, id))
}

func (r *Repository) UpdateStatus(ctx context.Context, id uuid.UUID, from, to domain.Status) (bool, error) {
	tag, err := r.db.Q(ctx).Exec(ctx,
		`UPDATE justification_requests SET status = $3, updated_at = clock_timestamp() WHERE id = $1 AND status = $2`, id, string(from), string(to))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repository) AddReview(ctx context.Context, rv *domain.Review) error {
	err := r.db.Q(ctx).QueryRow(ctx,
		`INSERT INTO justification_reviews (id, justification_request_id, reviewer_id, stage, decision, comment, reviewed_at)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), clock_timestamp())
		 RETURNING reviewed_at`,
		rv.ID, rv.RequestID, rv.ReviewerID, string(rv.Stage), string(rv.Decision), rv.Comment).Scan(&rv.ReviewedAt)
	if name, dup := database.IsUniqueViolation(err); dup && name == "justification_reviews_one_per_stage" {
		return domain.ErrWrongStage
	}
	if err != nil {
		return fmt.Errorf("insert review: %w", err)
	}
	return nil
}

// Detail runs three queries: the case, its evidence metadata and its reviews.
func (r *Repository) Detail(ctx context.Context, id uuid.UUID) (*domain.Detail, error) {
	c, err := r.GetCase(ctx, id, false)
	if err != nil {
		return nil, err
	}
	d := &domain.Detail{Case: *c, Evidence: []domain.Evidence{}, Reviews: []domain.Review{}}
	q := r.db.Q(ctx)

	erows, err := q.Query(ctx,
		`SELECT id, justification_request_id, storage_key, original_filename, mime_type, size_bytes, created_at
		   FROM justification_evidence WHERE justification_request_id = $1 ORDER BY created_at, id`, id)
	if err != nil {
		return nil, err
	}
	for erows.Next() {
		var e domain.Evidence
		if err := erows.Scan(&e.ID, &e.RequestID, &e.StorageKey, &e.OriginalFilename, &e.MimeType, &e.SizeBytes, &e.CreatedAt); err != nil {
			erows.Close()
			return nil, err
		}
		d.Evidence = append(d.Evidence, e)
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return nil, err
	}

	rrows, err := q.Query(ctx,
		`SELECT rv.id, rv.justification_request_id, rv.reviewer_id, rv.stage, rv.decision, COALESCE(rv.comment, ''), rv.reviewed_at,
		        u.first_name || ' ' || u.last_name
		   FROM justification_reviews rv JOIN users u ON u.id = rv.reviewer_id
		  WHERE rv.justification_request_id = $1
		  ORDER BY rv.reviewed_at, CASE rv.stage WHEN 'TUTOR' THEN 0 ELSE 1 END, rv.id`, id)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var rv domain.Review
		var stage, decision string
		if err := rrows.Scan(&rv.ID, &rv.RequestID, &rv.ReviewerID, &stage, &decision, &rv.Comment, &rv.ReviewedAt, &rv.ReviewerName); err != nil {
			return nil, err
		}
		rv.Stage, rv.Decision = domain.Stage(stage), domain.Decision(decision)
		d.Reviews = append(d.Reviews, rv)
	}
	return d, rrows.Err()
}

// List is one query, scoped in SQL, with the evidence count as a subselect (no N+1).
func (r *Repository) List(ctx context.Context, scope ports.ViewScope, status *domain.Status) ([]domain.Case, error) {
	var st *string
	if status != nil {
		s := string(*status)
		st = &s
	}
	rows, err := r.db.Q(ctx).Query(ctx, caseSelect+`
		WHERE ($2::text IS NULL OR j.status = $2)
		  AND ($1
		    OR ($3::uuid IS NOT NULL AND j.student_id = $3)
		    OR ($4::uuid IS NOT NULL AND ta.teacher_id = $4)
		    OR ($5::uuid IS NOT NULL AND g.tutor_id = $5))
		ORDER BY j.created_at DESC, j.id`,
		scope.All, st, scope.StudentID, scope.TeacherID, scope.TutorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Case{}
	for rows.Next() {
		c, err := scanCase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *Repository) Evidence(ctx context.Context, requestID, evidenceID uuid.UUID) (*domain.Evidence, error) {
	var e domain.Evidence
	err := r.db.Q(ctx).QueryRow(ctx,
		`SELECT id, justification_request_id, storage_key, original_filename, mime_type, size_bytes, created_at
		   FROM justification_evidence WHERE id = $1 AND justification_request_id = $2`, evidenceID, requestID).
		Scan(&e.ID, &e.RequestID, &e.StorageKey, &e.OriginalFilename, &e.MimeType, &e.SizeBytes, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrEvidenceNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}
