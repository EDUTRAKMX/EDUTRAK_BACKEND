package app_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	gradespg "github.com/edutrak/backend/internal/grades/adapters/postgres"
	gradesapp "github.com/edutrak/backend/internal/grades/application"
	"github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/config"
	"github.com/google/uuid"
)

// gradeFx is a ready class: group 2A (tutor), subject MAT1 with 7 units taught by
// "teacher", students s1 and s2 subject-enrolled; plus a second subject FIS1 taught
// by "teacher2" in the same group, with s1 enrolled.
type gradeFx struct {
	w              *world
	term, group    string
	assignment     string // MAT1, teacher
	assignment2    string // FIS1, teacher2
	units          []string
	units2         []string
	se1, se2, se1b string // subject enrollment ids: s1/MAT1, s2/MAT1, s1/FIS1
	foreignUnit    string // a unit of FIS1 (belongs to another subject than assignment)
}

func newGradeFx(t *testing.T) *gradeFx { return newGradeFxWith(t, nil) }

func newGradeFxWith(t *testing.T, mutate func(*config.Config)) *gradeFx {
	t.Helper()
	w := newWorldWith(t, mutate)
	career, subject, term := w.seedPlan() // MAT1 has units 1 and 2, level 2
	for n := 3; n <= 7; n++ {
		w.must("director", "POST", "/api/academic/subjects/"+subject+"/units", fmt.Sprintf(`{"unit_number":%d,"name":"Unidad %d"}`, n, n), 201)
	}
	subject2 := idOf(w.must("director", "POST", "/api/academic/subjects", `{"code":"FIS1","name":"Fisica I"}`, 201))
	w.must("director", "POST", "/api/academic/subjects/"+subject2+"/units", `{"unit_number":1,"name":"Cinematica"}`, 201)
	w.must("director", "POST", "/api/academic/subjects/"+subject2+"/units", `{"unit_number":2,"name":"Dinamica"}`, 201)
	w.must("director", "POST", "/api/academic/careers/"+career+"/curriculum", fmt.Sprintf(`{"subject_id":%q,"level":2}`, subject2), 201)

	fx := &gradeFx{w: w, term: term}
	fx.group = idOf(w.must("director", "POST", "/api/academic/groups",
		fmt.Sprintf(`{"academic_term_id":%q,"career_id":%q,"code":"2A","level":2,"tutor_id":%q}`, term, career, w.ids["tutor"]), 201))
	for _, s := range []string{"s1", "s2"} {
		w.must("director", "POST", "/api/academic/groups/"+fx.group+"/enrollments", fmt.Sprintf(`{"student_id":%q}`, w.ids[s]), 201)
	}
	fx.assignment = idOf(w.must("director", "POST", "/api/academic/groups/"+fx.group+"/teaching-assignments",
		fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, subject, w.ids["teacher"]), 201))
	fx.assignment2 = idOf(w.must("director", "POST", "/api/academic/groups/"+fx.group+"/teaching-assignments",
		fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, subject2, w.ids["teacher2"]), 201))
	w.must("director", "POST", "/api/academic/teaching-assignments/"+fx.assignment+"/subject-enrollments", "", 200)
	// FIS1: only s1 takes it
	w.must("director", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q}`, w.ids["s1"], fx.assignment2), 201)

	ids := func(path string) []string {
		var out []string
		for _, it := range w.must("director", "GET", path, "", 200)["data"].([]any) {
			out = append(out, idOf(it.(map[string]any)))
		}
		return out
	}
	fx.units, fx.units2 = ids("/api/academic/subjects/"+subject+"/units"), ids("/api/academic/subjects/"+subject2+"/units")
	fx.foreignUnit = fx.units2[0]
	for _, it := range w.must("director", "GET", "/api/academic/teaching-assignments/"+fx.assignment+"/students", "", 200)["data"].([]any) {
		m := it.(map[string]any)
		switch m["student_id"] {
		case w.ids["s1"]:
			fx.se1 = idOf(m)
		case w.ids["s2"]:
			fx.se2 = idOf(m)
		}
	}
	for _, it := range w.must("director", "GET", "/api/academic/teaching-assignments/"+fx.assignment2+"/students", "", 200)["data"].([]any) {
		fx.se1b = idOf(it.(map[string]any))
	}
	if fx.se1 == "" || fx.se2 == "" || fx.se1b == "" || len(fx.units) != 7 {
		t.Fatalf("fixture incomplete: %+v", fx)
	}
	return fx
}

func (fx *gradeFx) body(se, unit, score string) string {
	return fmt.Sprintf(`{"subject_enrollment_id":%q,"subject_unit_id":%q,"score":%s}`, se, unit, score)
}

func (fx *gradeFx) capture(who, se, unit, score string) resp {
	return fx.w.do(who, "POST", "/api/grades", fx.body(se, unit, score))
}

func (fx *gradeFx) dbScore(gradeID string) string {
	fx.w.t.Helper()
	var s string
	if err := fx.w.db.Q(context.Background()).QueryRow(context.Background(), `SELECT score::text FROM grades WHERE id = $1`, gradeID).Scan(&s); err != nil {
		fx.w.t.Fatal(err)
	}
	return s
}

func TestGradesAuthenticationRequired(t *testing.T) {
	w := newWorld(t)
	id := uuid.NewString()
	for _, ep := range [][2]string{
		{"POST", "/api/grades"}, {"GET", "/api/grades/" + id}, {"PATCH", "/api/grades/" + id},
		{"GET", "/api/students/" + id + "/grades"}, {"GET", "/api/teaching-assignments/" + id + "/grades"},
	} {
		if r := w.c.do(ep[0], ep[1], "", `{}`); r.status != 401 {
			t.Errorf("%s %s without token: %d, want 401", ep[0], ep[1], r.status)
		}
		if r := w.c.do(ep[0], ep[1], "garbage", `{}`); r.status != 401 {
			t.Errorf("%s %s with garbage token: %d, want 401", ep[0], ep[1], r.status)
		}
	}
}

func TestGradesCaptureClassificationAndExactScores(t *testing.T) {
	fx := newGradeFx(t)
	// Every official boundary over HTTP, each on its own unit of the same attempt.
	cases := []struct{ score, wantScore, category string }{
		{"0", "0.00", "NA"}, {"79.99", "79.99", "NA"}, {"80", "80.00", "SA"}, {"89.99", "89.99", "SA"},
		{"90.0", "90.00", "DE"}, {"99.99", "99.99", "DE"}, {"100", "100.00", "AU"},
	}
	for i, c := range cases {
		r := fx.capture("teacher", fx.se1, fx.units[i], c.score)
		if r.status != 201 {
			t.Fatalf("score %s: %d %s", c.score, r.status, r.body)
		}
		if !strings.Contains(r.body, `"score":`+c.wantScore+`,`) {
			t.Errorf("score %s: body %s does not carry the exact value %s", c.score, r.body, c.wantScore)
		}
		m := r.json(t)
		if m["category"] != c.category || m["recorded_by"] != fx.w.ids["teacher"] {
			t.Errorf("score %s: category=%v recorded_by=%v, want %s / teacher", c.score, m["category"], m["recorded_by"], c.category)
		}
		if got := fx.dbScore(idOf(m)); got != c.wantScore {
			t.Errorf("score %s stored as %s, want %s", c.score, got, c.wantScore)
		}
	}
}

func TestGradesCaptureValidationAndRelations(t *testing.T) {
	fx := newGradeFx(t)
	w := fx.w
	u := fx.units[0]
	for _, tt := range []struct {
		name, score, code string
		status            int
	}{
		{"negative", "-0.01", "score_out_of_range", 422},
		{"above 100", "100.01", "score_out_of_range", 422},
		{"far above", "1000", "score_out_of_range", 422},
		{"three decimals", "89.999", "invalid_score_precision", 422},
		{"exponent", "1e2", "invalid_score", 422},
		{"null", "null", "invalid_score", 422},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := fx.capture("teacher", fx.se1, u, tt.score)
			if r.status != tt.status || r.errCode(t) != tt.code {
				t.Fatalf("status=%d code=%q body=%s", r.status, r.errCode(t), r.body)
			}
		})
	}
	// malformed or unexpected bodies -> 400
	for name, body := range map[string]string{
		"not json":            `{`,
		"score is text":       fx.body(fx.se1, u, `"abc"`),
		"teacher id in body":  fmt.Sprintf(`{"subject_enrollment_id":%q,"subject_unit_id":%q,"score":90,"teacher_id":%q}`, fx.se1, u, w.ids["teacher2"]),
		"recorded_by in body": fmt.Sprintf(`{"subject_enrollment_id":%q,"subject_unit_id":%q,"score":90,"recorded_by":%q}`, fx.se1, u, w.ids["teacher2"]),
		"bad uuid":            `{"subject_enrollment_id":"x","subject_unit_id":"y","score":90}`,
	} {
		if r := w.do("teacher", "POST", "/api/grades", body); r.status != 400 || r.errCode(t) != "invalid_body" {
			t.Errorf("%s: status=%d code=%q", name, r.status, r.errCode(t))
		}
	}
	if r := w.do("teacher", "POST", "/api/grades", `{"subject_enrollment_id":"`+fx.se1+`","subject_unit_id":"`+u+`"}`); r.status != 422 || r.errCode(t) != "invalid_score" {
		t.Errorf("missing score: %d %s", r.status, r.body)
	}
	// references
	for _, c := range []struct{ se, unit, code string }{
		{uuid.NewString(), u, "subject_enrollment_not_found"},
		{fx.se1, uuid.NewString(), "subject_unit_not_found"},
		{fx.se1, fx.foreignUnit, "unit_not_in_subject"}, // FIS1 unit on a MAT1 enrollment
		{fx.se1b, u, "unit_not_in_subject"},             // MAT1 unit on the FIS1 enrollment (teacher2's anyway)
	} {
		who := "teacher"
		if c.se == fx.se1b {
			who = "teacher2"
		}
		if r := fx.capture(who, c.se, c.unit, "90"); r.status != 422 || r.errCode(t) != c.code {
			t.Errorf("%+v: status=%d code=%q", c, r.status, r.errCode(t))
		}
	}
	if n := w.sqlInt(`SELECT count(*) FROM grades`); n != 0 {
		t.Fatalf("rejected requests stored %d grades", n)
	}
}

func TestGradesWriteAuthorization(t *testing.T) {
	fx := newGradeFx(t)
	w := fx.w
	first := fx.capture("teacher", fx.se1, fx.units[0], "70")
	if first.status != 201 {
		t.Fatal(first.body)
	}
	gid := idOf(first.json(t))
	for _, who := range []string{"teacher2", "tutor", "tutor2", "s1", "s2", "director", "admin"} {
		if r := fx.capture(who, fx.se1, fx.units[1], "100"); r.status != 403 || r.errCode(t) != "forbidden" {
			t.Errorf("POST as %s: %d %s", who, r.status, r.body)
		}
		if r := w.do(who, "PATCH", "/api/grades/"+gid, `{"score":100}`); r.status != 403 || r.errCode(t) != "forbidden" {
			t.Errorf("PATCH as %s: %d %s", who, r.status, r.body)
		}
	}
	if got := fx.dbScore(gid); got != "70.00" {
		t.Fatalf("a forbidden caller changed the score to %s", got)
	}
	if n := w.sqlInt(`SELECT count(*) FROM grades`); n != 1 {
		t.Fatalf("forbidden POSTs stored grades (%d total)", n)
	}
	// teacher2 teaches FIS1: can grade their own subject but not MAT1's students/units
	if r := fx.capture("teacher2", fx.se1b, fx.units2[0], "95"); r.status != 201 {
		t.Fatalf("teacher2 on own assignment: %d %s", r.status, r.body)
	}
	// teacher2 cannot use the MAT1 enrollment even with a FIS1 unit
	if r := fx.capture("teacher2", fx.se1, fx.units2[0], "95"); r.status != 403 {
		t.Errorf("teacher2 on MAT1 enrollment: %d", r.status)
	}
}

func TestGradesDuplicateAndUpdate(t *testing.T) {
	fx := newGradeFx(t)
	w := fx.w
	r := fx.capture("teacher", fx.se1, fx.units[0], "79.99")
	gid := idOf(r.json(t))
	recordedAt := r.json(t)["recorded_at"]

	if d := fx.capture("teacher", fx.se1, fx.units[0], "100"); d.status != 409 || d.errCode(t) != "grade_exists" {
		t.Fatalf("duplicate: %d %s", d.status, d.body)
	}
	if got := fx.dbScore(gid); got != "79.99" {
		t.Fatalf("duplicate POST overwrote the score: %s", got)
	}
	// the same unit for ANOTHER student attempt is not a duplicate
	if o := fx.capture("teacher", fx.se2, fx.units[0], "55.5"); o.status != 201 {
		t.Fatalf("other student: %d %s", o.status, o.body)
	}

	u := w.do("teacher", "PATCH", "/api/grades/"+gid, `{"score":80}`)
	if u.status != 200 {
		t.Fatalf("patch: %d %s", u.status, u.body)
	}
	um := u.json(t)
	if um["score"] != float64(80) || um["category"] != "SA" || um["recorded_at"] != recordedAt || um["updated_at"] == um["recorded_at"] {
		t.Fatalf("patched grade = %v", um)
	}
	if !strings.Contains(u.body, `"score":80.00,`) || fx.dbScore(gid) != "80.00" {
		t.Fatalf("score not exact: %s / %s", u.body, fx.dbScore(gid))
	}
	for _, c := range []struct {
		body, code string
		status     int
	}{
		{`{"score":-1}`, "score_out_of_range", 422}, {`{"score":100.01}`, "score_out_of_range", 422},
		{`{"score":50.005}`, "invalid_score_precision", 422}, {`{}`, "invalid_score", 422},
		{`{"score":"x"}`, "invalid_body", 400}, {`{"score":90,"recorded_by":"` + w.ids["teacher2"] + `"}`, "invalid_body", 400},
	} {
		if p := w.do("teacher", "PATCH", "/api/grades/"+gid, c.body); p.status != c.status || p.errCode(t) != c.code {
			t.Errorf("PATCH %s: %d %q (%s)", c.body, p.status, p.errCode(t), p.body)
		}
	}
	if fx.dbScore(gid) != "80.00" {
		t.Fatal("invalid PATCH changed the score")
	}
	w.expect("teacher", "PATCH", "/api/grades/"+uuid.NewString(), `{"score":90}`, 404, "grade_not_found")
	w.expect("teacher", "PATCH", "/api/grades/not-a-uuid", `{"score":90}`, 404, "grade_not_found")
	// recorded_by keeps the original recorder; zero is a valid correction
	if z := w.do("teacher", "PATCH", "/api/grades/"+gid, `{"score":0}`); z.status != 200 || z.json(t)["category"] != "NA" || z.json(t)["recorded_by"] != w.ids["teacher"] {
		t.Fatalf("zero patch: %d %s", z.status, z.body)
	}
	// audit
	if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'grades.created' AND actor_user_id = $1`, w.ids["teacher"]); n != 2 {
		t.Errorf("grades.created audit rows = %d, want 2", n)
	}
	if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'grades.updated' AND entity_id = $1 AND metadata->>'from' = '79.99' AND metadata->>'to' = '80.00'`, gid); n != 1 {
		t.Errorf("grades.updated audit row with from/to = %d, want 1", n)
	}
	if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action LIKE 'grades.%' AND (metadata::text ILIKE '%password%' OR metadata::text ILIKE '%token%')`); n != 0 {
		t.Errorf("grade audit rows mention secrets")
	}
}

