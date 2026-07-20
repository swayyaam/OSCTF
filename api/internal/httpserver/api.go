package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	strictnethttp "github.com/oapi-codegen/runtime/strictmiddleware/nethttp"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/handlers"
	"github.com/osctf/platform/internal/httpx"
)

// injectHTTP is a strict middleware exposing the raw writer/request to handlers
// through the context — needed for cookie writes (login/logout) since strict
// handler signatures only carry the context.
func injectHTTP(f strictnethttp.StrictHTTPHandlerFunc, _ string) strictnethttp.StrictHTTPHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		return f(httpx.WithHTTP(ctx, w, r), w, r, request)
	}
}

// newAPIHandler mounts the generated strict server over chi with the platform's
// error translation: domain errors (and ErrNotImplemented) become problem+json.
func newAPIHandler(srv *handlers.Server, log *slog.Logger) http.Handler {
	strict := apigen.NewStrictHandlerWithOptions(srv, []apigen.StrictMiddlewareFunc{injectHTTP}, apigen.StrictHTTPServerOptions{
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
