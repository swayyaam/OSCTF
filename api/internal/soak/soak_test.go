//go:build soak

// Package soak is the Phase 7 soak harness. It stands up the full platform
// in-process — real services, WS hub, scheduler, events/scoreboard on an
// injectable clock — and drives it with a fleet of participant actors over HTTP,
// sampling resource and throughput metrics throughout.
//
// Two independent axes:
//
//	-real    use the system clock instead of the accelerated injected clock.
//	-docker  use the real Docker runtime instead of FakeRuntime (needs a daemon).
//
// so you can run real containers on an accelerated clock, or the fake on
// wall-clock time. Commit 1 is the clean baseline: world + actors + metrics, no
// fault injection, no WS-convergence assertion, no invariant checks — if this
// can't run clean against a healthy fake, every failure in commit 2 is ambiguous.
//
//	go test -tags soak -run TestSoak ./internal/soak -timeout 5m \
//	  -args -duration=2m -seed=1 [-real] [-docker] [-speed=120]
package soak

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/flags"
	"github.com/osctf/platform/internal/handlers"
	"github.com/osctf/platform/internal/httpserver"
	"github.com/osctf/platform/internal/metrics"
	"github.com/osctf/platform/internal/redisx"
	runtimepkg "github.com/osctf/platform/internal/runtime"
	"github.com/osctf/platform/internal/scheduler"
	"github.com/osctf/platform/internal/scoreboard"
	"github.com/osctf/platform/internal/submissions"
	"github.com/osctf/platform/internal/teams"
	"github.com/osctf/platform/internal/testsupport"
	"github.com/osctf/platform/internal/users"
	"github.com/osctf/platform/internal/ws"
)

var (
	fDuration = flag.Duration("duration", 20*time.Second, "soak wall-clock duration")
	fSeed     = flag.Int64("seed", 1, "PRNG seed (deterministic action stream)")
	fActors   = flag.Int("actors", 100, "concurrent participant actors")
	fReal     = flag.Bool("real", false, "use the system clock instead of the accelerated injected clock")
	fDocker   = flag.Bool("docker", false, "use the real Docker runtime instead of FakeRuntime (needs a daemon)")
	fSpeed    = flag.Float64("speed", 120, "accelerated-clock speed: simulated seconds per wall second (ignored with -real)")
)

const (
	numTeams      = 30
	numChallenges = 15
	origin        = "http://soak.local" // matches BaseOrigin; the CSRF check compares the header, not the host
	soakPassword  = "soakPassw0rd!7"
	portRangeSize = 1000 // 30000..30999
)

// simClock is an accelerated wall clock: base + (wall elapsed)*speed.
type simClock struct {
	base  time.Time
	start time.Time
	speed float64
}

func (c *simClock) Now() time.Time {
	return c.base.Add(time.Duration(float64(time.Since(c.start)) * c.speed))
}

