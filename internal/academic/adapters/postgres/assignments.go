package postgres

import (
	"context"

	"github.com/edutrak/backend/internal/academic/domain"
	"github.com/edutrak/backend/internal/academic/ports"
	"github.com/edutrak/backend/internal/platform/database"
	"github.com/google/uuid"
)

type AssignmentRepository struct{ db *database.DB }

func NewAssignmentRepository(db *database.DB) *AssignmentRepository {
	return &AssignmentRepository{db: db}
}

const assignmentSelect = `SELECT ta.id, ta.group_id, ta.subject_id, ta.teacher_id, g.academic_term_id, g.tutor_id,
		g.code, s.code, s.name, u.first_name || ' ' || u.last_name
	FROM teaching_assignments ta
	JOIN groups g ON g.id = ta.group_id
	JOIN subjects s ON s.id = ta.subject_id
	JOIN users u ON u.id = ta.teacher_id`

// assignmentScope restricts rows to the caller's relationships
// ($1 user, $2 all, $3 teacher, $4 tutor, $5 student; aliases ta and g).
const assignmentScope = `($2
	OR ($3 AND ta.teacher_id = $1)
	OR ($4 AND g.tutor_id = $1)
	OR ($5 AND EXISTS (SELECT 1 FROM subject_enrollments se
	                    WHERE se.teaching_assignment_id = ta.id AND se.student_id = $1 AND se.status = 'ACTIVE')))`

func scanAssignment(row interface{ Scan(...any) error }) (*domain.TeachingAssignment, error) {
	var a domain.TeachingAssignment
	if err := row.Scan(&a.ID, &a.GroupID, &a.SubjectID, &a.TeacherID, &a.TermID, &a.GroupTutorID,
		&a.GroupCode, &a.SubjectCode, &a.SubjectName, &a.TeacherName); err != nil {
		return nil, notFoundOr(err, domain.ErrAssignmentNotFound)
	}
	return &a, nil
}

func (r *AssignmentRepository) Create(ctx context.Context, a *domain.TeachingAssignment) error {
	_, err := r.db.Q(ctx).Exec(ctx,
		`INSERT INTO teaching_assignments (id, group_id, subject_id, teacher_id) VALUES ($1, $2, $3, $4)`,
		a.ID, a.GroupID, a.SubjectID, a.TeacherID)
	return uniqueErr("insert teaching assignment", err,
		map[string]error{"teaching_assignments_group_id_subject_id_key": domain.ErrAssignmentExists})
}

func (r *AssignmentRepository) Get(ctx context.Context, id uuid.UUID) (*domain.TeachingAssignment, error) {
	return scanAssignment(r.db.Q(ctx).QueryRow(ctx, assignmentSelect+` WHERE ta.id = $1`, id))
}

func (r *AssignmentRepository) GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.TeachingAssignment, error) {
	// Lock only the assignment row (FOR UPDATE OF ta); the joined rows are just read.
	return scanAssignment(r.db.Q(ctx).QueryRow(ctx, assignmentSelect+` WHERE ta.id = $1 FOR UPDATE OF ta`, id))
}

