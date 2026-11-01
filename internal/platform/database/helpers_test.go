package database_test

import (
	"io"
	"io/fs"
	"log/slog"

	"github.com/edutrak/backend/migrations"
)

func migrationsFS() fs.FS         { return migrations.FS }
func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
