//go:build integration

package handlers_test

import (
	"context"
	"net/http"
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

// TestWSAdmissionKeysOnUser (3a-xi-1): with the per-key cap set to 1, two DIFFERENT
// logged-in users behind the SAME socket IP both connect — because admission keys on the
// user, not the IP — while a second connection for the SAME user is rejected. This is the
// shared-NAT case (GitHub issue #1's class): a campus of logged-in players is not squeezed
// through one IP budget.
func TestWSAdmissionKeysOnUser(t *testing.T) {
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
	hub.SetLimits(ws.Limits{MaxConns: 100, MaxConnsPerKey: 1, HandshakeBurst: 1000, HandshakeWindow: time.Minute})
	hub.SetKeyResolver(func(r *http.Request) string {
		if id, ok := auth.IdentityFrom(r.Context()); ok {
			return "u:" + id.UserID.String()
		}
		return "ip:" + r.RemoteAddr
	})
	go hub.Run(hubCtx)
	sb.SetBroadcaster(func(s scoreboard.Snapshot) { hub.BroadcastScoreboard(handlers.ToScoreboard(s)) })
	_ = sb.Recompute(context.Background())

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
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v0/ws"

	alice := registerUser(t, mux, "wsa", "wsa@x.test")
	bob := registerUser(t, mux, "wsb", "wsb@x.test")

	dial := func(jar *cookieJar) (*websocket.Conn, *http.Response, error) {
		opts := &websocket.DialOptions{HTTPHeader: http.Header{}}
		if ch := jar.cookieHeader(); ch != "" {
			opts.HTTPHeader.Set("Cookie", ch)
		}
		dctx, dcancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer dcancel()
		return websocket.Dial(dctx, wsURL, opts) //nolint:bodyclose // library manages the body
	}

	// alice and bob share the loopback IP but are distinct user keys → both admitted.
	ca, _, err := dial(alice)
	if err != nil {
		t.Fatalf("alice connect rejected: %v", err)
	}
	defer ca.Close(websocket.StatusNormalClosure, "")
	if typ := readType(t, ca); typ != "hello" {
		t.Fatalf("alice first frame = %q, want hello", typ)
	}
	cb, _, err := dial(bob)
	if err != nil {
		t.Fatalf("bob connect rejected despite a distinct user key (shared-NAT case broken): %v", err)
	}
	defer cb.Close(websocket.StatusNormalClosure, "")
	if typ := readType(t, cb); typ != "hello" {
		t.Fatalf("bob first frame = %q, want hello", typ)
	}

	// A second connection for the SAME user hits the per-key cap of 1.
	if c, resp, err := dial(alice); err == nil {
		_ = c.Close(websocket.StatusNormalClosure, "")
		t.Fatal("alice's second connection was accepted past the per-key cap")
	} else if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("alice second-connection status = %v, want 429", resp)
	}
}
