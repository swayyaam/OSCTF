package plugin

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/osctf/platform/internal/metrics"
)

func sinkTo(buf *bytes.Buffer, name string, now func() time.Time) *pluginStderrSink {
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return newPluginStderrSink(name, log, now)
}

// A plugin's hclog-JSON line is re-emitted through the host log, TAGGED source=plugin + plugin=name,
// with the level and fields preserved — so an operator can tell it from the platform's own lines.
func TestPluginStderrSinkTagsAndPreservesLevel(t *testing.T) {
	var buf bytes.Buffer
	s := sinkTo(&buf, "webhook", func() time.Time { return time.Unix(0, 0) })
	s.Write([]byte(`{"@level":"error","@message":"delivery failed","url":"x"}` + "\n"))
	out := buf.String()
	for _, want := range []string{`"msg":"delivery failed"`, `"source":"plugin"`, `"plugin":"webhook"`, `"level":"ERROR"`, `"url":"x"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in host log line: %s", want, out)
		}
	}
}

// A plugin logging past its budget is dropped and counted — the host is protected from a flood.
func TestPluginStderrSinkRateLimitsAndCounts(t *testing.T) {
	var buf bytes.Buffer
	now := time.Unix(0, 0)
	s := sinkTo(&buf, "p", func() time.Time { return now }) // frozen clock: only the burst gets through
	before := metrics.CounterValue(metrics.PluginLogDropped)
	line := []byte(`{"@level":"info","@message":"m"}` + "\n")
	for i := 0; i < defaultPluginLogBurst+10; i++ {
		s.Write(line)
	}
	if dropped := metrics.CounterValue(metrics.PluginLogDropped) - before; dropped != 10 {
		t.Errorf("dropped %v lines past the burst, want 10", dropped)
	}
}

func TestPluginStderrSinkTruncatesOverLong(t *testing.T) {
	var buf bytes.Buffer
	s := sinkTo(&buf, "p", func() time.Time { return time.Unix(0, 0) })
	long := `{"@level":"info","@message":"` + strings.Repeat("x", maxPluginLogLine) + `"}`
	s.Write(append([]byte(long), '\n'))
	if !strings.Contains(buf.String(), truncatedSuffix) {
		t.Error("over-long line was not truncated")
	}
}

// A non-JSON line (plain text, or a go-plugin internal line) is not dropped for being unstructured.
func TestPluginStderrSinkNonJSONBecomesInfo(t *testing.T) {
	var buf bytes.Buffer
	s := sinkTo(&buf, "p", func() time.Time { return time.Unix(0, 0) })
	s.Write([]byte("a plain line\n"))
	if !strings.Contains(buf.String(), "a plain line") {
		t.Error("non-JSON plugin line was dropped")
	}
}
