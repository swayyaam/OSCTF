package scoring_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/swayyaam/OSCTF/internal/scoring"
)

// stubEngine is a minimal ScoringEngine for registry tests.
type stubEngine struct {
	name string
	val  int
}

func (s stubEngine) Name() string                            { return s.name }
func (s stubEngine) Value(scoring.ChallengeScoring, int) int { return s.val }

// TestRegistryDeregister: deregistering a plugin mode removes it; deregistering a plugin that
// OVERRODE a built-in restores the built-in; deregistering a bare built-in is a no-op.
func TestRegistryDeregister(t *testing.T) {
	r := scoring.NewRegistry(scoring.StaticEngine{}, scoring.DynamicEngine{})

	// A new plugin mode → removed on deregister; Value then falls back to static.
	if err := r.Register("elo", stubEngine{"elo", 7}, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get("elo"); !ok {
		t.Fatal("elo not registered")
	}
	r.Deregister("elo")
	if _, ok := r.Get("elo"); ok {
		t.Error("elo still present after deregister")
	}

	// A plugin OVERRIDES the static built-in → deregister RESTORES the real static engine.
	if err := r.Register("static", stubEngine{"static", 999}, true); err != nil {
		t.Fatal(err)
	}
	if e, _ := r.Get("static"); e.Value(scoring.ChallengeScoring{Initial: 1}, 0) != 999 {
		t.Error("override did not take effect")
	}
	r.Deregister("static")
	e, ok := r.Get("static")
	if !ok || e.Value(scoring.ChallengeScoring{Initial: 5}, 0) != 5 { // real StaticEngine returns Initial
		t.Errorf("built-in static not restored after deregister (ok=%v)", ok)
	}

	// A bare built-in cannot "stop"; deregistering it is a no-op.
	r.Deregister("dynamic")
	if _, ok := r.Get("dynamic"); !ok {
		t.Error("dynamic built-in wrongly removed by deregister")
	}
}

// TestScoringRegistryResolution: built-ins resolve by name and through Value; an unknown
// mode falls back to static; a plugin engine registers without disturbing the built-ins.
func TestScoringRegistryResolution(t *testing.T) {
	r := scoring.NewRegistry(scoring.StaticEngine{}, scoring.DynamicEngine{})

	if _, ok := r.Get("static"); !ok {
		t.Fatal("Get(static) missed")
	}
	if _, ok := r.Get("dynamic"); !ok {
		t.Fatal("Get(dynamic) missed")
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("Get(nope) resolved, want miss")
	}
	if got := r.Value("static", scoring.ChallengeScoring{Initial: 300}, 99); got != 300 {
		t.Fatalf("Value(static) = %d, want 300", got)
	}
	if got := r.Value("unknown", scoring.ChallengeScoring{Initial: 42}, 5); got != 42 {
		t.Fatalf("Value(unknown) = %d, want 42 (static fallback)", got)
	}

	acme := stubEngine{name: "acme", val: 7}
	if err := r.Register("acme", acme, false); err != nil {
		t.Fatalf("Register(acme): %v", err)
	}
	if got := r.Value("acme", scoring.ChallengeScoring{Initial: 999}, 3); got != 7 {
		t.Fatalf("Value(acme) = %d, want 7 (plugin engine)", got)
	}
	if got := r.Value("static", scoring.ChallengeScoring{Initial: 300}, 0); got != 300 {
		t.Fatalf("built-in static changed after registering a plugin: %d", got)
	}
}

// TestScoringRegistryBuiltinOverrideProtected: static/dynamic can't be replaced without an
// explicit override — a buggy/hostile scoring plugin can't silently hijack core scoring.
func TestScoringRegistryBuiltinOverrideProtected(t *testing.T) {
	r := scoring.NewRegistry(scoring.StaticEngine{}, scoring.DynamicEngine{})

	rogue := stubEngine{name: "static", val: -1}
	if err := r.Register("static", rogue, false); err == nil {
		t.Fatal("Register(static, override=false) succeeded; want it refused (built-in protected)")
	}
	if got := r.Value("static", scoring.ChallengeScoring{Initial: 300}, 0); got != 300 {
		t.Fatalf("built-in static was replaced without override: %d", got)
	}
	if err := r.Register("static", rogue, true); err != nil {
		t.Fatalf("Register(static, override=true): %v", err)
	}
	if got := r.Value("static", scoring.ChallengeScoring{Initial: 300}, 0); got != -1 {
		t.Fatalf("override did not take effect: %d", got)
	}
}

// TestScoringRegistryReaderAtomicContention pins the reader-atomic swap invariant under
// contention: N readers resolve `static` in a tight loop while a writer swaps the map
// repeatedly. `static` is in every version, so every read must return a valid non-nil
// engine — never absent, never torn. Run under -race.
func TestScoringRegistryReaderAtomicContention(t *testing.T) {
	r := scoring.NewRegistry(scoring.StaticEngine{}, scoring.DynamicEngine{})

	var wg sync.WaitGroup
	var reads atomic.Int64
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = r.Register(fmt.Sprintf("p%d", i%8), stubEngine{name: "churn", val: i}, false)
		}
	}()

	const readers = 16
	for g := 0; g < readers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				e, ok := r.Get("static")
				if !ok || e == nil || e.Name() != "static" {
					t.Errorf("Get(static) during a swap returned (%v, ok=%v) — scoring registry swap is not reader-atomic", e, ok)
					return
				}
				reads.Add(1)
			}
		}()
	}

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
	if reads.Load() == 0 {
		t.Fatal("no reads happened — the contention test exercised nothing")
	}
}
