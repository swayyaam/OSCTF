// Double: reads its config through the PUBLIC sdk.Config() and reflects it in Value — so the
// host→plugin config path (OSCTF_PLUGIN_CONFIG env → sdk.Config) can be asserted end to end.
package main

import "github.com/swayyaam/OSCTF/plugin/sdk"

type engine struct{}

func (engine) Info() sdk.Info { return sdk.Info{Name: "configecho", Version: "0.0.0"} }

// Value adds the "bonus" config value to Initial, so a caller sees whether the config arrived.
func (engine) Value(in sdk.Score) int { return in.Initial + sdk.Config().Int("bonus") }

func main() { sdk.Serve(sdk.Scoring, engine{}) }
