// Package http is the HTTP driving adapter of the identity module. Its
// middleware is also what other modules use to authenticate and authorize.
package http

import (
	"context"
	"net/http"
	"strings"

	"github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/httpx"
)

// Authenticator turns a Bearer access token into a domain.Principal.
type Authenticator interface {
	Authenticate(ctx context.Context, accessToken string) (domain.Principal, error)
}

type principalKey struct{}

// PrincipalFrom returns the authenticated caller set by Authenticate.
func PrincipalFrom(ctx context.Context) (domain.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(domain.Principal)
	return p, ok
}

var errPasswordChangeRequired = domainError("password_change_required", "you must change your password before continuing")

// Authenticate requires a valid access token. Accounts flagged
// must_change_password may only reach the endpoints wrapped with
// AuthenticateAllowingPasswordChange.
func Authenticate(a Authenticator, next http.Handler) http.Handler {
	return authenticate(a, false, next)
}

// AuthenticateAllowingPasswordChange is for me, logout and change-password.
func AuthenticateAllowingPasswordChange(a Authenticator, next http.Handler) http.Handler {
	return authenticate(a, true, next)
}

func authenticate(a Authenticator, allowPending bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			httpx.Error(w, r, domain.ErrUnauthenticated)
			return
		}
		p, err := a.Authenticate(r.Context(), token)
		if err != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			httpx.Error(w, r, err)
			return
		}
		if p.MustChangePassword && !allowPending {
			httpx.Error(w, r, errPasswordChangeRequired)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

// RequireRoles must be used inside Authenticate. It is a coarse role gate;
// resource-level checks (ownership, assignment) still belong to the use cases.
func RequireRoles(next http.Handler, roles ...domain.Role) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			httpx.Error(w, r, domain.ErrUnauthenticated)
			return
		}
		if err := p.RequireAnyRole(roles...); err != nil {
			httpx.Error(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}
