package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	attendancenet "github.com/edutrak/backend/internal/attendance/adapters/network"
	attendancepg "github.com/edutrak/backend/internal/attendance/adapters/postgres"
	attendancesec "github.com/edutrak/backend/internal/attendance/adapters/security"
	attendanceapp "github.com/edutrak/backend/internal/attendance/application"
	"github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/config"
	"github.com/google/uuid"
)

// Requests made by the test client come from 192.0.2.10 (a documentation address),
// which these tests declare as the institutional network.
func institutional(c *config.Config) {
	c.AllowedNetworks = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
}

func newAttendanceFx(t *testing.T) *gradeFx { return newGradeFxWith(t, institutional) }

const att = "/api/attendance"

// startSession opens a session of MAT1 (teacher) and returns its id.
func (fx *gradeFx) startSession(t *testing.T) string {
	t.Helper()
	return idOf(fx.w.must("teacher", "POST", att+"/sessions", fmt.Sprintf(`{"teaching_assignment_id":%q}`, fx.assignment), 201))
}

func (fx *gradeFx) rotate(t *testing.T, session string) string {
	t.Helper()
	m := fx.w.must("teacher", "POST", att+"/sessions/"+session+"/qr/rotate", "", 200)
	tok, _ := m["token"].(string)
	if tok == "" {
		t.Fatalf("no token in %v", m)
	}
	return tok
}

func (fx *gradeFx) checkIn(who, session, token string) resp {
	return fx.w.do(who, "POST", att+"/sessions/"+session+"/check-in", fmt.Sprintf(`{"token":%q}`, token))
}

func (fx *gradeFx) recordStatus(session, enrollment string) string {
	fx.w.t.Helper()
	var s string
	err := fx.w.db.Q(context.Background()).QueryRow(context.Background(),
		`SELECT status FROM attendance_records WHERE attendance_session_id = $1 AND subject_enrollment_id = $2`, session, enrollment).Scan(&s)
	if err != nil {
		return ""
	}
	return s
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestAttendanceAuthenticationRequired(t *testing.T) {
	w := newWorld(t)
	id := uuid.NewString()
	for _, ep := range [][2]string{
		{"POST", att + "/sessions"}, {"GET", att + "/sessions/" + id}, {"POST", att + "/sessions/" + id + "/qr/rotate"},
		{"POST", att + "/sessions/" + id + "/check-in"}, {"POST", att + "/sessions/" + id + "/manual"},
		{"POST", att + "/sessions/" + id + "/close"}, {"PATCH", att + "/records/" + id},
		{"GET", att + "/teaching-assignments/" + id}, {"GET", att + "/students/" + id},
	} {
		if r := w.c.do(ep[0], ep[1], "", `{}`); r.status != 401 {
			t.Errorf("%s %s without token: %d", ep[0], ep[1], r.status)
		}
		if r := w.c.do(ep[0], ep[1], "garbage", `{}`); r.status != 401 {
			t.Errorf("%s %s with garbage token: %d", ep[0], ep[1], r.status)
		}
	}
}

func TestAttendanceStartAndQR(t *testing.T) {
	fx := newAttendanceFx(t)
	w := fx.w

	// body validation
	w.expect("teacher", "POST", att+"/sessions", `{"teaching_assignment_id":"`+fx.assignment+`","teacher_id":"`+w.ids["teacher2"]+`"}`, 400, "invalid_body")
	w.expect("teacher", "POST", att+"/sessions", `{"teaching_assignment_id":"`+fx.assignment+`","late_after_minutes":0}`, 422, "invalid_late_after_minutes")
	w.expect("teacher", "POST", att+"/sessions", `{"teaching_assignment_id":"`+fx.assignment+`","lates_per_absence":0}`, 422, "invalid_lates_per_absence")
	w.expect("teacher", "POST", att+"/sessions", fmt.Sprintf(`{"teaching_assignment_id":%q}`, uuid.NewString()), 422, "teaching_assignment_not_found")
	w.expect("teacher", "POST", att+"/sessions", fmt.Sprintf(`{"teaching_assignment_id":%q,"schedule_id":%q}`, fx.assignment, uuid.NewString()), 422, "schedule_not_in_assignment")
	// not the teacher of the assignment, or not a teacher at all
	for _, who := range []string{"teacher2", "tutor", "s1", "director", "admin"} {
		w.expect(who, "POST", att+"/sessions", fmt.Sprintf(`{"teaching_assignment_id":%q}`, fx.assignment), 403, "forbidden")
	}
	if n := w.sqlInt(`SELECT count(*) FROM attendance_sessions`); n != 0 {
		t.Fatalf("rejected requests opened %d sessions", n)
	}

	s := w.must("teacher", "POST", att+"/sessions", fmt.Sprintf(`{"teaching_assignment_id":%q}`, fx.assignment), 201)
	session := idOf(s)
	if s["status"] != "OPEN" || s["late_after_minutes"] != float64(15) || s["lates_per_absence"] != float64(3) || s["opened_by"] != w.ids["teacher"] {
		t.Fatalf("session = %v", s)
	}
	if _, leaks := s["qr_token_hash"]; leaks {
		t.Fatal("QR data must never be part of the session payload")
	}
	w.expect("teacher", "POST", att+"/sessions", fmt.Sprintf(`{"teaching_assignment_id":%q}`, fx.assignment), 409, "session_already_open")

	// QR rotation
	for _, who := range []string{"teacher2", "tutor", "s1", "director", "admin"} {
		w.expect(who, "POST", att+"/sessions/"+session+"/qr/rotate", "", 403, "forbidden")
	}
	w.expect("teacher", "POST", att+"/sessions/"+uuid.NewString()+"/qr/rotate", "", 404, "attendance_session_not_found")
	w.expect("teacher", "POST", att+"/sessions/zzz/qr/rotate", "", 404, "attendance_session_not_found")
	r := w.do("teacher", "POST", att+"/sessions/"+session+"/qr/rotate", "")
	if r.status != 200 {
		t.Fatalf("rotate: %d %s", r.status, r.body)
	}
	token, _ := r.json(t)["token"].(string)
	exp, _ := time.Parse(time.RFC3339Nano, r.json(t)["expires_at"].(string))
	if d := time.Until(exp); d < 20*time.Second || d > 31*time.Second {
		t.Fatalf("QR lifetime = %v, want about 30s", d)
	}
	stored := w.sqlString(`SELECT qr_token_hash FROM attendance_sessions WHERE id = $1`, session)
	if stored == token || stored != sha(token) {
		t.Fatalf("the database must hold only SHA-256(token); stored=%q", stored)
	}
	if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%' || $1 || '%'`, token); n != 0 {
		t.Fatal("the QR token must never reach the audit log")
	}
	token2 := fx.rotate(t, session)
	if token2 == token || w.sqlString(`SELECT qr_token_hash FROM attendance_sessions WHERE id = $1`, session) != sha(token2) {
		t.Fatal("rotation must replace the stored hash")
	}
	if r := fx.checkIn("s1", session, token); r.status != 403 || r.errCode(t) != "invalid_qr_token" {
		t.Fatalf("a rotated-away QR must be refused: %d %s", r.status, r.body)
	}
	if w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'attendance.session_started'`) != 1 {
		t.Fatal("session_started must be audited once")
	}
}

func TestAttendanceCheckIn(t *testing.T) {
	fx := newAttendanceFx(t)
	w := fx.w
	session := fx.startSession(t)
	token := fx.rotate(t, session)

	// the student identity is the token's; nothing else is accepted in the body
	for name, body := range map[string]string{
		"student_id":            fmt.Sprintf(`{"token":%q,"student_id":%q}`, token, w.ids["s2"]),
		"subject_enrollment_id": fmt.Sprintf(`{"token":%q,"subject_enrollment_id":%q}`, token, fx.se2),
		"ip":                    fmt.Sprintf(`{"token":%q,"ip":"192.0.2.99"}`, token),
		"status":                fmt.Sprintf(`{"token":%q,"status":"PRESENT"}`, token),
		"checked_in_at":         fmt.Sprintf(`{"token":%q,"checked_in_at":"2020-01-01T00:00:00Z"}`, token),
		"not json":              `{`,
	} {
		if r := w.do("s1", "POST", att+"/sessions/"+session+"/check-in", body); r.status != 400 || r.errCode(t) != "invalid_body" {
			t.Errorf("%s in body: %d %s", name, r.status, r.body)
		}
	}
	if r := w.do("s1", "POST", att+"/sessions/"+session+"/check-in", `{}`); r.status != 422 || r.errCode(t) != "qr_token_required" {
		t.Errorf("missing token: %d %s", r.status, r.body)
	}
	if r := fx.checkIn("s1", session, "wrong"); r.status != 403 || r.errCode(t) != "invalid_qr_token" {
		t.Errorf("wrong token: %d %s", r.status, r.body)
	}
	for _, who := range []string{"teacher", "teacher2", "tutor", "director", "admin"} {
		if r := fx.checkIn(who, session, token); r.status != 403 || r.errCode(t) != "forbidden" {
			t.Errorf("%s checking in: %d %s", who, r.status, r.body)
		}
	}
	if r := fx.checkIn("s3", session, token); r.status != 403 || r.errCode(t) != "not_enrolled" {
		t.Errorf("student of another group: %d %s", r.status, r.body)
	}
	if r := fx.w.do("s1", "POST", att+"/sessions/"+uuid.NewString()+"/check-in", `{"token":"x"}`); r.status != 404 {
		t.Errorf("unknown session: %d", r.status)
	}
	if n := w.sqlInt(`SELECT count(*) FROM attendance_records`); n != 0 {
		t.Fatalf("rejected check-ins stored %d records", n)
	}

	// success: PRESENT, own enrollment, server time, real connection address
	r := fx.checkIn("s1", session, token)
	if r.status != 201 {
		t.Fatalf("check-in: %d %s", r.status, r.body)
	}
	m := r.json(t)
	if m["status"] != "PRESENT" || m["method"] != "QR" || m["subject_enrollment_id"] != fx.se1 || m["recorded_by"] != w.ids["s1"] {
		t.Fatalf("record = %v", m)
	}
	if ip := w.sqlString(`SELECT host(ip_address) FROM attendance_records WHERE id = $1`, idOf(m)); ip != "192.0.2.10" {
		t.Fatalf("stored ip = %s, want the connection's 192.0.2.10", ip)
	}
	if strings.Contains(r.body, "192.0.2") {
		t.Fatal("the address must not be echoed back to the client")
	}
	if r := fx.checkIn("s1", session, token); r.status != 409 || r.errCode(t) != "attendance_already_registered" {
		t.Errorf("duplicate: %d %s", r.status, r.body)
	}
	// another student behind the same NAT address registers normally
	if r := fx.checkIn("s2", session, token); r.status != 201 {
		t.Errorf("second student on the same address: %d %s", r.status, r.body)
	}
	if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'attendance.recorded' AND actor_user_id = $1`, w.ids["s1"]); n != 1 {
		t.Errorf("recorded audit rows for s1 = %d", n)
	}
}

