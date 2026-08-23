package main

import (
	"testing"

	"github.com/swayyaam/OSCTF/internal/config"
)

// The composition invariant that no unit test of either half can see: the two consumers' claims
// PLUS the one-time reserve PLUS the unclaimed remainder sum to exactly the fd soft limit. If the
// reserve were applied per-consumer (double-counted) or a claim were made against the wrong
// remainder, this sum would not close. It also asserts the essential consumer (plugins, claimed
// first) is never starved to zero by the elastic one.
func TestDeriveResourceBudgetReservesExactlyOnce(t *testing.T) {
	cfg := &config.Config{WSMaxConns: 20000, PluginMaxInflightTotal: 256}

	for _, fdSoft := range []uint64{1024, 4096, 65536} {
		rb := deriveResourceBudget(fdSoft, cfg)
		_, reserved, remaining, _ := rb.acct.Split()
		sum := uint64(rb.pluginGlobal) + uint64(rb.wsMaxConns) + reserved + remaining
		if sum != fdSoft {
			t.Errorf("fdSoft=%d: plugin(%d)+ws(%d)+reserve(%d)+remaining(%d)=%d, want %d — the reserve is not counted exactly once",
				fdSoft, rb.pluginGlobal, rb.wsMaxConns, reserved, remaining, sum, fdSoft)
		}
		if rb.pluginGlobal < 1 {
			t.Errorf("fdSoft=%d: plugin got %d — the essential consumer (claimed first) was starved", fdSoft, rb.pluginGlobal)
		}
	}

	// Unbounded (Getrlimit failed / RLIM_INFINITY): both claims pass through unclamped.
	rb := deriveResourceBudget(0, cfg)
	if rb.pluginGlobal != 256 || rb.wsMaxConns != 20000 {
		t.Errorf("unbounded: plugin=%d ws=%d; want 256/20000 unclamped", rb.pluginGlobal, rb.wsMaxConns)
	}
}
