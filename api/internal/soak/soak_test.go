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
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osctf/platform/internal/apigen"
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
	"github.com/osctf/platform/internal/scoring"
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
	fFaults   = flag.Bool("faults", true, "inject seeded deploy/destroy/reconcile faults + container vanish/exit")
	fWSC      = flag.Int("wsclients", 8, "scoreboard WebSocket clients that must converge to the REST snapshot")
	fBreakSB  = flag.Bool("break-scoreboard", false, "DEBUG: stop recomputing the scoreboard so REST goes stale — proves the from-scratch invariant bites")
	fAgeLost  = flag.Bool("age-lost-rows", true, "age a vanished row's updated_at past reconcile's DB-wall grace so lost→reap fires in-run; -age-lost-rows=false measures the vanish→lost→reap path going dark under an accelerated clock")
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
	var fake *runtimepkg.FakeRuntime
	if *fDocker {
		dr, err := runtimepkg.NewDockerRuntime(q, testsupport.DiscardLogger(), "")
		if err != nil {
			t.Fatalf("docker runtime: %v", err)
		}
		baseRT = dr
	} else {
		fake = runtimepkg.NewFakeRuntimeWithClock(q, clk)
		baseRT = fake
	}
	rt := &faultyRuntime{ChallengeRuntime: baseRT, fake: fake, faults: *fFaults, rng: rand.New(rand.NewSource(*fSeed ^ 0x5a17))}

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
		Recompute: func(rctx context.Context) {
			if !*fBreakSB { // -break-scoreboard: stop refreshing the cache so REST goes stale
				_ = sb.Recompute(rctx)
			}
		},
		Auth: auth.NewEmailPasswordProvider(q, nil), Sessions: sessions,
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

	// An authenticated client for the invariant checker's participant-surface scans.
	checkGet := authedGetter(ctx, t, srv.URL, sessions)
	wsColl := &wsCollector{latest: map[int]map[uuid.UUID]int{}}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); runBackground(runCtx, sched, sb, mgr, m) }()
	wg.Add(1)
	go func() { defer wg.Done(); m.sample(runCtx) }()
	wg.Add(1)
	go func() { defer wg.Done(); runFaults(runCtx, pool, fake, m, *fSeed) }()
	wg.Add(1)
	go func() { defer wg.Done(); m.checkInvariants(runCtx, pool, q, sb, checkGet) }()

	// WS clients live on their own context so they outlast the actors and receive the
	// final quiescent broadcast before the convergence check.
	wsCtx, wsCancel := context.WithCancel(ctx)
	var wsWG sync.WaitGroup
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v0/ws"
	for i := 0; i < *fWSC; i++ {
		wsWG.Add(1)
		go func(id int) { defer wsWG.Done(); wsClient(wsCtx, wsURL, id, wsColl) }(i)
	}

	w := &world{base: srv.URL, static: static, perTeam: perTeam, cursed: cursed}
	for i := 0; i < *fActors; i++ {
		wg.Add(1)
		a := newActor(ctx, t, i, w, sessions, rng.Int63())
		go func() { defer wg.Done(); a.run(runCtx, m) }()
	}
	wg.Wait() // actors + background + faults + invariants done; WS clients still live

	// End-of-run teardown: jump the clock past event end, converge CleanupEnded, and
	// assert zero live per-team instances + the full port range reclaimed. This is the
	// single moment a real event changes the most state at once.
	endOfRunTeardown(ctx, t, sc, sched, pool)

	// Quiesced (no new solves): a couple of final recomputes flush the throttled
	// broadcast to the still-connected WS clients, which must then match REST.
	for i := 0; i < 3; i++ {
		_ = sb.Recompute(ctx)
		time.Sleep(600 * time.Millisecond)
	}
	m.wsConverged, m.wsChecked = checkWSConvergence(ctx, sb, wsColl)
	wsCancel()
	wsWG.Wait()

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

