package httpserver

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/handlers"
	"github.com/osctf/platform/internal/httpx"
)

// newAPIHandler mounts the generated strict server over chi with the platform's
// error translation: domain errors (and ErrNotImplemented) become problem+json.
func newAPIHandler(srv *handlers.Server, log *slog.Logger) http.Handler {
	strict := apigen.NewStrictHandlerWithOptions(srv, nil, apigen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			httpx.WriteProblem(w, r, httpx.Problem{
				Type:   "https://osctf.dev/errors/bad-request",
				Title:  "Bad request",
				Status: http.StatusBadRequest,
				Detail: err.Error(),
			})
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, handlers.ErrNotImplemented) {
				httpx.WriteProblem(w, r, httpx.Problem{
					Type:   "https://osctf.dev/errors/internal",
					Title:  "Not implemented",
					Status: http.StatusNotImplemented,
					Detail: "This endpoint is not implemented yet.",
				})
				return
			}
			httpx.RenderError(w, r, log, err)
		},
	})

	r := chi.NewRouter()
	return apigen.HandlerFromMux(strict, r)
}
