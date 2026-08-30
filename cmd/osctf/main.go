// Command osctf is the OSCTF client CLI: a pure API-v1 client for operating a deployment from a
// terminal, a script, or an agent. It is separate from the `platform` server binary on purpose —
// the server stays dependency-light, and this holds no privileged path.
package main

import "os"

func main() {
	err := newRootCmd().Execute()
	if err != nil {
		newPrinter(g.json).fail(err)
	}
	os.Exit(codeOf(err))
}
