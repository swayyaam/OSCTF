// Double: CRASH ON LAUNCH — exits non-zero before serving, every time. Under the loader's
// restart policy this becomes a crash-LOOP (retry → crash → retry) that must reach a terminal
// `failed`/quarantine within the cap, never spawning without bound.
package main

import "os"

func main() { os.Exit(1) }
