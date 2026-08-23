package scoreboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/swayyaam/OSCTF/internal/clock"
	"github.com/swayyaam/OSCTF/internal/db/gen"
	"github.com/swayyaam/OSCTF/internal/events"
	"github.com/swayyaam/OSCTF/internal/metrics"
)

const (
	keyCurrent = "scoreboard:current"
	keyFrozen  = "scoreboard:frozen"
)

// Broadcaster receives each freshly computed snapshot (the WS hub in M6). Nil is fine.
type Broadcaster func(Snapshot)

// Service computes, caches, and serves standings, and manages the freeze snapshot.
type Service struct {
	// q is the read surface compute needs (scoreStore). *gen.Queries satisfies it in
	// production; a test can inject a store that blocks or counts concurrency to pin
	// that compute() runs OUTSIDE the mutex.
	q      scoreStore
	rdb    *redis.Client
	events *events.Service
	clock  clock.Clock

	mu        sync.Mutex
	lastCount int // valid-solve-row count of the newest board written to keyCurrent (monotonic write guard)
	broadcast Broadcaster
}

// New builds the scoreboard service.
func New(q *gen.Queries, rdb *redis.Client, ev *events.Service, c clock.Clock) *Service {
	return &Service{q: q, rdb: rdb, events: ev, clock: c}
}

// SetBroadcaster wires the WS hub (M6). Called once at startup after construction.
func (s *Service) SetBroadcaster(b Broadcaster) { s.broadcast = b }

// Recompute rebuilds standings from the DB and writes the live cache for the SOLVE
// path (a correct submission). compute() runs OUTSIDE the mutex — it is the slow part
// (two DB reads), and holding s.mu across it serialized every recompute behind one
// in-flight compute, so under load (100+ concurrent solves) recomputes queued on the
// mutex faster than they drained and the served board lagged the log by the queue-drain
// time, a window the soak caught crossing its 600ms re-read.
//
// With compute() outside the lock, recomputes can finish out of order, so the write is
// guarded on the valid-solve-row COUNT (a data-derived version, not wall time): the
// solve log is append-only, so a board built from more rows is strictly newer, and a
// slow older recompute that finishes late is dropped rather than clobbering it. Keying
// the guard on a timestamp or entry-sequence would NOT be safe — the gap between
// reading the clock and Postgres taking the ListValidSolves snapshot lets a later-
// stamped recompute read fewer rows, so time does not order the data.
func (s *Service) Recompute(ctx context.Context) error { return s.recompute(ctx, false, true) }

// RecomputeForce is ONLY for admin actions that legitimately SHRINK the log — hiding or
// deleting a challenge drops its solves, a lower count the count guard would otherwise
// reject as stale. It runs in two steps: an unconditional write that lands the shrink and
// resets the guard baseline, then an immediately following GUARDED recompute. The force
// bypasses the guard, so it can clobber a solve that landed during its compute; the
// guarded re-run reads the current count and picks that solve straight back up, bounding
// the miss to one compute rather than "until the next solve" — production has no periodic
// recompute (the 15s ticker recomputes only on a phase change), so the next solve is not
// guaranteed to follow. Everything else (the solve path, the ticker, startup) stays
// guarded: a guarded recompute never clobbers a newer board.
//
// The force write suppresses its broadcast so WS clients don't briefly see a concurrent
// solve blink out; the guarded re-run broadcasts the settled board.
func (s *Service) RecomputeForce(ctx context.Context) error {
	if err := s.recompute(ctx, true, false); err != nil {
		return err
	}
	return s.recompute(ctx, false, true)
}

func (s *Service) recompute(ctx context.Context, force, broadcast bool) error {
	snap, count, err := compute(ctx, s.q, s.clock())
	if err != nil {
		return err
	}
	snap.Frozen = s.frozen(ctx)

	if hook := afterComputeHook; hook != nil {
		hook() // test seam: sequence concurrent recomputes between the read and the write
	}

	s.mu.Lock()
	if !force && count < s.lastCount {
		s.mu.Unlock() // an already-published board reflects more solves; don't regress it
		return nil
	}
	werr := s.write(ctx, keyCurrent, snap)
	if werr == nil {
		s.lastCount = count
	}
	s.mu.Unlock()
	if werr != nil {
		return werr
	}

	if broadcast && s.broadcast != nil {
		// served(repair=false): we just computed keyCurrent, and read-repair here would
		// recurse into another recompute under continuous solves.
		pub, perr := s.served(ctx, false, false)
		if perr != nil {
			return perr
		}
		s.broadcast(pub)
	}
	return nil
}

// afterComputeHook, when set by a test, runs in recompute() after compute() has read
// the DB and before the guarded write — the seam that makes the out-of-order
// concurrent-recompute race deterministic. It is nil in production.
var afterComputeHook func()

// breakReadRepair, when a test flips it via BreakReadRepairForTest, disables read-repair
// so Current() serves the possibly-stale cache. It exists only to PROVE read-repair is
// what closes #6: with it set, the soak's -break-readrepair reappears the intermittent
// mismatch at the pre-fix rate — the same negative-control discipline as -break-scoreboard.
// Never set in production.
var breakReadRepair bool

