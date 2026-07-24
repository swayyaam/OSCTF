// cookie-monster: sets a role=guest cookie; a role=admin cookie reveals the flag.
// Stateless and shared-instance safe (no server-side session).
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
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("role")
		if err != nil {
			http.SetCookie(w, &http.Cookie{Name: "role", Value: "guest", Path: "/"})
			fmt.Fprintln(w, "<h1>Members area</h1><p>You are logged in as a guest.</p>")
			return
		}
		if c.Value == "admin" {
			fmt.Fprintf(w, "<h1>Admin panel</h1><p>Flag: %s</p>", flag)
			return
		}
		fmt.Fprintf(w, "<h1>Members area</h1><p>You are logged in as %q. Only admins see the flag.</p>", c.Value)
	})

	addr := ":8000"
	fmt.Println("listening on", addr)
	server := &http.Server{Addr: addr, Handler: mux}
	_ = server.ListenAndServe()
}
