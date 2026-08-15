package httpserver

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProxyMisconfigWarn: the warning fires exactly once when TrustProxy is off but a
// forwarded header arrives, and stays silent when there is no forwarded header or when
// TrustProxy is on (the configured, trusted case).
func TestProxyMisconfigWarn(t *testing.T) {
	chain := func(trustProxy bool) (http.Handler, *bytes.Buffer) {
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
		next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
		return proxyMisconfigWarn(log, trustProxy)(next), &buf
	}
	call := func(h http.Handler, xff string) {
		r := httptest.NewRequest(http.MethodGet, "/api/v0/event", nil)
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
	}

	h, buf := chain(false)
	call(h, "203.0.113.9")
	call(h, "203.0.113.9")
	if got := strings.Count(buf.String(), "OSCTF_TRUST_PROXY is off"); got != 1 {
		t.Fatalf("warnings = %d, want exactly 1 (warn once, not per request)", got)
	}

	h2, buf2 := chain(false)
	call(h2, "")
	if buf2.Len() != 0 {
		t.Errorf("warned without a forwarded header: %s", buf2.String())
	}

	h3, buf3 := chain(true)
	call(h3, "203.0.113.9")
	if buf3.Len() != 0 {
		t.Errorf("warned while TrustProxy is on (trusted case): %s", buf3.String())
	}
}
