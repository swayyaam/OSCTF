// env-hunter proves the platform injects FLAG at runtime: the image ships with
// no flag baked in; /debug?var=FLAG reads it from the environment. The listing
// endpoint hides FLAG, so players must reason about the injection contract.
package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "<h1>Env service</h1><p>Try <code>/debug?var=PATH</code>. The listing hides some names.</p>")
	})
	mux.HandleFunc("/env", func(w http.ResponseWriter, _ *http.Request) {
		for _, e := range os.Environ() {
			name := strings.SplitN(e, "=", 2)[0]
			if name == "FLAG" {
				continue // hidden from the listing on purpose
			}
			fmt.Fprintln(w, name)
		}
	})
	mux.HandleFunc("/debug", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("var")
		if name == "" {
			fmt.Fprintln(w, "usage: /debug?var=NAME")
			return
		}
		fmt.Fprintf(w, "%s=%s\n", name, os.Getenv(name))
	})

	addr := ":8000"
	fmt.Println("listening on", addr)
	server := &http.Server{Addr: addr, Handler: mux}
	_ = server.ListenAndServe()
}
