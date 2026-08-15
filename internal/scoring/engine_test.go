package scoring

import "testing"

func TestStatic(t *testing.T) {
	t.Parallel()
	e := StaticEngine{}
	p := ChallengeScoring{Initial: 500, Min: 100, Decay: 50}
	for _, solves := range []int{0, 1, 10, 1000} {
		if got := e.Value(p, solves); got != 500 {
			t.Errorf("Static.Value(solves=%d) = %d, want 500", solves, got)
		}
	}
}

// Exact test vectors from docs/v0.1/07-scoring.md (Initial=500, Min=100, Decay=50).
func TestDynamicVectors(t *testing.T) {
	t.Parallel()
	p := ChallengeScoring{Initial: 500, Min: 100, Decay: 50}
	cases := []struct {
		solves int
		want   int
	}{
		{0, 500},
		{1, 500}, // rounds from 499.84
		{5, 496},
		{10, 484},
		{25, 400},
		{50, 100}, // reaches Min
		{80, 100}, // clamped
	}
	e := DynamicEngine{}
	for _, c := range cases {
		if got := e.Value(p, c.solves); got != c.want {
			t.Errorf("Dynamic.Value(solves=%d) = %d, want %d", c.solves, got, c.want)
		}
	}
}

func TestDynamicNegativeSolvesTreatedAsZero(t *testing.T) {
	t.Parallel()
	p := ChallengeScoring{Initial: 500, Min: 100, Decay: 50}
	if got := (DynamicEngine{}).Value(p, -5); got != 500 {
		t.Errorf("Dynamic.Value(-5) = %d, want 500", got)
	}
}

func TestDynamicDefaultParams(t *testing.T) {
	t.Parallel()
	// Admin UI prefill defaults: Initial=500, Min=100, Decay=25.
	p := ChallengeScoring{Initial: 500, Min: 100, Decay: 25}
	if got := (DynamicEngine{}).Value(p, 0); got != 500 {
		t.Errorf("value at 0 solves = %d, want 500", got)
	}
	if got := (DynamicEngine{}).Value(p, 25); got != 100 {
		t.Errorf("value at decay solves = %d, want 100 (Min)", got)
	}
	if got := (DynamicEngine{}).Value(p, 100); got != 100 {
		t.Errorf("value past decay = %d, want 100 (clamped)", got)
	}
}

func TestRegistryAndValue(t *testing.T) {
	t.Parallel()
	if Value("static", ChallengeScoring{Initial: 300}, 99) != 300 {
		t.Error("Value(static) wrong")
	}
	if Value("unknown", ChallengeScoring{Initial: 42}, 5) != 42 {
		t.Error("Value(unknown) should fall back to static")
	}
	reg := Engines()
	if reg["static"].Name() != "static" || reg["dynamic"].Name() != "dynamic" {
		t.Error("registry names wrong")
	}
}
