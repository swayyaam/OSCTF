package httpserver

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/osctf/platform/internal/httpx"
	"github.com/osctf/platform/internal/metrics"
)

// statusRecorder captures the response status code for logging and metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer (for WS hijack/flush).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// requestID assigns each request a UUID, echoes it in X-Request-Id, and stores it
// in the context so handlers and the error renderer can correlate.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uuid.NewString()
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(httpx.WithRequestID(r.Context(), id)))
	})
}

// securityHeaders sets the baseline response headers on every response.
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
		"script-src 'self'; connect-src 'self' ws: wss:"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}

// recoverer converts a panic into a 500 problem+json response.
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic recovered",
						"error", rec,
						"request_id", httpx.RequestID(r.Context()),
						"path", r.URL.Path)
					httpx.WriteProblem(w, r, httpx.Problem{
						Type:   "https://osctf.dev/errors/internal",
						Title:  "Internal server error",
						Status: http.StatusInternalServerError,
						Detail: "Something broke. Reference the request id when reporting this.",
					})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// observability logs each request and records HTTP metrics.
func observability(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}
			dur := time.Since(start)
			metrics.HTTPRequests.WithLabelValues(route, r.Method, strconv.Itoa(rec.status)).Inc()
			metrics.HTTPDuration.WithLabelValues(route, r.Method).Observe(dur.Seconds())

			log.Info("request",
				"request_id", httpx.RequestID(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"route", route,
				"status", rec.status,
				"duration_ms", dur.Milliseconds())
		})
	}
}
