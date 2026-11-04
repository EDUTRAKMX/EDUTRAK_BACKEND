package app_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edutrak/backend/internal/app"
	iddomain "github.com/edutrak/backend/internal/identity/domain"
	tutoringpg "github.com/edutrak/backend/internal/tutoring/adapters/postgres"
	tutoringapp "github.com/edutrak/backend/internal/tutoring/application"
	tports "github.com/edutrak/backend/internal/tutoring/ports"
	"github.com/google/uuid"
)

const tx = "/api/tutoring"

// tfx is the justification fixture (group 2A tutored by "tutor" with s1 and s2, absences
// recorded) plus a second group 2B tutored by "tutor2" with s3 and a second period.
type tfx struct {
	*jfx
	career, group2B string
}

func newTFx(t *testing.T) *tfx {
	t.Helper()
	j := newJFx(t)
	w := j.w
	f := &tfx{jfx: j}
	f.career = w.sqlString(`SELECT career_id::text FROM groups WHERE id = $1`, j.group)
	f.group2B = idOf(w.must("director", "POST", "/api/academic/groups",
		fmt.Sprintf(`{"academic_term_id":%q,"career_id":%q,"code":"2B","level":2,"tutor_id":%q}`, j.term, f.career, w.ids["tutor2"]), 201))
	w.must("director", "POST", "/api/academic/groups/"+f.group2B+"/enrollments", fmt.Sprintf(`{"student_id":%q}`, w.ids["s3"]), 201)
	return f
}

func (f *tfx) follow(who, student, group, note string) resp {
	return f.w.do(who, "POST", tx+"/follow-ups", fmt.Sprintf(`{"student_id":%q,"group_id":%q,"note":%q}`, student, group, note))
}

func (f *tfx) mustFollow(who, student, group, note string) string {
	f.w.t.Helper()
	r := f.follow(who, student, group, note)
	if r.status != 201 {
		f.w.t.Fatalf("follow-up as %s: %d %s", who, r.status, r.body)
	}
	return idOf(r.json(f.w.t))
}

func TestTutoringAuthenticationRequired(t *testing.T) {
	w := newWorld(t)
	id := uuid.NewString()
	for _, ep := range [][2]string{
		{"GET", tx + "/students"}, {"GET", tx + "/students/" + id}, {"GET", tx + "/students/" + id + "/follow-ups"},
		{"POST", tx + "/follow-ups"}, {"GET", tx + "/follow-ups"}, {"GET", tx + "/follow-ups/" + id},
	} {
		if r := w.c.do(ep[0], ep[1], "", `{}`); r.status != 401 {
			t.Errorf("%s %s without token: %d", ep[0], ep[1], r.status)
		}
	}
}

