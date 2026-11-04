// Package app is the composition root: it wires adapters into use cases and
// routes. It is the only place that knows every concrete implementation.
package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	acadhttp "github.com/edutrak/backend/internal/academic/adapters/http"
	acadpg "github.com/edutrak/backend/internal/academic/adapters/postgres"
	acadapp "github.com/edutrak/backend/internal/academic/application"
	analyticshttp "github.com/edutrak/backend/internal/analytics/adapters/http"
	analyticspg "github.com/edutrak/backend/internal/analytics/adapters/postgres"
	analyticsapp "github.com/edutrak/backend/internal/analytics/application"
	analyticsdomain "github.com/edutrak/backend/internal/analytics/domain"
	attendancehttp "github.com/edutrak/backend/internal/attendance/adapters/http"
	attendancenet "github.com/edutrak/backend/internal/attendance/adapters/network"
	attendancepg "github.com/edutrak/backend/internal/attendance/adapters/postgres"
	attendancesec "github.com/edutrak/backend/internal/attendance/adapters/security"
	attendanceapp "github.com/edutrak/backend/internal/attendance/application"
	auditpg "github.com/edutrak/backend/internal/audit/adapters/postgres"
	gradeshttp "github.com/edutrak/backend/internal/grades/adapters/http"
	gradespg "github.com/edutrak/backend/internal/grades/adapters/postgres"
	gradesapp "github.com/edutrak/backend/internal/grades/application"
	idhttp "github.com/edutrak/backend/internal/identity/adapters/http"
	idpg "github.com/edutrak/backend/internal/identity/adapters/postgres"
	idratelimit "github.com/edutrak/backend/internal/identity/adapters/ratelimit"
	idsecurity "github.com/edutrak/backend/internal/identity/adapters/security"
	idapp "github.com/edutrak/backend/internal/identity/application"
	justattendance "github.com/edutrak/backend/internal/justifications/adapters/attendance"
	justhttp "github.com/edutrak/backend/internal/justifications/adapters/http"
	justpg "github.com/edutrak/backend/internal/justifications/adapters/postgres"
	juststorage "github.com/edutrak/backend/internal/justifications/adapters/storage"
	justapp "github.com/edutrak/backend/internal/justifications/application"
	"github.com/edutrak/backend/internal/platform/config"
	"github.com/edutrak/backend/internal/platform/database"
	"github.com/edutrak/backend/internal/platform/httpx"
	tutoringhttp "github.com/edutrak/backend/internal/tutoring/adapters/http"
	tutoringpg "github.com/edutrak/backend/internal/tutoring/adapters/postgres"
	tutoringapp "github.com/edutrak/backend/internal/tutoring/application"
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

// NewAcademicService builds the academic use cases on top of PostgreSQL.
func NewAcademicService(db *database.DB) *acadapp.Service {
	return acadapp.NewService(acadapp.Deps{
		Catalog:            acadpg.NewCatalogRepository(db),
		Terms:              acadpg.NewTermRepository(db),
		Groups:             acadpg.NewGroupRepository(db),
		Assignments:        acadpg.NewAssignmentRepository(db),
		SubjectEnrollments: acadpg.NewSubjectEnrollmentRepository(db),
		Users:              acadpg.NewUserDirectory(db),
		Audit:              auditpg.NewRecorder(db),
		Tx:                 db,
		NewID:              uuid.New,
	})
}

// NewGradesService builds the grades use cases on top of PostgreSQL.
func NewGradesService(db *database.DB) *gradesapp.Service {
	return gradesapp.NewService(gradesapp.Deps{
		Grades:   gradespg.NewRepository(db),
		Academic: gradespg.NewAcademicReader(db),
		Audit:    auditpg.NewRecorder(db),
		Tx:       db,
		NewID:    uuid.New,
	})
}

