// Package httpx holds transport helpers shared across the HTTP layer: request-ID
// context plumbing, JSON writing, and the single problem+json error translator.
// It imports apperr and stdlib only.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/osctf/platform/internal/apperr"
)

// ctxKey is unexported to keep the request-ID key private to this package.
type ctxKey int

const requestIDKey ctxKey = iota

// WithRequestID stores the request ID in the context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID returns the request ID from the context, or "" if absent.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// Problem is the RFC 9457 body. Shape matches the OpenAPI Problem schema.
type Problem struct {
	Type      string              `json:"type"`
	Title     string              `json:"title"`
	Status    int                 `json:"status"`
	Detail    string              `json:"detail,omitempty"`
	Errors    map[string][]string `json:"errors,omitempty"`
	RequestID string              `json:"request_id,omitempty"`
}

const problemBase = "https://osctf.dev/errors/"

// WriteJSON serializes v as application/json with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// WriteProblem writes a problem+json body.
func WriteProblem(w http.ResponseWriter, r *http.Request, p Problem) {
	p.RequestID = RequestID(r.Context())
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// RenderError is the one place domain errors become HTTP responses.
// Unknown errors become a generic 500 with detail only in the server log.
func RenderError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	switch e := classify(err); e.typ {
	case "validation":
		var v *apperr.Validation
		errors.As(err, &v)
		WriteProblem(w, r, Problem{
			Type: problemBase + "validation", Title: "Validation failed",
			Status: http.StatusUnprocessableEntity,
			Detail: "One or more fields are invalid.",
			Errors: v.Fields,
		})
	case "rate-limited":
		var rl *apperr.RateLimited
		errors.As(err, &rl)
		secs := int(rl.RetryAfter.Seconds())
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		WriteProblem(w, r, Problem{
			Type: problemBase + "rate-limited", Title: "Too many requests",
			Status: http.StatusTooManyRequests, Detail: e.detail,
		})
	default:
		if e.status == http.StatusInternalServerError {
			log.Error("unhandled error", "error", err.Error(), "request_id", RequestID(r.Context()))
		}
		if e.retryAfter > 0 {
			secs := int(e.retryAfter.Seconds())
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		}
		WriteProblem(w, r, Problem{
			Type: problemBase + e.typ, Title: e.title,
			Status: e.status, Detail: e.detail,
		})
	}
}

type classified struct {
	typ        string
	title      string
	status     int
	detail     string
	retryAfter time.Duration
}

func classify(err error) classified {
	var v *apperr.Validation
	var rl *apperr.RateLimited
	var fb *apperr.Forbidden
	var cf *apperr.Conflict
	var nf *apperr.NotFound
	switch {
	case errors.As(err, &v):
		return classified{typ: "validation", status: http.StatusUnprocessableEntity}
	case errors.As(err, &rl):
		return classified{typ: "rate-limited", status: http.StatusTooManyRequests, detail: "Slow down and retry shortly."}
	case errors.As(err, &nf):
		return classified{typ: "not-found", title: "Not found", status: http.StatusNotFound, detail: nf.Detail}
	case errors.Is(err, apperr.ErrNotFound):
		return classified{typ: "not-found", title: "Not found", status: http.StatusNotFound, detail: "Resource not found."}
	case errors.As(err, &fb):
		return classified{typ: "forbidden", title: "Forbidden", status: http.StatusForbidden, detail: fb.Detail}
	case errors.Is(err, apperr.ErrForbidden):
		return classified{typ: "forbidden", title: "Forbidden", status: http.StatusForbidden, detail: "You are not allowed to do that."}
	case errors.As(err, &cf):
		return classified{typ: "conflict", title: "Conflict", status: http.StatusConflict, detail: cf.Detail}
	case errors.Is(err, apperr.ErrConflict):
		return classified{typ: "conflict", title: "Conflict", status: http.StatusConflict, detail: "Conflicting request."}
	case errors.Is(err, apperr.ErrUnauthenticated):
		return classified{typ: "unauthenticated", title: "Authentication required", status: http.StatusUnauthorized, detail: "You must be signed in."}
	case errors.Is(err, apperr.ErrBadRequest):
		return classified{typ: "bad-request", title: "Bad request", status: http.StatusBadRequest, detail: "The request could not be understood."}
	case errors.Is(err, apperr.ErrUnavailable):
		var ua *apperr.Unavailable
		detail := "A dependency is temporarily unavailable."
		var ra time.Duration
		if errors.As(err, &ua) {
			detail = ua.Detail
			ra = ua.RetryAfter
		}
		return classified{typ: "unavailable", title: "Service unavailable", status: http.StatusServiceUnavailable, detail: detail, retryAfter: ra}
	case errors.Is(err, apperr.ErrNotImplemented):
		var ni *apperr.NotImplemented
		detail := "This endpoint is not implemented yet."
		if errors.As(err, &ni) {
			detail = ni.Detail
		}
		return classified{typ: "not-implemented", title: "Not implemented", status: http.StatusNotImplemented, detail: detail}
	default:
		return classified{typ: "internal", title: "Internal server error", status: http.StatusInternalServerError, detail: "Something broke. Reference the request id when reporting this."}
	}
}