func TestGradesReadAuthorizationAndShapes(t *testing.T) {
	fx := newGradeFx(t)
	w := fx.w
	g0 := idOf(fx.capture("teacher", fx.se1, fx.units[0], "0").json(t)) // a REAL zero
	fx.capture("teacher", fx.se1, fx.units[1], "92.5")
	fx.capture("teacher", fx.se2, fx.units[0], "100")
	fx.capture("teacher2", fx.se1b, fx.units2[0], "85")

	t.Run("single grade", func(t *testing.T) {
		for _, who := range []string{"teacher", "tutor", "s1", "director"} {
			if r := w.do(who, "GET", "/api/grades/"+g0, ""); r.status != 200 || r.json(t)["score"] != float64(0) {
				t.Errorf("%s: %d %s", who, r.status, r.body)
			}
		}
		for _, who := range []string{"teacher2", "tutor2", "s2", "s3", "admin"} {
			w.expect(who, "GET", "/api/grades/"+g0, "", 403, "forbidden")
		}
		w.expect("director", "GET", "/api/grades/"+uuid.NewString(), "", 404, "grade_not_found")
		w.expect("director", "GET", "/api/grades/zzz", "", 404, "grade_not_found")
	})

	sheets := func(who, student string, wantStatus int) []any {
		t.Helper()
		r := w.do(who, "GET", "/api/students/"+student+"/grades", "")
		if r.status != wantStatus {
			t.Fatalf("%s reading %s: %d %s", who, student, r.status, r.body)
		}
		if wantStatus != 200 {
			return nil
		}
		return r.json(t)["data"].([]any)
	}

	t.Run("student grades by role", func(t *testing.T) {
		// s1 has two attempts/subjects (MAT1 + FIS1); each lists ALL units of the subject
		own := sheets("s1", w.ids["s1"], 200)
		if len(own) != 2 {
			t.Fatalf("s1 sees %d sheets, want 2", len(own))
		}
		var mat map[string]any
		for _, s := range own {
			if s.(map[string]any)["subject"].(map[string]any)["code"] == "MAT1" {
				mat = s.(map[string]any)
			}
		}
		units := mat["units"].([]any)
		if len(units) != 7 {
			t.Fatalf("MAT1 sheet has %d units, want all 7", len(units))
		}
		zero := units[0].(map[string]any)["grade"].(map[string]any)
		if zero["score"] != float64(0) || zero["category"] != "NA" {
			t.Errorf("captured 0.00 must be reported as a grade, got %v", zero)
		}
		if units[1].(map[string]any)["grade"].(map[string]any)["score"] != 92.5 {
			t.Error("unit 2 score")
		}
		for i := 2; i < 7; i++ {
			if units[i].(map[string]any)["grade"] != nil {
				t.Errorf("pending unit %d must have grade null, never 0", i+1)
			}
		}
		if mat["attempt_number"] != float64(1) || mat["academic_term"].(map[string]any)["name"] != "2026-1" {
			t.Errorf("sheet header = %v", mat)
		}

		// director sees everything; teacher only MAT1; teacher2 only FIS1; tutor both
		if len(sheets("director", w.ids["s1"], 200)) != 2 || len(sheets("tutor", w.ids["s1"], 200)) != 2 {
			t.Error("director and tutor should see both attempts")
		}
		if got := sheets("teacher", w.ids["s1"], 200); len(got) != 1 || got[0].(map[string]any)["subject"].(map[string]any)["code"] != "MAT1" {
			t.Errorf("teacher must only see their own subject, got %v", got)
		}
		if got := sheets("teacher2", w.ids["s1"], 200); len(got) != 1 || got[0].(map[string]any)["subject"].(map[string]any)["code"] != "FIS1" {
			t.Errorf("teacher2 must only see their own subject, got %v", got)
		}
		// s2 only takes MAT1
		if len(sheets("s2", w.ids["s2"], 200)) != 1 {
			t.Error("s2 should see one sheet")
		}
		// a student with no subject enrollments gets an empty list, not an error
		if len(sheets("s3", w.ids["s3"], 200)) != 0 {
			t.Error("s3 has no grades")
		}
	})

	t.Run("forbidden and missing", func(t *testing.T) {
		sheets("s1", w.ids["s2"], 403) // student -> other student
		sheets("s2", w.ids["s1"], 403)
		sheets("tutor2", w.ids["s1"], 403) // tutor of another group
		sheets("admin", w.ids["s1"], 403)  // administrators get no academic records
		sheets("admin", w.ids["admin"], 403)
		sheets("s1", w.ids["teacher"], 403)
		sheets("teacher", w.ids["s4"], 403) // s4 is not in any of their assignments
		sheets("tutor", w.ids["s4"], 403)
		sheets("director", uuid.NewString(), 404)
		sheets("director", w.ids["teacher"], 404) // not a student
		sheets("director", "not-a-uuid", 404)
		// a teacher whose only link is another subject cannot see a student they never taught
		sheets("teacher2", w.ids["s2"], 403)
	})

	t.Run("assignment table", func(t *testing.T) {
		path := "/api/teaching-assignments/" + fx.assignment + "/grades"
		for _, who := range []string{"teacher", "tutor", "director"} {
			if r := w.do(who, "GET", path, ""); r.status != 200 {
				t.Errorf("%s: %d %s", who, r.status, r.body)
			}
		}
		for _, who := range []string{"teacher2", "tutor2", "s1", "s2", "admin"} {
			w.expect(who, "GET", path, "", 403, "forbidden")
		}
		w.expect("director", "GET", "/api/teaching-assignments/"+uuid.NewString()+"/grades", "", 404, "teaching_assignment_not_found")
		w.expect("director", "GET", "/api/teaching-assignments/zzz/grades", "", 404, "teaching_assignment_not_found")

		m := w.must("teacher", "GET", path, "", 200)
		if len(m["units"].([]any)) != 7 {
			t.Fatalf("units = %v", m["units"])
		}
		rows := m["students"].([]any)
		if len(rows) != 2 {
			t.Fatalf("rows = %d, want the 2 enrolled students", len(rows))
		}
		for _, row := range rows {
			rm := row.(map[string]any)
			cells := rm["units"].([]any)
			if len(cells) != 7 {
				t.Fatalf("row has %d cells, want one per unit", len(cells))
			}
			st := rm["student"].(map[string]any)
			if _, leaked := st["curp"]; leaked || st["institutional_number"] == nil {
				t.Errorf("student = %v", st)
			}
			captured := 0
			for _, c := range cells {
				if c.(map[string]any)["grade"] != nil {
					captured++
				}
			}
			switch rm["subject_enrollment_id"] {
			case fx.se1:
				if captured != 2 {
					t.Errorf("s1 has %d captured cells, want 2", captured)
				}
			case fx.se2:
				if captured != 1 {
					t.Errorf("s2 has %d captured cells, want 1", captured)
				}
			}
		}
		if _, hasAverage := m["average"]; hasAverage {
			t.Error("no average is exposed: the institutional policy is undefined")
		}
	})
}

