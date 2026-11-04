package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	analyticspg "github.com/edutrak/backend/internal/analytics/adapters/postgres"
	analyticsapp "github.com/edutrak/backend/internal/analytics/application"
	analyticsdomain "github.com/edutrak/backend/internal/analytics/domain"
	"github.com/edutrak/backend/internal/app"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

const ax = "/api/analytics"

// afx is the tutoring fixture (2A with tutor/s1/s2, 2B with tutor2/s3) plus a seeded attendance
// history whose figures are known exactly:
//
//	MAT1  s1: 9 PRESENT, 1 LATE, 1 ABSENT   -> 11 closed records, 10/11 = 90.91%
//	      s2: 7 PRESENT, 3 ABSENT           -> 10 closed records, 7/10  = 70.00%
//	      group: 17/21 = 80.95% (NOT the mean of 90.91 and 70.00)
//	plus an OPEN session where s1 is "ABSENT", which must not be counted.
type afx struct {
	*tfx
	teacherID string
}

func (f *afx) seedSession(t *testing.T, assignment string, daysAgo int, status string, records map[string]string) {
	t.Helper()
	sid := uuid.NewString()
	d := time.Now().UTC().AddDate(0, 0, -daysAgo)
	closed := "'" + d.Format("2006-01-02") + " 08:00+00'"
	closedAt := closed
	if status == "OPEN" {
		closedAt = "NULL"
	}
	mustExecSQL(t, f.w.db, fmt.Sprintf(
		`INSERT INTO attendance_sessions (id, teaching_assignment_id, session_date, status, opened_by, opened_at, closed_at) VALUES ('%s', '%s', '%s', '%s', '%s', %s, %s)`,
		sid, assignment, d.Format("2006-01-02"), status, f.teacherID, closed, closedAt))
	for se, st := range records {
		mustExecSQL(t, f.w.db, fmt.Sprintf(
			`INSERT INTO attendance_records (id, attendance_session_id, subject_enrollment_id, status, method, recorded_by) VALUES ('%s', '%s', '%s', '%s', 'MANUAL', '%s')`,
			uuid.NewString(), sid, se, st, f.teacherID))
	}
}

