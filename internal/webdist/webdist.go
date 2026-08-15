// Package webdist serves the React SPA. In production it is compiled with the
// `embed_spa` build tag and serves the SPA embedded into the binary; without the
// tag (local `go run`) it serves a placeholder pointing at the Vite dev server.
// The static/ directory is gitignored and populated at image-build time.
package webdist

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const devPlaceholder = `<!doctype html>
<html><head><meta charset="utf-8"><title>OSCTF (dev)</title></head>
<body style="font-family:ui-sans-serif,system-ui;max-width:40rem;margin:4rem auto;line-height:1.6">
<h1>OSCTF API is running</h1>
<p>The dashboard is not embedded in this build. During development run the Vite dev
server and open it instead:</p>
<pre>make dev-web   # http://localhost:5173</pre>
<p>Production images build with <code>-tags embed_spa</code> and serve the SPA from here.</p>
</body></html>`

// Handler serves embedded SPA files with an index.html fallback for client-side
// routes. /api/* is routed before this handler and never falls through.
func Handler() http.Handler {
	if !Embedded {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(devPlaceholder))
		})
	}
	fileServer := http.FileServerFS(FS)
	index, _ := fs.ReadFile(FS, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if clean == "." || clean == "" {
			clean = "index.html"
		}
		if _, err := fs.Stat(FS, clean); err != nil {
			// Unknown path: hand it to the client router via index.html.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(index)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
