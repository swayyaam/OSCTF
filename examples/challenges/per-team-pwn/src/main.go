// per-team-pwn is a tiny TCP service that hands over the per-instance FLAG once
// the client sends the magic word. The flag is injected by the platform (unique
// per team), never baked into the image. Reads/writes nothing outside the socket,
// so it runs happily on a read-only rootfs.
package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
)

func flag() string {
	f := os.Getenv("FLAG")
	if f == "" {
		f = "OSCTF{flag_not_injected}"
	}
	return f
}

func handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	fmt.Fprintln(conn, "== gatekeeper ==")
	fmt.Fprintln(conn, "Say the magic word to receive your flag.")
	fmt.Fprint(conn, "> ")
	sc := bufio.NewScanner(conn)
	if sc.Scan() {
		word := strings.TrimSpace(sc.Text())
		if strings.EqualFold(word, "please") {
			fmt.Fprintln(conn, flag())
			return
		}
		fmt.Fprintln(conn, "the gate stays shut.")
	}
}

func main() {
	ln, err := net.Listen("tcp", ":9000")
	if err != nil {
		fmt.Println("listen:", err)
		os.Exit(1)
	}
	fmt.Println("listening on :9000")
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go handle(conn)
	}
}
