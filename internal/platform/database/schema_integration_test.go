package database_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/edutrak/backend/internal/platform/database"
	"github.com/edutrak/backend/internal/platform/database/dbtest"
	"github.com/edutrak/backend/migrations"
	"github.com/google/uuid"
)

// Fictitious fixtures only.
func insertUser(t *testing.T, db *database.DB, number string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Q(context.Background()).Exec(context.Background(),
		`INSERT INTO users (id, institutional_number, institutional_email, password_hash, first_name, last_name)
		 VALUES ($1, $2, $3, 'x', 'Test', 'User')`, id, number, number+"@uttcampus.edu.mx")
	if err != nil {
		t.Fatalf("insert user %s: %v", number, err)
	}
	return id
}

func TestMigrationsCreateAll23Tables(t *testing.T) {
	db := dbtest.New(t)
	var n int
	rows, err := db.Q(context.Background()).Query(context.Background(),
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' AND table_name <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		have[name] = true
		n++
	}
	want := []string{"users", "roles", "user_roles", "auth_sessions", "careers", "academic_terms", "subjects",
		"curriculum_subjects", "subject_units", "groups", "enrollments", "teaching_assignments", "schedules",
		"subject_enrollments", "grades", "attendance_sessions", "attendance_records", "justification_requests",
		"justification_evidence", "justification_reviews", "tutoring_followups", "alerts", "audit_logs"}
	if n != len(want) {
		t.Fatalf("tables = %d, want %d", n, len(want))
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("missing table %s", w)
		}
	}
	rows.Close()
	// Re-running is a no-op.
	if err := db.Migrate(context.Background(), migrationsFS(), discardLogger()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestUserConstraints(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	insertUser(t, db, "20240001")

	tests := []struct {
		name, number, email, wantConstraint string
	}{
		{"duplicate number", "20240001", "20240001@uttcampus.edu.mx", "users_institutional_number_key"},
		{"7 digits", "2024002", "2024002@uttcampus.edu.mx", "users_institutional_number_format"},
		{"letters", "2024000a", "2024000a@uttcampus.edu.mx", "users_institutional_number_format"},
		{"email mismatch", "20240003", "other@uttcampus.edu.mx", "users_institutional_email_format"},
		{"wrong domain", "20240004", "20240004@gmail.com", "users_institutional_email_format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.Q(ctx).Exec(ctx,
				`INSERT INTO users (id, institutional_number, institutional_email, password_hash, first_name, last_name)
				 VALUES ($1, $2, $3, 'x', 'T', 'U')`, uuid.New(), tt.number, tt.email)
			if err == nil || !strings.Contains(err.Error(), tt.wantConstraint) {
				t.Fatalf("err = %v, want constraint %s", err, tt.wantConstraint)
			}
		})
	}
}

// academicFixture builds the minimal chain needed by grades/attendance tests.
type academicFixture struct {
	teacher, student, subjectEnrollment, unit, session uuid.UUID
}

