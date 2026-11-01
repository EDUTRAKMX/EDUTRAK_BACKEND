// Package domain holds identity entities and rules. It knows nothing about
// HTTP, SQL or token formats.
package domain

import (
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/edutrak/backend/internal/platform/apperr"
	"github.com/google/uuid"
)

type Role string

const (
	RoleAdmin    Role = "ADMIN"
	RoleDirector Role = "DIRECTOR"
	RoleTeacher  Role = "TEACHER"
	RoleTutor    Role = "TUTOR"
	RoleStudent  Role = "STUDENT"
)

var validRoles = map[Role]bool{RoleAdmin: true, RoleDirector: true, RoleTeacher: true, RoleTutor: true, RoleStudent: true}

func (r Role) Valid() bool { return validRoles[r] }

const InstitutionalEmailDomain = "uttcampus.edu.mx"

var institutionalNumberRe = regexp.MustCompile(`^[0-9]{8}$`)

// Errors returned by the identity module.
var (
	ErrInvalidCredentials = apperr.New(apperr.KindUnauthenticated, "invalid_credentials", "invalid institutional number or password")
	ErrUnauthenticated    = apperr.New(apperr.KindUnauthenticated, "unauthenticated", "authentication required")
	ErrAccountInactive    = apperr.New(apperr.KindForbidden, "account_inactive", "account is inactive")
	ErrForbidden          = apperr.New(apperr.KindForbidden, "forbidden", "you do not have permission to perform this action")
	ErrUserNotFound       = apperr.New(apperr.KindNotFound, "user_not_found", "user not found")
	ErrDuplicateUser      = apperr.New(apperr.KindConflict, "user_exists", "a user with that institutional number already exists")
	ErrWrongCurrentPass   = apperr.New(apperr.KindValidation, "wrong_current_password", "current password is incorrect")
	ErrSamePassword       = apperr.New(apperr.KindValidation, "same_password", "new password must differ from the current one")
)

// NewTooManyAttempts is returned when login attempts are being throttled.
func NewTooManyAttempts(retryAfter time.Duration) *apperr.Error {
	return &apperr.Error{
		Kind: apperr.KindRateLimited, Code: "too_many_attempts",
		Message: "too many login attempts, try again later", RetryAfter: retryAfter,
	}
}

func validation(code, msg string) *apperr.Error { return apperr.New(apperr.KindValidation, code, msg) }

// User is an account. Roles are loaded together with the user.
type User struct {
	ID                  uuid.UUID
	InstitutionalNumber string
	Email               string
	PasswordHash        string
	FirstName           string
	LastName            string
	IsActive            bool
	MustChangePassword  bool
	Roles               []Role
}

func (u *User) HasRole(r Role) bool {
	for _, have := range u.Roles {
		if have == r {
			return true
		}
	}
	return false
}

// NewInstitutionalEmail derives the institutional address from the number.
func NewInstitutionalEmail(number string) string { return number + "@" + InstitutionalEmailDomain }

func ValidateInstitutionalNumber(n string) error {
	if !institutionalNumberRe.MatchString(n) {
		return validation("invalid_institutional_number", "institutional number must be exactly 8 digits")
	}
	return nil
}

const (
	MinPasswordLen = 10
	MaxPasswordLen = 128
)

// ValidatePassword enforces the password policy (length-based, per NIST guidance).
func ValidatePassword(p string) error {
	n := utf8.RuneCountInString(p)
	switch {
	case n < MinPasswordLen:
		return validation("weak_password", "password must be at least 10 characters")
	case len(p) > MaxPasswordLen:
		return validation("weak_password", "password must be at most 128 bytes")
	}
	return nil
}

// ValidateNewUser checks the fields needed to create an account.
func ValidateNewUser(number, firstName, lastName string, roles []Role) error {
	if err := ValidateInstitutionalNumber(number); err != nil {
		return err
	}
	if firstName == "" || lastName == "" {
		return validation("name_required", "first and last name are required")
	}
	if len(roles) == 0 {
		return validation("role_required", "at least one role is required")
	}
	for _, r := range roles {
		if !r.Valid() {
			return validation("invalid_role", "unknown role")
		}
	}
	return nil
}

// RevokeReason records why a session ended. Only RevokeRotated makes a later
// presentation of the session's refresh token suspicious (see Service.Refresh).
type RevokeReason string

const (
	RevokeRotated         RevokeReason = "ROTATED"          // normal refresh rotation
	RevokeLogout          RevokeReason = "LOGOUT"           // explicit logout
	RevokePasswordChanged RevokeReason = "PASSWORD_CHANGED" // user changed their password
	RevokePasswordReset   RevokeReason = "PASSWORD_RESET"   // an ADMIN reset the password
	RevokeReuseDetected   RevokeReason = "REUSE_DETECTED"   // revoked because a rotated token was replayed
)

// Session is a revocable login. Only the hash of the refresh token is kept.
type Session struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	RefreshTokenHash string
	UserAgent        string
	IPAddress        string
	CreatedAt        time.Time
	ExpiresAt        time.Time
	RevokedAt        *time.Time
	RevokedReason    RevokeReason // empty while the session is not revoked
}

func (s *Session) IsActive(now time.Time) bool {
	return s.RevokedAt == nil && now.Before(s.ExpiresAt)
}

// Principal is the authenticated caller attached to a request.
type Principal struct {
	UserID             uuid.UUID
	SessionID          uuid.UUID
	Roles              []Role
	MustChangePassword bool
}

func (p Principal) HasRole(r Role) bool {
	for _, have := range p.Roles {
		if have == r {
			return true
		}
	}
	return false
}

func (p Principal) HasAnyRole(roles ...Role) bool {
	for _, r := range roles {
		if p.HasRole(r) {
			return true
		}
	}
	return false
}

// RequireAnyRole returns ErrForbidden unless the principal holds one of roles.
func (p Principal) RequireAnyRole(roles ...Role) error {
	if p.HasAnyRole(roles...) {
		return nil
	}
	return ErrForbidden
}

// RequireSelf allows access to a resource owned by ownerID only to its owner.
// Other modules combine it with role checks (role + relationship to resource).
func (p Principal) RequireSelf(ownerID uuid.UUID) error {
	if p.UserID == ownerID {
		return nil
	}
	return ErrForbidden
}
