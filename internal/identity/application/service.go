// Package application implements the identity use cases.
package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	"github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/identity/ports"
	"github.com/google/uuid"
)

type Deps struct {
	Users      ports.UserRepository
	Sessions   ports.SessionRepository
	Hasher     ports.PasswordHasher
	Tokens     ports.TokenService
	Clock      ports.Clock
	Audit      ports.AuditRecorder
	Tx         ports.Transactor
	Limiter    ports.LoginLimiter
	NewID      func() uuid.UUID
	RefreshTTL time.Duration
	// ReuseGrace is how long after a rotation the old refresh token is treated
	// as a benign retry/race (plain 401) rather than as a replay. Zero means
	// DefaultReuseGrace.
	ReuseGrace time.Duration
}

// DefaultReuseGrace covers a client that fires two refreshes at once (two tabs,
// a network retry) without punishing it as if the token had been stolen.
const DefaultReuseGrace = 10 * time.Second

type Service struct {
	Deps
	// dummyHash lets Login spend the same time on unknown users as on known ones.
	dummyHash string
}

func NewService(d Deps) (*Service, error) {
	if d.Limiter == nil {
		return nil, errors.New("identity: login limiter is required")
	}
	if d.ReuseGrace == 0 {
		d.ReuseGrace = DefaultReuseGrace
	}
	var junk [16]byte
	if _, err := rand.Read(junk[:]); err != nil {
		return nil, fmt.Errorf("random: %w", err)
	}
	dummy, err := d.Hasher.Hash(base64.RawURLEncoding.EncodeToString(junk[:]))
	if err != nil {
		return nil, fmt.Errorf("dummy hash: %w", err)
	}
	return &Service{Deps: d, dummyHash: dummy}, nil
}

// AuthResult is returned by Login and Refresh.
type AuthResult struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
	User             *domain.User
}

type LoginInput struct {
	InstitutionalNumber string
	Password            string
	UserAgent           string
	IPAddress           string
}

// Login authenticates by institutional number and password. Attempts are
// throttled per client IP and per (account, IP) BEFORE any user lookup, so the
// throttle behaves identically for existing and non-existing accounts. Keying
// the account counter on the IP too means a third party cannot lock a victim out.
func (s *Service) Login(ctx context.Context, in LoginInput) (*AuthResult, error) {
	number := "invalid" // malformed numbers share one key so garbage cannot bloat the limiter
	if domain.ValidateInstitutionalNumber(in.InstitutionalNumber) == nil {
		number = in.InstitutionalNumber
	}
	keys := []string{"ip:" + in.IPAddress, "acct:" + number + "|" + in.IPAddress}
	for _, k := range keys {
		if ok, wait := s.Limiter.Allow(k); !ok {
			return nil, domain.NewTooManyAttempts(wait)
		}
	}
	res, err := s.attemptLogin(ctx, in)
	switch {
	case errors.Is(err, domain.ErrInvalidCredentials):
		for _, k := range keys {
			s.Limiter.Fail(k)
		}
	case err == nil:
		s.Limiter.Reset(keys[1])
	}
	return res, err
}

func (s *Service) attemptLogin(ctx context.Context, in LoginInput) (*AuthResult, error) {
	if domain.ValidateInstitutionalNumber(in.InstitutionalNumber) != nil || in.Password == "" {
		return nil, domain.ErrInvalidCredentials
	}
	user, err := s.Users.FindByInstitutionalNumber(ctx, in.InstitutionalNumber)
	if errors.Is(err, domain.ErrUserNotFound) {
		_, _ = s.Hasher.Verify(in.Password, s.dummyHash)
		return nil, domain.ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("find user: %w", err)
	}
	ok, err := s.Hasher.Verify(in.Password, user.PasswordHash)
	if err != nil {
		return nil, fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return nil, domain.ErrInvalidCredentials
	}
	if !user.IsActive {
		return nil, domain.ErrAccountInactive
	}
	return s.startSession(ctx, user, in.UserAgent, in.IPAddress)
}

func (s *Service) startSession(ctx context.Context, user *domain.User, userAgent, ip string) (*AuthResult, error) {
	now := s.Clock.Now()
	refresh, refreshHash, err := s.Tokens.NewRefreshToken()
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	sess := &domain.Session{
		ID:               s.NewID(),
		UserID:           user.ID,
		RefreshTokenHash: refreshHash,
		UserAgent:        userAgent,
		IPAddress:        ip,
		CreatedAt:        now,
		ExpiresAt:        now.Add(s.RefreshTTL),
	}
	if err := s.Sessions.Create(ctx, sess); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	access, accessExp, err := s.Tokens.IssueAccessToken(ports.AccessClaims{UserID: user.ID, SessionID: sess.ID}, now)
	if err != nil {
		return nil, fmt.Errorf("access token: %w", err)
	}
	return &AuthResult{
		AccessToken: access, AccessExpiresAt: accessExp,
		RefreshToken: refresh, RefreshExpiresAt: sess.ExpiresAt,
		User: user,
	}, nil
}

