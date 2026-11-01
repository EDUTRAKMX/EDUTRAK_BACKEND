// Package domain defines the audit event recorded by every module for
// sensitive operations. Metadata must never contain passwords, tokens,
// secrets or file contents.
package domain

import "github.com/google/uuid"

type Event struct {
	ActorUserID *uuid.UUID // nil for system actions
	Action      string     // e.g. "identity.password_reset"
	EntityType  string
	EntityID    string
	Metadata    map[string]any
}
