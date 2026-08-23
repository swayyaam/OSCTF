// Double: MALFORMED — serves Info fine but returns a gRPC error status on Value (and an
// out-of-contract Info name mismatch is available via NAME). Proves the host maps a plugin
// error to a domain error and does not trust the response.
package main

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/swayyaam/OSCTF/internal/plugin/pluginpb"
	"github.com/swayyaam/OSCTF/internal/plugin/plugintest"
)

type malformed struct{ plugintest.OKScoring }

func (malformed) Value(context.Context, *pluginpb.ScoreRequest) (*pluginpb.ScoreResponse, error) {
	return nil, status.Error(codes.Internal, "boom: malformed plugin")
}

func main() { plugintest.ServeScoring(malformed{plugintest.OKScoring{Name: "malformed"}}) }
