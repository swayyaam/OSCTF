//go:build integration

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osctf/platform/internal/metrics"

	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/flags"
	"github.com/osctf/platform/internal/handlers"
	"github.com/osctf/platform/internal/httpserver"
	"github.com/osctf/platform/internal/redisx"
	"github.com/osctf/platform/internal/runtime"
	"github.com/osctf/platform/internal/scheduler"
	"github.com/osctf/platform/internal/scoreboard"
	"github.com/osctf/platform/internal/submissions"
	"github.com/osctf/platform/internal/teams"
	"github.com/osctf/platform/internal/testsupport"
	"github.com/osctf/platform/internal/users"
	"github.com/osctf/platform/internal/ws"
)

// 3c — the flag-containment scanner. It is deliberately REUSABLE (3c-iii): a set of
// secret strings, each with a policy of who may legitimately see it, is swept against a
// list of endpoints across caller classes. A new v0.3 route is covered by appending it to
// `probes`, not by writing a new test. The same walker also scans WebSocket frames, the
// captured structured-log output, and /metrics.

// --- the reusable scanner -----------------------------------------------------

// secret is a string that must not appear in responses/logs/metrics, except to the caller
// classes listed in allowed. Classes: "owner", "nonowner", "anon", "admin".
type secret struct {
	name    string
	value   string
	allowed map[string]bool
}

func (s secret) leakedTo(class string) bool { return !s.allowed[class] }

// jsonContains walks decoded JSON for needle at any depth, in keys AND values.
func jsonContains(v any, needle string) bool {
	switch x := v.(type) {
	case string:
		return strings.Contains(x, needle)
	case map[string]any:
		for k, val := range x {
			if strings.Contains(k, needle) || jsonContains(val, needle) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if jsonContains(e, needle) {
				return true
			}
		}
	}
	return false
}

// contains reports whether needle appears anywhere in body: as a JSON key/value at any
// depth, or (for non-JSON bodies, headers, logs, metrics) as a raw substring.
func contains(body []byte, needle string) bool {
	var v any
	if json.Unmarshal(body, &v) == nil && jsonContains(v, needle) {
		return true
	}
	return bytes.Contains(body, []byte(needle))
}

type probe struct {
	name, method, path, body string
}

type caller struct {
	class string
	jar   *cookieJar
}

// sweepREST runs every probe as every caller and returns a finding for each forbidden
// secret found in a response body or header.
func sweepREST(t *testing.T, srv http.Handler, secrets []secret, probes []probe, callers []caller) []string {
	t.Helper()
	var findings []string
	for _, p := range probes {
		for _, c := range callers {
			rec := do(t, srv, c.jar, p.method, p.path, p.body)
			hdr := headerText(rec.Header())
			for _, s := range secrets {
				if !s.leakedTo(c.class) {
					continue
				}
				if contains(rec.Body.Bytes(), s.value) {
					findings = append(findings, fmt.Sprintf("%s: secret %s leaked to %s in body (status %d)", p.name, s.name, c.class, rec.Code))
				}
				if strings.Contains(hdr, s.value) {
					findings = append(findings, fmt.Sprintf("%s: secret %s leaked to %s in header (status %d)", p.name, s.name, c.class, rec.Code))
				}
			}
		}
	}
	return findings
}