func TestGradesFrozenByClosedTermAndInactiveEnrollment(t *testing.T) {
	fx := newGradeFx(t)
	w := fx.w
	gid := idOf(fx.capture("teacher", fx.se1, fx.units[0], "60").json(t))

	// a withdrawn subject enrollment accepts no more grades
	w.db.Q(context.Background()).Exec(context.Background(), `UPDATE subject_enrollments SET status = 'WITHDRAWN' WHERE id = $1`, fx.se2)
	if r := fx.capture("teacher", fx.se2, fx.units[0], "90"); r.status != 409 || r.errCode(t) != "subject_enrollment_inactive" {
		t.Errorf("withdrawn: %d %s", r.status, r.body)
	}

	w.must("director", "POST", "/api/academic/terms/"+fx.term+"/close", "", 200)
	if r := fx.capture("teacher", fx.se1, fx.units[1], "90"); r.status != 409 || r.errCode(t) != "term_closed" {
		t.Errorf("POST in closed term: %d %s", r.status, r.body)
	}
	if r := w.do("teacher", "PATCH", "/api/grades/"+gid, `{"score":99}`); r.status != 409 || r.errCode(t) != "term_closed" {
		t.Errorf("PATCH in closed term: %d %s", r.status, r.body)
	}
	if fx.dbScore(gid) != "60.00" {
		t.Fatal("closed term must keep the score")
	}
	// history stays readable for everyone entitled to it
	w.must("s1", "GET", "/api/grades/"+gid, "", 200)
	w.must("teacher", "GET", "/api/teaching-assignments/"+fx.assignment+"/grades", "", 200)
	w.must("s1", "GET", "/api/students/"+w.ids["s1"]+"/grades", "", 200)
	// authorization is still checked first: a foreign teacher gets 403, not 409
	if r := fx.capture("teacher2", fx.se1, fx.units[1], "90"); r.status != 403 {
		t.Errorf("foreign teacher in closed term: %d, want 403", r.status)
	}
}

