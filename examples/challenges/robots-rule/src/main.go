// robots-rule: a tiny stateless web app. /robots.txt disallows a secret admin
// path that serves the injected flag. Shared instance — no per-team state.
package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	flag := os.Getenv("FLAG")
	if flag == "" {
		flag = "OSCTF{flag_not_injected}"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "<h1>Definitely Not Hiding Anything</h1><p>Move along.</p>")
	})
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "User-agent: *\nDisallow: /s3cr3t-admin\n")
	})
	mux.HandleFunc("/s3cr3t-admin", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "Welcome, admin. Here is your flag: %s\n", flag)
	})

	addr := ":8000"
	fmt.Println("listening on", addr)
	server := &http.Server{Addr: addr, Handler: mux}
	_ = server.ListenAndServe()
}
