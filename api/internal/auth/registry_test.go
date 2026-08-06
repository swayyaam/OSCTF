package auth_test

import (
	"context"
	"fmt"
	"sync"
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

// TestAuthRegistryReaderAtomicSwap pins invariant (c): a register/swap is atomic from a
// reader's perspective. Under concurrent Get + Register, a name present in every map
// version (here `email`, never removed) must ALWAYS resolve — never absent, never torn.
// Run under -race; a non-atomic swap (or a torn map read) fails this.
func TestAuthRegistryReaderAtomicSwap(t *testing.T) {
	email := &stubProvider{name: "email"}
	r := auth.NewRegistry(email)

	var wg sync.WaitGroup
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
			_ = r.Register(fmt.Sprintf("p%d", i%4), &stubProvider{name: "churn"}, false)
		}
	}()

	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, ok := r.Get("email"); !ok {
					t.Errorf("Get(email) returned absent during a concurrent swap — registry swap is not reader-atomic")
					return
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
