// Package db owns the pgx connection pool and the goose migration runner.
// It embeds the migrations (via the migrations subpackage) and exposes a small
// WithTx transaction helper. It may import config only.
package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/osctf/platform/internal/db/migrations"
)

// Connect opens a pgx pool and pings it. It retries the initial connection to
// tolerate compose start ordering (Postgres may not be ready the instant we boot).
func Connect(ctx context.Context, dsn string, log *slog.Logger) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: parse dsn: %w", err)
	}

	const attempts = 10
	var lastErr error
	for i := 1; i <= attempts; i++ {
		pool, perr := pgxpool.NewWithConfig(ctx, cfg)
		if perr == nil {
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			perr = pool.Ping(pingCtx)
			cancel()
			if perr == nil {
				return pool, nil
			}
			pool.Close()
		}
		lastErr = perr
		if i < attempts {
			log.Warn("waiting for postgres", "attempt", i, "error", perr)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(3 * time.Second):
			}
		}
	}
	return nil, fmt.Errorf("db: connect after %d attempts: %w", attempts, lastErr)
}

// Migrate runs all embedded goose migrations up. It opens a short-lived
// database/sql handle over the pgx stdlib driver because goose needs one.
func Migrate(ctx context.Context, dsn string) error {
	sqlDB := stdlib.OpenDB(mustParse(dsn))
	defer func() { _ = sqlDB.Close() }()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("db: set dialect: %w", err)
	}
	goose.SetLogger(goose.NopLogger())
	if err := goose.UpContext(ctx, sqlDB, "."); err != nil {
		return fmt.Errorf("db: migrate up: %w", err)
	}
	return nil
}

func mustParse(dsn string) pgx.ConnConfig {
	cc, err := pgx.ParseConfig(dsn)
	if err != nil {
		// Connect already validated the DSN via pgxpool.ParseConfig before this runs.
		panic(fmt.Sprintf("db: parse dsn for migrations: %v", err))
	}
	return *cc
}

// WithTx runs fn inside a transaction, committing on nil error and rolling back
// otherwise. Services own their transaction boundaries through this helper.
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return errors.Join(err, fmt.Errorf("db: rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit tx: %w", err)
	}
	return nil
}
