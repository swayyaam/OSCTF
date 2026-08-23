package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/swayyaam/OSCTF/internal/metrics"
)

// maxPluginLogLine truncates an over-long plugin log line before it reaches host logs — a plugin
// must not be able to emit a megabyte log entry into the operator's logs.
const maxPluginLogLine = 8 * 1024

// Per-plugin log budget: a token bucket allowing a burst then a steady rate. Per-plugin (not
// global) so one chatty or crash-looping plugin cannot starve another's budget OR flood the host.
const (
	defaultPluginLogBurst  = 200
	defaultPluginLogPerSec = 20
	truncatedSuffix        = "…[truncated]"
)

// pluginStderrSink is the per-plugin io.Writer wired to go-plugin's SyncStderr (the plugin redirects
// its os.Stderr over a gRPC stdio stream to here — that is the plugin→host log path). It line-buffers
// the stream, and for each line: RATE-LIMITS (drops + counts past the budget, so a plugin cannot
// flood/degrade the host through a channel meant for its benefit), TRUNCATES over-long lines, parses
// the plugin's hclog-JSON, and re-emits it through the host slog TAGGED source=plugin, plugin=<name>
// at the plugin's own level — so plugin output joins the host log, clearly attributed and bounded.
type pluginStderrSink struct {
	name string
	log  *slog.Logger

	mu     sync.Mutex
	buf    []byte
	tokens float64
	max    float64
	perSec float64
	last   time.Time
	now    func() time.Time
}

func newPluginStderrSink(name string, log *slog.Logger, now func() time.Time) *pluginStderrSink {
	if now == nil {
		now = time.Now
	}
	return &pluginStderrSink{
		name: name, log: log, now: now,
		tokens: defaultPluginLogBurst, max: defaultPluginLogBurst, perSec: defaultPluginLogPerSec, last: now(),
	}
}

func (s *pluginStderrSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.buf = append(s.buf, p...)
	var lines [][]byte
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		line := make([]byte, i)
		copy(line, s.buf[:i])
		lines = append(lines, line)
		s.buf = s.buf[i+1:]
	}
	s.mu.Unlock()
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		s.emit(line)
	}
	return len(p), nil
}

func (s *pluginStderrSink) emit(line []byte) {
	if !s.allow() {
		metrics.PluginLogDropped.Inc()
		return
	}
	if len(line) > maxPluginLogLine {
		line = append(line[:maxPluginLogLine:maxPluginLogLine], []byte(truncatedSuffix)...)
	}
	level, msg, attrs := parsePluginLine(line)
	// source=plugin + plugin=<name> is the distinct tag: an operator can tell a plugin's line from
	// the platform's own. The plugin's fields follow.
	head := []slog.Attr{slog.String("source", "plugin"), slog.String("plugin", s.name)}
	s.log.LogAttrs(context.Background(), level, msg, append(head, attrs...)...)
}

// parsePluginLine reads a plugin hclog-JSON line into a level, message, and the remaining fields.
// A non-JSON line (a plugin that wrote plain text, or a go-plugin internal line) becomes an Info
// message carrying the raw text — never dropped for being unstructured.
func parsePluginLine(line []byte) (slog.Level, string, []slog.Attr) {
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		return slog.LevelInfo, string(line), nil
	}
	level := slog.LevelInfo
	if lv, ok := m["@level"].(string); ok {
		level = mapLevel(lv)
	}
	msg, _ := m["@message"].(string)
	var attrs []slog.Attr
	for k, v := range m {
		switch k {
		case "@level", "@message", "@timestamp", "@module":
			// dropped: level/message are hoisted; timestamp/module are the host's to stamp
		default:
			attrs = append(attrs, slog.Any(k, v))
		}
	}
	return level, msg, attrs
}

func mapLevel(s string) slog.Level {
	switch s {
	case "trace", "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func (s *pluginStderrSink) allow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.now()
	s.tokens += t.Sub(s.last).Seconds() * s.perSec
	if s.tokens > s.max {
		s.tokens = s.max
	}
	s.last = t
	if s.tokens < 1 {
		return false
	}
	s.tokens--
	return true
}

// interface assertion — the sink is what go-plugin's SyncStderr expects.
var _ io.Writer = (*pluginStderrSink)(nil)
