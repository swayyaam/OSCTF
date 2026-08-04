//go:build soak

// Package soak is the Phase 7 soak harness. It stands up the full platform
// in-process — real services, WS hub, scheduler, events/scoreboard on an
// injectable clock — and drives it with a fleet of participant actors over HTTP,
// sampling resource, throughput, and scheduler metrics throughout.
//
// Independent axes:
//
//	-real       system clock instead of the accelerated injected clock.
//	-docker     real Docker runtime instead of FakeRuntime (needs a daemon).
//	-scenario   IP model: mixed (shared NATs + a few unique, the realistic venue
//	            default), onenat (everyone behind one IP — venue worst case), or
//	            unique (one IP per actor — an unrealistic upper bound, for contrast).
//	-think      mean per-actor think-time between actions (0 = unpaced stress, used
//	            to find the resource ceiling; a realistic value keeps rate limits
//	            un-tripped).
//
// so you can run real containers on an accelerated clock, the fake on wall-clock
// time, a realistic paced venue, or an unpaced stress run — independently.
//
//	go test -tags soak -run TestSoak ./internal/soak -timeout 6m \
//	  -args -duration=2m -seed=1 [-real] [-docker] [-scenario=mixed] [-think=1.2s]
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
	"net/url"
	goruntime "runtime"
	"sort"
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
	fThink    = flag.Duration("think", 1200*time.Millisecond, "mean per-actor think-time between actions (0 = unpaced stress)")
	fScenario = flag.String("scenario", "mixed", "IP model: mixed | onenat | unique")
	fPool     = flag.Int("pool", 64, "DB pool max connections")
)

const (
	numTeams      = 30
	numChallenges = 16 // 10 static + 5 per-team (the pool) + 1 dedicated cursed per-team
	teamSize      = 4
	origin        = "http://soak.local" // matches BaseOrigin; the CSRF check compares the header, not the host
	soakPassword  = "soakPassw0rd!7"
	portRangeSize = 1000 // 30000..30999
)

// cohort assigns each actor a behaviour that exercises a distinct scheduler path.
type cohort int

const (
	cohortChurner    cohort = iota // read/submit/start/stop with pacing (the realistic majority)
	cohortCamper                   // start instances and never stop them → they reach TTL → expiry
	cohortExtender                 // start + extend repeatedly → extend, then refusal at max lifetime
	cohortFailDeploy               // target the cursed challenge → deploys fail → stale rows → reaper
)

// simClock is an accelerated wall clock (base + wall-elapsed*speed) with a jump
// offset so the end-of-run teardown can push simulated time past event end.
type simClock struct {
	base   time.Time
	start  time.Time
	speed  float64
	offset atomic.Int64 // extra simulated nanoseconds (jump)
}

func (c *simClock) Now() time.Time {
	return c.base.Add(time.Duration(float64(time.Since(c.start))*c.speed) + time.Duration(c.offset.Load()))
}
func (c *simClock) Jump(d time.Duration) { c.offset.Add(int64(d)) }

