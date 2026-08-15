package httpx

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP extracts the caller's IP. X-Forwarded-For is honored only when the
// deployment declares a trusted proxy (OSCTF_TRUST_PROXY); the first entry is
// the original client. Otherwise the socket peer address wins.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if ip := net.ParseIP(first); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
