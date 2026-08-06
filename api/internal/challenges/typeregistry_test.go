package challenges

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type stubType struct{ id string }

func (s stubType) ID() string                                         { return s.id }
func (stubType) ValidateConfig(map[string]string) map[string][]string { return nil }

// TestTypeRegistryResolution: built-ins resolve; unknown ids miss; a plugin type registers
// without disturbing the built-ins.
func TestTypeRegistryResolution(t *testing.T) {
	r := NewTypeRegistry(builtinType{"standard"}, builtinType{"container"})

	if !r.IsRegistered("standard") || !r.IsRegistered("container") {
		t.Fatal("built-ins should resolve")
	}
	if r.IsRegistered("regex-flag") {
		t.Fatal("unregistered type resolved, want miss")
	}
	if err := r.Register("regex-flag", stubType{"regex-flag"}, false); err != nil {
		t.Fatalf("register: %v", err)
	}
	if !r.IsRegistered("regex-flag") {
		t.Fatal("registered plugin type should resolve")
	}
	if !r.IsRegistered("standard") {
		t.Fatal("built-in disturbed by a plugin registration")
	}
}

// TestTypeRegistryBuiltinOverrideProtected: a built-in can't be replaced without override.
func TestTypeRegistryBuiltinOverrideProtected(t *testing.T) {
	r := NewTypeRegistry(builtinType{"standard"})
	if err := r.Register("standard", stubType{"standard"}, false); err == nil {
		t.Fatal("Register(standard, override=false) succeeded; want it refused")
	}
	if err := r.Register("standard", stubType{"standard"}, true); err != nil {
		t.Fatalf("override: %v", err)
	}
}

// TestTypeRegistryReaderAtomicContention pins the reader-atomic swap invariant: N readers
// resolve `standard` in a tight loop while a writer swaps the map. `standard` is in every
// version, so every read must resolve — never absent, never torn. Run under -race.
func TestTypeRegistryReaderAtomicContention(t *testing.T) {
	r := NewTypeRegistry(builtinType{"standard"}, builtinType{"container"})

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
			_ = r.Register(fmt.Sprintf("p%d", i%8), stubType{"churn"}, false)
		}
	}()

	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if !r.IsRegistered("standard") {
					t.Errorf("IsRegistered(standard) false during a swap — type registry swap is not reader-atomic")
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
