package httpserver

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/httpx"
	"github.com/osctf/platform/internal/redisx"
)

// tokenMiddleware resolves an `Authorization: Bearer osctf_pat_…` credential into an Identity
// for non-browser clients. It is deliberately the ONLY place a token is read: never a cookie,
// query parameter, or form field — accepting a token from any ambient channel would silently
// forfeit CSRF protection (which is why bearer requests skip the origin check). A resolved
// session takes precedence; a request presents one credential or the other.
//
// On a valid token it rate-limits by TOKEN IDENTITY (not IP or account) and enforces the
// scope gate before handing off. An invalid/expired/banned token is rejected outright rather
// than falling through to anonymous, since a bearer credential is presented deliberately.
func tokenMiddleware(tokens *auth.TokenService, limiter *redisx.Limiter, burst int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if tokens == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := auth.IdentityFrom(r.Context()); ok {
				next.ServeHTTP(w, r) // a session already authenticated this request
				return
			}
			raw, ok := bearerFromHeader(r)
			if !ok {
				next.ServeHTTP(w, r) // no bearer credential → anonymous
				return
			}
			id, err := tokens.Authenticate(r.Context(), raw, time.Now())
			if err != nil {
				writeTokenAuthError(w, r, err)
				return
			}
			// Rate-limit by the token's identity: automation traffic is legitimately unlike a
			// browser's, and an IP-keyed limit misfires for a CI runner or bot behind shared NAT.
			if limiter != nil && burst > 0 {
				allowed, retryAfter, lerr := limiter.Allow(r.Context(), "token", id.TokenID.String(), burst, window)
				if lerr == nil && !allowed {
					w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
					httpx.WriteProblem(w, r, httpx.Problem{
						Type:   "https://osctf.dev/errors/rate-limited",
						Title:  "Too many requests",
						Status: http.StatusTooManyRequests,
						Detail: "token rate limit exceeded",
					})
					return
				}
			}
			// Scope gate: the token must carry the scope its route class requires. This is the
			// scope half of role ∩ scope — the role gate still runs in the handler, so a scope
			// never grants what the role lacks.
			if !scopeAllows(id.Scopes, r.Method, r.URL.Path) {
				httpx.WriteProblem(w, r, httpx.Problem{
					Type:   "https://osctf.dev/errors/forbidden",
					Title:  "Forbidden",
					Status: http.StatusForbidden,
					Detail: "the token's scopes do not permit this request",
				})
				return
			}
			// last_used_at is advisory; never fail a request on it.
			_ = tokens.Touch(r.Context(), id.TokenID)
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
		})
	}
}

// bearerFromHeader reads a bearer token from the Authorization header ONLY.
func bearerFromHeader(r *http.Request) (string, bool) {
	body, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return "", false
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", false
	}
	return body, true
}

// scopeAllows reports whether the token's scopes permit a request of this class. Paths are
// prefix-stripped here ("/admin/…", "/challenges/…").
func scopeAllows(scopes []string, method, path string) bool {
	req := requiredScope(method, path)
	for _, s := range scopes {
		if s == req {
			return true
		}
	}
	return false
}

func requiredScope(method, path string) string {
	if path == "/admin" || strings.HasPrefix(path, "/admin/") {
		return auth.ScopeAdmin
	}
	switch method {
	case http.MethodGet, http.MethodHead:
		return auth.ScopeRead
	default:
		return auth.ScopeSubmit
	}
}

// writeTokenAuthError maps a token failure to a problem. Credential problems (invalid,
// expired, banned owner) are 401; a scope the server can't understand fails closed as 403.
func writeTokenAuthError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, auth.ErrTokenScope) {
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   "https://osctf.dev/errors/forbidden",
			Title:  "Forbidden",
			Status: http.StatusForbidden,
			Detail: "the token's scopes are not recognized",
		})
		return
	}
	httpx.WriteProblem(w, r, httpx.Problem{
		Type:   "https://osctf.dev/errors/unauthorized",
		Title:  "Unauthorized",
		Status: http.StatusUnauthorized,
		Detail: "invalid or expired token",
	})
}
