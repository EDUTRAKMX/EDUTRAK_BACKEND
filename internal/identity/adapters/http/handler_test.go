package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edutrak/backend/internal/identity/application"
	"github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/httpx"
	"github.com/google/uuid"
)

// stubService implements Service; only Authenticate and the calls recorded in
// `called` carry behaviour, which is all the HTTP layer needs to be tested.
type stubService struct {
	principals map[string]domain.Principal
	called     []string
	loginErr   error
}

func (s *stubService) Authenticate(_ context.Context, tok string) (domain.Principal, error) {
	if p, ok := s.principals[tok]; ok {
		return p, nil
	}
	return domain.Principal{}, domain.ErrUnauthenticated
}
func (s *stubService) Login(context.Context, application.LoginInput) (*application.AuthResult, error) {
	s.called = append(s.called, "login")
	return nil, s.loginErr
}
func (s *stubService) Refresh(context.Context, string, string, string) (*application.AuthResult, error) {
	return nil, domain.ErrUnauthenticated
}
func (s *stubService) Logout(context.Context, domain.Principal) error {
	s.called = append(s.called, "logout")
	return nil
}
func (s *stubService) Me(_ context.Context, p domain.Principal) (*domain.User, error) {
	return &domain.User{ID: p.UserID, InstitutionalNumber: "20240001", Email: "20240001@uttcampus.edu.mx", Roles: p.Roles}, nil
}
func (s *stubService) ChangePassword(context.Context, domain.Principal, string, string) error {
	s.called = append(s.called, "change_password")
	return nil
}
func (s *stubService) ResetPassword(context.Context, domain.Principal, uuid.UUID, string) error {
	s.called = append(s.called, "reset_password")
	return nil
}
func (s *stubService) CreateUser(context.Context, domain.Principal, application.CreateUserInput) (*domain.User, error) {
	s.called = append(s.called, "create_user")
	return &domain.User{ID: uuid.New()}, nil
}

func newServer(svc *stubService) http.Handler {
	mux := http.NewServeMux()
	NewHandler(svc, nil).Register(mux)
	return httpx.Base(slog.New(slog.NewTextHandler(io.Discard, nil)), mux)
}

func do(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestProtectedRoutesRequireValidToken(t *testing.T) {
	svc := &stubService{principals: map[string]domain.Principal{"good": {UserID: uuid.New(), Roles: []domain.Role{domain.RoleStudent}}}}
	h := newServer(svc)
	target := uuid.New().String()

	routes := []struct{ method, path, body string }{
		{"GET", "/api/auth/me", ""},
		{"POST", "/api/auth/logout", ""},
		{"PATCH", "/api/auth/password", `{"current_password":"a","new_password":"b"}`},
		{"POST", "/api/admin/users", `{}`},
		{"PUT", "/api/admin/users/" + target + "/password", `{"temporary_password":"x"}`},
	}
	for _, rt := range routes {
		for name, hdr := range map[string]string{"no token": "", "invalid token": "bogus"} {
			t.Run(rt.method+" "+rt.path+" "+name, func(t *testing.T) {
				if rec := do(h, rt.method, rt.path, hdr, rt.body); rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401", rec.Code)
				}
			})
		}
	}
	if len(svc.called) != 0 {
		t.Fatalf("use cases invoked without authentication: %v", svc.called)
	}
}

func TestAdminRoutesRejectNonAdmins(t *testing.T) {
	svc := &stubService{principals: map[string]domain.Principal{}}
	for _, r := range []domain.Role{domain.RoleStudent, domain.RoleTeacher, domain.RoleTutor, domain.RoleDirector} {
		svc.principals[string(r)] = domain.Principal{UserID: uuid.New(), Roles: []domain.Role{r}}
	}
	svc.principals["admin"] = domain.Principal{UserID: uuid.New(), Roles: []domain.Role{domain.RoleAdmin}}
	h := newServer(svc)
	path := "/api/admin/users/" + uuid.New().String() + "/password"

	for _, r := range []domain.Role{domain.RoleStudent, domain.RoleTeacher, domain.RoleTutor, domain.RoleDirector} {
		if rec := do(h, "PUT", path, string(r), `{"temporary_password":"long-enough-pass"}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s reset: status = %d, want 403", r, rec.Code)
		}
		if rec := do(h, "POST", "/api/admin/users", string(r), `{}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s create: status = %d, want 403", r, rec.Code)
		}
	}
	if len(svc.called) != 0 {
		t.Fatalf("use cases invoked for non-admins: %v", svc.called)
	}
	if rec := do(h, "PUT", path, "admin", `{"temporary_password":"long-enough-pass"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("admin reset: status = %d, want 204", rec.Code)
	}
}

func TestMustChangePasswordGating(t *testing.T) {
	svc := &stubService{principals: map[string]domain.Principal{
		"pending": {UserID: uuid.New(), Roles: []domain.Role{domain.RoleAdmin}, MustChangePassword: true},
	}}
	h := newServer(svc)

	if rec := do(h, "POST", "/api/admin/users", "pending", `{}`); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "password_change_required") {
		t.Fatalf("admin route: %d %s", rec.Code, rec.Body.String())
	}
	for _, rt := range []struct{ m, p, b string }{
		{"GET", "/api/auth/me", ""},
		{"PATCH", "/api/auth/password", `{"current_password":"a","new_password":"b"}`},
		{"POST", "/api/auth/logout", ""},
	} {
		if rec := do(h, rt.m, rt.p, "pending", rt.b); rec.Code >= 400 {
			t.Errorf("%s %s should be allowed, got %d", rt.m, rt.p, rec.Code)
		}
	}
}

func TestLoginErrorsAndBodyHandling(t *testing.T) {
	svc := &stubService{loginErr: domain.ErrInvalidCredentials}
	h := newServer(svc)

	if rec := do(h, "POST", "/api/auth/login", "", `{"institutional_number":"20240001","password":"x"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad credentials: %d", rec.Code)
	}
	for name, body := range map[string]string{"malformed": `{`, "unknown field": `{"institutional_number":"1","password":"x","admin":true}`, "empty": ``} {
		if rec := do(h, "POST", "/api/auth/login", "", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}
}

func TestResetPasswordMalformedIDLooksLikeNotFound(t *testing.T) {
	svc := &stubService{principals: map[string]domain.Principal{"admin": {UserID: uuid.New(), Roles: []domain.Role{domain.RoleAdmin}}}}
	if rec := do(newServer(svc), "PUT", "/api/admin/users/not-a-uuid/password", "admin", `{"temporary_password":"long-enough-pass"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