func headerText(h http.Header) string {
	var b strings.Builder
	for k, vs := range h {
		for _, v := range vs {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// --- log-capturing full stack -------------------------------------------------

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type containment struct {
	srv     http.Handler
	pool    *pgxpool.Pool
	q       *gen.Queries
	logs    *syncBuf
	hub     *ws.Hub
	sb      *scoreboard.Service
	ts      *httptest.Server
	hubStop context.CancelFunc
}

// containmentServer wires the full stack with a logger that captures EVERY log line (so
// the scanner can assert no flag is echoed to logs), the scoreboard→hub broadcaster, and
// a real listener for WebSocket dials.
func containmentServer(t *testing.T) *containment {
	t.Helper()
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
	logs := &syncBuf{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	clk := clock.System()
	sessions := auth.NewSessionStore(rdb, time.Hour)
	ev := events.New(q, clk)
	sb := scoreboard.New(q, rdb, ev, clk)
	mgr := runtime.NewManager(runtime.NewFakeRuntime(q), q, "ctf.example.com", 30000, 30999)
	sched := scheduler.New(mgr, q, ev, flags.NewGenerator("osctf"), audit.New(q, log), clk, log,
		scheduler.Config{TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 50})
	hubCtx, hubStop := context.WithCancel(context.Background())
	hub := ws.NewHub(log)
	go hub.Run(hubCtx)
	sb.SetBroadcaster(func(s scoreboard.Snapshot) { hub.BroadcastScoreboard(handlers.ToScoreboard(s)) })
	_ = sb.Recompute(context.Background())

	h := handlers.New(handlers.Deps{
		Users: users.New(q, sessions, true), Teams: teams.New(pool, 4), Events: ev,
		Challenges:  challenges.New(q, newMemStore()),
		Submissions: submissions.New(pool, ev, clk, audit.New(q, log)),
		Scoreboard:  sb, Recompute: func(ctx context.Context) { _ = sb.Recompute(ctx) },
		Runtime: mgr, Scheduler: sched,
		Auth: auth.NewEmailPasswordProvider(q, nil), Sessions: sessions,
		Limiter: redisx.NewLimiter(rdb), Audit: audit.New(q, log),
		Log: log, SessionTTL: time.Hour, MaxAttachmentMB: 10,
	})
	srv := httpserver.New(httpserver.Deps{Log: log, Handlers: h, Sessions: sessions, BaseOrigin: testOrigin, WSHandler: hub.Handler()})
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close(); hubStop() })
	return &containment{srv: srv, pool: pool, q: q, logs: logs, hub: hub, sb: sb, ts: ts, hubStop: hubStop}
}

// --- seed + probe builders ----------------------------------------------------

func staticChallengeBody(title, flag string) string {
	return fmt.Sprintf(`{"title":%q,"category":"misc","flag":%q,"scoring":"static","points_initial":100,"visible":true}`, title, flag)
}

func perInstanceBody(title string) string {
	return fmt.Sprintf(`{"title":%q,"category":"web","kind":"container","flag":"OSCTF{placeholder}","image":"osctf/example:0.1","internal_port":8000,"connection_template":"http://{host}:{port}","scoring":"static","points_initial":100,"visible":true,"instancing":"per_team","flag_mode":"per_instance"}`, title)
}

func meOf(t *testing.T, srv http.Handler, jar *cookieJar) (userID, teamID string) {
	t.Helper()
	rec := do(t, srv, jar, http.MethodGet, "/api/v0/auth/me", "")
	var out struct {
		Id   string `json:"id"`
		Team *struct {
			Id string `json:"id"`
		} `json:"team"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Team == nil {
		t.Fatal("caller has no team")
	}
	return out.Id, out.Team.Id
}

func restProbes(statID, statSlug, instID, instSlug, teamID, userID string) []probe {
	const P = "/api/v0"
	return []probe{
		{"getEvent", http.MethodGet, P + "/event", ""},
		{"getScoreboard", http.MethodGet, P + "/scoreboard", ""},
		{"listTeams", http.MethodGet, P + "/teams", ""},
		{"getTeam", http.MethodGet, P + "/teams/" + teamID, ""},
		{"getUser", http.MethodGet, P + "/users/" + userID, ""},
		{"getMe", http.MethodGet, P + "/auth/me", ""},
		{"listChallenges", http.MethodGet, P + "/challenges", ""},
		{"getChallenge.static", http.MethodGet, P + "/challenges/" + statSlug, ""},
		{"getChallenge.inst", http.MethodGet, P + "/challenges/" + instSlug, ""},
		{"startInstance", http.MethodPost, P + "/challenges/" + instSlug + "/instance", ""},
		{"extendInstance", http.MethodPost, P + "/challenges/" + instSlug + "/instance/extend", ""},
		{"submitFlag.wrong", http.MethodPost, P + "/challenges/" + instSlug + "/submit", `{"flag":"OSCTF{nope}"}`},
		{"adminGetChallenge.static", http.MethodGet, P + "/admin/challenges/" + statID, ""},
		{"adminGetChallenge.inst", http.MethodGet, P + "/admin/challenges/" + instID, ""},
		{"adminListChallenges", http.MethodGet, P + "/admin/challenges", ""},
		{"adminListInstances", http.MethodGet, P + "/admin/instances", ""},
		{"adminGetInstance.inst", http.MethodGet, P + "/admin/challenges/" + instID + "/instance", ""},
		{"adminGetInstanceLogs.inst", http.MethodGet, P + "/admin/challenges/" + instID + "/instance/logs", ""},
		{"adminListSubmissions", http.MethodGet, P + "/admin/submissions", ""},
		{"adminListTeams", http.MethodGet, P + "/admin/teams", ""},
		{"adminListUsers", http.MethodGet, P + "/admin/users", ""},
		{"adminGetStats", http.MethodGet, P + "/admin/stats", ""},
		{"adminGetEvent", http.MethodGet, P + "/admin/event", ""},
	}
}

// wsFrames dials the scoreboard socket with the given jar and returns the frames
// delivered on connect (hello + any replayed scoreboard) as raw JSON.
func wsFrames(t *testing.T, c *containment, jar *cookieJar, n int) [][]byte {
	t.Helper()
	opts := &websocket.DialOptions{HTTPHeader: http.Header{}}
	if h := jar.cookieHeader(); h != "" {
		opts.HTTPHeader.Set("Cookie", h)
	}
	dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dcancel()
	conn, _, err := websocket.Dial(dctx, "ws"+strings.TrimPrefix(c.ts.URL, "http")+"/api/v0/ws", opts) //nolint:bodyclose
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	var out [][]byte
	for i := 0; i < n; i++ {
		rctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, data, err := conn.Read(rctx)
		cancel()
		if err != nil {
			break
		}
		out = append(out, data)
	}
	return out
}

func TestFlagContainmentIntegration(t *testing.T) {
	c := containmentServer(t)
	srv := c.srv
	ctx := context.Background()

	admin := makeAdmin(t, srv, c.pool, "root", "root@x.test")
	const staticFlag = "OSCTF{STATIC_never_leak_7a1b2c}"
	statID, statSlug := createChallenge(t, srv, admin, staticChallengeBody("StaticChal", staticFlag))
	instID, instSlug := createChallenge(t, srv, admin, perInstanceBody("InstChal"))

	owner := teamUp(t, srv, "owner", "owner@x.test", "Owners")
	nonowner := teamUp(t, srv, "nonown", "nonown@x.test", "NonOwners")
	ownerUserID, ownerTeamID := meOf(t, srv, owner)

	// owner starts an instance → a per-instance flag F is generated + stored.
	if rec := do(t, srv, owner, http.MethodPost, "/api/v0/challenges/"+instSlug+"/instance", ""); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("owner start = %d (%s)", rec.Code, rec.Body)
	}
	var perInstFlag string
	if err := c.pool.QueryRow(ctx, `SELECT flag FROM instances WHERE challenge_id=$1 AND team_id=$2`, instID, ownerTeamID).Scan(&perInstFlag); err != nil {
		t.Fatalf("read per-instance flag: %v", err)
	}
	if perInstFlag == "" || perInstFlag == "OSCTF{placeholder}" {
		t.Fatalf("expected a generated per-instance flag, got %q", perInstFlag)
	}

	secrets := []secret{
		{name: "static-flag", value: staticFlag, allowed: map[string]bool{"admin": true}},
		{name: "per-instance-flag", value: perInstFlag, allowed: map[string]bool{"owner": true}},
	}

	// owner solves the static challenge (real solve data on the board) and its own
	// per-instance challenge (exercises the correct-path provided redaction).
	do(t, srv, owner, http.MethodPost, "/api/v0/challenges/"+statSlug+"/submit", `{"flag":"`+staticFlag+`"}`)
	do(t, srv, owner, http.MethodPost, "/api/v0/challenges/"+instSlug+"/submit", `{"flag":"`+perInstFlag+`"}`)
	_ = c.sb.Recompute(ctx)

	// --- sharing: nonowner starts its OWN instance (a prerequisite to submit), then
	// submits owner's per-instance flag — the sharing signal only fires for a team that
	// has started the challenge but submits a value matching a different team's flag. ---
	if rec := do(t, srv, nonowner, http.MethodPost, "/api/v0/challenges/"+instSlug+"/instance", ""); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("nonowner start = %d (%s)", rec.Code, rec.Body)
	}
	before := metrics.CounterValue(metrics.FlagSharingSignals)
	share := do(t, srv, nonowner, http.MethodPost, "/api/v0/challenges/"+instSlug+"/submit", `{"flag":"`+perInstFlag+`"}`)
	if share.Code != http.StatusOK {
		t.Fatalf("sharing submit = %d (%s)", share.Code, share.Body)
	}
	var sr struct {
		Correct bool `json:"correct"`
	}
	_ = json.Unmarshal(share.Body.Bytes(), &sr)
	if sr.Correct {
		t.Error("another team's per-instance flag accepted as correct — sharing not rejected")
	}
	if contains(share.Body.Bytes(), perInstFlag) {
		t.Error("sharing response echoed the flag value")
	}
	if metrics.CounterValue(metrics.FlagSharingSignals) <= before {
		t.Error("sharing signal (osctf_flag_sharing_signals_total) did not fire")
	}

	// --- REST sweep across every caller class ---
	callers := []caller{
		{"owner", owner}, {"nonowner", nonowner}, {"anon", &cookieJar{}}, {"admin", admin},
	}
	probes := restProbes(statID, statSlug, instID, instSlug, ownerTeamID, ownerUserID)
	if leaks := sweepREST(t, srv, secrets, probes, callers); len(leaks) > 0 {
		t.Errorf("REST flag leaks:\n  - %s", strings.Join(leaks, "\n  - "))
	}

	// --- WebSocket frames (owner + nonowner) ---
	for _, cl := range []caller{{"owner", owner}, {"nonowner", nonowner}, {"anon", &cookieJar{}}} {
		for _, f := range wsFrames(t, c, cl.jar, 2) {
			for _, s := range secrets {
				if s.leakedTo(cl.class) && contains(f, s.value) {
					t.Errorf("WS frame leaked secret %s to %s: %s", s.name, cl.class, f)
				}
			}
		}
	}

	// --- WS reconnect replay (3c-ii): a fresh client gets the cached lastScoreboard;
	// no flag may ride that replay, normal or frozen. ---
	checkReplay := func(label string) {
		for _, f := range wsFrames(t, c, &cookieJar{}, 2) { // anon late joiner
			for _, s := range secrets {
				if contains(f, s.value) {
					t.Errorf("WS reconnect replay (%s) leaked secret %s: %s", label, s.name, f)
				}
			}
		}
	}
	checkReplay("normal")
	// Freeze and recompute, then reconnect again.
	fz := time.Now().UTC().Add(-time.Minute)
	if _, err := c.pool.Exec(ctx, `UPDATE events SET freeze_at=$1`, fz); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	_ = c.sb.Recompute(ctx)
	checkReplay("frozen")
	if _, err := c.pool.Exec(ctx, `UPDATE events SET freeze_at=NULL`); err != nil {
		t.Fatalf("unfreeze: %v", err)
	}

	// --- expired/destroyed instance (3c-i): after teardown the flag must be gone from
	// every response, log, metric, and audit row. ---
	if rec := do(t, srv, owner, http.MethodDelete, "/api/v0/challenges/"+instSlug+"/instance", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("owner stop = %d (%s)", rec.Code, rec.Body)
	}
	if leaks := sweepREST(t, srv, secrets, probes, callers); len(leaks) > 0 {
		t.Errorf("post-teardown REST flag leaks:\n  - %s", strings.Join(leaks, "\n  - "))
	}

	// --- /metrics, structured logs, audit rows ---
	m := do(t, srv, admin, http.MethodGet, "/metrics", "")
	logs := c.logs.String()
	var auditMeta string
	if err := c.pool.QueryRow(ctx, `SELECT coalesce(string_agg(meta::text,' '),'') FROM audit_log`).Scan(&auditMeta); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	for _, s := range secrets {
		if contains(m.Body.Bytes(), s.value) {
			t.Errorf("/metrics leaked secret %s", s.name)
		}
		if strings.Contains(logs, s.value) {
			t.Errorf("structured logs leaked secret %s", s.name)
		}
		if strings.Contains(auditMeta, s.value) {
			t.Errorf("audit_log rows leaked secret %s", s.name)
		}
	}
}