func TestAttendanceLateQRAndExpiry(t *testing.T) {
	fx := newAttendanceFx(t)
	w := fx.w
	ctx := context.Background()

	// 20 minutes after opening (server time) the QR check-in is LATE
	session := fx.startSession(t)
	w.db.Q(ctx).Exec(ctx, `UPDATE attendance_sessions SET opened_at = now() - interval '20 minutes' WHERE id = $1`, session)
	token := fx.rotate(t, session)
	if r := fx.checkIn("s1", session, token); r.status != 201 || r.json(t)["status"] != "LATE" {
		t.Fatalf("late check-in: %d %s", r.status, r.body)
	}
	// 14 minutes in: still PRESENT
	w.db.Q(ctx).Exec(ctx, `UPDATE attendance_sessions SET opened_at = now() - interval '14 minutes' WHERE id = $1`, session)
	if r := fx.checkIn("s2", session, token); r.status != 201 || r.json(t)["status"] != "PRESENT" {
		t.Fatalf("on-time check-in: %d %s", r.status, r.body)
	}

	// a per-session threshold of 5 minutes
	w.must("teacher", "POST", att+"/sessions/"+session+"/close", "", 200)
	strict := idOf(w.must("teacher", "POST", att+"/sessions", fmt.Sprintf(`{"teaching_assignment_id":%q,"late_after_minutes":5,"lates_per_absence":2}`, fx.assignment), 201))
	w.db.Q(ctx).Exec(ctx, `UPDATE attendance_sessions SET opened_at = now() - interval '6 minutes' WHERE id = $1`, strict)
	if r := fx.checkIn("s1", strict, fx.rotate(t, strict)); r.status != 201 || r.json(t)["status"] != "LATE" {
		t.Fatalf("strict session: %d %s", r.status, r.body)
	}

	// an old screenshot: the QR expires
	w.must("teacher", "POST", att+"/sessions/"+strict+"/close", "", 200)
	third := fx.startSession(t)
	old := fx.rotate(t, third)
	w.db.Q(ctx).Exec(ctx, `UPDATE attendance_sessions SET qr_expires_at = now() - interval '1 second' WHERE id = $1`, third)
	if r := fx.checkIn("s1", third, old); r.status != 403 || r.errCode(t) != "qr_expired" {
		t.Fatalf("expired QR: %d %s", r.status, r.body)
	}
	if fx.recordStatus(third, fx.se1) != "" {
		t.Fatal("an expired QR must not register anything")
	}
	// a fresh token works again
	if r := fx.checkIn("s1", third, fx.rotate(t, third)); r.status != 201 {
		t.Fatalf("fresh QR: %d %s", r.status, r.body)
	}
}

