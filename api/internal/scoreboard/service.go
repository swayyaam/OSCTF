package scoreboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"

	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
)

const (
	keyCurrent = "scoreboard:current"
	keyFrozen  = "scoreboard:frozen"
)

// Broadcaster receives each freshly computed snapshot (the WS hub in M6). Nil is fine.
type Broadcaster func(Snapshot)

// Service computes, caches, and serves standings, and manages the freeze snapshot.
type Service struct {
	q      *gen.Queries
	rdb    *redis.Client
	events *events.Service
	clock  clock.Clock

	mu        sync.Mutex
	broadcast Broadcaster
}

// New builds the scoreboard service.
func New(q *gen.Queries, rdb *redis.Client, ev *events.Service, c clock.Clock) *Service {
	return &Service{q: q, rdb: rdb, events: ev, clock: c}
}

// SetBroadcaster wires the WS hub (M6). Called once at startup after construction.
func (s *Service) SetBroadcaster(b Broadcaster) { s.broadcast = b }

// Recompute rebuilds standings from the DB and writes the live cache, then
// broadcasts the public snapshot (frozen data during a freeze so WS consumers,
// who have no per-connection auth, never see live standings). The compute+write
// is serialized by a per-process mutex; the broadcast runs after the lock is
// released (Current may take the lock via the freeze path).
func (s *Service) Recompute(ctx context.Context) error {
	s.mu.Lock()
	snap, err := compute(ctx, s.q, s.clock())
	if err != nil {
		s.mu.Unlock()
		return err
	}
	snap.Frozen = s.frozen(ctx)
	werr := s.write(ctx, keyCurrent, snap)
	s.mu.Unlock()
	if werr != nil {
		return werr
	}

	if s.broadcast != nil {
		pub, perr := s.Current(ctx, false)
		if perr != nil {
			return perr
		}
		s.broadcast(pub)
	}
	return nil
}

// Current returns the standings snapshot for a caller. Non-admins during a freeze
// get the frozen snapshot; admins always get live data (with frozen=true so their
// UI can still show the banner).
func (s *Service) Current(ctx context.Context, isAdmin bool) (Snapshot, error) {
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
		return Snapshot{}, err
	}
	if !ok {
		// Cache miss: compute, store, return.
		snap, err = compute(ctx, s.q, s.clock())
		if err != nil {
			return Snapshot{}, err
		}
		if werr := s.write(ctx, keyCurrent, snap); werr != nil {
			return Snapshot{}, werr
		}
	}
	snap.Frozen = frozen
	return snap, nil
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
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, err := compute(ctx, s.q, s.clock())
	if err != nil {
		return err
	}
	snap.Frozen = true
	return s.write(ctx, keyFrozen, snap)
}

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
