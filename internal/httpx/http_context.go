package httpx

import (
	"context"
	"net/http"
)

// The strict-server handlers receive only a context; cookie writes (login,
// logout) need the ResponseWriter. A strict middleware stashes both here.

type httpCtxKey int

const httpKey httpCtxKey = iota

// HTTPPair carries the raw request/response of the in-flight call.
type HTTPPair struct {
	W http.ResponseWriter
	R *http.Request
}

// WithHTTP attaches the writer/request pair to the context.
func WithHTTP(ctx context.Context, w http.ResponseWriter, r *http.Request) context.Context {
	return context.WithValue(ctx, httpKey, HTTPPair{W: w, R: r})
}

// HTTPFrom returns the writer/request pair attached by the strict middleware.
func HTTPFrom(ctx context.Context) (HTTPPair, bool) {
	p, ok := ctx.Value(httpKey).(HTTPPair)
	return p, ok
}
