// Double: SLOW BUT COOPERATIVE — a long call like `slow`, except it HONORS ctx cancellation:
// on cancel it returns promptly with codes.Canceled instead of running to completion. It is the
// companion to `slow` (which ignores ctx): the drain's cancel-then-kill path only means anything
// against a plugin that actually cooperates, so without this double that path is asserted by
// nothing — the dormant-code gap. It is also the positive example the author kit points to for
// "your plugin must honour ctx cancellation."
package main

import (
	"context"
	"time"

	"google.golang.org/grpc/status"

	"github.com/swayyaam/OSCTF/internal/plugin/pluginpb"
	"github.com/swayyaam/OSCTF/internal/plugin/plugintest"
)

type slowCoop struct{ plugintest.OKScoring }

func (s slowCoop) Value(ctx context.Context, req *pluginpb.ScoreRequest) (*pluginpb.ScoreResponse, error) {
	select {
	case <-ctx.Done(): // cooperative: return at once when the host cancels (drain)
		return nil, status.FromContextError(ctx.Err()).Err()
	case <-time.After(4 * time.Second):
		return s.OKScoring.Value(ctx, req)
	}
}

func main() { plugintest.ServeScoring(slowCoop{plugintest.OKScoring{Name: "slowcoop"}}) }
