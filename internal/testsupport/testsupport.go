// Package testsupport spins up ephemeral Postgres and Redis via testcontainers
// for integration tests. Tests that call these helpers are named *Integration
// and run under `go test -run Integration`; they skip when Docker is absent.
package testsupport

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/client"
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
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
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
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
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
	rc := redis.NewClient(opt)
	t.Cleanup(func() { _ = rc.Close() })
	return rc
}

// RedisPausable starts a throwaway Redis and returns the client plus pause/unpause controls that
// docker-PAUSE the container — freezing the process while PRESERVING its data. That is the faithful
// way to model a Redis outage and recovery for a live event: while paused, client ops hit real
// dial/read timeouts (bounded below so the test fails fast, not hangs), and on unpause the sessions
// and freeze snapshot are intact so "does it resume?" is a real question. Preferred over closing the
// client, which would mock away the timeout and reconnect paths this exists to exercise.
func RedisPausable(t *testing.T) (rdb *redis.Client, pause, unpause func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ctx := context.Background()
	container, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Skipf("testsupport: cannot start redis (docker unavailable?): %v", err)
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("testsupport: docker client: %v", err)
	}
	id := container.GetContainerID()
	// Always unpause before terminate — a paused container cannot be removed — and close both clients.
	t.Cleanup(func() {
		_ = cli.ContainerUnpause(ctx, id)
		_ = container.Terminate(ctx)
		_ = cli.Close()
	})

	uri, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("testsupport: redis uri: %v", err)
	}
	opt, err := redis.ParseURL(uri)
	if err != nil {
		t.Fatalf("testsupport: parse redis uri: %v", err)
	}
	// Bound the ops so a paused Redis surfaces a timeout error fast instead of hanging the test, and
	// disable retries so the failure is immediate and deterministic (a real outage, not a flake).
	opt.DialTimeout, opt.ReadTimeout, opt.WriteTimeout = time.Second, time.Second, time.Second
	opt.MaxRetries = -1
	rdb = redis.NewClient(opt)
	t.Cleanup(func() { _ = rdb.Close() })

	pause = func() {
		if err := cli.ContainerPause(ctx, id); err != nil {
			t.Fatalf("testsupport: pause redis: %v", err)
		}
	}
	unpause = func() {
		if err := cli.ContainerUnpause(ctx, id); err != nil {
			t.Fatalf("testsupport: unpause redis: %v", err)
		}
	}
	return rdb, pause, unpause
}
