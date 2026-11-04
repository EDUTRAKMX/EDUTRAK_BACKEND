// Command seed creates the first ADMIN account from SEED_ADMIN_NUMBER and
// SEED_ADMIN_PASSWORD. It is idempotent: an existing account is left untouched.
//
//	go run ./cmd/seed        first ADMIN (any environment)
//	go run ./cmd/seed demo   fictitious demonstration data (never when APP_ENV=production)
//
// Use only fictitious data in development.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/edutrak/backend/internal/app"
	"github.com/edutrak/backend/internal/identity/application"
	"github.com/edutrak/backend/internal/identity/domain"
	"github.com/edutrak/backend/internal/platform/config"
	"github.com/edutrak/backend/internal/platform/database"
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
	if len(os.Args) > 1 && os.Args[1] == "demo" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if cfg.Env == "production" {
			return errors.New("refusing to create demo data when APP_ENV=production")
		}
		db, err := database.Connect(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer db.Close()
		return runDemo(ctx, cfg, db)
	}
	if len(os.Args) > 1 {
		return fmt.Errorf("unknown argument %q (only \"demo\" is supported)", os.Args[1])
	}
	number := strings.TrimSpace(os.Getenv("SEED_ADMIN_NUMBER"))
	password := os.Getenv("SEED_ADMIN_PASSWORD")
	if number == "" || password == "" {
		return errors.New("SEED_ADMIN_NUMBER and SEED_ADMIN_PASSWORD are required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	db, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	svc, err := app.NewIdentityService(db, cfg)
	if err != nil {
		return err
	}
	_, err = svc.BootstrapUser(ctx, application.CreateUserInput{
		InstitutionalNumber: number, FirstName: "Admin", LastName: "EDUTRAK",
		Password: password, Roles: []domain.Role{domain.RoleAdmin},
	})
	switch {
	case errors.Is(err, domain.ErrDuplicateUser):
		fmt.Println("admin already exists; nothing to do")
		return nil
	case err != nil:
		return err
	}
	fmt.Println("admin created:", number)
	return nil
}
