// Package testsupport spins up ephemeral Postgres and Redis via testcontainers
// for integration tests. Tests that call these helpers are named *Integration
// and run under `go test -run Integration`; they skip when Docker is absent.
package testsupport

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/osctf/platform/internal/db"
)

// Postgres starts a throwaway Postgres, migrates it, and returns a connected pool.
func Postgres(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("osctf"),
		postgres.WithUsername("osctf"),
		postgres.WithPassword("osctf"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Skipf("testsupport: cannot start postgres (docker unavailable?): %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("testsupport: connection string: %v", err)
	}
	if err := db.Migrate(ctx, dsn); err != nil {
		t.Fatalf("testsupport: migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dsn, discardLogger())
	if err != nil {
		t.Fatalf("testsupport: connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, dsn
}

// Redis starts a throwaway Redis and returns a connected client.
func Redis(t *testing.T) *redis.Client {
	t.Helper()
	ctx := context.Background()
	container, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Skipf("testsupport: cannot start redis (docker unavailable?): %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	uri, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("testsupport: redis uri: %v", err)
	}
	opt, err := redis.ParseURL(uri)
	if err != nil {
		t.Fatalf("testsupport: parse redis uri: %v", err)
	}
	client := redis.NewClient(opt)
	t.Cleanup(func() { _ = client.Close() })
	return client
}