func TestAttendanceManualCorrectionAndClose(t *testing.T) {
	fx := newAttendanceFx(t)
	w := fx.w
	ctx := context.Background()
	session := fx.startSession(t)
	manual := func(who, se, status string) resp {
		return w.do(who, "POST", att+"/sessions/"+session+"/manual", fmt.Sprintf(`{"subject_enrollment_id":%q,"status":%q}`, se, status))
	}

	if r := manual("teacher", fx.se1, "JUSTIFIED"); r.status != 422 || r.errCode(t) != "status_not_allowed" {
		t.Errorf("JUSTIFIED: %d %s", r.status, r.body)
	}
	if r := manual("teacher", fx.se1, "ON_TIME"); r.status != 422 {
		t.Errorf("unknown status: %d", r.status)
	}
	if r := manual("teacher", uuid.NewString(), "PRESENT"); r.status != 422 || r.errCode(t) != "subject_enrollment_not_found" {
		t.Errorf("unknown enrollment: %d %s", r.status, r.body)
	}
	if r := manual("teacher", fx.se1b, "PRESENT"); r.status != 422 { // s1's FIS1 enrollment, not in this assignment
		t.Errorf("enrollment of another assignment: %d %s", r.status, r.body)
	}
	for _, who := range []string{"teacher2", "tutor", "s1", "director", "admin"} {
		if r := manual(who, fx.se1, "PRESENT"); r.status != 403 {
			t.Errorf("%s: %d", who, r.status)
		}
	}
	if w.w2count(`SELECT count(*) FROM attendance_records`) != 0 {
		t.Fatal("rejected manual requests stored records")
	}

	// manual capture needs no QR, and no institutional network
	r := w.c.doFrom("198.51.100.4:4000", "POST", att+"/sessions/"+session+"/manual", w.token["teacher"], fmt.Sprintf(`{"subject_enrollment_id":%q,"status":"LATE"}`, fx.se1))
	if r.status != 201 || r.json(t)["status"] != "LATE" || r.json(t)["method"] != "MANUAL" || r.json(t)["recorded_by"] != w.ids["teacher"] {
		t.Fatalf("manual from outside the network: %d %s", r.status, r.body)
	}
	recID := idOf(r.json(t))
	if ip := w.sqlString(`SELECT COALESCE(host(ip_address), 'none') FROM attendance_records WHERE id = $1`, recID); ip != "none" {
		t.Fatalf("manual records must not store the teacher's address, got %s", ip)
	}
	if r := manual("teacher", fx.se1, "PRESENT"); r.status != 409 || r.errCode(t) != "attendance_already_registered" {
		t.Errorf("duplicate manual: %d %s", r.status, r.body)
	}

	// correction
	patch := func(who, id, status string) resp {
		return w.do(who, "PATCH", att+"/records/"+id, fmt.Sprintf(`{"status":%q}`, status))
	}
	if r := patch("teacher", recID, "PRESENT"); r.status != 200 || r.json(t)["status"] != "PRESENT" {
		t.Fatalf("correction: %d %s", r.status, r.body)
	}
	if r := patch("teacher", recID, "JUSTIFIED"); r.status != 422 || r.errCode(t) != "status_not_allowed" {
		t.Errorf("correct to JUSTIFIED: %d %s", r.status, r.body)
	}
	for _, who := range []string{"teacher2", "tutor", "s1", "director", "admin"} {
		if r := patch(who, recID, "ABSENT"); r.status != 403 {
			t.Errorf("%s correcting: %d", who, r.status)
		}
	}
	if fx.recordStatus(session, fx.se1) != "PRESENT" {
		t.Fatal("a forbidden correction changed the record")
	}
	w.expect("teacher", "PATCH", att+"/records/"+uuid.NewString(), `{"status":"ABSENT"}`, 404, "attendance_record_not_found")
	w.expect("teacher", "PATCH", att+"/records/zzz", `{"status":"ABSENT"}`, 404, "attendance_record_not_found")
	w.expect("teacher", "PATCH", att+"/records/"+recID, `{"status":"ABSENT","method":"QR"}`, 400, "invalid_body")
	if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'attendance.corrected' AND metadata->>'from' = 'LATE' AND metadata->>'to' = 'PRESENT'`); n != 1 {
		t.Errorf("correction audit rows = %d", n)
	}
	// a record already justified by the justification flow is off limits here
	w.db.Q(ctx).Exec(ctx, `UPDATE attendance_records SET status = 'JUSTIFIED' WHERE id = $1`, recID)
	w.expect("teacher", "PATCH", att+"/records/"+recID, `{"status":"PRESENT"}`, 409, "record_justified")
	w.db.Q(ctx).Exec(ctx, `UPDATE attendance_records SET status = 'PRESENT' WHERE id = $1`, recID)

	// close: s2 never registered -> ABSENT; s1 keeps PRESENT
	for _, who := range []string{"teacher2", "tutor", "s1", "director", "admin"} {
		w.expect(who, "POST", att+"/sessions/"+session+"/close", "", 403, "forbidden")
	}
	if fx.recordStatus(session, fx.se2) != "" {
		t.Fatal("nothing may be generated before the close")
	}
	res := w.must("teacher", "POST", att+"/sessions/"+session+"/close", "", 200)
	if res["absences_created"] != float64(1) || res["session"].(map[string]any)["status"] != "CLOSED" || res["session"].(map[string]any)["closed_at"] == nil {
		t.Fatalf("close result = %v", res)
	}
	if fx.recordStatus(session, fx.se2) != "ABSENT" || fx.recordStatus(session, fx.se1) != "PRESENT" {
		t.Fatal("close must mark only the missing student as ABSENT")
	}
	if w.sqlString(`SELECT COALESCE(qr_token_hash, 'none') FROM attendance_sessions WHERE id = $1`, session) != "none" {
		t.Fatal("closing must invalidate the QR")
	}
	w.expect("teacher", "POST", att+"/sessions/"+session+"/close", "", 409, "session_closed")
	w.expect("teacher", "POST", att+"/sessions/"+session+"/qr/rotate", "", 409, "session_closed")
	if r := manual("teacher", fx.se2, "PRESENT"); r.status != 409 || r.errCode(t) != "session_closed" {
		t.Errorf("manual after close: %d %s", r.status, r.body)
	}
	if n := w.sqlInt(`SELECT count(*) FROM attendance_records WHERE attendance_session_id = $1`, session); n != 2 {
		t.Fatalf("records = %d, want exactly 2 (one per enrolled student)", n)
	}
	// conservative post-close policy: the responsible teacher may still fix the generated absence, audited
	absentID := w.sqlString(`SELECT id::text FROM attendance_records WHERE attendance_session_id = $1 AND subject_enrollment_id = $2`, session, fx.se2)
	if r := patch("teacher", absentID, "LATE"); r.status != 200 {
		t.Fatalf("post-close correction: %d %s", r.status, r.body)
	}
	if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'attendance.corrected' AND metadata->>'session_status' = 'CLOSED'`); n != 1 {
		t.Errorf("post-close correction not audited: %d", n)
	}
	if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'attendance.session_closed' AND (metadata->>'absences_created')::int = 1`); n != 1 {
		t.Error("session_closed audit row missing")
	}
	// the generated absence's integrity: a record's enrollment always belongs to its session's assignment
	if n := w.sqlInt(`SELECT count(*) FROM attendance_records r
		JOIN attendance_sessions s ON s.id = r.attendance_session_id
		JOIN subject_enrollments se ON se.id = r.subject_enrollment_id
		WHERE se.teaching_assignment_id <> s.teaching_assignment_id`); n != 0 {
		t.Fatalf("%d records point to an enrollment of another assignment", n)
	}
}

func (w *world) w2count(q string, args ...any) int { return w.sqlInt(q, args...) }

func TestAttendanceCloseIgnoresWithdrawnStudents(t *testing.T) {
	fx := newAttendanceFx(t)
	w := fx.w
	w.db.Q(context.Background()).Exec(context.Background(), `UPDATE subject_enrollments SET status = 'WITHDRAWN' WHERE id = $1`, fx.se2)
	session := fx.startSession(t)
	res := w.must("teacher", "POST", att+"/sessions/"+session+"/close", "", 200)
	if res["absences_created"] != float64(1) {
		t.Fatalf("absences = %v, want only the active student", res["absences_created"])
	}
	if fx.recordStatus(session, fx.se2) != "" {
		t.Fatal("a withdrawn enrollment must not get an absence")
	}
	// and a withdrawn student cannot check in either
	s2 := fx.startSession(t)
	if r := fx.checkIn("s2", s2, fx.rotate(t, s2)); r.status != 409 || r.errCode(t) != "subject_enrollment_inactive" {
		t.Fatalf("withdrawn check-in: %d %s", r.status, r.body)
	}
}

func TestAttendanceClosedTermFreezesEverything(t *testing.T) {
	fx := newAttendanceFx(t)
	w := fx.w
	session := fx.startSession(t)
	token := fx.rotate(t, session)
	rec := idOf(w.must("teacher", "POST", att+"/sessions/"+session+"/manual", fmt.Sprintf(`{"subject_enrollment_id":%q,"status":"PRESENT"}`, fx.se2), 201))
	w.must("director", "POST", "/api/academic/terms/"+fx.term+"/close", "", 200)

	if r := fx.checkIn("s1", session, token); r.status != 409 || r.errCode(t) != "term_closed" {
		t.Errorf("check-in: %d %s", r.status, r.body)
	}
	w.expect("teacher", "POST", att+"/sessions/"+session+"/qr/rotate", "", 409, "term_closed")
	w.expect("teacher", "POST", att+"/sessions/"+session+"/manual", fmt.Sprintf(`{"subject_enrollment_id":%q,"status":"PRESENT"}`, fx.se1), 409, "term_closed")
	w.expect("teacher", "PATCH", att+"/records/"+rec, `{"status":"ABSENT"}`, 409, "term_closed")
	w.expect("teacher", "POST", att+"/sessions/"+session+"/close", "", 409, "term_closed")
	w.expect("teacher", "POST", att+"/sessions", fmt.Sprintf(`{"teaching_assignment_id":%q}`, fx.assignment2), 403, "forbidden") // not theirs: 403 first
	// reading history still works
	w.must("teacher", "GET", att+"/sessions/"+session, "", 200)
	w.must("s2", "GET", att+"/students/"+w.ids["s2"], "", 200)
}

func TestAttendanceReads(t *testing.T) {
	fx := newAttendanceFx(t)
	w := fx.w
	ctx := context.Background()

	// three sessions where s1 is LATE (manual), s2 present once and absent twice via close
	var firstSession string
	for i := 0; i < 3; i++ {
		s := fx.startSession(t)
		if i == 0 {
			firstSession = s
		}
		w.must("teacher", "POST", att+"/sessions/"+s+"/manual", fmt.Sprintf(`{"subject_enrollment_id":%q,"status":"LATE"}`, fx.se1), 201)
		if i == 0 {
			w.must("teacher", "POST", att+"/sessions/"+s+"/manual", fmt.Sprintf(`{"subject_enrollment_id":%q,"status":"PRESENT"}`, fx.se2), 201)
		}
		w.must("teacher", "POST", att+"/sessions/"+s+"/close", "", 200)
	}
	// LATE records are never rewritten as ABSENT: the equivalence is only an indicator
	if n := w.sqlInt(`SELECT count(*) FROM attendance_records WHERE status = 'LATE'`); n != 3 {
		t.Fatalf("LATE rows = %d, want 3 untouched", n)
	}

	t.Run("assignment overview", func(t *testing.T) {
		path := att + "/teaching-assignments/" + fx.assignment
		for _, who := range []string{"teacher", "tutor", "director"} {
			w.must(who, "GET", path, "", 200)
		}
		for _, who := range []string{"teacher2", "tutor2", "s1", "admin"} {
			w.expect(who, "GET", path, "", 403, "forbidden")
		}
		w.expect("director", "GET", att+"/teaching-assignments/"+uuid.NewString(), "", 404, "teaching_assignment_not_found")
		m := w.must("teacher", "GET", path, "", 200)
		if m["total_sessions"] != float64(3) || len(m["sessions"].([]any)) != 3 {
			t.Fatalf("overview = %v", m)
		}
		var s1, s2 map[string]any
		for _, row := range m["students"].([]any) {
			r := row.(map[string]any)
			if r["subject_enrollment_id"] == fx.se1 {
				s1 = r["attendance"].(map[string]any)
			} else if r["subject_enrollment_id"] == fx.se2 {
				s2 = r["attendance"].(map[string]any)
			}
		}
		// s1: 3 lates, 3 per absence -> one absence by equivalence, nothing rewritten
		if s1["late"] != float64(3) || s1["absent"] != float64(0) || s1["lates_per_absence"] != float64(3) ||
			s1["absences_from_lates"] != float64(1) || s1["effective_absences"] != float64(1) {
			t.Errorf("s1 attendance = %v", s1)
		}
		// s2: 1 present, 2 generated absences
		if s2["present"] != float64(1) || s2["absent"] != float64(2) || s2["effective_absences"] != float64(2) {
			t.Errorf("s2 attendance = %v", s2)
		}
	})

	t.Run("session detail", func(t *testing.T) {
		path := att + "/sessions/" + firstSession
		for _, who := range []string{"teacher", "tutor", "director"} {
			w.must(who, "GET", path, "", 200)
		}
		for _, who := range []string{"teacher2", "tutor2", "s1", "s3", "admin"} {
			w.expect(who, "GET", path, "", 403, "forbidden")
		}
		w.expect("teacher", "GET", att+"/sessions/"+uuid.NewString(), "", 404, "attendance_session_not_found")
		m := w.must("teacher", "GET", path, "", 200)
		if len(m["students"].([]any)) != 2 || m["counts"].(map[string]any)["late"] != float64(1) || m["counts"].(map[string]any)["present"] != float64(1) {
			t.Fatalf("detail = %v", m)
		}
		// an open session with nothing registered lists pending students with record: null
		open := fx.startSession(t)
		d := w.must("teacher", "GET", att+"/sessions/"+open, "", 200)
		if d["pending"] != float64(2) {
			t.Fatalf("pending = %v", d["pending"])
		}
		for _, row := range d["students"].([]any) {
			if row.(map[string]any)["record"] != nil {
				t.Fatal("a student without a record must have record null, not a fake status")
			}
		}
		w.db.Q(ctx).Exec(ctx, `UPDATE attendance_sessions SET status='CLOSED', closed_at=now() WHERE id=$1`, open)
	})

	t.Run("student history", func(t *testing.T) {
		studentPath := func(id string) string { return att + "/students/" + id }
		m := w.must("s1", "GET", studentPath(w.ids["s1"]), "", 200)
		data := m["data"].([]any)
		if len(data) != 2 { // MAT1 and FIS1
			t.Fatalf("s1 sheets = %d", len(data))
		}
		for _, d := range data {
			sheet := d.(map[string]any)
			if sheet["subject"].(map[string]any)["code"] == "MAT1" {
				a := sheet["attendance"].(map[string]any)
				if len(sheet["records"].([]any)) != 3 || a["late"] != float64(3) || a["effective_absences"] != float64(1) {
					t.Errorf("MAT1 sheet = %v", sheet)
				}
			}
		}
		// own data only
		for _, who := range []string{"s2", "s3", "admin", "tutor2"} {
			w.expect(who, "GET", studentPath(w.ids["s1"]), "", 403, "forbidden")
		}
		w.expect("s1", "GET", studentPath(w.ids["s2"]), "", 403, "forbidden")
		for _, who := range []string{"tutor", "director"} {
			if len(w.must(who, "GET", studentPath(w.ids["s1"]), "", 200)["data"].([]any)) != 2 {
				t.Errorf("%s should see both attempts", who)
			}
		}
		// teachers only see the attempts of their own assignments
		if got := w.must("teacher", "GET", studentPath(w.ids["s1"]), "", 200)["data"].([]any); len(got) != 1 || got[0].(map[string]any)["subject"].(map[string]any)["code"] != "MAT1" {
			t.Errorf("teacher view = %v", got)
		}
		if got := w.must("teacher2", "GET", studentPath(w.ids["s1"]), "", 200)["data"].([]any); len(got) != 1 || len(got[0].(map[string]any)["records"].([]any)) != 0 {
			t.Errorf("teacher2 view = %v", got)
		}
		w.expect("teacher2", "GET", studentPath(w.ids["s2"]), "", 403, "forbidden") // never taught s2
		w.expect("director", "GET", studentPath(uuid.NewString()), "", 404, "student_not_found")
		w.expect("director", "GET", studentPath("zzz"), "", 404, "student_not_found")
		if len(w.must("s3", "GET", studentPath(w.ids["s3"]), "", 200)["data"].([]any)) != 0 {
			t.Error("a student with no enrollments gets an empty list")
		}
	})
}

// --- network policy over the full HTTP stack -------------------------------------------------

func TestAttendanceNetworkPolicyEndToEnd(t *testing.T) {
	proxy := netip.MustParsePrefix("203.0.113.10/32")
	type cfgCase struct {
		name   string
		mutate func(*config.Config)
	}
	setup := func(t *testing.T, c cfgCase) (*gradeFx, string, string) {
		fx := newGradeFxWith(t, c.mutate)
		session := fx.startSession(t)
		return fx, session, fx.rotate(t, session)
	}
	try := func(fx *gradeFx, remote, session, token string, headers ...string) resp {
		return fx.w.c.doFrom(remote, "POST", att+"/sessions/"+session+"/check-in", fx.w.token["s1"], fmt.Sprintf(`{"token":%q}`, token), headers...)
	}

	t.Run("no trusted proxy: the header is ignored", func(t *testing.T) {
		fx, s, tok := setup(t, cfgCase{"direct", institutional})
		if r := try(fx, "198.51.100.4:5000", s, tok); r.status != 403 || r.errCode(t) != "network_not_allowed" {
			t.Errorf("outside address: %d %s", r.status, r.body)
		}
		for _, h := range [][]string{
			{"X-Forwarded-For", "192.0.2.50"},
			{"X-Forwarded-For", "192.0.2.1, 192.0.2.2"},
			{"X-Real-IP", "192.0.2.50"},
			{"Forwarded", "for=192.0.2.50"},
			{"X-Forwarded-For", "192.0.2.50", "X-Forwarded-For", "192.0.2.51"},
		} {
			if r := try(fx, "198.51.100.4:5000", s, tok, h...); r.status != 403 || r.errCode(t) != "network_not_allowed" {
				t.Errorf("spoofed %v: %d %s", h, r.status, r.body)
			}
		}
		if fx.w.sqlInt(`SELECT count(*) FROM attendance_records`) != 0 {
			t.Fatal("spoofed requests registered attendance")
		}
		if r := try(fx, "192.0.2.77:5000", s, tok); r.status != 201 { // a real institutional peer
			t.Errorf("institutional peer: %d %s", r.status, r.body)
		}
	})

	t.Run("trusted proxy: the chain is honoured right to left", func(t *testing.T) {
		fx, s, tok := setup(t, cfgCase{"proxy", func(c *config.Config) { institutional(c); c.TrustedProxies = []netip.Prefix{proxy} }})
		if r := try(fx, "203.0.113.10:443", s, tok, "X-Forwarded-For", "198.51.100.4"); r.status != 403 {
			t.Errorf("outside client behind the proxy: %d %s", r.status, r.body)
		}
		if r := try(fx, "203.0.113.10:443", s, tok, "X-Forwarded-For", "192.0.2.50, 198.51.100.4"); r.status != 403 {
			t.Errorf("client-prepended fake institutional entry: %d %s", r.status, r.body)
		}
		if r := try(fx, "203.0.113.10:443", s, tok); r.status != 403 {
			t.Errorf("proxy without forwarded header is judged as itself (outside): %d", r.status)
		}
		if r := try(fx, "198.51.100.4:5000", s, tok, "X-Forwarded-For", "192.0.2.50"); r.status != 403 {
			t.Errorf("an untrusted peer cannot borrow the trusted proxy's privilege: %d", r.status)
		}
		if r := try(fx, "203.0.113.10:443", s, tok, "X-Forwarded-For", "192.0.2.50"); r.status != 201 {
			t.Errorf("institutional client behind the trusted proxy: %d %s", r.status, r.body)
		}
		if ip := fx.w.sqlString(`SELECT host(ip_address) FROM attendance_records LIMIT 1`); ip != "192.0.2.50" {
			t.Errorf("stored ip = %s, want the forwarded client address", ip)
		}
	})

	t.Run("no networks configured: nobody, not even private ranges", func(t *testing.T) {
		fx, s, tok := setup(t, cfgCase{"empty", nil})
		for _, remote := range []string{"192.0.2.10:1", "10.0.0.5:1", "192.168.1.9:1", "172.16.0.3:1", "127.0.0.1:1", "198.51.100.4:1"} {
			if r := try(fx, remote, s, tok); r.status != 403 || r.errCode(t) != "network_not_allowed" {
				t.Errorf("%s: %d %s (a missing configuration must never mean 'any network')", remote, r.status, r.body)
			}
		}
		// but manual capture by the teacher does not depend on the network policy
		fx.w.must("teacher", "POST", att+"/sessions/"+s+"/manual", fmt.Sprintf(`{"subject_enrollment_id":%q,"status":"PRESENT"}`, fx.se1), 201)
	})

	t.Run("development switch", func(t *testing.T) {
		fx, s, tok := setup(t, cfgCase{"dev", func(c *config.Config) { c.AttendanceAllowAnyNetwork = true }})
		if r := try(fx, "198.51.100.4:5000", s, tok); r.status != 201 {
			t.Errorf("allow-any in development: %d %s", r.status, r.body)
		}
	})

	t.Run("the development switch is ignored in production", func(t *testing.T) {
		fx, s, tok := setup(t, cfgCase{"prod", func(c *config.Config) {
			c.Env, c.AttendanceAllowAnyNetwork = "production", true // config.Load rejects this; the wiring must be safe even if it slipped through
			institutional(c)
		}})
		if r := try(fx, "198.51.100.4:5000", s, tok); r.status != 403 {
			t.Errorf("production must not honour allow-any: %d %s", r.status, r.body)
		}
	})

	t.Run("shared NAT address and CIDR boundaries", func(t *testing.T) {
		fx, s, tok := setup(t, cfgCase{"nat", func(c *config.Config) {
			c.AllowedNetworks = []netip.Prefix{netip.MustParsePrefix("198.51.100.7/32")} // the campus' single egress address
		}})
		for _, who := range []string{"s1", "s2"} {
			r := fx.w.c.doFrom("198.51.100.7:1000", "POST", att+"/sessions/"+s+"/check-in", fx.w.token[who], fmt.Sprintf(`{"token":%q}`, tok))
			if r.status != 201 {
				t.Errorf("%s behind the shared egress address: %d %s", who, r.status, r.body)
			}
		}
		fx2, s2, tok2 := setup(t, cfgCase{"nat2", func(c *config.Config) { c.AllowedNetworks = []netip.Prefix{netip.MustParsePrefix("198.51.100.7/32")} }})
		if r := try(fx2, "198.51.100.8:1000", s2, tok2); r.status != 403 {
			t.Errorf("the address next to the /32: %d", r.status)
		}
	})
}

// --- concurrency -----------------------------------------------------------------------------

func TestAttendanceConcurrency(t *testing.T) {
	fx := newAttendanceFx(t)
	w := fx.w

	t.Run("simultaneous check-ins of the same student create one record", func(t *testing.T) {
		session := fx.startSession(t)
		token := fx.rotate(t, session)
		var wg sync.WaitGroup
		var mu sync.Mutex
		created, conflicts := 0, 0
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := fx.checkIn("s1", session, token)
				mu.Lock()
				defer mu.Unlock()
				switch r.status {
				case 201:
					created++
				case 409:
					conflicts++
				default:
					t.Errorf("unexpected %d: %s", r.status, r.body)
				}
			}()
		}
		wg.Wait()
		if created != 1 || conflicts != 11 {
			t.Fatalf("created=%d conflicts=%d, want 1 / 11", created, conflicts)
		}
		if n := w.sqlInt(`SELECT count(*) FROM attendance_records WHERE attendance_session_id = $1`, session); n != 1 {
			t.Fatalf("%d records stored", n)
		}
		if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'attendance.recorded' AND metadata->>'session_id' = $1`, session); n != 1 {
			t.Fatalf("audit rows = %d, only the winner may be audited", n)
		}
		w.must("teacher", "POST", att+"/sessions/"+session+"/close", "", 200)
	})

	t.Run("check-ins racing the close: every student ends with exactly one coherent record", func(t *testing.T) {
		for round := 0; round < 12; round++ {
			session := fx.startSession(t)
			token := fx.rotate(t, session)
			var wg sync.WaitGroup
			results := map[string]int{}
			var mu sync.Mutex
			start := make(chan struct{})
			for _, who := range []string{"s1", "s2"} {
				wg.Add(1)
				go func(who string) {
					defer wg.Done()
					<-start
					r := fx.checkIn(who, session, token)
					mu.Lock()
					results[who] = r.status
					mu.Unlock()
				}(who)
			}
			var closeStatus int
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				closeStatus = w.do("teacher", "POST", att+"/sessions/"+session+"/close", "").status
			}()
			close(start)
			wg.Wait()

			if closeStatus != 200 {
				t.Fatalf("round %d: close = %d", round, closeStatus)
			}
			if n := w.sqlInt(`SELECT count(*) FROM attendance_records WHERE attendance_session_id = $1`, session); n != 2 {
				t.Fatalf("round %d: %d records, want exactly one per student", round, n)
			}
			for who, se := range map[string]string{"s1": fx.se1, "s2": fx.se2} {
				st := fx.recordStatus(session, se)
				switch results[who] {
				case 201: // got in before the close: must keep PRESENT/LATE, never overwritten by an absence
					if st != "PRESENT" && st != "LATE" {
						t.Fatalf("round %d: %s checked in (201) but the record is %s", round, who, st)
					}
				case 409, 403: // lost the race: the close made them ABSENT
					if st != "ABSENT" {
						t.Fatalf("round %d: %s was refused (%d) but the record is %s", round, who, results[who], st)
					}
				default:
					t.Fatalf("round %d: %s got %d", round, who, results[who])
				}
			}
			if h := w.sqlString(`SELECT COALESCE(qr_token_hash, 'none') FROM attendance_sessions WHERE id = $1`, session); h != "none" {
				t.Fatalf("round %d: the closed session still holds a QR", round)
			}
		}
	})

	t.Run("concurrent rotations leave exactly one valid token", func(t *testing.T) {
		session := fx.startSession(t)
		tokens := make([]string, 8)
		var wg sync.WaitGroup
		for i := range tokens {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				r := w.do("teacher", "POST", att+"/sessions/"+session+"/qr/rotate", "")
				if r.status != 200 {
					t.Errorf("rotate: %d %s", r.status, r.body)
					return
				}
				tokens[i], _ = r.json(t)["token"].(string)
			}(i)
		}
		wg.Wait()
		valid := 0
		for _, tok := range tokens {
			if tok == "" {
				continue
			}
			if sha(tok) == w.sqlString(`SELECT qr_token_hash FROM attendance_sessions WHERE id = $1`, session) {
				valid++
			}
		}
		if valid != 1 {
			t.Fatalf("%d of the 8 issued tokens match the stored hash, want exactly 1", valid)
		}
		accepted := 0
		for _, tok := range tokens {
			if r := fx.checkIn("s1", session, tok); r.status == 201 {
				accepted++
			} else if r.status != 403 {
				t.Errorf("unexpected %d: %s", r.status, r.body)
			}
		}
		if accepted != 1 {
			t.Fatalf("%d tokens were accepted, want exactly the last issued one", accepted)
		}
		w.must("teacher", "POST", att+"/sessions/"+session+"/close", "", 200)
	})

	t.Run("simultaneous starts open exactly one session", func(t *testing.T) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		created, conflicts := 0, 0
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := w.do("teacher", "POST", att+"/sessions", fmt.Sprintf(`{"teaching_assignment_id":%q}`, fx.assignment))
				mu.Lock()
				defer mu.Unlock()
				switch r.status {
				case 201:
					created++
				case 409:
					conflicts++
				default:
					t.Errorf("unexpected %d: %s", r.status, r.body)
				}
			}()
		}
		wg.Wait()
		if created != 1 || conflicts != 7 {
			t.Fatalf("created=%d conflicts=%d, want 1 / 7", created, conflicts)
		}
		if n := w.sqlInt(`SELECT count(*) FROM attendance_sessions WHERE teaching_assignment_id = $1 AND status = 'OPEN'`, fx.assignment); n != 1 {
			t.Fatalf("%d open sessions", n)
		}
	})

	t.Run("concurrent corrections serialize without losing an update", func(t *testing.T) {
		session := w.sqlString(`SELECT id::text FROM attendance_sessions WHERE teaching_assignment_id = $1 AND status = 'OPEN'`, fx.assignment)
		rec := idOf(w.must("teacher", "POST", att+"/sessions/"+session+"/manual", fmt.Sprintf(`{"subject_enrollment_id":%q,"status":"PRESENT"}`, fx.se2), 201))
		statuses := []string{"LATE", "ABSENT", "LATE", "ABSENT", "LATE", "ABSENT", "LATE", "ABSENT"}
		var wg sync.WaitGroup
		for _, st := range statuses {
			wg.Add(1)
			go func(st string) {
				defer wg.Done()
				if r := w.do("teacher", "PATCH", att+"/records/"+rec, fmt.Sprintf(`{"status":%q}`, st)); r.status != 200 {
					t.Errorf("PATCH %s: %d %s", st, r.status, r.body)
				}
			}(st)
		}
		wg.Wait()
		// the audit trail is an unbroken chain starting at PRESENT and ending at the stored status
		rows, err := w.db.Q(context.Background()).Query(context.Background(),
			`SELECT metadata->>'from', metadata->>'to' FROM audit_logs WHERE action = 'attendance.corrected' AND entity_id = $1 ORDER BY id`, rec)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		prev := "PRESENT"
		for rows.Next() {
			var from, to string
			_ = rows.Scan(&from, &to)
			if from != prev {
				t.Fatalf("correction went from %s but the previous status was %s: lost update", from, prev)
			}
			prev = to
		}
		var stored string
		_ = w.db.Q(context.Background()).QueryRow(context.Background(), `SELECT status FROM attendance_records WHERE id = $1`, rec).Scan(&stored)
		if prev != stored {
			t.Fatalf("audit chain ends at %s but the record is %s", prev, stored)
		}
	})
}

