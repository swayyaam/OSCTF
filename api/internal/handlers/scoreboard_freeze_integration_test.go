//go:build integration

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

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

// Redis keys the scoreboard service writes (unexported there; mirrored so the
// freeze/cache tests can read and wipe the raw bytes).
const sbKeyCurrent = "scoreboard:current"

// sbHarness wires the full stack (including the WS hub) with an injectable clock and
// exposes the internals the freeze/Redis tests need to inspect.
type sbHarness struct {
	t    *testing.T
	pool *pgxpool.Pool
	rdb  *redis.Client
	q    *gen.Queries
	sb   *scoreboard.Service
	mux  http.Handler
	ts   *httptest.Server
	now  *atomic.Int64
}

func (h *sbHarness) setNow(tm time.Time) { h.now.Store(tm.UnixNano()) }

func (h *sbHarness) rawKey(key string) ([]byte, bool) {
	b, err := h.rdb.Get(context.Background(), key).Bytes()
	if err != nil {
		return nil, false
	}
	return b, true
}

func (h *sbHarness) delKey(key string) { h.rdb.Del(context.Background(), key) }

func (h *sbHarness) setFreezeAt(tm *time.Time) {
	if _, err := h.pool.Exec(context.Background(), `UPDATE events SET freeze_at = $1`, tm); err != nil {
		h.t.Fatalf("set freeze_at: %v", err)
	}
}

func (h *sbHarness) getScoreboard(jar *cookieJar) []byte {
	rec := do(h.t, h.mux, jar, http.MethodGet, "/api/v0/scoreboard", "")
	if rec.Code != http.StatusOK {
		h.t.Fatalf("GET scoreboard = %d (%s)", rec.Code, rec.Body)
	}
	return rec.Body.Bytes()
}

