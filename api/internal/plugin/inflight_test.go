package plugin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/metrics"
	"github.com/osctf/platform/internal/plugin/pluginpb"
)

// budgetConfig is the supervisor config the in-flight tests share: real clock + sleeper (so the
// drain actually waits) and a SHORT drain timeout, so a slow call reliably outlasts the drain
// and cleanup kills promptly instead of idling for the plugin's full 4s.
func budgetConfig() superConfig {
	return superConfig{sleep: realSleep, now: clock.System(), drainTimeout: 200 * time.Millisecond}
}

// scoringValue calls Value on a scoring client; returns the plugin's error (or the transport's,
// when the process is killed mid-call). Fills the budget with the slow double.
func scoringValue(client any) error {
	_, err := client.(pluginpb.ScoringClient).Value(context.Background(),
		&pluginpb.ScoreRequest{Initial: 500, Min: 100, Decay: 50, Solves: 3})
	return err
}

// startReady tracks a plugin under a unique name, launches the real double, waits for ready, and
// registers cleanup that stops it. Returns the supervisor.
func startReady(t *testing.T, l *Loader, name, bin string, cfg superConfig) *supervisor {
	t.Helper()
	l.track(name)
	launch := realLaunch(launchSpec{bin: bin, key: KeyScoring, startTimeout: 15 * time.Second, pollInterval: 50 * time.Millisecond})
	s := newSupervisor(l, name, launch, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-s.done })
	s.start(ctx)
	waitForState(t, s, StateReady, 15*time.Second)
	return s
}

// waitInflight polls the per-plugin in-flight gauge until it equals want (or the deadline).
func waitInflight(t *testing.T, name string, want float64, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if metrics.GaugeValue(metrics.PluginInflight.WithLabelValues(name)) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := metrics.GaugeValue(metrics.PluginInflight.WithLabelValues(name))
	t.Fatalf("in-flight for %s did not reach %v within %s (stuck at %v)", name, want, within, got)
}

// saturate dispatches n slow calls that each hold a slot until killed, and waits until all n are
// in flight. They are freed when the plugin is stopped at cleanup (or drained on reload).
func saturate(t *testing.T, l *Loader, name string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		go func() { _ = l.dispatch(context.Background(), name, "Value", scoringValue) }()
	}
	waitInflight(t, name, float64(n), 5*time.Second)
}

// Invariant #11 (shed names the cap): a call shed at the per-plugin cap and one shed at the
// global cap return DISTINCT, operator-legible errors and increment DISTINCT metric levels —
// "one plugin at its own cap" and "the shared budget exhausted" need different responses.
func TestShedDistinguishesPerPluginFromGlobal(t *testing.T) {
	slow := buildDouble(t, "slow")

	t.Run("per_plugin", func(t *testing.T) {
		name := "slow-shed-perplugin"
		l := newLoader()
		l.budget = newBudget(2, 0, 150*time.Millisecond) // cap 2, global unbounded
		startReady(t, l, name, slow, budgetConfig())
		saturate(t, l, name, 2)

		before := metrics.CounterValue(metrics.PluginInflightShed.WithLabelValues(name, "per_plugin"))
		err := l.dispatch(context.Background(), name, "Value", scoringValue) // 3rd → sheds
		var shed *ShedError
		if !errors.As(err, &shed) || shed.Level != ShedPerPlugin {
			t.Fatalf("3rd call err = %v; want a per-plugin ShedError", err)
		}
		if shed.Error() != "plugin "+name+": at per-plugin in-flight cap" {
			t.Errorf("message = %q; want it to name the per-plugin cap", shed.Error())
		}
		if got := metrics.CounterValue(metrics.PluginInflightShed.WithLabelValues(name, "per_plugin")); got != before+1 {
			t.Errorf("per_plugin shed metric = %v; want %v", got, before+1)
		}
	})

	t.Run("global", func(t *testing.T) {
		name := "slow-shed-global"
		l := newLoader()
		l.budget = newBudget(10, 2, 150*time.Millisecond) // per-plugin generous, GLOBAL cap 2
		startReady(t, l, name, slow, budgetConfig())
		saturate(t, l, name, 2) // fills the global budget under the per-plugin cap

		before := metrics.CounterValue(metrics.PluginInflightShed.WithLabelValues(name, "global"))
		err := l.dispatch(context.Background(), name, "Value", scoringValue)
		var shed *ShedError
		if !errors.As(err, &shed) || shed.Level != ShedGlobal {
			t.Fatalf("3rd call err = %v; want a global ShedError", err)
		}
		if shed.Error() != "plugin "+name+": global plugin budget exhausted" {
			t.Errorf("message = %q; want it to name the global budget", shed.Error())
		}
		if got := metrics.CounterValue(metrics.PluginInflightShed.WithLabelValues(name, "global")); got != before+1 {
			t.Errorf("global shed metric = %v; want %v", got, before+1)
		}
	})
}