// Authenticate validates an access token against its (revocable) session and
// the current state of the user, returning the caller's Principal.
func (s *Service) Authenticate(ctx context.Context, accessToken string) (domain.Principal, error) {
	now := s.Clock.Now()
	claims, err := s.Tokens.ParseAccessToken(accessToken, now)
	if err != nil {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	sess, err := s.Sessions.FindByID(ctx, claims.SessionID)
	if errors.Is(err, domain.ErrUnauthenticated) {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	if err != nil {
		return domain.Principal{}, fmt.Errorf("find session: %w", err)
	}
	if sess.UserID != claims.UserID || !sess.IsActive(now) {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	user, err := s.Users.FindByID(ctx, claims.UserID)
	if errors.Is(err, domain.ErrUserNotFound) {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	if err != nil {
		return domain.Principal{}, fmt.Errorf("find user: %w", err)
	}
	if !user.IsActive {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	return domain.Principal{UserID: user.ID, SessionID: sess.ID, Roles: user.Roles, MustChangePassword: user.MustChangePassword}, nil
}

// Refresh rotates the refresh token (the old session is revoked with reason
// ROTATED and a new one is created).
//
// An already-revoked token is handled by revocation reason:
//   - LOGOUT, PASSWORD_*, REUSE_DETECTED: plain 401. A stale client must not log
//     the user out of their other devices.
//   - ROTATED within ReuseGrace of the rotation: plain 401 (concurrent or retried
//     refresh).
//   - ROTATED after the grace period: the superseded token is being replayed, so
//     every active session of the user is revoked (REUSE_DETECTED) and the event
//     is audited.
func (s *Service) Refresh(ctx context.Context, refreshToken, userAgent, ip string) (*AuthResult, error) {
	if refreshToken == "" {
		return nil, domain.ErrUnauthenticated
	}
	now := s.Clock.Now()
	sess, err := s.Sessions.FindByRefreshTokenHash(ctx, s.Tokens.HashRefreshToken(refreshToken))
	if errors.Is(err, domain.ErrUnauthenticated) {
		return nil, domain.ErrUnauthenticated
	}
	if err != nil {
		return nil, fmt.Errorf("find session: %w", err)
	}
	if sess.RevokedAt != nil {
		return nil, s.handleRevokedRefresh(ctx, sess, now)
	}
	if !sess.IsActive(now) {
		return nil, domain.ErrUnauthenticated
	}
	user, err := s.Users.FindByID(ctx, sess.UserID)
	if errors.Is(err, domain.ErrUserNotFound) {
		return nil, domain.ErrUnauthenticated
	}
	if err != nil {
		return nil, fmt.Errorf("find user: %w", err)
	}
	if !user.IsActive {
		return nil, domain.ErrUnauthenticated
	}

	var res *AuthResult
	err = s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		revoked, err := s.Sessions.Revoke(ctx, sess.ID, now, domain.RevokeRotated)
		if err != nil {
			return fmt.Errorf("revoke session: %w", err)
		}
		if !revoked { // lost a race with a concurrent refresh
			return domain.ErrUnauthenticated
		}
		res, err = s.startSession(ctx, user, userAgent, ip)
		return err
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// handleRevokedRefresh always returns an error: ErrUnauthenticated, or an
// infrastructure error if replay handling itself fails.
func (s *Service) handleRevokedRefresh(ctx context.Context, sess *domain.Session, now time.Time) error {
	if sess.RevokedReason != domain.RevokeRotated || now.Sub(*sess.RevokedAt) <= s.ReuseGrace {
		return domain.ErrUnauthenticated
	}
	err := s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.Sessions.RevokeAllForUser(ctx, sess.UserID, now, domain.RevokeReuseDetected); err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
		// Actor is unknown (the legitimate user or a thief), so it is left empty.
		return s.record(ctx, nil, "identity.refresh_reuse_detected", sess.UserID,
			map[string]any{"session_id": sess.ID.String()})
	})
	if err != nil {
		return err
	}
	return domain.ErrUnauthenticated
}

// Logout revokes the caller's session. It is idempotent: logging out an
// already-ended session succeeds without writing a second audit row.
func (s *Service) Logout(ctx context.Context, p domain.Principal) error {
	now := s.Clock.Now()
	return s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		revoked, err := s.Sessions.Revoke(ctx, p.SessionID, now, domain.RevokeLogout)
		if err != nil {
			return fmt.Errorf("revoke session: %w", err)
		}
		if !revoked {
			return nil
		}
		return s.record(ctx, &p.UserID, "identity.logout", p.UserID, map[string]any{"session_id": p.SessionID.String()})
	})
}

// Me returns the caller's own account.
func (s *Service) Me(ctx context.Context, p domain.Principal) (*domain.User, error) {
	u, err := s.Users.FindByID(ctx, p.UserID)
	if errors.Is(err, domain.ErrUserNotFound) {
		return nil, domain.ErrUnauthenticated
	}
	if err != nil {
		return nil, fmt.Errorf("find user: %w", err)
	}
	return u, nil
}

// ChangePassword requires the current password, keeps the session it was made
// from and revokes every other session of the user (approved policy).
func (s *Service) ChangePassword(ctx context.Context, p domain.Principal, current, next string) error {
	user, err := s.Users.FindByID(ctx, p.UserID)
	if errors.Is(err, domain.ErrUserNotFound) {
		return domain.ErrUnauthenticated
	}
	if err != nil {
		return fmt.Errorf("find user: %w", err)
	}
	ok, err := s.Hasher.Verify(current, user.PasswordHash)
	if err != nil {
		return fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return domain.ErrWrongCurrentPass
	}
	if err := domain.ValidatePassword(next); err != nil {
		return err
	}
	if next == current {
		return domain.ErrSamePassword
	}
	hash, err := s.Hasher.Hash(next)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	now := s.Clock.Now()
	return s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.Users.UpdatePassword(ctx, user.ID, hash, false); err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		if err := s.Sessions.RevokeOthersForUser(ctx, user.ID, p.SessionID, now, domain.RevokePasswordChanged); err != nil {
			return fmt.Errorf("revoke other sessions: %w", err)
		}
		return s.record(ctx, &p.UserID, "identity.password_changed", user.ID, nil)
	})
}

