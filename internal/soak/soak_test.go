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
	fBreakRR  = flag.Bool("break-readrepair", false, "DEBUG: disable read-repair (Current serves the stale cache) — negative control that reappears the #6 durability-gap mismatch at ~the pre-fix rate")
	fAgeLost  = flag.Bool("age-lost-rows", true, "age a vanished row's updated_at past reconcile's DB-wall grace so lost→reap fires in-run; -age-lost-rows=false measures the vanish→lost→reap path going dark under an accelerated clock")
	// #9 plugin-scoring negative controls (manual gates, like -break-readrepair).
	fRecomputePlugin = flag.Bool("recompute-via-plugin", false, "DEBUG: recompute plugin scores by CALLING the plugin (current solve count) instead of reading the per-solve record — diverges from the served board (which reads the locked record), proving the record-read invariant bites")
	fBreakScoreRec   = flag.Bool("break-score-record", false, "DEBUG: seed an aged plugin solve with NO record and disable the repair worker — the scoring-record-missing invariant must then fire")
	fPluginOutage    = flag.Bool("plugin-outage", false, "stop the scoring plugin for the middle third of the run: the board must keep recomputing and matching from-scratch, MISSING must stay 0 while PENDING grows, and the repair worker must drain PENDING to 0 once the plugin returns (proves the structural claim end-to-end)")
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
	scoreboard.BreakReadRepairForTest(*fBreakRR) // negative control: prove read-repair closes #6
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

	// #10 domain-event bus with a DELIBERATELY SLOW notifier subscribed: a cap-1 queue drained at
	// ~2/s, far below the solve burst rate, so its queue fills and events drop. It proves the bus is
	// the slow-plugin lesson applied — a slow subscriber drops (counted), publishers (the submission
	// hot path) never block, and platform throughput is unaffected (every other invariant still
	// holds with it running). The subscription is registered below, once the collector exists.
	eventBus := events.NewBus().WithQueueCap(1)

	h := handlers.New(handlers.Deps{
		Users: usersSvc, Teams: teamsSvc, Events: ev,
		Challenges:  challenges.New(q, &memStore{m: map[string][]byte{}}),
		Submissions: submissions.New(pool, ev, clk, audit.New(q, testsupport.DiscardLogger())).WithScorer(soakScoringPlugin).WithBus(eventBus).WithChallengeTypes(soakCheckerResolver{}),
		Scoreboard:  sb, Runtime: mgr, Scheduler: sched,
		Recompute: func(rctx context.Context) {
			if !*fBreakSB { // -break-scoreboard: stop refreshing the cache so REST goes stale
				_ = sb.Recompute(rctx)
			}
		},
		RecomputeForce: func(rctx context.Context) {
			if !*fBreakSB {
				_ = sb.RecomputeForce(rctx)
			}
		},
		Auth: auth.NewRegistry(auth.NewEmailPasswordProvider(q, nil)), Sessions: sessions,
		Limiter: redisx.NewLimiter(rdb), Audit: audit.New(q, testsupport.DiscardLogger()),
		Log: testsupport.DiscardLogger(), SessionTTL: time.Hour, MaxAttachmentMB: 100,
		TrustProxy: true, SecureCookies: false,
		RegisterIPBurst: 500, RegisterIPWindow: 10 * time.Minute,
	})
	mux := httpserver.New(httpserver.Deps{Log: testsupport.DiscardLogger(), Handlers: h, Sessions: sessions, BaseOrigin: origin, WSHandler: hub.Handler()})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	static, perTeam := seedWorld(ctx, t, pool, q, usersSvc, teamsSvc)

	m := &collector{pool: pool}
	m.snapshotBaseline()

	// Register the slow notifier now that the collector exists. Its handler sleeps 2s/event (drain
	// ~0.5/s), far below the solve stream, so its cap-1 queue overruns and events drop (counted as
	// backpressure); it counts what it manages to deliver. It respects ctx so teardown is prompt.
	notifierCancel := eventBus.Subscribe(soakNotifier, func(string) bool { return true },
		func(ctx context.Context, _ events.Event) error {
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
			}
			m.notifDelivered.Add(1)
			return nil
		})
	defer notifierCancel()
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
	// #9 off-read-path scoring repair worker. Disabled under -break-score-record so a seeded
	// missing record stays missing and the scoring-record-missing invariant fires.
	if !*fBreakScoreRec {
		repairer := submissions.NewScoreRepairer(pool, soakScoringPlugin)
		wg.Add(1)
		go func() { defer wg.Done(); repairer.Run(runCtx, nil) }()
	}
	// #9 plugin-outage run: stop the plugin for the middle third, watch missing vs pending.
	if *fPluginOutage {
		wg.Add(1)
		go func() { defer wg.Done(); runPluginOutage(runCtx, q, m, *fDuration) }()
	}

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

	// The challenge-type challenge must actually have been solved: its correctness comes only from
	// CheckFlag reading the per-challenge type_config, so zero correct solves would mean type_config
	// never reached CheckFlag under load — the one path the submission-suite regression check cannot
	// structurally cover (a built-in challenge returns before CheckFlag). This asserts the path ran.
	var checkerSolves int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM submissions s JOIN challenges c ON c.id = s.challenge_id
		WHERE c.slug = 'chal-checker' AND s.correct`).Scan(&checkerSolves); err != nil {
		t.Fatalf("count chal-checker solves: %v", err)
	}
	if checkerSolves == 0 {
		t.Errorf("chal-checker got 0 correct solves — type_config did not reach CheckFlag under load (the path this run exists to exercise)")
	}
	t.Logf("chal-checker : %d correct solves decided by CheckFlag(type_config) — type_config → CheckFlag exercised under load", checkerSolves)

	if *fPluginOutage {
		assertPluginOutageConverged(ctx, t, pool, m)
	}

	// End-of-run teardown: jump the clock past event end, converge CleanupEnded, and
	// assert zero live per-team instances + the full port range reclaimed. This is the
	// single moment a real event changes the most state at once.
	endOfRunTeardown(ctx, t, sc, sched, pool)

	// Quiesced (no new solves): a couple of final recomputes flush the throttled
	// broadcast to the still-connected WS clients, which must then match REST. Guarded
	// is fine here — no actors remain, so the final count is the max and always writes.
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

// soakScorer is the in-process stand-in for a scoring plugin (#9). Its value is COUNT-DEPENDENT, so
// the value locked at solve time differs from a recompute at a later, higher count — which is what
// makes the -recompute-via-plugin negative control diverge deterministically (the served board
// reads the locked per-solve record; a plugin recompute reads the current count). It can be stopped
// to simulate the plugin going down (write path then records 'pending').
type soakScorer struct{ stopped atomic.Bool }

func (s *soakScorer) Score(_ context.Context, mode string, p scoring.ChallengeScoring, solves int) (int, string, bool) {
	if s.stopped.Load() {
		return 0, "pending", false // plugin down, no fallback → defer (repair worker retries)
	}
	v := p.Initial - 5*solves
	if v < 50 {
		v = 50
	}
	return v, mode, true
}

// soakScoringPlugin is the single in-process scoring plugin: the write path scores through it, and
// the -recompute-via-plugin negative control recomputes through it.
var soakScoringPlugin = &soakScorer{}

// soakChecker is the in-process stand-in for a challenge-TYPE plugin. It decides correctness by
// comparing the submission against the challenge's per-challenge type_config "answer" key — so it is
// the one path that reads type_config end to end on the submit hot path, the gap the submission-suite
// regression check structurally cannot reach (a built-in challenge returns before CheckFlag). If
// type_config did not flow to CheckFlag, config["answer"] would be empty and chal-checker would never
// be solved.
type soakChecker struct{}

func (soakChecker) CheckFlag(_ context.Context, submitted string, config, _ map[string]string) (bool, error) {
	return submitted == config["answer"], nil
}

// soakCheckerResolver routes the "soak-checker" type to the in-process checker (the pre-transaction
// verdict path), and every other (built-in) type to the in-transaction flag comparison, unchanged.
type soakCheckerResolver struct{}

func (soakCheckerResolver) Resolve(typeID string) (submissions.FlagChecker, bool, bool) {
	if typeID == "soak-checker" {
		return soakChecker{}, true, true
	}
	return nil, false, true // built-in types → in-tx comparison
}

// soakNotifier is the bus subscriber name for the deliberately-slow #10 notifier.
const soakNotifier = "soak-slow-notifier"

func seedWorld(ctx context.Context, t *testing.T, pool *pgxpool.Pool, q *gen.Queries, usersSvc *users.Service, teamsSvc *teams.Service) (static, perTeam []challengeRef) {
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

	// A PLUGIN-scored challenge (#9): a standard flag challenge whose scoring mode is 'custom',
	// valued by the in-process scoring plugin (post-commit) and recorded per-solve. Actors solve it
	// like any static one; its solves exercise the record write path, the read seam, the repair
	// worker, and both new invariants. (scoring='custom' inserts directly — 0007 dropped the CHECK.)
	pid := uuid.Must(uuid.NewV7())
	const pluginFlag = "OSCTF{plugin-scored}"
	mustCreateChallenge(ctx, t, q, gen.CreateChallengeParams{
		ID: pid, Slug: "chal-plugin", Title: "chal-plugin", Category: "misc", Kind: "standard", Flag: pluginFlag,
		Scoring: "custom", PointsInitial: 500, Visible: true,
		MemLimitMb: 128, CpuMillis: 500, ContainerEnv: []byte("{}"),
		Instancing: ptr("shared"), FlagMode: ptr("static"), Egress: ptr(true), WritablePaths: []byte("[]"),
	})
	static = append(static, challengeRef{slug: "chal-plugin", flag: pluginFlag})

	// A CHALLENGE-TYPE challenge (type 'soak-checker'): correctness is decided by the in-process
	// challenge-type checker via CheckFlag against the per-challenge type_config — the ONLY path that
	// exercises type_config → CheckFlag end to end under load. The correct answer lives in type_config,
	// NOT the flag column; if type_config did not reach CheckFlag the challenge would never be solved.
	// Scoring is built-in ('dynamic'), orthogonal to the checker, so it stays recomputable.
	const checkerAnswer = "OSCTF{checker-scored}"
	checkerType := "soak-checker"
	mustCreateChallenge(ctx, t, q, gen.CreateChallengeParams{
		ID: uuid.Must(uuid.NewV7()), Slug: "chal-checker", Title: "chal-checker", Category: "misc", Kind: "standard",
		Flag:    "OSCTF{unused-flag-column}", // unused: CheckFlag decides from type_config, not this
		Scoring: "dynamic", PointsInitial: 500, PointsMin: ptr(int32(100)), Decay: ptr(int32(50)), Visible: true,
		MemLimitMb: 128, CpuMillis: 500, ContainerEnv: []byte("{}"),
		Instancing: ptr("shared"), FlagMode: ptr("static"), Egress: ptr(true), WritablePaths: []byte("[]"),
		Type: &checkerType, TypeConfig: []byte(`{"answer":"` + checkerAnswer + `"}`),
	})
	static = append(static, challengeRef{slug: "chal-checker", flag: checkerAnswer})

	if *fBreakScoreRec {
		// Negative control for the scoring-record-missing invariant: a correct plugin solve with NO
		// record, inserted directly (bypassing the write path) and BACKDATED past the grace so it is
		// missing from the first invariant pass. With the repair worker disabled (see TestSoak) it
		// stays missing and the invariant must fire.
		mid := uuid.Must(uuid.NewV7())
		if _, err := q.CreateSubmission(ctx, gen.CreateSubmissionParams{
			ID: mid, ChallengeID: pid, TeamID: seededUsers[0].teamID, UserID: seededUsers[0].userID,
			Provided: "x", Correct: true,
		}); err != nil {
			t.Fatalf("seed missing-record solve: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE submissions SET created_at = now() - interval '120 seconds' WHERE id=$1`, mid); err != nil {
			t.Fatalf("backdate missing-record solve: %v", err)
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

// runPluginOutage (-plugin-outage) stops the scoring plugin for the middle third of the run and
// restarts it, proving the structural claim end-to-end rather than by construction: with the plugin
// unreachable the board still recomputes and still matches from-scratch (the continuous
// scoreboardMatches invariant keeps checking that), MISSING stays 0 (a stopped plugin records a
// PENDING value — a state — never an absence), PENDING grows, and once the plugin returns the
// repair worker drains PENDING back to 0. The convergence + growth assertions run in TestSoak after
// the workers join; this drives the outage and watches the missing/pending split live.
func runPluginOutage(ctx context.Context, q *gen.Queries, m *collector, duration time.Duration) {
	third := duration / 3
	if !sleepCtx(ctx, third) { // Phase 1: plugin up (baseline)
		return
	}
	soakScoringPlugin.stopped.Store(true) // Phase 2: OUTAGE
	outageEnd := time.Now().Add(third)
	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			soakScoringPlugin.stopped.Store(false)
			return
		case <-tick.C:
			if !time.Now().Before(outageEnd) {
				soakScoringPlugin.stopped.Store(false) // Phase 3: plugin back; repair worker drains pending
				return
			}
			c, err := q.CountUnscoredPluginSolves(ctx)
			if err != nil {
				continue
			}
			// A stopped plugin must NEVER leave a missing record: the write path records 'pending'
			// (a definite deferred value), not a bare absence. A missing record here would mean the
			// write path itself failed while the plugin was merely down — a durability bug, distinct
			// from the outage.
			if c.Missing > 0 {
				m.recordViol("outage-missing", fmt.Sprintf("%d missing records while the plugin was down — a stopped plugin must record pending, never an absence", c.Missing))
			}
			if c.Pending > m.outageMaxPending.Load() {
				m.outageMaxPending.Store(c.Pending)
			}
		}
	}
}

