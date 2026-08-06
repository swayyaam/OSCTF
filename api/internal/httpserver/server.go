// Package httpserver assembles the chi router: middleware stack, operational
// endpoints (/healthz, /readyz, /metrics), the mounted /api/v0 handler, and the
// embedded SPA fallback. It may import everything below it in the tree.
package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/handlers"
	"github.com/osctf/platform/internal/httpx"
	"github.com/osctf/platform/internal/metrics"
	"github.com/osctf/platform/internal/redisx"
	"github.com/osctf/platform/internal/webdist"
)

// ReadyFunc reports readiness: it returns a map of component name -> failure
// reason. An empty map means everything is reachable.
type ReadyFunc func(ctx context.Context) map[string]string

// Deps are the inputs to New.
type Deps struct {
	Log *slog.Logger
	// Handlers implements the generated API surface. Nil mounts a 501 catch-all.
	Handlers *handlers.Server
	Ready    ReadyFunc
	// Sessions resolves cookies to identities; nil skips session handling.
	Sessions *auth.SessionStore
	// BaseOrigin is the deployment origin for the CSRF check (required when
	// Sessions is set). CORSDevOrigin optionally allows the Vite dev server.
	BaseOrigin    string
	CORSDevOrigin string
	// WSHandler serves GET /api/{v0,v1}/ws (the live scoreboard socket). Nil disables it.
	WSHandler http.Handler
	// V0Sunset is the date advertised in the /api/v0 Sunset header (RFC 8594). The zero
	// value still emits a header (an ancient date); production passes cfg.APIV0Sunset.
	V0Sunset time.Time
	// Tokens resolves `Authorization: Bearer` API tokens; nil disables token auth.
	Tokens *auth.TokenService
	// Limiter rate-limits token requests by token identity; nil disables that limit.
	Limiter         *redisx.Limiter
	TokenRateBurst  int
	TokenRateWindow time.Duration
	// TrustProxy mirrors OSCTF_TRUST_PROXY. When false the server warns once if it
	// receives a forwarded header, since a proxy is likely in front but its client IPs
	// are being ignored (every request would then key to the proxy IP).
	TrustProxy bool
}

// New builds the top-level HTTP handler.
func New(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(requestID)
	r.Use(recoverer(d.Log))
	r.Use(observability(d.Log))
	r.Use(securityHeaders)
	r.Use(proxyMisconfigWarn(d.Log, d.TrustProxy))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	})

	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		var failing map[string]string
		if d.Ready != nil {
			failing = d.Ready(r.Context())
		}
		if len(failing) == 0 {
			httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
			return
		}
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":  "not ready",
			"failing": failing,
		})
	})

	r.Handle("/metrics", metrics.Handler())

	var api http.Handler
	if d.Handlers != nil {
		api = newAPIHandler(d.Handlers, d.Log)
	} else {
		api = http.HandlerFunc(notImplemented)
	}
	// The WebSocket lives at /api/v0/ws — not modelled in OpenAPI — so special-case
	// it ahead of the generated handler (paths here are already /api/v0-stripped).
	if d.WSHandler != nil {
		generated := api
		ws := d.WSHandler
		api = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ws" {
				ws.ServeHTTP(w, r)
				return
			}
			generated.ServeHTTP(w, r)
		})
	}
	if d.CORSDevOrigin != "" {
		api = corsDevMiddleware(d.CORSDevOrigin)(api)
	}
	if d.BaseOrigin != "" {
		api = originCheckMiddleware(d.BaseOrigin, d.CORSDevOrigin)(api)
	}
	// Token auth wraps the origin check (bearer requests skip CSRF) but is wrapped BY the
	// session middleware (a resolved session takes precedence over a presented token).
	if d.Tokens != nil {
		api = tokenMiddleware(d.Tokens, d.Limiter, d.TokenRateBurst, d.TokenRateWindow)(api)
	}
	if d.Sessions != nil {
		api = sessionMiddleware(d.Sessions)(api)
	}
	// /api/v1 is the canonical, semver-governed surface; /api/v0 is a DEPRECATED ALIAS that
	// serves the exact same `api` handler value — there is no second handler set that could
	// drift — and adds Deprecation/Sunset headers. Both strip their prefix before the shared
	// handler, so the WS special-case (`/ws`) and every route resolve identically on each.
	r.Mount("/api/v1", http.StripPrefix("/api/v1", api))
	r.Mount("/api/v0", http.StripPrefix("/api/v0", deprecatedAlias(d.V0Sunset, api)))

	// Anything under /api that didn't match the mount is a 404 problem, never SPA.
	r.HandleFunc("/api/*", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   "https://osctf.dev/errors/not-found",
			Title:  "Not found",
			Status: http.StatusNotFound,
			Detail: "No such API endpoint.",
		})
	})

	// Everything else is the SPA.
	r.NotFound(webdist.Handler().ServeHTTP)
	r.Handle("/*", webdist.Handler())

	return r
}

// deprecatedAlias wraps the /api/v0 mount so every response signals the deprecation:
// `Deprecation: true` (the alias is deprecated in favour of /api/v1) and a `Sunset` date
// (RFC 8594) after which v0 may be removed. The headers are set before the handler runs, so
// they ride even error responses. /api/v1 is wrapped by nothing and carries neither.
func deprecatedAlias(sunset time.Time, next http.Handler) http.Handler {
	sunsetHeader := sunset.UTC().Format(http.TimeFormat)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "true")
		w.Header().Set("Sunset", sunsetHeader)
		next.ServeHTTP(w, r)
	})
}

func notImplemented(w http.ResponseWriter, r *http.Request) {
	httpx.WriteProblem(w, r, httpx.Problem{
		Type:   "https://osctf.dev/errors/internal",
		Title:  "Not implemented",
		Status: http.StatusNotImplemented,
		Detail: "This endpoint is not implemented yet.",
	})
}
