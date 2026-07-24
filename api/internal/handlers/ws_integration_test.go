package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/handlers"
	"github.com/osctf/platform/internal/httpserver"
	"github.com/osctf/platform/internal/metrics"
	"github.com/osctf/platform/internal/redisx"
	"github.com/osctf/platform/internal/scoreboard"
	"github.com/osctf/platform/internal/submissions"
	"github.com/osctf/platform/internal/teams"
	"github.com/osctf/platform/internal/testsupport"
	"github.com/osctf/platform/internal/users"
	"github.com/osctf/platform/internal/ws"

	"github.com/google/uuid"
)

func TestWebSocketScoreboardIntegration(t *testing.T) {
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
	sb.SetBroadcaster(hub.BroadcastScoreboard)
	if err := sb.Recompute(context.Background()); err != nil {
		t.Fatalf("warm scoreboard: %v", err)
	}

	h := handlers.New(handlers.Deps{
		Users: users.New(q, sessions, true), Teams: teams.New(pool, 4),
		Events: ev, Challenges: challenges.New(q, newMemStore()),
		Submissions: submissions.New(pool, ev, clock.System()), Scoreboard: sb,
		Recompute: func(rctx context.Context) { _ = sb.Recompute(rctx) },
		Auth:      auth.NewEmailPasswordProvider(q, nil), Sessions: sessions,
		Limiter: redisx.NewLimiter(rdb), Audit: audit.New(q, discardLog()), SessionTTL: time.Hour,
	})
	mux := httpserver.New(httpserver.Deps{
		Log: discardLog(), Handlers: h, Sessions: sessions, BaseOrigin: testOrigin, WSHandler: hub.Handler(),
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Set up a solvable challenge and a team.
	admin := makeAdmin(t, mux, pool, "root", "root@example.com")
	_, slug := createChallenge(t, mux, admin,
		`{"title":"WS","category":"misc","flag":"OSCTF{ws}","scoring":"static","points_initial":100,"visible":true}`)
	player := teamUp(t, mux, "wsr", "wsr@example.com", "Wsers")

	// Connect a WS client.
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v0/ws"
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	conn, _, err := websocket.Dial(dialCtx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	// First frames on connect: hello, then scoreboard.
	if typ := readType(t, conn); typ != "hello" {
		t.Fatalf("first message type = %q, want hello", typ)
	}
	if typ := readType(t, conn); typ != "scoreboard" {
		t.Fatalf("second message type = %q, want scoreboard", typ)
	}

	// Gauge shows one connection.
	waitGauge(t, 1)

	// A solve → the client receives an updated scoreboard within ~2s.
	rec := do(t, mux, player, http.MethodPost, "/api/v0/challenges/"+slug+"/submit", `{"flag":"OSCTF{ws}"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit = %d", rec.Code)
	}
	deadline := time.Now().Add(3 * time.Second)
	var points int
	for time.Now().Before(deadline) {
		typ, payload := readMessage(t, conn)
		if typ != "scoreboard" {
			continue
		}
		var sbMsg struct {
			Standings []struct {
				Name   string `json:"name"`
				Points int    `json:"points"`
			} `json:"standings"`
		}
		_ = json.Unmarshal(payload, &sbMsg)
		for _, row := range sbMsg.Standings {
			if row.Name == "Wsers" && row.Points == 100 {
				points = row.Points
			}
		}
		if points == 100 {
			break
		}
	}
	if points != 100 {
		t.Error("did not receive updated scoreboard (Wsers=100) over the socket")
	}

	// Disconnect → the gauge decrements.
	_ = conn.Close(websocket.StatusNormalClosure, "done")
	waitGauge(t, 0)
}

func readType(t *testing.T, c *websocket.Conn) string {
	t.Helper()
	typ, _ := readMessage(t, c)
	return typ
}

func readMessage(t *testing.T, c *websocket.Conn) (string, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var env struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(data, &env)
	return env.Type, env.Data
}

func waitGauge(t *testing.T, want float64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if metrics.GaugeValue(metrics.WSConnections) == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("ws gauge = %v, want %v", metrics.GaugeValue(metrics.WSConnections), want)
}
