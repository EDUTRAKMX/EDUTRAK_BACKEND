package app_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	acadpg "github.com/edutrak/backend/internal/academic/adapters/postgres"
	acadapp "github.com/edutrak/backend/internal/academic/application"
	"github.com/edutrak/backend/internal/app"
	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	"github.com/edutrak/backend/internal/identity/application"
	"github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/config"
	"github.com/edutrak/backend/internal/platform/database"
	"github.com/edutrak/backend/internal/platform/database/dbtest"
	"github.com/google/uuid"
)

const acadPass = "academic-passphrase-123"

// world is a migrated database with one account per role (all fictitious),
// the full HTTP handler and a bearer token for every account.
type world struct {
	t     *testing.T
	db    *database.DB
	cfg   config.Config
	c     client
	ids   map[string]string // account key -> user id
	token map[string]string // account key -> access token
}

func newWorld(t *testing.T) *world {
	t.Helper()
	db := dbtest.New(t)
	cfg := config.Config{
		JWTSecret: []byte("0123456789abcdef0123456789abcdef"), AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: time.Hour,
		LoginMaxFailuresPerAccount: 5, LoginMaxFailuresPerIP: 150, LoginRateWindow: 15 * time.Minute,
	}
	idsvc, err := app.NewIdentityService(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	accounts := []struct {
		key, number string
		role        domain.Role
	}{
		{"admin", "10000001", domain.RoleAdmin}, {"director", "20000001", domain.RoleDirector},
		{"teacher", "30000001", domain.RoleTeacher}, {"teacher2", "30000002", domain.RoleTeacher},
		{"tutor", "50000001", domain.RoleTutor}, {"tutor2", "50000002", domain.RoleTutor},
		{"s1", "40000001", domain.RoleStudent}, {"s2", "40000002", domain.RoleStudent},
		{"s3", "40000003", domain.RoleStudent}, {"s4", "40000004", domain.RoleStudent},
	}
	w := &world{t: t, db: db, cfg: cfg, ids: map[string]string{}, token: map[string]string{}}
	for _, a := range accounts {
		u, err := idsvc.BootstrapUser(context.Background(), application.CreateUserInput{
			InstitutionalNumber: a.number, FirstName: "Nombre" + a.key, LastName: "Apellido" + a.key, Password: acadPass, Roles: []domain.Role{a.role},
		})
		if err != nil {
			t.Fatal(err)
		}
		w.ids[a.key] = u.ID.String()
	}
	h, err := app.NewHTTPHandler(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	w.c = client{t, h}
	for _, a := range accounts {
		r := w.c.do("POST", "/api/auth/login", "", `{"institutional_number":"`+a.number+`","password":"`+acadPass+`"}`)
		if r.status != 200 {
			t.Fatalf("login %s: %d %s", a.key, r.status, r.body)
		}
		w.token[a.key], _ = r.json(t)["access_token"].(string)
	}
	return w
}

func (w *world) do(who, method, path, body string) resp {
	w.t.Helper()
	return w.c.do(method, path, w.token[who], body)
}

// must performs a request that has to succeed with wantStatus and returns the decoded body.
func (w *world) must(who, method, path, body string, wantStatus int) map[string]any {
	w.t.Helper()
	r := w.do(who, method, path, body)
	if r.status != wantStatus {
		w.t.Fatalf("%s %s as %s: status %d, want %d: %s", method, path, who, r.status, wantStatus, r.body)
	}
	return r.json(w.t)
}

func (w *world) expect(who, method, path, body string, wantStatus int, wantCode string) {
	w.t.Helper()
	r := w.do(who, method, path, body)
	if r.status != wantStatus {
		w.t.Errorf("%s %s as %s: status %d, want %d: %s", method, path, who, r.status, wantStatus, r.body)
		return
	}
	if wantCode != "" && r.errCode(w.t) != wantCode {
		w.t.Errorf("%s %s as %s: error code %q, want %q", method, path, who, r.errCode(w.t), wantCode)
	}
}

func idOf(m map[string]any) string { s, _ := m["id"].(string); return s }

// count returns len(data) of a list response.
func (w *world) count(who, path string) int {
	w.t.Helper()
	m := w.must(who, "GET", path, "", 200)
	data, ok := m["data"].([]any)
	if !ok {
		w.t.Fatalf("GET %s: no data array: %v", path, m)
	}
	return len(data)
}

func (w *world) sqlInt(query string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.db.Q(context.Background()).QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// seedPlan creates career + subject (with two units) + curriculum at level 2, an
// active term and returns their ids.
func (w *world) seedPlan() (career, subject, term string) {
	w.t.Helper()
	career = idOf(w.must("director", "POST", "/api/academic/careers", `{"code":"tic","name":"Tecnologias de la Informacion"}`, 201))
	subject = idOf(w.must("director", "POST", "/api/academic/subjects", `{"code":"mat1","name":"Matematicas I"}`, 201))
	w.must("director", "POST", "/api/academic/subjects/"+subject+"/units", `{"unit_number":1,"name":"Algebra"}`, 201)
	w.must("director", "POST", "/api/academic/subjects/"+subject+"/units", `{"unit_number":2,"name":"Calculo"}`, 201)
	w.must("director", "POST", "/api/academic/careers/"+career+"/curriculum", fmt.Sprintf(`{"subject_id":%q,"level":2}`, subject), 201)
	term = idOf(w.must("director", "POST", "/api/academic/terms", `{"name":"2026-1","start_date":"2026-01-05","end_date":"2026-04-30"}`, 201))
	w.must("director", "POST", "/api/academic/terms/"+term+"/activate", "", 200)
	return
}

func TestAcademicAuthenticationRequired(t *testing.T) {
	w := newWorld(t)
	for _, ep := range [][2]string{
		{"GET", "/api/academic/careers"}, {"GET", "/api/academic/terms"}, {"GET", "/api/academic/groups"},
		{"POST", "/api/academic/groups"}, {"GET", "/api/academic/schedules"}, {"GET", "/api/academic/subject-enrollments"},
		{"POST", "/api/academic/terms/" + uuid.NewString() + "/activate"},
	} {
		if r := w.c.do(ep[0], ep[1], "", ""); r.status != 401 {
			t.Errorf("%s %s without token: %d, want 401", ep[0], ep[1], r.status)
		}
		if r := w.c.do(ep[0], ep[1], "not-a-valid-token", ""); r.status != 401 {
			t.Errorf("%s %s with a garbage token: %d, want 401", ep[0], ep[1], r.status)
		}
	}
}

func TestAcademicCatalogsAndTerms(t *testing.T) {
	w := newWorld(t)
	career, subject, term := w.seedPlan()

	t.Run("catalog reads are open to every academic role, writes to the director", func(t *testing.T) {
		for _, who := range []string{"director", "teacher", "tutor", "s1", "admin"} {
			if w.count(who, "/api/academic/careers") != 1 || w.count(who, "/api/academic/subjects") != 1 {
				t.Errorf("%s should see the catalogs", who)
			}
		}
		if w.count("s1", "/api/academic/subjects/"+subject+"/units") != 2 || w.count("s1", "/api/academic/careers/"+career+"/curriculum") != 1 {
			t.Error("units / curriculum not listed")
		}
		if w.count("s1", "/api/academic/careers/"+career+"/curriculum?level=2") != 1 || w.count("s1", "/api/academic/careers/"+career+"/curriculum?level=3") != 0 {
			t.Error("curriculum level filter")
		}
		w.expect("s1", "GET", "/api/academic/careers/"+career+"/curriculum?level=abc", "", 422, "invalid_level")
		for _, who := range []string{"s1", "teacher", "tutor", "admin"} {
			w.expect(who, "POST", "/api/academic/careers", `{"code":"X","name":"X"}`, 403, "forbidden")
			w.expect(who, "POST", "/api/academic/subjects", `{"code":"X","name":"X"}`, 403, "forbidden")
		}
		w.expect("director", "POST", "/api/academic/careers", `{"code":"TIC","name":"Duplicada"}`, 409, "career_code_taken")
		w.expect("director", "POST", "/api/academic/subjects", `{"code":"MAT1","name":"Duplicada"}`, 409, "subject_code_taken")
		w.expect("director", "POST", "/api/academic/subjects/"+subject+"/units", `{"unit_number":1,"name":"Otra"}`, 409, "unit_number_taken")
		w.expect("director", "POST", "/api/academic/subjects/"+subject+"/units", `{"unit_number":0,"name":"Cero"}`, 422, "invalid_unit_number")
		w.expect("director", "POST", "/api/academic/careers/"+career+"/curriculum", fmt.Sprintf(`{"subject_id":%q,"level":2}`, subject), 409, "curriculum_entry_exists")
		// the same subject may appear at another level of the same career
		w.must("director", "POST", "/api/academic/careers/"+career+"/curriculum", fmt.Sprintf(`{"subject_id":%q,"level":3}`, subject), 201)
		w.expect("director", "POST", "/api/academic/careers/"+career+"/curriculum", fmt.Sprintf(`{"subject_id":%q,"level":0}`, subject), 422, "invalid_level")
		w.expect("director", "GET", "/api/academic/careers/"+uuid.NewString(), "", 404, "career_not_found")
		w.expect("director", "GET", "/api/academic/subjects/not-a-uuid", "", 404, "subject_not_found")
		w.expect("director", "GET", "/api/academic/subjects/"+uuid.NewString()+"/units", "", 404, "subject_not_found")
	})

	t.Run("term lifecycle and validation", func(t *testing.T) {
		if got := w.must("director", "GET", "/api/academic/terms/"+term, "", 200); got["status"] != "active" || got["start_date"] != "2026-01-05" {
			t.Fatalf("term = %v", got)
		}
		w.expect("director", "POST", "/api/academic/terms", `{"name":"x","start_date":"2026-05-01","end_date":"2026-04-01"}`, 422, "invalid_dates")
		w.expect("director", "POST", "/api/academic/terms", `{"name":"x","start_date":"2026-05-01","end_date":"2026-05-01"}`, 422, "invalid_dates")
		w.expect("director", "POST", "/api/academic/terms", `{"name":"x","start_date":"05/01/2026","end_date":"2026-06-01"}`, 422, "invalid_start_date")
		w.expect("director", "POST", "/api/academic/terms", `{"name":"x","start_date":"2026-05-01"}`, 422, "invalid_end_date")
		w.expect("director", "POST", "/api/academic/terms", `{"name":"2026-1","start_date":"2026-05-01","end_date":"2026-06-01"}`, 409, "term_name_taken")
		w.expect("director", "POST", "/api/academic/terms", `{"nombre":"x"}`, 400, "invalid_body")

		draft := idOf(w.must("director", "POST", "/api/academic/terms", `{"name":"2026-2","start_date":"2026-05-04","end_date":"2026-08-28"}`, 201))
		w.expect("director", "POST", "/api/academic/terms/"+draft+"/close", "", 409, "invalid_term_transition")
		w.expect("director", "POST", "/api/academic/terms/"+term+"/activate", "", 409, "invalid_term_transition")
		// more than one active term is allowed: only `status` models the state
		w.must("director", "POST", "/api/academic/terms/"+draft+"/activate", "", 200)
		if w.count("director", "/api/academic/terms") != 2 {
			t.Error("director should list both terms")
		}
		w.must("director", "POST", "/api/academic/terms/"+draft+"/close", "", 200)
		w.expect("director", "POST", "/api/academic/terms/"+draft+"/activate", "", 409, "invalid_term_transition")
		w.expect("director", "POST", "/api/academic/terms/"+draft+"/close", "", 409, "invalid_term_transition")
		w.expect("director", "GET", "/api/academic/terms/"+uuid.NewString(), "", 404, "term_not_found")

		for _, who := range []string{"admin", "teacher", "tutor", "s1"} {
			w.expect(who, "GET", "/api/academic/terms", "", 403, "forbidden")
			w.expect(who, "GET", "/api/academic/terms/"+term, "", 403, "forbidden")
			w.expect(who, "POST", "/api/academic/terms", `{"name":"z","start_date":"2027-01-01","end_date":"2027-02-01"}`, 403, "forbidden")
			w.expect(who, "POST", "/api/academic/terms/"+term+"/close", "", 403, "forbidden")
		}
		if got := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action IN ('academic.term_created','academic.term_activated','academic.term_closed')`); got != 5 {
			t.Errorf("term audit rows = %d, want 5 (2 created, 2 activated, 1 closed)", got)
		}
	})
}

func TestAcademicGroupsAssignmentsAndRoleViews(t *testing.T) {
	w := newWorld(t)
	career, subject, term := w.seedPlan()
	otherSubject := idOf(w.must("director", "POST", "/api/academic/subjects", `{"code":"FIS1","name":"Fisica I"}`, 201)) // exists, not in plan
	inactiveCareer := "00000000-0000-0000-0000-00000000000a"
	w.db.Q(context.Background()).Exec(context.Background(), `INSERT INTO careers (id, code, name, is_active) VALUES ($1, 'OFF', 'Carrera cerrada', false)`, inactiveCareer)

	var group string
	t.Run("create group", func(t *testing.T) {
		mk := func(f string, args ...any) string { return fmt.Sprintf(f, args...) }
		w.expect("director", "POST", "/api/academic/groups", mk(`{"academic_term_id":%q,"career_id":%q,"code":"2A","level":0}`, term, career), 422, "invalid_level")
		w.expect("director", "POST", "/api/academic/groups", mk(`{"academic_term_id":%q,"career_id":%q,"code":"2A","level":-2}`, term, career), 422, "invalid_level")
		w.expect("director", "POST", "/api/academic/groups", mk(`{"academic_term_id":%q,"career_id":%q,"code":"","level":2}`, term, career), 422, "code_required")
		w.expect("director", "POST", "/api/academic/groups", mk(`{"academic_term_id":%q,"career_id":%q,"code":"2A","level":2}`, uuid.NewString(), career), 422, "term_not_found")
		w.expect("director", "POST", "/api/academic/groups", mk(`{"academic_term_id":%q,"career_id":%q,"code":"2A","level":2}`, term, uuid.NewString()), 422, "career_not_found")
		w.expect("director", "POST", "/api/academic/groups", mk(`{"academic_term_id":%q,"career_id":%q,"code":"2A","level":2}`, term, inactiveCareer), 409, "career_inactive")
		for _, notTutor := range []string{"teacher", "s1", "director"} {
			w.expect("director", "POST", "/api/academic/groups", mk(`{"academic_term_id":%q,"career_id":%q,"code":"2A","level":2,"tutor_id":%q}`, term, career, w.ids[notTutor]), 422, "invalid_tutor")
		}
		w.expect("director", "POST", "/api/academic/groups", mk(`{"academic_term_id":%q,"career_id":%q,"code":"2A","level":2,"tutor_id":%q}`, term, career, uuid.NewString()), 422, "user_not_found")
		if n := w.sqlInt(`SELECT count(*) FROM groups`); n != 0 {
			t.Fatalf("rejected requests left %d groups", n)
		}
		g := w.must("director", "POST", "/api/academic/groups", mk(`{"academic_term_id":%q,"career_id":%q,"code":"2a","level":2,"tutor_id":%q}`, term, career, w.ids["tutor"]), 201)
		group = idOf(g)
		if g["code"] != "2A" || g["tutor_id"] != w.ids["tutor"] || g["career_code"] != "TIC" || g["academic_term_name"] != "2026-1" {
			t.Fatalf("group = %v", g)
		}
		w.expect("director", "POST", "/api/academic/groups", mk(`{"academic_term_id":%q,"career_id":%q,"code":"2A","level":2}`, term, career), 409, "group_code_taken")
		for _, who := range []string{"teacher", "tutor", "s1", "admin"} {
			w.expect(who, "POST", "/api/academic/groups", mk(`{"academic_term_id":%q,"career_id":%q,"code":"9Z","level":2}`, term, career), 403, "forbidden")
		}
	})

	t.Run("tutor assignment", func(t *testing.T) {
		w.expect("director", "PUT", "/api/academic/groups/"+group+"/tutor", fmt.Sprintf(`{"tutor_id":%q}`, w.ids["teacher"]), 422, "invalid_tutor")
		w.expect("director", "PUT", "/api/academic/groups/"+group+"/tutor", `{}`, 422, "tutor_id_required")
		w.expect("tutor", "PUT", "/api/academic/groups/"+group+"/tutor", fmt.Sprintf(`{"tutor_id":%q}`, w.ids["tutor"]), 403, "forbidden")
		if g := w.must("director", "PUT", "/api/academic/groups/"+group+"/tutor", fmt.Sprintf(`{"tutor_id":%q}`, w.ids["tutor2"]), 200); g["tutor_id"] != w.ids["tutor2"] {
			t.Fatalf("tutor not changed: %v", g)
		}
		w.expect("tutor", "GET", "/api/academic/groups/"+group, "", 403, "forbidden") // no longer the tutor
		if g := w.must("director", "PUT", "/api/academic/groups/"+group+"/tutor", fmt.Sprintf(`{"tutor_id":%q}`, w.ids["tutor"]), 200); g["tutor_id"] != w.ids["tutor"] {
			t.Fatalf("tutor not restored: %v", g)
		}
		w.must("director", "PUT", "/api/academic/groups/"+group+"/tutor", `{"tutor_id":null}`, 200)
		w.expect("tutor", "GET", "/api/academic/groups/"+group, "", 403, "forbidden")
		w.must("director", "PUT", "/api/academic/groups/"+group+"/tutor", fmt.Sprintf(`{"tutor_id":%q}`, w.ids["tutor"]), 200)
	})

	t.Run("enroll students", func(t *testing.T) {
		enroll := func(who string) string { return fmt.Sprintf(`{"student_id":%q}`, w.ids[who]) }
		w.must("director", "POST", "/api/academic/groups/"+group+"/enrollments", enroll("s1"), 201)
		w.must("director", "POST", "/api/academic/groups/"+group+"/enrollments", enroll("s2"), 201)
		w.expect("director", "POST", "/api/academic/groups/"+group+"/enrollments", enroll("s1"), 409, "already_enrolled")
		for _, notStudent := range []string{"teacher", "tutor", "director"} {
			w.expect("director", "POST", "/api/academic/groups/"+group+"/enrollments", enroll(notStudent), 422, "invalid_student")
		}
		w.expect("director", "POST", "/api/academic/groups/"+group+"/enrollments", fmt.Sprintf(`{"student_id":%q}`, uuid.NewString()), 422, "user_not_found")
		w.expect("director", "POST", "/api/academic/groups/"+uuid.NewString()+"/enrollments", enroll("s3"), 404, "group_not_found")
		for _, who := range []string{"teacher", "tutor", "s1", "admin"} {
			w.expect(who, "POST", "/api/academic/groups/"+group+"/enrollments", enroll("s3"), 403, "forbidden")
		}
		if w.count("director", "/api/academic/groups/"+group+"/enrollments") != 2 || w.count("tutor", "/api/academic/groups/"+group+"/enrollments") != 2 {
			t.Error("director and own tutor should see both students")
		}
		for _, who := range []string{"tutor2", "teacher", "s1", "admin"} {
			w.expect(who, "GET", "/api/academic/groups/"+group+"/enrollments", "", 403, "forbidden")
		}
		list := w.must("director", "GET", "/api/academic/groups/"+group+"/enrollments", "", 200)["data"].([]any)
		student, _ := list[0].(map[string]any)["student"].(map[string]any)
		if _, leaked := student["curp"]; leaked || student["institutional_number"] == nil {
			t.Errorf("student summary = %v (must show the number and never contact data)", student)
		}
	})

	var assignment string
	t.Run("teaching assignments", func(t *testing.T) {
		path := "/api/academic/groups/" + group + "/teaching-assignments"
		w.expect("director", "POST", path, fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, otherSubject, w.ids["teacher"]), 422, "subject_not_in_curriculum")
		w.expect("director", "POST", path, fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, subject, w.ids["tutor"]), 422, "invalid_teacher")
		w.expect("director", "POST", path, fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, uuid.NewString(), w.ids["teacher"]), 422, "subject_not_found")
		w.expect("teacher", "POST", path, fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, subject, w.ids["teacher"]), 403, "forbidden")
		a := w.must("director", "POST", path, fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, subject, w.ids["teacher"]), 201)
		assignment = idOf(a)
		if a["subject_code"] != "MAT1" || a["group_code"] != "2A" || a["teacher_id"] != w.ids["teacher"] {
			t.Fatalf("assignment = %v", a)
		}
		w.expect("director", "POST", path, fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, subject, w.ids["teacher2"]), 409, "teaching_assignment_exists")

		// A subject that is in the plan only at ANOTHER level is rejected for a level-2 group.
		w.must("director", "POST", "/api/academic/careers/"+career+"/curriculum", fmt.Sprintf(`{"subject_id":%q,"level":5}`, otherSubject), 201)
		w.expect("director", "POST", path, fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, otherSubject, w.ids["teacher"]), 422, "subject_not_in_curriculum")

		// teacher swap
		w.expect("director", "PUT", "/api/academic/teaching-assignments/"+assignment+"/teacher", fmt.Sprintf(`{"teacher_id":%q}`, w.ids["s1"]), 422, "invalid_teacher")
		w.expect("teacher", "PUT", "/api/academic/teaching-assignments/"+assignment+"/teacher", fmt.Sprintf(`{"teacher_id":%q}`, w.ids["teacher2"]), 403, "forbidden")
		w.must("director", "PUT", "/api/academic/teaching-assignments/"+assignment+"/teacher", fmt.Sprintf(`{"teacher_id":%q}`, w.ids["teacher2"]), 200)
		w.expect("teacher", "GET", "/api/academic/teaching-assignments/"+assignment, "", 403, "forbidden")
		w.must("director", "PUT", "/api/academic/teaching-assignments/"+assignment+"/teacher", fmt.Sprintf(`{"teacher_id":%q}`, w.ids["teacher"]), 200)
		w.must("teacher", "GET", "/api/academic/teaching-assignments/"+assignment, "", 200)
		w.expect("director", "GET", "/api/academic/teaching-assignments/"+uuid.NewString(), "", 404, "teaching_assignment_not_found")
	})

	t.Run("schedules", func(t *testing.T) {
		path := "/api/academic/teaching-assignments/" + assignment + "/schedules"
		s := w.must("director", "POST", path, `{"day_of_week":1,"start_time":"08:00","end_time":"10:00","classroom":"Aula 3"}`, 201)
		if s["classroom"] != "Aula 3" || s["start_time"] != "08:00" {
			t.Fatalf("schedule = %v", s)
		}
		w.must("director", "POST", path, `{"day_of_week":3,"start_time":"08:00","end_time":"10:00"}`, 201)
		w.expect("director", "POST", path, `{"day_of_week":1,"start_time":"09:00","end_time":"11:00"}`, 409, "schedule_overlap")
		w.must("director", "POST", path, `{"day_of_week":1,"start_time":"10:00","end_time":"11:00"}`, 201) // back to back
		w.expect("director", "POST", path, `{"day_of_week":2,"start_time":"11:00","end_time":"10:00"}`, 422, "invalid_time_range")
		w.expect("director", "POST", path, `{"day_of_week":2,"start_time":"10:00","end_time":"10:00"}`, 422, "invalid_time_range")
		w.expect("director", "POST", path, `{"day_of_week":0,"start_time":"08:00","end_time":"09:00"}`, 422, "invalid_day_of_week")
		w.expect("director", "POST", path, `{"day_of_week":8,"start_time":"08:00","end_time":"09:00"}`, 422, "invalid_day_of_week")
		w.expect("director", "POST", path, `{"day_of_week":2,"start_time":"8am","end_time":"09:00"}`, 422, "invalid_time")
		w.expect("teacher", "POST", path, `{"day_of_week":4,"start_time":"08:00","end_time":"09:00"}`, 403, "forbidden")
		w.expect("director", "POST", "/api/academic/teaching-assignments/"+uuid.NewString()+"/schedules", `{"day_of_week":4,"start_time":"08:00","end_time":"09:00"}`, 404, "teaching_assignment_not_found")
		if w.count("teacher", path) != 3 || w.count("tutor", path) != 3 {
			t.Error("the assignment's teacher and the group's tutor should see its 3 slots")
		}
		w.expect("teacher2", "GET", path, "", 403, "forbidden")
		w.expect("tutor2", "GET", path, "", 403, "forbidden")
		w.expect("s3", "GET", path, "", 403, "forbidden")
	})

	t.Run("subject enrollments", func(t *testing.T) {
		w.expect("director", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q}`, w.ids["s3"], assignment), 422, "student_not_in_group")
		w.expect("director", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q}`, w.ids["teacher"], assignment), 422, "invalid_student")
		w.expect("director", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q}`, w.ids["s1"], uuid.NewString()), 422, "teaching_assignment_not_found")
		w.expect("director", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q,"attempt_number":2}`, w.ids["s1"], assignment), 422, "invalid_attempt_number")
		w.expect("teacher", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q}`, w.ids["s1"], assignment), 403, "forbidden")
		se := w.must("director", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q,"attempt_number":1}`, w.ids["s1"], assignment), 201)
		if se["attempt_number"] != float64(1) || se["status"] != "ACTIVE" {
			t.Fatalf("subject enrollment = %v", se)
		}
		w.expect("director", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q}`, w.ids["s1"], assignment), 409, "subject_enrollment_exists")

		// bulk: only s2 is still missing; running it again creates nothing
		path := "/api/academic/teaching-assignments/" + assignment + "/subject-enrollments"
		if got := w.must("director", "POST", path, "", 200); got["created"] != float64(1) {
			t.Fatalf("bulk created = %v, want 1", got)
		}
		if got := w.must("director", "POST", path, "", 200); got["created"] != float64(0) {
			t.Fatalf("bulk repeated created = %v, want 0", got)
		}
		w.expect("teacher", "POST", path, "", 403, "forbidden")
		if n := w.sqlInt(`SELECT count(*) FROM subject_enrollments`); n != 2 {
			t.Fatalf("subject enrollments in DB = %d, want 2", n)
		}
	})

	t.Run("each role only sees what it is related to", func(t *testing.T) {
		counts := []struct {
			who                            string
			groups, assignments, schedules int
		}{
			{"director", 1, 1, 3},
			{"teacher", 1, 1, 3},
			{"teacher2", 0, 0, 0},
			{"tutor", 1, 1, 3},
			{"tutor2", 0, 0, 0},
			{"s1", 1, 1, 3},
			{"s2", 1, 1, 3},
			{"s3", 0, 0, 0},
		}
		for _, c := range counts {
			if got := w.count(c.who, "/api/academic/groups"); got != c.groups {
				t.Errorf("%s sees %d groups, want %d", c.who, got, c.groups)
			}
			if got := w.count(c.who, "/api/academic/teaching-assignments"); got != c.assignments {
				t.Errorf("%s sees %d assignments, want %d", c.who, got, c.assignments)
			}
			if got := w.count(c.who, "/api/academic/schedules"); got != c.schedules {
				t.Errorf("%s sees %d schedule slots, want %d", c.who, got, c.schedules)
			}
		}
		for _, who := range []string{"admin"} {
			w.expect(who, "GET", "/api/academic/groups", "", 403, "forbidden")
			w.expect(who, "GET", "/api/academic/teaching-assignments", "", 403, "forbidden")
			w.expect(who, "GET", "/api/academic/schedules", "", 403, "forbidden")
			w.expect(who, "GET", "/api/academic/subject-enrollments", "", 403, "forbidden")
			w.expect(who, "GET", "/api/academic/groups/"+group, "", 403, "forbidden")
		}

		// direct access to a group / assignment that belongs to someone else
		for _, who := range []string{"teacher2", "tutor2", "s3"} {
			w.expect(who, "GET", "/api/academic/groups/"+group, "", 403, "forbidden")
			w.expect(who, "GET", "/api/academic/groups/"+group+"/teaching-assignments", "", 403, "forbidden")
			w.expect(who, "GET", "/api/academic/teaching-assignments/"+assignment, "", 403, "forbidden")
			w.expect(who, "GET", "/api/academic/teaching-assignments?group_id="+group, "", 403, "forbidden") // filtering by a foreign group is refused, not silently empty
		}
		for _, who := range []string{"teacher", "tutor", "s1", "director"} {
			w.must(who, "GET", "/api/academic/groups/"+group, "", 200)
			w.must(who, "GET", "/api/academic/teaching-assignments/"+assignment, "", 200)
		}
		w.expect("director", "GET", "/api/academic/groups/"+uuid.NewString(), "", 404, "group_not_found")
		w.expect("director", "GET", "/api/academic/groups/not-a-uuid", "", 404, "group_not_found")
		w.expect("director", "GET", "/api/academic/groups?academic_term_id=xyz", "", 422, "invalid_academic_term_id")
		if w.count("director", "/api/academic/groups?academic_term_id="+term) != 1 || w.count("director", "/api/academic/groups?academic_term_id="+uuid.NewString()) != 0 {
			t.Error("term filter")
		}

		// roster of an assignment: only its teacher (and the director)
		path := "/api/academic/teaching-assignments/" + assignment + "/students"
		if w.count("teacher", path) != 2 || w.count("director", path) != 2 {
			t.Error("teacher and director should list the 2 enrolled students")
		}
		for _, who := range []string{"teacher2", "tutor", "s1", "admin"} {
			w.expect(who, "GET", path, "", 403, "forbidden")
		}

		// subject enrollments: a student only ever gets their own, whatever the client sends
		own := w.must("s1", "GET", "/api/academic/subject-enrollments?student_id="+w.ids["s2"], "", 200)["data"].([]any)
		if len(own) != 1 || own[0].(map[string]any)["student_id"] != w.ids["s1"] {
			t.Errorf("s1 asked for s2 and got %v", own)
		}
		if w.count("s3", "/api/academic/subject-enrollments") != 0 {
			t.Error("s3 has no subject enrollments")
		}
		if w.count("teacher", "/api/academic/subject-enrollments") != 2 || w.count("teacher2", "/api/academic/subject-enrollments") != 0 {
			t.Error("teacher sees exactly the enrollments of the assignments they teach")
		}
		w.expect("tutor", "GET", "/api/academic/subject-enrollments", "", 403, "forbidden")
		if w.count("director", "/api/academic/subject-enrollments?student_id="+w.ids["s2"]) != 1 {
			t.Error("director may filter by student")
		}
		w.expect("director", "GET", "/api/academic/subject-enrollments?teaching_assignment_id=oops", "", 422, "invalid_teaching_assignment_id")
	})

	t.Run("closed term freezes the academic structure", func(t *testing.T) {
		w.must("director", "POST", "/api/academic/terms/"+term+"/close", "", 200)
		w.expect("director", "POST", "/api/academic/groups", fmt.Sprintf(`{"academic_term_id":%q,"career_id":%q,"code":"3A","level":2}`, term, career), 409, "term_closed")
		w.expect("director", "POST", "/api/academic/groups/"+group+"/enrollments", fmt.Sprintf(`{"student_id":%q}`, w.ids["s3"]), 409, "term_closed")
		w.expect("director", "PUT", "/api/academic/groups/"+group+"/tutor", fmt.Sprintf(`{"tutor_id":%q}`, w.ids["tutor2"]), 409, "term_closed")
		w.expect("director", "POST", "/api/academic/groups/"+group+"/teaching-assignments", fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, otherSubject, w.ids["teacher"]), 409, "term_closed")
		w.expect("director", "PUT", "/api/academic/teaching-assignments/"+assignment+"/teacher", fmt.Sprintf(`{"teacher_id":%q}`, w.ids["teacher2"]), 409, "term_closed")
		w.expect("director", "POST", "/api/academic/teaching-assignments/"+assignment+"/schedules", `{"day_of_week":5,"start_time":"08:00","end_time":"09:00"}`, 409, "term_closed")
		w.expect("director", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q}`, w.ids["s2"], assignment), 409, "term_closed")
		w.expect("director", "POST", "/api/academic/teaching-assignments/"+assignment+"/subject-enrollments", "", 409, "term_closed")
		// history stays readable
		w.must("s1", "GET", "/api/academic/groups/"+group, "", 200)
		if w.count("teacher", "/api/academic/schedules") != 3 {
			t.Error("closed term data must remain readable")
		}
	})

	t.Run("audit trail holds ids and never secrets", func(t *testing.T) {
		for _, action := range []string{"academic.group_created", "academic.group_tutor_changed", "academic.student_enrolled",
			"academic.teaching_assignment_created", "academic.teacher_changed", "academic.subject_enrollment_created",
			"academic.subject_enrollments_bulk_created", "academic.term_closed"} {
			if w.sqlInt(`SELECT count(*) FROM audit_logs WHERE action = $1 AND actor_user_id = $2`, action, w.ids["director"]) == 0 {
				t.Errorf("no audit row for %s by the director", action)
			}
		}
		if n := w.sqlInt(`SELECT count(*) FROM audit_logs WHERE metadata::text ILIKE '%password%' OR metadata::text ILIKE '%token%'`); n != 0 {
			t.Errorf("audit metadata mentions secrets in %d rows", n)
		}
	})
}

func TestAcademicRetakeGetsNextAttemptAcrossTerms(t *testing.T) {
	w := newWorld(t)
	career, subject, term1 := w.seedPlan()
	enroll := fmt.Sprintf(`{"student_id":%q}`, w.ids["s1"])
	g1 := idOf(w.must("director", "POST", "/api/academic/groups", fmt.Sprintf(`{"academic_term_id":%q,"career_id":%q,"code":"2A","level":2}`, term1, career), 201))
	w.must("director", "POST", "/api/academic/groups/"+g1+"/enrollments", enroll, 201)
	a1 := idOf(w.must("director", "POST", "/api/academic/groups/"+g1+"/teaching-assignments", fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, subject, w.ids["teacher"]), 201))
	w.must("director", "POST", "/api/academic/teaching-assignments/"+a1+"/subject-enrollments", "", 200)

	term2 := idOf(w.must("director", "POST", "/api/academic/terms", `{"name":"2026-2","start_date":"2026-05-04","end_date":"2026-08-28"}`, 201))
	g2 := idOf(w.must("director", "POST", "/api/academic/groups", fmt.Sprintf(`{"academic_term_id":%q,"career_id":%q,"code":"2A","level":2}`, term2, career), 201)) // same code, other term
	w.must("director", "POST", "/api/academic/groups/"+g2+"/enrollments", enroll, 201)
	a2 := idOf(w.must("director", "POST", "/api/academic/groups/"+g2+"/teaching-assignments", fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, subject, w.ids["teacher"]), 201))
	w.expect("director", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q,"attempt_number":1}`, w.ids["s1"], a2), 422, "invalid_attempt_number")
	w.must("director", "POST", "/api/academic/teaching-assignments/"+a2+"/subject-enrollments", "", 200)
	if got := w.sqlInt(`SELECT attempt_number FROM subject_enrollments WHERE teaching_assignment_id = $1`, a2); got != 2 {
		t.Fatalf("second attempt number = %d, want 2", got)
	}
}

