package fdbudget

import "testing"

// The reserve for other consumers is taken ONCE, and two consumers claiming in order subtract
// from the same remainder — so they cannot each assume the whole budget (the collective-
// exhaustion bug this package exists to prevent).
func TestClaimsShareOneReserveInOrder(t *testing.T) {
	b := New(65536) // reserve = 16384, remaining = 49152
	soft, reserved, _, _ := b.Split()
	if soft != 65536 || reserved != 16384 {
		t.Fatalf("reserve = %d (soft %d); want 16384 of 65536", reserved, soft)
	}

	// Essential fixed consumer claims first and gets exactly what it wants.
	if g, clamped := b.Claim("plugin-inflight", 256); g != 256 || clamped {
		t.Errorf("plugin claim = (%d, %v); want (256, false)", g, clamped)
	}
	// Elastic consumer claims the rest and, with headroom, also gets what it wants.
	if g, clamped := b.Claim("websocket-conns", 20000); g != 20000 || clamped {
		t.Errorf("ws claim = (%d, %v); want (20000, false)", g, clamped)
	}
	_, _, remaining, claims := b.Split()
	if remaining != 49152-256-20000 {
		t.Errorf("remaining = %d; want %d", remaining, 49152-256-20000)
	}
	if len(claims) != 2 || claims[0].Name != "plugin-inflight" || claims[1].Name != "websocket-conns" {
		t.Errorf("claims not recorded in order: %+v", claims)
	}
}

// Under a tight limit the priority order protects the essential consumer: plugins claim their
// budget first, WS absorbs (and is clamped to) whatever remains — WS shrinks, plugins keep
// working.
func TestTightLimitFavoursEssentialConsumer(t *testing.T) {
	b := New(1024) // reserve = 256, remaining = 768
	if g, clamped := b.Claim("plugin-inflight", 256); g != 256 || clamped {
		t.Errorf("plugin claim = (%d, %v); want (256, false)", g, clamped)
	}
	if g, clamped := b.Claim("websocket-conns", 20000); g != 512 || !clamped {
		t.Errorf("ws claim = (%d, %v); want (512, true) — clamped to the remainder", g, clamped)
	}
}

// A want of 0 or less means "as much as available" and is clamped to the remainder, not treated
// as unlimited — the config-says-unlimited case the fd limit makes concrete.
func TestUnlimitedWantClampsToRemainder(t *testing.T) {
	b := New(2048) // reserve = 512, remaining = 1536
	if g, clamped := b.Claim("plugin-inflight", 0); g != 1536 || !clamped {
		t.Errorf("claim(0) = (%d, %v); want (1536, true)", g, clamped)
	}
	if g, clamped := b.Claim("websocket-conns", 100); g != 0 || !clamped {
		t.Errorf("claim after exhaustion = (%d, %v); want (0, true)", g, clamped)
	}
}

// A zero or RLIM_INFINITY soft limit is unbounded: claims pass through unchanged, so a host with
// no meaningful fd constraint is never throttled by a synthetic cap.
func TestUnboundedPassesClaimsThrough(t *testing.T) {
	for _, soft := range []uint64{0, 1 << 31, 1 << 40} {
		b := New(soft)
		if g, clamped := b.Claim("plugin-inflight", 100000); g != 100000 || clamped {
			t.Errorf("New(%d) claim = (%d, %v); want (100000, false)", soft, g, clamped)
		}
	}
}