func TestSoak(t *testing.T) {
	if !flag.Parsed() {
		flag.Parse()
	}
	rng := rand.New(rand.NewSource(*fSeed))

	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	q := gen.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), *fDuration+2*time.Minute)
	defer cancel()

	// Clock: fixed sim base 2026-06-01 12:00; the event runs 11:00..20:00 sim, so
	// even a long accelerated soak stays inside a running, pre-freeze event.
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	sc := &simClock{base: base, start: time.Now(), speed: *fSpeed}
	var clk clock.Clock = sc.Now
	if *fReal {
		clk = clock.System()
	}
	if _, err := q.CreateEvent(ctx, gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "Soak CTF", Description: "soak",
		StartsAt: base.Add(-time.Hour), EndsAt: base.Add(8 * time.Hour), FreezeAt: nil,
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}

	// Real hashing gate, sized to the host (login/register go through it).
	auth.ConfigureHashGate(auth.DefaultHashConcurrency(), 5*time.Second)

	sessions := auth.NewSessionStore(rdb, time.Hour)
	ev := events.New(q, clk)
	sb := scoreboard.New(q, rdb, ev, clk)
	usersSvc := users.New(q, sessions, true)
	teamsSvc := teams.New(pool, 4)

	var rt runtimepkg.ChallengeRuntime
	if *fDocker {
		dr, err := runtimepkg.NewDockerRuntime(q, testsupport.DiscardLogger(), "")
		if err != nil {
			t.Fatalf("docker runtime: %v", err)
		}
		rt = dr
	} else {
		rt = runtimepkg.NewFakeRuntimeWithClock(q, clk)
	}
	mgr := runtimepkg.NewManager(rt, q, "127.0.0.1", 30000, 30000+portRangeSize-1)
	sched := scheduler.New(mgr, q, ev, flags.NewGenerator("osctf"), audit.New(q, testsupport.DiscardLogger()), clk,
		testsupport.DiscardLogger(), scheduler.Config{TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 3, ReapAfter: 15 * time.Minute})

	hubCtx, hubCancel := context.WithCancel(ctx)
	defer hubCancel()
	hub := ws.NewHub(testsupport.DiscardLogger())
	go hub.Run(hubCtx)
	sb.SetBroadcaster(func(s scoreboard.Snapshot) { hub.BroadcastScoreboard(handlers.ToScoreboard(s)) })

	h := handlers.New(handlers.Deps{
		Users: usersSvc, Teams: teamsSvc, Events: ev,
		Challenges:  challenges.New(q, &memStore{m: map[string][]byte{}}),
		Submissions: submissions.New(pool, ev, clk, audit.New(q, testsupport.DiscardLogger())),
		Scoreboard:  sb, Runtime: mgr, Scheduler: sched,
		Recompute: func(rctx context.Context) { _ = sb.Recompute(rctx) },
		Auth:      auth.NewEmailPasswordProvider(q, nil), Sessions: sessions,
		Limiter: redisx.NewLimiter(rdb), Audit: audit.New(q, testsupport.DiscardLogger()),
		Log: testsupport.DiscardLogger(), SessionTTL: time.Hour, MaxAttachmentMB: 100,
		TrustProxy: true, SecureCookies: false, // per-actor X-Forwarded-For gives each its own rate-limit bucket
		RegisterIPBurst: 500, RegisterIPWindow: 10 * time.Minute,
	})
	mux := httpserver.New(httpserver.Deps{Log: testsupport.DiscardLogger(), Handlers: h, Sessions: sessions, BaseOrigin: origin, WSHandler: hub.Handler()})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	static, perTeam := seedWorld(ctx, t, q, usersSvc, teamsSvc)

	// ---- run ----
	m := &collector{pool: pool}
	m.snapshotBaseline()
	runCtx, runCancel := context.WithTimeout(ctx, *fDuration)
	defer runCancel()

	var wg sync.WaitGroup
	// Background workers: the real periodic lifecycle, driven on a wall ticker with
	// the (accelerated) clock deciding expiry/reap/phase.
	wg.Add(1)
	go func() { defer wg.Done(); runBackground(runCtx, sched, sb, mgr) }()
	// Metrics sampler.
	wg.Add(1)
	go func() { defer wg.Done(); m.sample(runCtx) }()
	// Actors.
	world := &world{base: srv.URL, static: static, perTeam: perTeam}
	for i := 0; i < *fActors; i++ {
		wg.Add(1)
		a := newActor(i, world, rng.Int63())
		go func() { defer wg.Done(); a.run(runCtx, m) }()
	}
	wg.Wait()

	m.report(t, sc, *fReal)
}

// ---------------- world seeding ----------------

type challengeRef struct {
	slug string
	flag string // the static flag (only meaningful for static-flag challenges)
}

type world struct {
	base    string
	static  []challengeRef // submittable static challenges
	perTeam []challengeRef // per-team container challenges (instance ops)
}

type userCred struct {
	email  string
	teamID uuid.UUID
}

var seededUsers []userCred // populated by seedWorld, consumed by newActor

