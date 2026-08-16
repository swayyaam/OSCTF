package sdk

import (
	"os"
	"sync"

	hclog "github.com/hashicorp/go-hclog"
)

// Logger is the plugin's logger. Its output travels over go-plugin's stderr channel into the
// HOST's logs, tagged as plugin-origin so an operator can tell it from the platform's own lines.
// The surface is deliberately minimal — debug/info/warn/error, a message plus key/value pairs —
// not a logging framework.
//
// DO NOT LOG SECRETS OR PAYLOADS. Everything you log lands in the operator's host logs: your config
// values, an event's data, a submitted flag, a token — none of it belongs in a log line. The SDK
// cannot stop you; this is the rule. The host also rate-limits and truncates plugin log output, so
// a chatty or crash-looping plugin cannot flood or degrade the host through a channel that exists
// for the plugin's benefit — do not rely on every line being kept.
type Logger struct{ h hclog.Logger }

var (
	logOnce sync.Once
	logInst *Logger
)

// Log returns the plugin logger (created once).
func Log() *Logger {
	logOnce.Do(func() {
		logInst = &Logger{h: hclog.New(&hclog.LoggerOptions{
			Output:     os.Stderr, // go-plugin reads this and forwards to the host, tagged by plugin
			JSONFormat: true,      // so the host preserves level + key/value fields
			Level:      hclog.Debug,
		})}
	})
	return logInst
}

// Debug/Info/Warn/Error log at the named level. Args are alternating key, value pairs.
func (l *Logger) Debug(msg string, kv ...any) { l.h.Debug(msg, kv...) }
func (l *Logger) Info(msg string, kv ...any)  { l.h.Info(msg, kv...) }
func (l *Logger) Warn(msg string, kv ...any)  { l.h.Warn(msg, kv...) }
func (l *Logger) Error(msg string, kv ...any) { l.h.Error(msg, kv...) }