func (r *AssignmentRepository) List(ctx context.Context, s ports.Scope, f ports.AssignmentFilter) ([]domain.TeachingAssignment, error) {
	rows, err := r.db.Q(ctx).Query(ctx, assignmentSelect+`
		WHERE ($6::uuid IS NULL OR ta.group_id = $6) AND `+assignmentScope+`
		ORDER BY g.code, s.name`,
		s.UserID, s.All, s.Teacher, s.Tutor, s.Student, f.GroupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TeachingAssignment{}
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (r *AssignmentRepository) SetTeacher(ctx context.Context, id, teacherID uuid.UUID) error {
	tag, err := r.db.Q(ctx).Exec(ctx, `UPDATE teaching_assignments SET teacher_id = $2 WHERE id = $1`, id, teacherID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrAssignmentNotFound
	}
	return nil
}

func (r *AssignmentRepository) HasSubjectEnrollment(ctx context.Context, assignmentID, studentID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.Q(ctx).QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM subject_enrollments WHERE teaching_assignment_id = $1 AND student_id = $2 AND status = 'ACTIVE')`,
		assignmentID, studentID).Scan(&ok)
	return ok, err
}

const scheduleColumns = `sc.id, sc.teaching_assignment_id, sc.day_of_week,
	to_char(sc.start_time, 'HH24:MI'), to_char(sc.end_time, 'HH24:MI'), COALESCE(sc.classroom, '')`

func (r *AssignmentRepository) CreateSchedule(ctx context.Context, s *domain.Schedule) error {
	_, err := r.db.Q(ctx).Exec(ctx,
		`INSERT INTO schedules (id, teaching_assignment_id, day_of_week, start_time, end_time, classroom)
		 VALUES ($1, $2, $3, $4::time, $5::time, NULLIF($6, ''))`,
		s.ID, s.TeachingAssignmentID, s.DayOfWeek, s.StartTime, s.EndTime, s.Classroom)
	return err
}

func (r *AssignmentRepository) ListSchedules(ctx context.Context, assignmentID uuid.UUID) ([]domain.Schedule, error) {
	rows, err := r.db.Q(ctx).Query(ctx,
		`SELECT `+scheduleColumns+` FROM schedules sc WHERE sc.teaching_assignment_id = $1 ORDER BY sc.day_of_week, sc.start_time`, assignmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Schedule{}
	for rows.Next() {
		var s domain.Schedule
		if err := rows.Scan(&s.ID, &s.TeachingAssignmentID, &s.DayOfWeek, &s.StartTime, &s.EndTime, &s.Classroom); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *AssignmentRepository) ListScheduleEntries(ctx context.Context, s ports.Scope, f ports.AssignmentFilter) ([]domain.ScheduleEntry, error) {
	rows, err := r.db.Q(ctx).Query(ctx,
		`SELECT `+scheduleColumns+`, ta.group_id, ta.subject_id, ta.teacher_id, g.academic_term_id, g.tutor_id,
		        g.code, s.code, s.name, u.first_name || ' ' || u.last_name
		   FROM schedules sc
		   JOIN teaching_assignments ta ON ta.id = sc.teaching_assignment_id
		   JOIN groups g ON g.id = ta.group_id
		   JOIN subjects s ON s.id = ta.subject_id
		   JOIN users u ON u.id = ta.teacher_id
		  WHERE ($6::uuid IS NULL OR ta.group_id = $6) AND `+assignmentScope+`
		  ORDER BY sc.day_of_week, sc.start_time, g.code`,
		s.UserID, s.All, s.Teacher, s.Tutor, s.Student, f.GroupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ScheduleEntry{}
	for rows.Next() {
		var e domain.ScheduleEntry
		a := &e.Assignment
		if err := rows.Scan(&e.ID, &e.TeachingAssignmentID, &e.DayOfWeek, &e.StartTime, &e.EndTime, &e.Classroom,
			&a.GroupID, &a.SubjectID, &a.TeacherID, &a.TermID, &a.GroupTutorID,
			&a.GroupCode, &a.SubjectCode, &a.SubjectName, &a.TeacherName); err != nil {
			return nil, err
		}
		a.ID = e.TeachingAssignmentID
		out = append(out, e)
	}
	return out, rows.Err()
}

type SubjectEnrollmentRepository struct{ db *database.DB }

func NewSubjectEnrollmentRepository(db *database.DB) *SubjectEnrollmentRepository {
	return &SubjectEnrollmentRepository{db: db}
}

func (r *SubjectEnrollmentRepository) Create(ctx context.Context, e *domain.SubjectEnrollment) error {
	_, err := r.db.Q(ctx).Exec(ctx,
		`INSERT INTO subject_enrollments (id, student_id, teaching_assignment_id, attempt_number, status) VALUES ($1, $2, $3, $4, $5)`,
		e.ID, e.StudentID, e.TeachingAssignmentID, e.AttemptNumber, string(e.Status))
	return uniqueErr("insert subject enrollment", err,
		map[string]error{"subject_enrollments_attempt_key": domain.ErrSubjectEnrollExists})
}

func (r *SubjectEnrollmentRepository) NextAttempt(ctx context.Context, studentID, subjectID uuid.UUID) (int16, error) {
	var n int16
	err := r.db.Q(ctx).QueryRow(ctx,
		`SELECT COALESCE(MAX(se.attempt_number), 0)::smallint + 1
		   FROM subject_enrollments se JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id
		  WHERE se.student_id = $1 AND ta.subject_id = $2`, studentID, subjectID).Scan(&n)
	return n, err
}

func (r *SubjectEnrollmentRepository) ExistsForAssignment(ctx context.Context, studentID, assignmentID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.Q(ctx).QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM subject_enrollments WHERE student_id = $1 AND teaching_assignment_id = $2)`,
		studentID, assignmentID).Scan(&ok)
	return ok, err
}