func TestTutoringCreateValidationAndAuthorization(t *testing.T) {
	f := newTFx(t)
	w := f.w
	s1, s3 := w.ids["s1"], w.ids["s3"]

	r := f.follow("tutor", s1, f.group, "  Revisamos asistencia  ")
	if r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	m := r.json(t)
	if m["note"] != "Revisamos asistencia" || m["author"].(map[string]any)["id"] != w.ids["tutor"] ||
		m["student"].(map[string]any)["id"] != s1 || m["group"].(map[string]any)["code"] != "2A" || m["academic_term"].(map[string]any)["name"] != "2026-1" {
		t.Fatalf("response: %v", m)
	}
	if d, _ := m["followup_date"].(string); len(d) != 10 {
		t.Fatalf("date: %v", m["followup_date"])
	}

	// The author is the token's owner; any attempt to name one is a malformed body.
	body := fmt.Sprintf(`{"student_id":%q,"group_id":%q,"note":"n","tutor_id":%q}`, s1, f.group, w.ids["tutor2"])
	w.expect("tutor", "POST", tx+"/follow-ups", body, 400, "invalid_body")
	w.expect("tutor", "POST", tx+"/follow-ups", `{"student_id":`, 400, "invalid_body")

	good := func(extra string) string {
		return fmt.Sprintf(`{"student_id":%q,"group_id":%q,%s}`, s1, f.group, extra)
	}
	for _, c := range []struct {
		name, body, code string
		status           int
	}{
		{"blank note", good(`"note":"   "`), "note_required", 422},
		{"missing note", good(`"followup_date":"2026-02-01"`), "note_required", 422},
		{"note too long", good(`"note":"` + strings.Repeat("x", 4001) + `"`), "note_too_long", 422},
		{"bad date", good(`"note":"n","followup_date":"31/01/2026"`), "invalid_followup_date", 422},
		{"impossible date", good(`"note":"n","followup_date":"2026-02-30"`), "invalid_followup_date", 422},
		{"future date", good(`"note":"n","followup_date":"` + time.Now().AddDate(0, 0, 2).Format("2006-01-02") + `"`), "followup_date_in_future", 422},
		{"before the term", good(`"note":"n","followup_date":"2026-01-04"`), "followup_date_before_term", 422},
		{"bad student id", fmt.Sprintf(`{"student_id":"x","group_id":%q,"note":"n"}`, f.group), "invalid_student_id", 422},
		{"bad group id", fmt.Sprintf(`{"student_id":%q,"group_id":"x","note":"n"}`, s1), "invalid_group_id", 422},
		{"unknown student", fmt.Sprintf(`{"student_id":%q,"group_id":%q,"note":"n"}`, uuid.NewString(), f.group), "student_not_found", 422},
		{"unknown group", fmt.Sprintf(`{"student_id":%q,"group_id":%q,"note":"n"}`, s1, uuid.NewString()), "group_not_found", 422},
		{"student not enrolled in that group", fmt.Sprintf(`{"student_id":%q,"group_id":%q,"note":"n"}`, s3, f.group), "student_not_enrolled", 422},
		{"student of another group, naming that group", fmt.Sprintf(`{"student_id":%q,"group_id":%q,"note":"n"}`, s3, f.group2B), "forbidden", 403},
	} {
		w.expect("tutor", "POST", tx+"/follow-ups", c.body, c.status, c.code)
	}
	w.expect("tutor", "POST", tx+"/follow-ups", good(`"note":"n","followup_date":"2026-02-01"`), 201, "")

	// Everyone else is refused, even with the right ids.
	ok := good(`"note":"n"`)
	for _, who := range []string{"tutor2", "director", "teacher", "teacher2", "s1", "s2", "admin"} {
		w.expect(who, "POST", tx+"/follow-ups", ok, 403, "forbidden")
	}
	if n := w.sqlInt(`SELECT count(*) FROM tutoring_followups`); n != 2 {
		t.Fatalf("rows = %d, want 2 (rejected requests must not store anything)", n)
	}
	if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'tutoring.followup_created' AND actor_user_id = $1`, w.ids["tutor"]); n != 2 {
		t.Fatalf("audit events = %d", n)
	}
	if n := w.sqlInt(`SELECT count(*) FROM tutoring_followups WHERE tutor_id = $1 AND group_id = $2`, w.ids["tutor"], f.group); n != 2 {
		t.Fatalf("author/group stored wrongly: %d", n)
	}
}

func TestTutoringInactiveTutorAndClosedTerm(t *testing.T) {
	f := newTFx(t)
	w := f.w
	// A withdrawn student cannot receive new follow-ups; its history stays readable.
	old := f.mustFollow("tutor", w.ids["s2"], f.group, "antes de la baja")
	mustExecSQL(t, w.db, fmt.Sprintf(`UPDATE enrollments SET status = 'WITHDRAWN' WHERE student_id = '%s' AND group_id = '%s'`, w.ids["s2"], f.group))
	w.expect("tutor", "POST", tx+"/follow-ups", fmt.Sprintf(`{"student_id":%q,"group_id":%q,"note":"n"}`, w.ids["s2"], f.group), 409, "enrollment_inactive")
	w.must("tutor", "GET", tx+"/follow-ups/"+old, "", 200)

	// A closed term freezes tutoring writes (consistent with the rest of the system).
	w.must("director", "POST", "/api/academic/terms/"+f.term+"/close", "", 200)
	w.expect("tutor", "POST", tx+"/follow-ups", fmt.Sprintf(`{"student_id":%q,"group_id":%q,"note":"n"}`, w.ids["s1"], f.group), 409, "term_closed")
	w.must("tutor", "GET", tx+"/follow-ups/"+old, "", 200)

	// A deactivated tutor account is stopped by authentication itself.
	mustExecSQL(t, w.db, fmt.Sprintf(`UPDATE users SET is_active = FALSE WHERE id = '%s'`, w.ids["tutor"]))
	w.expect("tutor", "GET", tx+"/students", "", 401, "")
}

func TestTutoringReadsAndHistory(t *testing.T) {
	f := newTFx(t)
	w := f.w
	s1, s2, s3 := w.ids["s1"], w.ids["s2"], w.ids["s3"]
	a := f.mustFollow("tutor", s1, f.group, "primera")
	b := f.mustFollow("tutor", s1, f.group, "segunda")
	c := f.mustFollow("tutor", s2, f.group, "otro alumno")
	d := f.mustFollow("tutor2", s3, f.group2B, "alumno de 2B")
	_ = c

	// History of one student: only that student, newest first, with the author.
	h := w.must("tutor", "GET", tx+"/students/"+s1+"/follow-ups", "", 200)
	data := h["data"].([]any)
	if len(data) != 2 || h["total"].(float64) != 2 || idOf(data[0].(map[string]any)) != b || idOf(data[1].(map[string]any)) != a {
		t.Fatalf("history: %v", h)
	}
	// Pagination.
	p := w.must("tutor", "GET", tx+"/students/"+s1+"/follow-ups?limit=1&offset=1", "", 200)
	if len(p["data"].([]any)) != 1 || idOf(p["data"].([]any)[0].(map[string]any)) != a || p["total"].(float64) != 2 || p["limit"].(float64) != 1 {
		t.Fatalf("page: %v", p)
	}
	for _, q := range []string{"limit=0", "limit=201", "limit=x", "offset=-1"} {
		w.expect("tutor", "GET", tx+"/students/"+s1+"/follow-ups?"+q, "", 422, "invalid_paging")
	}
	w.expect("tutor", "GET", tx+"/follow-ups?student_id=zz", "", 422, "invalid_filter")
	w.expect("tutor", "GET", tx+"/follow-ups?from=2026-99-01", "", 422, "invalid_filter")

	// Scope: a tutor lists only the follow-ups of his groups, a director all of them.
	if n := w.count("tutor", tx+"/follow-ups"); n != 3 {
		t.Fatalf("tutor listing = %d", n)
	}
	if n := w.count("tutor2", tx+"/follow-ups"); n != 1 {
		t.Fatalf("tutor2 listing = %d", n)
	}
	if n := w.count("director", tx+"/follow-ups"); n != 4 {
		t.Fatalf("director listing = %d", n)
	}
	if n := w.count("director", tx+"/follow-ups?student_id="+s3); n != 1 {
		t.Fatalf("filter by student = %d", n)
	}
	if n := w.count("tutor", tx+"/follow-ups?group_id="+f.group2B); n != 0 {
		t.Fatalf("a foreign group filter leaked %d rows", n)
	}
	if n := w.count("director", tx+"/follow-ups?academic_term_id="+f.term+"&from=2020-01-01&to=2099-01-01"); n != 4 {
		t.Fatalf("term/date filter = %d", n)
	}
	if n := w.count("director", tx+"/follow-ups?from=2099-01-01"); n != 0 {
		t.Fatalf("from filter = %d", n)
	}

	// Detail and IDs in the URL: nobody reads a foreign file by changing the id.
	w.must("tutor", "GET", tx+"/follow-ups/"+a, "", 200)
	w.must("director", "GET", tx+"/follow-ups/"+d, "", 200)
	w.expect("tutor", "GET", tx+"/follow-ups/"+d, "", 403, "forbidden")
	w.expect("tutor2", "GET", tx+"/follow-ups/"+a, "", 403, "forbidden")
	w.expect("tutor2", "GET", tx+"/students/"+s1+"/follow-ups", "", 403, "forbidden")
	w.expect("tutor", "GET", tx+"/students/"+s3+"/follow-ups", "", 403, "forbidden")
	w.expect("tutor", "GET", tx+"/students/"+s1+"/follow-ups?group_id="+f.group2B, "", 404, "enrollment_not_found")
	w.expect("tutor", "GET", tx+"/students/"+uuid.NewString()+"/follow-ups", "", 404, "student_not_found")
	w.expect("tutor", "GET", tx+"/students/not-a-uuid/follow-ups", "", 404, "student_not_found")
	w.expect("tutor", "GET", tx+"/follow-ups/"+uuid.NewString(), "", 404, "followup_not_found")
	w.expect("tutor", "GET", tx+"/follow-ups/not-a-uuid", "", 404, "followup_not_found")

	// No other role gets any of it, whatever it can see in Attendance or Grades.
	for _, who := range []string{"admin", "teacher", "teacher2", "s1", "s2"} {
		for _, path := range []string{"/students", "/students/" + s1, "/students/" + s1 + "/follow-ups", "/follow-ups", "/follow-ups/" + a} {
			w.expect(who, "GET", tx+path, "", 403, "forbidden")
		}
	}
	// Notes are not leaked into the Academic views either.
	if r := w.do("director", "GET", "/api/academic/groups/"+f.group, ""); strings.Contains(r.body, "primera") {
		t.Fatal("a tutoring note appeared in an Academic response")
	}
}

func TestTutoringHistorySurvivesTutorChange(t *testing.T) {
	f := newTFx(t)
	w := f.w
	s1 := w.ids["s1"]
	first := f.mustFollow("tutor", s1, f.group, "del primer tutor")

	// Reassign the group to tutor2.
	w.must("director", "PUT", "/api/academic/groups/"+f.group+"/tutor", fmt.Sprintf(`{"tutor_id":%q}`, w.ids["tutor2"]), 200)

	// The previous tutor loses access at once, on every route and with the old URLs.
	w.expect("tutor", "GET", tx+"/follow-ups/"+first, "", 403, "forbidden")
	w.expect("tutor", "GET", tx+"/students/"+s1+"/follow-ups", "", 403, "forbidden")
	w.expect("tutor", "GET", tx+"/students/"+s1, "", 403, "forbidden")
	w.expect("tutor", "POST", tx+"/follow-ups", fmt.Sprintf(`{"student_id":%q,"group_id":%q,"note":"n"}`, s1, f.group), 403, "forbidden")
	if n := w.count("tutor", tx+"/follow-ups"); n != 0 {
		t.Fatalf("old tutor still lists %d", n)
	}
	if n := w.count("tutor", tx+"/students"); n != 0 {
		t.Fatalf("old tutor still lists %d students", n)
	}

	// The new tutor inherits the history, with the ORIGINAL author, and can add to it.
	h := w.must("tutor2", "GET", tx+"/students/"+s1+"/follow-ups", "", 200)["data"].([]any)
	if len(h) != 1 || h[0].(map[string]any)["author"].(map[string]any)["id"] != w.ids["tutor"] {
		t.Fatalf("inherited history: %v", h)
	}
	second := f.mustFollow("tutor2", s1, f.group, "del nuevo tutor")
	if got := w.sqlString(`SELECT tutor_id::text FROM tutoring_followups WHERE id = $1`, second); got != w.ids["tutor2"] {
		t.Fatalf("author of the new follow-up = %s", got)
	}
	if got := w.sqlString(`SELECT tutor_id::text FROM tutoring_followups WHERE id = $1`, first); got != w.ids["tutor"] {
		t.Fatalf("author of the old follow-up changed to %s", got)
	}
	if n := w.count("tutor2", tx+"/students/"+s1+"/follow-ups"); n != 2 {
		t.Fatalf("history length = %d", n)
	}
}

func TestTutoringStudentsAndMultiplePeriods(t *testing.T) {
	f := newTFx(t)
	w := f.w
	s1, s3 := w.ids["s1"], w.ids["s3"]
	f.mustFollow("tutor", s1, f.group, "periodo 1")

	rows := w.must("tutor", "GET", tx+"/students", "", 200)["data"].([]any)
	if len(rows) != 2 {
		t.Fatalf("tutor students = %d", len(rows))
	}
	first := rows[0].(map[string]any)
	st := first["student"].(map[string]any)
	if len(st) != 4 || st["institutional_number"] == nil || first["group"].(map[string]any)["code"] != "2A" ||
		first["career"].(map[string]any)["code"] == nil || first["academic_term"].(map[string]any)["name"] != "2026-1" {
		t.Fatalf("row shape: %v", first)
	}
	for _, r := range rows {
		m := r.(map[string]any)
		if m["student"].(map[string]any)["id"] == s1 && (m["followups_count"].(float64) != 1 || m["last_followup_date"] == nil) {
			t.Fatalf("counters: %v", m)
		}
		if m["student"].(map[string]any)["id"] == s3 {
			t.Fatal("a student of another group leaked into the list")
		}
	}
	if n := w.count("tutor2", tx+"/students"); n != 1 {
		t.Fatalf("tutor2 students = %d", n)
	}
	if n := w.count("director", tx+"/students"); n != 3 {
		t.Fatalf("director students = %d", n)
	}
	if n := w.count("director", tx+"/students?group_id="+f.group2B); n != 1 {
		t.Fatalf("group filter = %d", n)
	}
	w.expect("tutor", "GET", tx+"/students?status=bogus", "", 422, "invalid_filter")
	w.expect("tutor", "GET", tx+"/students?group_id=zz", "", 422, "invalid_filter")
	if n := w.count("tutor", tx+"/students?status=WITHDRAWN"); n != 0 {
		t.Fatalf("withdrawn = %d", n)
	}

	// A second period: the same student enrolled in another group of the same tutor.
	term2 := idOf(w.must("director", "POST", "/api/academic/terms", `{"name":"2026-2","start_date":"2026-05-05","end_date":"2026-08-30"}`, 201))
	w.must("director", "POST", "/api/academic/terms/"+term2+"/activate", "", 200)
	g2 := idOf(w.must("director", "POST", "/api/academic/groups",
		fmt.Sprintf(`{"academic_term_id":%q,"career_id":%q,"code":"3A","level":3,"tutor_id":%q}`, term2, f.career, w.ids["tutor"]), 201))
	w.must("director", "POST", "/api/academic/groups/"+g2+"/enrollments", fmt.Sprintf(`{"student_id":%q}`, s1), 201)
	f.mustFollow("tutor", s1, g2, "periodo 2")

	if n := w.count("tutor", tx+"/students"); n != 3 {
		t.Fatalf("students over both periods = %d", n)
	}
	if n := w.count("tutor", tx+"/students?academic_term_id="+term2); n != 1 {
		t.Fatalf("term filter = %d", n)
	}
	// History is never mixed: each group has its own, and the default is the most recent period.
	if n := w.count("tutor", tx+"/students/"+s1+"/follow-ups?group_id="+f.group); n != 1 {
		t.Fatalf("history of period 1 = %d", n)
	}
	if n := w.count("tutor", tx+"/students/"+s1+"/follow-ups?group_id="+g2); n != 1 {
		t.Fatalf("history of period 2 = %d", n)
	}
	if n := w.count("tutor", tx+"/follow-ups?student_id="+s1+"&academic_term_id="+term2); n != 1 {
		t.Fatalf("term-filtered history = %d", n)
	}
	// A date before the second term's start is refused for that group.
	w.expect("tutor", "POST", tx+"/follow-ups",
		fmt.Sprintf(`{"student_id":%q,"group_id":%q,"note":"n","followup_date":"2026-04-01"}`, s1, g2), 422, "followup_date_before_term")
	ov := w.must("tutor", "GET", tx+"/students/"+s1, "", 200)["enrollment"].(map[string]any)
	if ov["group"].(map[string]any)["code"] != "3A" || ov["academic_term"].(map[string]any)["name"] != "2026-2" {
		t.Fatalf("default overview context: %v", ov)
	}
	ov = w.must("tutor", "GET", tx+"/students/"+s1+"?group_id="+f.group, "", 200)["enrollment"].(map[string]any)
	if ov["group"].(map[string]any)["code"] != "2A" {
		t.Fatalf("explicit overview context: %v", ov)
	}
}

func TestTutoringOverviewReusesAcademicGradesAttendanceAndJustifications(t *testing.T) {
	f := newTFx(t)
	w := f.w
	s1 := w.ids["s1"]

	// Grades: unit 1 = 90.00, unit 2 = 0.00 (a real zero); the other five are pending.
	w.must("teacher", "POST", "/api/grades", f.body(f.se1, f.units[0], "90"), 201)
	w.must("teacher", "POST", "/api/grades", f.body(f.se1, f.units[1], "0"), 201)
	// Justifications: one pending request for the absence recorded in the fixture.
	f.created(t)

	ov := w.must("tutor", "GET", tx+"/students/"+s1+"?group_id="+f.group, "", 200)
	subjects := ov["subjects"].([]any)
	if len(subjects) != 2 {
		t.Fatalf("subjects = %d", len(subjects))
	}
	byCode := map[string]map[string]any{}
	for _, s := range subjects {
		m := s.(map[string]any)
		byCode[m["subject"].(map[string]any)["code"].(string)] = m
	}
	mat, fis := byCode["MAT1"], byCode["FIS1"]
	if mat == nil || fis == nil {
		t.Fatalf("subjects: %v", subjects)
	}
	g := mat["grades"].(map[string]any)
	units := g["units"].([]any)
	if g["units_total"].(float64) != 7 || g["units_graded"].(float64) != 2 || len(units) != 7 {
		t.Fatalf("grades summary: %v", g)
	}
	u1 := units[0].(map[string]any)["grade"].(map[string]any)
	u2 := units[1].(map[string]any)["grade"].(map[string]any)
	if u1["score"].(float64) != 90 || u1["category"] != "DE" || u2["score"].(float64) != 0 || u2["category"] != "NA" {
		t.Fatalf("captured grades: %v %v", u1, u2)
	}
	for _, u := range units[2:] {
		if u.(map[string]any)["grade"] != nil {
			t.Fatalf("a pending unit must be null, not 0.00: %v", u)
		}
	}
	if !strings.Contains(w.do("tutor", "GET", tx+"/students/"+s1+"?group_id="+f.group, "").body, `"score":90.00`) {
		t.Fatal("scores must keep two decimals")
	}
	att := mat["attendance"].(map[string]any)
	if att["absent"].(float64) != 1 || att["total"].(float64) != 1 || att["present"].(float64) != 0 || att["effective_absences"].(float64) != 1 || att["lates_per_absence"].(float64) != 3 {
		t.Fatalf("attendance: %v", att)
	}
	if j := mat["justifications"].(map[string]any); j["pending"].(float64) != 1 || j["approved"].(float64) != 0 || j["rejected"].(float64) != 0 {
		t.Fatalf("justifications: %v", j)
	}
	if fa := fis["attendance"].(map[string]any); fa["total"].(float64) != 0 || fa["lates_per_absence"] != nil || fis["grades"].(map[string]any)["units_graded"].(float64) != 0 {
		t.Fatalf("FIS1: %v", fis)
	}

	// Approve it through the real flow: the approved counter moves, no copies are kept in Tutoring.
	id := w.sqlString(`SELECT id::text FROM justification_requests LIMIT 1`)
	w.must("tutor", "POST", jx+"/"+id+"/tutor-decision", `{"decision":"APPROVED"}`, 200)
	w.must("teacher", "POST", jx+"/"+id+"/teacher-decision", `{"decision":"APPROVED"}`, 200)
	ov = w.must("tutor", "GET", tx+"/students/"+s1+"?group_id="+f.group, "", 200)
	for _, s := range ov["subjects"].([]any) {
		m := s.(map[string]any)
		if m["subject"].(map[string]any)["code"] == "MAT1" {
			a, j := m["attendance"].(map[string]any), m["justifications"].(map[string]any)
			if a["justified"].(float64) != 1 || a["absent"].(float64) != 0 || j["approved"].(float64) != 1 || j["pending"].(float64) != 0 {
				t.Fatalf("after approval: %v %v", a, j)
			}
		}
	}
	// A student only gets his own situation: the other student's data never shows up.
	body := w.do("tutor", "GET", tx+"/students/"+s1+"?group_id="+f.group, "").body
	if strings.Contains(body, w.ids["s2"]) || strings.Contains(body, "40000002") {
		t.Fatal("another student's data leaked into the overview")
	}
	// The director may look at the same overview; a foreign tutor may not.
	w.must("director", "GET", tx+"/students/"+s1, "", 200)
	w.expect("tutor2", "GET", tx+"/students/"+s1+"?group_id="+f.group, "", 403, "forbidden")
	w.expect("tutor2", "GET", tx+"/students/"+s1, "", 403, "forbidden")
}

func TestTutoringUsersWithSeveralRolesNeedTheRelationship(t *testing.T) {
	f := newTFx(t)
	w := f.w
	svc := app.NewTutoringService(w.db, w.cfg)
	ctx := context.Background()
	s1 := uuid.MustParse(w.ids["s1"])
	group := uuid.MustParse(f.group)

	// A teacher who is also the tutor of the group in the database (set directly).
	teacher := uuid.MustParse(w.ids["teacher"])
	mustExecSQL(t, w.db, fmt.Sprintf(`UPDATE groups SET tutor_id = '%s' WHERE id = '%s'`, teacher, group))

	teacherOnly := iddomain.Principal{UserID: teacher, SessionID: uuid.New(), Roles: []iddomain.Role{iddomain.RoleTeacher}}
	both := iddomain.Principal{UserID: teacher, SessionID: uuid.New(), Roles: []iddomain.Role{iddomain.RoleTeacher, iddomain.RoleTutor}}
	in := tutoringapp.CreateInput{StudentID: s1, GroupID: group, Note: "n"}
	if _, err := svc.CreateFollowUp(ctx, teacherOnly, in); err == nil {
		t.Fatal("TEACHER without the TUTOR role must not write, even if groups.tutor_id points to them")
	}
	if _, err := svc.CreateFollowUp(ctx, both, in); err != nil {
		t.Fatalf("TEACHER+TUTOR with the relationship must write: %v", err)
	}
	// The same user with both roles gets nothing for a group he does NOT tutor.
	other := uuid.MustParse(f.group2B)
	if _, err := svc.CreateFollowUp(ctx, both, tutoringapp.CreateInput{StudentID: uuid.MustParse(w.ids["s3"]), GroupID: other, Note: "n"}); err == nil {
		t.Fatal("the TUTOR role alone must not open a group the user does not tutor")
	}
	if _, _, err := svc.StudentHistory(ctx, teacherOnly, s1, nil, tports.Page{}); err == nil {
		t.Fatal("a TEACHER has no access to the tutoring history")
	}
}

func TestTutoringWritesRollBackWithTheirAuditRow(t *testing.T) {
	f := newTFx(t)
	w := f.w
	svc := tutoringapp.NewService(tutoringapp.Deps{
		Repo: tutoringpg.NewRepository(w.db), Overviews: tutoringpg.NewOverviewReader(w.db), Audit: failingAudit{}, Tx: w.db,
		Clock: clockFunc(time.Now), NewID: uuid.New,
	})
	tutor := iddomain.Principal{UserID: uuid.MustParse(w.ids["tutor"]), SessionID: uuid.New(), Roles: []iddomain.Role{iddomain.RoleTutor}}
	_, err := svc.CreateFollowUp(context.Background(), tutor, tutoringapp.CreateInput{
		StudentID: uuid.MustParse(w.ids["s1"]), GroupID: uuid.MustParse(f.group), Note: "no debe quedar"})
	if err == nil {
		t.Fatal("create must fail when its audit row cannot be written")
	}
	if n := w.sqlInt(`SELECT count(*) FROM tutoring_followups`); n != 0 {
		t.Fatalf("follow-up survived the rollback (%d rows)", n)
	}
}

func TestTutoringPrivacyNotesNeverReachLogsOrAudit(t *testing.T) {
	f := newTFx(t)
	w := f.w
	var logs bytes.Buffer
	h, err := app.NewHTTPHandler(w.db, w.cfg, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	c := client{t, h}
	secret := "OBSERVACION-RESERVADA-9137"
	body := fmt.Sprintf(`{"student_id":%q,"group_id":%q,"note":%q}`, w.ids["s1"], f.group, secret)
	if r := c.do("POST", tx+"/follow-ups", w.token["tutor"], body); r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	// The same text is rejected, refused and read back by outsiders: still nowhere in the logs.
	c.do("POST", tx+"/follow-ups", w.token["tutor2"], body)
	c.do("POST", tx+"/follow-ups", w.token["admin"], body)
	c.do("POST", tx+"/follow-ups", w.token["tutor"], strings.Replace(body, w.ids["s1"], uuid.NewString(), 1))
	c.do("GET", tx+"/students/"+w.ids["s1"]+"/follow-ups", w.token["tutor2"], "")
	c.do("GET", tx+"/students/"+w.ids["s1"]+"/follow-ups", w.token["tutor"], "")
	if strings.Contains(logs.String(), secret) {
		t.Fatal("a follow-up note appeared in the logs")
	}
	if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%' || $1 || '%'`, secret); n != 0 {
		t.Fatal("a follow-up note appeared in the audit trail")
	}
	// An error response never echoes the note either.
	if r := c.do("POST", tx+"/follow-ups", w.token["tutor2"], body); strings.Contains(r.body, secret) {
		t.Fatal("an error response echoed the note")
	}
}

