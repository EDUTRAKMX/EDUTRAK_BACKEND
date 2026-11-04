package postgres

import (
	"context"

	"github.com/edutrak/backend/internal/analytics/domain"
	"github.com/edutrak/backend/internal/analytics/ports"
	attendancedomain "github.com/edutrak/backend/internal/attendance/domain"
)

func recordStatus(s string) attendancedomain.RecordStatus { return attendancedomain.RecordStatus(s) }

// Overview aggregates one line per group in SQL, so the director's view never loads the
// individual grades or records. Definitions match AnalyzeGroup: ACTIVE enrollments, ACTIVE
// subject enrollments, records of CLOSED sessions, every unit of the subject (captured or not).
func (f *Facts) Overview(ctx context.Context, scope ports.Scope, flt ports.OverviewFilter) ([]domain.GroupOverview, error) {
	rows, err := f.db.Q(ctx).Query(ctx, `
		SELECT `+groupCols+`,
		       (SELECT count(*) FROM enrollments e WHERE e.group_id = g.id AND e.status = 'ACTIVE'),
		       att.present, att.late, att.absent, att.justified,
		       un.total, un.graded, un.na, un.sa, un.de, un.au,
		       rq.total, rq.pending_tutor, rq.pending_teacher, rq.tutor_rejected, rq.teacher_rejected, rq.approved, rq.absences,
		       (SELECT count(DISTINCT fu.student_id) FROM tutoring_followups fu
		          JOIN enrollments e ON e.student_id = fu.student_id AND e.group_id = g.id AND e.status = 'ACTIVE'
		         WHERE fu.group_id = g.id),
		       (SELECT count(*) FROM alerts a WHERE a.group_id = g.id AND a.status <> 'RESOLVED')
		  `+groupFrom+`
		  LEFT JOIN LATERAL (
		        SELECT count(*) FILTER (WHERE r.status = 'PRESENT') AS present, count(*) FILTER (WHERE r.status = 'LATE') AS late,
		               count(*) FILTER (WHERE r.status = 'ABSENT') AS absent, count(*) FILTER (WHERE r.status = 'JUSTIFIED') AS justified
		          FROM attendance_records r
		          JOIN attendance_sessions ses ON ses.id = r.attendance_session_id AND ses.status = 'CLOSED'
		          JOIN subject_enrollments se ON se.id = r.subject_enrollment_id AND se.status = 'ACTIVE'
		          JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id AND ta.group_id = g.id
		          JOIN enrollments e ON e.student_id = se.student_id AND e.group_id = g.id AND e.status = 'ACTIVE') att ON true
		  LEFT JOIN LATERAL (
		        SELECT count(*) AS total, count(gr.id) AS graded,
		               count(*) FILTER (WHERE gr.score < 80) AS na,
		               count(*) FILTER (WHERE gr.score >= 80 AND gr.score < 90) AS sa,
		               count(*) FILTER (WHERE gr.score >= 90 AND gr.score < 100) AS de,
		               count(*) FILTER (WHERE gr.score = 100) AS au
		          FROM subject_enrollments se
		          JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id AND ta.group_id = g.id
		          JOIN enrollments e ON e.student_id = se.student_id AND e.group_id = g.id AND e.status = 'ACTIVE'
		          JOIN subject_units su ON su.subject_id = ta.subject_id
		          LEFT JOIN grades gr ON gr.subject_enrollment_id = se.id AND gr.subject_unit_id = su.id
		         WHERE se.status = 'ACTIVE') un ON true
		  LEFT JOIN LATERAL (
		        SELECT count(*) AS total,
		               count(*) FILTER (WHERE jr.status = 'PENDING_TUTOR') AS pending_tutor,
		               count(*) FILTER (WHERE jr.status = 'PENDING_TEACHER') AS pending_teacher,
		               count(*) FILTER (WHERE jr.status = 'TUTOR_REJECTED') AS tutor_rejected,
		               count(*) FILTER (WHERE jr.status = 'TEACHER_REJECTED') AS teacher_rejected,
		               count(*) FILTER (WHERE jr.status = 'JUSTIFIED') AS approved,
		               count(DISTINCT jr.attendance_record_id) AS absences
		          FROM justification_requests jr
		          JOIN attendance_records r ON r.id = jr.attendance_record_id
		          JOIN subject_enrollments se ON se.id = r.subject_enrollment_id AND se.status = 'ACTIVE'
		          JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id AND ta.group_id = g.id
		          JOIN enrollments e ON e.student_id = se.student_id AND e.group_id = g.id AND e.status = 'ACTIVE') rq ON true
		 WHERE ($1 OR ($2::uuid IS NOT NULL AND g.tutor_id = $2))
		   AND ($3::uuid IS NULL OR t.id = $3) AND ($4::uuid IS NULL OR c.id = $4)
		 ORDER BY t.start_date DESC, c.code, g.code, g.id`, scope.All, scope.TutorID, flt.TermID, flt.CareerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.GroupOverview{}
	for rows.Next() {
		var o domain.GroupOverview
		g := &o.Group
		if err := rows.Scan(&g.ID, &g.Code, &g.Level, &g.TutorID, &g.CareerID, &g.CareerCode, &g.CareerName, &g.TermID, &g.TermName, &g.TermStatus, &g.TermStart, &g.TermEnd,
			&o.ActiveStudents,
			&o.Attendance.Present, &o.Attendance.Late, &o.Attendance.Absent, &o.Attendance.Justified,
			&o.Grades.Total, &o.Grades.Graded, &o.Grades.NA, &o.Grades.SA, &o.Grades.DE, &o.Grades.AU,
			&o.Requests.Total, &o.Requests.PendingTutor, &o.Requests.PendingTeacher, &o.Requests.TutorRejected, &o.Requests.TeacherRejected, &o.Requests.Approved, &o.Requests.AbsencesWithRequest,
			&o.StudentsWithFollowUp, &o.OpenAlerts); err != nil {
			return nil, err
		}
		o.Grades.Pending = o.Grades.Total - o.Grades.Graded
		out = append(out, o)
	}
	return out, rows.Err()
}
