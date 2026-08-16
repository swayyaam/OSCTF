package plugintest_test

import (
	"context"
	"testing"

	"github.com/osctf/platform/internal/plugin/pluginpb"
	"github.com/osctf/platform/internal/plugin/plugintest"
)

// End-to-end proof that a plugin receives its config through the public sdk.Config(): the host
// launches the plugin with the resolved config in OSCTF_PLUGIN_CONFIG (plugin.PluginConfigEnv), and
// the plugin reads it back via sdk.Config().Int(...). Because the helper writes the SAME const the
// SDK reads, this also pins the pairing structurally — not by a comment on two strings.
func TestPluginReceivesConfigViaSDK(t *testing.T) {
	bin := plugintest.Build(t, "configecho")

	t.Run("config reaches the plugin", func(t *testing.T) {
		c, sc := plugintest.DialWithConfig(t, bin, map[string]string{"bonus": "7"})
		defer c.Kill()
		resp, err := sc.Value(context.Background(), &pluginpb.ScoreRequest{Initial: 100})
		if err != nil {
			t.Fatalf("Value: %v", err)
		}
		if resp.GetValue() != 107 {
			t.Errorf("Value = %d, want 107 (100 + config bonus 7) — config did not reach the plugin via sdk.Config()", resp.GetValue())
		}
	})

	t.Run("no config -> zero, no crash", func(t *testing.T) {
		c, sc := plugintest.DialWithConfig(t, bin, nil)
		defer c.Kill()
		resp, err := sc.Value(context.Background(), &pluginpb.ScoreRequest{Initial: 100})
		if err != nil {
			t.Fatalf("Value: %v", err)
		}
		if resp.GetValue() != 100 {
			t.Errorf("Value = %d, want 100 (no config → bonus 0)", resp.GetValue())
		}
	})
}
