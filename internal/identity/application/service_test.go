package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/apperr"
	"github.com/google/uuid"
)

func TestLogin(t *testing.T) {
	tests := []struct {
		name     string
		number   string
		password string
		active   bool
		want     error
	}{
		{"ok", "20240001", goodPassword, true, nil},
		{"wrong password", "20240001", "wrong-password-123", true, domain.ErrInvalidCredentials},
		{"unknown user", "20249999", goodPassword, true, domain.ErrInvalidCredentials},
		{"malformed number", "abc", goodPassword, true, domain.ErrInvalidCredentials},
		{"empty password", "20240001", "", true, domain.ErrInvalidCredentials},
		{"inactive user", "20240001", goodPassword, false, domain.ErrAccountInactive},
		{"inactive user wrong password does not reveal state", "20240001", "wrong-password-123", false, domain.ErrInvalidCredentials},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.addUser("20240001", tt.active, domain.RoleStudent)
			res, err := e.svc.Login(context.Background(), LoginInput{InstitutionalNumber: tt.number, Password: tt.password})
			if !errors.Is(err, tt.want) && !(tt.want == nil && err == nil) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if tt.want == nil {
				if res.AccessToken == "" || res.RefreshToken == "" || len(e.sessions.byID) != 1 {
					t.Fatalf("incomplete result: %+v sessions=%d", res, len(e.sessions.byID))
				}
				for _, s := range e.sessions.byID {
					if s.RefreshTokenHash == res.RefreshToken {
						t.Fatal("raw refresh token stored instead of hash")
					}
				}
			} else if len(e.sessions.byID) != 0 {
				t.Fatal("failed login must not create a session")
			}
		})
	}
}