// NewAttendanceService builds the attendance use cases on top of PostgreSQL.
// The QR network check allows only the configured institutional networks; the
// "allow any" switch is ignored in production even if it somehow reached here.
func NewAttendanceService(db *database.DB, cfg config.Config) *attendanceapp.Service {
	allowAny := cfg.AttendanceAllowAnyNetwork && cfg.Env != "production"
	return attendanceapp.NewService(attendanceapp.Deps{
		Repo:                    attendancepg.NewRepository(db),
		Academic:                attendancepg.NewAcademicReader(db),
		Tokens:                  attendancesec.NewQRTokens(),
		Network:                 attendancenet.New(cfg.AllowedNetworks, allowAny),
		Clock:                   systemClock{},
		Audit:                   auditpg.NewRecorder(db),
		Tx:                      db,
		NewID:                   uuid.New,
		QRTTL:                   cfg.QRTokenTTL,
		DefaultLateAfterMinutes: cfg.AttendanceLateAfterMinutes,
		DefaultLatesPerAbsence:  cfg.AttendanceLatesPerAbsence,
		Location:                cfg.InstitutionLocation,
	})
}

// maxEvidenceBytes is the per-file limit (MAX_EVIDENCE_MB, at most 10 MiB; 10 when unset).
func maxEvidenceBytes(cfg config.Config) int64 {
	if cfg.MaxEvidenceMB < 1 || cfg.MaxEvidenceMB > 10 {
		return 10 << 20
	}
	return int64(cfg.MaxEvidenceMB) << 20
}

// NewJustificationsService builds the justification use cases. Evidence goes to the private
// directory STORAGE_DIR; the only write into attendance data is the Attendance module's own
// JustifyAbsence use case, reached through the gateway.
func NewJustificationsService(db *database.DB, cfg config.Config, log *slog.Logger) (*justapp.Service, error) {
	store, err := juststorage.New(cfg.StorageDir)
	if err != nil {
		return nil, err
	}
	return justapp.NewService(justapp.Deps{
		Repo:             justpg.NewRepository(db),
		Attendance:       justattendance.NewGateway(NewAttendanceService(db, cfg)),
		Storage:          store,
		Audit:            auditpg.NewRecorder(db),
		Tx:               db,
		NewID:            uuid.New,
		Log:              log,
		MaxEvidenceBytes: maxEvidenceBytes(cfg),
		EvidenceRequired: cfg.JustificationEvidenceRequired,
	}), nil
}

// NewTutoringService builds the tutoring use cases. Academic, Grades, Attendance and
// Justifications data are read through the overview reader; nothing is duplicated.
func NewTutoringService(db *database.DB, cfg config.Config) *tutoringapp.Service {
	return tutoringapp.NewService(tutoringapp.Deps{
		Repo:      tutoringpg.NewRepository(db),
		Overviews: tutoringpg.NewOverviewReader(db),
		Audit:     auditpg.NewRecorder(db),
		Tx:        db,
		Clock:     systemClock{},
		NewID:     uuid.New,
		Location:  cfg.InstitutionLocation,
	})
}

// NewAnalyticsService builds the analytics use cases. Indicators are derived from the other
// modules' tables on every read; the only table Analytics writes is alerts.
func NewAnalyticsService(db *database.DB, cfg config.Config) *analyticsapp.Service {
	return analyticsapp.NewService(analyticsapp.Deps{
		Facts:  analyticspg.NewFacts(db),
		Alerts: analyticspg.NewAlerts(db),
		Audit:  auditpg.NewRecorder(db),
		Tx:     db,
		Clock:  systemClock{},
		NewID:  uuid.New,
		Rules: analyticsdomain.Rules{
			AbsencesThreshold: cfg.AnalyticsAbsencesThreshold, TrendWindow: cfg.AnalyticsTrendWindow, TrendDropPoints: cfg.AnalyticsTrendDropPoints,
		},
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
	acadhttp.NewHandler(NewAcademicService(db), identity).Register(mux)
	gradeshttp.NewHandler(NewGradesService(db), identity).Register(mux)
	justifications, err := NewJustificationsService(db, cfg, log)
	if err != nil {
		return nil, err
	}
	justhttp.NewHandler(justifications, identity, maxEvidenceBytes(cfg)).Register(mux)
	attendancehttp.NewHandler(NewAttendanceService(db, cfg), identity, cfg.TrustedProxies).Register(mux)
	tutoringhttp.NewHandler(NewTutoringService(db, cfg), identity).Register(mux)
	analyticshttp.NewHandler(NewAnalyticsService(db, cfg), identity).Register(mux)
	return httpx.Base(log, mux), nil
}
