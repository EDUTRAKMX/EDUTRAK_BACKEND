// Package postgres implements the analytics readers and the alert repository on PostgreSQL.
// Academic, Grades, Attendance, Justifications and Tutoring tables are read through joins and
// aggregates; the only table Analytics writes is alerts.
package postgres

import (
	"context"
	"errors"

	"github.com/edutrak/backend/internal/analytics/domain"
	"github.com/edutrak/backend/internal/analytics/ports"
	"github.com/edutrak/backend/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Facts struct{ db *database.DB }

func NewFacts(db *database.DB) *Facts { return &Facts{db: db} }

const groupCols = `g.id, g.code, g.level, g.tutor_id, c.id, c.code, c.name, t.id, t.name, t.status, t.start_date, t.end_date`

const groupFrom = ` FROM groups g JOIN careers c ON c.id = g.career_id JOIN academic_terms t ON t.id = g.academic_term_id`

func scanGroup(row interface{ Scan(...any) error }, extra ...any) (domain.GroupInfo, error) {
	var g domain.GroupInfo
	dst := append([]any{&g.ID, &g.Code, &g.Level, &g.TutorID, &g.CareerID, &g.CareerCode, &g.CareerName,
		&g.TermID, &g.TermName, &g.TermStatus, &g.TermStart, &g.TermEnd}, extra...)
	err := row.Scan(dst...)
	return g, err
}

func (f *Facts) Group(ctx context.Context, id uuid.UUID, lock bool) (*domain.GroupInfo, error) {
	sql := `SELECT ` + groupCols + groupFrom + ` WHERE g.id = $1`
	if lock {
		sql += ` FOR SHARE OF g, t`
	}
	g, err := scanGroup(f.db.Q(ctx).QueryRow(ctx, sql, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrGroupNotFound
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (f *Facts) StudentExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var ok bool
	err := f.db.Q(ctx).QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = $1 AND r.code = 'STUDENT')`, id).Scan(&ok)
	return ok, err
}

func (f *Facts) Enrollment(ctx context.Context, studentID, groupID uuid.UUID) (*domain.EnrollmentRef, error) {
	var status string
	g, err := scanGroup(f.db.Q(ctx).QueryRow(ctx,
		`SELECT `+groupCols+`, e.status`+groupFrom+` JOIN enrollments e ON e.group_id = g.id WHERE e.student_id = $1 AND g.id = $2`, studentID, groupID), &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrContextNotFound
	}
	if err != nil {
		return nil, err
	}
	return &domain.EnrollmentRef{GroupInfo: g, StudentID: studentID, Status: status}, nil
}

func (f *Facts) Enrollments(ctx context.Context, studentID uuid.UUID, scope ports.Scope) ([]domain.EnrollmentRef, error) {
	rows, err := f.db.Q(ctx).Query(ctx,
		`SELECT `+groupCols+`, e.status`+groupFrom+` JOIN enrollments e ON e.group_id = g.id
		  WHERE e.student_id = $3 AND ($1 OR ($2::uuid IS NOT NULL AND g.tutor_id = $2))
		  ORDER BY (e.status = 'ACTIVE') DESC, t.start_date DESC, e.enrolled_at DESC, g.id`, scope.All, scope.TutorID, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.EnrollmentRef{}
	for rows.Next() {
		var status string
		g, err := scanGroup(rows, &status)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.EnrollmentRef{GroupInfo: g, StudentID: studentID, Status: status})
	}
	return out, rows.Err()
}

// Students loads, with five queries whatever the size of the group, everything the indicators
// need. Rules of the load:
//   - without studentID: the group's ACTIVE enrollments; with it: that one student, any status;
//   - attendance: only records of CLOSED sessions;
//   - grades: every unit of the subject, LEFT JOINed with the captured score (null = pending).
func (f *Facts) Students(ctx context.Context, groupID uuid.UUID, studentID *uuid.UUID) ([]domain.StudentFacts, error) {
	q := f.db.Q(ctx)
	// The same enrollment filter is used by every query below.
	const enrollmentFilter = `e.group_id = $1 AND (e.status = 'ACTIVE' OR $2::uuid IS NOT NULL) AND ($2::uuid IS NULL OR e.student_id = $2)`

	rows, err := q.Query(ctx, `
		SELECT u.id, u.institutional_number, u.first_name, u.last_name, e.status,
		       se.id, ta.id, s.code, s.name, se.attempt_number, se.status,
		       (SELECT (array_agg(ses.lates_per_absence ORDER BY ses.session_date DESC, ses.opened_at DESC))[1]::int
		          FROM attendance_sessions ses WHERE ses.teaching_assignment_id = ta.id)
		  FROM enrollments e
		  JOIN users u ON u.id = e.student_id
		  LEFT JOIN (subject_enrollments se
		             JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id
		             JOIN subjects s ON s.id = ta.subject_id)
		         ON se.student_id = e.student_id AND ta.group_id = e.group_id
		 WHERE `+enrollmentFilter+`
		 ORDER BY u.last_name, u.first_name, u.id, s.code, se.attempt_number, se.id`, groupID, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.StudentFacts
	byStudent := map[uuid.UUID]int{}
	bySE := map[uuid.UUID][2]int{} // subject enrollment -> (student index, subject index)
	for rows.Next() {
		var st domain.StudentFacts
		var seID, taID *uuid.UUID
		var code, name, seStatus *string
		var attempt, lates *int
		if err := rows.Scan(&st.StudentID, &st.Number, &st.First, &st.Last, &st.EnrollmentStatus,
			&seID, &taID, &code, &name, &attempt, &seStatus, &lates); err != nil {
			return nil, err
		}
		i, ok := byStudent[st.StudentID]
		if !ok {
			i = len(out)
			byStudent[st.StudentID] = i
			out = append(out, st)
		}
		if seID != nil {
			out[i].Subjects = append(out[i].Subjects, domain.SubjectFacts{
				SubjectEnrollmentID: *seID, AssignmentID: *taID, SubjectCode: *code, SubjectName: *name,
				Attempt: *attempt, Status: *seStatus, LatesPerAbsence: lates, Units: []domain.UnitFact{},
			})
			bySE[*seID] = [2]int{i, len(out[i].Subjects) - 1}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(out) == 0 {
		return []domain.StudentFacts{}, nil
	}
	subject := func(id uuid.UUID) *domain.SubjectFacts {
		if p, ok := bySE[id]; ok {
			return &out[p[0]].Subjects[p[1]]
		}
		return nil
	}

	urows, err := q.Query(ctx, `
		SELECT se.id, su.id, su.unit_number, su.name, (g.score * 100)::int
		  FROM enrollments e
		  JOIN subject_enrollments se ON se.student_id = e.student_id
		  JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id AND ta.group_id = e.group_id
		  JOIN subject_units su ON su.subject_id = ta.subject_id
		  LEFT JOIN grades g ON g.subject_enrollment_id = se.id AND g.subject_unit_id = su.id
		 WHERE `+enrollmentFilter+`
		 ORDER BY se.id, su.unit_number`, groupID, studentID)
	if err != nil {
		return nil, err
	}
	defer urows.Close()
	for urows.Next() {
		var se uuid.UUID
		var u domain.UnitFact
		if err := urows.Scan(&se, &u.UnitID, &u.Number, &u.Name, &u.Hundredths); err != nil {
			return nil, err
		}
		if s := subject(se); s != nil {
			s.Units = append(s.Units, u)
		}
	}
	if err := urows.Err(); err != nil {
		return nil, err
	}
	urows.Close()

	rrows, err := q.Query(ctx, `
		SELECT r.subject_enrollment_id, r.id, r.status, ses.session_date, ses.opened_at
		  FROM enrollments e
		  JOIN subject_enrollments se ON se.student_id = e.student_id
		  JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id AND ta.group_id = e.group_id
		  JOIN attendance_records r ON r.subject_enrollment_id = se.id
		  JOIN attendance_sessions ses ON ses.id = r.attendance_session_id AND ses.status = 'CLOSED'
		 WHERE `+enrollmentFilter+`
		 ORDER BY r.subject_enrollment_id, ses.session_date, ses.opened_at, r.id`, groupID, studentID)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var se uuid.UUID
		var rec domain.RecordFact
		var status string
		if err := rrows.Scan(&se, &rec.RecordID, &status, &rec.Date, &rec.OpenedAt); err != nil {
			return nil, err
		}
		rec.Status = recordStatus(status)
		if s := subject(se); s != nil {
			s.Records = append(s.Records, rec)
		}
	}
	if err := rrows.Err(); err != nil {
		return nil, err
	}
	rrows.Close()

	qrows, err := q.Query(ctx, `
		SELECT r.subject_enrollment_id, jr.id, jr.attendance_record_id, jr.status, jr.created_at
		  FROM enrollments e
		  JOIN subject_enrollments se ON se.student_id = e.student_id
		  JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id AND ta.group_id = e.group_id
		  JOIN attendance_records r ON r.subject_enrollment_id = se.id
		  JOIN justification_requests jr ON jr.attendance_record_id = r.id
		 WHERE `+enrollmentFilter+`
		 ORDER BY r.subject_enrollment_id, jr.created_at, jr.id`, groupID, studentID)
	if err != nil {
		return nil, err
	}
	defer qrows.Close()
	for qrows.Next() {
		var se uuid.UUID
		var rq domain.RequestFact
		var status string
		if err := qrows.Scan(&se, &rq.RequestID, &rq.RecordID, &status, &rq.CreatedAt); err != nil {
			return nil, err
		}
		rq.Status = domain.RequestStatus(status)
		if s := subject(se); s != nil {
			s.Requests = append(s.Requests, rq)
		}
	}
	if err := qrows.Err(); err != nil {
		return nil, err
	}
	qrows.Close()

	frows, err := q.Query(ctx, `
		SELECT f.student_id, count(*), max(f.followup_date)
		  FROM tutoring_followups f
		 WHERE f.group_id = $1 AND ($2::uuid IS NULL OR f.student_id = $2)
		 GROUP BY f.student_id`, groupID, studentID)
	if err != nil {
		return nil, err
	}
	defer frows.Close()
	for frows.Next() {
		var id uuid.UUID
		var fu domain.FollowUpFacts
		if err := frows.Scan(&id, &fu.Count, &fu.Last); err != nil {
			return nil, err
		}
		if i, ok := byStudent[id]; ok {
			out[i].FollowUps = fu
		}
	}
	return out, frows.Err()
}