// ResetPassword lets an ADMIN set a temporary password for any account. The
// user must change it at next login and all their sessions are revoked.
func (s *Service) ResetPassword(ctx context.Context, p domain.Principal, targetID uuid.UUID, temporary string) error {
	if err := p.RequireAnyRole(domain.RoleAdmin); err != nil {
		return err
	}
	if err := domain.ValidatePassword(temporary); err != nil {
		return err
	}
	target, err := s.Users.FindByID(ctx, targetID)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			return err
		}
		return fmt.Errorf("find user: %w", err)
	}
	hash, err := s.Hasher.Hash(temporary)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	now := s.Clock.Now()
	return s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.Users.UpdatePassword(ctx, target.ID, hash, true); err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		if err := s.Sessions.RevokeAllForUser(ctx, target.ID, now, domain.RevokePasswordReset); err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
		return s.record(ctx, &p.UserID, "identity.password_reset", target.ID, nil)
	})
}

type CreateUserInput struct {
	InstitutionalNumber string
	FirstName           string
	LastName            string
	Password            string
	Roles               []domain.Role
}

// CreateUser lets an ADMIN create an account. The new user must change the
// provided password at first login.
func (s *Service) CreateUser(ctx context.Context, p domain.Principal, in CreateUserInput) (*domain.User, error) {
	if err := p.RequireAnyRole(domain.RoleAdmin); err != nil {
		return nil, err
	}
	return s.createUser(ctx, &p.UserID, in, true)
}

// BootstrapUser creates an account without a caller (used by the seed command
// to create the first ADMIN). It is not reachable over HTTP.
func (s *Service) BootstrapUser(ctx context.Context, in CreateUserInput) (*domain.User, error) {
	return s.createUser(ctx, nil, in, false)
}

func (s *Service) createUser(ctx context.Context, actor *uuid.UUID, in CreateUserInput, mustChange bool) (*domain.User, error) {
	if err := domain.ValidateNewUser(in.InstitutionalNumber, in.FirstName, in.LastName, in.Roles); err != nil {
		return nil, err
	}
	if err := domain.ValidatePassword(in.Password); err != nil {
		return nil, err
	}
	hash, err := s.Hasher.Hash(in.Password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	u := &domain.User{
		ID:                  s.NewID(),
		InstitutionalNumber: in.InstitutionalNumber,
		Email:               domain.NewInstitutionalEmail(in.InstitutionalNumber),
		PasswordHash:        hash,
		FirstName:           in.FirstName,
		LastName:            in.LastName,
		IsActive:            true,
		MustChangePassword:  mustChange,
		Roles:               dedupeRoles(in.Roles),
	}
	err = s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.Users.Create(ctx, u); err != nil {
			return err
		}
		return s.record(ctx, actor, "identity.user_created", u.ID, map[string]any{"roles": u.Roles})
	})
	if err != nil {
		if errors.Is(err, domain.ErrDuplicateUser) {
			return nil, err
		}
		return nil, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

func (s *Service) record(ctx context.Context, actor *uuid.UUID, action string, entity uuid.UUID, meta map[string]any) error {
	if err := s.Audit.Record(ctx, auditdomain.Event{
		ActorUserID: actor, Action: action, EntityType: "user", EntityID: entity.String(), Metadata: meta,
	}); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}

func dedupeRoles(in []domain.Role) []domain.Role {
	seen := make(map[domain.Role]bool, len(in))
	out := make([]domain.Role, 0, len(in))
	for _, r := range in {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}
