// Double: well-behaved baseline. Serves correctly and promptly — the control the misbehaving
// doubles are compared against.
package main

import "github.com/osctf/platform/internal/plugin/plugintest"

func main() { plugintest.ServeScoring(plugintest.OKScoring{Name: "goodscore"}) }
