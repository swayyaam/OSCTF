package plugintest

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	hclog "github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"

	"github.com/osctf/platform/internal/plugin"
	"github.com/osctf/platform/internal/plugin/pluginpb"
)

// Build compiles a double (its directory name under doubles/) to a temp binary and returns the
// path. The doubles are real processes, so the loader (P3-c) and failure isolation (P3-d) are
// exercised against plugins that actually misbehave, not mocks.
func Build(t *testing.T, name string) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0) // locate doubles/ relative to this file, CWD-independent
	src := filepath.Join(filepath.Dir(self), "doubles", name)
	bin := filepath.Join(t.TempDir(), "double-"+name)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	//nolint:gosec // G204: a test harness building a known double (fixed source dir) to a temp path.
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build double %q: %v\n%s", name, err, out)
	}
	return bin
}

// clientConfig builds a go-plugin client for a built double binary (null logger to keep test
// output clean).
func clientConfig(bin string) *goplugin.ClientConfig {
	return &goplugin.ClientConfig{
		HandshakeConfig: plugin.Handshake,
		Plugins:         plugin.HostPluginSet(),
		//nolint:gosec // G204: launches the built double under test; go-plugin owns its lifecycle.
		Cmd:              exec.CommandContext(context.Background(), bin),
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		Logger:           hclog.NewNullLogger(),
	}
}

// Dial launches a built double and returns the go-plugin client (for Kill/lifecycle) plus a
// Scoring client. Fails the test if the handshake/dispense fails. The caller defers c.Kill().
func Dial(t *testing.T, bin string) (*goplugin.Client, pluginpb.ScoringClient) {
	t.Helper()
	c := goplugin.NewClient(clientConfig(bin))
	rpc, err := c.Client()
	if err != nil {
		c.Kill()
		t.Fatalf("dial %q: %v", bin, err)
	}
	raw, err := rpc.Dispense(plugin.KeyScoring)
	if err != nil {
		c.Kill()
		t.Fatalf("dispense scoring from %q: %v", bin, err)
	}
	return c, raw.(pluginpb.ScoringClient)
}

// TryDial is Dial without fataling: it returns the client (for Kill) and the error, so a test
// can assert a double is REFUSED (wrong ABI, crash-on-launch) rather than served.
func TryDial(bin string) (*goplugin.Client, pluginpb.ScoringClient, error) {
	c := goplugin.NewClient(clientConfig(bin))
	rpc, err := c.Client()
	if err != nil {
		return c, nil, err
	}
	raw, err := rpc.Dispense(plugin.KeyScoring)
	if err != nil {
		return c, nil, err
	}
	return c, raw.(pluginpb.ScoringClient), nil
}