func seedWorld(ctx context.Context, t *testing.T, q *gen.Queries, usersSvc *users.Service, teamsSvc *teams.Service) (static, perTeam []challengeRef) {
	t.Helper()
	// Users: numTeams*4 (~=120) so every team has a captain + members; actors index into them.
	perTeamCount := 4
	total := numTeams * perTeamCount
	if total < *fActors {
		total = *fActors
	}
	for i := 0; i < total; i++ {
		email := fmt.Sprintf("soaker%04d@soak.test", i)
		u, err := usersSvc.Register(ctx, users.RegisterInput{
			Username: fmt.Sprintf("soaker%04d", i), Email: email, Password: soakPassword,
		})
		if err != nil {
			t.Fatalf("seed user %d: %v", i, err)
		}
		seededUsers = append(seededUsers, userCred{email: email, teamID: u.ID}) // teamID filled after team assignment
	}
	// Teams: captain = user[t*perTeamCount]; the next few join.
	for tm := 0; tm < numTeams; tm++ {
		capIdx := tm * perTeamCount
		team, err := teamsSvc.Create(ctx, seededUsers[capIdx].teamID, fmt.Sprintf("Team-%02d", tm))
		if err != nil {
			t.Fatalf("create team %d: %v", tm, err)
		}
		seededUsers[capIdx].teamID = team.Row.ID
		for j := 1; j < perTeamCount; j++ {
			idx := capIdx + j
			if idx >= len(seededUsers) {
				break
			}
			if _, err := teamsSvc.Join(ctx, seededUsers[idx].teamID, team.Row.InviteCode); err != nil {
				t.Fatalf("join team %d user %d: %v", tm, idx, err)
			}
			seededUsers[idx].teamID = team.Row.ID
		}
	}
	// Challenges: 10 static (submittable) + 5 per-team container.
	for i := 0; i < numChallenges; i++ {
		id := uuid.Must(uuid.NewV7())
		slug := fmt.Sprintf("chal-%02d", i)
		if i < 10 {
			flag := fmt.Sprintf("OSCTF{static-%02d}", i)
			scoring := "static"
			var pointsMin, decay *int32
			if i%2 == 0 {
				scoring, pointsMin, decay = "dynamic", ptr(int32(100)), ptr(int32(50))
			}
			mustCreateChallenge(ctx, t, q, gen.CreateChallengeParams{
				ID: id, Slug: slug, Title: slug, Category: "misc", Kind: "standard", Flag: flag,
				Scoring: scoring, PointsInitial: 500, PointsMin: pointsMin, Decay: decay,
				Visible: true, MemLimitMb: 128, CpuMillis: 500,
				ContainerEnv: []byte("{}"), Instancing: ptr("shared"), FlagMode: ptr("static"),
				Egress: ptr(true), WritablePaths: []byte("[]"),
			})
			static = append(static, challengeRef{slug: slug, flag: flag})
		} else {
			img, port := "traefik/whoami:latest", int32(80)
			mustCreateChallenge(ctx, t, q, gen.CreateChallengeParams{
				ID: id, Slug: slug, Title: slug, Category: "pwn", Kind: "container", Flag: "OSCTF{placeholder}",
				Scoring: "static", PointsInitial: 500, Visible: true, Image: &img, InternalPort: &port,
				MemLimitMb: 128, CpuMillis: 500, ContainerEnv: []byte("{}"),
				Instancing: ptr("per_team"), FlagMode: ptr("per_instance"),
				Egress: ptr(true), WritablePaths: []byte("[]"),
			})
			perTeam = append(perTeam, challengeRef{slug: slug})
		}
	}
	return static, perTeam
}

func mustCreateChallenge(ctx context.Context, t *testing.T, q *gen.Queries, p gen.CreateChallengeParams) {
	t.Helper()
	if _, err := q.CreateChallenge(ctx, p); err != nil {
		t.Fatalf("create challenge %s: %v", p.Slug, err)
	}
}

func ptr[T any](v T) *T { return &v }

// ---------------- actors ----------------

type actor struct {
	id     int
	world  *world
	client *http.Client
	ip     string
	cred   userCred
	rng    *rand.Rand
}

func newActor(id int, w *world, seed int64) *actor {
	jar, _ := cookiejar.New(nil)
	return &actor{
		id: id, world: w,
		client: &http.Client{Jar: jar, Timeout: 20 * time.Second},
		ip:     fmt.Sprintf("10.%d.%d.2", id/256, id%256),
		cred:   seededUsers[id%len(seededUsers)],
		rng:    rand.New(rand.NewSource(seed)),
	}
}