func TestGradesConcurrency(t *testing.T) {
	fx := newGradeFx(t)
	w := fx.w

	t.Run("simultaneous identical POSTs create exactly one grade", func(t *testing.T) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		created, conflicts := 0, 0
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				r := fx.capture("teacher", fx.se1, fx.units[0], fmt.Sprintf("%d", 50+i))
				mu.Lock()
				defer mu.Unlock()
				switch r.status {
				case 201:
					created++
				case 409:
					conflicts++
					if r.errCode(t) != "grade_exists" {
						t.Errorf("conflict code = %s", r.errCode(t))
					}
				default:
					t.Errorf("unexpected %d: %s", r.status, r.body)
				}
			}(i)
		}
		wg.Wait()
		if created != 1 || conflicts != 9 {
			t.Fatalf("created=%d conflicts=%d, want 1 / 9", created, conflicts)
		}
		if n := w.sqlInt(`SELECT count(*) FROM grades WHERE subject_enrollment_id = $1 AND subject_unit_id = $2`, fx.se1, fx.units[0]); n != 1 {
			t.Fatalf("%d grades stored for the same attempt and unit", n)
		}
		if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'grades.created'`); n != 1 {
			t.Fatalf("audit rows = %d, the 9 rejected POSTs must not be audited", n)
		}
	})

	t.Run("simultaneous PATCHes serialize without corrupting the score", func(t *testing.T) {
		gid := w.sqlString(`SELECT id::text FROM grades WHERE subject_enrollment_id = $1`, fx.se1)
		sent := map[string]bool{}
		var wg sync.WaitGroup
		var mu sync.Mutex
		for i := 0; i < 8; i++ {
			score := fmt.Sprintf("%d.%02d", 70+i, 10+i) // 70.10, 71.11, ... all different from the stored value
			sent[score] = true
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := w.do("teacher", "PATCH", "/api/grades/"+gid, fmt.Sprintf(`{"score":%s}`, score))
				mu.Lock()
				defer mu.Unlock()
				if r.status != 200 {
					t.Errorf("PATCH %s: %d %s", score, r.status, r.body)
				}
			}()
		}
		wg.Wait()
		if final := fx.dbScore(gid); !sent[final] {
			t.Fatalf("final score %s was never sent", final)
		}
		if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = 'grades.updated' AND entity_id = $1`, gid); n != 8 {
			t.Fatalf("update audit rows = %d, want 8", n)
		}
		// Row locking serializes the edits, so the audit trail (identity order = lock order)
		// forms an unbroken chain: each update's "from" is the previous "to", starting from
		// the captured value. A lost update would break the chain.
		prev := w.sqlString(`SELECT metadata->>'score' FROM audit_logs WHERE action = 'grades.created' AND entity_id = $1`, gid)
		rows, err := w.db.Q(context.Background()).Query(context.Background(),
			`SELECT metadata->>'from', metadata->>'to' FROM audit_logs WHERE action = 'grades.updated' AND entity_id = $1 ORDER BY id`, gid)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var from, to string
			_ = rows.Scan(&from, &to)
			if from != prev {
				t.Fatalf("update went from %s but the previous value was %s: lost update", from, prev)
			}
			prev = to
		}
		if prev != fx.dbScore(gid) {
			t.Fatalf("audit chain ends at %s but the stored score is %s", prev, fx.dbScore(gid))
		}
	})

	t.Run("PATCH racing the term close ends consistent", func(t *testing.T) {
		gid := w.sqlString(`SELECT id::text FROM grades WHERE subject_enrollment_id = $1`, fx.se1)
		before := fx.dbScore(gid)
		var wg sync.WaitGroup
		var patch, closeR resp
		wg.Add(2)
		go func() { defer wg.Done(); patch = w.do("teacher", "PATCH", "/api/grades/"+gid, `{"score":1.23}`) }()
		go func() {
			defer wg.Done()
			closeR = w.do("director", "POST", "/api/academic/terms/"+fx.term+"/close", "")
		}()
		wg.Wait()
		if closeR.status != 200 {
			t.Fatalf("close: %d %s", closeR.status, closeR.body)
		}
		after := fx.dbScore(gid)
		switch patch.status {
		case 200:
			if after != "1.23" {
				t.Fatalf("patch succeeded but score is %s", after)
			}
		case 409:
			if after != before {
				t.Fatalf("patch rejected but score changed to %s", after)
			}
		default:
			t.Fatalf("patch: %d %s", patch.status, patch.body)
		}
		// once closed, nothing gets through
		if r := w.do("teacher", "PATCH", "/api/grades/"+gid, `{"score":2.34}`); r.status != 409 {
			t.Fatalf("after close: %d", r.status)
		}
	})
}

