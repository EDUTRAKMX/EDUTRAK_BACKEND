// Command migrate applies pending SQL migrations (forward-only).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/edutrak/backend/internal/platform/config"
	"github.com/edutrak/backend/internal/platform/database"
	"github.com/edutrak/backend/internal/platform/logging"
	"github.com/edutrak/backend/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	if err := config.LoadDotEnv(".env"); err != nil {
		return err
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	db, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Migrate(ctx, migrations.FS, logging.New(os.Stderr, cfg.Env, cfg.LogLevel))
}