func TestSoak(t *testing.T) {
	if !flag.Parsed() {
		flag.Parse()
	}
	rng := rand.New(rand.NewSource(*fSeed))

	_, dsn := testsupport.Postgres(t)
	pool := biggerPool(t, dsn, *fPool)
	rdb := testsupport.Redis(t)
	q := gen.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), *fDuration+3*time.Minute)
	defer cancel()

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	sc := &simClock{base: base, start: time.Now(), speed: *fSpeed}
	var clk clock.Clock = sc.Now
	if *fReal {
		clk = clock.System()
	}
	// Event runs 11:00..14:00 sim (3h) so a 2m/120x run (~4h sim) reaches event
	// END within the run only via the post-run jump below; during the run it stays
	// running and pre-freeze.
	if _, err := q.CreateEvent(ctx, gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "Soak CTF", Description: "soak",
		StartsAt: base.Add(-time.Hour), EndsAt: base.Add(8 * time.Hour), FreezeAt: nil,
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}

	auth.ConfigureHashGate(auth.DefaultHashConcurrency(), 5*time.Second)

	sessions := auth.NewSessionStore(rdb, time.Hour)
	ev := events.New(q, clk)
	sb := scoreboard.New(q, rdb, ev, clk)
	usersSvc := users.New(q, sessions, true)
	teamsSvc := teams.New(pool, teamSize)

	cursed := fmt.Sprintf("chal-%02d", numChallenges-1) // the fail-deploy cohort targets this per-team challenge
	var baseRT runtimepkg.ChallengeRuntime
	if *fDocker {
		dr, err := runtimepkg.NewDockerRuntime(q, testsupport.DiscardLogger(), "")
		if err != nil {
			t.Fatalf("docker runtime: %v", err)
		}
		baseRT = dr
	} else {
		baseRT = runtimepkg.NewFakeRuntimeWithClock(q, clk)
	}
	rt := &faultyRuntime{ChallengeRuntime: baseRT}

	mgr := runtimepkg.NewManager(rt, q, "127.0.0.1", 30000, 30000+portRangeSize-1)
	// TTL/Extend/MaxTTL are simulated time (the scheduler reads the injected clock);
	// ReapAfter is WALL time (ListStale compares DB updated_at, written by Postgres,
	// against time.Now()). Small values so a 2m run exercises expiry, max-lifetime
	// refusal, and the reaper.
	sched := scheduler.New(mgr, q, ev, flags.NewGenerator("osctf"), audit.New(q, testsupport.DiscardLogger()), clk,
		testsupport.DiscardLogger(), scheduler.Config{
			TTL: 20 * time.Minute, Extend: 15 * time.Minute, MaxTTL: time.Hour, Quota: 3,
			ReapAfter: 8 * time.Second,
		})

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
		TrustProxy: true, SecureCookies: false,
		RegisterIPBurst: 500, RegisterIPWindow: 10 * time.Minute,
	})
	mux := httpserver.New(httpserver.Deps{Log: testsupport.DiscardLogger(), Handlers: h, Sessions: sessions, BaseOrigin: origin, WSHandler: hub.Handler()})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	static, perTeam := seedWorld(ctx, t, q, usersSvc, teamsSvc)

	m := &collector{pool: pool}
	m.snapshotBaseline()
	runCtx, runCancel := context.WithTimeout(ctx, *fDuration)
	defer runCancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); runBackground(runCtx, sched, sb, mgr, m) }()
	wg.Add(1)
	go func() { defer wg.Done(); m.sample(runCtx) }()

	w := &world{base: srv.URL, static: static, perTeam: perTeam, cursed: cursed}
	for i := 0; i < *fActors; i++ {
		wg.Add(1)
		a := newActor(ctx, t, i, w, sessions, rng.Int63())
		go func() { defer wg.Done(); a.run(runCtx, m) }()
	}
	wg.Wait()

	m.report(t, sc)
}

// biggerPool builds a pool with a raised MaxConns from testsupport's DSN.
func biggerPool(t *testing.T, dsn string, maxConns int) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	//nolint:gosec // G115: maxConns is a small test flag value.
	cfg.MaxConns = int32(maxConns)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// ---------------- world seeding ----------------

type challengeRef struct {
	slug string
	flag string
}

type world struct {
	base    string
	static  []challengeRef
	perTeam []challengeRef
	cursed  string
}

type userCred struct {
	userID uuid.UUID
	teamID uuid.UUID
}

var seededUsers []userCred

// cursedID is the challenge id whose deploys the faultyRuntime fails; set by
// seedWorld before any actor runs, read by faultyRuntime.Deploy during the run.
var cursedID atomic.Pointer[uuid.UUID]