func newAFx(t *testing.T) *afx {
	t.Helper()
	f := &afx{tfx: newTFx(t)}
	f.teacherID = f.w.ids["teacher"]
	// The fixture already closed one session (today): s1 and s2 ABSENT. Seed the earlier ones.
	f.seedSession(t, f.assignment, 11, "CLOSED", map[string]string{f.se1: "PRESENT"}) // only s1 has a record
	s1 := []string{"PRESENT", "LATE", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT"}
	s2 := []string{"PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "PRESENT", "ABSENT", "ABSENT"}
	for i := 0; i < 9; i++ {
		f.seedSession(t, f.assignment, 10-i, "CLOSED", map[string]string{f.se1: s1[i], f.se2: s2[i]})
	}
	f.seedSession(t, f.assignment, 0, "OPEN", map[string]string{f.se1: "ABSENT"})
	return f
}

// getters over decoded JSON
func at(m any, path ...string) any {
	for _, p := range path {
		mm, ok := m.(map[string]any)
		if !ok {
			return nil
		}
		m = mm[p]
	}
	return m
}

func num(t *testing.T, m any, path ...string) float64 {
	t.Helper()
	v, ok := at(m, path...).(float64)
	if !ok {
		t.Fatalf("%v is not a number: %v", path, at(m, path...))
	}
	return v
}

func (f *afx) recalc(who, group string) resp {
	return f.w.do(who, "POST", ax+"/alerts/recalculate", fmt.Sprintf(`{"group_id":%q}`, group))
}

func (f *afx) mustRecalc(who, group string) map[string]any {
	f.w.t.Helper()
	r := f.recalc(who, group)
	if r.status != 200 {
		f.w.t.Fatalf("recalculate as %s: %d %s", who, r.status, r.body)
	}
	return r.json(f.w.t)
}

func subjectOf(t *testing.T, m map[string]any, code string) map[string]any {
	t.Helper()
	for _, s := range m["subjects"].([]any) {
		sm := s.(map[string]any)
		if at(sm, "subject", "code") == code {
			return sm
		}
	}
	t.Fatalf("subject %s not found in %v", code, m["subjects"])
	return nil
}

func TestAnalyticsAuthenticationRequired(t *testing.T) {
	w := newWorld(t)
	id := uuid.NewString()
	for _, ep := range [][2]string{
		{"GET", ax + "/students/" + id}, {"GET", ax + "/groups/" + id}, {"GET", ax + "/overview"},
		{"GET", ax + "/alerts"}, {"GET", ax + "/alerts/" + id}, {"POST", ax + "/alerts/recalculate"},
	} {
		if r := w.c.do(ep[0], ep[1], "", `{}`); r.status != 401 {
			t.Errorf("%s %s without token: %d", ep[0], ep[1], r.status)
		}
	}
}

func TestAnalyticsStudentIndicatorsAreExact(t *testing.T) {
	f := newAFx(t)
	w := f.w
	s1, s3 := w.ids["s1"], w.ids["s3"]

	w.must("teacher", "POST", "/api/grades", f.body(f.se1, f.units[0], "76"), 201)
	w.must("teacher", "POST", "/api/grades", f.body(f.se1, f.units[1], "90"), 201)
	f.mustFollow("tutor", s1, f.group, "primer seguimiento")
	f.mustFollow("tutor", s1, f.group, "segundo seguimiento")
	// Two requests for the SAME absence (one rejected, one pending) are two requests but one absence.
	rec := w.sqlString(`SELECT r.id::text FROM attendance_records r JOIN attendance_sessions s ON s.id = r.attendance_session_id
	                     WHERE r.subject_enrollment_id = $1 AND s.session_date = (SELECT max(session_date) FROM attendance_sessions WHERE status = 'CLOSED' AND teaching_assignment_id = $2) AND r.status = 'ABSENT'`, f.se1, f.assignment)
	first := idOf(w.must("s1", "POST", jx, fmt.Sprintf(`{"attendance_record_id":%q,"reason":"Consulta"}`, rec), 201))
	w.must("tutor", "POST", jx+"/"+first+"/tutor-decision", `{"decision":"REJECTED","comment":"Sin comprobante"}`, 200)
	w.must("s1", "POST", jx, fmt.Sprintf(`{"attendance_record_id":%q,"reason":"Con comprobante"}`, rec), 201)

	v := w.must("tutor", "GET", ax+"/students/"+s1+"?group_id="+f.group, "", 200)
	if at(v, "group", "code") != "2A" || at(v, "academic_term", "name") != "2026-1" || at(v, "student", "institutional_number") != "40000001" || at(v, "enrollment_status") != "ACTIVE" {
		t.Fatalf("context: %v", v)
	}
	mat := subjectOf(t, v, "MAT1")
	// attendance: open session excluded; JUSTIFIED/ABSENT/LATE kept apart; denominator excludes justified
	for k, want := range map[string]float64{"closed_session_records": 11, "present": 9, "late": 1, "absent": 1, "justified": 0, "unjustified_absences": 1, "absences_from_lates": 0, "effective_absences": 1, "lates_per_absence": 3} {
		if got := num(t, mat, "attendance", k); got != want {
			t.Errorf("attendance.%s = %v, want %v", k, got, want)
		}
	}
	if !strings.Contains(w.do("tutor", "GET", ax+"/students/"+s1+"?group_id="+f.group, "").body, `"attendance_pct":90.91`) {
		t.Error("attendance_pct must be 90.91 with two decimals")
	}
	// trend: last 5 closed sessions 80% vs previous 5 100%
	if num(t, mat, "attendance_trend", "window_sessions") != 5 || num(t, mat, "attendance_trend", "recent", "attendance_pct") != 80 ||
		num(t, mat, "attendance_trend", "previous", "attendance_pct") != 100 || num(t, mat, "attendance_trend", "drop_points") != 20 {
		t.Errorf("trend: %v", mat["attendance_trend"])
	}
	// grades: 2 captured, 5 pending (never NA), one real NA
	for k, want := range map[string]float64{"units_total": 7, "units_graded": 2, "units_pending": 5, "na": 1, "sa": 0, "de": 1, "au": 0} {
		if got := num(t, mat, "grades", k); got != want {
			t.Errorf("grades.%s = %v, want %v", k, got, want)
		}
	}
	naUnits := at(mat, "grades", "na_units").([]any)
	if len(naUnits) != 1 || num(t, naUnits[0], "score") != 76 || at(naUnits[0], "category") != "NA" || num(t, naUnits[0], "unit_number") != 1 {
		t.Errorf("na_units: %v", naUnits)
	}
	// justifications: requests vs absences
	for k, want := range map[string]float64{"requests_total": 2, "pending_tutor": 1, "tutor_rejected": 1, "approved": 0, "pending_teacher": 0, "absences_with_request": 1} {
		if got := num(t, mat, "justifications", k); got != want {
			t.Errorf("justifications.%s = %v, want %v", k, got, want)
		}
	}
	// FIS1: no records -> null percentage and trend, all units pending
	fis := subjectOf(t, v, "FIS1")
	if at(fis, "attendance", "attendance_pct") != nil || fis["attendance_trend"] != nil || num(t, fis, "grades", "units_pending") != 2 || num(t, fis, "attendance", "closed_session_records") != 0 {
		t.Errorf("FIS1: %v", fis)
	}
	if num(t, fis, "attendance", "unjustified_absences") != 0 || at(fis, "attendance", "lates_per_absence") != nil {
		t.Errorf("FIS1 attendance: %v", fis["attendance"])
	}
	// totals sum subjects
	if num(t, v, "totals", "attendance", "closed_session_records") != 11 || num(t, v, "totals", "grades", "units_total") != 9 || num(t, v, "totals", "grades", "units_pending") != 7 {
		t.Errorf("totals: %v", v["totals"])
	}
	// tutoring: counts only, never the text
	if num(t, v, "tutoring", "followups_count") != 2 || at(v, "tutoring", "last_followup_date") == nil {
		t.Errorf("tutoring: %v", v["tutoring"])
	}
	signals := map[string]bool{}
	for _, s := range v["signals"].([]any) {
		signals[at(s, "rule").(string)] = true
		if at(s, "message") == "" || at(s, "rule_description") == "" || at(s, "evidence") == nil {
			t.Errorf("a signal must explain itself: %v", s)
		}
	}
	if len(signals) != 3 || !signals["GRADE_NA_UNIT"] || !signals["ATTENDANCE_DROP"] || !signals["PENDING_JUSTIFICATIONS"] {
		t.Errorf("signals: %v", signals)
	}
	if n := len(v["alerts"].([]any)); n != 0 {
		t.Errorf("nothing was recalculated yet, got %d stored alerts", n)
	}
	if strings.Contains(w.do("tutor", "GET", ax+"/students/"+s1+"?group_id="+f.group, "").body, "seguimiento") {
		t.Error("tutoring notes must never appear in analytics")
	}

	// A student without any data: absence of data, not zeros.
	none := w.must("tutor2", "GET", ax+"/students/"+s3, "", 200)
	if len(none["subjects"].([]any)) != 0 || num(t, none, "totals", "attendance", "closed_session_records") != 0 || at(none, "totals", "attendance", "attendance_pct") != nil ||
		len(none["signals"].([]any)) != 0 || num(t, none, "tutoring", "followups_count") != 0 || at(none, "tutoring", "last_followup_date") != nil {
		t.Errorf("empty student: %v", none)
	}
}

func TestAnalyticsGroupAndOverviewAgree(t *testing.T) {
	f := newAFx(t)
	w := f.w
	w.must("teacher", "POST", "/api/grades", f.body(f.se1, f.units[0], "76"), 201)
	w.must("teacher", "POST", "/api/grades", f.body(f.se1, f.units[1], "90"), 201)
	f.mustFollow("tutor", w.ids["s1"], f.group, "uno")
	f.created(t) // a pending request for s1's absence
	f.mustRecalc("tutor", f.group)

	g := w.must("tutor", "GET", ax+"/groups/"+f.group, "", 200)
	if num(t, g, "active_students") != 2 || num(t, g, "attendance", "closed_session_records") != 21 || num(t, g, "attendance", "present") != 16 ||
		num(t, g, "attendance", "late") != 1 || num(t, g, "attendance", "absent") != 4 || num(t, g, "attendance", "justified") != 0 {
		t.Errorf("attendance: %v", g["attendance"])
	}
	if !strings.Contains(w.do("tutor", "GET", ax+"/groups/"+f.group, "").body, `"attendance_pct":80.95`) {
		t.Error("group attendance_pct must be 17/21 = 80.95, not an average of percentages")
	}
	// 2 students x 7 units (MAT1) + 1 student x 2 units (FIS1)
	for k, want := range map[string]float64{"units_total": 16, "units_graded": 2, "units_pending": 14, "na": 1, "de": 1} {
		if got := num(t, g, "grades", k); got != want {
			t.Errorf("grades.%s = %v, want %v", k, got, want)
		}
	}
	if num(t, g, "tutoring", "students_with_followup") != 1 || num(t, g, "tutoring", "students_without_followup") != 1 || num(t, g, "tutoring", "followups_count") != 1 {
		t.Errorf("tutoring: %v", g["tutoring"])
	}
	by := g["by_assignment"].([]any)
	if len(by) != 2 {
		t.Fatalf("assignments: %v", by)
	}
	for _, a := range by {
		switch at(a, "subject", "code") {
		case "FIS1":
			if num(t, a, "active_subject_enrollments") != 1 || at(a, "attendance", "attendance_pct") != nil {
				t.Errorf("FIS1: %v", a)
			}
		case "MAT1":
			if num(t, a, "active_subject_enrollments") != 2 || num(t, a, "attendance", "closed_session_records") != 21 {
				t.Errorf("MAT1: %v", a)
			}
		}
	}
	rows := g["students"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows: %v", rows)
	}
	if num(t, g, "signals_now", "GRADE_NA_UNIT") != 1 || num(t, g, "signals_now", "UNJUSTIFIED_ABSENCES") != 1 || num(t, g, "signals_now", "ATTENDANCE_DROP") != 2 || num(t, g, "signals_now", "PENDING_JUSTIFICATIONS") != 1 {
		t.Errorf("signals_now: %v", g["signals_now"])
	}
	// after the recalculation the stored alerts match the live signals
	for _, rule := range []string{"GRADE_NA_UNIT", "UNJUSTIFIED_ABSENCES", "ATTENDANCE_DROP", "PENDING_JUSTIFICATIONS"} {
		if num(t, g, "open_alerts", rule) != num(t, g, "signals_now", rule) {
			t.Errorf("open_alerts.%s differs from signals_now", rule)
		}
	}

	// A group with no attendance records: null, not 0%.
	g2 := w.must("tutor2", "GET", ax+"/groups/"+f.group2B, "", 200)
	if num(t, g2, "active_students") != 1 || at(g2, "attendance", "attendance_pct") != nil || num(t, g2, "attendance", "closed_session_records") != 0 {
		t.Errorf("empty group: %v", g2)
	}

	// The overview is computed by SQL aggregates and must agree with the group view.
	ov := w.must("director", "GET", ax+"/overview?academic_term_id="+f.term, "", 200)["data"].([]any)
	if len(ov) != 2 {
		t.Fatalf("overview rows: %d", len(ov))
	}
	for _, row := range ov {
		if at(row, "group", "code") != "2A" {
			continue
		}
		for _, k := range []string{"closed_session_records", "present", "late", "absent", "justified"} {
			if num(t, row, "attendance", k) != num(t, g, "attendance", k) {
				t.Errorf("overview attendance.%s = %v, group view %v", k, num(t, row, "attendance", k), num(t, g, "attendance", k))
			}
		}
		for _, k := range []string{"units_total", "units_graded", "units_pending", "na", "sa", "de", "au"} {
			if num(t, row, "grades", k) != num(t, g, "grades", k) {
				t.Errorf("overview grades.%s differs", k)
			}
		}
		if num(t, row, "justifications", "pending_tutor") != 1 || num(t, row, "tutoring", "students_with_followup") != 1 || num(t, row, "active_students") != 2 {
			t.Errorf("overview row: %v", row)
		}
		if num(t, row, "open_alerts") != 5 {
			t.Errorf("open alerts = %v", at(row, "open_alerts"))
		}
		if at(row, "attendance", "attendance_pct") != at(g, "attendance", "attendance_pct") {
			t.Errorf("overview pct %v vs group %v", at(row, "attendance", "attendance_pct"), at(g, "attendance", "attendance_pct"))
		}
	}
	// Scope and filters: the tutor sees only his group; unknown filters are 422.
	if n := w.count("tutor", ax+"/overview"); n != 1 {
		t.Errorf("tutor overview = %d", n)
	}
	if n := w.count("director", ax+"/overview?career_id="+f.career); n != 2 {
		t.Errorf("career filter = %d", n)
	}
	if n := w.count("director", ax+"/overview?career_id="+uuid.NewString()); n != 0 {
		t.Errorf("unknown career = %d", n)
	}
	w.expect("director", "GET", ax+"/overview?academic_term_id=zz", "", 422, "invalid_filter")
}

// checkRecalcAuditMetadata verifies that the audit metadata of a recalculation carries exactly the
// group id and the three counters, so no grade, message or evidence can be hiding in it.
func checkRecalcAuditMetadata(raw, group string) error {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return err
	}
	for k, v := range m {
		switch k {
		case "group_id":
			if v != group {
				return fmt.Errorf("group_id = %v", v)
			}
		case "created", "updated", "resolved":
			if _, ok := v.(float64); !ok {
				return fmt.Errorf("%s is not a counter: %v", k, v)
			}
		default:
			return fmt.Errorf("unexpected key %q", k)
		}
	}
	if len(m) != 4 {
		return fmt.Errorf("expected group_id and 3 counters, got %d keys", len(m))
	}
	return nil
}

// Deterministic regression: digits such as 76 inside an id are fine; a score key is not.
func TestRecalcAuditMetadataCheckIgnoresDigitsInIds(t *testing.T) {
	group := "00007600-1176-4076-8076-000000000076"
	ok := `{"group_id":"` + group + `","created":5,"updated":0,"resolved":0}`
	if err := checkRecalcAuditMetadata(ok, group); err != nil {
		t.Fatalf("a UUID containing 76 must not be flagged: %v", err)
	}
	for _, bad := range []string{
		`{"group_id":"` + group + `","created":5,"updated":0,"resolved":0,"score":76}`,
		`{"group_id":"` + group + `","created":5,"updated":0,"resolved":0,"message":"76.00"}`,
		`{"group_id":"` + group + `","created":"76","updated":0,"resolved":0}`,
		`{"group_id":"` + group + `","created":5,"updated":0}`,
	} {
		if checkRecalcAuditMetadata(bad, group) == nil {
			t.Errorf("must be flagged: %s", bad)
		}
	}
}

func TestAnalyticsRecalculationFollowsTheData(t *testing.T) {
	f := newAFx(t)
	w := f.w
	s1 := w.ids["s1"]
	g1 := idOf(w.must("teacher", "POST", "/api/grades", f.body(f.se1, f.units[0], "76"), 201))
	f.created(t)

	r := f.mustRecalc("tutor", f.group)
	// s1: NA unit, attendance drop, pending request · s2: 3 unjustified absences, attendance drop
	if num(t, r, "created") != 5 || num(t, r, "updated") != 0 || num(t, r, "resolved") != 0 {
		t.Fatalf("first run: %v", r)
	}
	if n := w.sqlInt(`SELECT count(*) FROM alerts WHERE group_id = $1`, f.group); n != 5 {
		t.Fatalf("stored alerts = %d", n)
	}
	// Second run: idempotent, no duplicates, and no new audit event.
	r = f.mustRecalc("tutor", f.group)
	if num(t, r, "created") != 0 || num(t, r, "unchanged") != 5 || w.sqlInt(`SELECT count(*) FROM alerts`) != 5 {
		t.Fatalf("second run: %v", r)
	}
	if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'analytics.alerts_recalculated'`); n != 1 {
		t.Fatalf("audit events = %d, want 1 (runs without changes are not audited)", n)
	}
	// Privacy contract of the audit event: ids and counts only. This inspects the structure of the
	// metadata, not a substring: the group id is a random UUID, which contains "76" by chance in
	// about 1 run in 10 (that made the former LIKE '%76%' check fail intermittently).
	meta := w.sqlString(`SELECT metadata::text FROM audit_logs WHERE action = 'analytics.alerts_recalculated'`)
	if err := checkRecalcAuditMetadata(meta, f.group); err != nil {
		t.Fatalf("scores must not be written to the audit trail: %v (metadata %s)", err, meta)
	}

	// Reading shows each alert with its explanation.
	list := w.must("tutor", "GET", ax+"/alerts", "", 200)
	if num(t, list, "total") != 5 || len(list["data"].([]any)) != 5 || num(t, list, "limit") != 50 {
		t.Fatalf("list: %v", list)
	}
	var naID string
	for _, a := range list["data"].([]any) {
		if at(a, "rule") == "GRADE_NA_UNIT" {
			naID = at(a, "id").(string)
			if at(a, "message") != "El alumno tiene una calificación de 76.00 (NA) en la Unidad 1 de Matematicas I." || at(a, "status") != "OPEN" ||
				at(a, "evidence", "score") != "76.00" || at(a, "evidence", "group_code") != "2A" || at(a, "evidence", "academic_term") != "2026-1" ||
				at(a, "subject", "code") != "MAT1" || at(a, "student", "id") != s1 || at(a, "rule_description") == "" || at(a, "evaluated_at") == nil || at(a, "resolved_at") != nil {
				t.Errorf("NA alert: %v", a)
			}
		}
	}
	d := w.must("tutor", "GET", ax+"/alerts/"+naID, "", 200)
	if at(d, "id") != naID {
		t.Fatalf("detail: %v", d)
	}

	// Filters and pagination.
	if n := w.count("tutor", ax+"/alerts?type=ATTENDANCE_DROP"); n != 2 {
		t.Errorf("type filter = %d", n)
	}
	if n := w.count("tutor", ax+"/alerts?student_id="+s1); n != 3 {
		t.Errorf("student filter = %d", n)
	}
	if n := w.count("tutor", ax+"/alerts?group_id="+f.group+"&academic_term_id="+f.term); n != 5 {
		t.Errorf("group/term filter = %d", n)
	}
	if n := w.count("tutor", ax+"/alerts?status=RESOLVED"); n != 0 {
		t.Errorf("resolved filter = %d", n)
	}
	p := w.must("tutor", "GET", ax+"/alerts?limit=2&offset=4", "", 200)
	if len(p["data"].([]any)) != 1 || num(t, p, "total") != 5 || num(t, p, "offset") != 4 {
		t.Errorf("page: %v", p)
	}

	// The teacher corrects NA -> SA: the next recalculation resolves exactly that alert.
	w.must("teacher", "PATCH", "/api/grades/"+g1, `{"score":82}`, 200)
	if st := at(w.must("tutor", "GET", ax+"/alerts/"+naID, "", 200), "status"); st != "OPEN" {
		t.Fatalf("alerts are only as fresh as the last recalculation, status = %v", st)
	}
	r = f.mustRecalc("tutor", f.group)
	if num(t, r, "resolved") != 1 || num(t, r, "created") != 0 {
		t.Fatalf("after the correction: %v", r)
	}
	d = w.must("tutor", "GET", ax+"/alerts/"+naID, "", 200)
	if at(d, "status") != "RESOLVED" || at(d, "resolved_at") == nil {
		t.Fatalf("resolved alert: %v", d)
	}
	if n := w.count("tutor", ax+"/alerts"); n != 4 {
		t.Errorf("unresolved = %d", n)
	}
	if n := w.count("tutor", ax+"/alerts?status=ALL"); n != 5 {
		t.Errorf("all = %d", n)
	}
	// The NA comes back: a NEW alert opens and the resolved one remains as history.
	w.must("teacher", "PATCH", "/api/grades/"+g1, `{"score":60}`, 200)
	f.mustRecalc("tutor", f.group)
	if w.sqlInt(`SELECT count(*) FROM alerts WHERE type = 'GRADE_NA_UNIT'`) != 2 || w.sqlInt(`SELECT count(*) FROM alerts WHERE type = 'GRADE_NA_UNIT' AND status <> 'RESOLVED'`) != 1 {
		t.Fatal("a recurring condition must open a new alert and keep the resolved one")
	}
	// 60.00 -> 65.50 (still NA): the same alert is refreshed, not duplicated.
	w.must("teacher", "PATCH", "/api/grades/"+g1, `{"score":65.5}`, 200)
	r = f.mustRecalc("tutor", f.group)
	if num(t, r, "updated") != 1 || num(t, r, "created") != 0 {
		t.Fatalf("refresh: %v", r)
	}

	// An absence becomes JUSTIFIED through the real flow: the pending alert and the drop clear.
	req := w.sqlString(`SELECT id::text FROM justification_requests LIMIT 1`)
	w.must("tutor", "POST", jx+"/"+req+"/tutor-decision", `{"decision":"APPROVED"}`, 200)
	r = f.mustRecalc("tutor", f.group)
	if num(t, r, "unchanged") == 0 { // pending moved from the tutor to the teacher: same alert, new counters
		t.Logf("recalc after tutor approval: %v", r)
	}
	if got := w.sqlString(`SELECT evidence->>'pending_teacher' FROM alerts WHERE type = 'PENDING_JUSTIFICATIONS' AND status <> 'RESOLVED'`); got != "1" {
		t.Fatalf("pending alert evidence after tutor approval: pending_teacher = %s", got)
	}
	w.must("teacher", "POST", jx+"/"+req+"/teacher-decision", `{"decision":"APPROVED"}`, 200)
	f.mustRecalc("tutor", f.group)
	if n := w.sqlInt(`SELECT count(*) FROM alerts WHERE type = 'PENDING_JUSTIFICATIONS' AND status <> 'RESOLVED'`); n != 0 {
		t.Errorf("pending alert survived the resolution: %d", n)
	}
	if n := w.sqlInt(`SELECT count(*) FROM alerts WHERE type = 'ATTENDANCE_DROP' AND student_id = $1 AND status <> 'RESOLVED'`, s1); n != 0 {
		t.Errorf("s1's attendance drop must clear once the absence is JUSTIFIED (4/4 vs 4/4): %d", n)
	}
	v := w.must("tutor", "GET", ax+"/students/"+s1+"?group_id="+f.group, "", 200)
	mat := subjectOf(t, v, "MAT1")
	if num(t, mat, "attendance", "justified") != 1 || num(t, mat, "attendance", "absent") != 0 || num(t, mat, "justifications", "approved") != 1 {
		t.Errorf("after approval: %v", mat)
	}
	// Single-student recalculation touches only that student's alerts.
	if r := w.do("tutor", "POST", ax+"/alerts/recalculate", fmt.Sprintf(`{"group_id":%q,"student_id":%q}`, f.group, w.ids["s2"])); r.status != 200 {
		t.Errorf("student recalculation: %d %s", r.status, r.body)
	}
}

func TestAnalyticsAuthorizationAndErrors(t *testing.T) {
	f := newAFx(t)
	w := f.w
	s1, s3 := w.ids["s1"], w.ids["s3"]
	f.mustRecalc("tutor", f.group)
	alert := idOf(w.must("tutor", "GET", ax+"/alerts", "", 200)["data"].([]any)[0].(map[string]any))

	for _, who := range []string{"admin", "teacher", "teacher2", "s1", "s2"} {
		for _, ep := range [][2]string{
			{"GET", "/students/" + s1}, {"GET", "/groups/" + f.group}, {"GET", "/overview"}, {"GET", "/alerts"}, {"GET", "/alerts/" + alert},
		} {
			w.expect(who, ep[0], ax+ep[1], "", 403, "forbidden")
		}
		w.expect(who, "POST", ax+"/alerts/recalculate", fmt.Sprintf(`{"group_id":%q}`, f.group), 403, "forbidden")
	}
	// A foreign tutor: 403 on every resource of another group, and nothing in his lists.
	w.expect("tutor2", "GET", ax+"/students/"+s1+"?group_id="+f.group, "", 403, "forbidden")
	w.expect("tutor2", "GET", ax+"/students/"+s1, "", 403, "forbidden")
	w.expect("tutor2", "GET", ax+"/groups/"+f.group, "", 403, "forbidden")
	w.expect("tutor2", "GET", ax+"/alerts/"+alert, "", 403, "forbidden")
	w.expect("tutor2", "POST", ax+"/alerts/recalculate", fmt.Sprintf(`{"group_id":%q}`, f.group), 403, "forbidden")
	w.expect("tutor", "GET", ax+"/students/"+s3, "", 403, "forbidden")
	if n := w.count("tutor2", ax+"/alerts"); n != 0 {
		t.Errorf("tutor2 lists %d alerts of another group", n)
	}
	if n := w.count("tutor2", ax+"/alerts?student_id="+s1); n != 0 {
		t.Errorf("enumeration through filters: %d", n)
	}
	if body := w.do("tutor2", "GET", ax+"/alerts?group_id="+f.group, "").body; strings.Contains(body, s1) {
		t.Error("another student's data leaked into a filtered list")
	}
	// Director: reads and may recalculate any group.
	for _, ep := range []string{"/students/" + s1, "/groups/" + f.group, "/overview", "/alerts", "/alerts/" + alert} {
		w.must("director", "GET", ax+ep, "", 200)
	}
	if n := w.count("director", ax+"/alerts"); n != 3 { // s1 drop; s2 absences + drop (no grades, no requests here)
		t.Errorf("director alerts = %d", n)
	}
	f.mustRecalc("director", f.group)

	// 404: nonexistent or malformed ids.
	w.expect("tutor", "GET", ax+"/students/"+uuid.NewString(), "", 404, "student_not_found")
	w.expect("tutor", "GET", ax+"/students/zz", "", 404, "student_not_found")
	w.expect("tutor", "GET", ax+"/students/"+s1+"?group_id="+f.group2B, "", 404, "enrollment_not_found")
	w.expect("tutor", "GET", ax+"/groups/"+uuid.NewString(), "", 404, "group_not_found")
	w.expect("tutor", "GET", ax+"/groups/zz", "", 404, "group_not_found")
	w.expect("tutor", "GET", ax+"/alerts/"+uuid.NewString(), "", 404, "alert_not_found")
	w.expect("tutor", "GET", ax+"/alerts/zz", "", 404, "alert_not_found")
	w.expect("tutor", "POST", ax+"/alerts/recalculate", fmt.Sprintf(`{"group_id":%q}`, uuid.NewString()), 404, "group_not_found")
	w.expect("tutor", "POST", ax+"/alerts/recalculate", fmt.Sprintf(`{"group_id":%q,"student_id":%q}`, f.group, s3), 404, "enrollment_not_found")
	// 422 / 400
	for _, q := range []string{"limit=0", "limit=201", "limit=x", "offset=-1"} {
		w.expect("tutor", "GET", ax+"/alerts?"+q, "", 422, "invalid_paging")
	}
	for _, q := range []string{"status=bogus", "type=RISK_SCORE", "student_id=zz", "group_id=zz", "academic_term_id=zz"} {
		w.expect("tutor", "GET", ax+"/alerts?"+q, "", 422, "invalid_filter")
	}
	w.expect("tutor", "GET", ax+"/students/"+s1+"?group_id=zz", "", 422, "invalid_filter")
	w.expect("tutor", "POST", ax+"/alerts/recalculate", `{}`, 422, "group_id_required")
	w.expect("tutor", "POST", ax+"/alerts/recalculate", `{"group_id":"zz"}`, 422, "invalid_group_id")
	w.expect("tutor", "POST", ax+"/alerts/recalculate", fmt.Sprintf(`{"group_id":%q,"student_id":"zz"}`, f.group), 422, "invalid_student_id")
	w.expect("tutor", "POST", ax+"/alerts/recalculate", fmt.Sprintf(`{"group_id":%q,"tutor_id":"x"}`, f.group), 400, "invalid_body")
	w.expect("tutor", "POST", ax+"/alerts/recalculate", `{"group_id":`, 400, "invalid_body")

	// 409: a closed term is frozen.
	w.must("director", "POST", "/api/academic/terms/"+f.term+"/close", "", 200)
	w.expect("tutor", "POST", ax+"/alerts/recalculate", fmt.Sprintf(`{"group_id":%q}`, f.group), 409, "term_closed")
	w.must("tutor", "GET", ax+"/groups/"+f.group, "", 200) // reads keep working
}

func TestAnalyticsFollowsTutorChangeAndKeepsPeriodsApart(t *testing.T) {
	f := newAFx(t)
	w := f.w
	s1 := w.ids["s1"]
	f.mustRecalc("tutor", f.group)
	alert := idOf(w.must("tutor", "GET", ax+"/alerts?student_id="+s1, "", 200)["data"].([]any)[0].(map[string]any))

	// Second period: s1 enrolled in group 3A (tutor "tutor") taking MAT1 again (attempt 2).
	term2 := idOf(w.must("director", "POST", "/api/academic/terms", `{"name":"2026-2","start_date":"2026-05-05","end_date":"2026-08-30"}`, 201))
	w.must("director", "POST", "/api/academic/terms/"+term2+"/activate", "", 200)
	g2 := idOf(w.must("director", "POST", "/api/academic/groups", fmt.Sprintf(`{"academic_term_id":%q,"career_id":%q,"code":"3A","level":2,"tutor_id":%q}`, term2, f.career, w.ids["tutor"]), 201))
	w.must("director", "POST", "/api/academic/groups/"+g2+"/enrollments", fmt.Sprintf(`{"student_id":%q}`, s1), 201)
	subject := w.sqlString(`SELECT subject_id::text FROM teaching_assignments WHERE id = $1`, f.assignment)
	asg2 := idOf(w.must("director", "POST", "/api/academic/groups/"+g2+"/teaching-assignments", fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, subject, w.ids["teacher"]), 201))
	se2 := idOf(w.must("director", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q}`, s1, asg2), 201))
	if w.sqlInt(`SELECT attempt_number FROM subject_enrollments WHERE id = $1`, se2) != 2 {
		t.Fatal("expected the second attempt")
	}
	units2 := w.must("director", "GET", "/api/academic/subjects/"+subject+"/units", "", 200)["data"].([]any)
	w.must("teacher", "POST", "/api/grades", f.body(se2, idOf(units2[0].(map[string]any)), "50"), 201)

	// Default context = most recent period; explicit group_id returns the other, never mixed.
	v := w.must("tutor", "GET", ax+"/students/"+s1, "", 200)
	mat := subjectOf(t, v, "MAT1")
	if at(v, "group", "code") != "3A" || num(t, mat, "attempt_number") != 2 || num(t, mat, "grades", "na") != 1 || num(t, mat, "attendance", "closed_session_records") != 0 {
		t.Errorf("period 2: %v", v)
	}
	v1 := w.must("tutor", "GET", ax+"/students/"+s1+"?group_id="+f.group, "", 200)
	if m := subjectOf(t, v1, "MAT1"); num(t, m, "attempt_number") != 1 || num(t, m, "grades", "na") != 0 || num(t, m, "attendance", "closed_session_records") != 11 {
		t.Errorf("period 1: %v", m)
	}
	// Recalculating period 2 never touches period 1's alerts, and the alerts keep their own group.
	before := w.sqlInt(`SELECT count(*) FROM alerts WHERE group_id = $1`, f.group)
	f.mustRecalc("tutor", g2)
	if w.sqlInt(`SELECT count(*) FROM alerts WHERE group_id = $1`, f.group) != before || w.sqlInt(`SELECT count(*) FROM alerts WHERE group_id = $1 AND type = 'GRADE_NA_UNIT'`, g2) != 1 {
		t.Fatal("alerts of different periods got mixed")
	}
	if n := w.count("tutor", ax+"/alerts?group_id="+g2); n != 1 {
		t.Errorf("period 2 alerts = %d", n)
	}

	// The group changes tutor: the old one is out immediately, the new one inherits the alerts.
	w.must("director", "PUT", "/api/academic/groups/"+f.group+"/tutor", fmt.Sprintf(`{"tutor_id":%q}`, w.ids["tutor2"]), 200)
	w.expect("tutor", "GET", ax+"/alerts/"+alert, "", 403, "forbidden")
	w.expect("tutor", "GET", ax+"/groups/"+f.group, "", 403, "forbidden")
	w.expect("tutor", "GET", ax+"/students/"+s1+"?group_id="+f.group, "", 403, "forbidden")
	w.expect("tutor", "POST", ax+"/alerts/recalculate", fmt.Sprintf(`{"group_id":%q}`, f.group), 403, "forbidden")
	if n := w.count("tutor", ax+"/alerts?group_id="+f.group); n != 0 {
		t.Errorf("old tutor lists %d alerts", n)
	}
	w.must("tutor2", "GET", ax+"/alerts/"+alert, "", 200)
	if n := w.count("tutor2", ax+"/alerts?group_id="+f.group); n != before {
		t.Errorf("new tutor lists %d alerts, want %d", n, before)
	}
	f.mustRecalc("tutor2", f.group)
	// The other group of the old tutor is unaffected.
	w.must("tutor", "GET", ax+"/groups/"+g2, "", 200)
}

