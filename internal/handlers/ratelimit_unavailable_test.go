package handlers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/swayyaam/OSCTF/internal/apperr"
	"github.com/swayyaam/OSCTF/internal/redisx"
)

// TestLimitFailsClosedWhenLimiterUnavailable pins the Redis-down behavior of the credential/mutation
// rate limiter (login, register, submit all route through s.limit): a limiter whose backend is
// unreachable must FAIL CLOSED as 503 Unavailable with a Retry-After — never a bare 500 (which tells
// a client its request was wrong) and never fail-open. 503 tells automation to come back; that
// distinction is the point.
func TestLimitFailsClosedWhenLimiterUnavailable(t *testing.T) {
	dead := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6390"}) // nothing listening
	_ = dead.Close()                                                // ops now fail fast (client closed)
	s := &Server{d: Deps{Limiter: redisx.NewLimiter(dead), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}

	err := s.limit(context.Background(), "login-ip", "1.2.3.4", 5, time.Minute)
	var unavail *apperr.Unavailable
	if !errors.As(err, &unavail) {
		t.Fatalf("limiter unavailable must return *apperr.Unavailable (503), got %T: %v", err, err)
	}
	if unavail.RetryAfter <= 0 {
		t.Error("the 503 must carry a Retry-After so a client (especially automation) knows to retry")
	}
}
