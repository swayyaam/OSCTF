// per-team-web serves a per-team, per-instance flag. The flag is NOT baked into
// the image; the platform injects a flag UNIQUE to each team's instance as the
// FLAG environment variable. The service reads it from the environment and hides
// it behind a small, discoverable step. Runs read-only-rootfs: it only writes to
// /tmp.
package main

import (
	"fmt"
	"net/http"
	"os"
)

func flag() string {
	f := os.Getenv("FLAG")
	if f == "" {
		f = "OSCTF{flag_not_injected}"
	}
	return f
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintln(w, `<h1>Team Portal</h1>
<p>Welcome to your team's private instance. The flag is not on this page.</p>
<p>Endpoints: <code>/notes</code>, <code>/flag</code>.</p>`)
	})
	// A "chatty" endpoint that leaks how to reach the flag (the intended step).
	mux.HandleFunc("/notes", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "TODO: remove debug route before launch — GET /flag?debug=1 dumps the secret")
	})
	// The flag is gated behind a trivial-but-required parameter so it isn't handed
	// out on a bare GET. Each team's instance returns ITS OWN flag.
	mux.HandleFunc("/flag", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("debug") != "1" {
			http.Error(w, "forbidden — read the notes", http.StatusForbidden)
			return
		}
		fmt.Fprintln(w, flag())
	})

	addr := ":8000"
	fmt.Println("listening on", addr)
	srv := &http.Server{Addr: addr, Handler: mux}
	_ = srv.ListenAndServe()
}
