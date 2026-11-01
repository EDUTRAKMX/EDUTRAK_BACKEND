// Package postgres implements the identity repositories on PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type UserRepository struct{ db *database.DB }

func NewUserRepository(db *database.DB) *UserRepository { return &UserRepository{db: db} }

const userColumns = `u.id, u.institutional_number, u.institutional_email, u.password_hash, u.first_name, u.last_name,
	u.is_active, u.must_change_password,
	COALESCE((SELECT array_agg(r.code ORDER BY r.id) FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = u.id), '{}')`

func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	var roles []string
	err := row.Scan(&u.ID, &u.InstitutionalNumber, &u.Email, &u.PasswordHash, &u.FirstName, &u.LastName,
		&u.IsActive, &u.MustChangePassword, &roles)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	for _, r := range roles {
		u.Roles = append(u.Roles, domain.Role(r))
	}
	return &u, nil
}

func (r *UserRepository) FindByInstitutionalNumber(ctx context.Context, number string) (*domain.User, error) {
	return scanUser(r.db.Q(ctx).QueryRow(ctx, `SELECT `+userColumns+` FROM users u WHERE u.institutional_number = $1`, number))
}

func (r *UserRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	return scanUser(r.db.Q(ctx).QueryRow(ctx, `SELECT `+userColumns+` FROM users u WHERE u.id = $1`, id))
}

// Create inserts the user and its roles. Call it inside a transaction (the
// application layer does) so a failure cannot leave a user without roles.
func (r *UserRepository) Create(ctx context.Context, u *domain.User) error {
	q := r.db.Q(ctx)
	_, err := q.Exec(ctx,
		`INSERT INTO users (id, institutional_number, institutional_email, password_hash, first_name, last_name, is_active, must_change_password)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		u.ID, u.InstitutionalNumber, u.Email, u.PasswordHash, u.FirstName, u.LastName, u.IsActive, u.MustChangePassword)
	if _, dup := database.IsUniqueViolation(err); dup {
		return domain.ErrDuplicateUser
	}
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	codes := make([]string, len(u.Roles))
	for i, role := range u.Roles {
		codes[i] = string(role)
	}
	tag, err := q.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE code = ANY($2)`, u.ID, codes)
	if err != nil {
		return fmt.Errorf("insert user roles: %w", err)
	}
	if int(tag.RowsAffected()) != len(codes) {
		return errors.New("insert user roles: unknown role")
	}
	return nil
}

func (r *UserRepository) UpdatePassword(ctx context.Context, id uuid.UUID, hash string, mustChange bool) error {
	tag, err := r.db.Q(ctx).Exec(ctx,
		`UPDATE users SET password_hash = $2, must_change_password = $3, updated_at = now() WHERE id = $1`, id, hash, mustChange)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

type SessionRepository struct{ db *database.DB }

func NewSessionRepository(db *database.DB) *SessionRepository { return &SessionRepository{db: db} }

const sessionColumns = `id, user_id, refresh_token_hash, COALESCE(user_agent, ''), COALESCE(host(ip_address), ''), created_at, expires_at, revoked_at, COALESCE(revoked_reason, '')`

func scanSession(row pgx.Row) (*domain.Session, error) {
	var s domain.Session
	var reason string
	err := row.Scan(&s.ID, &s.UserID, &s.RefreshTokenHash, &s.UserAgent, &s.IPAddress, &s.CreatedAt, &s.ExpiresAt, &s.RevokedAt, &reason)
	s.RevokedReason = domain.RevokeReason(reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SessionRepository) Create(ctx context.Context, s *domain.Session) error {
	_, err := r.db.Q(ctx).Exec(ctx,
		`INSERT INTO auth_sessions (id, user_id, refresh_token_hash, user_agent, ip_address, created_at, expires_at)
		 VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, '')::inet, $6, $7)`,
		s.ID, s.UserID, s.RefreshTokenHash, truncate(s.UserAgent, 255), s.IPAddress, s.CreatedAt, s.ExpiresAt)
	return err
}

func (r *SessionRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Session, error) {
	return scanSession(r.db.Q(ctx).QueryRow(ctx, `SELECT `+sessionColumns+` FROM auth_sessions WHERE id = $1`, id))
}

func (r *SessionRepository) FindByRefreshTokenHash(ctx context.Context, hash string) (*domain.Session, error) {
	return scanSession(r.db.Q(ctx).QueryRow(ctx, `SELECT `+sessionColumns+` FROM auth_sessions WHERE refresh_token_hash = $1`, hash))
}

func (r *SessionRepository) Revoke(ctx context.Context, id uuid.UUID, at time.Time, reason domain.RevokeReason) (bool, error) {
	tag, err := r.db.Q(ctx).Exec(ctx,
		`UPDATE auth_sessions SET revoked_at = $2, revoked_reason = $3 WHERE id = $1 AND revoked_at IS NULL`, id, at, string(reason))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *SessionRepository) RevokeAllForUser(ctx context.Context, userID uuid.UUID, at time.Time, reason domain.RevokeReason) error {
	_, err := r.db.Q(ctx).Exec(ctx,
		`UPDATE auth_sessions SET revoked_at = $2, revoked_reason = $3 WHERE user_id = $1 AND revoked_at IS NULL`, userID, at, string(reason))
	return err
}

func (r *SessionRepository) RevokeOthersForUser(ctx context.Context, userID, keepID uuid.UUID, at time.Time, reason domain.RevokeReason) error {
	_, err := r.db.Q(ctx).Exec(ctx,
		`UPDATE auth_sessions SET revoked_at = $3, revoked_reason = $4 WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL`,
		userID, keepID, at, string(reason))
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