// --- rollback ----------------------------------------------------------------------------------

func TestAttendanceWritesRollBackWithTheirAuditRow(t *testing.T) {
	fx := newAttendanceFx(t)
	w := fx.w
	session := fx.startSession(t)
	token := fx.rotate(t, session)

	svc := attendanceapp.NewService(attendanceapp.Deps{
		Repo: attendancepg.NewRepository(w.db), Academic: attendancepg.NewAcademicReader(w.db), Tokens: attendancesec.NewQRTokens(),
		Network: attendancenet.New([]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, false),
		Clock:   clockFunc(time.Now), Audit: failingAudit{}, Tx: w.db, NewID: uuid.New, QRTTL: 30 * time.Second,
	})
	teacher := domain.Principal{UserID: uuid.MustParse(w.ids["teacher"]), SessionID: uuid.New(), Roles: []domain.Role{domain.RoleTeacher}}
	student := domain.Principal{UserID: uuid.MustParse(w.ids["s1"]), SessionID: uuid.New(), Roles: []domain.Role{domain.RoleStudent}}
	ctx := context.Background()
	sid := uuid.MustParse(session)

	if _, err := svc.CheckIn(ctx, student, attendanceapp.CheckInInput{SessionID: sid, Token: token, ClientIP: netip.MustParseAddr("192.0.2.10")}); err == nil {
		t.Fatal("check-in must fail when its audit row cannot be written")
	}
	if n := w.sqlInt(`SELECT count(*) FROM attendance_records`); n != 0 {
		t.Fatalf("the check-in survived the rollback (%d records)", n)
	}
	if _, err := svc.RegisterManually(ctx, teacher, attendanceapp.ManualInput{SessionID: sid, SubjectEnrollmentID: uuid.MustParse(fx.se1), Status: "PRESENT"}); err == nil {
		t.Fatal("manual capture must fail when its audit row cannot be written")
	}
	if _, err := svc.CloseSession(ctx, teacher, sid); err == nil {
		t.Fatal("close must fail when its audit row cannot be written")
	}
	if st := w.sqlString(`SELECT status FROM attendance_sessions WHERE id = $1`, session); st != "OPEN" {
		t.Fatalf("session status = %s after a rolled-back close, want OPEN", st)
	}
	if n := w.sqlInt(`SELECT count(*) FROM attendance_records`); n != 0 {
		t.Fatalf("the close left %d generated absences behind", n)
	}
	if w.sqlString(`SELECT COALESCE(qr_token_hash, 'none') FROM attendance_sessions WHERE id = $1`, session) != sha(token) {
		t.Fatal("a rolled-back close must keep the QR as it was")
	}
	other := fx.assignment2
	if _, err := svc.StartSession(ctx, domain.Principal{UserID: uuid.MustParse(w.ids["teacher2"]), Roles: []domain.Role{domain.RoleTeacher}},
		attendanceapp.StartSessionInput{AssignmentID: uuid.MustParse(other)}); err == nil {
		t.Fatal("start must fail when its audit row cannot be written")
	}
	if n := w.sqlInt(`SELECT count(*) FROM attendance_sessions WHERE teaching_assignment_id = $1`, other); n != 0 {
		t.Fatalf("the started session survived the rollback (%d)", n)
	}
}

