//go:build integration

package handlers_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/handlers"
	"github.com/osctf/platform/internal/httpserver"
	"github.com/osctf/platform/internal/redisx"
	"github.com/osctf/platform/internal/scoreboard"
	"github.com/osctf/platform/internal/submissions"
	"github.com/osctf/platform/internal/teams"
	"github.com/osctf/platform/internal/testsupport"
	"github.com/osctf/platform/internal/users"
	"github.com/osctf/platform/internal/ws"
)

// TestWSFrameOrderingIntegration (3a-xiii): over the wire, hello must arrive before any
// scoreboard (a client must know the frozen state before the board), and a phase
// transition must be DELIVERED, not dropped or coalesced away — otherwise a client sits on
// a stale frozen flag for the rest of the event.
func TestWSFrameOrderingIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	q := gen.New(pool)
	now := time.Now().UTC()
	if _, err := q.CreateEvent(context.Background(), gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("event: %v", err)
	}
	sessions := auth.NewSessionStore(rdb, time.Hour)
	ev := events.New(q, clock.System())
	sb := scoreboard.New(q, rdb, ev, clock.System())
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	hub := ws.NewHub(discardLog())
	go hub.Run(hubCtx)
	sb.SetBroadcaster(func(s scoreboard.Snapshot) { hub.BroadcastScoreboard(handlers.ToScoreboard(s)) })
	if err := sb.Recompute(context.Background()); err != nil { // warm → a joiner gets hello + snapshot
		t.Fatalf("warm: %v", err)
	}

	h := handlers.New(handlers.Deps{
		Users: users.New(q, sessions, true), Teams: teams.New(pool, 4), Events: ev,
		Challenges: challenges.New(q, newMemStore()), Scoreboard: sb,
		Submissions: submissions.New(pool, ev, clock.System(), audit.New(q, discardLog())),
		Recompute:   func(ctx context.Context) { _ = sb.Recompute(ctx) },
		Auth:        auth.NewRegistry(auth.NewEmailPasswordProvider(q, nil)), Sessions: sessions,
		Limiter: redisx.NewLimiter(rdb), Audit: audit.New(q, discardLog()), SessionTTL: time.Hour,
	})
	mux := httpserver.New(httpserver.Deps{Log: discardLog(), Handlers: h, Sessions: sessions, BaseOrigin: testOrigin, WSHandler: hub.Handler()})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dcancel()
	conn, _, err := websocket.Dial(dctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/api/v0/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	// hello strictly precedes the first scoreboard on the wire.
	if typ := readType(t, conn); typ != "hello" {
		t.Fatalf("first frame = %q, want hello (must precede any board)", typ)
	}
	if typ := readType(t, conn); typ != "scoreboard" {
		t.Fatalf("second frame = %q, want scoreboard", typ)
	}

	// A phase transition is delivered in order, not dropped.
	hub.BroadcastPhase("ended")
	if typ := readType(t, conn); typ != "event.phase" {
		t.Fatalf("frame after BroadcastPhase = %q, want event.phase (a phase transition must not be dropped)", typ)
	}
}
