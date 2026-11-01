// Package postgres persists audit events in audit_logs.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/edutrak/backend/internal/audit/domain"
	"github.com/edutrak/backend/internal/platform/database"
)

type Recorder struct{ db *database.DB }

func NewRecorder(db *database.DB) *Recorder { return &Recorder{db: db} }

func (r *Recorder) Record(ctx context.Context, e domain.Event) error {
	meta := e.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal audit metadata: %w", err)
	}
	_, err = r.db.Q(ctx).Exec(ctx,
		`INSERT INTO audit_logs (actor_user_id, action, entity_type, entity_id, metadata) VALUES ($1, $2, $3, $4, $5)`,
		e.ActorUserID, e.Action, e.EntityType, e.EntityID, raw)
	if err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}
