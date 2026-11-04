package app_test

import (
	"fmt"
	"strings"
	"testing"
)

// The scenarios below follow a business journey across several modules through the public API
// and a real PostgreSQL. They chain modules (Academic -> Grades/Attendance -> Justifications ->
// Tutoring -> Analytics); per-module rules are covered in each module's own tests.

func studentAnalytics(f *gradeFx, who, student string) map[string]any {
	f.w.t.Helper()
	return f.w.must(who, "GET", ax+"/students/"+student+"?group_id="+f.group, "", 200)
}

func recalcGroup(w *world, who, group string) map[string]any {
	w.t.Helper()
	return w.must(who, "POST", ax+"/alerts/recalculate", fmt.Sprintf(`{"group_id":%q}`, group), 200)
}

// Journey A: academic context -> grade capture -> history and categories -> indicators.
func TestJourneyAcademicToGradesToIndicators(t *testing.T) {
	fx := newGradeFx(t) // career, subject, units, term, group, assignment and enrollments via the real API
	w := fx.w
	s1 := w.ids["s1"]

	// Nothing captured yet: every unit is pending (not NA, not 0.00).
	g := subjectOf(t, studentAnalytics(fx, "tutor", s1), "MAT1")
	if num(t, g, "grades", "units_total") != 7 || num(t, g, "grades", "units_pending") != 7 || num(t, g, "grades", "na") != 0 {
		t.Fatalf("before capture: %v", at(g, "grades"))
	}

	w.must("teacher", "POST", "/api/grades", fx.body(fx.se1, fx.units[0], "76.50"), 201)
	w.must("teacher", "POST", "/api/grades", fx.body(fx.se1, fx.units[1], "94"), 201)

	// The student's own history carries both scores and their derived categories.
	hist := w.do("s1", "GET", "/api/students/"+s1+"/grades", "")
	if hist.status != 200 || !strings.Contains(hist.body, `"NA"`) || !strings.Contains(hist.body, `"DE"`) {
		t.Fatalf("history: %d %s", hist.status, hist.body)
	}
	// Another student cannot read it, and ADMIN has no academic access either.
	w.expect("s2", "GET", "/api/students/"+s1+"/grades", "", 403, "forbidden")
	w.expect("admin", "GET", "/api/students/"+s1+"/grades", "", 403, "forbidden")

	// Analytics (tutor of the group) agrees with Grades, with exact categories and pending units.
	g = subjectOf(t, studentAnalytics(fx, "tutor", s1), "MAT1")
	for k, want := range map[string]float64{"units_graded": 2, "units_pending": 5, "na": 1, "de": 1, "sa": 0, "au": 0} {
		if got := num(t, g, "grades", k); got != want {
			t.Errorf("grades.%s = %v, want %v", k, got, want)
		}
	}
	// A correction (PATCH) flows to the indicators: 76.50 -> 80.00 is SA, no more NA.
	gradeID := w.sqlString(`SELECT id::text FROM grades WHERE subject_enrollment_id = $1 AND subject_unit_id = $2`, fx.se1, fx.units[0])
	w.must("teacher", "PATCH", "/api/grades/"+gradeID, `{"score":80}`, 200)
	g = subjectOf(t, studentAnalytics(fx, "tutor", s1), "MAT1")
	if num(t, g, "grades", "na") != 0 || num(t, g, "grades", "sa") != 1 {
		t.Fatalf("after correction: %v", at(g, "grades"))
	}
}

// Journey B: open session -> QR -> student check-in -> close -> records -> indicators.
func TestJourneyAttendanceQRToIndicators(t *testing.T) {
	fx := newAttendanceFx(t)
	w := fx.w
	s1, s2 := w.ids["s1"], w.ids["s2"]

	session := fx.startSession(t)
	token := fx.rotate(t, session)
	if r := fx.checkIn("s1", session, token); r.status != 201 && r.status != 200 {
		t.Fatalf("check-in: %d %s", r.status, r.body)
	}
	if got := fx.recordStatus(session, fx.se1); got != "PRESENT" {
		t.Fatalf("s1 after check-in = %q", got)
	}
	// An open session is not counted: the missing record of s2 is not an absence yet.
	if a := subjectOf(t, studentAnalytics(fx, "tutor", s2), "MAT1"); num(t, a, "attendance", "closed_session_records") != 0 {
		t.Fatalf("open session was counted: %v", at(a, "attendance"))
	}

	w.must("teacher", "POST", att+"/sessions/"+session+"/close", "", 200)
	if got := fx.recordStatus(session, fx.se2); got != "ABSENT" {
		t.Fatalf("s2 after close = %q, want ABSENT", got)
	}
	// The token died with the session.
	if r := fx.checkIn("s2", session, token); r.status < 400 {
		t.Fatalf("check-in after close succeeded: %d", r.status)
	}

	a1 := subjectOf(t, studentAnalytics(fx, "tutor", s1), "MAT1")
	a2 := subjectOf(t, studentAnalytics(fx, "tutor", s2), "MAT1")
	if num(t, a1, "attendance", "present") != 1 || num(t, a1, "attendance", "attendance_pct") != 100 {
		t.Errorf("s1: %v", at(a1, "attendance"))
	}
	if num(t, a2, "attendance", "absent") != 1 || num(t, a2, "attendance", "unjustified_absences") != 1 || num(t, a2, "attendance", "attendance_pct") != 0 {
		t.Errorf("s2: %v", at(a2, "attendance"))
	}
	// The student sees their own attendance; another student does not.
	w.must("s1", "GET", att+"/students/"+s1, "", 200)
	w.expect("s2", "GET", att+"/students/"+s1, "", 403, "forbidden")
}

