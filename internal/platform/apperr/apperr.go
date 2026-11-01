// Package apperr defines the error taxonomy shared by every module. Domain and
// application code return *Error values; the HTTP adapter maps Kind to a status.
package apperr

import (
	"errors"
	"time"
)

type Kind int

const (
	KindValidation Kind = iota + 1
	KindUnauthenticated
	KindForbidden
	KindNotFound
	KindConflict
	KindRateLimited
)

// Error is an expected (non-infrastructure) failure. Code is a stable,
// machine-readable identifier; Message is safe to show to clients.
// RetryAfter is only meaningful for KindRateLimited.
type Error struct {
	Kind       Kind
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func New(kind Kind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message}
}

// Is matches by Kind and Code so sentinels can be compared with errors.Is.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Kind == e.Kind && t.Code == e.Code
}

// As returns the *Error in err's chain, if any.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