func TestTutoringDatabaseConstraints(t *testing.T) {
	f := newTFx(t)
	w := f.w
	id := f.mustFollow("tutor", w.ids["s1"], f.group, "intocable")
	ctx := context.Background()
	exec := func(q string, args ...any) error { _, err := w.db.Q(ctx).Exec(ctx, q, args...); return err }

	ins := `INSERT INTO tutoring_followups (id, student_id, tutor_id, group_id, followup_date, note) VALUES ($1, $2, $3, $4, '2026-02-03', $5)`
	if err := exec(ins, uuid.New(), w.ids["s3"], w.ids["tutor"], f.group, "x"); err == nil {
		t.Error("student not enrolled in the group must violate the composite foreign key")
	}
	if err := exec(ins, uuid.New(), w.ids["s1"], uuid.New(), f.group, "x"); err == nil {
		t.Error("unknown tutor must violate the foreign key")
	}
	if err := exec(ins, uuid.New(), w.ids["s1"], w.ids["tutor"], f.group, " \n\t"); err == nil {
		t.Error("blank note must violate the check")
	}
	if err := exec(ins, uuid.New(), w.ids["s1"], w.ids["tutor"], f.group, strings.Repeat("x", 4001)); err == nil {
		t.Error("overlong note must violate the check")
	}
	if err := exec(`UPDATE tutoring_followups SET note = 'reescrito' WHERE id = $1`, id); err == nil {
		t.Error("history is append-only: UPDATE must fail")
	}
	if err := exec(`DELETE FROM tutoring_followups WHERE id = $1`, id); err == nil {
		t.Error("history is append-only: DELETE must fail")
	}
	if got := w.sqlString(`SELECT note FROM tutoring_followups WHERE id = $1`, id); got != "intocable" {
		t.Errorf("note = %q", got)
	}
}

