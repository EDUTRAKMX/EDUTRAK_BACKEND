package app_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/google/uuid"
)

const specPath = "../../api/openapi.yaml"

func loadSpec(t *testing.T) (*openapi3.T, routers.Router) {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(specPath)
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("OpenAPI document is not valid: %v", err)
	}
	doc.Servers = openapi3.Servers{{URL: "/"}}
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	return doc, router
}

var paramRe = regexp.MustCompile(`\{[^}]*\}`)

func normalize(path string) string { return paramRe.ReplaceAllString(path, "{}") }

// routesInSource extracts "METHOD /path" from the handlers that register routes.
func routesInSource(t *testing.T) map[string]bool {
	t.Helper()
	files, _ := filepath.Glob("../*/adapters/http/handler.go")
	files = append(files, "app.go")
	baseRe := regexp.MustCompile(`const base = "([^"]*)"`)
	routeRe := regexp.MustCompile(`(?:route|mux\.Handle(?:Func)?)\("([A-Z]+) ([^"]*)"(\s*\+\s*base)?(?:\s*\+\s*"([^"]*)")?`)
	out := map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := strings.ReplaceAll(string(raw), "\r\n", "\n")
		base := ""
		if m := baseRe.FindStringSubmatch(src); m != nil {
			base = m[1]
		}
		for _, m := range routeRe.FindAllStringSubmatch(src, -1) {
			path := m[2]
			if m[3] != "" {
				path += base
			}
			path += m[4]
			if !strings.HasPrefix(path, "/") {
				continue
			}
			out[m[1]+" "+normalize(path)] = true
		}
	}
	return out
}

func TestOpenAPISpecIsValidAndMatchesTheRoutes(t *testing.T) {
	doc, _ := loadSpec(t)
	inSpec := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			inSpec[method+" "+normalize(path)] = true
		}
	}
	inCode := routesInSource(t)
	if len(inCode) < 60 {
		t.Fatalf("route extraction looks broken: only %d routes found", len(inCode))
	}
	var missingDoc, missingCode []string
	for r := range inCode {
		if !inSpec[r] {
			missingDoc = append(missingDoc, r)
		}
	}
	for r := range inSpec {
		if !inCode[r] {
			missingCode = append(missingCode, r)
		}
	}
	sort.Strings(missingDoc)
	sort.Strings(missingCode)
	if len(missingDoc) > 0 {
		t.Errorf("routes in the code but not in api/openapi.yaml:\n  %s", strings.Join(missingDoc, "\n  "))
	}
	if len(missingCode) > 0 {
		t.Errorf("operations documented but not registered in the code:\n  %s", strings.Join(missingCode, "\n  "))
	}
}

// Every documented operation exists and (except the public ones) refuses a request without a
// valid token, so no route can sidestep authentication unnoticed.
func TestEveryDocumentedOperationRequiresAuthentication(t *testing.T) {
	doc, _ := loadSpec(t)
	w := newWorld(t)
	public := map[string]bool{
		"GET /healthz": true, "GET /readyz": true, "POST /api/auth/login": true, "POST /api/auth/refresh": true,
	}
	id := uuid.NewString()
	n := 0
	for path, item := range doc.Paths.Map() {
		real := paramRe.ReplaceAllString(path, id)
		for method := range item.Operations() {
			if public[method+" "+path] {
				continue
			}
			for _, token := range []string{"", "garbage.token.value"} {
				r := w.c.do(method, real, token, `{}`)
				if r.status != 401 {
					t.Errorf("%s %s with token %q = %d, want 401 (%s)", method, path, token, r.status, r.body)
				}
				n++
			}
		}
	}
	if n < 100 {
		t.Fatalf("only %d requests were checked", n)
	}
}

// contract performs a real request through the handler and validates both the request and the
// response against api/openapi.yaml.
type contract struct {
	t      *testing.T
	w      *world
	router routers.Router
}

func (c *contract) call(who, method, path, body string, wantStatus int, hdr ...string) map[string]any {
	c.t.Helper()
	token := c.w.token[who]
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:4321"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	c.w.c.h.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		c.t.Fatalf("%s %s as %s = %d, want %d: %s", method, path, who, rec.Code, wantStatus, rec.Body.String())
	}

	// Validate against the spec using a fresh copy of the request (the handler consumed the body).
	vreq := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	for k, v := range req.Header {
		vreq.Header[k] = v
	}
	route, params, err := c.router.FindRoute(vreq)
	if err != nil {
		c.t.Fatalf("%s %s is not documented: %v", method, path, err)
	}
	in := &openapi3filter.RequestValidationInput{
		Request: vreq, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}
	if wantStatus < 400 {
		if err := openapi3filter.ValidateRequest(context.Background(), in); err != nil {
			c.t.Errorf("%s %s: request does not match the spec: %v", method, path, err)
		}
	}
	rin := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in, Status: rec.Code, Header: rec.Header(),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}
	rin.SetBodyBytes(rec.Body.Bytes())
	if err := openapi3filter.ValidateResponse(context.Background(), rin); err != nil {
		c.t.Errorf("%s %s: response does not match the spec: %v\nbody: %s", method, path, err, truncate(rec.Body.String(), 600))
	}
	if rec.Body.Len() == 0 {
		return nil
	}
	return resp{rec.Code, rec.Body.String()}.json(c.t)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// Real responses of every module validate against the documented schemas (types, formats,
