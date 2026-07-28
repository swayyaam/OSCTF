package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

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
)

// fullServer wires every service with an injectable clock and event window.
func fullServer(t *testing.T, pool *pgxpool.Pool, rdb *redis.Client, clk clock.Clock, start, end time.Time, freeze *time.Time) http.Handler {
	t.Helper()
	q := gen.New(pool)
	if _, err := q.CreateEvent(context.Background(), gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: start, EndsAt: end, FreezeAt: freeze,
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}
	sessions := auth.NewSessionStore(rdb, time.Hour)
	ev := events.New(q, clk)
	sb := scoreboard.New(q, rdb, ev, clk)
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
	return httpserver.New(httpserver.Deps{Log: discardLog(), Handlers: h, Sessions: sessions, BaseOrigin: testOrigin})
}

func createChallenge(t *testing.T, srv http.Handler, admin *cookieJar, body string) (id, slug string) {
	t.Helper()
	rec := do(t, srv, admin, http.MethodPost, "/api/v0/admin/challenges", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create challenge = %d (%s)", rec.Code, rec.Body)
	}
	var c struct{ Id, Slug string }
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	return c.Id, c.Slug
}

func teamUp(t *testing.T, srv http.Handler, username, email, teamName string) *cookieJar {
	t.Helper()
	jar := registerUser(t, srv, username, email)
	if rec := do(t, srv, jar, http.MethodPost, "/api/v0/teams", `{"name":"`+teamName+`"}`); rec.Code != http.StatusCreated {
		t.Fatalf("team create = %d (%s)", rec.Code, rec.Body)
	}
	return jar
}

// TestConcurrentDoubleSolveRace is the mandatory race test: two concurrent correct
// submissions for one team+challenge yield exactly one 200-correct and one 409.
func TestConcurrentDoubleSolveRace(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	now := time.Now().UTC()
	srv := fullServer(t, pool, rdb, clock.System(), now.Add(-time.Hour), now.Add(time.Hour), nil)

	admin := makeAdmin(t, srv, pool, "root", "root@example.com")
	_, slug := createChallenge(t, srv, admin,
		`{"title":"Race","category":"misc","flag":"OSCTF{race}","scoring":"static","points_initial":100,"visible":true}`)
	player := teamUp(t, srv, "racer", "racer@example.com", "Racers")

	const n = 6
	codes := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			// Each goroutine gets its own jar copy sharing the same session cookie.
			j := &cookieJar{cookies: append([]*http.Cookie(nil), player.cookies...)}
			rec := do(t, srv, j, http.MethodPost, "/api/v0/challenges/"+slug+"/submit", `{"flag":"OSCTF{race}"}`)
			codes[idx] = rec.Code
		}(i)
	}
	close(start)
	wg.Wait()

	correct, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			correct++
		case http.StatusConflict:
			conflict++
		}
	}
	if correct != 1 {
		t.Errorf("got %d 200-correct responses, want exactly 1 (codes=%v)", correct, codes)
	}
	if correct+conflict != n {
		t.Errorf("unexpected status codes: %v", codes)
	}

	// Exactly one solve row exists.
	var solves int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM submissions WHERE correct`).Scan(&solves); err != nil {
		t.Fatal(err)
	}
	if solves != 1 {
		t.Errorf("solve rows = %d, want 1", solves)
	}
}

// TestDynamicScoringAndStandings checks dynamic decay and tiebreak ordering
// against a hand-computed fixture.
func TestDynamicScoringAndStandings(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	now := time.Now().UTC()
	srv := fullServer(t, pool, rdb, clock.System(), now.Add(-time.Hour), now.Add(time.Hour), nil)

	admin := makeAdmin(t, srv, pool, "root", "root@example.com")
	// Dynamic: Initial 500, Min 100, Decay 50. Two solves → value 500 (rounds from 499.36).
	_, slug := createChallenge(t, srv, admin,
		`{"title":"Dyn","category":"crypto","flag":"OSCTF{dyn}","scoring":"dynamic","points_initial":500,"points_min":100,"decay":50,"visible":true}`)

	teamA := teamUp(t, srv, "alice", "a@example.com", "Alpha")
	teamB := teamUp(t, srv, "bob", "b@example.com", "Bravo")

	// Team A solves first, then Team B.
	if rec := do(t, srv, teamA, http.MethodPost, "/api/v0/challenges/"+slug+"/submit", `{"flag":"OSCTF{dyn}"}`); rec.Code != http.StatusOK {
		t.Fatalf("A submit = %d", rec.Code)
	}
	time.Sleep(10 * time.Millisecond)
	if rec := do(t, srv, teamB, http.MethodPost, "/api/v0/challenges/"+slug+"/submit", `{"flag":"OSCTF{dyn}"}`); rec.Code != http.StatusOK {
		t.Fatalf("B submit = %d", rec.Code)
	}

	rec := do(t, srv, &cookieJar{}, http.MethodGet, "/api/v0/scoreboard", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("scoreboard = %d", rec.Code)
	}
	var sb struct {
		Standings []struct {
			Name   string `json:"name"`
			Points int    `json:"points"`
			Rank   *int   `json:"rank"`
			Solves int    `json:"solves"`
		} `json:"standings"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sb)
	if len(sb.Standings) != 2 {
		t.Fatalf("standings has %d rows, want 2", len(sb.Standings))
	}
	// With 2 solves, value = round(500 - 400*(4/2500)) = round(499.36) = 499. Both teams
	// have 499 points; Alpha solved earlier so ranks first.
	for _, row := range sb.Standings {
		if row.Points != 499 {
			t.Errorf("%s points = %d, want 499", row.Name, row.Points)
		}
	}
	if sb.Standings[0].Name != "Alpha" || *sb.Standings[0].Rank != 1 {
		t.Errorf("rank 1 = %+v, want Alpha", sb.Standings[0])
	}
	if sb.Standings[1].Name != "Bravo" || *sb.Standings[1].Rank != 2 {
		t.Errorf("rank 2 = %+v, want Bravo", sb.Standings[1])
	}
}

