package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/edutrak/backend/internal/app"
	"github.com/edutrak/backend/internal/identity/application"
	"github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/config"
	"github.com/edutrak/backend/internal/platform/database"
	"github.com/edutrak/backend/internal/platform/database/dbtest"
)

// All accounts below are fictitious.
const (
	adminPass   = "admin-passphrase-123"
	studentPass = "student-passphrase-123"
	tempPass    = "temporary-passphrase-123"
	newPass     = "brand-new-passphrase-123"
)

type client struct {
	t *testing.T
	h http.Handler
}

type resp struct {
	status int
	body   string
}

func (c client) do(method, path, token, body string, hdr ...string) resp {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:4321"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return resp{rec.Code, rec.Body.String()}
}

func mustExecSQL(t *testing.T, db *database.DB, sql string) {
	t.Helper()
	if _, err := db.Q(context.Background()).Exec(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("response is not JSON: %q", r.body)
	}
	return m
}

func (r resp) errCode(t *testing.T) string {
	t.Helper()
	e, _ := r.json(t)["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestAPIEndToEndOnPostgres(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	cfg := config.Config{
		JWTSecret:       []byte("0123456789abcdef0123456789abcdef"),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: time.Hour,

		LoginMaxFailuresPerAccount: 5,
		LoginMaxFailuresPerIP:      150,
		LoginRateWindow:            15 * time.Minute,
	}
	svc, err := app.NewIdentityService(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []application.CreateUserInput{
		{InstitutionalNumber: "10000001", FirstName: "Admin", LastName: "Prueba", Password: adminPass, Roles: []domain.Role{domain.RoleAdmin}},
		{InstitutionalNumber: "40000001", FirstName: "Alumno", LastName: "Prueba", Password: studentPass, Roles: []domain.Role{domain.RoleStudent}},
	} {
		if _, err := svc.BootstrapUser(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	h, err := app.NewHTTPHandler(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c := client{t, h}
	login := func(number, pass string) resp {
		return c.do("POST", "/api/auth/login", "", `{"institutional_number":"`+number+`","password":"`+pass+`"}`)
	}
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }

	t.Run("health", func(t *testing.T) {
		if r := c.do("GET", "/healthz", "", ""); r.status != 200 {
			t.Fatalf("healthz %d", r.status)
		}
		if r := c.do("GET", "/readyz", "", ""); r.status != 200 {
			t.Fatalf("readyz %d", r.status)
		}
	})

	t.Run("login does not reveal whether the account exists", func(t *testing.T) {
		unknown := login("99999999", "whatever-password")
		wrong := login("10000001", "wrong-password-123")
		if unknown.status != 401 || wrong.status != 401 {
			t.Fatalf("statuses %d / %d, want 401", unknown.status, wrong.status)
		}
		strip := func(r resp) string { // request_id differs per request
			m := r.json(t)["error"].(map[string]any)
			delete(m, "request_id")
			b, _ := json.Marshal(m)
			return string(b)
		}
		if strip(unknown) != strip(wrong) {
			t.Fatalf("distinguishable responses:\n%s\n%s", unknown.body, wrong.body)
		}
	})

	var adminAccess, adminRefresh string
	t.Run("admin login: no secrets in response", func(t *testing.T) {
		r := login("10000001", adminPass)
		if r.status != 200 {
			t.Fatalf("login %d %s", r.status, r.body)
		}
		for _, banned := range []string{"password_hash", "$argon2", adminPass} {
			if strings.Contains(r.body, banned) {
				t.Fatalf("response leaks %q", banned)
			}
		}
		m := r.json(t)
		adminAccess, adminRefresh = str(m, "access_token"), str(m, "refresh_token")
		if adminAccess == "" || adminRefresh == "" {
			t.Fatal("missing tokens")
		}
		if me := c.do("GET", "/api/auth/me", adminAccess, ""); me.status != 200 || strings.Contains(me.body, "password_hash") || strings.Contains(me.body, "$argon2") {
			t.Fatalf("me %d %s", me.status, me.body)
		}
	})

	t.Run("client-supplied ip is rejected and X-Forwarded-For is ignored without trusted proxies", func(t *testing.T) {
		if r := c.do("POST", "/api/auth/login", "", `{"institutional_number":"40000001","password":"`+studentPass+`","ip":"10.9.9.9"}`); r.status != 400 {
			t.Fatalf("body with ip field: %d, want 400", r.status)
		}
		if r := c.do("POST", "/api/auth/login", "", `{"institutional_number":"40000001","password":"`+studentPass+`"}`, "X-Forwarded-For", "10.9.9.9"); r.status != 200 {
			t.Fatalf("login %d", r.status)
		}
		var ip string
		err := db.Q(ctx).QueryRow(ctx,
			`SELECT host(ip_address) FROM auth_sessions s JOIN users u ON u.id = s.user_id
			 WHERE u.institutional_number = '40000001' ORDER BY s.created_at DESC LIMIT 1`).Scan(&ip)
		if err != nil || ip != "192.0.2.10" {
			t.Fatalf("stored ip = %q (err %v), want the real peer 192.0.2.10", ip, err)
		}
	})

	var studentAccess string
	t.Run("student cannot use admin endpoints; unauthenticated gets 401", func(t *testing.T) {
		studentAccess = str(login("40000001", studentPass).json(t), "access_token")
		if r := c.do("POST", "/api/admin/users", studentAccess, `{}`); r.status != 403 {
			t.Fatalf("student create user: %d", r.status)
		}
		if r := c.do("POST", "/api/admin/users", "", `{}`); r.status != 401 {
			t.Fatalf("no token: %d", r.status)
		}
		if r := c.do("POST", "/api/admin/users", "garbage.token.value", `{}`); r.status != 401 {
			t.Fatalf("bad token: %d", r.status)
		}
	})

	t.Run("admin creates user over HTTP", func(t *testing.T) {
		r := c.do("POST", "/api/admin/users", adminAccess,
			`{"institutional_number":"20000001","first_name":"Ana","last_name":"Prueba","password":"`+tempPass+`","roles":["TEACHER","TUTOR"]}`)
		if r.status != 201 {
			t.Fatalf("create %d %s", r.status, r.body)
		}
		if strings.Contains(r.body, tempPass) || strings.Contains(r.body, "$argon2") {
			t.Fatal("create-user response leaks credentials")
		}
		if dup := c.do("POST", "/api/admin/users", adminAccess,
			`{"institutional_number":"20000001","first_name":"Ana","last_name":"Prueba","password":"`+tempPass+`","roles":["TEACHER"]}`); dup.status != 409 {
			t.Fatalf("duplicate: %d", dup.status)
		}
	})

	t.Run("admin reset forces password change and kills sessions", func(t *testing.T) {
		before := login("40000001", studentPass)
		studentRefresh := str(before.json(t), "refresh_token")
		var studentID string
		if err := db.Q(ctx).QueryRow(ctx, `SELECT id::text FROM users WHERE institutional_number = '40000001'`).Scan(&studentID); err != nil {
			t.Fatal(err)
		}
		// Unknown target is 404; non-admin is 403.
		if r := c.do("PUT", "/api/admin/users/00000000-0000-0000-0000-000000000000/password", adminAccess, `{"temporary_password":"`+tempPass+`"}`); r.status != 404 {
			t.Fatalf("unknown target %d", r.status)
		}
		if r := c.do("PUT", "/api/admin/users/"+studentID+"/password", studentAccess, `{"temporary_password":"`+tempPass+`"}`); r.status != 403 {
			t.Fatalf("student reset %d", r.status)
		}
		r := c.do("PUT", "/api/admin/users/"+studentID+"/password", adminAccess, `{"temporary_password":"`+tempPass+`"}`)
		if r.status != 204 || r.body != "" {
			t.Fatalf("reset %d %q", r.status, r.body)
		}
		if r := c.do("POST", "/api/auth/refresh", "", `{"refresh_token":"`+studentRefresh+`"}`); r.status != 401 {
			t.Fatalf("refresh after reset: %d, want 401", r.status)
		}
		if login("40000001", studentPass).status != 401 {
			t.Fatal("old password still works")
		}
		tmp := login("40000001", tempPass)
		tmpAccess := str(tmp.json(t), "access_token")
		if tmp.status != 200 || tmp.json(t)["user"].(map[string]any)["must_change_password"] != true {
			t.Fatalf("temp login %d %s", tmp.status, tmp.body)
		}
		// Pending accounts may only reach me/logout/password.
		if r := c.do("POST", "/api/admin/users", tmpAccess, `{}`); r.errCode(t) != "password_change_required" {
			t.Fatalf("pending account on gated route: %d %s", r.status, r.body)
		}
		if r := c.do("GET", "/api/auth/me", tmpAccess, ""); r.status != 200 {
			t.Fatalf("me while pending: %d", r.status)
		}
		if r := c.do("PATCH", "/api/auth/password", tmpAccess, `{"current_password":"wrong-current-pass","new_password":"`+newPass+`"}`); r.status != 422 || r.errCode(t) != "wrong_current_password" {
			t.Fatalf("wrong current: %d %s", r.status, r.body)
		}
		if r := c.do("PATCH", "/api/auth/password", tmpAccess, `{"current_password":"`+tempPass+`","new_password":"short"}`); r.status != 422 {
			t.Fatalf("weak new: %d", r.status)
		}
		// A second session, to prove change-password revokes the others (RevokeOthersForUser on real SQL).
		other := login("40000001", tempPass)
		otherAccess, otherRefresh := str(other.json(t), "access_token"), str(other.json(t), "refresh_token")
		if r := c.do("PATCH", "/api/auth/password", tmpAccess, `{"current_password":"`+tempPass+`","new_password":"`+newPass+`"}`); r.status != 204 {
			t.Fatalf("change: %d %s", r.status, r.body)
		}
		if r := c.do("GET", "/api/auth/me", tmpAccess, ""); r.status != 200 {
			t.Fatalf("current session must survive change: %d", r.status)
		}
		if r := c.do("GET", "/api/auth/me", otherAccess, ""); r.status != 401 {
			t.Fatalf("other session must be revoked: %d", r.status)
		}
		if r := c.do("POST", "/api/auth/refresh", "", `{"refresh_token":"`+otherRefresh+`"}`); r.status != 401 {
			t.Fatalf("other refresh must fail: %d", r.status)
		}
		if login("40000001", newPass).json(t)["user"].(map[string]any)["must_change_password"] != false {
			t.Fatal("must_change_password should be cleared after change")
		}
	})

	t.Run("logout revokes only its session; its refresh token then gives 401 without side effects", func(t *testing.T) {
		r := login("10000001", adminPass)
		access, refresh := str(r.json(t), "access_token"), str(r.json(t), "refresh_token")
		otherDevice := str(login("10000001", adminPass).json(t), "access_token")

		if out := c.do("POST", "/api/auth/logout", access, ""); out.status != 204 {
			t.Fatalf("logout %d", out.status)
		}
		if me := c.do("GET", "/api/auth/me", access, ""); me.status != 401 {
			t.Fatalf("access after logout: %d", me.status)
		}
		// Backdate the logout so we are well past any grace period.
		mustExecSQL(t, db, `UPDATE auth_sessions SET revoked_at = revoked_at - interval '1 hour' WHERE revoked_reason = 'LOGOUT'`)
		for i := 0; i < 2; i++ {
			if rf := c.do("POST", "/api/auth/refresh", "", `{"refresh_token":"`+refresh+`"}`); rf.status != 401 {
				t.Fatalf("refresh after logout (try %d): %d", i, rf.status)
			}
		}
		if me := c.do("GET", "/api/auth/me", otherDevice, ""); me.status != 200 {
			t.Fatalf("the user's other device must stay logged in, got %d", me.status)
		}
	})

	t.Run("refresh rotation and reuse over HTTP", func(t *testing.T) {
		r := login("10000001", adminPass)
		first := str(r.json(t), "refresh_token")
		rot := c.do("POST", "/api/auth/refresh", "", `{"refresh_token":"`+first+`"}`)
		if rot.status != 200 || str(rot.json(t), "refresh_token") == first {
			t.Fatalf("rotate %d", rot.status)
		}
		successor := str(rot.json(t), "access_token")

		// Immediate replay: benign retry.
		if again := c.do("POST", "/api/auth/refresh", "", `{"refresh_token":"`+first+`"}`); again.status != 401 {
			t.Fatalf("replay %d", again.status)
		}
		if me := c.do("GET", "/api/auth/me", successor, ""); me.status != 200 {
			t.Fatalf("successor must survive a replay inside the grace period, me=%d", me.status)
		}
		// Replay after the grace period: real reuse, sessions revoked.
		mustExecSQL(t, db, `UPDATE auth_sessions SET revoked_at = revoked_at - interval '1 minute' WHERE revoked_reason = 'ROTATED'`)
		if again := c.do("POST", "/api/auth/refresh", "", `{"refresh_token":"`+first+`"}`); again.status != 401 {
			t.Fatalf("late replay %d", again.status)
		}
		if me := c.do("GET", "/api/auth/me", successor, ""); me.status != 401 {
			t.Fatalf("late replay must revoke the successor, me=%d", me.status)
		}
	})

	t.Run("login throttling returns 429 with Retry-After and does not reveal accounts", func(t *testing.T) {
		attempt := func(number string) (resp, http.Header) {
			req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"institutional_number":"`+number+`","password":"wrong-password-123"}`))
			req.RemoteAddr = "198.51.100.7:1111" // dedicated client so other subtests are unaffected
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return resp{rec.Code, rec.Body.String()}, rec.Header()
		}
		var blocked []resp
		for _, number := range []string{"10000001", "88888888"} { // existing admin, non-existing
			for i := 0; i < 5; i++ {
				if r, _ := attempt(number); r.status != 401 {
					t.Fatalf("%s attempt %d: %d, want 401", number, i, r.status)
				}
			}
			r, hdr := attempt(number)
			if r.status != 429 || r.errCode(t) != "too_many_attempts" {
				t.Fatalf("%s: %d %s", number, r.status, r.body)
			}
			// Window is 15m and was opened moments ago, so Retry-After is just under 900s.
			secs, err := strconv.Atoi(hdr.Get("Retry-After"))
			if err != nil || secs < 890 || secs > 900 {
				t.Fatalf("%s: Retry-After = %q, want ~900", number, hdr.Get("Retry-After"))
			}
			blocked = append(blocked, r)
		}
		strip := func(r resp) string {
			m := r.json(t)["error"].(map[string]any)
			delete(m, "request_id")
			b, _ := json.Marshal(m)
			return string(b)
		}
		if strip(blocked[0]) != strip(blocked[1]) {
			t.Fatalf("throttled responses differ between existing and non-existing accounts:\n%s\n%s", blocked[0].body, blocked[1].body)
		}
	})

	t.Run("many different users behind one IP are not throttled by aggregate failures", func(t *testing.T) {
		for i := 0; i < 60; i++ { // 60 > the former limit of 20 aggregated failures
			req := httptest.NewRequest("POST", "/api/auth/login",
				strings.NewReader(`{"institutional_number":"`+fmt.Sprintf("7000%04d", i)+`","password":"wrong-password-123"}`))
			req.RemoteAddr = "203.0.113.50:2222"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != 401 {
				t.Fatalf("user %d behind the shared IP got %d, want 401", i, rec.Code)
			}
		}
	})

	t.Run("audit rows exist and hold no secrets", func(t *testing.T) {
		rows, err := db.Q(ctx).Query(ctx, `SELECT action, metadata::text FROM audit_logs`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		seen := map[string]int{}
		for rows.Next() {
			var action, meta string
			if err := rows.Scan(&action, &meta); err != nil {
				t.Fatal(err)
			}
			seen[action]++
			for _, banned := range []string{adminPass, studentPass, tempPass, newPass, "$argon2", "refresh", "access_token"} {
				if strings.Contains(meta, banned) {
					t.Fatalf("audit metadata for %s contains %q", action, banned)
				}
			}
		}
		for _, want := range []string{"identity.user_created", "identity.password_reset", "identity.password_changed",
			"identity.logout", "identity.refresh_reuse_detected"} {
			if seen[want] == 0 {
				t.Errorf("no audit row for %s (have %v)", want, seen)
			}
		}
	})
}
