package postgres

import (
	"context"

	"github.com/edutrak/backend/internal/platform/database"
	"github.com/edutrak/backend/internal/tutoring/domain"
	"github.com/google/uuid"
)

// OverviewReader reads the academic situation of one student in one group. It issues a
// constant number of queries (two) however many subjects the student takes.
type OverviewReader struct{ db *database.DB }

func NewOverviewReader(db *database.DB) *OverviewReader { return &OverviewReader{db: db} }

func (o *OverviewReader) Subjects(ctx context.Context, studentID, groupID uuid.UUID) ([]domain.SubjectOverview, error) {
	rows, err := o.db.Q(ctx).Query(ctx, `
		SELECT se.id, ta.id, s.code, s.name, tu.first_name, tu.last_name, se.attempt_number, se.status,
		       a.present, a.late, a.absent, a.justified, a.lates_per_absence,
		       j.pending, j.approved, j.rejected
		  FROM subject_enrollments se
		  JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id AND ta.group_id = $2
		  JOIN subjects s ON s.id = ta.subject_id
		  JOIN users tu ON tu.id = ta.teacher_id
		  LEFT JOIN LATERAL (
		        SELECT count(*) FILTER (WHERE r.status = 'PRESENT') AS present,
		               count(*) FILTER (WHERE r.status = 'LATE') AS late,
		               count(*) FILTER (WHERE r.status = 'ABSENT') AS absent,
		               count(*) FILTER (WHERE r.status = 'JUSTIFIED') AS justified,
		               (array_agg(ses.lates_per_absence ORDER BY ses.session_date DESC, ses.opened_at DESC))[1]::int AS lates_per_absence
		          FROM attendance_records r JOIN attendance_sessions ses ON ses.id = r.attendance_session_id
		         WHERE r.subject_enrollment_id = se.id) a ON true
		  LEFT JOIN LATERAL (
		        SELECT count(*) FILTER (WHERE jr.status IN ('PENDING_TUTOR', 'PENDING_TEACHER')) AS pending,
		               count(*) FILTER (WHERE jr.status = 'JUSTIFIED') AS approved,
		               count(*) FILTER (WHERE jr.status IN ('TUTOR_REJECTED', 'TEACHER_REJECTED')) AS rejected
		          FROM justification_requests jr JOIN attendance_records r2 ON r2.id = jr.attendance_record_id
		         WHERE r2.subject_enrollment_id = se.id) j ON true
		 WHERE se.student_id = $1
		 ORDER BY s.name, s.code, se.attempt_number, se.id`, studentID, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.SubjectOverview{}
	index := map[uuid.UUID]int{}
	for rows.Next() {
		var s domain.SubjectOverview
		if err := rows.Scan(&s.SubjectEnrollmentID, &s.AssignmentID, &s.SubjectCode, &s.SubjectName, &s.TeacherFirst, &s.TeacherLast,
			&s.AttemptNumber, &s.Status,
			&s.Attendance.Present, &s.Attendance.Late, &s.Attendance.Absent, &s.Attendance.Justified, &s.Attendance.LatesPerAbsence,
			&s.Justifications.Pending, &s.Justifications.Approved, &s.Justifications.Rejected); err != nil {
			return nil, err
		}
		s.Units = []domain.UnitGrade{}
		index[s.SubjectEnrollmentID] = len(out)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(out) == 0 {
		return out, nil
	}

	// Every unit of each subject, with its grade if one was captured (LEFT JOIN: pending != 0.00).
	urows, err := o.db.Q(ctx).Query(ctx, `
		SELECT se.id, su.id, su.unit_number, su.name, (g.score * 100)::int
		  FROM subject_enrollments se
		  JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id AND ta.group_id = $2
		  JOIN subject_units su ON su.subject_id = ta.subject_id
		  LEFT JOIN grades g ON g.subject_enrollment_id = se.id AND g.subject_unit_id = su.id
		 WHERE se.student_id = $1
		 ORDER BY se.id, su.unit_number`, studentID, groupID)
	if err != nil {
		return nil, err
	}
	defer urows.Close()
	for urows.Next() {
		var seID uuid.UUID
		var u domain.UnitGrade
		if err := urows.Scan(&seID, &u.UnitID, &u.UnitNumber, &u.UnitName, &u.Hundredths); err != nil {
			return nil, err
		}
		if i, ok := index[seID]; ok {
			out[i].Units = append(out[i].Units, u)
		}
	}
	return out, urows.Err()
}
