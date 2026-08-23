// Double: SLOW ONLY ON SHUTDOWN — responds normally, but on a stop signal takes far longer
// than the 30s drain window to exit. Exercises the drain-timeout branch: the loader must
// cancel in-flight calls at the deadline, KILL the process, and reclaim its resources rather
// than wait forever — the branch where a resource leak is most likely.
package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/swayyaam/OSCTF/internal/plugin/plugintest"
)

func main() {
	c := make(chan os.Signal, 4)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-c
		time.Sleep(45 * time.Second) // longer than OSCTF_PLUGIN_DRAIN_TIMEOUT (30s) → drain-then-kill
		os.Exit(0)
	}()
	plugintest.ServeScoring(plugintest.OKScoring{Name: "slowshutdown"})
}
