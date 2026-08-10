// Package fdbudget apportions a process's file-descriptor limit among the consumers that hold
// an fd per unit of concurrent work: live WebSocket connections and in-flight plugin calls
// (each pins its inbound request fd for the call's duration). If either consumer sized itself
// against the whole limit independently, N of them could collectively exhaust it — the "accept:
// too many open files" cliff that takes down Postgres, Docker, and Redis together, before any
// single consumer's own admission control sheds load.
//
// So the reserve for everything else (DB pool, Docker, Redis, S3, HTTP) is taken ONCE, and
// consumers Claim from the shared remainder in priority order: the essential, fixed-size
// consumers (plugin calls — auth/scoring) claim first and are guaranteed their budget; the
// elastic consumer (WebSockets, degradable to polling) claims last and absorbs whatever is
// left. RLIM_INFINITY / an unreadable limit leaves every claim unclamped.
package fdbudget

import (
	"fmt"
	"sync"
)

// unlimited treats an implausibly large soft limit (or RLIM_INFINITY) as "no fd constraint".
const unlimited = 1 << 31

// Claim records one consumer's apportionment, for logging the derived split at startup.
type Claim struct {
	Name    string // consumer, e.g. "plugin-inflight" or "websocket-conns"
	Want    int    // what it asked for (0 or negative means "as much as available")
	Granted int    // what it actually got after clamping to the remainder
	Clamped bool   // true if Granted < Want (the limit, not the config, is binding)
}

// Budget is the shared fd accountant. Construct once at startup, Claim in priority order.
type Budget struct {
	mu        sync.Mutex
	soft      uint64 // RLIMIT_NOFILE soft limit (0 when unbounded)
	reserved  uint64 // held back for DB/Docker/Redis/HTTP and other non-counted consumers
	remaining uint64 // still available to Claim
	unbounded bool
	claims    []Claim
}

// New computes the one-time reserve (a quarter of the limit, at least 256) and the claimable
// remainder. A zero or implausibly large soft limit yields an unbounded budget (claims pass
// through unchanged), matching a host with no meaningful fd constraint.
func New(fdSoftLimit uint64) *Budget {
	if fdSoftLimit == 0 || fdSoftLimit >= unlimited {
		return &Budget{unbounded: true}
	}
	reserve := fdSoftLimit / 4
	if reserve < 256 {
		reserve = 256
	}
	remaining := uint64(0)
	if fdSoftLimit > reserve {
		remaining = fdSoftLimit - reserve
	}
	return &Budget{soft: fdSoftLimit, reserved: reserve, remaining: remaining}
}

// Claim grants up to want fds to a named consumer, subtracting from the shared remainder. A
// want of 0 or less means "as much as available" and is always clamped to the remainder; a want
// above the remainder is clamped down to it. Returns the grant and whether the fd limit (not the
// configured value) was the binding constraint. Claim in priority order: essential fixed
// consumers first, the elastic one last.
func (b *Budget) Claim(name string, want int) (granted int, clamped bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unbounded {
		b.claims = append(b.claims, Claim{Name: name, Want: want, Granted: want, Clamped: false})
		return want, false
	}
	//nolint:gosec // G115: remaining < unlimited (1<<31), so it fits in a non-negative int.
	rem := int(b.remaining)
	granted = want
	if want <= 0 || want > rem {
		granted = rem
		clamped = true
	}
	//nolint:gosec // G115: granted is clamped to [0, rem] above, never negative.
	b.remaining -= uint64(granted)
	b.claims = append(b.claims, Claim{Name: name, Want: want, Granted: granted, Clamped: clamped})
	return granted, clamped
}

// Split returns the soft limit, the one-time reserve, the still-unclaimed remainder, and every
// claim in order — so startup can log exactly where the fds went.
func (b *Budget) Split() (soft, reserved, remaining uint64, claims []Claim) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.soft, b.reserved, b.remaining, append([]Claim(nil), b.claims...)
}

// String renders the split as a single operator-readable line for the startup log.
func (b *Budget) String() string {
	soft, reserved, remaining, claims := b.Split()
	if b.unbounded {
		s := "fd budget: unbounded (no RLIMIT_NOFILE constraint)"
		for _, c := range claims {
			s += fmt.Sprintf("; %s=%d", c.Name, c.Granted)
		}
		return s
	}
	s := fmt.Sprintf("fd budget: soft=%d reserved=%d remaining=%d", soft, reserved, remaining)
	for _, c := range claims {
		s += fmt.Sprintf("; %s=%d", c.Name, c.Granted)
		if c.Clamped {
			s += fmt.Sprintf("(clamped from %d)", c.Want)
		}
	}
	return s
}
