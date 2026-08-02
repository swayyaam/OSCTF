package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/osctf/platform/internal/apperr"
)

// runConcurrentHashes releases n HashPassword calls simultaneously (a real
// thundering-herd) and reports how many succeeded plus the peak observed
// concurrency. Not safe to run in parallel with anything that hashes — it
// resets the package-global watermark.
func runConcurrentHashes(t *testing.T, n int) (ok int, peak int32) {
	t.Helper()
	hashInFlight.Store(0)
	hashPeak.Store(0)
	var succeeded atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // all goroutines begin together for maximal contention
			if _, err := HashPassword(context.Background(), "burst-password"); err == nil {
				succeeded.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	return int(succeeded.Load()), hashPeak.Load()
}

// TestHashGateBoundsConcurrency is the core issue-#3 proof: with the gate at k,
// a burst of n concurrent hashes never has more than k derivations in flight
// (so peak memory ≤ k×64 MiB), yet all n still succeed. The ungated baseline
// shows the pre-fix behaviour — peak concurrency ≈ n — for contrast.
func TestHashGateBoundsConcurrency(t *testing.T) {
	const k, n = 3, 12

	// Baseline: gate off. Every derivation runs at once — this is the OOM path,
	// n×64 MiB resident at the peak.
	ConfigureHashGate(0, 0)
	okBase, peakBase := runConcurrentHashes(t, n)
	if okBase != n {
		t.Fatalf("baseline: %d/%d hashes succeeded", okBase, n)
	}
	if peakBase <= k {
		t.Fatalf("baseline peak concurrency %d ≤ k=%d; the herd isn't actually concurrent, test is meaningless", peakBase, k)
	}

	// Gated: peak concurrency must never exceed k; with a generous queue wait no
	// request is shed — they queue and all complete.
	ConfigureHashGate(k, 10*time.Second)
	defer ConfigureHashGate(0, 0)
	okGated, peakGated := runConcurrentHashes(t, n)
	if okGated != n {
		t.Fatalf("gated: %d/%d hashes succeeded; want all to queue and complete", okGated, n)
	}
	if peakGated > k {
		t.Fatalf("gated peak concurrency %d exceeded gate size %d (memory not bounded)", peakGated, k)
	}
	if peakGated < 2 {
		t.Fatalf("gated peak concurrency %d < 2; the gate serialized everything, not exercising k slots", peakGated)
	}
	t.Logf("peak concurrency: ungated=%d (~%d MiB in flight) → gated=%d (≤%d MiB)",
		peakBase, int(peakBase)*64, peakGated, k*64)
}

// TestHashGateShedsWhenFull checks the load-shed path deterministically (no
// reliance on argon2 timing): a full gate whose wait elapses returns a
// 503-mapped error carrying a Retry-After, and a freed slot admits the next.
func TestHashGateShedsWhenFull(t *testing.T) {
	ConfigureHashGate(1, 20*time.Millisecond)
	defer ConfigureHashGate(0, 0)

	release, err := acquireHash(context.Background())
	if err != nil {
		t.Fatalf("first acquire on empty gate: %v", err)
	}

	// Gate is full; the second acquire waits maxWait, then is shed.
	if _, err := acquireHash(context.Background()); !errors.Is(err, apperr.ErrUnavailable) {
		t.Fatalf("saturated acquire = %v; want apperr.ErrUnavailable (503)", err)
	} else {
		var ua *apperr.Unavailable
		if !errors.As(err, &ua) || ua.RetryAfter <= 0 {
			t.Fatalf("shed error must carry a positive Retry-After; got %+v", ua)
		}
	}

	// Freeing the slot lets the next caller in.
	release()
	release2, err := acquireHash(context.Background())
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release2()
}

// TestHashGateContextCancelFreesCaller ensures a queued caller whose request is
// cancelled (client hung up) returns immediately with the context error rather
// than holding a slot until the wait elapses — real load-shedding under load.
func TestHashGateContextCancelFreesCaller(t *testing.T) {
	ConfigureHashGate(1, time.Minute) // long wait: only cancellation can free the second caller
	defer ConfigureHashGate(0, 0)

	release, err := acquireHash(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := acquireHash(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire on cancelled ctx = %v; want context.Canceled", err)
	}
}

func TestHashConcurrencyFor(t *testing.T) {
	t.Parallel()
	const mib = uint64(1) << 20
	cases := []struct {
		name  string
		memMB uint64 // 0 ⇒ unknown
		want  int
	}{
		{"unknown falls back to 0", 0, 0},
		{"128MB clamps up to floor 2", 128, 2}, // 128/4/64 = 0 → 2
		{"512MB", 512, 2},                      // 512/4/64 = 2
		{"4GB", 4096, 16},                      // 4096/4/64 = 16
		{"16GB", 16384, 64},                    // 16384/4/64 = 64 (at ceiling)
		{"1TB clamps down to 64", 1 << 20, 64},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var mem uint64
			if c.memMB > 0 {
				mem = c.memMB * mib
			}
			if got := hashConcurrencyFor(mem); got != c.want {
				t.Errorf("hashConcurrencyFor(%d MB) = %d; want %d", c.memMB, got, c.want)
			}
		})
	}
}

func TestReadCgroupLimit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if v, ok := readCgroupLimit(write("valid", "4294967296\n")); !ok || v != 4294967296 {
		t.Errorf("valid limit: got %d, ok=%v; want 4294967296, true", v, ok)
	}
	if _, ok := readCgroupLimit(write("v2max", "max\n")); ok {
		t.Error(`"max" (cgroup v2 unlimited) should return ok=false`)
	}
	if _, ok := readCgroupLimit(write("v1max", "9223372036854771712\n")); ok {
		t.Error("cgroup v1 unlimited sentinel should return ok=false")
	}
	if _, ok := readCgroupLimit(write("zero", "0")); ok {
		t.Error("zero limit should return ok=false")
	}
	if _, ok := readCgroupLimit(filepath.Join(dir, "absent")); ok {
		t.Error("absent file should return ok=false")
	}
}

func TestReadMemTotal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "meminfo")
	if err := os.WriteFile(p, []byte("MemFree: 100 kB\nMemTotal:    8192000 kB\nBuffers: 5 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := readMemTotal(p), uint64(8192000)*1024; got != want {
		t.Errorf("readMemTotal = %d; want %d", got, want)
	}
	if got := readMemTotal(filepath.Join(dir, "absent")); got != 0 {
		t.Errorf("absent meminfo = %d; want 0", got)
	}
}