func buildAcademic(t *testing.T, db *database.DB) academicFixture {
	t.Helper()
	ctx := context.Background()
	q := db.Q(ctx)
	f := academicFixture{
		teacher: insertUser(t, db, "30000001"), student: insertUser(t, db, "40000001"),
		subjectEnrollment: uuid.New(), unit: uuid.New(), session: uuid.New(),
	}
	career, term, subject, group, enrollment, assignment := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO careers (id, code, name) VALUES ($1, 'TIC', 'Tecnologias')`, []any{career}},
		{`INSERT INTO academic_terms (id, name, start_date, end_date) VALUES ($1, '2026-1', '2026-01-05', '2026-04-30')`, []any{term}},
		{`INSERT INTO subjects (id, code, name) VALUES ($1, 'MAT1', 'Matematicas')`, []any{subject}},
		{`INSERT INTO subject_units (id, subject_id, unit_number, name) VALUES ($1, $2, 1, 'Unidad 1')`, []any{f.unit, subject}},
		{`INSERT INTO groups (id, career_id, academic_term_id, code, level) VALUES ($1, $2, $3, 'A1', 1)`, []any{group, career, term}},
		{`INSERT INTO enrollments (id, student_id, group_id) VALUES ($1, $2, $3)`, []any{enrollment, f.student, group}},
		{`INSERT INTO teaching_assignments (id, group_id, subject_id, teacher_id) VALUES ($1, $2, $3, $4)`, []any{assignment, group, subject, f.teacher}},
		{`INSERT INTO subject_enrollments (id, enrollment_id, teaching_assignment_id) VALUES ($1, $2, $3)`, []any{f.subjectEnrollment, enrollment, assignment}},
		{`INSERT INTO attendance_sessions (id, teaching_assignment_id, session_date, opened_by) VALUES ($1, $2, '2026-02-02', $3)`, []any{f.session, assignment, f.teacher}},
	}
	for _, s := range steps {
		if _, err := q.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("fixture %q: %v", s.sql, err)
		}
	}
	return f
}

func TestGradeScoreRange(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	f := buildAcademic(t, db)

	tests := []struct {
		score string
		ok    bool
	}{
		{"0.00", true}, {"79.99", true}, {"80.00", true}, {"89.99", true}, {"90.00", true},
		{"99.99", true}, {"100.00", true}, {"100.01", false}, {"-0.01", false},
	}
	for _, tt := range tests {
		t.Run(tt.score, func(t *testing.T) {
			_, err := db.Q(ctx).Exec(ctx, `DELETE FROM grades`)
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Q(ctx).Exec(ctx,
				`INSERT INTO grades (id, subject_enrollment_id, subject_unit_id, score, captured_by) VALUES ($1, $2, $3, $4::numeric, $5)`,
				uuid.New(), f.subjectEnrollment, f.unit, tt.score, f.teacher)
			if tt.ok != (err == nil) {
				t.Fatalf("score %s: err = %v, want ok=%v", tt.score, err, tt.ok)
			}
		})
	}
}

func TestGradeUniquePerUnitAndScoreNotRounded(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	f := buildAcademic(t, db)
	ins := `INSERT INTO grades (id, subject_enrollment_id, subject_unit_id, score, captured_by) VALUES ($1, $2, $3, $4::numeric, $5)`
	if _, err := db.Q(ctx).Exec(ctx, ins, uuid.New(), f.subjectEnrollment, f.unit, "89.99", f.teacher); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.Q(ctx).QueryRow(ctx, `SELECT score::text FROM grades`).Scan(&got); err != nil || got != "89.99" {
		t.Fatalf("score = %q, err = %v", got, err)
	}
	if _, err := db.Q(ctx).Exec(ctx, ins, uuid.New(), f.subjectEnrollment, f.unit, "90.00", f.teacher); err == nil {
		t.Fatal("second grade for same enrollment+unit must violate UNIQUE")
	}
}

func TestDoubleAttendanceRejected(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	f := buildAcademic(t, db)
	ins := `INSERT INTO attendance_records (id, attendance_session_id, subject_enrollment_id, status, method, recorded_by)
	        VALUES ($1, $2, $3, 'PRESENT', 'QR', $4)`
	if _, err := db.Q(ctx).Exec(ctx, ins, uuid.New(), f.session, f.subjectEnrollment, f.student); err != nil {
		t.Fatal(err)
	}
	_, err := db.Q(ctx).Exec(ctx, ins, uuid.New(), f.session, f.subjectEnrollment, f.student)
	if c, ok := database.IsUniqueViolation(err); !ok || c != "attendance_records_unique_per_session" {
		t.Fatalf("err = %v, want unique violation", err)
	}
}

func TestConcurrentAttendanceOnlyOneWins(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	f := buildAcademic(t, db)

	const workers = 12
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			_, err := db.Q(ctx).Exec(ctx,
				`INSERT INTO attendance_records (id, attendance_session_id, subject_enrollment_id, status, method, recorded_by)
				 VALUES ($1, $2, $3, 'PRESENT', 'QR', $4)`, uuid.New(), f.session, f.subjectEnrollment, f.student)
			results <- err
		}()
	}
	wins := 0
	for i := 0; i < workers; i++ {
		if err := <-results; err == nil {
			wins++
		} else if _, dup := database.IsUniqueViolation(err); !dup {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d concurrent inserts succeeded, want exactly 1", wins)
	}
}

func TestForeignKeysRejectMissingReferences(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	_, err := db.Q(ctx).Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) VALUES ($1, 1)`, uuid.New())
	if err == nil || !strings.Contains(err.Error(), "foreign key") {
		t.Fatalf("err = %v, want FK violation", err)
	}
	_, err = db.Q(ctx).Exec(ctx,
		`INSERT INTO attendance_records (id, attendance_session_id, subject_enrollment_id, status, method, recorded_by)
		 VALUES ($1, $2, $3, 'PRESENT', 'QR', $4)`, uuid.New(), uuid.New(), uuid.New(), uuid.New())
	if err == nil || !strings.Contains(err.Error(), "foreign key") {
		t.Fatalf("err = %v, want FK violation", err)
	}
	f := buildAcademic(t, db)
	var groupID uuid.UUID
	if err := db.Q(ctx).QueryRow(ctx, `SELECT id FROM groups`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	_, err = db.Q(ctx).Exec(ctx, `UPDATE groups SET tutor_id = $2 WHERE id = $1`, groupID, uuid.New())
	if err == nil || !strings.Contains(err.Error(), "groups_tutor_id_fkey") {
		t.Fatalf("groups.tutor_id with unknown user: err = %v, want FK violation", err)
	}
	if _, err := db.Q(ctx).Exec(ctx, `UPDATE groups SET tutor_id = $2 WHERE id = $1`, groupID, f.teacher); err != nil {
		t.Fatalf("groups.tutor_id with real user: %v", err)
	}
	_, err = db.Q(ctx).Exec(ctx, `INSERT INTO grades (id, subject_enrollment_id, subject_unit_id, score, captured_by) VALUES ($1, $2, $3, 50, $4)`,
		uuid.New(), uuid.New(), f.unit, f.teacher)
	if err == nil || !strings.Contains(err.Error(), "foreign key") {
		t.Fatalf("grade for unknown subject_enrollment: err = %v, want FK violation", err)
	}
}

