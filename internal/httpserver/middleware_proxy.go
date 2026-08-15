package httpserver

import (
	"log/slog"
	"net/http"
	"sync"
)

// proxyMisconfigWarn logs once if OSCTF_TRUST_PROXY is off yet a forwarded header
// arrives — a strong sign the platform sits behind a reverse proxy whose client IPs are
// being ignored. In that state every request keys to the proxy's socket address, so the
// per-IP rate limits and the WebSocket per-client caps collapse into one shared bucket
// for the entire event. (A proxy cannot be detected before traffic arrives, so this is a
// first-request warning rather than a startup one.)
//
// The opposite misconfiguration — TrustProxy ON with no trusted proxy in front — is not
// auto-detectable: a client could then forge X-Forwarded-For to dodge its own limit or
// exhaust another IP's. Only enable OSCTF_TRUST_PROXY when a proxy you control sets the
// header (see docs/v0.1/10-deployment.md).
func proxyMisconfigWarn(log *slog.Logger, trustProxy bool) func(http.Handler) http.Handler {
	var once sync.Once
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !trustProxy && log != nil {
				if xff := r.Header.Get("X-Forwarded-For"); xff != "" || r.Header.Get("X-Real-IP") != "" {
					once.Do(func() {
						log.Warn("OSCTF_TRUST_PROXY is off but a forwarded header was received — the platform appears to be behind a reverse proxy. Per-IP rate limits and WebSocket caps will attribute every request to the proxy IP (one shared bucket for the whole event). Set OSCTF_TRUST_PROXY=true only if a proxy you control sets X-Forwarded-For.",
							"x_forwarded_for", xff, "remote_addr", r.RemoteAddr)
					})
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