// BreakReadRepairForTest toggles the read-repair kill switch. Test-only.
func BreakReadRepairForTest(v bool) { breakReadRepair = v }

// staleRepairBudget bounds an inline read-repair recompute. Past it, the read serves the
// stale board and counts it (ScoreboardStaleServed) rather than hanging the request.
const staleRepairBudget = time.Second

// Current returns the standings snapshot for a participant read, read-repaired: if the
// served board is behind the solve log it is recomputed before it is returned, so a read
// can never serve a board older than the log (the read side of the data-derived guard).
// Non-admins during a freeze get the frozen snapshot; admins always get live data (with
// frozen=true so their UI can still show the banner).
func (s *Service) Current(ctx context.Context, isAdmin bool) (Snapshot, error) {
	return s.served(ctx, isAdmin, true)
}

// served is Current with read-repair optional: the WS broadcast passes repair=false
// because it just recomputed and repairing there would recurse under continuous solves.
func (s *Service) served(ctx context.Context, isAdmin, repair bool) (Snapshot, error) {
	frozen := s.frozen(ctx)

	if frozen && !isAdmin {
		snap, ok, err := s.read(ctx, keyFrozen)
		if err != nil {
			return Snapshot{}, err
		}
		if !ok {
			// Lazily capture the frozen snapshot on the first frozen read so we
			// never serve live data to non-admins during a freeze (the ticker is a
			// backstop, not the only writer).
			if err := s.MaybeSnapshotFreeze(ctx); err != nil {
				return Snapshot{}, err
			}
			snap, ok, err = s.read(ctx, keyFrozen)
			if err != nil {
				return Snapshot{}, err
			}
		}
		if ok {
			snap.Frozen = true
			return snap, nil
		}
	}

	snap, ok, err := s.read(ctx, keyCurrent)
	if err != nil {
		// Redis cache read failed (Redis down). A LIVE board is authoritative in Postgres, so DEGRADE:
		// recompute directly and serve — a slightly slower board beats a dark one mid-event. But a
		// non-admin during a FREEZE must NOT degrade: a frozen snapshot lives only in Redis, so with
		// Redis down there is no authority for it and this fails closed. The two paths deliberately
		// behave DIFFERENTLY under the same outage (pinned by the Redis-outage test); do not unify them.
		if frozen && !isAdmin {
			return Snapshot{}, err
		}
		return s.degradeToRecompute(ctx, frozen)
	}
	switch {
	case !ok:
		// Cache miss: compute fresh (already the newest) and store. keyCurrent never
		// expires, so this only runs before the first Recompute.
		snap, _, err = compute(ctx, s.q, s.clock())
		if err != nil {
			return Snapshot{}, err
		}
		if werr := s.write(ctx, keyCurrent, snap); werr != nil {
			return Snapshot{}, werr
		}
	case repair && !breakReadRepair:
		if fresh, repaired, rerr := s.readRepair(ctx, snap); rerr != nil {
			return Snapshot{}, rerr
		} else if repaired {
			snap = fresh
		}
	}
	snap.Frozen = frozen
	return snap, nil
}

// degradeToRecompute serves a scoreboard read directly from Postgres when the Redis cache is
// unavailable — the live-read fallback that keeps the board from going dark during a Redis outage.
// It is BOUNDED by staleRepairBudget (the same bound as inline read-repair) so a cache outage cannot
// turn every read into an unbounded DB load — that is how a partial outage becomes a total one — and
// COUNTED. It does not write the cache (Redis is down). If even the bounded recompute cannot finish,
// there is genuinely nothing to serve and it fails closed. The FREEZE path never calls this (see
// served): a frozen snapshot has no Postgres authority to fall back to.
func (s *Service) degradeToRecompute(ctx context.Context, frozen bool) (Snapshot, error) {
	rctx, cancel := context.WithTimeout(ctx, staleRepairBudget)
	defer cancel()
	snap, _, err := compute(rctx, s.q, s.clock())
	if err != nil {
		return Snapshot{}, err // bounded recompute failed → fail closed (the bound stops a DB cascade)
	}
	metrics.ScoreboardDegradedServed.Inc()
	snap.Frozen = frozen
	return snap, nil
}

// readRepair returns a fresher snapshot when the cached board is behind the solve log.
// The served board records the valid-solve count it was computed from (snap.SolveCount);
// if CountValidSolves has moved past it the board must not be served stale, so it is
// recomputed inline, bounded by staleRepairBudget. On timeout the stale board is served
// and counted — a bounded read latency in exchange for never an unbounded one. The result
// is served regardless of whether the cache write below succeeds, so THIS read is always
// fresh; the guarded write just spares the next reader the same repair.
func (s *Service) readRepair(ctx context.Context, snap Snapshot) (Snapshot, bool, error) {
	live, err := s.q.CountValidSolves(ctx)
	if err != nil {
		return snap, false, nil // count failed — serve what we have; the next read/tick retries
	}
	if int(live) <= snap.SolveCount {
		return snap, false, nil // not behind the log
	}
	metrics.ScoreboardStaleReads.Inc()

	rctx, cancel := context.WithTimeout(ctx, staleRepairBudget)
	defer cancel()
	fresh, count, cerr := compute(rctx, s.q, s.clock())
	if cerr != nil {
		metrics.ScoreboardStaleServed.Inc()
		return snap, false, nil // bounded fallback: serve stale (counted)
	}

	// Publish under the write guard so a concurrent newer write is not clobbered.
	s.mu.Lock()
	if count >= s.lastCount {
		if werr := s.write(ctx, keyCurrent, fresh); werr == nil {
			s.lastCount = count
		}
	}
	s.mu.Unlock()
	return fresh, true, nil
}