func TestJustificationConstraints(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	f := buildAcademic(t, db)
	rec := uuid.New()
	if _, err := db.Q(ctx).Exec(ctx,
		`INSERT INTO attendance_records (id, attendance_session_id, subject_enrollment_id, status, method, recorded_by)
		 VALUES ($1, $2, $3, 'ABSENT', 'MANUAL', $4)`, rec, f.session, f.subjectEnrollment, f.teacher); err != nil {
		t.Fatal(err)
	}
	insReq := `INSERT INTO justification_requests (id, attendance_record_id, student_id, reason, status) VALUES ($1, $2, $3, 'enfermedad', $4)`
	first := uuid.New()
	if _, err := db.Q(ctx).Exec(ctx, insReq, first, rec, f.student, "PENDING_TUTOR"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Q(ctx).Exec(ctx, insReq, uuid.New(), rec, f.student, "PENDING_TUTOR"); err == nil {
		t.Fatal("two open requests for the same absence must be rejected")
	}
	if _, err := db.Q(ctx).Exec(ctx, `UPDATE justification_requests SET status = 'TUTOR_REJECTED' WHERE id = $1`, first); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Q(ctx).Exec(ctx, insReq, uuid.New(), rec, f.student, "PENDING_TUTOR"); err != nil {
		t.Fatalf("new request after rejection should be allowed: %v", err)
	}
	if _, err := db.Q(ctx).Exec(ctx, insReq, uuid.New(), uuid.New(), f.student, "BOGUS"); err == nil {
		t.Fatal("invalid status must be rejected")
	}
	if _, err := db.Q(ctx).Exec(ctx,
		`INSERT INTO justification_evidence (id, justification_request_id, storage_key, original_filename, mime_type, size_bytes)
		 VALUES ($1, $2, 'k1', 'a.pdf', 'application/pdf', 10485761)`, uuid.New(), first); err == nil {
		t.Fatal("evidence over 10 MB must be rejected")
	}
}

func mustExec(t *testing.T, db *database.DB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Q(context.Background()).Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func execErr(db *database.DB, sql string, args ...any) error {
	_, err := db.Q(context.Background()).Exec(context.Background(), sql, args...)
	return err
}

func TestAcademicTermMatchesDictionary(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()

	var n int
	_ = db.Q(ctx).QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'academic_terms' AND column_name = 'is_current'`).Scan(&n)
	if n != 0 {
		t.Fatal("academic_terms.is_current must not exist; status is the only source of term state")
	}

	mustExec(t, db, `INSERT INTO academic_terms (id, name, start_date, end_date) VALUES ($1, '2026-1', '2026-01-05', '2026-04-30')`, uuid.New())
	var status string
	_ = db.Q(ctx).QueryRow(ctx, `SELECT status FROM academic_terms`).Scan(&status)
	if status != "draft" {
		t.Fatalf("default status = %q, want draft", status)
	}
	for _, ok := range []string{"draft", "active", "closed"} {
		mustExec(t, db, `INSERT INTO academic_terms (id, name, start_date, end_date, status) VALUES ($1, $2, '2026-01-05', '2026-04-30', $3)`,
			uuid.New(), "term-"+ok, ok)
	}
	if err := execErr(db, `INSERT INTO academic_terms (id, name, start_date, end_date, status) VALUES ($1, 'bad', '2026-01-05', '2026-04-30', 'current')`, uuid.New()); err == nil {
		t.Fatal("status outside draft/active/closed must be rejected")
	}
	if err := execErr(db, `INSERT INTO academic_terms (id, name, start_date, end_date) VALUES ($1, $2, '2026-01-05', '2026-04-30')`, uuid.New(), strings.Repeat("x", 81)); err == nil {
		t.Fatal("name longer than 80 chars must be rejected")
	}
	if err := execErr(db, `INSERT INTO academic_terms (id, name, start_date, end_date) VALUES ($1, 'inverted', '2026-04-30', '2026-01-05')`, uuid.New()); err == nil {
		t.Fatal("end_date before start_date must be rejected")
	}
}

func columnsOf(t *testing.T, db *database.DB, table string) map[string]bool {
	t.Helper()
	ctx := context.Background()
	rows, err := db.Q(ctx).Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols[c] = true
	}
	return cols
}

func TestLevelColumnsMatchDictionary(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()

	for _, table := range []string{"curriculum_subjects", "groups"} {
		cols := columnsOf(t, db, table)
		if !cols["level"] || cols["term_number"] {
			t.Errorf("%s columns: level=%v term_number=%v, want level only", table, cols["level"], cols["term_number"])
		}
	}
	var typ, nullable string
	if err := db.Q(ctx).QueryRow(ctx, `SELECT data_type, is_nullable FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'groups' AND column_name = 'level'`).Scan(&typ, &nullable); err != nil {
		t.Fatal(err)
	}
	if typ != "smallint" || nullable != "NO" {
		t.Errorf("groups.level = %s nullable=%s, want smallint NOT NULL", typ, nullable)
	}

	// curriculum_subjects: composite PK (career_id, subject_id, level), no surrogate id.
	if columnsOf(t, db, "curriculum_subjects")["id"] {
		t.Error("curriculum_subjects must not have a surrogate id")
	}
	rows, err := db.Q(ctx).Query(ctx, `SELECT a.attname FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = 'curriculum_subjects'::regclass AND i.indisprimary
		ORDER BY array_position(i.indkey::int[], a.attnum::int)`)
	if err != nil {
		t.Fatal(err)
	}
	var pk []string
	for rows.Next() {
		var c string
		_ = rows.Scan(&c)
		pk = append(pk, c)
	}
	rows.Close()
	if strings.Join(pk, ",") != "career_id,subject_id,level" {
		t.Errorf("curriculum_subjects PK = %v, want [career_id subject_id level]", pk)
	}

	career, subject := uuid.New(), uuid.New()
	mustExec(t, db, `INSERT INTO careers (id, code, name) VALUES ($1, 'TIC', 'Tecnologias')`, career)
	mustExec(t, db, `INSERT INTO subjects (id, code, name) VALUES ($1, 'MAT1', 'Matematicas')`, subject)
	ins := `INSERT INTO curriculum_subjects (career_id, subject_id, level) VALUES ($1, $2, $3)`
	mustExec(t, db, ins, career, subject, 1)
	mustExec(t, db, ins, career, subject, 2) // same subject at another level is allowed by the PK
	if err := execErr(db, ins, career, subject, 2); err == nil {
		t.Error("duplicate (career, subject, level) must violate the PK")
	}
	if err := execErr(db, ins, career, subject, 0); err == nil {
		t.Error("level 0 must be rejected")
	}

	// groups.level: required and positive.
	term := uuid.New()
	mustExec(t, db, `INSERT INTO academic_terms (id, name, start_date, end_date) VALUES ($1, '2026-1', '2026-01-05', '2026-04-30')`, term)
	if err := execErr(db, `INSERT INTO groups (id, career_id, academic_term_id, code) VALUES ($1, $2, $3, 'A1')`, uuid.New(), career, term); err == nil {
		t.Error("group without level must be rejected")
	}
	if err := execErr(db, `INSERT INTO groups (id, career_id, academic_term_id, code, level) VALUES ($1, $2, $3, 'A1', 0)`, uuid.New(), career, term); err == nil {
		t.Error("group level 0 must be rejected")
	}
	mustExec(t, db, `INSERT INTO groups (id, career_id, academic_term_id, code, level) VALUES ($1, $2, $3, 'A1', 3)`, uuid.New(), career, term)
}

func TestAttendanceLatenessConfiguration(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	f := buildAcademic(t, db)

	var late, per int
	if err := db.Q(ctx).QueryRow(ctx, `SELECT late_after_minutes, lates_per_absence FROM attendance_sessions WHERE id = $1`, f.session).Scan(&late, &per); err != nil {
		t.Fatal(err)
	}
	if late != 15 || per != 3 {
		t.Fatalf("defaults = %d minutes / %d lates, want 15 / 3", late, per)
	}
	for _, col := range []string{"late_after_minutes", "lates_per_absence"} {
		for _, bad := range []int{0, -1} {
			if err := execErr(db, `UPDATE attendance_sessions SET `+col+` = $2 WHERE id = $1`, f.session, bad); err == nil {
				t.Errorf("%s = %d must be rejected", col, bad)
			}
		}
		mustExec(t, db, `UPDATE attendance_sessions SET `+col+` = 1 WHERE id = $1`, f.session)
	}
}

func TestAuthSessionRevocationConstraints(t *testing.T) {
	db := dbtest.New(t)
	user := insertUser(t, db, "10000001")
	ins := func(hash, revokedAt, reason string) error {
		return execErr(db, `INSERT INTO auth_sessions (id, user_id, refresh_token_hash, expires_at, revoked_at, revoked_reason)
			VALUES ($1, $2, $3, now() + interval '1 day', NULLIF($4, '')::timestamptz, NULLIF($5, ''))`,
			uuid.New(), user, hash, revokedAt, reason)
	}
	h := func(c string) string { return strings.Repeat(c, 64) }

	if err := ins(h("a"), "", ""); err != nil {
		t.Fatalf("active session: %v", err)
	}
	for i, reason := range []string{"ROTATED", "LOGOUT", "PASSWORD_CHANGED", "PASSWORD_RESET", "REUSE_DETECTED"} {
		if err := ins(h(string(rune('b'+i))), "2026-01-01T00:00:00Z", reason); err != nil {
			t.Errorf("reason %s: %v", reason, err)
		}
	}
	if err := ins(h("g"), "2026-01-01T00:00:00Z", ""); err == nil {
		t.Error("revoked session without a reason must be rejected")
	}
	if err := ins(h("h"), "", "LOGOUT"); err == nil {
		t.Error("reason without revoked_at must be rejected")
	}
	if err := ins(h("i"), "2026-01-01T00:00:00Z", "BECAUSE"); err == nil {
		t.Error("unknown reason must be rejected")
	}
}

// Upgrading a database that already holds data from before migration 0005.
func TestMigration0005UpgradesExistingData(t *testing.T) {
	db := dbtest.NewEmpty(t)
	ctx := context.Background()

	old := fstest.MapFS{}
	for _, name := range []string{"0001_identity.sql", "0002_academic.sql", "0003_grades_attendance.sql", "0004_justifications_tutoring_analytics_audit.sql"} {
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		old[name] = &fstest.MapFile{Data: body}
	}
	if err := db.Migrate(ctx, old, discardLogger()); err != nil {
		t.Fatalf("migrate to 0004: %v", err)
	}

	user := insertUser(t, db, "10000001")
	revoked, active := uuid.New(), uuid.New()
	mustExec(t, db, `INSERT INTO auth_sessions (id, user_id, refresh_token_hash, expires_at, revoked_at) VALUES ($1, $2, $3, now() + interval '1 day', now())`,
		revoked, user, strings.Repeat("a", 64))
	mustExec(t, db, `INSERT INTO auth_sessions (id, user_id, refresh_token_hash, expires_at) VALUES ($1, $2, $3, now() + interval '1 day')`,
		active, user, strings.Repeat("b", 64))
	term, career, subject := uuid.New(), uuid.New(), uuid.New()
	mustExec(t, db, `INSERT INTO academic_terms (id, name, start_date, end_date, is_current) VALUES ($1, '2026-1', '2026-01-05', '2026-04-30', true)`, term)
	mustExec(t, db, `INSERT INTO careers (id, code, name) VALUES ($1, 'TIC', 'Tecnologias')`, career)
	mustExec(t, db, `INSERT INTO subjects (id, code, name) VALUES ($1, 'MAT1', 'Matematicas')`, subject)
	mustExec(t, db, `INSERT INTO curriculum_subjects (id, career_id, subject_id, term_number) VALUES ($1, $2, $3, 4)`, uuid.New(), career, subject)
	mustExec(t, db, `INSERT INTO groups (id, career_id, academic_term_id, code, term_number) VALUES ($1, $2, $3, 'A1', 7)`, uuid.New(), career, term)

	if err := db.Migrate(ctx, migrations.FS, discardLogger()); err != nil {
		t.Fatalf("upgrade to latest: %v", err)
	}

	// 0006 keeps the data under the new column names.
	var curriculumLevel, groupLevel int
	_ = db.Q(ctx).QueryRow(ctx, `SELECT level FROM curriculum_subjects`).Scan(&curriculumLevel)
	_ = db.Q(ctx).QueryRow(ctx, `SELECT level FROM groups`).Scan(&groupLevel)
	if curriculumLevel != 4 || groupLevel != 7 {
		t.Fatalf("levels after upgrade = %d / %d, want 4 / 7", curriculumLevel, groupLevel)
	}

	var reason *string
	_ = db.Q(ctx).QueryRow(ctx, `SELECT revoked_reason FROM auth_sessions WHERE id = $1`, revoked).Scan(&reason)
	if reason == nil || *reason != "LOGOUT" {
		t.Fatalf("legacy revoked session reason = %v, want LOGOUT", reason)
	}
	_ = db.Q(ctx).QueryRow(ctx, `SELECT revoked_reason FROM auth_sessions WHERE id = $1`, active).Scan(&reason)
	if reason != nil {
		t.Fatalf("active session reason = %v, want NULL", *reason)
	}
	var status string
	_ = db.Q(ctx).QueryRow(ctx, `SELECT status FROM academic_terms`).Scan(&status)
	if status != "draft" {
		t.Fatalf("legacy term status = %q, want draft", status)
	}
}

func TestWithinTxRollsBackOnError(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	id := uuid.New()
	err := db.WithinTx(ctx, func(ctx context.Context) error {
		if _, err := db.Q(ctx).Exec(ctx,
			`INSERT INTO users (id, institutional_number, institutional_email, password_hash, first_name, last_name)
			 VALUES ($1, '50000001', '50000001@uttcampus.edu.mx', 'x', 'T', 'U')`, id); err != nil {
			return err
		}
		return context.Canceled // simulate a later failure
	})
	if err == nil {
		t.Fatal("expected error from tx")
	}
	var n int
	_ = db.Q(ctx).QueryRow(ctx, `SELECT count(*) FROM users WHERE id = $1`, id).Scan(&n)
	if n != 0 {
		t.Fatal("insert survived rollback")
	}
}
