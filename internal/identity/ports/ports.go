// Package ports declares what the identity application layer needs from the outside world.
package ports

import (
	"context"
	"time"

	auditdomain "github.com/edutrak/backend/internal/audit/domain"
	"github.com/edutrak/backend/internal/identity/domain"
	"github.com/google/uuid"
)

// UserRepository returns domain.ErrUserNotFound when a user does not exist and
// domain.ErrDuplicateUser from Create on a unique violation.
type UserRepository interface {
	Create(ctx context.Context, u *domain.User) error
	FindByInstitutionalNumber(ctx context.Context, number string) (*domain.User, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	UpdatePassword(ctx context.Context, id uuid.UUID, hash string, mustChange bool) error
}

// SessionRepository returns domain.ErrUnauthenticated when a session is not found.
type SessionRepository interface {
	Create(ctx context.Context, s *domain.Session) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Session, error)
	FindByRefreshTokenHash(ctx context.Context, hash string) (*domain.Session, error)
	// Revoke revokes an active session and reports whether it was active; it is
	// atomic, so only one of several concurrent callers gets true. An already
	// revoked session keeps its original reason.
	Revoke(ctx context.Context, id uuid.UUID, at time.Time, reason domain.RevokeReason) (revoked bool, err error)
	// RevokeAllForUser revokes every ACTIVE session of the user.
	RevokeAllForUser(ctx context.Context, userID uuid.UUID, at time.Time, reason domain.RevokeReason) error
	// RevokeOthersForUser revokes every active session of the user except keepID.
	RevokeOthersForUser(ctx context.Context, userID, keepID uuid.UUID, at time.Time, reason domain.RevokeReason) error
}

// LoginLimiter throttles login attempts. Keys are opaque to the implementation.
// The in-memory adapter is per process; replace it with a shared store (e.g.
// PostgreSQL) if the API is ever deployed with more than one instance.
type LoginLimiter interface {
	// Allow reports whether the key may attempt now, or how long to wait.
	Allow(key string) (allowed bool, retryAfter time.Duration)
	// Fail records a failed attempt for the key.
	Fail(key string)
	// Reset clears the key's failures (after a successful login).
	Reset(key string)
}

type PasswordHasher interface {
	Hash(password string) (string, error)
	// Verify reports whether password matches hash. A malformed hash is an error.
	Verify(password, hash string) (bool, error)
}

type AccessClaims struct {
	UserID    uuid.UUID
	SessionID uuid.UUID
}

type TokenService interface {
	IssueAccessToken(c AccessClaims, now time.Time) (token string, expiresAt time.Time, err error)
	// ParseAccessToken validates signature and expiry; any failure is an error.
	ParseAccessToken(token string, now time.Time) (AccessClaims, error)
	// NewRefreshToken returns a random opaque token and its hash for storage.
	NewRefreshToken() (token, hash string, err error)
	HashRefreshToken(token string) string
}

type Clock interface{ Now() time.Time }

// AuditRecorder is satisfied by the audit module's recorder.
type AuditRecorder interface {
	Record(ctx context.Context, e auditdomain.Event) error
}

// Transactor runs fn atomically; repositories called with the given ctx join the transaction.
type Transactor interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}