// ReconcileIfBehind recomputes (and broadcasts) only when the cached board is behind the
// solve log. It is the SLOW backstop the periodic ticker calls — read-repair is the
// correctness mechanism for served reads, but nothing else covers WS clients (they get
// broadcasts, not reads) when a per-solve recompute was missed and no HTTP read triggered
// a repair. The version check keeps it a no-op — no recompute, no redundant broadcast —
// while the board is up to date.
func (s *Service) ReconcileIfBehind(ctx context.Context) error {
	snap, ok, err := s.read(ctx, keyCurrent)
	if err != nil {
		return err
	}
	if !ok {
		return s.Recompute(ctx) // no cache yet — establish it
	}
	live, err := s.q.CountValidSolves(ctx)
	if err != nil {
		return err
	}
	if int(live) > snap.SolveCount {
		return s.Recompute(ctx) // behind the log — refresh the cache and broadcast
	}
	return nil
}

// MaybeSnapshotFreeze writes the frozen snapshot once, when the freeze point has
// passed and no frozen snapshot exists yet. Called by the freeze ticker (M6).
func (s *Service) MaybeSnapshotFreeze(ctx context.Context) error {
	if !s.frozen(ctx) {
		return nil
	}
	exists, err := s.rdb.Exists(ctx, keyFrozen).Result()
	if err != nil {
		return fmt.Errorf("scoreboard: checking frozen key: %w", err)
	}
	if exists > 0 {
		return nil
	}
	if hook := afterFrozenExistsCheckHook; hook != nil {
		hook() // test seam: drives the concurrent first-snapshot race
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check under the lock: both the public read path and the background loop call
	// this, so another goroutine may have snapshotted between the existence check
	// above and the lock. Without this the later writer recomputes and overwrites the
	// frozen board with a slightly later snapshot, leaking solves that landed after
	// the freeze into the "frozen" view.
	exists, err = s.rdb.Exists(ctx, keyFrozen).Result()
	if err != nil {
		return fmt.Errorf("scoreboard: re-checking frozen key: %w", err)
	}
	if exists > 0 {
		return nil
	}
	snap, _, err := compute(ctx, s.q, s.clock())
	if err != nil {
		return err
	}
	snap.Frozen = true
	// SETNX, not SET: the first writer wins even across processes — the mutex only
	// serializes this replica — so the frozen board is captured exactly once at
	// freeze onset and never overwritten by a later racing snapshot.
	return s.writeNX(ctx, keyFrozen, snap)
}

// afterFrozenExistsCheckHook, when set by a test, runs after the frozen-key
// existence check and before the snapshot write. It exists to make the
// concurrent-first-snapshot race deterministic; it is nil in production.
var afterFrozenExistsCheckHook func()

// ClearFreeze removes the frozen snapshot (admin cleared freeze_at → board unfreezes).
func (s *Service) ClearFreeze(ctx context.Context) error {
	if err := s.rdb.Del(ctx, keyFrozen).Err(); err != nil {
		return fmt.Errorf("scoreboard: clearing frozen snapshot: %w", err)
	}
	return nil
}

func (s *Service) frozen(ctx context.Context) bool {
	e, err := s.events.Get(ctx)
	if err != nil {
		return false
	}
	return s.events.Frozen(e)
}

func (s *Service) write(ctx context.Context, key string, snap Snapshot) error {
	b, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("scoreboard: marshaling snapshot: %w", err)
	}
	if err := s.rdb.Set(ctx, key, b, 0).Err(); err != nil {
		return fmt.Errorf("scoreboard: writing %s: %w", key, err)
	}
	return nil
}

// writeNX writes a snapshot only if the key is absent (SET NX), so a once-only value
// like the frozen board cannot be overwritten by a concurrent write — the atomicity
// is Redis-side, so it holds across processes, not just this replica's mutex.
func (s *Service) writeNX(ctx context.Context, key string, snap Snapshot) error {
	b, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("scoreboard: marshaling snapshot: %w", err)
	}
	if err := s.rdb.SetNX(ctx, key, b, 0).Err(); err != nil {
		return fmt.Errorf("scoreboard: writing %s: %w", key, err)
	}
	return nil
}

func (s *Service) read(ctx context.Context, key string) (Snapshot, bool, error) {
	b, err := s.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("scoreboard: reading %s: %w", key, err)
	}
	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return Snapshot{}, false, fmt.Errorf("scoreboard: unmarshaling %s: %w", key, err)
	}
	return snap, true, nil
}
