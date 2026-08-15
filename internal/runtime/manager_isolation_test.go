package runtime

import (
	"io"
	"log/slog"
	"testing"
)

// TestIsolationGate pins the fail-closed container-deploy gate so it cannot silently invert:
// unenforced/unknown isolation with the override OFF must refuse; the override ON must permit;
// enforced isolation always permits. The gate runs at the top of DeployForTeam.
func TestIsolationGate(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Daemon does NOT enforce isolation (the Docker Desktop case).
	m := NewManager(nil, nil, "h", 30000, 30001)
	m.RecordIsolationVerdict(false)

	m.ConfigureIsolationGate(false, log) // override OFF → refuse
	if err := m.isolationGate(); err == nil {
		t.Error("override OFF + unenforced isolation must REFUSE the deploy")
	}
	m.ConfigureIsolationGate(true, log) // override ON → permit
	if err := m.isolationGate(); err != nil {
		t.Errorf("override ON must PERMIT the deploy, got: %v", err)
	}

	// Enforced isolation (native Linux) permits regardless of the override.
	m.RecordIsolationVerdict(true)
	m.ConfigureIsolationGate(false, log)
	if err := m.isolationGate(); err != nil {
		t.Errorf("enforced isolation must PERMIT the deploy, got: %v", err)
	}

	// Verdict not yet known (self-check pending or errored) must FAIL CLOSED with the override off,
	// and the override must still open it — so a startup window can never leak an unisolated deploy.
	mu := NewManager(nil, nil, "h", 30000, 30001)
	mu.ConfigureIsolationGate(false, log)
	if err := mu.isolationGate(); err == nil {
		t.Error("unknown verdict + override OFF must REFUSE (fail closed)")
	}
	mu.ConfigureIsolationGate(true, log)
	if err := mu.isolationGate(); err != nil {
		t.Errorf("unknown verdict + override ON must PERMIT, got: %v", err)
	}
}