// failingAudit is defined in the academic integration tests.
func TestGradesWritesRollBackWithTheirAuditRow(t *testing.T) {
	fx := newGradeFx(t)
	w := fx.w
	gid := idOf(fx.capture("teacher", fx.se1, fx.units[0], "60").json(t))
	svc := gradesapp.NewService(gradesapp.Deps{
		Grades: gradespg.NewRepository(w.db), Academic: gradespg.NewAcademicReader(w.db),
		Audit: failingAudit{}, Tx: w.db, NewID: uuid.New,
	})
	teacher := domain.Principal{UserID: uuid.MustParse(w.ids["teacher"]), SessionID: uuid.New(), Roles: []domain.Role{domain.RoleTeacher}}
	ctx := context.Background()

	if _, err := svc.CaptureGrade(ctx, teacher, gradesapp.CaptureGradeInput{
		SubjectEnrollmentID: uuid.MustParse(fx.se1), SubjectUnitID: uuid.MustParse(fx.units[1]), Score: "90"}); err == nil {
		t.Fatal("capture must fail when its audit row cannot be written")
	}
	if n := w.sqlInt(`SELECT count(*) FROM grades`); n != 1 {
		t.Fatalf("grades = %d, the rolled-back capture must leave nothing", n)
	}
	if _, err := svc.UpdateGrade(ctx, teacher, uuid.MustParse(gid), "99"); err == nil {
		t.Fatal("update must fail when its audit row cannot be written")
	}
	if got := fx.dbScore(gid); got != "60.00" {
		t.Fatalf("score = %s after a rolled-back update, want 60.00", got)
	}
}