// nullability, enums), so the examples and the contract cannot drift from the DTOs unnoticed.
func TestRealResponsesMatchTheOpenAPISchemas(t *testing.T) {
	_, router := loadSpec(t)
	f := newAFx(t) // groups 2A/2B, grades setup, absences, requests, tutoring and seeded attendance
	w := f.w
	c := &contract{t: t, w: w, router: router}
	s1 := w.ids["s1"]

	// Identity
	login := w.c.do("POST", "/api/auth/login", "", `{"institutional_number":"40000001","password":"`+acadPass+`"}`)
	{
		req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"institutional_number":"40000001","password":"`+acadPass+`"}`))
		route, params, err := router.FindRoute(req)
		if err != nil {
			t.Fatal(err)
		}
		rin := &openapi3filter.ResponseValidationInput{
			RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
			Status:                 login.status, Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}},
			Options: &openapi3filter.Options{IncludeResponseStatus: true},
		}
		rin.SetBodyBytes([]byte(login.body))
		if err := openapi3filter.ValidateResponse(context.Background(), rin); err != nil {
			t.Errorf("login response: %v", err)
		}
	}
	c.call("s1", "GET", "/api/auth/me", "", 200)
	c.call("admin", "POST", "/api/admin/users", `{"institutional_number":"40000099","first_name":"Alumno","last_name":"Contrato","password":"temporary-passphrase-123","roles":["STUDENT"]}`, 201)

	// Academic
	career := f.career
	subject := w.sqlString(`SELECT id::text FROM subjects WHERE code = 'MAT1'`)
	c.call("s1", "GET", "/api/academic/careers", "", 200)
	c.call("s1", "GET", "/api/academic/careers/"+career, "", 200)
	c.call("s1", "GET", "/api/academic/careers/"+career+"/curriculum?level=2", "", 200)
	c.call("s1", "GET", "/api/academic/subjects", "", 200)
	c.call("s1", "GET", "/api/academic/subjects/"+subject, "", 200)
	c.call("s1", "GET", "/api/academic/subjects/"+subject+"/units", "", 200)
	c.call("director", "GET", "/api/academic/terms", "", 200)
	c.call("director", "GET", "/api/academic/terms/"+f.term, "", 200)
	c.call("director", "GET", "/api/academic/groups?academic_term_id="+f.term, "", 200)
	c.call("tutor", "GET", "/api/academic/groups/"+f.group, "", 200)
	c.call("tutor", "GET", "/api/academic/groups/"+f.group+"/enrollments", "", 200)
	c.call("teacher", "GET", "/api/academic/groups/"+f.group+"/teaching-assignments", "", 200)
	c.call("teacher", "GET", "/api/academic/teaching-assignments", "", 200)
	c.call("teacher", "GET", "/api/academic/teaching-assignments/"+f.assignment, "", 200)
	c.call("teacher", "GET", "/api/academic/teaching-assignments/"+f.assignment+"/students", "", 200)
	c.call("director", "POST", "/api/academic/teaching-assignments/"+f.assignment+"/schedules", `{"day_of_week":1,"start_time":"08:00","end_time":"10:00","classroom":"Aula 3"}`, 201)
	c.call("teacher", "GET", "/api/academic/teaching-assignments/"+f.assignment+"/schedules", "", 200)
	c.call("teacher", "GET", "/api/academic/schedules", "", 200)
	c.call("s1", "GET", "/api/academic/subject-enrollments", "", 200)
	c.call("director", "PUT", "/api/academic/groups/"+f.group+"/tutor", fmt.Sprintf(`{"tutor_id":%q}`, w.ids["tutor"]), 200)

	// Grades
	g := c.call("teacher", "POST", "/api/grades", f.body(f.se1, f.units[0], "76.5"), 201)
	gid := idOf(g)
	c.call("s1", "GET", "/api/grades/"+gid, "", 200)
	c.call("teacher", "PATCH", "/api/grades/"+gid, `{"score":91}`, 200)
	c.call("s1", "GET", "/api/students/"+s1+"/grades", "", 200)
	c.call("teacher", "GET", "/api/teaching-assignments/"+f.assignment+"/grades", "", 200)

	// Attendance (the fixture leaves a seeded OPEN session: close it, only one may be open)
	c.call("teacher", "POST", att+"/sessions/"+w.sqlString(`SELECT id::text FROM attendance_sessions WHERE status = 'OPEN' AND teaching_assignment_id = $1`, f.assignment)+"/close", "", 200)
	session := f.startSession(t)
	c.call("teacher", "GET", att+"/sessions/"+session, "", 200)
	tok := c.call("teacher", "POST", att+"/sessions/"+session+"/qr/rotate", "", 200)["token"].(string)
	c.call("s1", "POST", att+"/sessions/"+session+"/check-in", fmt.Sprintf(`{"token":%q}`, tok), 201)
	c.call("teacher", "POST", att+"/sessions/"+session+"/manual", fmt.Sprintf(`{"subject_enrollment_id":%q,"status":"LATE"}`, f.se2), 201)
	rec := w.sqlString(`SELECT id::text FROM attendance_records WHERE attendance_session_id = $1 AND subject_enrollment_id = $2`, session, f.se2)
	c.call("teacher", "PATCH", att+"/records/"+rec, `{"status":"PRESENT"}`, 200)
	c.call("teacher", "POST", att+"/sessions/"+session+"/close", "", 200)
	c.call("tutor", "GET", att+"/teaching-assignments/"+f.assignment, "", 200)
	c.call("s1", "GET", att+"/students/"+s1, "", 200)

	// Justifications (JSON and multipart)
	body, ct := multipartBody(part{field: "attendance_record_id", data: []byte(f.rec1)}, part{field: "reason", data: []byte("Consulta médica")},
		part{field: "evidence", filename: "constancia.pdf", data: pdfFile})
	jr := c.call("s1", "POST", jx, body, 201, "Content-Type", ct)
	jid := idOf(jr)
	evID := jr["evidence"].([]any)[0].(map[string]any)["id"].(string)
	c.call("s1", "GET", jx+"?status=PENDING_TUTOR", "", 200)
	c.call("tutor", "GET", jx+"/"+jid, "", 200)
	c.call("tutor", "POST", jx+"/"+jid+"/tutor-decision", `{"decision":"APPROVED"}`, 200)
	dl := w.c.do("GET", jx+"/"+jid+"/evidence/"+evID, w.token["teacher"], "")
	if dl.status != 200 {
		t.Fatalf("evidence download: %d", dl.status)
	}
	c.call("teacher", "POST", jx+"/"+jid+"/teacher-decision", `{"decision":"APPROVED"}`, 200)
	jr2 := c.call("s2", "POST", jx, fmt.Sprintf(`{"attendance_record_id":%q,"reason":"Trámite"}`, f.rec2), 201)
	c.call("tutor", "POST", jx+"/"+idOf(jr2)+"/tutor-decision", `{"decision":"REJECTED","comment":"Sin comprobante"}`, 200)

	// Tutoring
	fu := c.call("tutor", "POST", tx+"/follow-ups", fmt.Sprintf(`{"student_id":%q,"group_id":%q,"note":"seguimiento de contrato"}`, s1, f.group), 201)
	c.call("tutor", "GET", tx+"/follow-ups/"+idOf(fu), "", 200)
	c.call("tutor", "GET", tx+"/follow-ups?student_id="+s1, "", 200)
	c.call("tutor", "GET", tx+"/students", "", 200)
	c.call("tutor", "GET", tx+"/students/"+s1+"?group_id="+f.group, "", 200)
	c.call("tutor", "GET", tx+"/students/"+s1+"/follow-ups?limit=10", "", 200)

	// Analytics
	c.call("tutor", "POST", ax+"/alerts/recalculate", fmt.Sprintf(`{"group_id":%q}`, f.group), 200)
	c.call("tutor", "GET", ax+"/students/"+s1+"?group_id="+f.group, "", 200)
	c.call("tutor", "GET", ax+"/groups/"+f.group, "", 200)
	c.call("director", "GET", ax+"/overview", "", 200)
	alerts := c.call("tutor", "GET", ax+"/alerts?status=ALL", "", 200)
	list, _ := alerts["data"].([]any)
	if len(list) == 0 {
		t.Fatal("expected at least one alert to validate")
	}
	c.call("tutor", "GET", ax+"/alerts/"+idOf(list[0].(map[string]any)), "", 200)

	// Errors keep the documented envelope.
	c.call("s1", "GET", "/api/academic/terms", "", 403)
	c.call("director", "GET", "/api/academic/terms/"+uuid.NewString(), "", 404)
	c.call("teacher", "POST", "/api/grades", f.body(f.se1, f.units[0], "50"), 409)
	c.call("teacher", "POST", "/api/grades", `{"score":1}`, 422)

	// Unknown routes and methods answer JSON too.
	r := w.c.do("GET", "/api/nope", w.token["s1"], "")
	if r.status != 404 || r.errCode(t) != "not_found" {
		t.Errorf("unknown route: %d %s", r.status, r.body)
	}
	r = w.c.do("DELETE", "/api/auth/me", w.token["s1"], "")
	if r.status != 405 || r.errCode(t) != "method_not_allowed" {
		t.Errorf("wrong method: %d %s", r.status, r.body)
	}
	_ = io.Discard
}
