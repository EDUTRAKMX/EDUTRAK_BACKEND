package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/edutrak/backend/internal/app"
	"github.com/edutrak/backend/internal/identity/application"
	"github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/config"
	"github.com/edutrak/backend/internal/platform/database"
)

// Demo data is entirely FICTITIOUS. It is created through the real API of the application (so every
// rule, audit event and constraint applies), never by raw SQL, and only ever ADDS rows: it does not
// delete or reset anything. It refuses to run when APP_ENV=production.
const (
	demoDirector = "90000001"
	demoTutor1   = "91000001"
	demoTutor2   = "91000002"
	demoTeacher1 = "92000001"
	demoTeacher2 = "92000002"
)

var demoStudents = []struct{ number, first, last string }{
	{"93000001", "Ana", "Demostración"}, {"93000002", "Beto", "Demostración"}, {"93000003", "Carla", "Demostración"},
	{"93000004", "Diego", "Demostración"}, {"93000005", "Elena", "Demostración"}, {"93000006", "Fabián", "Demostración"},
}

type demoClient struct {
	h     http.Handler
	token map[string]string // institutional number -> access token
	ids   map[string]string // institutional number -> user id
}

func (c *demoClient) do(who, method, path, body string) (map[string]any, error) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if t := c.token[who]; t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	if rec.Code >= 300 {
		return nil, fmt.Errorf("%s %s as %s: %d %s", method, path, who, rec.Code, rec.Body.String())
	}
	var m map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
	}
	return m, nil
}

// must records the first error and turns later calls into no-ops, keeping the script readable.
type steps struct {
	c   *demoClient
	err error
}

func (s *steps) call(who, method, path, body string) map[string]any {
	if s.err != nil {
		return nil
	}
	m, err := s.c.do(who, method, path, body)
	s.err = err
	return m
}

func str(m map[string]any, k string) string { v, _ := m[k].(string); return v }