func TestGradesDatabaseConstraints(t *testing.T) {
	fx := newGradeFx(t)
	w := fx.w
	ctx := context.Background()
	exec := func(sql string, args ...any) error {
		_, err := w.db.Q(ctx).Exec(ctx, sql, args...)
		return err
	}
	ins := `INSERT INTO grades (id, subject_enrollment_id, subject_unit_id, score, recorded_by) VALUES ($1, $2, $3, $4::numeric, $5)`

	// CHECK: range
	for _, bad := range []string{"-0.01", "100.01", "1000"} {
		if err := exec(ins, uuid.New(), fx.se1, fx.units[0], bad, w.ids["teacher"]); err == nil {
			t.Errorf("score %s must violate a constraint", bad)
		}
	}
	// FK failures
	if err := exec(ins, uuid.New(), uuid.New(), fx.units[0], "50", w.ids["teacher"]); err == nil {
		t.Error("unknown subject_enrollment_id must fail")
	}
	if err := exec(ins, uuid.New(), fx.se1, uuid.New(), "50", w.ids["teacher"]); err == nil {
		t.Error("unknown subject_unit_id must fail")
	}
	if err := exec(ins, uuid.New(), fx.se1, fx.units[0], "50", uuid.NewString()); err == nil {
		t.Error("unknown recorded_by must fail")
	}
	// NOT NULL
	if err := exec(`INSERT INTO grades (id, subject_enrollment_id, subject_unit_id, recorded_by) VALUES ($1, $2, $3, $4)`,
		uuid.New(), fx.se1, fx.units[0], w.ids["teacher"]); err == nil {
		t.Error("score is NOT NULL: a missing grade is the absence of a row")
	}
	// round trip of every boundary, exact to the hundredth, through the repository
	repo := gradespg.NewRepository(w.db)
	for i, s := range []string{"0.00", "0.01", "79.99", "80.00", "89.99", "90.00", "99.99"} {
		if err := exec(ins, uuid.New(), fx.se2, fx.units[i], s, w.ids["teacher"]); err != nil {
			t.Fatalf("score %s: %v", s, err)
		}
	}
	sheet, err := repo.AssignmentSheet(ctx, uuid.MustParse(fx.assignment))
	if err != nil {
		t.Fatal(err)
	}
	var s2 []string
	for _, row := range sheet.Rows {
		if row.SubjectEnrollmentID.String() != fx.se2 {
			continue
		}
		for _, u := range row.Units {
			if u.Score != nil {
				s2 = append(s2, u.Score.String())
			}
		}
	}
	if strings.Join(s2, ",") != "0.00,0.01,79.99,80.00,89.99,90.00,99.99" {
		t.Fatalf("round trip = %v", s2)
	}
	// UNIQUE is enforced by the database itself, not only by the application
	if err := exec(ins, uuid.New(), fx.se2, fx.units[0], "10", w.ids["teacher"]); err == nil {
		t.Error("duplicate (subject_enrollment_id, subject_unit_id) must be rejected by PostgreSQL")
	}
	// no stored category column: it is derived
	if n := w.sqlInt(`SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'grades' AND column_name IN ('category','captured_by','created_at')`); n != 0 {
		t.Errorf("grades has %d unexpected columns", n)
	}
}

func (w *world) sqlString(query string, args ...any) string {
	w.t.Helper()
	var v string
	if err := w.db.Q(context.Background()).QueryRow(context.Background(), query, args...).Scan(&v); err != nil {
		w.t.Fatal(err)
	}
	return v
}
