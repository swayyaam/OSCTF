package submissions

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/metrics"
	"github.com/osctf/platform/internal/scoring"
)

// repairBatch bounds how many missing/pending records one pass backfills, so a large backlog is
// worked off over several ticks rather than one unbounded pass holding the DB and the plugin.
const repairBatch = 200

// RepairInterval is the repair worker's tick and the backfill-latency bound: a plugin-scored solve
// whose post-commit record write failed (MISSING) gets a record within one tick, and the
// missing/pending gauges are refreshed each tick. Stated so operators can reason about how long a
// missing record — a participant-visible 0 on the board — can persist before it is filled.
const RepairInterval = 10 * time.Second

// ScoreRepairer backfills MISSING and PENDING plugin-scoring records OFF the scoreboard read path.
// The board never waits on it: a missing record reads as the deterministic default 0 until a tick
// fills the real value. Keeping repair here (not inline on read) is what makes the recompute
// deterministic — compute() and an independent recompute resolve the same missing record
// identically regardless of whether the plugin is reachable at read time.
type ScoreRepairer struct {
	q      *gen.Queries
	scorer Scorer
	batch  int32
}

// NewScoreRepairer builds the repair worker. A nil scorer is valid: RepairOnce then only refreshes
// the missing/pending gauges and backfills nothing (there is no way to value a plugin mode).
func NewScoreRepairer(pool *pgxpool.Pool, scorer Scorer) *ScoreRepairer {
	return &ScoreRepairer{q: gen.New(pool), scorer: scorer, batch: repairBatch}
}

// RepairOnce runs one reconcile pass: refresh the missing/pending gauges, then backfill up to batch
// records. It records a value when the scorer produces one, and 'pending' when the scorer defers —
// so every valid plugin solve ends the pass with a RECORD (scored_by set), never a bare absence.
// Returns the number of records given a concrete value this pass. Bounded by batch and, per record,
// by scoreDeadline.
func (r *ScoreRepairer) RepairOnce(ctx context.Context) (int, error) {
	// Durability gauges first, so they refresh even when the scorer is nil or the backfill errors.
	if c, err := r.q.CountUnscoredPluginSolves(ctx); err == nil {
		metrics.PluginScoresMissing.Set(float64(c.Missing))
		metrics.PluginScoresPending.Set(float64(c.Pending))
	}
	if r.scorer == nil {
		return 0, nil
	}

	rows, err := r.q.ListSolvesNeedingScore(ctx, r.batch)
	if err != nil {
		return 0, fmt.Errorf("submissions: list solves needing score: %w", err)
	}
	solveCounts := map[uuid.UUID]int{} // CountChallengeSolves cached per challenge within the pass
	filled := 0
	for _, row := range rows {
		solves, ok := solveCounts[row.ChallengeID]
		if !ok {
			n, cerr := r.q.CountChallengeSolves(ctx, row.ChallengeID)
			if cerr != nil {
				continue // leave this row for the next pass rather than record a wrong count
			}
			solves = int(n)
			solveCounts[row.ChallengeID] = solves
		}

		sctx, cancel := context.WithTimeout(ctx, scoreDeadline)
		value, by, hasValue := r.scorer.Score(sctx, row.Scoring, rowScoreParams(row), solves)
		cancel()

		var scoredValue *int32
		if hasValue {
			v := int32(value) //nolint:gosec // G115: a scoring point value, bounded by the int32 point columns / RPC field.
			scoredValue = &v
		}
		label := by
		n, rerr := r.q.RecordScore(ctx, gen.RecordScoreParams{ID: row.ID, ScoredBy: &label, ScoredValue: scoredValue})
		if rerr != nil || n == 0 {
			continue // a concurrent write already recorded it, or the write failed → next pass
		}
		if hasValue {
			filled++
			metrics.PluginScoresRepaired.Inc()
		}
	}
	return filled, nil
}

// Run drives RepairOnce on a ticker until ctx is cancelled. Each pass uses a background-derived,
// bounded context so an in-flight pass finishes across a shutdown signal (the loop stops on the
// ctx.Err check), mirroring the other platform workers.
func (r *ScoreRepairer) Run(ctx context.Context, warn func(err error)) {
	ticker := time.NewTicker(RepairInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			pctx, cancel := context.WithTimeout(context.Background(), RepairInterval)
			if _, err := r.RepairOnce(pctx); err != nil && warn != nil {
				warn(err)
			}
			cancel()
		}
	}
}

// rowScoreParams extracts scoring params from a needing-score row (mirrors scoreParams for a
// gen.Challenge).
func rowScoreParams(row gen.ListSolvesNeedingScoreRow) scoring.ChallengeScoring {
	params := scoring.ChallengeScoring{Initial: int(row.PointsInitial)}
	if row.PointsMin != nil {
		params.Min = int(*row.PointsMin)
	}
	if row.Decay != nil {
		params.Decay = int(*row.Decay)
	}
	return params
}
