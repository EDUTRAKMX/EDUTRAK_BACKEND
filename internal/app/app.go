// Package app is the composition root: it wires adapters into use cases and
// routes. It is the only place that knows every concrete implementation.
package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	auditpg "github.com/edutrak/backend/internal/audit/adapters/postgres"
	idhttp "github.com/edutrak/backend/internal/identity/adapters/http"
	idpg "github.com/edutrak/backend/internal/identity/adapters/postgres"
	idratelimit "github.com/edutrak/backend/internal/identity/adapters/ratelimit"
	idsecurity "github.com/edutrak/backend/internal/identity/adapters/security"
	idapp "github.com/edutrak/backend/internal/identity/application"
	"github.com/edutrak/backend/internal/platform/config"
	"github.com/edutrak/backend/internal/platform/database"
	"github.com/edutrak/backend/internal/platform/httpx"
	"github.com/google/uuid"
)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// NewIdentityService builds the identity use cases on top of PostgreSQL.
func NewIdentityService(db *database.DB, cfg config.Config) (*idapp.Service, error) {
	tokens, err := idsecurity.NewJWTTokenService(cfg.JWTSecret, cfg.AccessTokenTTL)
	if err != nil {
		return nil, err
	}
	clock := systemClock{}
	limiter := idratelimit.New(clock,
		idratelimit.Rule{MaxFailures: cfg.LoginMaxFailuresPerAccount, Window: cfg.LoginRateWindow},
		idratelimit.Rule{MaxFailures: cfg.LoginMaxFailuresPerIP, Window: cfg.LoginRateWindow})
	return idapp.NewService(idapp.Deps{
		Limiter:    limiter,
		Users:      idpg.NewUserRepository(db),
		Sessions:   idpg.NewSessionRepository(db),
		Hasher:     idsecurity.NewArgon2Hasher(idsecurity.DefaultArgon2Params),
		Tokens:     tokens,
		Clock:      clock,
		Audit:      auditpg.NewRecorder(db),
		Tx:         db,
		NewID:      uuid.New,
		RefreshTTL: cfg.RefreshTokenTTL,
	})
}

// NewHTTPHandler returns the full API handler.
func NewHTTPHandler(db *database.DB, cfg config.Config, log *slog.Logger) (http.Handler, error) {
	identity, err := NewIdentityService(db, cfg)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			httpx.LoggerFrom(r.Context()).Error("readiness check failed", "error", err)
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	idhttp.NewHandler(identity, cfg.TrustedProxies).Register(mux)
	return httpx.Base(log, mux), nil
}
