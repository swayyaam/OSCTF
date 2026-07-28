// hardening-demo serves its flag at / and lets players observe the v0.2 runtime
// hardening: a read-only rootfs with only /tmp and declared writable_paths (/data)
// writable, dropped capabilities, and no network egress. The /write endpoint tries
// to create a file at a caller-chosen path and reports success or the OS error.
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
		fmt.Fprintf(w, `<h1>Locked Down</h1>
<p>Flag: <code>%s</code></p>
<p>Try <code>/write?path=/etc/passwd</code> (read-only, fails) vs
<code>/write?path=/tmp/x</code> or <code>/write?path=/data/x</code> (writable).</p>`, flag())
	})
	mux.HandleFunc("/write", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		if path == "" {
			http.Error(w, "usage: /write?path=/some/file", http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(path, []byte("hardening-demo\n"), 0o600); err != nil {
			fmt.Fprintf(w, "write %s: FAILED (%v)\n", path, err)
			return
		}
		fmt.Fprintf(w, "write %s: OK\n", path)
	})

	addr := ":8080"
	fmt.Println("listening on", addr)
	srv := &http.Server{Addr: addr, Handler: mux}
	_ = srv.ListenAndServe()
}
