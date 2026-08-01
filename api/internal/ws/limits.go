package ws

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Limits bounds the public, unauthenticated WebSocket endpoint. Without them a single
// client can open connections until the process dies — each live connection holds two
// goroutines (read/write pumps) and a send buffer — and CTF participants are exactly the
// population that will try. A zero field disables that particular limit.
//
// The per-client caps and the handshake rate are keyed by an admission KEY, not raw IP:
// authenticated connections key on the user id (so a whole campus/venue NAT of logged-in
// players is not squeezed through one IP budget — GitHub issue #1's shared-IP class), and
// only anonymous connections fall back to the client IP.
//
// Per-key keying keyed on the user is bypassable: an attacker who registers N accounts
// gets N × MaxConnsPerKey connections. Registration limits bound N only loosely (and issue
// #1 wants those raised for shared-IP venues), so the per-key cap is a fairness limit, not
// the DoS backstop. The GLOBAL cap (MaxConns, itself clamped to the file-descriptor budget
// by SafeGlobalCap) is the real backstop against process exhaustion.
type Limits struct {
	MaxConns        int           // global cap on live connections (0 = unlimited)
	MaxConnsPerKey  int           // cap on live connections per admission key (0 = unlimited)
	HandshakeBurst  int           // max handshake attempts per key within HandshakeWindow (0 = unlimited)
	HandshakeWindow time.Duration // sliding window for the handshake rate limit
}

// DefaultLimits protects a single-server deployment without squeezing a large event.
// Generous per-key budgets plus per-user keying mean a NAT full of logged-in players is
// unaffected; the caps still stop any single key — and the global ceiling any fleet —
// from exhausting the process. Operators can override every value (OSCTF_WS_*).
func DefaultLimits() Limits {
	return Limits{MaxConns: 20000, MaxConnsPerKey: 256, HandshakeBurst: 600, HandshakeWindow: time.Minute}
}

// admissions tracks live-connection counts and recent handshake attempts per admission
// key. All access is under mu; it never touches the hub's event loop, so a rejection
// cannot stall or degrade existing connections.
type admissions struct {
	mu      sync.Mutex
	limits  Limits
	total   int
	perKey  map[string]int
	hsHits  map[string][]time.Time
	forgive map[string]int // keys owed a handshake exemption after a server-initiated disconnect
	keyFn   func(*http.Request) string
	nowFn   func() time.Time
}

func newAdmissions(limits Limits) *admissions {
	return &admissions{
		limits:  limits,
		perKey:  map[string]int{},
		hsHits:  map[string][]time.Time{},
		forgive: map[string]int{},
		nowFn:   time.Now,
	}
}

// forgiveHandshake grants key one handshake that will not count against (or be denied by)
// the handshake rate limit. It is called when the SERVER sheds a connection (overflow
// disconnect): the client's immediate reconnect must not be punished by the limiter for a
// disconnect the server chose, or a slow client could churn itself out of reconnecting.
// Capped so it can never become a standing bypass.
func (a *admissions) forgiveHandshake(key string) {
	a.mu.Lock()
	if a.forgive[key] < 4 {
		a.forgive[key]++
	}
	a.mu.Unlock()
}

// keyOf resolves the admission key for a request: the configured resolver (user-or-IP in
// production) or, by default, the socket peer IP.
func (a *admissions) keyOf(r *http.Request) string {
	if a.keyFn != nil {
		return a.keyFn(r)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "ip:" + r.RemoteAddr
	}
	return "ip:" + host
}

// admit reserves a connection slot for key, enforcing the handshake rate and then the
// global and per-key caps. It returns ("", true) on success; on rejection it reserves
// nothing and returns the name of the limit that fired ("handshake_rate", "global",
// "per_key") so the caller can reject the handshake before upgrading.
func (a *admissions) admit(key string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// A reconnect owed an exemption (after a server-initiated overflow disconnect) skips
	// the handshake rate limit entirely — it is not counted and cannot be denied — so the
	// shed client can always get back in. Connection caps below still apply.
	forgiven := a.forgive[key] > 0
	if forgiven {
		a.forgive[key]--
		if a.forgive[key] == 0 {
			delete(a.forgive, key)
		}
	}
	if !forgiven && a.limits.HandshakeBurst > 0 {
		now := a.nowFn()
		hits := a.hsHits[key][:0]
		for _, t := range a.hsHits[key] {
			if now.Sub(t) < a.limits.HandshakeWindow {
				hits = append(hits, t)
			}
		}
		if len(hits) >= a.limits.HandshakeBurst {
			a.hsHits[key] = hits // keep the pruned window; the attempt is still counted as denied
			return "handshake_rate", false
		}
		a.hsHits[key] = append(hits, now)
	}
	if a.limits.MaxConns > 0 && a.total >= a.limits.MaxConns {
		return "global", false
	}
	if a.limits.MaxConnsPerKey > 0 && a.perKey[key] >= a.limits.MaxConnsPerKey {
		return "per_key", false
	}
	a.total++
	a.perKey[key]++
	return "", true
}

// release returns a slot previously reserved by admit. It MUST run on every connection
// teardown — clean close, abrupt drop, panic, and shutdown — or dead slots accumulate
// against the per-key cap and legitimate reconnects are rejected while the hub holds no
// real clients.
func (a *admissions) release(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.total > 0 {
		a.total--
	}
	if a.perKey[key] > 0 {
		a.perKey[key]--
		if a.perKey[key] == 0 {
			delete(a.perKey, key)
		}
	}
}

// retryAfterSeconds is a hint for the Retry-After header on a rejected handshake: the
// window for a handshake-rate rejection, a few seconds for a capacity rejection.
func (a *admissions) retryAfterSeconds(limit string) int {
	if limit == "handshake_rate" && a.limits.HandshakeWindow > 0 {
		return int(a.limits.HandshakeWindow.Seconds())
	}
	return 5
}

// liveTotal exposes the current reservation count for tests.
func (a *admissions) liveTotal() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.total
}

// SafeGlobalCap clamps a configured global connection cap to what the process's
// file-descriptor limit can actually support. Each live WebSocket connection consumes a
// socket fd; if the global cap exceeds RLIMIT_NOFILE the server hits "accept: too many
// open files" — which takes down HTTP, Postgres, and Docker calls together — before
// admission control ever sheds WS load. It reserves a quarter of the fd budget (at least
// 256) for those other consumers (DB pool, Docker, Redis, S3, HTTP) and returns the
// effective cap plus whether the configured value had to be reduced, so the caller can
// warn loudly. fdSoftLimit==0 or an "unlimited" value leaves the configured cap untouched.
func SafeGlobalCap(configured int, fdSoftLimit uint64) (effective int, clamped bool) {
	const unlimited = 1 << 31 // treat implausibly large / RLIM_INFINITY as "no fd constraint"
	if fdSoftLimit == 0 || fdSoftLimit >= unlimited {
		return configured, false
	}
	reserve := fdSoftLimit / 4
	if reserve < 256 {
		reserve = 256
	}
	supported := 1
	if fdSoftLimit > reserve {
		supported = int(fdSoftLimit - reserve)
	}
	// A configured cap of 0 means "unlimited"; the fd budget makes that concrete.
	if configured <= 0 || configured > supported {
		return supported, true
	}
	return configured, false
}
