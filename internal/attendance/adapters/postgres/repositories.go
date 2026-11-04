// Package postgres implements the attendance repositories on PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/edutrak/backend/internal/attendance/domain"
	"github.com/edutrak/backend/internal/attendance/ports"
	"github.com/edutrak/backend/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Repository struct{ db *database.DB }

func NewRepository(db *database.DB) *Repository { return &Repository{db: db} }

const sessionColumns = `id, teaching_assignment_id, schedule_id, session_date, status, opened_by, opened_at, closed_at,
	COALESCE(qr_token_hash, ''), qr_expires_at, late_after_minutes, lates_per_absence`

func scanSession(row interface{ Scan(...any) error }) (*domain.Session, error) {
	var s domain.Session
	var status string
	if err := row.Scan(&s.ID, &s.AssignmentID, &s.ScheduleID, &s.Date, &status, &s.OpenedBy, &s.OpenedAt, &s.ClosedAt,
		&s.QRTokenHash, &s.QRExpiresAt, &s.LateAfterMinutes, &s.LatesPerAbsence); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrSessionNotFound
		}
		return nil, err
	}
	s.Status = domain.SessionStatus(status)
	return &s, nil
}

func (r *Repository) CreateSession(ctx context.Context, s *domain.Session) error {
	_, err := r.db.Q(ctx).Exec(ctx,
		`INSERT INTO attendance_sessions (id, teaching_assignment_id, schedule_id, session_date, status, opened_by, opened_at, late_after_minutes, lates_per_absence)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		s.ID, s.AssignmentID, s.ScheduleID, s.Date, string(s.Status), s.OpenedBy, s.OpenedAt, s.LateAfterMinutes, s.LatesPerAbsence)
	if name, dup := database.IsUniqueViolation(err); dup && name == "attendance_sessions_one_open_per_assignment_idx" {
		return domain.ErrSessionAlreadyOpen
	}
	if err != nil {
		return fmt.Errorf("insert attendance session: %w", err)
	}
	return nil
}

func (r *Repository) GetSession(ctx context.Context, id uuid.UUID, lock ports.Lock) (*domain.Session, error) {
	sql := `SELECT ` + sessionColumns + ` FROM attendance_sessions WHERE id = $1`
	switch lock {
	case ports.LockShare:
		sql += ` FOR SHARE`
	case ports.LockUpdate:
		sql += ` FOR UPDATE`
	}
	return scanSession(r.db.Q(ctx).QueryRow(ctx, sql, id))
}

func (r *Repository) SetQR(ctx context.Context, sessionID uuid.UUID, hash string, expiresAt time.Time) error {
	_, err := r.db.Q(ctx).Exec(ctx, `UPDATE attendance_sessions SET qr_token_hash = $2, qr_expires_at = $3 WHERE id = $1`, sessionID, hash, expiresAt)
	return err
}

func (r *Repository) CloseSession(ctx context.Context, sessionID uuid.UUID, at time.Time) error {
	_, err := r.db.Q(ctx).Exec(ctx,
		`UPDATE attendance_sessions SET status = 'CLOSED', closed_at = $2, qr_token_hash = NULL, qr_expires_at = NULL WHERE id = $1`, sessionID, at)
	return err
}

func (r *Repository) MarkAbsentees(ctx context.Context, sessionID, assignmentID, by uuid.UUID, at time.Time) (int, error) {
	tag, err := r.db.Q(ctx).Exec(ctx,
		`INSERT INTO attendance_records (id, attendance_session_id, subject_enrollment_id, status, method, recorded_at, recorded_by)
		 SELECT gen_random_uuid(), $1, se.id, 'ABSENT', 'MANUAL', $4, $3
		   FROM subject_enrollments se
		  WHERE se.teaching_assignment_id = $2 AND se.status = 'ACTIVE'
		 ON CONFLICT (attendance_session_id, subject_enrollment_id) DO NOTHING`,
		sessionID, assignmentID, by, at)
	if err != nil {
		return 0, fmt.Errorf("mark absentees: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

const recordColumns = `id, attendance_session_id, subject_enrollment_id, status, method, recorded_at, recorded_by, COALESCE(host(ip_address), '')`

func scanRecord(row interface{ Scan(...any) error }) (*domain.Record, error) {
	var rec domain.Record
	var status, method string
	if err := row.Scan(&rec.ID, &rec.SessionID, &rec.SubjectEnrollmentID, &status, &method, &rec.RecordedAt, &rec.RecordedBy, &rec.IPAddress); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrRecordNotFound
		}
		return nil, err
	}
	rec.Status, rec.Method = domain.RecordStatus(status), domain.Method(method)
	return &rec, nil
}

func (r *Repository) CreateRecord(ctx context.Context, rec *domain.Record) error {
	_, err := r.db.Q(ctx).Exec(ctx,
		`INSERT INTO attendance_records (id, attendance_session_id, subject_enrollment_id, status, method, recorded_at, recorded_by, ip_address)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::inet)`,
		rec.ID, rec.SessionID, rec.SubjectEnrollmentID, string(rec.Status), string(rec.Method), rec.RecordedAt, rec.RecordedBy, rec.IPAddress)
	if name, dup := database.IsUniqueViolation(err); dup && name == "attendance_records_unique_per_session" {
		return domain.ErrAlreadyRegistered
	}
	if err != nil {
		return fmt.Errorf("insert attendance record: %w", err)
	}
	return nil
}

func (r *Repository) GetRecord(ctx context.Context, id uuid.UUID, forUpdate bool) (*domain.Record, error) {
	sql := `SELECT ` + recordColumns + ` FROM attendance_records WHERE id = $1`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	return scanRecord(r.db.Q(ctx).QueryRow(ctx, sql, id))
}

func (r *Repository) UpdateRecordStatus(ctx context.Context, id uuid.UUID, status domain.RecordStatus) (*domain.Record, error) {
	return scanRecord(r.db.Q(ctx).QueryRow(ctx,
		`UPDATE attendance_records SET status = $2 WHERE id = $1 RETURNING `+recordColumns, id, string(status)))
}

func (c *countsAcc) add(status string) {
	switch domain.RecordStatus(status) {
	case domain.StatusPresent:
		c.Present++
	case domain.StatusLate:
		c.Late++
	case domain.StatusAbsent:
		c.Absent++
	case domain.StatusJustified:
		c.Justified++
	}
}

type countsAcc struct{ domain.Counts }

// SessionDetail returns the session and one row per student (two queries): every
// ACTIVE subject enrollment of the assignment, plus any withdrawn one that has a record.
func (r *Repository) SessionDetail(ctx context.Context, sessionID uuid.UUID) (*domain.SessionDetail, error) {
	q := r.db.Q(ctx)
	sess, err := scanSession(q.QueryRow(ctx, `SELECT `+sessionColumns+` FROM attendance_sessions WHERE id = $1`, sessionID))
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `
		SELECT se.id, u.id, u.institutional_number, u.first_name, u.last_name,
		       r.id, r.status, r.method, r.recorded_at, r.recorded_by, COALESCE(host(r.ip_address), '')
		  FROM subject_enrollments se
		  JOIN users u ON u.id = se.student_id
		  LEFT JOIN attendance_records r ON r.attendance_session_id = $1 AND r.subject_enrollment_id = se.id
		 WHERE se.teaching_assignment_id = $2 AND (se.status = 'ACTIVE' OR r.id IS NOT NULL)
		 ORDER BY u.last_name, u.first_name, u.institutional_number, se.id`, sessionID, sess.AssignmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	detail := &domain.SessionDetail{Session: *sess, Rows: []domain.SessionRow{}}
	var acc countsAcc
	for rows.Next() {
		var row domain.SessionRow
		var recID *uuid.UUID
		var status, method *string
		var at *time.Time
		var by *uuid.UUID
		var ip string
		if err := rows.Scan(&row.SubjectEnrollmentID, &row.Student.ID, &row.Student.InstitutionalNumber, &row.Student.FirstName, &row.Student.LastName,
			&recID, &status, &method, &at, &by, &ip); err != nil {
			return nil, err
		}
		if recID != nil && status != nil && method != nil && at != nil && by != nil {
			row.Record = &domain.Record{ID: *recID, SessionID: sessionID, SubjectEnrollmentID: row.SubjectEnrollmentID,
				Status: domain.RecordStatus(*status), Method: domain.Method(*method), RecordedAt: *at, RecordedBy: *by, IPAddress: ip}
			acc.add(*status)
		}
		detail.Rows = append(detail.Rows, row)
	}
	detail.Counts = acc.Counts
	return detail, rows.Err()
}

// AssignmentOverview runs two aggregate queries regardless of the group size.
func (r *Repository) AssignmentOverview(ctx context.Context, assignmentID uuid.UUID) (*domain.AssignmentOverview, error) {
	q := r.db.Q(ctx)
	ov := &domain.AssignmentOverview{AssignmentID: assignmentID, Sessions: []domain.SessionSummary{}, Students: []domain.StudentSummary{}}

	srows, err := q.Query(ctx, `
		SELECT s.id, s.teaching_assignment_id, s.schedule_id, s.session_date, s.status, s.opened_by, s.opened_at, s.closed_at,
		       '', NULL::timestamptz, s.late_after_minutes, s.lates_per_absence,
		       COUNT(r.id) FILTER (WHERE r.status = 'PRESENT'), COUNT(r.id) FILTER (WHERE r.status = 'LATE'),
		       COUNT(r.id) FILTER (WHERE r.status = 'ABSENT'), COUNT(r.id) FILTER (WHERE r.status = 'JUSTIFIED')
		  FROM attendance_sessions s
		  LEFT JOIN attendance_records r ON r.attendance_session_id = s.id
		 WHERE s.teaching_assignment_id = $1
		 GROUP BY s.id
		 ORDER BY s.opened_at DESC, s.id`, assignmentID)
	if err != nil {
		return nil, err
	}
	for srows.Next() {
		var s domain.Session
		var status string
		var c domain.Counts
		if err := srows.Scan(&s.ID, &s.AssignmentID, &s.ScheduleID, &s.Date, &status, &s.OpenedBy, &s.OpenedAt, &s.ClosedAt,
			&s.QRTokenHash, &s.QRExpiresAt, &s.LateAfterMinutes, &s.LatesPerAbsence,
			&c.Present, &c.Late, &c.Absent, &c.Justified); err != nil {
			srows.Close()
			return nil, err
		}
		s.Status = domain.SessionStatus(status)
		if len(ov.Sessions) == 0 {
			ov.LatesPerAbsence = s.LatesPerAbsence // most recently opened session
		}
		ov.Sessions = append(ov.Sessions, domain.SessionSummary{Session: s, Counts: c})
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return nil, err
	}

	rows, err := q.Query(ctx, `
		SELECT se.id, u.id, u.institutional_number, u.first_name, u.last_name, se.status,
		       COUNT(r.id) FILTER (WHERE r.status = 'PRESENT'), COUNT(r.id) FILTER (WHERE r.status = 'LATE'),
		       COUNT(r.id) FILTER (WHERE r.status = 'ABSENT'), COUNT(r.id) FILTER (WHERE r.status = 'JUSTIFIED')
		  FROM subject_enrollments se
		  JOIN users u ON u.id = se.student_id
		  LEFT JOIN attendance_records r ON r.subject_enrollment_id = se.id
		 WHERE se.teaching_assignment_id = $1
		 GROUP BY se.id, u.id
		 ORDER BY u.last_name, u.first_name, u.institutional_number, se.id`, assignmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st domain.StudentSummary
		if err := rows.Scan(&st.SubjectEnrollmentID, &st.Student.ID, &st.Student.InstitutionalNumber, &st.Student.FirstName, &st.Student.LastName,
			&st.Status, &st.Counts.Present, &st.Counts.Late, &st.Counts.Absent, &st.Counts.Justified); err != nil {
			return nil, err
		}
		ov.Students = append(ov.Students, st)
	}
	return ov, rows.Err()
}

// StudentAttendance reads every attempt of a student with all its records in ONE query.
func (r *Repository) StudentAttendance(ctx context.Context, studentID uuid.UUID, scope ports.ViewScope) ([]domain.StudentAttendance, error) {
	rows, err := r.db.Q(ctx).Query(ctx, `
		SELECT se.id, ta.id, se.attempt_number, t.id, t.name, g.code, s.id, s.code, s.name,
		       r.id, ses.id, ses.session_date, r.status, r.method, r.recorded_at, ses.lates_per_absence
		  FROM subject_enrollments se
		  JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id
		  JOIN groups g ON g.id = ta.group_id
		  JOIN academic_terms t ON t.id = g.academic_term_id
		  JOIN subjects s ON s.id = ta.subject_id
		  LEFT JOIN attendance_records r ON r.subject_enrollment_id = se.id
		  LEFT JOIN attendance_sessions ses ON ses.id = r.attendance_session_id
		 WHERE se.student_id = $1
		   AND ($2 OR ($3::uuid IS NOT NULL AND ta.teacher_id = $3) OR ($4::uuid IS NOT NULL AND g.tutor_id = $4))
		 ORDER BY t.start_date DESC, s.name, se.attempt_number, se.id, ses.session_date, ses.opened_at`,
		studentID, scope.All, scope.TeacherID, scope.TutorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.StudentAttendance{}
	var acc countsAcc
	flush := func() {
		if n := len(out); n > 0 {
			out[n-1].Counts = acc.Counts
		}
		acc = countsAcc{}
	}
	for rows.Next() {
		var sa domain.StudentAttendance
		var recID, sessID *uuid.UUID
		var date, at *time.Time
		var status, method *string
		var lates *int
		if err := rows.Scan(&sa.SubjectEnrollmentID, &sa.AssignmentID, &sa.AttemptNumber, &sa.TermID, &sa.TermName, &sa.GroupCode,
			&sa.SubjectID, &sa.SubjectCode, &sa.SubjectName, &recID, &sessID, &date, &status, &method, &at, &lates); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].SubjectEnrollmentID != sa.SubjectEnrollmentID {
			flush()
			sa.Records = []domain.StudentRecord{}
			out = append(out, sa)
		}
		cur := &out[len(out)-1]
		if recID != nil && sessID != nil && date != nil && status != nil && method != nil && at != nil {
			cur.Records = append(cur.Records, domain.StudentRecord{RecordID: *recID, SessionID: *sessID, SessionDate: *date,
				Status: domain.RecordStatus(*status), Method: domain.Method(*method), RecordedAt: *at})
			acc.add(*status)
			if lates != nil {
				cur.LatesPerAbsence = *lates // rows are ordered by date: the last one wins (most recent session)
			}
		}
	}
	flush()
	return out, rows.Err()
}

// AcademicReader answers attendance's questions about academic data. It only reads.
type AcademicReader struct{ db *database.DB }

func NewAcademicReader(db *database.DB) *AcademicReader { return &AcademicReader{db: db} }

func (r *AcademicReader) Assignment(ctx context.Context, id uuid.UUID, lockTerm bool) (*domain.AssignmentRef, error) {
	sql := `SELECT ta.id, ta.teacher_id, g.tutor_id, t.id, t.status
		  FROM teaching_assignments ta
		  JOIN groups g ON g.id = ta.group_id
		  JOIN academic_terms t ON t.id = g.academic_term_id
		 WHERE ta.id = $1`
	if lockTerm {
		sql += ` FOR SHARE OF t`
	}
	var a domain.AssignmentRef
	err := r.db.Q(ctx).QueryRow(ctx, sql, id).Scan(&a.ID, &a.TeacherID, &a.GroupTutorID, &a.TermID, &a.TermStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrAssignmentNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *AcademicReader) ScheduleBelongs(ctx context.Context, scheduleID, assignmentID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.Q(ctx).QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schedules WHERE id = $1 AND teaching_assignment_id = $2)`, scheduleID, assignmentID).Scan(&ok)
	return ok, err
}

func (r *AcademicReader) StudentEnrollment(ctx context.Context, assignmentID, studentID uuid.UUID) (*domain.EnrollmentRef, error) {
	var e domain.EnrollmentRef
	err := r.db.Q(ctx).QueryRow(ctx,
		`SELECT id, status FROM subject_enrollments WHERE teaching_assignment_id = $1 AND student_id = $2
		  ORDER BY attempt_number DESC LIMIT 1`, assignmentID, studentID).Scan(&e.ID, &e.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotEnrolled
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *AcademicReader) EnrollmentInAssignment(ctx context.Context, enrollmentID, assignmentID uuid.UUID) (*domain.EnrollmentRef, error) {
	var e domain.EnrollmentRef
	err := r.db.Q(ctx).QueryRow(ctx,
		`SELECT id, status FROM subject_enrollments WHERE id = $1 AND teaching_assignment_id = $2`, enrollmentID, assignmentID).Scan(&e.ID, &e.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrBodyEnrollmentNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
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
