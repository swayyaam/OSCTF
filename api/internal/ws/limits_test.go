package ws

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestAdmissionsGlobalCap: the global cap admits up to MaxConns live connections and
// rejects the next with the "global" reason; releasing frees a slot.
func TestAdmissionsGlobalCap(t *testing.T) {
	a := newAdmissions(Limits{MaxConns: 2})
	if _, ok := a.admit("1.1.1.1"); !ok {
		t.Fatal("1st admit rejected")
	}
	if _, ok := a.admit("2.2.2.2"); !ok {
		t.Fatal("2nd admit rejected")
	}
	if limit, ok := a.admit("3.3.3.3"); ok || limit != "global" {
		t.Fatalf("3rd admit = (%q, %v), want (global, false)", limit, ok)
	}
	a.release("1.1.1.1")
	if _, ok := a.admit("4.4.4.4"); !ok {
		t.Fatal("admit after release rejected — the freed global slot was not returned")
	}
}

// TestAdmissionsPerKeyCap: one key is capped at MaxConnsPerKey without affecting others.
// The key is per-user (or per anon IP), so two different keys — e.g. two logged-in players
// behind the same NAT — get independent budgets.
func TestAdmissionsPerKeyCap(t *testing.T) {
	a := newAdmissions(Limits{MaxConns: 100, MaxConnsPerKey: 2})
	const key = "u:alice"
	if _, ok := a.admit(key); !ok {
		t.Fatal("1st per-key admit rejected")
	}
	if _, ok := a.admit(key); !ok {
		t.Fatal("2nd per-key admit rejected")
	}
	if limit, ok := a.admit(key); ok || limit != "per_key" {
		t.Fatalf("3rd same-key admit = (%q, %v), want (per_key, false)", limit, ok)
	}
	if _, ok := a.admit("u:bob"); !ok {
		t.Fatal("a different key (another user behind the same NAT) was rejected by the per-key cap")
	}
	a.release(key)
	if _, ok := a.admit(key); !ok {
		t.Fatal("same key rejected after release — the per-key slot was not returned")
	}
}

// TestAdmissionsHandshakeRate: handshake attempts (not just live connections) are
// throttled per key within a sliding window; releasing a connection does not refund a
// handshake, and the window eventually clears.
func TestAdmissionsHandshakeRate(t *testing.T) {
	a := newAdmissions(Limits{MaxConns: 100, MaxConnsPerKey: 100, HandshakeBurst: 2, HandshakeWindow: time.Minute})
	base := time.Now()
	a.nowFn = func() time.Time { return base }
	const key = "ip:5.5.5.5"

	a.admit(key)
	a.release(key) // a completed-then-closed connection still counts as a handshake
	a.admit(key)
	a.release(key)
	if limit, ok := a.admit(key); ok || limit != "handshake_rate" {
		t.Fatalf("3rd handshake within the window = (%q, %v), want (handshake_rate, false)", limit, ok)
	}

	a.nowFn = func() time.Time { return base.Add(2 * time.Minute) } // window elapses
	if limit, ok := a.admit(key); !ok {
		t.Fatalf("handshake rejected (%q) after the window elapsed", limit)
	}
}

// TestAdmissionsHandshakeForgiveness: after a server-initiated disconnect, the shed
// client's reconnect is exempt from the handshake rate limit so it can never be locked out
// of getting back in — the exemption is one-shot and does not count against the window.
func TestAdmissionsHandshakeForgiveness(t *testing.T) {
	a := newAdmissions(Limits{MaxConns: 100, MaxConnsPerKey: 100, HandshakeBurst: 1, HandshakeWindow: time.Minute})
	const key = "u:slowpoke"

	if _, ok := a.admit(key); !ok { // burns the single handshake in the window
		t.Fatal("first handshake rejected")
	}
	if limit, ok := a.admit(key); ok || limit != "handshake_rate" {
		t.Fatalf("second handshake = (%q, %v), want (handshake_rate, false) — window exhausted", limit, ok)
	}
	// The server sheds the client (overflow); its reconnect must be forgiven.
	a.forgiveHandshake(key)
	if _, ok := a.admit(key); !ok {
		t.Fatal("forgiven reconnect rejected — a shed client can be locked out")
	}
	// Forgiveness is one-shot: the next handshake is limited again.
	if limit, ok := a.admit(key); ok || limit != "handshake_rate" {
		t.Fatalf("post-forgiveness handshake = (%q, %v), want (handshake_rate, false)", limit, ok)
	}
}

// TestAdmissionKeyOfDefaultAndResolver: keyOf defaults to the socket IP and honours an
// injected resolver (the production user-or-IP keying).
func TestAdmissionKeyOfDefaultAndResolver(t *testing.T) {
	a := newAdmissions(Limits{})
	r := httptest.NewRequest(http.MethodGet, "/api/v0/ws", nil)
	r.RemoteAddr = "203.0.113.7:44321"
	if got := a.keyOf(r); got != "ip:203.0.113.7" {
		t.Errorf("default keyOf = %q, want ip:203.0.113.7", got)
	}
	a.keyFn = func(*http.Request) string { return "u:carol" }
	if got := a.keyOf(r); got != "u:carol" {
		t.Errorf("resolver keyOf = %q, want u:carol", got)
	}
}

// TestSafeGlobalCap: the global cap is clamped to the fd budget (with a reserve for
// non-WS consumers), an unlimited (0) cap is made concrete, and an unknown/unlimited fd
// limit leaves the configured value untouched.
func TestSafeGlobalCap(t *testing.T) {
	cases := []struct {
		name       string
		configured int
		fdSoft     uint64
		wantCap    int
		wantClamp  bool
	}{
		{"under budget passes through", 500, 4096, 500, false},
		{"over budget clamps to fd-reserve", 20000, 4096, 3072, true}, // 4096 - 1024
		{"1024 fd host clamps 20000", 20000, 1024, 768, true},         // 1024 - 256 (min reserve)
		{"unlimited config made concrete", 0, 4096, 3072, true},       // 0 → supported
		{"unknown fd limit untouched", 20000, 0, 20000, false},        // Getrlimit failed / 0
		{"infinite fd limit untouched", 20000, 1 << 40, 20000, false}, // RLIM_INFINITY
		{"generous host keeps config", 20000, 65536, 20000, false},    // 65536 - 16384 = 49152 >= 20000
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eff, clamped := SafeGlobalCap(c.configured, c.fdSoft)
			if eff != c.wantCap || clamped != c.wantClamp {
				t.Errorf("SafeGlobalCap(%d, %d) = (%d, %v), want (%d, %v)", c.configured, c.fdSoft, eff, clamped, c.wantCap, c.wantClamp)
			}
		})
	}
}

// TestAdmissionsZeroDisables: zero limits disable each check (unlimited).
func TestAdmissionsZeroDisables(t *testing.T) {
	a := newAdmissions(Limits{}) // all zero
	for i := 0; i < 50; i++ {
		if _, ok := a.admit("1.2.3.4"); !ok {
			t.Fatalf("admit %d rejected with all limits disabled", i)
		}
	}
}