func (a *actor) run(ctx context.Context, m *collector) {
	// Stagger startup so 100 actors don't log in on the same millisecond — a
	// synchronized argon2 login burst is what the hash-gate test already covers;
	// here we want a steady-state baseline, not that spike.
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(a.rng.Int63n(int64(5 * time.Second)))):
	}
	// Log in once for a session cookie. A failure is not fatal to the baseline —
	// the actor still exercises public GETs — and is recorded in the histogram.
	_ = a.do(ctx, m, http.MethodPost, "/api/v0/auth/login",
		fmt.Sprintf(`{"email":%q,"password":%q}`, a.cred.email, soakPassword))
	for ctx.Err() == nil {
		a.act(ctx, m)
	}
}

// act performs one weighted random action.
func (a *actor) act(ctx context.Context, m *collector) {
	switch n := a.rng.Intn(14); {
	case n < 3: // read scoreboard
		a.do(ctx, m, http.MethodGet, "/api/v0/scoreboard", "")
	case n < 6: // read challenges
		a.do(ctx, m, http.MethodGet, "/api/v0/challenges", "")
	case n < 10: // submit a flag (mostly wrong, ~20% correct)
		c := a.world.static[a.rng.Intn(len(a.world.static))]
		flag := fmt.Sprintf("OSCTF{wrong-%d}", a.rng.Int())
		if a.rng.Intn(5) == 0 {
			flag = c.flag
		}
		a.do(ctx, m, http.MethodPost, "/api/v0/challenges/"+c.slug+"/submit", fmt.Sprintf(`{"flag":%q}`, flag))
	case n < 12: // start a per-team instance
		c := a.world.perTeam[a.rng.Intn(len(a.world.perTeam))]
		a.do(ctx, m, http.MethodPost, "/api/v0/challenges/"+c.slug+"/instance", "")
	case n < 13: // extend
		c := a.world.perTeam[a.rng.Intn(len(a.world.perTeam))]
		a.do(ctx, m, http.MethodPost, "/api/v0/challenges/"+c.slug+"/instance/extend", "")
	default: // stop
		c := a.world.perTeam[a.rng.Intn(len(a.world.perTeam))]
		a.do(ctx, m, http.MethodDelete, "/api/v0/challenges/"+c.slug+"/instance", "")
	}
}

func (a *actor) do(ctx context.Context, m *collector, method, path, body string) int {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.world.base+path, rdr)
	if err != nil {
		return 0
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", origin)
	}
	req.Header.Set("X-Forwarded-For", a.ip)
	resp, err := a.client.Do(req)
	if err != nil {
		m.record(0)
		return 0
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	m.record(resp.StatusCode)
	return resp.StatusCode
}

// ---------------- background lifecycle ----------------

func runBackground(ctx context.Context, sched *scheduler.Scheduler, sb *scoreboard.Service, mgr *runtimepkg.Manager) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_ = sched.ExpireOnce(ctx)
			_, _ = sched.ReapStaleOnce(ctx)
			_, _ = sched.CleanupEnded(ctx)
			_ = mgr.Reconcile(ctx)
			_ = sb.Recompute(ctx)
		}
	}
}

// ---------------- metrics ----------------

type collector struct {
	pool *pgxpool.Pool

	actions  atomic.Int64
	statuses [6]atomic.Int64 // index: 0=err,1=2xx,2=4xx-other,3=429,4=503,5=5xx-other

	peakHeap       atomic.Uint64
	peakGoroutines atomic.Int64
	peakConns      atomic.Int64
	peakInstances  atomic.Int64

	baseExpiries float64
	baseSpawns   float64
}

func (m *collector) record(code int) {
	m.actions.Add(1)
	switch {
	case code == 0:
		m.statuses[0].Add(1)
	case code >= 200 && code < 300:
		m.statuses[1].Add(1)
	case code == 429:
		m.statuses[3].Add(1)
	case code == 503:
		m.statuses[4].Add(1)
	case code >= 500:
		m.statuses[5].Add(1)
	default:
		m.statuses[2].Add(1)
	}
}

func (m *collector) snapshotBaseline() {
	m.baseExpiries = metrics.CounterValue(metrics.InstanceExpiries)
	m.baseSpawns = metrics.CounterValue(metrics.InstanceSpawns)
}