func (r *SubjectEnrollmentRepository) EnrollGroup(ctx context.Context, assignmentID uuid.UUID) (int, error) {
	tag, err := r.db.Q(ctx).Exec(ctx,
		`INSERT INTO subject_enrollments (id, student_id, teaching_assignment_id, attempt_number, status)
		 SELECT gen_random_uuid(), e.student_id, ta.id,
		        COALESCE((SELECT MAX(se2.attempt_number)
		                    FROM subject_enrollments se2 JOIN teaching_assignments ta2 ON ta2.id = se2.teaching_assignment_id
		                   WHERE se2.student_id = e.student_id AND ta2.subject_id = ta.subject_id), 0) + 1,
		        'ACTIVE'
		   FROM teaching_assignments ta
		   JOIN enrollments e ON e.group_id = ta.group_id AND e.status = 'ACTIVE'
		  WHERE ta.id = $1
		    AND NOT EXISTS (SELECT 1 FROM subject_enrollments se WHERE se.student_id = e.student_id AND se.teaching_assignment_id = ta.id)`,
		assignmentID)
	if err != nil {
		return 0, uniqueErr("bulk subject enrollments", err,
			map[string]error{"subject_enrollments_attempt_key": domain.ErrSubjectEnrollExists})
	}
	return int(tag.RowsAffected()), nil
}

const subjectEnrollmentSelect = `SELECT se.id, se.student_id, se.teaching_assignment_id, se.attempt_number, se.status,
		u.institutional_number, u.first_name, u.last_name
	FROM subject_enrollments se
	JOIN users u ON u.id = se.student_id`

func (r *SubjectEnrollmentRepository) query(ctx context.Context, sql string, args ...any) ([]domain.SubjectEnrollment, error) {
	rows, err := r.db.Q(ctx).Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.SubjectEnrollment{}
	for rows.Next() {
		var e domain.SubjectEnrollment
		var status string
		if err := rows.Scan(&e.ID, &e.StudentID, &e.TeachingAssignmentID, &e.AttemptNumber, &status,
			&e.Student.InstitutionalNumber, &e.Student.FirstName, &e.Student.LastName); err != nil {
			return nil, err
		}
		e.Status = domain.SubjectEnrollmentStatus(status)
		e.Student.ID = e.StudentID
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *SubjectEnrollmentRepository) ListByAssignment(ctx context.Context, assignmentID uuid.UUID) ([]domain.SubjectEnrollment, error) {
	return r.query(ctx, subjectEnrollmentSelect+
		` WHERE se.teaching_assignment_id = $1 ORDER BY u.last_name, u.first_name, u.institutional_number`, assignmentID)
}

// List: $1 user, $2 all, $3 teacher, $4 student, $5 assignment filter, $6 student filter.
func (r *SubjectEnrollmentRepository) List(ctx context.Context, s ports.Scope, f ports.SubjectEnrollmentFilter) ([]domain.SubjectEnrollment, error) {
	return r.query(ctx, subjectEnrollmentSelect+`
		WHERE ($5::uuid IS NULL OR se.teaching_assignment_id = $5)
		  AND ($6::uuid IS NULL OR se.student_id = $6)
		  AND ($2
		    OR ($3 AND EXISTS (SELECT 1 FROM teaching_assignments ta WHERE ta.id = se.teaching_assignment_id AND ta.teacher_id = $1))
		    OR ($4 AND se.student_id = $1))
		ORDER BY u.last_name, u.first_name, se.teaching_assignment_id, se.attempt_number`,
		s.UserID, s.All, s.Teacher, s.Student, f.TeachingAssignmentID, f.StudentID)
}
