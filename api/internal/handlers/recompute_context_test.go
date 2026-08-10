package handlers

import (
	"context"
	"testing"
)

// The scoreboard recompute follows an already-committed solve (Submit commits its tx
// before the handler calls s.recompute). It must therefore NOT inherit the request's
// cancellation: if a client disconnects mid-request, the request context is cancelled,
// and a recompute that ran on it would fail its DB reads / Redis write and leave the
// served board silently disagreeing with the log until the next tick repairs it. These
// pin that both recompute paths detach from the caller's cancellation.

func TestRecomputeDetachesFromRequestCancellation(t *testing.T) {
	var got error
	s := &Server{d: Deps{Recompute: func(ctx context.Context) { got = ctx.Err() }}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // client disconnected after the solve committed

	s.recompute(ctx)

	if got != nil {
		t.Fatalf("recompute ran on a cancelled context (%v) — a client disconnecting mid-request abandons the board update for a committed solve", got)
	}
}

func TestRecomputeForceDetachesFromRequestCancellation(t *testing.T) {
	var got error
	s := &Server{d: Deps{RecomputeForce: func(ctx context.Context) { got = ctx.Err() }}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s.recomputeForce(ctx)

	if got != nil {
		t.Fatalf("recomputeForce ran on a cancelled context (%v) — an admin mutation's board update must not be abandoned on disconnect", got)
	}
}

// The detached context must still carry request-scoped values (auth, request id, trace)
// so the recompute's DB/Redis calls and any logging keep them.
func TestRecomputeKeepsContextValues(t *testing.T) {
	type ctxKey string
	const k ctxKey = "req-id"
	var got any
	s := &Server{d: Deps{Recompute: func(ctx context.Context) { got = ctx.Value(k) }}}

	ctx := context.WithValue(context.Background(), k, "abc123")
	s.recompute(ctx)

	if got != "abc123" {
		t.Fatalf("detached recompute context dropped request values: got %v, want abc123", got)
	}
}