func runDemo(ctx context.Context, cfg config.Config, db *database.DB) error {
	if cfg.Env == "production" {
		return errors.New("refusing to create demo data when APP_ENV=production")
	}
	password := os.Getenv("DEMO_PASSWORD")
	if password == "" {
		password = "demo-passphrase-2026" // fictitious; override with DEMO_PASSWORD
	}
	idsvc, err := app.NewIdentityService(db, cfg)
	if err != nil {
		return err
	}
	c := &demoClient{token: map[string]string{}, ids: map[string]string{}}

	type acct struct {
		number, first, last string
		role                domain.Role
	}
	accounts := []acct{
		{demoDirector, "Dirección", "Demostración", domain.RoleDirector},
		{demoTutor1, "Tutora", "Demostración Uno", domain.RoleTutor}, {demoTutor2, "Tutor", "Demostración Dos", domain.RoleTutor},
		{demoTeacher1, "Profesora", "Demostración Uno", domain.RoleTeacher}, {demoTeacher2, "Profesor", "Demostración Dos", domain.RoleTeacher},
	}
	for _, s := range demoStudents {
		accounts = append(accounts, acct{s.number, s.first, s.last, domain.RoleStudent})
	}
	created := 0
	for _, a := range accounts {
		u, err := idsvc.BootstrapUser(ctx, application.CreateUserInput{
			InstitutionalNumber: a.number, FirstName: a.first, LastName: a.last, Password: password, Roles: []domain.Role{a.role},
		})
		switch {
		case errors.Is(err, domain.ErrDuplicateUser):
			continue
		case err != nil:
			return err
		}
		c.ids[a.number] = u.ID.String()
		created++
	}
	if created == 0 {
		fmt.Println("demo data already present; nothing to do")
		return nil
	}
	if created != len(accounts) {
		return errors.New("demo accounts are partially present; refusing to continue (it never deletes data: use a fresh database)")
	}

	h, err := app.NewHTTPHandler(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	c.h = h
	for _, a := range accounts {
		m, err := c.do("", "POST", "/api/auth/login", fmt.Sprintf(`{"institutional_number":%q,"password":%q}`, a.number, password))
		if err != nil {
			return err
		}
		c.token[a.number] = str(m, "access_token")
	}

	s := &steps{c: c}
	d := demoDirector
	career := str(s.call(d, "POST", "/api/academic/careers", `{"code":"DEMO-TIC","name":"Tecnologías de la Información (demo)"}`), "id")
	mat := str(s.call(d, "POST", "/api/academic/subjects", `{"code":"MAT1","name":"Matemáticas I (demo)"}`), "id")
	pro := str(s.call(d, "POST", "/api/academic/subjects", `{"code":"PRO1","name":"Programación I (demo)"}`), "id")
	for _, sub := range []string{mat, pro} {
		for n := 1; n <= 4; n++ {
			s.call(d, "POST", "/api/academic/subjects/"+sub+"/units", fmt.Sprintf(`{"unit_number":%d,"name":"Unidad %d"}`, n, n))
		}
		s.call(d, "POST", "/api/academic/careers/"+career+"/curriculum", fmt.Sprintf(`{"subject_id":%q,"level":2}`, sub))
	}
	term := str(s.call(d, "POST", "/api/academic/terms", `{"name":"DEMO-2026-1","start_date":"2026-01-05","end_date":"2026-12-18"}`), "id")
	s.call(d, "POST", "/api/academic/terms/"+term+"/activate", "")
	g2a := str(s.call(d, "POST", "/api/academic/groups", fmt.Sprintf(`{"academic_term_id":%q,"career_id":%q,"code":"2A","level":2,"tutor_id":%q}`, term, career, c.ids[demoTutor1])), "id")
	g2b := str(s.call(d, "POST", "/api/academic/groups", fmt.Sprintf(`{"academic_term_id":%q,"career_id":%q,"code":"2B","level":2,"tutor_id":%q}`, term, career, c.ids[demoTutor2])), "id")
	for i, st := range demoStudents {
		g := g2a
		if i == 5 {
			g = g2b
		}
		s.call(d, "POST", "/api/academic/groups/"+g+"/enrollments", fmt.Sprintf(`{"student_id":%q}`, c.ids[st.number]))
	}
	matA := str(s.call(d, "POST", "/api/academic/groups/"+g2a+"/teaching-assignments", fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, mat, c.ids[demoTeacher1])), "id")
	proA := str(s.call(d, "POST", "/api/academic/groups/"+g2a+"/teaching-assignments", fmt.Sprintf(`{"subject_id":%q,"teacher_id":%q}`, pro, c.ids[demoTeacher2])), "id")
	for _, a := range []string{matA, proA} {
		s.call(d, "POST", "/api/academic/teaching-assignments/"+a+"/subject-enrollments", "")
	}
	s.call(d, "POST", "/api/academic/teaching-assignments/"+matA+"/schedules", `{"day_of_week":1,"start_time":"08:00","end_time":"10:00","classroom":"Aula 3"}`)
	if s.err != nil {
		return s.err
	}

	// Subject enrollments of MAT1, by student number.
	enrollments := map[string]string{}
	m := s.call(d, "GET", "/api/academic/teaching-assignments/"+matA+"/students", "")
	if s.err != nil {
		return s.err
	}
	byUser := map[string]string{}
	for k, v := range c.ids {
		byUser[v] = k
	}
	for _, it := range m["data"].([]any) {
		e := it.(map[string]any)
		enrollments[byUser[str(e, "student_id")]] = str(e, "id")
	}

	// Grades (MAT1, teacher 1): Ana/Beto fine, Carla with a failing unit (NA), Diego partial.
	units := s.call(d, "GET", "/api/academic/subjects/"+mat+"/units", "")
	if s.err != nil {
		return s.err
	}
	var unitIDs []string
	for _, it := range units["data"].([]any) {
		unitIDs = append(unitIDs, str(it.(map[string]any), "id"))
	}
	scores := map[string][]string{
		"93000001": {"95", "100", "92.5", "98"}, "93000002": {"82", "88.75", "85"}, "93000003": {"62.5", "78", "81"},
		"93000004": {"90"}, "93000005": {"70", "84"},
	}
	for num, list := range scores {
		for i, sc := range list {
			s.call(demoTeacher1, "POST", "/api/grades", fmt.Sprintf(`{"subject_enrollment_id":%q,"subject_unit_id":%q,"score":%s}`, enrollments[num], unitIDs[i], sc))
		}
	}

	// Attendance (MAT1): ten closed sessions. P present, L late, A absent (left to the close).
	pattern := map[string]string{
		"93000001": "PPPPPPPPPP", // perfect
		"93000002": "PPAPPAPPAP", // 3 unjustified absences -> UNJUSTIFIED_ABSENCES
		"93000003": "PPPPPAAAAP", // sharp drop in the second half -> ATTENDANCE_DROP
		"93000004": "PLPLLPPLPP", // lates
		"93000005": "PPPPPPPPPA",
	}
	for i := 0; i < 10 && s.err == nil; i++ {
		sess := str(s.call(demoTeacher1, "POST", "/api/attendance/sessions", fmt.Sprintf(`{"teaching_assignment_id":%q}`, matA)), "id")
		for num, p := range pattern {
			switch p[i] {
			case 'P':
				s.call(demoTeacher1, "POST", "/api/attendance/sessions/"+sess+"/manual", fmt.Sprintf(`{"subject_enrollment_id":%q,"status":"PRESENT"}`, enrollments[num]))
			case 'L':
				s.call(demoTeacher1, "POST", "/api/attendance/sessions/"+sess+"/manual", fmt.Sprintf(`{"subject_enrollment_id":%q,"status":"LATE"}`, enrollments[num]))
			}
		}
		s.call(demoTeacher1, "POST", "/api/attendance/sessions/"+sess+"/close", "")
	}
	if s.err != nil {
		return s.err
	}

	// Justifications: Beto's absence is fully approved (tutor then teacher); Carla's stays pending.
	absence := func(num string) string {
		m := s.call(d, "GET", "/api/attendance/students/"+c.ids[num], "")
		if s.err != nil {
			return ""
		}
		for _, row := range m["data"].([]any) {
			for _, r := range row.(map[string]any)["records"].([]any) {
				if rec := r.(map[string]any); str(rec, "status") == "ABSENT" {
					return str(rec, "id")
				}
			}
		}
		s.err = fmt.Errorf("no absence found for %s", num)
		return ""
	}
	if rec := absence("93000002"); rec != "" {
		j := str(s.call("93000002", "POST", "/api/justifications", fmt.Sprintf(`{"attendance_record_id":%q,"reason":"Cita médica (demo)"}`, rec)), "id")
		s.call(demoTutor1, "POST", "/api/justifications/"+j+"/tutor-decision", `{"decision":"APPROVED","comment":"Comprobante revisado"}`)
		s.call(demoTeacher1, "POST", "/api/justifications/"+j+"/teacher-decision", `{"decision":"APPROVED"}`)
	}
	if rec := absence("93000003"); rec != "" {
		s.call("93000003", "POST", "/api/justifications", fmt.Sprintf(`{"attendance_record_id":%q,"reason":"Trámite familiar (demo)"}`, rec))
	}

	// Tutoring and alerts.
	s.call(demoTutor1, "POST", "/api/tutoring/follow-ups", fmt.Sprintf(`{"student_id":%q,"group_id":%q,"note":"Se acordó revisar asistencia y Matemáticas (demo)"}`, c.ids["93000003"], g2a))
	s.call(demoTutor1, "POST", "/api/tutoring/follow-ups", fmt.Sprintf(`{"student_id":%q,"group_id":%q,"note":"Seguimiento general (demo)"}`, c.ids["93000002"], g2a))
	for _, g := range []struct{ who, id string }{{demoTutor1, g2a}, {demoTutor2, g2b}} {
		s.call(g.who, "POST", "/api/analytics/alerts/recalculate", fmt.Sprintf(`{"group_id":%q}`, g.id))
	}
	if s.err != nil {
		return fmt.Errorf("demo data is incomplete: %w", s.err)
	}

	fmt.Println("demo data created (all fictitious). Accounts, password from DEMO_PASSWORD or the documented default:")
	for _, a := range accounts {
		fmt.Printf("  %-9s %s  %s %s\n", a.role, a.number, a.first, a.last)
	}
	return nil
}
