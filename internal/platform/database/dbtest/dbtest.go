// Package dbtest gives integration tests an isolated, fully migrated schema on
// the PostgreSQL pointed to by TEST_DATABASE_URL. Tests are skipped when the
// variable is unset, so `go test ./...` works without a database.
package dbtest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/edutrak/backend/internal/platform/database"
	"github.com/edutrak/backend/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// New creates a throwaway schema, applies every migration inside it and
// returns a DB whose connections use that schema. The schema is dropped on cleanup.
func New(t *testing.T) *database.DB {
	t.Helper()
	db := NewEmpty(t)
	if err := db.Migrate(context.Background(), migrations.FS, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// NewEmpty is like New but applies no migrations, so tests can exercise upgrade paths.
func NewEmpty(t *testing.T) *database.DB {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	schema := "t_" + strings.ReplaceAll(uuid.NewString(), "-", "")

	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA %q`, schema)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), fmt.Sprintf(`DROP SCHEMA %q CASCADE`, schema))
		_ = admin.Close(context.Background())
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	db, err := database.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect to schema: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}