func (m *collector) sample(ctx context.Context) {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			var ms goruntime.MemStats
			goruntime.ReadMemStats(&ms)
			liftUint(&m.peakHeap, ms.HeapInuse)
			liftInt(&m.peakGoroutines, int64(goruntime.NumGoroutine()))
			liftInt(&m.peakConns, int64(m.pool.Stat().AcquiredConns()))
			liftInt(&m.peakInstances, m.liveInstances(ctx))
		}
	}
}

func (m *collector) liveInstances(ctx context.Context) int64 {
	var n int64
	// running + pending rows are the port holders.
	_ = m.pool.QueryRow(ctx, `SELECT count(*) FROM instances WHERE state IN ('running','pending')`).Scan(&n)
	return n
}

func (m *collector) report(t *testing.T, sc *simClock, real bool) {
	t.Helper()
	var ms goruntime.MemStats
	goruntime.ReadMemStats(&ms)
	st := m.pool.Stat()
	total := m.actions.Load()
	runDur := *fDuration
	dur := runDur.Seconds()

	clockDesc := "system (real)"
	ratio := 1.0
	if !real {
		simElapsed := time.Since(sc.start).Seconds() * sc.speed
		ratio = simElapsed / time.Since(sc.start).Seconds()
		clockDesc = fmt.Sprintf("accelerated x%.0f", sc.speed)
	}

	peakInst := m.peakInstances.Load()
	line := strings.Repeat("─", 60)
	t.Logf("\n%s\nSOAK BASELINE  (commit 1: no faults, no invariants)\n%s", line, line)
	t.Logf("config      : actors=%d duration=%s seed=%d docker=%v clock=%s", *fActors, *fDuration, *fSeed, *fDocker, clockDesc)
	if !real {
		simEventHrs := (time.Since(sc.start).Seconds() * sc.speed) / 3600
		t.Logf("time        : %s wall simulated %.2f h of event  (ratio ~%.0fx sim:wall)", *fDuration, simEventHrs, ratio)
	}
	t.Logf("throughput  : %d actions  %.0f actions/s", total, float64(total)/dur)
	t.Logf("  by status : 2xx=%d 4xx=%d 429=%d 503=%d 5xx=%d err=%d",
		m.statuses[1].Load(), m.statuses[2].Load(), m.statuses[3].Load(), m.statuses[4].Load(), m.statuses[5].Load(), m.statuses[0].Load())
	t.Logf("heap inuse  : peak %d MiB  final %d MiB", m.peakHeap.Load()/(1<<20), ms.HeapInuse/(1<<20))
	t.Logf("goroutines  : peak %d  final %d", m.peakGoroutines.Load(), goruntime.NumGoroutine())
	t.Logf("db pool     : peak acquired %d  (idle %d, total %d, max %d)", m.peakConns.Load(), st.IdleConns(), st.TotalConns(), st.MaxConns())
	t.Logf("instances   : peak live %d  port-range util %.1f%% of %d", peakInst, 100*float64(peakInst)/float64(portRangeSize), portRangeSize)
	t.Logf("lifecycle   : spawns=%.0f expiries=%.0f (deltas over the run)",
		metrics.CounterValue(metrics.InstanceSpawns)-m.baseSpawns, metrics.CounterValue(metrics.InstanceExpiries)-m.baseExpiries)
	t.Logf("%s", line)

	if total == 0 {
		t.Fatal("no actions executed — harness did not drive the world")
	}
}

func liftUint(a *atomic.Uint64, v uint64) {
	for {
		cur := a.Load()
		if v <= cur || a.CompareAndSwap(cur, v) {
			return
		}
	}
}

func liftInt(a *atomic.Int64, v int64) {
	for {
		cur := a.Load()
		if v <= cur || a.CompareAndSwap(cur, v) {
			return
		}
	}
}

// ---------------- in-memory object store ----------------

type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.m[key] = data
	s.mu.Unlock()
	return nil
}

func (s *memStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	data, ok := s.m[key]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("soak memstore: %s not found", key)
	}
	return io.NopCloser(strings.NewReader(string(data))), nil
}

func (s *memStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	delete(s.m, key)
	s.mu.Unlock()
	return nil
}