func TestTutoringConcurrency(t *testing.T) {
	f := newTFx(t)
	w := f.w
	s1 := w.ids["s1"]

	// Independent interventions are never serialized into conflicts: all of them are stored.
	const n = 12
	var wg sync.WaitGroup
	statuses := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = f.follow("tutor", s1, f.group, fmt.Sprintf("simultaneo %d", i)).status
		}(i)
	}
	wg.Wait()
	for i, s := range statuses {
		if s != 201 {
			t.Fatalf("concurrent create %d -> %d", i, s)
		}
	}
	if rows, events := w.sqlInt(`SELECT count(*) FROM tutoring_followups`), w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'tutoring.followup_created'`); rows != n || events != n {
		t.Fatalf("rows = %d, audit events = %d, want %d", rows, events, n)
	}

	// Creating while the group changes tutor: each create either completes before the change
	// (stored, audited, authored by the old tutor) or is refused afterwards; never half-done.
	var wg2 sync.WaitGroup
	results := make([]int, 20)
	for i := range results {
		wg2.Add(1)
		go func(i int) {
			defer wg2.Done()
			if i == 10 {
				results[i] = w.do("director", "PUT", "/api/academic/groups/"+f.group+"/tutor", fmt.Sprintf(`{"tutor_id":%q}`, w.ids["tutor2"])).status
				return
			}
			results[i] = f.follow("tutor", s1, f.group, fmt.Sprintf("carrera %d", i)).status
		}(i)
	}
	wg2.Wait()
	if results[10] != 200 {
		t.Fatalf("tutor change -> %d", results[10])
	}
	created := 0
	for i, s := range results {
		if i == 10 {
			continue
		}
		switch s {
		case 201:
			created++
		case 403:
		default:
			t.Fatalf("create during tutor change -> %d", s)
		}
	}
	if rows := w.sqlInt(`SELECT count(*) FROM tutoring_followups WHERE note LIKE 'carrera %'`); rows != created {
		t.Fatalf("rows = %d, successful creates = %d", rows, created)
	}
	if ev := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'tutoring.followup_created'`); ev != n+created {
		t.Fatalf("audit events = %d, want %d", ev, n+created)
	}
	// Afterwards the old tutor is out for good.
	w.expect("tutor", "POST", tx+"/follow-ups", fmt.Sprintf(`{"student_id":%q,"group_id":%q,"note":"n"}`, s1, f.group), 403, "forbidden")

	// Creating while the term is closed: stored before the close or 409, nothing in between.
	var wg3 sync.WaitGroup
	closeStatus := 0
	res := make([]int, 10)
	for i := range res {
		wg3.Add(1)
		go func(i int) {
			defer wg3.Done()
			res[i] = f.follow("tutor2", s1, f.group, fmt.Sprintf("cierre %d", i)).status
		}(i)
	}
	wg3.Add(1)
	go func() {
		defer wg3.Done()
		closeStatus = w.do("director", "POST", "/api/academic/terms/"+f.term+"/close", "").status
	}()
	wg3.Wait()
	if closeStatus != 200 {
		t.Fatalf("close -> %d", closeStatus)
	}
	ok := 0
	for _, s := range res {
		switch s {
		case 201:
			ok++
		case 409:
		default:
			t.Fatalf("create during close -> %d", s)
		}
	}
	if rows := w.sqlInt(`SELECT count(*) FROM tutoring_followups WHERE note LIKE 'cierre %'`); rows != ok {
		t.Fatalf("rows = %d, successful creates = %d", rows, ok)
	}
}