func seedWorld(ctx context.Context, t *testing.T, q *gen.Queries, usersSvc *users.Service, teamsSvc *teams.Service) (static, perTeam []challengeRef) {
	t.Helper()
	total := numTeams * teamSize
	if total < *fActors {
		total = *fActors
	}
	for i := 0; i < total; i++ {
		u, err := usersSvc.Register(ctx, users.RegisterInput{
			Username: fmt.Sprintf("soaker%04d", i), Email: fmt.Sprintf("soaker%04d@soak.test", i), Password: soakPassword,
		})
		if err != nil {
			t.Fatalf("seed user %d: %v", i, err)
		}
		seededUsers = append(seededUsers, userCred{userID: u.ID})
	}
	for tm := 0; tm < numTeams; tm++ {
		capIdx := tm * teamSize
		team, err := teamsSvc.Create(ctx, seededUsers[capIdx].userID, fmt.Sprintf("Team-%02d", tm))
		if err != nil {
			t.Fatalf("create team %d: %v", tm, err)
		}
		seededUsers[capIdx].teamID = team.Row.ID
		for j := 1; j < teamSize && capIdx+j < len(seededUsers); j++ {
			if _, err := teamsSvc.Join(ctx, seededUsers[capIdx+j].userID, team.Row.InviteCode); err != nil {
				t.Fatalf("join team %d user %d: %v", tm, capIdx+j, err)
			}
			seededUsers[capIdx+j].teamID = team.Row.ID
		}
	}
	for i := 0; i < numChallenges; i++ {
		id := uuid.Must(uuid.NewV7())
		slug := fmt.Sprintf("chal-%02d", i)
		if i < 10 {
			flag := fmt.Sprintf("OSCTF{static-%02d}", i)
			scoring := "static"
			var pmin, decay *int32
			if i%2 == 0 {
				scoring, pmin, decay = "dynamic", ptr(int32(100)), ptr(int32(50))
			}
			mustCreateChallenge(ctx, t, q, gen.CreateChallengeParams{
				ID: id, Slug: slug, Title: slug, Category: "misc", Kind: "standard", Flag: flag,
				Scoring: scoring, PointsInitial: 500, PointsMin: pmin, Decay: decay, Visible: true,
				MemLimitMb: 128, CpuMillis: 500, ContainerEnv: []byte("{}"),
				Instancing: ptr("shared"), FlagMode: ptr("static"), Egress: ptr(true), WritablePaths: []byte("[]"),
			})
			static = append(static, challengeRef{slug: slug, flag: flag})
		} else {
			img, port := "traefik/whoami:latest", int32(80)
			mustCreateChallenge(ctx, t, q, gen.CreateChallengeParams{
				ID: id, Slug: slug, Title: slug, Category: "pwn", Kind: "container", Flag: "OSCTF{placeholder}",
				Scoring: "static", PointsInitial: 500, Visible: true, Image: &img, InternalPort: &port,
				MemLimitMb: 128, CpuMillis: 500, ContainerEnv: []byte("{}"),
				Instancing: ptr("per_team"), FlagMode: ptr("per_instance"), Egress: ptr(true), WritablePaths: []byte("[]"),
			})
			if i == numChallenges-1 {
				// The dedicated cursed challenge: deploys always fail. Kept OUT of the
				// general perTeam pool so only the fail-deploy cohort targets it, and its
				// 503s don't pollute every cohort's start path.
				cid := id
				cursedID.Store(&cid)
			} else {
				perTeam = append(perTeam, challengeRef{slug: slug})
			}
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

// ---------------- faulty runtime ----------------

// faultyRuntime wraps a ChallengeRuntime and fails every Deploy of the cursed
// challenge, so the fail-deploy cohort produces error rows for the reaper to
// clear. (Commit 2 extends this with seeded random faults.)
type faultyRuntime struct {
	runtimepkg.ChallengeRuntime
}

func (f *faultyRuntime) Deploy(ctx context.Context, spec runtimepkg.InstanceSpec) (runtimepkg.Instance, error) {
	if c := cursedID.Load(); c != nil && spec.ChallengeID == *c {
		return runtimepkg.Instance{}, runtimepkg.ErrDaemonTimeout // deploy-time fault → error row
	}
	return f.ChallengeRuntime.Deploy(ctx, spec)
}

// ---------------- actors ----------------

type actor struct {
	id     int
	world  *world
	client *http.Client
	ip     string
	coh    cohort
	rng    *rand.Rand
}

func newActor(ctx context.Context, t *testing.T, id int, w *world, sessions *auth.SessionStore, seed int64) *actor {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	cred := seededUsers[id%len(seededUsers)]
	// Pre-create a session so the steady-state load isn't confounded by a login
	// burst; the actor still exercises login once at startup (below).
	sess, err := sessions.Create(ctx, cred.userID, "user", "10.0.0.1", "soak")
	if err != nil {
		t.Fatalf("session for actor %d: %v", id, err)
	}
	u, _ := url.Parse(w.base)
	jar.SetCookies(u, []*http.Cookie{{Name: auth.CookieName, Value: sess.Token, Path: "/"}})

	return &actor{
		id: id, world: w,
		client: &http.Client{Jar: jar, Timeout: 20 * time.Second},
		ip:     actorIP(id),
		coh:    cohortOf(id),
		rng:    rand.New(rand.NewSource(seed)),
	}
}

// actorIP maps an actor to a client IP under the chosen scenario.
func actorIP(id int) string {
	switch *fScenario {
	case "onenat":
		return "203.0.113.1"
	case "unique":
		return fmt.Sprintf("10.%d.%d.2", id/256, id%256)
	default: // mixed: 4 shared NATs hold most actors; the last few are unique
		if id >= *fActors-4 {
			return fmt.Sprintf("10.9.%d.2", id)
		}
		return fmt.Sprintf("203.0.113.%d", 10+(id%4))
	}
}

func cohortOf(id int) cohort {
	switch {
	case id%100 < 15:
		return cohortCamper
	case id%100 < 30:
		return cohortExtender
	case id%100 < 40:
		return cohortFailDeploy
	default:
		return cohortChurner
	}
}

func (a *actor) run(ctx context.Context, m *collector) {
	// Stagger startup, then one HTTP login on the actor's IP (measured; may 429 on a
	// shared NAT — the actor keeps its pre-created session cookie either way).
	if !sleepCtx(ctx, time.Duration(a.rng.Int63n(int64(5*time.Second)))) {
		return
	}
	m.recordAction("login", a.do(ctx, http.MethodPost, "/api/v0/auth/login",
		fmt.Sprintf(`{"email":%q,"password":%q}`, fmt.Sprintf("soaker%04d@soak.test", a.id%len(seededUsers)), soakPassword)))
	for ctx.Err() == nil {
		a.act(ctx, m)
		if *fThink > 0 {
			// Exponential think-time, mean *fThink.
			if !sleepCtx(ctx, time.Duration(a.rng.ExpFloat64()*float64(*fThink))) {
				return
			}
		}
	}
}

func (a *actor) act(ctx context.Context, m *collector) {
	pt := func() challengeRef { return a.world.perTeam[a.rng.Intn(len(a.world.perTeam))] }
	switch a.coh {
	case cohortCamper:
		// Start occasionally, then leave instances alone (reads) so they sit idle to
		// TTL and expire — hammering start would starve ExpireOnce of the team lock.
		if a.rng.Intn(4) == 0 {
			m.recordAction("start", a.do(ctx, http.MethodPost, "/api/v0/challenges/"+pt().slug+"/instance", ""))
		} else {
			m.recordAction("scoreboard", a.do(ctx, http.MethodGet, "/api/v0/scoreboard", ""))
		}
	case cohortExtender:
		c := pt()
		if a.rng.Intn(2) == 0 {
			m.recordAction("start", a.do(ctx, http.MethodPost, "/api/v0/challenges/"+c.slug+"/instance", ""))
		} else {
			m.recordAction("extend", a.do(ctx, http.MethodPost, "/api/v0/challenges/"+c.slug+"/instance/extend", ""))
		}
	case cohortFailDeploy:
		// Leave one failed deploy as a pending row, then read — hammering start would
		// re-touch the row (never ageing past ReapAfter) and hold the team lock the
		// reaper's non-blocking destroy needs, starving the very path under test.
		if a.rng.Intn(6) == 0 {
			m.recordAction("start", a.do(ctx, http.MethodPost, "/api/v0/challenges/"+a.world.cursed+"/instance", ""))
		} else {
			m.recordAction("scoreboard", a.do(ctx, http.MethodGet, "/api/v0/scoreboard", ""))
		}
	default: // churner
		switch n := a.rng.Intn(12); {
		case n < 3:
			m.recordAction("scoreboard", a.do(ctx, http.MethodGet, "/api/v0/scoreboard", ""))
		case n < 6:
			m.recordAction("challenges", a.do(ctx, http.MethodGet, "/api/v0/challenges", ""))
		case n < 9:
			c := a.world.static[a.rng.Intn(len(a.world.static))]
			flag := fmt.Sprintf("OSCTF{wrong-%d}", a.rng.Int())
			if a.rng.Intn(5) == 0 {
				flag = c.flag
			}
			m.recordAction("submit", a.do(ctx, http.MethodPost, "/api/v0/challenges/"+c.slug+"/submit", fmt.Sprintf(`{"flag":%q}`, flag)))
		case n < 11:
			m.recordAction("start", a.do(ctx, http.MethodPost, "/api/v0/challenges/"+pt().slug+"/instance", ""))
		default:
			m.recordAction("stop", a.do(ctx, http.MethodDelete, "/api/v0/challenges/"+pt().slug+"/instance", ""))
		}
	}
}

func (a *actor) do(ctx context.Context, method, path, body string) int {
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
		return 0
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// ---------------- background lifecycle ----------------

func runBackground(ctx context.Context, sched *scheduler.Scheduler, sb *scoreboard.Service, mgr *runtimepkg.Manager, m *collector) {
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			// NOTE: CleanupEnded is deliberately NOT called here — it destroys ALL
			// per-team instances unconditionally (it is the event-end teardown, gated
			// by the caller on the ended phase). Calling it mid-run would wipe every
			// instance every tick. It runs only in the end-of-run teardown.
			_ = sched.ExpireOnce(ctx)
			if n, err := sched.ReapStaleOnce(ctx); err == nil && n > 0 {
				m.reaped.Add(int64(n))
			}
			_ = mgr.Reconcile(ctx)
			_ = sb.Recompute(ctx)
		}
	}
}

// ---------------- metrics ----------------

type collector struct {
	pool *pgxpool.Pool

	actions atomic.Int64
	// per-action-type 429s + successes, keyed by a small fixed set.
	rl    sync.Mutex
	byAct map[string]*actStat

	reaped atomic.Int64

	peakHeap       atomic.Uint64
	peakGoroutines atomic.Int64
	peakConns      atomic.Int64
	peakInstances  atomic.Int64

	baseExpiries float64
	baseSpawns   float64
}

type actStat struct {
	total int64
	codes map[int]int64
}

func (s *actStat) n(code int) int64 { return s.codes[code] }

func (m *collector) recordAction(name string, code int) {
	m.actions.Add(1)
	m.rl.Lock()
	if m.byAct == nil {
		m.byAct = map[string]*actStat{}
	}
	s := m.byAct[name]
	if s == nil {
		s = &actStat{codes: map[int]int64{}}
		m.byAct[name] = s
	}
	s.total++
	s.codes[code]++
	m.rl.Unlock()
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
			var n int64
			_ = m.pool.QueryRow(ctx, `SELECT count(*) FROM instances WHERE state IN ('running','pending')`).Scan(&n)
			liftInt(&m.peakInstances, n)
		}
	}
}