type clockFunc func() time.Time

func (f clockFunc) Now() time.Time { return f() }

// --- database constraints -------------------------------------------------------------------------

func TestAttendanceDatabaseConstraints(t *testing.T) {
	fx := newAttendanceFx(t)
	w := fx.w
	ctx := context.Background()
	exec := func(sql string, args ...any) error { _, err := w.db.Q(ctx).Exec(ctx, sql, args...); return err }
	insSession := `INSERT INTO attendance_sessions (id, teaching_assignment_id, session_date, status, opened_by, closed_at, qr_token_hash, qr_expires_at, late_after_minutes, lates_per_absence)
		VALUES ($1, $2, '2026-02-02', $3, $4, $5, $6, $7, $8, $9)`
	teacher := w.ids["teacher"]
	sess := func(assignment, status string, closedAt, hash, exp any, late, lates int) error {
		return exec(insSession, uuid.New(), assignment, status, teacher, closedAt, hash, exp, late, lates)
	}

	// the open-session rule is enforced by PostgreSQL, not only by the application
	if err := sess(fx.assignment, "OPEN", nil, nil, nil, 15, 3); err != nil {
		t.Fatal(err)
	}
	if err := sess(fx.assignment, "OPEN", nil, nil, nil, 15, 3); err == nil {
		t.Error("two OPEN sessions for one assignment must violate the unique index")
	}
	if err := sess(fx.assignment, "CLOSED", time.Now(), nil, nil, 15, 3); err != nil {
		t.Errorf("any number of CLOSED sessions is fine: %v", err)
	}
	if err := sess(fx.assignment2, "OPEN", nil, nil, nil, 15, 3); err != nil {
		t.Errorf("another assignment may have its own open session: %v", err)
	}
	// configuration and state checks
	if err := sess(fx.assignment2, "CLOSED", time.Now(), nil, nil, 0, 3); err == nil {
		t.Error("late_after_minutes = 0 must be rejected")
	}
	if err := sess(fx.assignment2, "CLOSED", time.Now(), nil, nil, 15, 0); err == nil {
		t.Error("lates_per_absence = 0 must be rejected")
	}
	if err := sess(fx.assignment2, "PAUSED", nil, nil, nil, 15, 3); err == nil {
		t.Error("unknown status must be rejected")
	}
	if err := sess(fx.assignment2, "CLOSED", nil, nil, nil, 15, 3); err == nil {
		t.Error("CLOSED without closed_at must be rejected")
	}
	if err := sess(uuid.NewString(), "CLOSED", time.Now(), nil, nil, 15, 3); err == nil {
		t.Error("unknown teaching assignment must violate the FK")
	}
	// QR consistency (migration 0009)
	if err := sess(fx.assignment2, "CLOSED", time.Now(), strings.Repeat("a", 64), time.Now(), 15, 3); err == nil {
		t.Error("a CLOSED session must not hold a QR")
	}
	w.db.Q(ctx).Exec(ctx, `UPDATE attendance_sessions SET status='CLOSED', closed_at=now() WHERE teaching_assignment_id=$1`, fx.assignment2)
	if err := exec(`UPDATE attendance_sessions SET qr_token_hash = $2 WHERE teaching_assignment_id = $1 AND status = 'CLOSED'`, fx.assignment, strings.Repeat("b", 64)); err == nil {
		t.Error("a hash without an expiry (or on a closed session) must be rejected")
	}

	// records (free the assignment's SQL-seeded open session first)
	w.db.Q(ctx).Exec(ctx, `UPDATE attendance_sessions SET status='CLOSED', closed_at=now() WHERE teaching_assignment_id=$1 AND status='OPEN'`, fx.assignment)
	open := fx.startSession(t)
	insRec := `INSERT INTO attendance_records (id, attendance_session_id, subject_enrollment_id, status, method, recorded_by, ip_address) VALUES ($1, $2, $3, $4, $5, $6, $7)`
	rec := func(session, se, status, method string, ip any) error {
		return exec(insRec, uuid.New(), session, se, status, method, teacher, ip)
	}
	for _, bad := range [][2]string{{"MAYBE", "QR"}, {"PRESENT", "FAX"}, {"present", "QR"}} {
		if err := rec(open, fx.se1, bad[0], bad[1], nil); err == nil {
			t.Errorf("status/method %v must be rejected", bad)
		}
	}
	if err := rec(uuid.NewString(), fx.se1, "PRESENT", "QR", nil); err == nil {
		t.Error("unknown session must violate the FK")
	}
	if err := rec(open, uuid.NewString(), "PRESENT", "QR", nil); err == nil {
		t.Error("unknown subject enrollment must violate the FK")
	}
	if err := rec(open, fx.se1, "PRESENT", "QR", "not-an-ip"); err == nil {
		t.Error("ip_address must be a valid inet")
	}
	for i, st := range []string{"PRESENT", "LATE", "ABSENT", "JUSTIFIED"} { // every status is storable (JUSTIFIED belongs to a later module)
		w.db.Q(ctx).Exec(ctx, `DELETE FROM attendance_records`)
		if err := rec(open, fx.se1, st, []string{"QR", "NFC", "MANUAL", "MANUAL"}[i], nil); err != nil {
			t.Errorf("status %s: %v", st, err)
		}
	}
	if err := rec(open, fx.se1, "ABSENT", "MANUAL", nil); err == nil {
		t.Error("UNIQUE (session, subject enrollment) must reject a second record")
	}
	if err := rec(open, fx.se2, "PRESENT", "QR", "192.0.2.10"); err != nil {
		t.Errorf("valid record with ip: %v", err)
	}
	if got := w.sqlString(`SELECT host(ip_address) FROM attendance_records WHERE subject_enrollment_id = $1`, fx.se2); got != "192.0.2.10" {
		t.Errorf("ip round trip = %s", got)
	}
}
