// Double: WRONG ABI MAJOR — serves correctly but with a go-plugin ProtocolVersion the host
// does not speak. The handshake must refuse it BEFORE any call (no partial init), and the
// loader logs an ABI-major mismatch and skips it.
package main

import (
	"github.com/swayyaam/OSCTF/internal/plugin"
	"github.com/swayyaam/OSCTF/internal/plugin/plugintest"
)

func main() {
	hs := plugin.Handshake
	hs.ProtocolVersion = plugin.ABIMajor + 1 // a major the host refuses
	plugintest.ServeScoringHandshake(plugintest.OKScoring{Name: "wrongabi"}, hs)
}
