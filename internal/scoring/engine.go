// Package scoring computes challenge point values. It is pure: numbers in,
// numbers out — no I/O, no clock, no imports beyond stdlib (docs/v0.1/07-scoring.md).
package scoring

import "math"

// ChallengeScoring holds the scoring parameters of a challenge.
type ChallengeScoring struct {
	Initial int // points_initial
	Min     int // points_min  (dynamic only)
	Decay   int // decay       (dynamic only; solve count at which value reaches Min)
}

// ModeStatic and ModeDynamic are the built-in scoring mode ids. Their value is a pure function of
// the solve count, recomputed from the formula on every read. Any other mode is plugin-provided
// and scored locked-at-solve from a recorded value (0007) — never by calling the plugin on the
// read path.
const (
	ModeStatic  = "static"
	ModeDynamic = "dynamic"
)

// IsBuiltinMode reports whether mode is a platform built-in (static/dynamic). It is a CONSTANT
// check — no registry lookup, no plugin access, no I/O — so a scoreboard recompute can classify a
// solve and stay deterministic regardless of plugin or registry state (a plugin deregistered
// mid-read must not change how a past solve is scored). Everything else is a plugin mode, whose
// value the recompute reads from the per-solve record, not from any engine.
func IsBuiltinMode(mode string) bool {
	return mode == ModeStatic || mode == ModeDynamic
}

// ScoringEngine computes a challenge's current point value given its valid solve count.
type ScoringEngine interface {
	Name() string // "static" | "dynamic"
	Value(params ChallengeScoring, solves int) int
}

// StaticEngine always returns the initial value.
type StaticEngine struct{}

// Name implements ScoringEngine.
func (StaticEngine) Name() string { return ModeStatic }

// Value implements ScoringEngine: the value never changes.
func (StaticEngine) Value(params ChallengeScoring, _ int) int { return params.Initial }

// DynamicEngine decays the value with solve count (CTFd-compatible shape):
//
//	Value = max(Min, round( Initial - (Initial-Min) * solves² / Decay² ))
type DynamicEngine struct{}

// Name implements ScoringEngine.
func (DynamicEngine) Name() string { return ModeDynamic }

// Value implements ScoringEngine.
func (DynamicEngine) Value(params ChallengeScoring, solves int) int {
	if solves < 0 {
		solves = 0
	}
	if params.Decay <= 0 {
		return params.Min
	}
	s := float64(solves)
	d := float64(params.Decay)
	val := float64(params.Initial) - float64(params.Initial-params.Min)*(s*s)/(d*d)
	rounded := int(math.Round(val))
	if rounded < params.Min {
		return params.Min
	}
	return rounded
}

// The scoring registry (mutable, override-protected, atomic-swap) and the package-level
// Value / Register / Engines convenience functions live in registry.go. The engines above
// stay pure.
