// Package httpserver assembles the chi router: middleware stack, operational
// endpoints (/healthz, /readyz, /metrics), the mounted /api/v0 handler, and the
// embedded SPA fallback. It may import everything below it in the tree.
package httpserver

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/osctf/platform/internal/httpx"
	"github.com/osctf/platform/internal/metrics"
	"github.com/osctf/platform/internal/webdist"
)

// ReadyFunc reports readiness: it returns a map of component name -> failure
// reason. An empty map means everything is reachable.
type ReadyFunc func(ctx context.Context) map[string]string

// Deps are the inputs to New.
type Deps struct {
	Log *slog.Logger
	// APIHandler serves everything under /api/v0. Nil mounts a 501 stub.
	APIHandler http.Handler
	Ready      ReadyFunc
}

// New builds the top-level HTTP handler.
func New(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(requestID)
	r.Use(recoverer(d.Log))
	r.Use(observability(d.Log))
	r.Use(securityHeaders)

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

	api := d.APIHandler
	if api == nil {
		api = http.HandlerFunc(notImplemented)
	}
	r.Mount("/api/v0", http.StripPrefix("/api/v0", api))

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

func notImplemented(w http.ResponseWriter, r *http.Request) {
	httpx.WriteProblem(w, r, httpx.Problem{
		Type:   "https://osctf.dev/errors/internal",
		Title:  "Not implemented",
		Status: http.StatusNotImplemented,
		Detail: "This endpoint is not implemented yet.",
	})
}
