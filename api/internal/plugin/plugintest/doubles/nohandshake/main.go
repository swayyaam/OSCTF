// Double: NEVER HANDSHAKES — a valid executable that starts and then blocks forever without
// calling plugin.Serve, so the go-plugin handshake never completes and the loader's launch is
// stuck until its StartTimeout. It exists to prove boot does not gate serving: a manifest
// pointing at a binary that never handshakes must leave the core answering while its supervisor
// sits in `launching`.
package main

func main() { select {} }
