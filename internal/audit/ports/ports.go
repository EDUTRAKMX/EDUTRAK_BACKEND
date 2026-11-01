// Package ports declares the audit module's outbound interfaces.
package ports

import (
	"context"

	"github.com/edutrak/backend/internal/audit/domain"
)

// Recorder appends an event. Implementations join the caller's transaction
// when ctx carries one, so the audit row commits or rolls back with the change.
type Recorder interface {
	Record(ctx context.Context, e domain.Event) error
}