// faultyRuntime wraps a ChallengeRuntime: it always fails the cursed challenge's
// deploys (deterministic, for the fail-deploy cohort) and, when -faults is set,
// injects seeded random deploy/destroy failures. The fault injector (runFaults)
// additionally vanishes/exits live containers via fake.
type faultyRuntime struct {
	runtimepkg.ChallengeRuntime
	fake   *runtimepkg.FakeRuntime // nil in -docker mode
	faults bool
	mu     sync.Mutex
	rng    *rand.Rand
}

func (f *faultyRuntime) chance(p float64) bool {
	if !f.faults {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rng.Float64() < p
}

func (f *faultyRuntime) Deploy(ctx context.Context, spec runtimepkg.InstanceSpec) (runtimepkg.Instance, error) {
	if c := cursedID.Load(); c != nil && spec.ChallengeID == *c {
		return runtimepkg.Instance{}, runtimepkg.ErrDaemonTimeout // deterministic: the cursed challenge
	}
	if f.chance(0.04) {
		return runtimepkg.Instance{}, runtimepkg.ErrDaemonTimeout // random deploy fault → stuck row → reaper
	}
	return f.ChallengeRuntime.Deploy(ctx, spec)
}

func (f *faultyRuntime) Destroy(ctx context.Context, id uuid.UUID) error {
	if f.chance(0.03) {
		return runtimepkg.ErrDaemonTimeout // transient destroy fault; the row stays, retried later
	}
	return f.ChallengeRuntime.Destroy(ctx, id)
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
			if !*fBreakSB {
				_ = sb.Recompute(ctx)
			}
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

	reaped         atomic.Int64
	faultsInjected atomic.Int64

	invChecks atomic.Int64
	invCostNs atomic.Int64
	invLag    atomic.Int64 // scoreboard checks that mismatched transiently then reconciled (lag, not a bug)

	vmu        sync.Mutex
	viol       map[string]int // invariant name → count of confirmed violations
	violSample []string       // a few human-readable examples

	wsConverged, wsChecked int

	peakHeap       atomic.Uint64
	peakGoroutines atomic.Int64
	peakConns      atomic.Int64
	peakInstances  atomic.Int64

	baseExpiries float64
	baseSpawns   float64
}

func (m *collector) recordViol(name, detail string) {
	m.vmu.Lock()
	if m.viol == nil {
		m.viol = map[string]int{}
	}
	m.viol[name]++
	if len(m.violSample) < 12 {
		m.violSample = append(m.violSample, name+": "+detail)
	}
	m.vmu.Unlock()
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

	avgMs := 0.0
	if c := m.invChecks.Load(); c > 0 {
		avgMs = float64(m.invCostNs.Load()) / float64(c) / 1e6
	}
	t.Logf("faults      : container vanish/exit injected=%d  (+seeded deploy/destroy faults inline)", m.faultsInjected.Load())
	t.Logf("invariants  : %d checks  avg %.1f ms/check  scoreboard transient-lag=%d", m.invChecks.Load(), avgMs, m.invLag.Load())
	t.Logf("ws converge : %d/%d clients matched the REST snapshot", m.wsConverged, m.wsChecked)

	m.vmu.Lock()
	nviol := 0
	for _, c := range m.viol {
		nviol += c
	}
	viols, samples := m.viol, m.violSample
	m.vmu.Unlock()
	if nviol == 0 {
		t.Logf("invariants  : ALL HELD (0 confirmed violations)")
	} else {
		t.Logf("invariants  : %d CONFIRMED VIOLATIONS %v", nviol, viols)
		for _, s := range samples {
			t.Logf("   • %s", s)
		}
	}
	t.Logf("%s", line)

	if total == 0 {
		t.Fatal("no actions executed")
	}
	if nviol > 0 {
		t.Errorf("%d confirmed invariant violations: %v", nviol, viols)
	}
	// A strict majority of the configured clients must converge — "at least one
	// converged" isn't an assertion. 8/8 every seed across the sweep, so a majority
	// threshold sits well inside the observed noise while still failing a real
	// broadcast regression (a client that never connects counts against the gate).
	if *fWSC > 0 {
		if want := *fWSC/2 + 1; m.wsConverged < want {
			t.Errorf("WS convergence below majority: %d/%d configured clients matched the REST snapshot (%d received a board), want >= %d",
				m.wsConverged, *fWSC, m.wsChecked, want)
		}
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

// ---------------- fault injection ----------------

// runFaults vanishes/exits a random live container periodically (fake only), so
// reconcile marks the row lost and the reaper reclaims it — exercising the
// lost→reap path under load. faultyRuntime handles the deploy/destroy faults.
func runFaults(ctx context.Context, pool *pgxpool.Pool, fake *runtimepkg.FakeRuntime, m *collector, seed int64) {
	if fake == nil || !*fFaults {
		return
	}
	rng := rand.New(rand.NewSource(seed ^ 0x7a1c))
	tick := time.NewTicker(700 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			var id uuid.UUID
			if err := pool.QueryRow(ctx, `SELECT id FROM instances WHERE state='running' ORDER BY random() LIMIT 1`).Scan(&id); err != nil {
				continue
			}
			if rng.Intn(2) == 0 {
				fake.VanishContainer(id)
			} else {
				fake.ExitContainer(id)
			}
			// Age the row past reconcile's grace (which trails the ~150s-wall deploy
			// timeout) so the vanished container is marked lost this run and then
			// reclaimed by the reaper — the fault path we want to exercise; a 2m run
			// would otherwise never reach the grace. This manufactures the DB-clock
			// elapsed-time PRECONDITION only; the grace comparison itself
			// (clock_timestamp() - updated_at >= reconcileGrace) then runs unchanged.
			// Grace is anchored to updated_at, written by Postgres now() (DB clock),
			// so the accelerated app clock cannot reach it — see AGENTS.md.
			if *fAgeLost {
				_, _ = pool.Exec(ctx, `UPDATE instances SET updated_at = now() - interval '3 minutes' WHERE id=$1 AND state='running'`, id)
			}
			m.faultsInjected.Add(1)
		}
	}
}

// ---------------- continuous invariants ----------------

func (m *collector) checkInvariants(ctx context.Context, pool *pgxpool.Pool, q *gen.Queries, sb *scoreboard.Service, get func(string) []byte) {
	// Sampled, not every tick: the from-scratch recompute reads the whole solve log.
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			start := time.Now()
			m.oneInvariantPass(ctx, pool, q, sb, get)
			m.invChecks.Add(1)
			m.invCostNs.Add(int64(time.Since(start)))
		}
	}
}

