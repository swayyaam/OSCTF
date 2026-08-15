package auth_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/auth"
)

// stubProvider is a minimal AuthProvider for registry tests.
type stubProvider struct{ name string }

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Authenticate(context.Context, string, string) (uuid.UUID, error) {
	return uuid.Nil, nil
}

// TestAuthRegistryResolution: the built-in resolves as Default and by name; unknown
// names miss; a plugin provider registers without disturbing the default.
func TestAuthRegistryResolution(t *testing.T) {
	email := &stubProvider{name: "email"}
	r := auth.NewRegistry(email)

	if got := r.Default(); got != email {
		t.Fatalf("Default() = %v, want the built-in email provider", got)
	}
	if got, ok := r.Get("email"); !ok || got != email {
		t.Fatalf("Get(email) = %v,%v, want the built-in", got, ok)
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatalf("Get(nope) resolved, want miss")
	}

	oidc := &stubProvider{name: "oidc"}
	if err := r.Register("oidc", oidc, false); err != nil {
		t.Fatalf("Register(oidc): %v", err)
	}
	if got, ok := r.Get("oidc"); !ok || got != oidc {
		t.Fatalf("Get(oidc) = %v,%v, want the registered provider", got, ok)
	}
	if got := r.Default(); got != email {
		t.Fatalf("Default() changed to %v after registering a plugin; want email", got)
	}
}

// TestAuthRegistryBuiltinOverrideProtected: a built-in is not replaceable without an
// explicit override — a buggy/hostile plugin can't silently hijack `email`.
func TestAuthRegistryBuiltinOverrideProtected(t *testing.T) {
	email := &stubProvider{name: "email"}
	r := auth.NewRegistry(email)

	other := &stubProvider{name: "email"}
	if err := r.Register("email", other, false); err == nil {
		t.Fatalf("Register(email, override=false) succeeded; want it refused (built-in protected)")
	}
	if got, _ := r.Get("email"); got != email {
		t.Fatalf("built-in email was replaced without override; got %v", got)
	}
	if err := r.Register("email", other, true); err != nil {
		t.Fatalf("Register(email, override=true): %v", err)
	}
	if got, _ := r.Get("email"); got != other {
		t.Fatalf("override did not take effect; got %v", got)
	}
}

// TestAuthRegistryHasUsableLogin pins the boot-refuse condition: email disabled with no
// other provider means no way to log in (the platform must refuse to start).
func TestAuthRegistryHasUsableLogin(t *testing.T) {
	r := auth.NewRegistry(&stubProvider{name: "email"})

	if !r.HasUsableLogin(true) {
		t.Fatal("email enabled → should be usable")
	}
	if r.HasUsableLogin(false) {
		t.Fatal("email disabled with only email registered → must be unusable (boot-refuse case)")
	}
	if err := r.Register("oidc", &stubProvider{name: "oidc"}, false); err != nil {
		t.Fatalf("register oidc: %v", err)
	}
	if !r.HasUsableLogin(false) {
		t.Fatal("email disabled but a non-email provider registered → should be usable")
	}
}

// TestAuthRegistryReaderAtomicSwap pins invariant (c) as a contention test: a
// register/swap is atomic from a reader's perspective. N readers resolve `email` in a
// tight loop while a writer swaps the map repeatedly under load. `email` is present in
// every map version, so every read must return a VALID, NON-NIL provider that is actually
// the email provider — never absent, never nil, never torn. A copy-on-write bug (or a
// non-atomic swap) shows here as a missing/torn lookup. Run under -race.
func TestAuthRegistryReaderAtomicSwap(t *testing.T) {
	email := &stubProvider{name: "email"}
	r := auth.NewRegistry(email)

	var wg sync.WaitGroup
	var reads atomic.Int64
	stop := make(chan struct{})

	// Writer: churn a rotating set of plugin registrations so the map is swapped
	// continuously while readers run.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = r.Register(fmt.Sprintf("p%d", i%8), &stubProvider{name: "churn"}, false)
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
				p, ok := r.Get("email")
				if !ok || p == nil || p.Name() != "email" {
					t.Errorf("Get(email) during a swap returned (%v, ok=%v) — registry swap is not reader-atomic", p, ok)
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