func TestAnalyticsRecalculationRollsBackWithItsAuditRow(t *testing.T) {
	f := newAFx(t)
	w := f.w
	svc := analyticsapp.NewService(analyticsapp.Deps{
		Facts: analyticspg.NewFacts(w.db), Alerts: analyticspg.NewAlerts(w.db), Audit: failingAudit{}, Tx: w.db,
		Clock: clockFunc(time.Now), NewID: uuid.New, Rules: analyticsdomain.DefaultRules(),
	})
	tutor := iddomain.Principal{UserID: uuid.MustParse(w.ids["tutor"]), SessionID: uuid.New(), Roles: []iddomain.Role{iddomain.RoleTutor}}
	if _, err := svc.RecalculateAlerts(context.Background(), tutor, uuid.MustParse(f.group), nil); err == nil {
		t.Fatal("recalculation must fail when its audit event cannot be written")
	}
	if n := w.sqlInt(`SELECT count(*) FROM alerts`); n != 0 {
		t.Fatalf("alerts survived the rollback (%d rows)", n)
	}
	// With audit working the same call succeeds, proving the data (not the setup) was the issue.
	f.mustRecalc("tutor", f.group)
	if w.sqlInt(`SELECT count(*) FROM alerts`) == 0 {
		t.Fatal("expected alerts")
	}
}

