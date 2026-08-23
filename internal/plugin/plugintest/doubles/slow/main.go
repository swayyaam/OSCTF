// Double: SLOW — responds correctly, every time, in 4 seconds, and deliberately IGNORES the
// request context. It passes every health check and never trips quarantine; it just degrades
// under load. The most valuable double: it is the shape of the scheduler-mutex-across-deploy
// and unbounded-argon2 bugs (correct, just slow). Ignoring the ctx is intentional — it proves
// the HOST-side deadline fires without relying on the plugin cooperating.
package main

import (
	"context"
	"time"

	"github.com/swayyaam/OSCTF/internal/plugin/pluginpb"
	"github.com/swayyaam/OSCTF/internal/plugin/plugintest"
)

type slow struct{ plugintest.OKScoring }

func (s slow) Value(ctx context.Context, req *pluginpb.ScoreRequest) (*pluginpb.ScoreResponse, error) {
	time.Sleep(4 * time.Second) // no ctx check: a non-cooperative slow plugin
	return s.OKScoring.Value(ctx, req)
}

func main() { plugintest.ServeScoring(slow{plugintest.OKScoring{Name: "slow"}}) }