// Journey C: absence -> request -> tutor -> teacher -> JUSTIFIED in Attendance -> Analytics after recalculating.
func TestJourneyJustificationReachesAnalytics(t *testing.T) {
	f := newTFx(t) // one closed session: s1 and s2 ABSENT
	w := f.w
	s1 := w.ids["s1"]
	pending := func() int { // PENDING_JUSTIFICATIONS alerts currently unresolved for s1
		return w.sqlInt(`SELECT count(*) FROM alerts WHERE student_id = $1 AND type = 'PENDING_JUSTIFICATIONS' AND status <> 'RESOLVED'`, s1)
	}

	req := f.created(t)
	recalcGroup(w, "tutor", f.group)
	if pending() != 1 {
		t.Fatal("a pending request should raise PENDING_JUSTIFICATIONS after recalculating")
	}
	// Live indicators show it without recalculating; the absence is still unjustified.
	a := subjectOf(t, studentAnalytics(f.gradeFx, "tutor", s1), "MAT1")
	if num(t, a, "attendance", "absent") != 1 || num(t, a, "justifications", "pending_tutor") != 1 {
		t.Fatalf("pending state: %v / %v", at(a, "attendance"), at(a, "justifications"))
	}

	if r := f.decide("tutor", req, "tutor", "APPROVED", ""); r.status != 200 {
		t.Fatalf("tutor: %d %s", r.status, r.body)
	}
	if r := f.decide("teacher", req, "teacher", "APPROVED", ""); r.status != 200 {
		t.Fatalf("teacher: %d %s", r.status, r.body)
	}
	if got := f.attendanceOf(f.rec1); got != "JUSTIFIED" {
		t.Fatalf("attendance record = %s, want JUSTIFIED", got)
	}

	// Stored alerts only change on demand...
	if pending() != 1 {
		t.Fatal("alerts must not change until recalculated")
	}
	recalcGroup(w, "tutor", f.group)
	if pending() != 0 {
		t.Fatal("the alert should be resolved after recalculating")
	}
	a = subjectOf(t, studentAnalytics(f.gradeFx, "tutor", s1), "MAT1")
	if num(t, a, "attendance", "justified") != 1 || num(t, a, "attendance", "absent") != 0 {
		t.Errorf("after approval: %v", at(a, "attendance"))
	}
	// No attended-or-absent record is left: there is no percentage (null), never 0%.
	if at(a, "attendance", "attendance_pct") != nil {
		t.Errorf("attendance_pct = %v, want null", at(a, "attendance", "attendance_pct"))
	}
	// The other student's absence is untouched.
	if got := f.attendanceOf(f.rec2); got != "ABSENT" {
		t.Errorf("s2 record = %s", got)
	}
}

// Journey D: tutor consults and follows up -> the group changes tutor -> access moves, in every module.
func TestJourneyTutorChangeMovesAccessAcrossModules(t *testing.T) {
	f := newTFx(t)
	w := f.w
	s1 := w.ids["s1"]

	w.must("tutor", "GET", tx+"/students/"+s1+"?group_id="+f.group, "", 200)
	first := f.mustFollow("tutor", s1, f.group, "seguimiento inicial")
	if n := w.count("tutor", tx+"/students/"+s1+"/follow-ups"); n != 1 {
		t.Fatalf("history = %d", n)
	}
	req := f.created(t) // s1 asks to justify; the current tutor would decide first

	w.must("director", "PUT", "/api/academic/groups/"+f.group+"/tutor", fmt.Sprintf(`{"tutor_id":%q}`, w.ids["tutor2"]), 200)

	// The previous tutor loses Tutoring, Analytics and Justifications access at once.
	w.expect("tutor", "GET", tx+"/follow-ups/"+first, "", 403, "forbidden")
	w.expect("tutor", "GET", ax+"/groups/"+f.group, "", 403, "forbidden")
	w.expect("tutor", "GET", ax+"/students/"+s1+"?group_id="+f.group, "", 403, "forbidden")
	w.expect("tutor", "POST", ax+"/alerts/recalculate", fmt.Sprintf(`{"group_id":%q}`, f.group), 403, "forbidden")
	if r := f.decide("tutor", req, "tutor", "APPROVED", ""); r.status != 403 {
		t.Errorf("old tutor decided a request: %d %s", r.status, r.body)
	}

	// The new tutor inherits history, indicators and the pending decision.
	if n := w.count("tutor2", tx+"/students/"+s1+"/follow-ups"); n != 1 {
		t.Fatalf("inherited history = %d", n)
	}
	w.must("tutor2", "GET", ax+"/groups/"+f.group, "", 200)
	recalcGroup(w, "tutor2", f.group)
	if r := f.decide("tutor2", req, "tutor", "APPROVED", ""); r.status != 200 {
		t.Fatalf("new tutor decision: %d %s", r.status, r.body)
	}
}