func TestAnalyticsConcurrentEvaluationsNeverDuplicate(t *testing.T) {
	f := newAFx(t)
	w := f.w
	w.must("teacher", "POST", "/api/grades", f.body(f.se1, f.units[0], "76"), 201)
	f.created(t)

	const n = 10
	var wg sync.WaitGroup
	created := make([]int, n)
	statuses := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			who := "tutor"
			if i%3 == 0 {
				who = "director"
			}
			r := f.recalc(who, f.group)
			statuses[i] = r.status
			if r.status == 200 {
				created[i] = int(r.json(t)["created"].(float64))
			}
		}(i)
	}
	wg.Wait()
	total := 0
	for i := range statuses {
		if statuses[i] != 200 {
			t.Fatalf("recalculation %d -> %d", i, statuses[i])
		}
		total += created[i]
	}
	if total != 5 || w.sqlInt(`SELECT count(*) FROM alerts`) != 5 {
		t.Fatalf("created %d, stored %d, want exactly 5", total, w.sqlInt(`SELECT count(*) FROM alerts`))
	}
	if ev := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'analytics.alerts_recalculated'`); ev != 1 {
		t.Fatalf("audit events = %d, want 1", ev)
	}

	// Evaluations racing with data changes: after the dust settles one more run converges on the truth.
	g := idOf(w.must("teacher", "POST", "/api/grades", f.body(f.se2, f.units[0], "70"), 201))
	var wg2 sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg2.Add(1)
		go func(i int) {
			defer wg2.Done()
			if i == 4 {
				w.do("teacher", "PATCH", "/api/grades/"+g, `{"score":95}`)
				return
			}
			if r := f.recalc("tutor", f.group); r.status != 200 {
				t.Errorf("racing recalculation -> %d %s", r.status, r.body)
			}
		}(i)
	}
	wg2.Wait()
	f.mustRecalc("tutor", f.group)
	live := w.must("tutor", "GET", ax+"/groups/"+f.group, "", 200)
	for _, rule := range []string{"GRADE_NA_UNIT", "UNJUSTIFIED_ABSENCES", "ATTENDANCE_DROP", "PENDING_JUSTIFICATIONS"} {
		if num(t, live, "open_alerts", rule) != num(t, live, "signals_now", rule) {
			t.Errorf("%s: stored %v, live %v", rule, at(live, "open_alerts", rule), at(live, "signals_now", rule))
		}
	}
	if d := w.sqlInt(`SELECT count(*) FROM (SELECT student_id, type, condition_key FROM alerts WHERE status <> 'RESOLVED' GROUP BY 1,2,3 HAVING count(*) > 1) x`); d != 0 {
		t.Fatalf("%d duplicated unresolved conditions", d)
	}
}

func TestAnalyticsDatabaseConstraints(t *testing.T) {
	f := newAFx(t)
	w := f.w
	f.mustRecalc("tutor", f.group)
	ctx := context.Background()
	exec := func(q string, args ...any) error { _, err := w.db.Q(ctx).Exec(ctx, q, args...); return err }
	row := w.sqlString(`SELECT id::text FROM alerts WHERE type = 'UNJUSTIFIED_ABSENCES' LIMIT 1`)
	student := w.sqlString(`SELECT student_id::text FROM alerts WHERE id = $1`, row)
	key := w.sqlString(`SELECT condition_key FROM alerts WHERE id = $1`, row)

	ins := `INSERT INTO alerts (id, student_id, type, message, group_id, condition_key, status, resolved_at) VALUES ($1, $2, $3, 'm', $4, $5, $6, $7)`
	if err := exec(ins, uuid.New(), student, "UNJUSTIFIED_ABSENCES", f.group, key, "OPEN", nil); err == nil {
		t.Error("two unresolved alerts for the same condition must be impossible")
	}
	if err := exec(ins, uuid.New(), student, "UNJUSTIFIED_ABSENCES", f.group, key, "RESOLVED", time.Now()); err != nil {
		t.Errorf("resolved copies are history and must be allowed: %v", err)
	}
	if err := exec(ins, uuid.New(), student, "RISK_SCORE", f.group, "x", "OPEN", nil); err == nil {
		t.Error("unknown alert type accepted")
	}
	if err := exec(ins, uuid.New(), student, "ATTENDANCE_DROP", f.group, "x", "RESOLVED", nil); err == nil {
		t.Error("a RESOLVED alert needs resolved_at")
	}
	if err := exec(ins, uuid.New(), student, "ATTENDANCE_DROP", f.group, "x", "OPEN", time.Now()); err == nil {
		t.Error("an unresolved alert cannot have resolved_at")
	}
	if err := exec(ins, uuid.New(), student, "ATTENDANCE_DROP", uuid.New(), "x", "OPEN", nil); err == nil {
		t.Error("unknown group accepted")
	}
}

func TestAnalyticsPrivacyAndLogs(t *testing.T) {
	f := newAFx(t)
	w := f.w
	var logs bytes.Buffer
	h, err := app.NewHTTPHandler(w.db, w.cfg, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	c := client{t, h}
	secret := "NOTA-PRIVADA-ANALYTICS-5521"
	f.mustFollow("tutor", w.ids["s1"], f.group, secret)
	if r := c.do("POST", ax+"/alerts/recalculate", w.token["tutor"], fmt.Sprintf(`{"group_id":%q}`, f.group)); r.status != 200 {
		t.Fatalf("recalculate: %d %s", r.status, r.body)
	}
	for _, ep := range []string{"/students/" + w.ids["s1"], "/groups/" + f.group, "/overview", "/alerts"} {
		for _, who := range []string{"tutor", "director", "tutor2", "admin", "s1"} {
			if r := c.do("GET", ax+ep, w.token[who], ""); strings.Contains(r.body, secret) {
				t.Fatalf("GET %s as %s returned a tutoring note", ep, who)
			}
		}
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatal("a tutoring note reached the logs")
	}
	if n := w.sqlInt(`SELECT count(*) FROM alerts WHERE message LIKE '%' || $1 || '%' OR evidence::text LIKE '%' || $1 || '%'`, secret); n != 0 {
		t.Fatal("a tutoring note reached an alert")
	}
	if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%' || $1 || '%'`, secret); n != 0 {
		t.Fatal("a tutoring note reached the audit trail")
	}
	// Foreign users cannot even tell whether a student exists in another group.
	if r := c.do("GET", ax+"/groups/"+f.group, w.token["tutor2"], ""); strings.Contains(r.body, "40000001") {
		t.Fatal("another group's student number leaked in an error")
	}
}