// Invariant #11 (isolation): a plugin saturated at its per-plugin cap does not delay calls to a
// DIFFERENT plugin — the whole point of a PER-plugin cap over a single global one.
func TestPerPluginCapDoesNotDelayOtherPlugins(t *testing.T) {
	slowBin := buildDouble(t, "slow")
	goodBin := buildDouble(t, "goodscore")

	l := newLoader()
	l.budget = newBudget(2, 0, 150*time.Millisecond) // per-plugin 2, global unbounded (no cross-starve)
	startReady(t, l, "slow-iso", slowBin, budgetConfig())
	startReady(t, l, "good-iso", goodBin, budgetConfig())

	saturate(t, l, "slow-iso", 2) // slow fully saturated

	start := time.Now()
	if err := l.dispatch(context.Background(), "good-iso", "Value", scoringValue); err != nil {
		t.Fatalf("goodscore call while slow is saturated: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("goodscore took %s while slow was at its cap — the per-plugin cap is not isolating", elapsed)
	}
}

// Invariant #11 (a slow-but-successful plugin is visible without logs): the latency histogram
// records a 4s SUCCESSFUL call in its tail buckets — a mean would smear it, DefBuckets would
// bury it above 1s.
func TestSlowPluginLatencyIsVisibleInHistogram(t *testing.T) {
	slow := buildDouble(t, "slow")
	name := "slow-histogram"
	l := newLoader()
	startReady(t, l, name, slow, budgetConfig())

	if err := l.dispatch(context.Background(), name, "Value", scoringValue); err != nil {
		t.Fatalf("slow call: %v", err) // slow SUCCEEDS (correct, just 4s)
	}

	h := pluginHistogram(t, name)
	if h == nil {
		t.Fatal("no latency histogram recorded for the slow plugin")
	}
	if h.GetSampleCount() < 1 || h.GetSampleSum() < 3.5 {
		t.Errorf("histogram count=%d sum=%.2f; want a ~4s observation recorded", h.GetSampleCount(), h.GetSampleSum())
	}
	// The observation must land in the TAIL: cumulative count at le=2s is below the total.
	if le2 := cumulativeAtLE(h, 2.0); le2 >= h.GetSampleCount() {
		t.Errorf("all %d observations are <=2s (le2=%d) — the 4s tail is not visible", h.GetSampleCount(), le2)
	}
}

// Invariant #11 (THE money test — drain-timeout release): a slow plugin holds its whole
// in-flight budget; a reload drains, times out on the slow calls, and kills. Every slot MUST be
// released on that cancel path — repeated across reloads, because a one-slot-per-reload leak is
// invisible until it accumulates and the budget can no longer be filled (the port-leak shape).
func TestDrainTimeoutReleasesInflightSlots(t *testing.T) {
	slow := buildDouble(t, "slow")
	name := "slow-drain"
	l := newLoader()
	l.budget = newBudget(2, 0, 500*time.Millisecond)
	s := startReady(t, l, name, slow, budgetConfig()) // 200ms drain < slow's 4s

	for i := 0; i < 3; i++ {
		var wg sync.WaitGroup
		for j := 0; j < 2; j++ { // fill the whole per-plugin budget with slow calls
			wg.Add(1)
			go func() { defer wg.Done(); _ = l.dispatch(context.Background(), name, "Value", scoringValue) }()
		}
		waitInflight(t, name, 2, 5*time.Second)

		// Reload drains the old instance (200ms), times out on the slow calls, kills it — each
		// killed call's deferred release must free its slot.
		if err := s.reload(context.Background()); err != nil {
			t.Fatalf("reload %d: %v", i, err)
		}
		// A slot leaked on the drain-timeout path would never let this reach 0, and the next
		// fill would shed instead of acquiring.
		waitInflight(t, name, 0, 5*time.Second)
		wg.Wait()
	}
}

// pluginHistogram gathers the registry and returns the call-duration histogram for one plugin.
func pluginHistogram(t *testing.T, name string) *dto.Histogram {
	t.Helper()
	fams, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, f := range fams {
		if f.GetName() != "osctf_plugin_call_duration_seconds" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "plugin" && lp.GetValue() == name {
					return m.GetHistogram()
				}
			}
		}
	}
	return nil
}

// cumulativeAtLE returns the cumulative observation count in the bucket with upper bound le.
func cumulativeAtLE(h *dto.Histogram, le float64) uint64 {
	for _, b := range h.GetBucket() {
		if b.GetUpperBound() == le {
			return b.GetCumulativeCount()
		}
	}
	return 0
}
