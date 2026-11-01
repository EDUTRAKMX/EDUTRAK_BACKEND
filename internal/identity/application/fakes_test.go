package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	"github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/identity/ports"
	"github.com/google/uuid"
)

var t0 = time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type fakeUsers struct {
	byID map[uuid.UUID]*domain.User
}

func (f *fakeUsers) Create(_ context.Context, u *domain.User) error {
	for _, e := range f.byID {
		if e.InstitutionalNumber == u.InstitutionalNumber {
			return domain.ErrDuplicateUser
		}
	}
	c := *u
	f.byID[u.ID] = &c
	return nil
}
func (f *fakeUsers) FindByInstitutionalNumber(_ context.Context, n string) (*domain.User, error) {
	for _, u := range f.byID {
		if u.InstitutionalNumber == n {
			c := *u
			return &c, nil
		}
	}
	return nil, domain.ErrUserNotFound
}
func (f *fakeUsers) FindByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	if u, ok := f.byID[id]; ok {
		c := *u
		return &c, nil
	}
	return nil, domain.ErrUserNotFound
}
func (f *fakeUsers) UpdatePassword(_ context.Context, id uuid.UUID, hash string, must bool) error {
	u, ok := f.byID[id]
	if !ok {
		return domain.ErrUserNotFound
	}
	u.PasswordHash, u.MustChangePassword = hash, must
	return nil
}

type fakeSessions struct{ byID map[uuid.UUID]*domain.Session }

func (f *fakeSessions) Create(_ context.Context, s *domain.Session) error {
	c := *s
	f.byID[s.ID] = &c
	return nil
}
func (f *fakeSessions) FindByID(_ context.Context, id uuid.UUID) (*domain.Session, error) {
	if s, ok := f.byID[id]; ok {
		c := *s
		return &c, nil
	}
	return nil, domain.ErrUnauthenticated
}
func (f *fakeSessions) FindByRefreshTokenHash(_ context.Context, h string) (*domain.Session, error) {
	for _, s := range f.byID {
		if s.RefreshTokenHash == h {
			c := *s
			return &c, nil
		}
	}
	return nil, domain.ErrUnauthenticated
}
func (f *fakeSessions) Revoke(_ context.Context, id uuid.UUID, at time.Time, why domain.RevokeReason) (bool, error) {
	s, ok := f.byID[id]
	if !ok || s.RevokedAt != nil {
		return false, nil
	}
	s.RevokedAt, s.RevokedReason = &at, why
	return true, nil
}
func (f *fakeSessions) RevokeAllForUser(_ context.Context, uid uuid.UUID, at time.Time, why domain.RevokeReason) error {
	for _, s := range f.byID {
		if s.UserID == uid && s.RevokedAt == nil {
			s.RevokedAt, s.RevokedReason = &at, why
		}
	}
	return nil
}
func (f *fakeSessions) RevokeOthersForUser(_ context.Context, uid, keep uuid.UUID, at time.Time, why domain.RevokeReason) error {
	for _, s := range f.byID {
		if s.UserID == uid && s.ID != keep && s.RevokedAt == nil {
			s.RevokedAt, s.RevokedReason = &at, why
		}
	}
	return nil
}

// activeSessions counts non-revoked sessions of a user.
func (f *fakeSessions) activeSessions(uid uuid.UUID) int {
	n := 0
	for _, s := range f.byID {
		if s.UserID == uid && s.RevokedAt == nil {
			n++
		}
	}
	return n
}

// fakeLimiter blocks a key for `block` after `max` failures; time is not modelled.
type fakeLimiter struct {
	max      int
	failures map[string]int
	resets   []string
}

func newFakeLimiter(max int) *fakeLimiter { return &fakeLimiter{max: max, failures: map[string]int{}} }
func (f *fakeLimiter) Allow(key string) (bool, time.Duration) {
	if f.failures[key] >= f.max {
		return false, time.Minute
	}
	return true, 0
}
func (f *fakeLimiter) Fail(key string)  { f.failures[key]++ }
func (f *fakeLimiter) Reset(key string) { delete(f.failures, key); f.resets = append(f.resets, key) }