func (m *collector) oneInvariantPass(ctx context.Context, pool *pgxpool.Pool, q *gen.Queries, sb *scoreboard.Service, get func(string) []byte) {
	// Quota: no team ever has more than the configured running instances.
	var overQuota int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT team_id FROM instances WHERE state='running' AND team_id IS NOT NULL GROUP BY team_id HAVING count(*) > 3) x`).Scan(&overQuota)
	if overQuota > 0 {
		m.recordViol("quota-exceeded", fmt.Sprintf("%d teams over quota 3", overQuota))
	}
	// Port range: no duplicate host_port, and never more allocated than the range holds.
	var used, distinct int
	_ = pool.QueryRow(ctx, `SELECT count(host_port), count(DISTINCT host_port) FROM instances WHERE host_port IS NOT NULL`).Scan(&used, &distinct)
	if used != distinct {
		m.recordViol("duplicate-port", fmt.Sprintf("%d ports held vs %d distinct", used, distinct))
	}
	if distinct > portRangeSize {
		m.recordViol("port-range-exceeded", fmt.Sprintf("%d > %d", distinct, portRangeSize))
	}
	// Scoreboard: what REST SERVES (the cached snapshot, exactly as a client sees it —
	// no forced recompute here) must equal an independent from-scratch recompute of
	// the solve log. Under continuous solves the cache lags the log by up to the
	// background recompute interval, so a one-shot mismatch is lag; re-read after a
	// pause longer than that interval, and a mismatch that survives is a real stale-
	// cache / broken-recompute divergence.
	if ok, _ := scoreboardMatches(ctx, q, sb); !ok {
		time.Sleep(600 * time.Millisecond) // > the 400ms background recompute interval
		if ok2, d2 := scoreboardMatches(ctx, q, sb); !ok2 {
			m.recordViol("scoreboard-mismatch", d2) // survived a full recompute cycle → real
		} else {
			m.invLag.Add(1)
		}
	}
	// No flag on a participant surface, points non-negative.
	if snap, err := sb.Current(ctx, false); err == nil {
		for _, e := range snap.Standings {
			if e.Points < 0 {
				m.recordViol("negative-points", fmt.Sprintf("team %s: %d", e.TeamID, e.Points))
				break
			}
		}
	}
	for _, path := range []string{"/api/v0/scoreboard", "/api/v0/challenges"} {
		if bytes.Contains(get(path), []byte("OSCTF{")) {
			m.recordViol("flag-leak", path)
		}
	}
}

// scoreboardMatches reports whether the scoreboard REST SERVES (sb.Current — the
// cached snapshot, no forced recompute) equals an independent from-scratch
// recompute of the solve log, and a detail string for the first discrepancy. It
// records nothing — the caller decides (after a delayed re-read) whether a
// mismatch is a confirmed stale-cache violation or ordinary lag.
func scoreboardMatches(ctx context.Context, q *gen.Queries, sb *scoreboard.Service) (bool, string) {
	want, err := independentStandings(ctx, q)
	if err != nil {
		return true, ""
	}
	snap, err := sb.Current(ctx, false)
	if err != nil {
		return true, ""
	}
	got := map[uuid.UUID]int{}
	for _, e := range snap.Standings {
		got[e.TeamID] = e.Points
	}
	for tid, pts := range want {
		if got[tid] != pts {
			return false, fmt.Sprintf("team %s: rest=%d fromscratch=%d", tid, got[tid], pts)
		}
	}
	for tid, pts := range got {
		if pts != 0 && want[tid] != pts {
			return false, fmt.Sprintf("overcount team %s: rest=%d fromscratch=%d", tid, pts, want[tid])
		}
	}
	return true, ""
}

// independentStandings recomputes team points straight from the valid-solve log
// and the pure scoring engine, replicating compute()'s rules (per-challenge value
// from the non-banned solve count) without touching the scoreboard service's
// snapshot/cache/broadcast path — so a stale cache or broadcast divergence shows.
func independentStandings(ctx context.Context, q *gen.Queries) (map[uuid.UUID]int, error) {
	solves, err := q.ListValidSolves(ctx)
	if err != nil {
		return nil, err
	}
	type cp struct {
		mode string
		p    scoring.ChallengeScoring
	}
	count := map[uuid.UUID]int{}
	params := map[uuid.UUID]cp{}
	for _, r := range solves {
		if _, ok := params[r.ChallengeID]; !ok {
			c := cp{mode: r.Scoring, p: scoring.ChallengeScoring{Initial: int(r.PointsInitial)}}
			if r.PointsMin != nil {
				c.p.Min = int(*r.PointsMin)
			}
			if r.Decay != nil {
				c.p.Decay = int(*r.Decay)
			}
			params[r.ChallengeID] = c
		}
		if !r.TeamBanned {
			count[r.ChallengeID]++
		}
	}
	value := make(map[uuid.UUID]int, len(params))
	for id, c := range params {
		value[id] = scoring.Value(c.mode, c.p, count[id])
	}
	pts := map[uuid.UUID]int{}
	for _, r := range solves {
		pts[r.TeamID] += value[r.ChallengeID]
	}
	return pts, nil
}

// ---------------- WebSocket convergence ----------------

type wsCollector struct {
	mu     sync.Mutex
	latest map[int]map[uuid.UUID]int
}

func (c *wsCollector) set(id int, s map[uuid.UUID]int) {
	c.mu.Lock()
	c.latest[id] = s
	c.mu.Unlock()
}

func wsClient(ctx context.Context, wsURL string, id int, coll *wsCollector) {
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(4 << 20)
	for ctx.Err() == nil {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var env struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(data, &env) != nil || env.Type != "scoreboard" {
			continue
		}
		var snap apigen.Scoreboard
		if json.Unmarshal(env.Data, &snap) != nil {
			continue
		}
		st := make(map[uuid.UUID]int, len(snap.Standings))
		for _, e := range snap.Standings {
			st[e.TeamId] = e.Points
		}
		coll.set(id, st)
	}
}

// checkWSConvergence asserts every WS client that received a board has converged
// to the REST snapshot. Returns (converged, checked).
func checkWSConvergence(ctx context.Context, sb *scoreboard.Service, coll *wsCollector) (converged, checked int) {
	snap, err := sb.Current(ctx, false)
	if err != nil {
		return 0, 0
	}
	rest := map[uuid.UUID]int{}
	for _, e := range snap.Standings {
		if e.Points != 0 {
			rest[e.TeamID] = e.Points
		}
	}
	coll.mu.Lock()
	defer coll.mu.Unlock()
	for _, st := range coll.latest {
		if len(st) == 0 {
			continue // never received a non-empty board
		}
		checked++
		nz := map[uuid.UUID]int{}
		for tid, p := range st {
			if p != 0 {
				nz[tid] = p
			}
		}
		if len(nz) == len(rest) && func() bool {
			for tid, p := range nz {
				if rest[tid] != p {
					return false
				}
			}
			return true
		}() {
			converged++
		}
	}
	return converged, checked
}

// ---------------- end-of-run teardown ----------------

// endOfRunTeardown advances simulated time past the event end, converges the
// event-end cleanup, and asserts zero live per-team instances and a fully
// reclaimed port range — the single moment a real event changes the most state.
func endOfRunTeardown(ctx context.Context, t *testing.T, sc *simClock, sched *scheduler.Scheduler, pool *pgxpool.Pool) {
	t.Helper()
	if sc == nil {
		return // -real clock: cannot jump past the event end
	}
	sc.Jump(10 * time.Hour) // event ends at base+8h
	deadline := time.Now().Add(30 * time.Second)
	for {
		remaining, err := sched.CleanupEnded(ctx)
		if err != nil {
			t.Fatalf("teardown CleanupEnded: %v", err)
		}
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("event-end teardown did not converge: %d per-team instances remain", remaining)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var live int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM instances WHERE team_id IS NOT NULL AND state IN ('running','pending','error','lost')`).Scan(&live)
	if live != 0 {
		t.Errorf("after event-end teardown: %d per-team instances still present, want 0", live)
	}
	var ports int
	_ = pool.QueryRow(ctx, `SELECT count(DISTINCT host_port) FROM instances WHERE host_port IS NOT NULL`).Scan(&ports)
	if ports != 0 {
		t.Errorf("after event-end teardown: %d host ports still reserved, want 0 (port range not fully reclaimed)", ports)
	}
}

// authedGetter returns a GET closure authenticated as a participant, for the
// invariant checker's participant-surface scans.
func authedGetter(ctx context.Context, t *testing.T, base string, sessions *auth.SessionStore) func(string) []byte {
	t.Helper()
	sess, err := sessions.Create(ctx, seededUsers[0].userID, "user", "10.0.0.9", "checker")
	if err != nil {
		t.Fatalf("checker session: %v", err)
	}
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(base)
	jar.SetCookies(u, []*http.Cookie{{Name: auth.CookieName, Value: sess.Token, Path: "/"}})
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	return func(path string) []byte {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return nil
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return b
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
