package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPrincipalAuthorization(t *testing.T) {
	owner := uuid.New()
	teacherTutor := Principal{UserID: owner, Roles: []Role{RoleTeacher, RoleTutor}}

	tests := []struct {
		name  string
		roles []Role
		ok    bool
	}{
		{"has one of several", []Role{RoleTutor, RoleDirector}, true},
		{"has second role", []Role{RoleTeacher}, true},
		{"lacks role", []Role{RoleAdmin, RoleStudent}, false},
		{"no roles required denies", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := teacherTutor.RequireAnyRole(tt.roles...)
			if tt.ok != (err == nil) || (!tt.ok && !errors.Is(err, ErrForbidden)) {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
		})
	}

	if err := teacherTutor.RequireSelf(owner); err != nil {
		t.Fatalf("own resource: %v", err)
	}
	if err := teacherTutor.RequireSelf(uuid.New()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign resource: %v", err)
	}
}

func TestValidateInstitutionalNumber(t *testing.T) {
	for n, ok := range map[string]bool{
		"20240001": true, "00000000": true,
		"2024001": false, "202400011": false, "2024000a": false, "": false, " 2024001": false, "２０２４０００１": false,
	} {
		if err := ValidateInstitutionalNumber(n); (err == nil) != ok {
			t.Errorf("%q: err = %v, want ok=%v", n, err, ok)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	for name, tt := range map[string]struct {
		p  string
		ok bool
	}{
		"too short":       {"123456789", false},
		"minimum":         {"1234567890", true},
		"passphrase":      {"correct horse battery", true},
		"too long":        {strings.Repeat("a", MaxPasswordLen+1), false},
		"max":             {strings.Repeat("a", MaxPasswordLen), true},
		"unicode counted": {"contraseña-ñandú", true},
	} {
		if err := ValidatePassword(tt.p); (err == nil) != tt.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, tt.ok)
		}
	}
}

func TestSessionIsActive(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	revoked := now.Add(-time.Minute)
	tests := []struct {
		name string
		s    Session
		want bool
	}{
		{"active", Session{ExpiresAt: now.Add(time.Hour)}, true},
		{"expired", Session{ExpiresAt: now.Add(-time.Second)}, false},
		{"expires exactly now", Session{ExpiresAt: now}, false},
		{"revoked", Session{ExpiresAt: now.Add(time.Hour), RevokedAt: &revoked}, false},
	}
	for _, tt := range tests {
		if got := tt.s.IsActive(now); got != tt.want {
			t.Errorf("%s: got %v", tt.name, got)
		}
	}
}