func TestAuthenticate(t *testing.T) {
	login := func(e *env, u *domain.User) *AuthResult {
		res, err := e.svc.Login(context.Background(), LoginInput{InstitutionalNumber: u.InstitutionalNumber, Password: goodPassword})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	t.Run("valid token returns principal with roles", func(t *testing.T) {
		e := newEnv(t)
		u := e.addUser("20240001", true, domain.RoleTeacher, domain.RoleTutor)
		p, err := e.svc.Authenticate(context.Background(), login(e, u).AccessToken)
		if err != nil || p.UserID != u.ID || !p.HasRole(domain.RoleTutor) || !p.HasRole(domain.RoleTeacher) {
			t.Fatalf("p=%+v err=%v", p, err)
		}
	})
	t.Run("garbage token", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.Authenticate(context.Background(), "nonsense"); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("expired token", func(t *testing.T) {
		e := newEnv(t)
		tok := login(e, e.addUser("20240001", true, domain.RoleStudent)).AccessToken
		e.clock.now = e.clock.now.Add(time.Hour)
		if _, err := e.svc.Authenticate(context.Background(), tok); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("revoked session", func(t *testing.T) {
		e := newEnv(t)
		u := e.addUser("20240001", true, domain.RoleStudent)
		res := login(e, u)
		p, _ := e.svc.Authenticate(context.Background(), res.AccessToken)
		if err := e.svc.Logout(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Authenticate(context.Background(), res.AccessToken); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("token must stop working after logout, err = %v", err)
		}
	})
	t.Run("user deactivated after login", func(t *testing.T) {
		e := newEnv(t)
		u := e.addUser("20240001", true, domain.RoleStudent)
		res := login(e, u)
		e.users.byID[u.ID].IsActive = false
		if _, err := e.svc.Authenticate(context.Background(), res.AccessToken); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("role revocation takes effect immediately", func(t *testing.T) {
		e := newEnv(t)
		u := e.addUser("20240001", true, domain.RoleAdmin)
		res := login(e, u)
		e.users.byID[u.ID].Roles = []domain.Role{domain.RoleStudent}
		p, err := e.svc.Authenticate(context.Background(), res.AccessToken)
		if err != nil || p.HasRole(domain.RoleAdmin) {
			t.Fatalf("stale role: p=%+v err=%v", p, err)
		}
	})
}

func TestRefresh(t *testing.T) {
	setup := func(t *testing.T) (*env, *AuthResult) {
		e := newEnv(t)
		e.addUser("20240001", true, domain.RoleStudent)
		res, err := e.svc.Login(context.Background(), LoginInput{InstitutionalNumber: "20240001", Password: goodPassword})
		if err != nil {
			t.Fatal(err)
		}
		return e, res
	}

	t.Run("rotates tokens", func(t *testing.T) {
		e, first := setup(t)
		second, err := e.svc.Refresh(context.Background(), first.RefreshToken, "", "")
		if err != nil || second.RefreshToken == first.RefreshToken {
			t.Fatalf("err=%v same=%v", err, second != nil && second.RefreshToken == first.RefreshToken)
		}
		if _, err := e.svc.Authenticate(context.Background(), first.AccessToken); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatal("old access token must die with its session")
		}
		if _, err := e.svc.Authenticate(context.Background(), second.AccessToken); err != nil {
			t.Fatalf("new access token should work: %v", err)
		}
	})
	t.Run("replay of a rotated token after the grace period revokes every session and is audited", func(t *testing.T) {
		e, first := setup(t)
		other, _ := e.svc.Login(context.Background(), LoginInput{InstitutionalNumber: "20240001", Password: goodPassword}) // another device
		second, _ := e.svc.Refresh(context.Background(), first.RefreshToken, "", "")
		e.clock.now = e.clock.now.Add(11 * time.Second)

		if _, err := e.svc.Refresh(context.Background(), first.RefreshToken, "", ""); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v", err)
		}
		for name, tok := range map[string]string{"rotated successor": second.AccessToken, "other device": other.AccessToken} {
			if _, err := e.svc.Authenticate(context.Background(), tok); !errors.Is(err, domain.ErrUnauthenticated) {
				t.Fatalf("%s must be revoked by reuse detection", name)
			}
		}
		if got := e.auditActions(); len(got) != 1 || got[0] != "identity.refresh_reuse_detected" {
			t.Fatalf("audit = %v", got)
		}
		if ev := e.audit.events[0]; ev.ActorUserID != nil || ev.Metadata["session_id"] == nil {
			t.Fatalf("unexpected audit event: %+v", ev)
		}
		reasons := map[domain.RevokeReason]int{}
		for _, s := range e.sessions.byID {
			reasons[s.RevokedReason]++
		}
		if reasons[domain.RevokeRotated] != 1 || reasons[domain.RevokeReuseDetected] != 2 {
			t.Fatalf("revocation reasons = %v", reasons)
		}
	})
	t.Run("second refresh within the grace period is a plain 401", func(t *testing.T) {
		e, first := setup(t)
		second, _ := e.svc.Refresh(context.Background(), first.RefreshToken, "", "")
		e.clock.now = e.clock.now.Add(5 * time.Second)
		if _, err := e.svc.Refresh(context.Background(), first.RefreshToken, "", ""); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v", err)
		}
		if _, err := e.svc.Authenticate(context.Background(), second.AccessToken); err != nil {
			t.Fatalf("legitimate successor must survive a benign retry: %v", err)
		}
		if len(e.audit.events) != 0 {
			t.Fatalf("no audit expected, got %v", e.auditActions())
		}
	})
	t.Run("refresh after logout is 401 and does not touch other sessions", func(t *testing.T) {
		e, first := setup(t)
		other, _ := e.svc.Login(context.Background(), LoginInput{InstitutionalNumber: "20240001", Password: goodPassword})
		p, _ := e.svc.Authenticate(context.Background(), first.AccessToken)
		if err := e.svc.Logout(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		e.clock.now = e.clock.now.Add(11 * time.Second) // beyond the grace period, access tokens still valid
		if _, err := e.svc.Refresh(context.Background(), first.RefreshToken, "", ""); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v", err)
		}
		if _, err := e.svc.Authenticate(context.Background(), other.AccessToken); err != nil {
			t.Fatalf("other device must stay logged in: %v", err)
		}
		if got := e.auditActions(); len(got) != 1 || got[0] != "identity.logout" {
			t.Fatalf("audit = %v, want only the logout", got)
		}
	})
	t.Run("refresh after password change or reset is 401 without cascading", func(t *testing.T) {
		for _, reason := range []domain.RevokeReason{domain.RevokePasswordChanged, domain.RevokePasswordReset, domain.RevokeReuseDetected} {
			e, first := setup(t)
			other, _ := e.svc.Login(context.Background(), LoginInput{InstitutionalNumber: "20240001", Password: goodPassword})
			sess, _ := e.sessions.FindByRefreshTokenHash(context.Background(), (&fakeTokens{}).HashRefreshToken(first.RefreshToken))
			_, _ = e.sessions.Revoke(context.Background(), sess.ID, e.clock.now, reason)
			e.clock.now = e.clock.now.Add(11 * time.Second)
			if _, err := e.svc.Refresh(context.Background(), first.RefreshToken, "", ""); !errors.Is(err, domain.ErrUnauthenticated) {
				t.Fatalf("%s: err = %v", reason, err)
			}
			if _, err := e.svc.Authenticate(context.Background(), other.AccessToken); err != nil {
				t.Fatalf("%s: unrelated session was revoked: %v", reason, err)
			}
		}
	})
	t.Run("expired refresh token", func(t *testing.T) {
		e, first := setup(t)
		e.clock.now = e.clock.now.Add(48 * time.Hour)
		if _, err := e.svc.Refresh(context.Background(), first.RefreshToken, "", ""); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unknown and empty token", func(t *testing.T) {
		e, _ := setup(t)
		for _, tok := range []string{"", "made-up"} {
			if _, err := e.svc.Refresh(context.Background(), tok, "", ""); !errors.Is(err, domain.ErrUnauthenticated) {
				t.Fatalf("token %q: err = %v", tok, err)
			}
		}
	})
}

func TestLogoutThenRefreshFails(t *testing.T) {
	e := newEnv(t)
	e.addUser("20240001", true, domain.RoleStudent)
	res, _ := e.svc.Login(context.Background(), LoginInput{InstitutionalNumber: "20240001", Password: goodPassword})
	p, err := e.svc.Authenticate(context.Background(), res.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Logout(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Refresh(context.Background(), res.RefreshToken, "", ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("refresh after logout: err = %v", err)
	}
	// Logging out twice is harmless and is audited once.
	if err := e.svc.Logout(context.Background(), p); err != nil {
		t.Fatalf("second logout: %v", err)
	}
	if got := e.auditActions(); len(got) != 1 || got[0] != "identity.logout" || e.audit.events[0].ActorUserID == nil {
		t.Fatalf("audit = %v", got)
	}
}

func TestRevocationReasons(t *testing.T) {
	ctx := context.Background()
	login := func(e *env) *AuthResult {
		r, err := e.svc.Login(ctx, LoginInput{InstitutionalNumber: "20240001", Password: goodPassword})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	reasonOf := func(e *env, res *AuthResult) domain.RevokeReason {
		s, _ := e.sessions.FindByRefreshTokenHash(ctx, (&fakeTokens{}).HashRefreshToken(res.RefreshToken))
		return s.RevokedReason
	}

	t.Run("rotation", func(t *testing.T) {
		e := newEnv(t)
		e.addUser("20240001", true, domain.RoleStudent)
		first := login(e)
		if _, err := e.svc.Refresh(ctx, first.RefreshToken, "", ""); err != nil {
			t.Fatal(err)
		}
		if got := reasonOf(e, first); got != domain.RevokeRotated {
			t.Fatalf("reason = %q", got)
		}
	})
	t.Run("password change keeps the current session and revokes the rest", func(t *testing.T) {
		e := newEnv(t)
		u := e.addUser("20240001", true, domain.RoleStudent)
		current, other1, other2 := login(e), login(e), login(e)
		p, _ := e.svc.Authenticate(ctx, current.AccessToken)
		if err := e.svc.ChangePassword(ctx, p, goodPassword, "a-brand-new-passphrase"); err != nil {
			t.Fatal(err)
		}
		if e.sessions.activeSessions(u.ID) != 1 {
			t.Fatalf("active sessions = %d, want only the current one", e.sessions.activeSessions(u.ID))
		}
		if reasonOf(e, other1) != domain.RevokePasswordChanged || reasonOf(e, other2) != domain.RevokePasswordChanged || reasonOf(e, current) != "" {
			t.Fatal("unexpected revocation reasons")
		}
	})
	t.Run("admin reset revokes every session", func(t *testing.T) {
		e := newEnv(t)
		admin := e.addUser("20240009", true, domain.RoleAdmin)
		target := e.addUser("20240001", true, domain.RoleStudent)
		a, b := login(e), login(e)
		if err := e.svc.ResetPassword(ctx, principalOf(admin), target.ID, "temporary-pass-123"); err != nil {
			t.Fatal(err)
		}
		if e.sessions.activeSessions(target.ID) != 0 || reasonOf(e, a) != domain.RevokePasswordReset || reasonOf(e, b) != domain.RevokePasswordReset {
			t.Fatal("reset must revoke all sessions with reason PASSWORD_RESET")
		}
	})
}

func TestLoginRateLimiting(t *testing.T) {
	ctx := context.Background()
	bad := func(e *env, number, ip string) error {
		_, err := e.svc.Login(ctx, LoginInput{InstitutionalNumber: number, Password: "wrong-password-123", IPAddress: ip})
		return err
	}

	t.Run("blocks after repeated failures even with the right password", func(t *testing.T) {
		e := newEnv(t)
		e.addUser("20240001", true, domain.RoleStudent)
		for i := 0; i < 3; i++ {
			if err := bad(e, "20240001", "192.0.2.1"); !errors.Is(err, domain.ErrInvalidCredentials) {
				t.Fatalf("attempt %d: %v", i, err)
			}
		}
		_, err := e.svc.Login(ctx, LoginInput{InstitutionalNumber: "20240001", Password: goodPassword, IPAddress: "192.0.2.1"})
		ae, ok := apperr.As(err)
		if !ok || ae.Kind != apperr.KindRateLimited || ae.RetryAfter <= 0 {
			t.Fatalf("err = %v, want rate limited with RetryAfter", err)
		}
	})
	t.Run("existing and non-existing accounts are throttled identically", func(t *testing.T) {
		e := newEnv(t)
		e.addUser("20240001", true, domain.RoleStudent)
		for _, number := range []string{"20240001", "20249999"} {
			for i := 0; i < 3; i++ {
				_ = bad(e, number, "192.0.2.1")
			}
			err := bad(e, number, "192.0.2.1")
			if !errors.Is(err, domain.NewTooManyAttempts(0)) {
				t.Fatalf("%s: err = %v, want too_many_attempts", number, err)
			}
		}
	})
	t.Run("a third party cannot lock the victim out from another IP", func(t *testing.T) {
		e := newEnv(t)
		e.addUser("20240001", true, domain.RoleStudent)
		for i := 0; i < 3; i++ {
			_ = bad(e, "20240001", "198.51.100.66") // attacker
		}
		if _, err := e.svc.Login(ctx, LoginInput{InstitutionalNumber: "20240001", Password: goodPassword, IPAddress: "192.0.2.1"}); err != nil {
			t.Fatalf("victim login from own IP: %v", err)
		}
	})
	t.Run("success resets the account counter", func(t *testing.T) {
		e := newEnv(t)
		e.addUser("20240001", true, domain.RoleStudent)
		_ = bad(e, "20240001", "192.0.2.1")
		_ = bad(e, "20240001", "192.0.2.1")
		if _, err := e.svc.Login(ctx, LoginInput{InstitutionalNumber: "20240001", Password: goodPassword, IPAddress: "192.0.2.1"}); err != nil {
			t.Fatal(err)
		}
		if got := e.limiter.failures["acct:20240001|192.0.2.1"]; got != 0 {
			t.Fatalf("account failures = %d after success", got)
		}
	})
	t.Run("malformed numbers share one key", func(t *testing.T) {
		e := newEnv(t)
		for i := 0; i < 10; i++ {
			_ = bad(e, "garbage-"+string(rune('a'+i)), "192.0.2.1")
		}
		if n := len(e.limiter.failures); n != 2 { // ip key + shared "invalid" account key
			t.Fatalf("limiter keys = %d, want 2", n)
		}
	})
	t.Run("throttled attempts do not reach the password check", func(t *testing.T) {
		e := newEnv(t)
		for i := 0; i < 3; i++ {
			_ = bad(e, "20240001", "192.0.2.1")
		}
		before := e.limiter.failures["ip:192.0.2.1"]
		_ = bad(e, "20240001", "192.0.2.1")
		if e.limiter.failures["ip:192.0.2.1"] != before {
			t.Fatal("blocked attempts must not keep extending the counter")
		}
	})
}

func TestMe(t *testing.T) {
	e := newEnv(t)
	u := e.addUser("20240001", true, domain.RoleStudent)
	got, err := e.svc.Me(context.Background(), principalOf(u))
	if err != nil || got.ID != u.ID {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if _, err := e.svc.Me(context.Background(), domain.Principal{UserID: uuid.New()}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("deleted user: err = %v", err)
	}
}

func TestChangePassword(t *testing.T) {
	const newPass = "a-brand-new-passphrase"
	weak := apperr.New(apperr.KindValidation, "weak_password", "")
	tests := []struct {
		name    string
		current string
		next    string
		want    error // nil means success
	}{
		{"ok", goodPassword, newPass, nil},
		{"wrong current password", "not-the-password", newPass, domain.ErrWrongCurrentPass},
		{"empty current password", "", newPass, domain.ErrWrongCurrentPass},
		{"weak new password", goodPassword, "short", weak},
		{"same password", goodPassword, goodPassword, domain.ErrSamePassword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			u := e.addUser("20240001", true, domain.RoleStudent)
			err := e.svc.ChangePassword(context.Background(), principalOf(u), tt.current, tt.next)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if e.users.byID[u.ID].PasswordHash != "hash:"+goodPassword {
				t.Fatal("password must be unchanged on failure")
			}
		})
	}

	t.Run("success revokes other sessions but keeps current", func(t *testing.T) {
		e := newEnv(t)
		u := e.addUser("20240001", true, domain.RoleStudent)
		a, _ := e.svc.Login(context.Background(), LoginInput{InstitutionalNumber: "20240001", Password: goodPassword})
		b, _ := e.svc.Login(context.Background(), LoginInput{InstitutionalNumber: "20240001", Password: goodPassword})
		pa, _ := e.svc.Authenticate(context.Background(), a.AccessToken)
		if err := e.svc.ChangePassword(context.Background(), pa, goodPassword, newPass); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Authenticate(context.Background(), a.AccessToken); err != nil {
			t.Fatalf("current session must survive: %v", err)
		}
		if _, err := e.svc.Authenticate(context.Background(), b.AccessToken); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("other session must be revoked: %v", err)
		}
		if got := e.users.byID[u.ID]; got.PasswordHash != "hash:"+newPass || got.MustChangePassword {
			t.Fatalf("unexpected user state: %+v", got)
		}
		if len(e.audit.events) != 1 || e.audit.events[0].Action != "identity.password_changed" {
			t.Fatalf("audit = %+v", e.audit.events)
		}
	})
}

func TestResetPassword(t *testing.T) {
	const temp = "temporary-pass-123"

	t.Run("only ADMIN may reset", func(t *testing.T) {
		for _, role := range []domain.Role{domain.RoleStudent, domain.RoleTeacher, domain.RoleTutor, domain.RoleDirector} {
			e := newEnv(t)
			caller := e.addUser("20240001", true, role)
			target := e.addUser("20240002", true, domain.RoleStudent)
			err := e.svc.ResetPassword(context.Background(), principalOf(caller), target.ID, temp)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("role %s: err = %v", role, err)
			}
			if e.users.byID[target.ID].PasswordHash != "hash:"+goodPassword {
				t.Fatalf("role %s changed a password", role)
			}
		}
	})
	t.Run("admin resets, forces change, revokes sessions, audits", func(t *testing.T) {
		e := newEnv(t)
		admin := e.addUser("20240001", true, domain.RoleAdmin)
		target := e.addUser("20240002", true, domain.RoleStudent)
		res, _ := e.svc.Login(context.Background(), LoginInput{InstitutionalNumber: "20240002", Password: goodPassword})

		if err := e.svc.ResetPassword(context.Background(), principalOf(admin), target.ID, temp); err != nil {
			t.Fatal(err)
		}
		got := e.users.byID[target.ID]
		if got.PasswordHash != "hash:"+temp || !got.MustChangePassword {
			t.Fatalf("user = %+v", got)
		}
		if _, err := e.svc.Authenticate(context.Background(), res.AccessToken); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatal("existing sessions must be revoked")
		}
		if len(e.audit.events) != 1 || e.audit.events[0].Action != "identity.password_reset" || e.audit.events[0].EntityID != target.ID.String() {
			t.Fatalf("audit = %+v", e.audit.events)
		}
		for _, v := range e.audit.events[0].Metadata {
			if s, ok := v.(string); ok && s == temp {
				t.Fatal("password leaked into audit metadata")
			}
		}
	})
	t.Run("unknown target", func(t *testing.T) {
		e := newEnv(t)
		admin := e.addUser("20240001", true, domain.RoleAdmin)
		if err := e.svc.ResetPassword(context.Background(), principalOf(admin), uuid.New(), temp); !errors.Is(err, domain.ErrUserNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("weak temporary password", func(t *testing.T) {
		e := newEnv(t)
		admin := e.addUser("20240001", true, domain.RoleAdmin)
		target := e.addUser("20240002", true, domain.RoleStudent)
		if err := e.svc.ResetPassword(context.Background(), principalOf(admin), target.ID, "weak"); err == nil {
			t.Fatal("expected validation error")
		}
	})
}

func TestCreateUser(t *testing.T) {
	in := CreateUserInput{InstitutionalNumber: "20250001", FirstName: "Ana", LastName: "Prueba", Password: goodPassword,
		Roles: []domain.Role{domain.RoleTeacher, domain.RoleTutor, domain.RoleTeacher}}

	t.Run("non admin forbidden", func(t *testing.T) {
		e := newEnv(t)
		caller := e.addUser("20240001", true, domain.RoleDirector)
		if _, err := e.svc.CreateUser(context.Background(), principalOf(caller), in); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("admin creates multi-role user with derived email", func(t *testing.T) {
		e := newEnv(t)
		admin := e.addUser("20240001", true, domain.RoleAdmin)
		u, err := e.svc.CreateUser(context.Background(), principalOf(admin), in)
		if err != nil {
			t.Fatal(err)
		}
		if u.Email != "20250001@uttcampus.edu.mx" || len(u.Roles) != 2 || !u.MustChangePassword || u.PasswordHash == goodPassword {
			t.Fatalf("user = %+v", u)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		e := newEnv(t)
		admin := e.addUser("20240001", true, domain.RoleAdmin)
		_, _ = e.svc.CreateUser(context.Background(), principalOf(admin), in)
		if _, err := e.svc.CreateUser(context.Background(), principalOf(admin), in); !errors.Is(err, domain.ErrDuplicateUser) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("validation", func(t *testing.T) {
		e := newEnv(t)
		admin := e.addUser("20240001", true, domain.RoleAdmin)
		bad := []CreateUserInput{
			{InstitutionalNumber: "123", FirstName: "A", LastName: "B", Password: goodPassword, Roles: []domain.Role{domain.RoleStudent}},
			{InstitutionalNumber: "20250002", FirstName: "", LastName: "B", Password: goodPassword, Roles: []domain.Role{domain.RoleStudent}},
			{InstitutionalNumber: "20250002", FirstName: "A", LastName: "B", Password: goodPassword},
			{InstitutionalNumber: "20250002", FirstName: "A", LastName: "B", Password: goodPassword, Roles: []domain.Role{"ROOT"}},
			{InstitutionalNumber: "20250002", FirstName: "A", LastName: "B", Password: "short", Roles: []domain.Role{domain.RoleStudent}},
		}
		for i, b := range bad {
			if _, err := e.svc.CreateUser(context.Background(), principalOf(admin), b); err == nil {
				t.Fatalf("case %d: expected error", i)
			}
		}
	})
}
