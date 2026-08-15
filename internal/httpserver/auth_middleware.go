package httpserver

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/httpx"
)

// sessionMiddleware resolves the session cookie into an Identity on the context.
// Missing/invalid sessions pass through anonymously — endpoint guards decide.
func sessionMiddleware(sessions *auth.SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(auth.CookieName)
			if err == nil && cookie.Value != "" {
				if sess, serr := sessions.Get(r.Context(), cookie.Value); serr == nil {
					r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{
						UserID:       sess.UserID,
						Role:         sess.Role,
						SessionToken: sess.Token,
						Method:       auth.AuthSession,
					}))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// originCheckMiddleware enforces the CSRF origin rule on mutating methods:
// the Origin (fallback Referer) header must match the deployment origin.
// Requests with neither header are rejected (docs/v0.1/06-auth.md).
func originCheckMiddleware(baseOrigin, devOrigin string) func(http.Handler) http.Handler {
	allowed := map[string]struct{}{baseOrigin: {}}
	if devOrigin != "" {
		allowed[devOrigin] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			// A bearer-authenticated request skips the CSRF origin check: the credential is a
			// deliberate Authorization header, not an ambient cookie, so it cannot be driven
			// cross-site (a browser won't attach it, and CORS gates any cross-origin attempt).
			if id, ok := auth.IdentityFrom(r.Context()); ok && id.Method == auth.AuthToken {
				next.ServeHTTP(w, r)
				return
			}
			origin := r.Header.Get("Origin")
			if origin == "" {
				if ref := r.Header.Get("Referer"); ref != "" {
					if u, err := url.Parse(ref); err == nil {
						origin = u.Scheme + "://" + u.Host
					}
				}
			}
			if _, ok := allowed[strings.TrimSuffix(origin, "/")]; !ok {
				httpx.WriteProblem(w, r, httpx.Problem{
					Type:   "https://osctf.dev/errors/forbidden",
					Title:  "Forbidden",
					Status: http.StatusForbidden,
					Detail: "origin check failed",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// corsDevMiddleware allows the Vite dev origin when configured. Production
// deployments are same-origin and never set OSCTF_CORS_DEV_ORIGIN.
func corsDevMiddleware(devOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if devOrigin == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Origin") == devOrigin {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", devOrigin)
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type")
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