type fakeHasher struct{}

func (fakeHasher) Hash(p string) (string, error) { return "hash:" + p, nil }
func (fakeHasher) Verify(p, h string) (bool, error) {
	if !strings.HasPrefix(h, "hash:") {
		return false, errors.New("malformed hash")
	}
	return h == "hash:"+p, nil
}

type fakeTokens struct {
	n int
}

// Access tokens look like "access|<user>|<session>|<unix expiry>".
func (f *fakeTokens) IssueAccessToken(c ports.AccessClaims, now time.Time) (string, time.Time, error) {
	exp := now.Add(15 * time.Minute)
	return fmt.Sprintf("access|%s|%s|%d", c.UserID, c.SessionID, exp.Unix()), exp, nil
}
func (f *fakeTokens) ParseAccessToken(tok string, now time.Time) (ports.AccessClaims, error) {
	parts := strings.Split(tok, "|")
	if len(parts) != 4 || parts[0] != "access" {
		return ports.AccessClaims{}, errors.New("invalid token")
	}
	var exp int64
	fmt.Sscan(parts[3], &exp)
	if now.Unix() >= exp {
		return ports.AccessClaims{}, errors.New("expired")
	}
	uid, err1 := uuid.Parse(parts[1])
	sid, err2 := uuid.Parse(parts[2])
	if err1 != nil || err2 != nil {
		return ports.AccessClaims{}, errors.New("invalid ids")
	}
	return ports.AccessClaims{UserID: uid, SessionID: sid}, nil
}
func (f *fakeTokens) NewRefreshToken() (string, string, error) {
	f.n++
	tok := fmt.Sprintf("refresh-%d", f.n)
	return tok, f.HashRefreshToken(tok), nil
}
func (f *fakeTokens) HashRefreshToken(t string) string { return "h(" + t + ")" }

type fakeAudit struct{ events []auditdomain.Event }

func (f *fakeAudit) Record(_ context.Context, e auditdomain.Event) error {
	f.events = append(f.events, e)
	return nil
}

type fakeTx struct{}

func (fakeTx) WithinTx(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

type env struct {
	svc      *Service
	users    *fakeUsers
	sessions *fakeSessions
	audit    *fakeAudit
	clock    *fakeClock
	limiter  *fakeLimiter
}

// auditActions returns the recorded audit actions in order.
func (e *env) auditActions() []string {
	out := make([]string, len(e.audit.events))
	for i, ev := range e.audit.events {
		out[i] = ev.Action
	}
	return out
}

const goodPassword = "correct-horse-battery"

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		users:    &fakeUsers{byID: map[uuid.UUID]*domain.User{}},
		sessions: &fakeSessions{byID: map[uuid.UUID]*domain.Session{}},
		audit:    &fakeAudit{},
		clock:    &fakeClock{now: t0},
		limiter:  newFakeLimiter(3),
	}
	svc, err := NewService(Deps{
		Users: e.users, Sessions: e.sessions, Hasher: fakeHasher{}, Tokens: &fakeTokens{},
		Clock: e.clock, Audit: e.audit, Tx: fakeTx{}, Limiter: e.limiter, NewID: uuid.New, RefreshTTL: 24 * time.Hour,
		ReuseGrace: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	return e
}

// addUser stores a user with fictitious data and returns it.
func (e *env) addUser(number string, active bool, roles ...domain.Role) *domain.User {
	u := &domain.User{
		ID: uuid.New(), InstitutionalNumber: number, Email: domain.NewInstitutionalEmail(number),
		PasswordHash: "hash:" + goodPassword, FirstName: "Test", LastName: "User", IsActive: active, Roles: roles,
	}
	e.users.byID[u.ID] = u
	return u
}

func principalOf(u *domain.User) domain.Principal {
	return domain.Principal{UserID: u.ID, SessionID: uuid.New(), Roles: u.Roles}
}