func newSBHarness(t *testing.T, start, end time.Time, freeze *time.Time) *sbHarness {
	t.Helper()
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	q := gen.New(pool)

	var now atomic.Int64
	now.Store(time.Now().UTC().UnixNano())
	clk := clock.Clock(func() time.Time { return time.Unix(0, now.Load()).UTC() })

	if _, err := q.CreateEvent(context.Background(), gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: start, EndsAt: end, FreezeAt: freeze,
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}

	sessions := auth.NewSessionStore(rdb, time.Hour)
	ev := events.New(q, clk)
	sb := scoreboard.New(q, rdb, ev, clk)
	hubCtx, hubCancel := context.WithCancel(context.Background())
	t.Cleanup(hubCancel)
	hub := ws.NewHub(discardLog())
	go hub.Run(hubCtx)
	sb.SetBroadcaster(func(s scoreboard.Snapshot) { hub.BroadcastScoreboard(handlers.ToScoreboard(s)) })

	h := handlers.New(handlers.Deps{
		Users:       users.New(q, sessions, true),
		Teams:       teams.New(pool, 4),
		Events:      ev,
		Challenges:  challenges.New(q, newMemStore()),
		Submissions: submissions.New(pool, ev, clk, audit.New(q, discardLog())),
		Scoreboard:  sb,
		Recompute:   func(rctx context.Context) { _ = sb.Recompute(rctx) },
		Auth:        auth.NewEmailPasswordProvider(q, nil),
		Sessions:    sessions,
		Limiter:     redisx.NewLimiter(rdb),
		Audit:       audit.New(q, discardLog()),
		SessionTTL:  time.Hour,
	})
	mux := httpserver.New(httpserver.Deps{
		Log: discardLog(), Handlers: h, Sessions: sessions, BaseOrigin: testOrigin, WSHandler: hub.Handler(),
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	return &sbHarness{t: t, pool: pool, rdb: rdb, q: q, sb: sb, mux: mux, ts: ts, now: &now}
}

// TestScoreboardFrozenSnapshotByteStableAcrossRecompute: once frozen, the board a
// non-admin is served must stay byte-for-byte identical even as new solves land and
// Recompute runs. Setup happens while unfrozen; the freeze is applied afterwards so
// the frozen snapshot captures a real, populated board.
func TestScoreboardFrozenSnapshotByteStableAcrossRecompute(t *testing.T) {
	now := time.Now().UTC()
	h := newSBHarness(t, now.Add(-2*time.Hour), now.Add(time.Hour), nil) // unfrozen

	admin := makeAdmin(t, h.mux, h.pool, "root", "root@example.com")
	_, slug := createChallenge(t, h.mux, admin,
		`{"title":"F","category":"misc","flag":"OSCTF{f}","scoring":"static","points_initial":100,"visible":true}`)
	teamA := teamUp(t, h.mux, "user1", "user1@example.com", "Alpha")
	if rec := do(t, h.mux, teamA, http.MethodPost, "/api/v0/challenges/"+slug+"/submit", `{"flag":"OSCTF{f}"}`); rec.Code != http.StatusOK {
		t.Fatalf("Alpha submit = %d", rec.Code)
	}

	// Freeze now, then capture the frozen board (Alpha at 100).
	freeze := now.Add(-time.Minute)
	h.setFreezeAt(&freeze)
	first := h.getScoreboard(&cookieJar{})
	if !strings.Contains(string(first), `"frozen":true`) || !strings.Contains(string(first), `"Alpha"`) {
		t.Fatalf("expected frozen board containing Alpha, got %s", first)
	}

	// A second team solves during the freeze; the frozen board must not move.
	teamB := teamUp(t, h.mux, "user2", "user2@example.com", "Bravo")
	if rec := do(t, h.mux, teamB, http.MethodPost, "/api/v0/challenges/"+slug+"/submit", `{"flag":"OSCTF{f}"}`); rec.Code != http.StatusOK {
		t.Fatalf("Bravo submit = %d", rec.Code)
	}
	for i := 0; i < 3; i++ {
		if err := h.sb.Recompute(context.Background()); err != nil {
			t.Fatalf("recompute: %v", err)
		}
		got := h.getScoreboard(&cookieJar{})
		if string(got) != string(first) {
			t.Fatalf("frozen board changed under Recompute:\n first=%s\n now  =%s", first, got)
		}
		if strings.Contains(string(got), `"Bravo"`) {
			t.Fatalf("frozen board leaked a post-freeze solver: %s", got)
		}
	}
}

// TestScoreboardSolveDuringFreezeInvisibleUntilThaw: solves during freeze are hidden
// from non-admins, then after thaw appear with their TRUE timestamps and in the
// CORRECT order — two teams solve the same challenge at different times, so after
// thaw the equal-points tiebreak (earliest last-solve first) ranks them.
func TestScoreboardSolveDuringFreezeInvisibleUntilThaw(t *testing.T) {
	now := time.Now().UTC()
	h := newSBHarness(t, now.Add(-2*time.Hour), now.Add(time.Hour), nil) // unfrozen
	h.setNow(now)                                                        // controllable clock → distinct solve times

	admin := makeAdmin(t, h.mux, h.pool, "root", "root@example.com")
	_, slug := createChallenge(t, h.mux, admin,
		`{"title":"F","category":"misc","flag":"OSCTF{f}","scoring":"static","points_initial":100,"visible":true}`)
	alpha := teamUp(t, h.mux, "user1", "user1@example.com", "Alpha")
	bravo := teamUp(t, h.mux, "user2", "user2@example.com", "Bravo")

	// Freeze with both teams present but scoreless, and capture the frozen snapshot.
	freeze := now.Add(-time.Minute)
	h.setFreezeAt(&freeze)
	if p := teamPoints(t, h.mux, &cookieJar{}, "Alpha"); p != 0 {
		t.Fatalf("frozen board should show Alpha at 0 before the solves, got %d", p)
	}

	// Two solves during freeze, one minute apart → distinct last_solve_at.
	h.setNow(now)
	if rec := do(t, h.mux, alpha, http.MethodPost, "/api/v0/challenges/"+slug+"/submit", `{"flag":"OSCTF{f}"}`); rec.Code != http.StatusOK {
		t.Fatalf("Alpha submit during freeze = %d", rec.Code)
	}
	h.setNow(now.Add(time.Minute))
	if rec := do(t, h.mux, bravo, http.MethodPost, "/api/v0/challenges/"+slug+"/submit", `{"flag":"OSCTF{f}"}`); rec.Code != http.StatusOK {
		t.Fatalf("Bravo submit during freeze = %d", rec.Code)
	}

	// Invisible while frozen.
	if p := teamPoints(t, h.mux, &cookieJar{}, "Alpha"); p != 0 {
		t.Errorf("frozen board shows Alpha=%d, want 0 (solve hidden)", p)
	}
	if p := teamPoints(t, h.mux, &cookieJar{}, "Bravo"); p != 0 {
		t.Errorf("frozen board shows Bravo=%d, want 0 (solve hidden)", p)
	}

	// Thaw: both solves appear, ranked by earliest last-solve (Alpha before Bravo).
	h.setFreezeAt(nil)
	if err := h.sb.Recompute(context.Background()); err != nil {
		t.Fatalf("recompute after thaw: %v", err)
	}
	body := h.getScoreboard(&cookieJar{})
	if strings.Contains(string(body), `"frozen":true`) {
		t.Fatalf("board still frozen after thaw: %s", body)
	}
	var sb struct {
		Standings []struct {
			Name        string     `json:"name"`
			Points      int        `json:"points"`
			Rank        *int       `json:"rank"`
			LastSolveAt *time.Time `json:"last_solve_at"`
		} `json:"standings"`
	}
	if err := json.Unmarshal(body, &sb); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(sb.Standings) < 2 {
		t.Fatalf("after thaw want >=2 ranked teams, got %s", body)
	}
	a, b := sb.Standings[0], sb.Standings[1]
	if a.Name != "Alpha" || b.Name != "Bravo" {
		t.Fatalf("wrong order after thaw: got [%s, %s], want [Alpha, Bravo] (earliest last-solve first)", a.Name, b.Name)
	}
	if a.Points != 100 || b.Points != 100 {
		t.Errorf("points after thaw = Alpha %d / Bravo %d, want 100/100", a.Points, b.Points)
	}
	if a.LastSolveAt == nil || b.LastSolveAt == nil {
		t.Fatal("null last_solve_at after thaw; the solves' true timestamps must appear")
	}
	if !a.LastSolveAt.Before(*b.LastSolveAt) {
		t.Errorf("last_solve_at order wrong: Alpha %v not before Bravo %v", *a.LastSolveAt, *b.LastSolveAt)
	}
}

// TestScoreboardRecomputeDeterministicAndReconstructs covers determinism (run twice,
// identical bytes), incremental-equals-from-scratch (recompute-per-solve equals a
// single recompute over the same final log), and Redis-wipe reconstruction.
func TestScoreboardRecomputeDeterministicAndReconstructs(t *testing.T) {
	now := time.Now().UTC()
	h := newSBHarness(t, now.Add(-2*time.Hour), now.Add(time.Hour), nil)
	h.setNow(now) // freeze the clock so GeneratedAt is stable across recomputes

	admin := makeAdmin(t, h.mux, h.pool, "root", "root@example.com")
	_, slug := createChallenge(t, h.mux, admin,
		`{"title":"D","category":"misc","flag":"OSCTF{d}","scoring":"dynamic","points_initial":500,"points_min":100,"decay":10,"visible":true}`)
	for i, name := range []string{"Alpha", "Bravo", "Charlie"} {
		u := "user" + string(rune('a'+i))
		jar := teamUp(t, h.mux, u, u+"@example.com", name)
		if rec := do(t, h.mux, jar, http.MethodPost, "/api/v0/challenges/"+slug+"/submit", `{"flag":"OSCTF{d}"}`); rec.Code != http.StatusOK {
			t.Fatalf("submit %s = %d", name, rec.Code)
		}
		if err := h.sb.Recompute(context.Background()); err != nil {
			t.Fatalf("recompute: %v", err)
		}
	}
	incremental, ok := h.rawKey(sbKeyCurrent)
	if !ok {
		t.Fatal("no current snapshot after solves")
	}
	// Guard against a vacuous pass on empty boards: the standings must be populated.
	for _, name := range []string{"Alpha", "Bravo", "Charlie"} {
		if !strings.Contains(string(incremental), `"`+name+`"`) {
			t.Fatalf("board missing %s; test would be comparing empty boards: %s", name, incremental)
		}
	}

	h.delKey(sbKeyCurrent)
	if err := h.sb.Recompute(context.Background()); err != nil {
		t.Fatalf("from-scratch recompute: %v", err)
	}
	fromScratch, _ := h.rawKey(sbKeyCurrent)
	if string(incremental) != string(fromScratch) {
		t.Fatalf("incremental != from-scratch:\n inc=%s\n scr=%s", incremental, fromScratch)
	}

	if err := h.sb.Recompute(context.Background()); err != nil {
		t.Fatalf("recompute twice: %v", err)
	}
	again, _ := h.rawKey(sbKeyCurrent)
	if string(again) != string(fromScratch) {
		t.Fatalf("recompute not deterministic:\n a=%s\n b=%s", fromScratch, again)
	}

	h.delKey(sbKeyCurrent)
	rebuilt := h.getScoreboard(&cookieJar{}) // cache miss → recompute + repopulate
	stored, ok := h.rawKey(sbKeyCurrent)
	if !ok {
		t.Fatal("cache not repopulated after miss")
	}
	assertSameScoreboardData(t, rebuilt, stored)
}

// TestScoreboardEmptyEncodesAsArrayNotNull guards the "[] not null" invariant across
// the layers a client sees: the Redis-cached snapshot and the REST body. The
// scoreboard payload has no nested list fields, so standings is the only list level.
func TestScoreboardEmptyEncodesAsArrayNotNull(t *testing.T) {
	now := time.Now().UTC()
	h := newSBHarness(t, now.Add(-2*time.Hour), now.Add(time.Hour), nil)
	if err := h.sb.Recompute(context.Background()); err != nil {
		t.Fatalf("recompute: %v", err)
	}
	cached, ok := h.rawKey(sbKeyCurrent)
	if !ok {
		t.Fatal("no cached snapshot")
	}
	if !strings.Contains(string(cached), `"standings":[]`) {
		t.Errorf("cached empty board must encode standings as [], got %s", cached)
	}
	rest := h.getScoreboard(&cookieJar{})
	if !strings.Contains(string(rest), `"standings":[]`) {
		t.Errorf("REST empty board must encode standings as [], got %s", rest)
	}
}

// TestWebSocketScoreboardMatchesRESTByteForByte: the scoreboard a WS client is served
// (the live broadcast AND the cached replay on a fresh connect) must be byte-for-byte
// identical to the REST snapshot — in normal, frozen, and thawed states. Divergence
// here (WS and REST serializing different Go types) is invisible to every other tier.
func TestWebSocketScoreboardMatchesRESTByteForByte(t *testing.T) {
	now := time.Now().UTC()
	freeze := now.Add(-time.Hour)
	h := newSBHarness(t, now.Add(-2*time.Hour), now.Add(2*time.Hour), nil) // start unfrozen

	admin := makeAdmin(t, h.mux, h.pool, "root", "root@example.com")
	_, slug := createChallenge(t, h.mux, admin,
		`{"title":"W","category":"misc","flag":"OSCTF{w}","scoring":"static","points_initial":100,"visible":true}`)
	team := teamUp(t, h.mux, "user1", "user1@example.com", "Alpha")
	if rec := do(t, h.mux, team, http.MethodPost, "/api/v0/challenges/"+slug+"/submit", `{"flag":"OSCTF{w}"}`); rec.Code != http.StatusOK {
		t.Fatalf("submit = %d", rec.Code)
	}

	// Persistent client: reading its broadcast frame confirms the hub processed a
	// Recompute (so the cached lastScoreboard used for replay is current).
	barrier := h.dialWS(t)
	defer barrier.Close(websocket.StatusNormalClosure, "")
	drainType(t, barrier, "hello")
	drainType(t, barrier, "scoreboard")

	check := func(state string) {
		if err := h.sb.Recompute(context.Background()); err != nil {
			t.Fatalf("[%s] recompute: %v", state, err)
		}
		wsLive := readScoreboardData(t, barrier)
		// The REST HTTP body carries a trailing newline from the JSON response
		// encoder; the WS frame (a data value inside an envelope) does not. That is a
		// transport artifact, not a data difference, so compare against the trimmed
		// body — everything else (keys, order, values) must match byte-for-byte.
		rest := strings.TrimRight(string(h.getScoreboard(&cookieJar{})), "\n")
		if string(wsLive) != rest {
			t.Fatalf("[%s] WS live frame != REST snapshot:\n ws  =%s\n rest=%s", state, wsLive, rest)
		}
		fresh := h.dialWS(t)
		drainType(t, fresh, "hello")
		wsReplay := readScoreboardData(t, fresh)
		fresh.Close(websocket.StatusNormalClosure, "")
		if string(wsReplay) != rest {
			t.Fatalf("[%s] WS cached replay != REST snapshot:\n ws  =%s\n rest=%s", state, wsReplay, rest)
		}
	}

	check("normal")
	h.setFreezeAt(&freeze)
	check("frozen")
	h.setFreezeAt(nil)
	check("thawed")
}

// --- WS helpers -------------------------------------------------------------

func (h *sbHarness) dialWS(t *testing.T) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(h.ts.URL, "http") + "/api/v0/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	return conn
}

func drainType(t *testing.T, c *websocket.Conn, want string) {
	t.Helper()
	if typ := readType(t, c); typ != want {
		t.Fatalf("ws frame type = %q, want %q", typ, want)
	}
}

// readScoreboardData reads until a scoreboard frame arrives (tolerating the hub's
// broadcast throttle) and returns its data payload.
func readScoreboardData(t *testing.T, c *websocket.Conn) []byte {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		typ, data := readMessage(t, c)
		if typ == "scoreboard" {
			return data
		}
	}
	t.Fatal("no scoreboard frame within deadline")
	return nil
}

func assertSameScoreboardData(t *testing.T, a, b []byte) {
	t.Helper()
	norm := func(raw []byte) string {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		out, _ := json.Marshal(v)
		return string(out)
	}
	if norm(a) != norm(b) {
		t.Fatalf("scoreboard data differs:\n a=%s\n b=%s", a, b)
	}
}