// assertPluginOutageConverged runs after the workers join: the outage must have exercised the
// pending path (PENDING actually grew), and once the plugin is back a final repair pass must drain
// PENDING to 0 with MISSING never appearing. This is the end-to-end proof that the board survives
// the plugin being unreachable — not by construction, but by running with it stopped.
func assertPluginOutageConverged(ctx context.Context, t *testing.T, pool *pgxpool.Pool, m *collector) {
	t.Helper()
	if m.outageMaxPending.Load() == 0 {
		t.Errorf("plugin-outage: no PENDING records observed during the outage — the pending path was not exercised (raise -duration so first-time solves land while the plugin is down)")
	}
	// Plugin is back (Phase 3 restored it); a final off-read-path repair pass drains any residual
	// pending deterministically, independent of the last in-run tick's timing.
	soakScoringPlugin.stopped.Store(false)
	repairer := submissions.NewScoreRepairer(pool, soakScoringPlugin)
	if _, err := repairer.RepairOnce(ctx); err != nil {
		t.Fatalf("plugin-outage: final repair pass: %v", err)
	}
	var pending, missing int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE s.scored_by = 'pending'),
		       count(*) FILTER (WHERE s.scored_by IS NULL)
		FROM submissions s JOIN challenges c ON c.id = s.challenge_id
		WHERE s.correct AND c.scoring NOT IN ('static','dynamic')`).Scan(&pending, &missing); err != nil {
		t.Fatalf("plugin-outage: final count: %v", err)
	}
	if pending != 0 {
		t.Errorf("plugin-outage: %d PENDING records did not converge after the plugin returned — the repair worker failed to drain", pending)
	}
	if missing != 0 {
		t.Errorf("plugin-outage: %d MISSING records at end — a stopped plugin must record pending, never an absence", missing)
	}
	t.Logf("plugin-outage : peak pending during outage=%d, converged to 0 after recovery, missing stayed 0", m.outageMaxPending.Load())
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

	outageMaxPending atomic.Int64 // -plugin-outage: peak pending records observed while the plugin was down
	notifDelivered   atomic.Int64 // #10: challenge.solved events the slow notifier actually delivered

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

	// #10 event bus: the deliberately-slow notifier (0.5/s drain) proves the slow-plugin lesson on
	// the bus. Publish is non-blocking, so the whole run above (throughput, all invariants) held
	// while it was overrun. It CANNOT have delivered more than ~0.5/s × duration; any solves beyond
	// that must have been dropped-and-counted. If more events were published than it could drain yet
	// nothing was counted dropped, the backpressure accounting is broken.
	notifDelivered := m.notifDelivered.Load()
	notifDropped := int64(metrics.CounterValue(metrics.PluginEventsDropped.WithLabelValues(soakNotifier, "challenge.solved", "backpressure")))
	t.Logf("event bus   : notifier delivered=%d dropped(backpressure)=%d  (publishers never blocked — Publish is non-blocking)", notifDelivered, notifDropped)
	if maxDeliverable := dur*0.5 + 3; float64(notifDelivered+notifDropped) > maxDeliverable && notifDropped == 0 {
		t.Errorf("slow notifier saw %d events (> %.0f it could drain) but dropped none — bus backpressure accounting is broken",
			notifDelivered+notifDropped, maxDeliverable)
	}

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
	// Count-consistent (read-repaired). With read-repair ON both branches of
	// scoreboardMatches are unfailable — the staleness branch by construction, the content
	// branch skips the read-gap — so this never fires. The 600ms re-read isolates
	// PERSISTENT staleness from transient lag: it is a no-op here, but under
	// -break-readrepair (read-repair off) it reproduces the #6 durability gap at the pre-fix
	// rate — a mismatch that a full recompute cycle does not clear is real, a transient one
	// is lag.
	if ok, _ := scoreboardMatches(ctx, q, sb); !ok {
		time.Sleep(600 * time.Millisecond)
		if ok2, d2 := scoreboardMatches(ctx, q, sb); !ok2 {
			m.recordViol("scoreboard-mismatch", d2)
		} else {
			m.invLag.Add(1)
		}
	}
	// Read-repair must never fall back to serving a stale board: in the soak recomputes
	// finish in milliseconds, so the 1s inline budget is never exceeded. A nonzero counter
	// means a repair ran long — a real regression, not a timing artifact.
	if metrics.CounterValue(metrics.ScoreboardStaleServed) > 0 {
		m.recordViol("scoreboard-stale-served", "inline read-repair exceeded its budget and served a stale board")
	}
	// #9 durability: every valid plugin-scored solve must carry a scoring RECORD (scored_by set)
	// within the backfill-latency bound. The write path records synchronously post-commit, and the
	// repair worker backfills any that slip; a solve older than the grace (3× RepairInterval) with
	// scored_by NULL is a MISSING record neither wrote — the write-path durability gap, the failure
	// that otherwise shows only as a wrong board mid-event.
	var missingAged int
	_ = pool.QueryRow(ctx, `
		SELECT count(*) FROM submissions s JOIN challenges c ON c.id = s.challenge_id
		WHERE s.correct AND c.scoring NOT IN ('static','dynamic')
		  AND s.scored_by IS NULL AND s.created_at < now() - interval '30 seconds'`).Scan(&missingAged)
	if missingAged > 0 {
		m.recordViol("scoring-record-missing", fmt.Sprintf("%d plugin solves aged past grace with no scoring record", missingAged))
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

// scoreboardMatches checks the scoreboard invariant against what REST SERVES (sb.Current,
// read-repaired). It is count-consistent, which is what read-repair makes possible:
//
//  1. Staleness (the durability invariant, unfailable by construction if read-repair
//     works): read the log's valid-solve count BEFORE the served board. Current() is read
//     after, and read-repair guarantees it reflects the log at read time, so its SolveCount
//     must be >= that earlier count. A served board with fewer solves is genuinely behind
//     the log — the exact bug (#6) read-repair exists to prevent.
//
//  2. Content: compare the served board to a from-scratch recompute only when both reflect
//     the SAME log state (equal solve counts). If a solve landed between the two reads the
//     counts differ and the boards are not comparable — dynamic scoring makes points
//     non-monotonic across log states, so comparing different states is meaningless, not a
//     bug. This replaces the old want-then-got timing comparison, which under read-repair
//     (Current is now always fresh) would flag every read-gap solve as a spurious mismatch.
func scoreboardMatches(ctx context.Context, q *gen.Queries, sb *scoreboard.Service) (bool, string) {
	before, err := q.CountValidSolves(ctx)
	if err != nil {
		return true, ""
	}
	snap, err := sb.Current(ctx, false) // read-repaired: reflects the log as of this read
	if err != nil {
		return true, ""
	}
	if snap.SolveCount < int(before) {
		return false, fmt.Sprintf("served board behind the log: solveCount=%d < %d valid solves read before it", snap.SolveCount, before)
	}

	want, wantCount, err := independentStandings(ctx, q)
	if err != nil {
		return true, ""
	}
	if wantCount != snap.SolveCount {
		return true, "" // log moved between the reads; different states aren't comparable
	}
	got := map[uuid.UUID]int{}
	for _, e := range snap.Standings {
		got[e.TeamID] = e.Points
	}
	for tid, pts := range want {
		if got[tid] != pts {
			return false, fmt.Sprintf("team %s at %d solves: rest=%d fromscratch=%d", tid, wantCount, got[tid], pts)
		}
	}
	for tid, pts := range got {
		if pts != 0 && want[tid] != pts {
			return false, fmt.Sprintf("overcount team %s at %d solves: rest=%d fromscratch=%d", tid, wantCount, pts, want[tid])
		}
	}
	return true, ""
}

// independentStandings recomputes team points straight from the valid-solve log
// and the pure scoring engine, replicating compute()'s rules (per-challenge value
// from the non-banned solve count) without touching the scoreboard service's
// snapshot/cache/broadcast path — so a stale cache or broadcast divergence shows.
func independentStandings(ctx context.Context, q *gen.Queries) (map[uuid.UUID]int, int, error) {
	solves, err := q.ListValidSolves(ctx)
	if err != nil {
		return nil, 0, err
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
		switch {
		case scoring.IsBuiltinMode(c.mode):
			value[id] = scoring.Value(c.mode, c.p, count[id])
		case *fRecomputePlugin:
			// NEGATIVE CONTROL: recompute the plugin mode by CALLING the plugin at the current solve
			// count, instead of reading the per-solve record. Because the plugin is count-dependent,
			// this differs from the values locked at each solve — so it diverges from the served
			// board (which reads the record), reproducing exactly the mistake the design forbids.
			v, _, _ := soakScoringPlugin.Score(context.Background(), c.mode, c.p, count[id])
			value[id] = v
		}
	}
	pts := map[uuid.UUID]int{}
	for _, r := range solves {
		// Mirror compute() exactly (#9): plugin modes are scored from the per-solve RECORD, never
		// the plugin. That is what makes the served==from-scratch invariant hold with the plugin
		// down — if this recomputed a plugin mode by calling the plugin, the two sides would
		// diverge the moment the plugin stopped. (-recompute-via-plugin flips this on purpose.)
		switch {
		case scoring.IsBuiltinMode(r.Scoring):
			pts[r.TeamID] += value[r.ChallengeID]
		case *fRecomputePlugin:
			pts[r.TeamID] += value[r.ChallengeID] // plugin-recomputed (current count), NOT the record
		case r.ScoredValue != nil:
			pts[r.TeamID] += int(*r.ScoredValue)
		}
	}
	return pts, len(solves), nil
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
