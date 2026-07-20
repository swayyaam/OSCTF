// Package seed performs idempotent first-boot seeding: the admin account, the
// default event (M4), and the example challenges (M10).
package seed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/config"
	"github.com/osctf/platform/internal/db/gen"
)

// insecureDefaultPasswords are compose-file defaults that trigger a boot warning.
var insecureDefaultPasswords = map[string]struct{}{
	"change-me-now": {},
	"changeme":      {},
}

// EnsureAdmin creates the seed admin from OSCTF_ADMIN_EMAIL / OSCTF_ADMIN_PASSWORD
// if no user with that email exists. Idempotent: an existing user is left untouched.
func EnsureAdmin(ctx context.Context, q *gen.Queries, cfg *config.Config, log *slog.Logger) error {
	if cfg.AdminEmail == "" || cfg.AdminPassword == "" {
		return errors.New("seed: OSCTF_ADMIN_EMAIL and OSCTF_ADMIN_PASSWORD are required on first boot")
	}
	if _, insecure := insecureDefaultPasswords[cfg.AdminPassword]; insecure {
		log.Warn("SECURITY: the admin password is the compose-file default — change OSCTF_ADMIN_PASSWORD before the event")
	}

	_, err := q.GetUserByEmail(ctx, cfg.AdminEmail)
	if err == nil {
		return nil // already exists; leave untouched
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("seed: checking admin: %w", err)
	}

	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return fmt.Errorf("seed: hashing admin password: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("seed: generating id: %w", err)
	}
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{
		ID:           id,
		Username:     "admin",
		Email:        cfg.AdminEmail,
		PasswordHash: hash,
		Role:         "admin",
		Hidden:       true,
	}); err != nil {
		return fmt.Errorf("seed: creating admin: %w", err)
	}
	log.Info("seeded admin account", "email", cfg.AdminEmail, "username", "admin")
	return nil
}
