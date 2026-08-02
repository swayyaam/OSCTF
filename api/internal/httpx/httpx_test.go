package httpx

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/osctf/platform/internal/apperr"
)

// TestRenderUnavailableRetryAfter covers the argon2id hashing gate's shed path:
// an *apperr.Unavailable carrying a RetryAfter must render as 503 with a
// Retry-After header (seconds, rounded up to at least 1).
func TestRenderUnavailableRetryAfter(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantRetry  string // "" ⇒ header must be absent
	}{
		{
			name:       "shed carries Retry-After",
			err:        &apperr.Unavailable{Detail: "busy", RetryAfter: 5 * time.Second},
			wantStatus: http.StatusServiceUnavailable,
			wantRetry:  "5",
		},
		{
			name:       "sub-second rounds up to 1",
			err:        &apperr.Unavailable{Detail: "busy", RetryAfter: 20 * time.Millisecond},
			wantStatus: http.StatusServiceUnavailable,
			wantRetry:  "1",
		},
		{
			name:       "plain unavailable sets no Retry-After",
			err:        &apperr.Unavailable{Detail: "runtime down"},
			wantStatus: http.StatusServiceUnavailable,
			wantRetry:  "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
			RenderError(rec, req, log, c.err)

			if rec.Code != c.wantStatus {
				t.Errorf("status = %d; want %d", rec.Code, c.wantStatus)
			}
			if got := rec.Header().Get("Retry-After"); got != c.wantRetry {
				t.Errorf("Retry-After = %q; want %q", got, c.wantRetry)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q; want application/problem+json", ct)
			}
		})
	}
}
