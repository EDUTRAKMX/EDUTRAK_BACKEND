package http

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"github.com/edutrak/backend/internal/identity/application"
	"github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/apperr"
	"github.com/edutrak/backend/internal/platform/httpx"
	"github.com/edutrak/backend/internal/platform/netx"
	"github.com/google/uuid"
)

// Service is what the handlers need from the application layer.
type Service interface {
	Authenticator
	Login(ctx context.Context, in application.LoginInput) (*application.AuthResult, error)
	Refresh(ctx context.Context, refreshToken, userAgent, ip string) (*application.AuthResult, error)
	Logout(ctx context.Context, p domain.Principal) error
	Me(ctx context.Context, p domain.Principal) (*domain.User, error)
	ChangePassword(ctx context.Context, p domain.Principal, current, next string) error
	ResetPassword(ctx context.Context, p domain.Principal, targetID uuid.UUID, temporary string) error
	CreateUser(ctx context.Context, p domain.Principal, in application.CreateUserInput) (*domain.User, error)
}

type Handler struct {
	svc            Service
	trustedProxies []netip.Prefix
}

func NewHandler(svc Service, trustedProxies []netip.Prefix) *Handler {
	return &Handler{svc: svc, trustedProxies: trustedProxies}
}

// Register mounts the identity routes on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/login", h.login)
	mux.HandleFunc("POST /api/auth/refresh", h.refresh)
	mux.Handle("POST /api/auth/logout", AuthenticateAllowingPasswordChange(h.svc, http.HandlerFunc(h.logout)))
	mux.Handle("GET /api/auth/me", AuthenticateAllowingPasswordChange(h.svc, http.HandlerFunc(h.me)))
	mux.Handle("PATCH /api/auth/password", AuthenticateAllowingPasswordChange(h.svc, http.HandlerFunc(h.changePassword)))

	mux.Handle("POST /api/admin/users",
		Authenticate(h.svc, RequireRoles(http.HandlerFunc(h.createUser), domain.RoleAdmin)))
	mux.Handle("PUT /api/admin/users/{id}/password",
		Authenticate(h.svc, RequireRoles(http.HandlerFunc(h.resetPassword), domain.RoleAdmin)))
}

type userDTO struct {
	ID                  uuid.UUID     `json:"id"`
	InstitutionalNumber string        `json:"institutional_number"`
	Email               string        `json:"email"`
	FirstName           string        `json:"first_name"`
	LastName            string        `json:"last_name"`
	Roles               []domain.Role `json:"roles"`
	MustChangePassword  bool          `json:"must_change_password"`
}

func toUserDTO(u *domain.User) userDTO {
	roles := u.Roles
	if roles == nil {
		roles = []domain.Role{}
	}
	return userDTO{u.ID, u.InstitutionalNumber, u.Email, u.FirstName, u.LastName, roles, u.MustChangePassword}
}

type authDTO struct {
	TokenType        string    `json:"token_type"`
	AccessToken      string    `json:"access_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshToken     string    `json:"refresh_token"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	User             userDTO   `json:"user"`
}

func toAuthDTO(r *application.AuthResult) authDTO {
	return authDTO{"Bearer", r.AccessToken, r.AccessExpiresAt, r.RefreshToken, r.RefreshExpiresAt, toUserDTO(r.User)}
}

func (h *Handler) clientIP(r *http.Request) string {
	if ip, ok := netx.ClientIP(r, h.trustedProxies); ok {
		return ip.String()
	}
	return ""
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstitutionalNumber string `json:"institutional_number"`
		Password            string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Login(r.Context(), application.LoginInput{
		InstitutionalNumber: req.InstitutionalNumber, Password: req.Password,
		UserAgent: r.UserAgent(), IPAddress: h.clientIP(r),
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAuthDTO(res))
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Refresh(r.Context(), req.RefreshToken, r.UserAgent(), h.clientIP(r))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAuthDTO(res))
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	if err := h.svc.Logout(r.Context(), p); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	u, err := h.svc.Me(r.Context(), p)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toUserDTO(u))
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	p, _ := PrincipalFrom(r.Context())
	if err := h.svc.ChangePassword(r.Context(), p, req.CurrentPassword, req.NewPassword); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		// Same response as an unknown user: do not reveal anything about ids.
		httpx.Error(w, r, domain.ErrUserNotFound)
		return
	}
	var req struct {
		TemporaryPassword string `json:"temporary_password"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	p, _ := PrincipalFrom(r.Context())
	if err := h.svc.ResetPassword(r.Context(), p, id, req.TemporaryPassword); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstitutionalNumber string        `json:"institutional_number"`
		FirstName           string        `json:"first_name"`
		LastName            string        `json:"last_name"`
		Password            string        `json:"password"`
		Roles               []domain.Role `json:"roles"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	p, _ := PrincipalFrom(r.Context())
	u, err := h.svc.CreateUser(r.Context(), p, application.CreateUserInput{
		InstitutionalNumber: req.InstitutionalNumber, FirstName: req.FirstName, LastName: req.LastName,
		Password: req.Password, Roles: req.Roles,
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toUserDTO(u))
}

func domainError(code, msg string) *apperr.Error {
	return apperr.New(apperr.KindForbidden, code, msg)
}
