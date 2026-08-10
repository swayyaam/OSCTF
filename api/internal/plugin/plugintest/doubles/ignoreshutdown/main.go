// Double: IGNORES SHUTDOWN — serves correctly but traps and ignores SIGINT/SIGTERM, so a
// graceful stop does not make it exit. Forces the loader's drain-then-KILL path (go-plugin
// escalates to SIGKILL) and the reaping that must follow, so a stubborn plugin can't wedge
// shutdown or leak a process.
package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/osctf/platform/internal/plugin/plugintest"
)

func main() {
	// Swallow graceful-stop signals; only SIGKILL (uncatchable) will end this process.
	c := make(chan os.Signal, 4)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for range c {
		}
	}()
	plugintest.ServeScoring(plugintest.OKScoring{Name: "ignoreshutdown"})
}
