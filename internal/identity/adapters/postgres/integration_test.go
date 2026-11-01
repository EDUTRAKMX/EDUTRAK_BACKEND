package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	auditpg "github.com/edutrak/backend/internal/audit/adapters/postgres"
	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	idpg "github.com/edutrak/backend/internal/identity/adapters/postgres"
	"github.com/edutrak/backend/internal/identity/adapters/ratelimit"
	"github.com/edutrak/backend/internal/identity/adapters/security"
	"github.com/edutrak/backend/internal/identity/application"
	"github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/database"
	"github.com/edutrak/backend/internal/platform/database/dbtest"
	"github.com/google/uuid"
)

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type failingAudit struct{}

func (failingAudit) Record(context.Context, auditdomain.Event) error { return errors.New("audit down") }

func newService(t *testing.T, db *database.DB, audit interface {
	Record(context.Context, auditdomain.Event) error
}) *application.Service {
	t.Helper()
	tokens, err := security.NewJWTTokenService([]byte("0123456789abcdef0123456789abcdef"), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.NewService(application.Deps{
		Users: idpg.NewUserRepository(db), Sessions: idpg.NewSessionRepository(db),
		Hasher: security.NewArgon2Hasher(security.Argon2Params{MemoryKiB: 8, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}),
		Tokens: tokens, Clock: realClock{}, Audit: audit, Tx: db, NewID: uuid.New, RefreshTTL: time.Hour,
		Limiter: ratelimit.New(realClock{}, ratelimit.Rule{MaxFailures: 50, Window: time.Minute}, ratelimit.Rule{MaxFailures: 50, Window: time.Minute}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

const pw = "correct-horse-battery"

func TestIdentityEndToEndOnPostgres(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	svc := newService(t, db, auditpg.NewRecorder(db))

	admin, err := svc.BootstrapUser(ctx, application.CreateUserInput{
		InstitutionalNumber: "10000001", FirstName: "Admin", LastName: "Prueba", Password: pw, Roles: []domain.Role{domain.RoleAdmin},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BootstrapUser(ctx, application.CreateUserInput{
		InstitutionalNumber: "10000001", FirstName: "X", LastName: "Y", Password: pw, Roles: []domain.Role{domain.RoleStudent},
	}); !errors.Is(err, domain.ErrDuplicateUser) {
		t.Fatalf("duplicate: %v", err)
	}

	res, err := svc.Login(ctx, application.LoginInput{InstitutionalNumber: "10000001", Password: pw, UserAgent: "test", IPAddress: "192.168.1.10"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.Authenticate(ctx, res.AccessToken)
	if err != nil || p.UserID != admin.ID || !p.HasRole(domain.RoleAdmin) {
		t.Fatalf("p=%+v err=%v", p, err)
	}

	// Admin creates a multi-role user, then resets that user's password.
	teacher, err := svc.CreateUser(ctx, p, application.CreateUserInput{
		InstitutionalNumber: "20000001", FirstName: "Ana", LastName: "Prueba", Password: pw,
		Roles: []domain.Role{domain.RoleTeacher, domain.RoleTutor},
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := idpg.NewUserRepository(db).FindByID(ctx, teacher.ID)
	if err != nil || !loaded.HasRole(domain.RoleTeacher) || !loaded.HasRole(domain.RoleTutor) || loaded.HasRole(domain.RoleAdmin) {
		t.Fatalf("loaded = %+v err = %v", loaded, err)
	}

	tRes, err := svc.Login(ctx, application.LoginInput{InstitutionalNumber: "20000001", Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPassword(ctx, p, teacher.ID, "another-temp-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, tRes.AccessToken); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("sessions must be revoked after reset: %v", err)
	}
	if _, err := svc.Login(ctx, application.LoginInput{InstitutionalNumber: "20000001", Password: pw}); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("old password must stop working: %v", err)
	}
	after, err := svc.Login(ctx, application.LoginInput{InstitutionalNumber: "20000001", Password: "another-temp-password"})
	if err != nil || !after.User.MustChangePassword {
		t.Fatalf("after=%+v err=%v", after, err)
	}

	// Refresh rotation and reuse detection against the real table.
	second, err := svc.Refresh(ctx, res.RefreshToken, "test", "192.168.1.10")
	if err != nil {
		t.Fatal(err)
	}
	// Replaying the superseded token right away is a benign retry: 401, successor untouched.
	if _, err := svc.Refresh(ctx, res.RefreshToken, "", ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("immediate replay: %v", err)
	}
	if _, err := svc.Authenticate(ctx, second.AccessToken); err != nil {
		t.Fatalf("successor must survive a replay inside the grace period: %v", err)
	}
	// Backdate the rotation so the replay is outside the grace period: real reuse.
	if _, err := db.Q(ctx).Exec(ctx, `UPDATE auth_sessions SET revoked_at = revoked_at - interval '1 minute' WHERE revoked_reason = 'ROTATED'`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(ctx, res.RefreshToken, "", ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("late replay: %v", err)
	}
	if _, err := svc.Authenticate(ctx, second.AccessToken); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("late replay must revoke the user's active sessions")
	}
	var reuseRows int
	_ = db.Q(ctx).QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE revoked_reason = 'REUSE_DETECTED'`).Scan(&reuseRows)
	if reuseRows != 1 {
		t.Fatalf("sessions revoked as REUSE_DETECTED = %d, want 1", reuseRows)
	}

	// Only hashes are stored.
	var leaked int
	_ = db.Q(ctx).QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE refresh_token_hash IN ($1, $2)`, res.RefreshToken, second.RefreshToken).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("raw refresh token found in auth_sessions")
	}

	// Audit trail exists and has no secrets.
	var n int
	_ = db.Q(ctx).QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action IN ('identity.user_created','identity.password_reset')`).Scan(&n)
	if n != 3 { // admin bootstrap, teacher created, password reset
		t.Fatalf("audit rows = %d, want 3", n)
	}
	_ = db.Q(ctx).QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'identity.refresh_reuse_detected' AND actor_user_id IS NULL`).Scan(&n)
	if n != 1 {
		t.Fatalf("reuse audit rows = %d, want 1", n)
	}
}

func TestCreateUserRollsBackWhenAuditFails(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	svc := newService(t, db, failingAudit{})

	_, err := svc.BootstrapUser(ctx, application.CreateUserInput{
		InstitutionalNumber: "10000002", FirstName: "A", LastName: "B", Password: pw, Roles: []domain.Role{domain.RoleStudent},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var n int
	_ = db.Q(ctx).QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	if n != 0 {
		t.Fatalf("user persisted despite audit failure (%d rows): partial state", n)
	}
}

func TestConcurrentRefreshOnlyOneSucceeds(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	svc := newService(t, db, auditpg.NewRecorder(db))
	if _, err := svc.BootstrapUser(ctx, application.CreateUserInput{
		InstitutionalNumber: "10000003", FirstName: "A", LastName: "B", Password: pw, Roles: []domain.Role{domain.RoleStudent},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Login(ctx, application.LoginInput{InstitutionalNumber: "10000003", Password: pw})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 8
	out := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			_, err := svc.Refresh(ctx, res.RefreshToken, "", "")
			out <- err
		}()
	}
	ok := 0
	for i := 0; i < workers; i++ {
		if err := <-out; err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d refreshes succeeded with the same token, want exactly 1", ok)
	}
	// The losers must not have punished the user: the winner's session is still active.
	var active int
	_ = db.Q(ctx).QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE revoked_at IS NULL`).Scan(&active)
	if active != 1 {
		t.Fatalf("active sessions after concurrent refresh = %d, want 1 (the winner)", active)
	}
}
