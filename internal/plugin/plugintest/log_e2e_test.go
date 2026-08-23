package plugintest_test

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/swayyaam/OSCTF/internal/plugin/pluginpb"
	"github.com/swayyaam/OSCTF/internal/plugin/plugintest"
)

// syncBuffer is a mutex-guarded buffer — go-plugin's stderr pump writes it from another goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// End-to-end proof of the TRANSPORT: what a plugin logs via sdk.Log() travels over go-plugin's gRPC
// stdio stream to the host's SyncStderr. (The host-side sink then tags + rate-limits + re-emits it
// through the host slog — that logic is unit-tested in the plugin package's pluginStderrSink tests.)
// Delivery is asynchronous, so we poll.
func TestPluginLogReachesHostViaStdio(t *testing.T) {
	bin := plugintest.Build(t, "logecho")

	var out syncBuffer
	c, sc := plugintest.DialCaptureStderr(t, bin, &out)
	defer c.Kill()

	if _, err := sc.Value(context.Background(), &pluginpb.ScoreRequest{Solves: 3}); err != nil {
		t.Fatalf("Value: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), "scored-a-challenge") {
			return // the plugin's sdk.Log line reached the host over the stdio channel
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("plugin log line never reached the host over stdio within 5s; captured: %q", out.String())
}