func (m *collector) report(t *testing.T, sc *simClock) {
	t.Helper()
	var ms goruntime.MemStats
	goruntime.ReadMemStats(&ms)
	st := m.pool.Stat()
	total := m.actions.Load()
	runDur := *fDuration
	dur := runDur.Seconds()

	line := strings.Repeat("─", 66)
	t.Logf("\n%s\nSOAK  (scenario=%s think=%s pool=%d docker=%v)\n%s", line, *fScenario, *fThink, *fPool, *fDocker, line)
	if !*fReal {
		simHrs := (time.Since(sc.start).Seconds() * sc.speed) / 3600
		t.Logf("time        : %s wall  ~%.2f h simulated  (%.0fx)", runDur, simHrs, sc.speed)
	} else {
		t.Logf("time        : %s wall  (real clock)", runDur)
	}
	t.Logf("throughput  : %d actions  %.0f actions/s  (%d actors)", total, float64(total)/dur, *fActors)

	// 429 breakdown by action — under a realistic profile this should be ~0 except
	// where a real limit is genuinely tight for the scenario.
	var total429 int64
	m.rl.Lock()
	for _, name := range []string{"login", "submit", "start", "extend", "stop", "scoreboard", "challenges"} {
		s := m.byAct[name]
		if s == nil || s.total == 0 {
			continue
		}
		total429 += s.n(429)
		t.Logf("  %-11s: %5d  [%s]", name, s.total, histo(s.codes))
	}
	m.rl.Unlock()
	t.Logf("429 total   : %d  (%.2f%% of actions)", total429, 100*float64(total429)/float64(max64(total, 1)))

	t.Logf("heap inuse  : peak %d MiB  final %d MiB", m.peakHeap.Load()/(1<<20), ms.HeapInuse/(1<<20))
	t.Logf("goroutines  : peak %d  final %d", m.peakGoroutines.Load(), goruntime.NumGoroutine())
	t.Logf("db pool     : peak acquired %d / %d max  (idle %d, total %d)", m.peakConns.Load(), st.MaxConns(), st.IdleConns(), st.TotalConns())
	poolBound := "no (ceiling is elsewhere: CPU / DB / lock contention)"
	if m.peakConns.Load() >= int64(st.MaxConns()) {
		poolBound = "YES — raise -pool"
	}
	t.Logf("pool-bound? : %s", poolBound)
	peakInst := m.peakInstances.Load()
	t.Logf("instances   : peak live %d  port-range util %.1f%% of %d", peakInst, 100*float64(peakInst)/float64(portRangeSize), portRangeSize)

	// Scheduler exercise (7-3): these must be nonzero or the soak isn't testing the scheduler.
	extends, refusals := m.ok2xx("extend"), m.code("extend", 409)
	quota := m.code("start", 409)
	t.Logf("scheduler   : expiries=%.0f  spawns=%.0f  extends=%d  extend-refusals(409)=%d  start-quota(409)=%d  reaped=%d",
		metrics.CounterValue(metrics.InstanceExpiries)-m.baseExpiries, metrics.CounterValue(metrics.InstanceSpawns)-m.baseSpawns,
		extends, refusals, quota, m.reaped.Load())
	// DB state diagnostic: stuck pending/error rows mean the reaper isn't clearing;
	// running rows with no expires_at mean expiry can't fire.
	dctx := context.Background()
	rows, _ := m.pool.Query(dctx, `SELECT state, count(*) FROM instances GROUP BY state ORDER BY state`)
	var states []string
	for rows.Next() {
		var s string
		var n int
		_ = rows.Scan(&s, &n)
		states = append(states, fmt.Sprintf("%s=%d", s, n))
	}
	rows.Close()
	var runNoTTL int
	_ = m.pool.QueryRow(dctx, `SELECT count(*) FROM instances WHERE state='running' AND expires_at IS NULL`).Scan(&runNoTTL)
	t.Logf("db state    : %s  (running-without-ttl=%d)", strings.Join(states, " "), runNoTTL)
	t.Logf("%s", line)

	if total == 0 {
		t.Fatal("no actions executed")
	}
}

func (m *collector) code(name string, code int) int64 {
	m.rl.Lock()
	defer m.rl.Unlock()
	if s := m.byAct[name]; s != nil {
		return s.codes[code]
	}
	return 0
}

func (m *collector) ok2xx(name string) int64 {
	m.rl.Lock()
	defer m.rl.Unlock()
	var n int64
	if s := m.byAct[name]; s != nil {
		for c, v := range s.codes {
			if c >= 200 && c < 300 {
				n += v
			}
		}
	}
	return n
}

// histo renders a code→count map sorted by code, e.g. "200:396 404:59 503:300".
func histo(codes map[int]int64) string {
	keys := make([]int, 0, len(codes))
	for c := range codes {
		keys = append(keys, c)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, c := range keys {
		parts = append(parts, fmt.Sprintf("%d:%d", c, codes[c]))
	}
	return strings.Join(parts, " ")
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
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