// failingAudit makes every audit write fail, to prove the business change rolls back with it.
type failingAudit struct{}

func (failingAudit) Record(context.Context, auditdomain.Event) error {
	return errors.New("audit unavailable")
}

func TestAcademicWritesRollBackWithTheirAuditRow(t *testing.T) {
	w := newWorld(t)
	career, subject, term := w.seedPlan()
	g := idOf(w.must("director", "POST", "/api/academic/groups", fmt.Sprintf(`{"academic_term_id":%q,"career_id":%q,"code":"2A","level":2}`, term, career), 201))

	svc := acadapp.NewService(acadapp.Deps{
		Catalog: acadpg.NewCatalogRepository(w.db), Terms: acadpg.NewTermRepository(w.db), Groups: acadpg.NewGroupRepository(w.db),
		Assignments: acadpg.NewAssignmentRepository(w.db), SubjectEnrollments: acadpg.NewSubjectEnrollmentRepository(w.db),
		Users: acadpg.NewUserDirectory(w.db), Audit: failingAudit{}, Tx: w.db, NewID: uuid.New,
	})
	director := domain.Principal{UserID: uuid.MustParse(w.ids["director"]), SessionID: uuid.New(), Roles: []domain.Role{domain.RoleDirector}}
	ctx := context.Background()
	gid, tid, sid := uuid.MustParse(g), uuid.MustParse(term), uuid.MustParse(subject)

	if _, err := svc.CreateGroup(ctx, director, acadapp.CreateGroupInput{TermID: tid, CareerID: uuid.MustParse(career), Code: "3A", Level: 2}); err == nil {
		t.Fatal("CreateGroup must fail when its audit row cannot be written")
	}
	if n := w.sqlInt(`SELECT count(*) FROM groups WHERE code = '3A'`); n != 0 {
		t.Fatalf("group survived the rollback (%d rows)", n)
	}
	if _, err := svc.EnrollStudent(ctx, director, gid, uuid.MustParse(w.ids["s1"])); err == nil {
		t.Fatal("EnrollStudent must fail when its audit row cannot be written")
	}
	if n := w.sqlInt(`SELECT count(*) FROM enrollments`); n != 0 {
		t.Fatalf("enrollment survived the rollback (%d rows)", n)
	}
	if _, err := svc.CreateAssignment(ctx, director, gid, acadapp.CreateAssignmentInput{SubjectID: sid, TeacherID: uuid.MustParse(w.ids["teacher"])}); err == nil {
		t.Fatal("CreateAssignment must fail when its audit row cannot be written")
	}
	if n := w.sqlInt(`SELECT count(*) FROM teaching_assignments`); n != 0 {
		t.Fatalf("assignment survived the rollback (%d rows)", n)
	}
	if _, err := svc.CloseTerm(ctx, director, tid); err == nil {
		t.Fatal("CloseTerm must fail when its audit row cannot be written")
	}
	var status string
	_ = w.db.Q(ctx).QueryRow(ctx, `SELECT status FROM academic_terms WHERE id = $1`, tid).Scan(&status)
	if status != "active" {
		t.Fatalf("term status = %s after a rolled-back close, want active", status)
	}
}