// TestFreezeBehavior verifies non-admins see the frozen snapshot while a new solve
// lands, and admins see live data.
func TestFreezeBehavior(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	now := time.Now().UTC()
	// Event started 2h ago, ends in 1h, froze 1h ago.
	freeze := now.Add(-time.Hour)
	srv := fullServer(t, pool, rdb, clock.System(), now.Add(-2*time.Hour), now.Add(time.Hour), &freeze)

	admin := makeAdmin(t, srv, pool, "root", "root@example.com")
	_, slug := createChallenge(t, srv, admin,
		`{"title":"Frz","category":"misc","flag":"OSCTF{frz}","scoring":"static","points_initial":100,"visible":true}`)
	teamA := teamUp(t, srv, "alice", "a@example.com", "Alpha")

	// Force the frozen snapshot to be written now (empty standings), then a solve lands.
	rec := do(t, srv, &cookieJar{}, http.MethodGet, "/api/v0/scoreboard", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("initial scoreboard = %d", rec.Code)
	}
	var pre struct {
		Frozen bool `json:"frozen"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &pre)
	if !pre.Frozen {
		t.Fatal("public scoreboard should report frozen=true")
	}

	if rec := do(t, srv, teamA, http.MethodPost, "/api/v0/challenges/"+slug+"/submit", `{"flag":"OSCTF{frz}"}`); rec.Code != http.StatusOK {
		t.Fatalf("submit during freeze = %d", rec.Code)
	}

	// Public board: still frozen — Alpha shows 0 points (snapshot predates the solve).
	pubPts := teamPoints(t, srv, &cookieJar{}, "Alpha")
	if pubPts != 0 {
		t.Errorf("public (frozen) Alpha points = %d, want 0 (frozen)", pubPts)
	}
	// Admin board: live — Alpha shows 100.
	adminPts := teamPoints(t, srv, admin, "Alpha")
	if adminPts != 100 {
		t.Errorf("admin (live) Alpha points = %d, want 100", adminPts)
	}
}

func teamPoints(t *testing.T, srv http.Handler, jar *cookieJar, name string) int {
	t.Helper()
	rec := do(t, srv, jar, http.MethodGet, "/api/v0/scoreboard", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("scoreboard = %d", rec.Code)
	}
	var sb struct {
		Standings []struct {
			Name   string `json:"name"`
			Points int    `json:"points"`
		} `json:"standings"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sb)
	for _, row := range sb.Standings {
		if row.Name == name {
			return row.Points
		}
	}
	t.Fatalf("team %q not on the board", name)
	return -1
}