func TestAcademicConcurrentWritesStayConsistent(t *testing.T) {
	w := newWorld(t)
	career, subject, term := w.seedPlan()
	group := idOf(w.must("director", "POST", "/api/academic/groups", fmt.Sprintf(`{"academic_term_id":%q,"career_id":%q,"code":"2A","level":2}`, term, career), 201))
	w.must("director", "POST", "/api/academic/groups/"+group+"/enrollments", fmt.Sprintf(`{"student_id":%q}`, w.ids["s1"]), 201)
	assignment := idOf(w.must("director", "POST", "/api/academic/groups/"+group+"/teaching-assignments", fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, subject, w.ids["teacher"]), 201))

	race := func(n int, f func() resp) (ok, conflict int) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := f()
				mu.Lock()
				defer mu.Unlock()
				switch r.status {
				case 201, 200:
					ok++
				case 409:
					conflict++
				default:
					t.Errorf("unexpected status %d: %s", r.status, r.body)
				}
			}()
		}
		wg.Wait()
		return
	}

	ok, conflict := race(8, func() resp {
		return w.do("director", "POST", "/api/academic/groups/"+group+"/enrollments", fmt.Sprintf(`{"student_id":%q}`, w.ids["s2"]))
	})
	if ok != 1 || conflict != 7 {
		t.Errorf("concurrent enrollments: %d created / %d conflicts, want 1 / 7", ok, conflict)
	}

	ok, conflict = race(8, func() resp {
		return w.do("director", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q}`, w.ids["s1"], assignment))
	})
	if ok != 1 || conflict != 7 {
		t.Errorf("concurrent subject enrollments: %d created / %d conflicts, want 1 / 7", ok, conflict)
	}
	if n := w.sqlInt(`SELECT count(*) FROM subject_enrollments WHERE student_id = $1`, w.ids["s1"]); n != 1 {
		t.Errorf("student has %d subject enrollments, want 1", n)
	}

	// the same overlapping slot submitted concurrently: exactly one wins
	ok, conflict = race(8, func() resp {
		return w.do("director", "POST", "/api/academic/teaching-assignments/"+assignment+"/schedules", `{"day_of_week":2,"start_time":"08:00","end_time":"10:00"}`)
	})
	if ok != 1 || conflict != 7 {
		t.Errorf("concurrent schedules: %d created / %d conflicts, want 1 / 7", ok, conflict)
	}

	// two groups racing for the same code in the same term
	ok, conflict = race(6, func() resp {
		return w.do("director", "POST", "/api/academic/groups", fmt.Sprintf(`{"academic_term_id":%q,"career_id":%q,"code":"9Z","level":2}`, term, career))
	})
	if ok != 1 || conflict != 5 {
		t.Errorf("concurrent groups: %d created / %d conflicts, want 1 / 5", ok, conflict)
	}
}

// A repeated HTTP request must never create another academic opportunity, whether
// it is sent again sequentially, with an explicit attempt_number, or concurrently
// with the bulk enrollment.
func TestAcademicSubjectEnrollmentIsIdempotent(t *testing.T) {
	w := newWorld(t)
	career, subject, term := w.seedPlan()
	group := idOf(w.must("director", "POST", "/api/academic/groups", fmt.Sprintf(`{"academic_term_id":%q,"career_id":%q,"code":"2A","level":2}`, term, career), 201))
	for _, s := range []string{"s1", "s2", "s3"} {
		w.must("director", "POST", "/api/academic/groups/"+group+"/enrollments", fmt.Sprintf(`{"student_id":%q}`, w.ids[s]), 201)
	}
	assignment := idOf(w.must("director", "POST", "/api/academic/groups/"+group+"/teaching-assignments", fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, subject, w.ids["teacher"]), 201))
	body := func(extra string) string {
		return fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q%s}`, w.ids["s1"], assignment, extra)
	}
	attempts := func(student string) []int {
		rows, err := w.db.Q(context.Background()).Query(context.Background(),
			`SELECT attempt_number FROM subject_enrollments WHERE student_id = $1 ORDER BY attempt_number`, student)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []int
		for rows.Next() {
			var n int
			_ = rows.Scan(&n)
			out = append(out, n)
		}
		return out
	}

	w.must("director", "POST", "/api/academic/subject-enrollments", body(""), 201)
	for _, extra := range []string{"", `,"attempt_number":1`, `,"attempt_number":2`, ""} {
		w.expect("director", "POST", "/api/academic/subject-enrollments", body(extra), 409, "subject_enrollment_exists")
	}
	if got := attempts(w.ids["s1"]); len(got) != 1 || got[0] != 1 {
		t.Fatalf("attempts for s1 after repeats = %v, want [1]", got)
	}

	// bulk is idempotent and does not add attempts for s1
	if got := w.must("director", "POST", "/api/academic/teaching-assignments/"+assignment+"/subject-enrollments", "", 200); got["created"] != float64(2) {
		t.Fatalf("bulk created = %v, want 2 (s2, s3)", got)
	}
	if got := w.must("director", "POST", "/api/academic/teaching-assignments/"+assignment+"/subject-enrollments", "", 200); got["created"] != float64(0) {
		t.Fatalf("bulk repeated created = %v, want 0", got)
	}

	// concurrent single + bulk for a student who has none yet
	w.db.Q(context.Background()).Exec(context.Background(), `DELETE FROM subject_enrollments WHERE student_id = $1`, w.ids["s3"])
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var r resp
			if i%2 == 0 {
				r = w.do("director", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q}`, w.ids["s3"], assignment))
			} else {
				r = w.do("director", "POST", "/api/academic/teaching-assignments/"+assignment+"/subject-enrollments", "")
			}
			if r.status != 200 && r.status != 201 && r.status != 409 {
				t.Errorf("unexpected status %d: %s", r.status, r.body)
			}
		}(i)
	}
	wg.Wait()
	if got := attempts(w.ids["s3"]); len(got) != 1 || got[0] != 1 {
		t.Fatalf("attempts for s3 after racing single+bulk = %v, want [1]", got)
	}

	// student_id is only honored for the director, after role and group membership checks
	w.expect("s1", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q}`, w.ids["s2"], assignment), 403, "forbidden")
	w.expect("teacher", "POST", "/api/academic/subject-enrollments", body(""), 403, "forbidden")
	w.expect("director", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q}`, w.ids["s4"], assignment), 422, "student_not_in_group")
	w.expect("director", "POST", "/api/academic/subject-enrollments", fmt.Sprintf(`{"student_id":%q,"teaching_assignment_id":%q}`, w.ids["tutor"], assignment), 422, "invalid_student")

	// every subject enrollment belongs to a student enrolled in the group of its assignment
	if n := w.sqlInt(`SELECT count(*) FROM subject_enrollments se JOIN teaching_assignments ta ON ta.id = se.teaching_assignment_id
		WHERE NOT EXISTS (SELECT 1 FROM enrollments e WHERE e.group_id = ta.group_id AND e.student_id = se.student_id)`); n != 0 {
		t.Fatalf("%d subject enrollments point to a student outside the assignment's group", n)
	}
	if n := w.sqlInt(`SELECT count(*) FROM subject_enrollments WHERE created_at IS NULL`); n != 0 {
		t.Fatal("created_at must always be set")
	}
}
