package sdk

import (
	"encoding/json"
	"os"
	"strconv"
	"sync"

	"github.com/swayyaam/OSCTF/internal/plugin"
)

// configEnv is the environment variable the host sets on the plugin process carrying its resolved
// config. It is the SAME const the host writes (plugin.PluginConfigEnv) — one shared definition, so
// the read side and the write side cannot drift. Used only internally here, so no internal type
// crosses the public surface.
const configEnv = plugin.PluginConfigEnv

// PluginConfig is the plugin's resolved configuration: the manifest's declared keys plus any
// OSCTF_PLUGIN_<NAME>_<KEY> environment overrides (env wins; secrets come from env only). The host
// validates it against the manifest schema BEFORE the plugin launches — a required key is present
// and typed values parse — so a plugin that reads its config can trust it.
type PluginConfig struct {
	values map[string]string
}

var (
	cfgOnce   sync.Once
	cfgLoaded *PluginConfig
)

// Config returns the plugin's configuration, parsed once from the host-provided environment. Use
// it inside your Serve impl, e.g. sdk.Config().String("webhook_url").
func Config() *PluginConfig {
	cfgOnce.Do(func() {
		cfgLoaded = &PluginConfig{values: parseConfigEnv(os.Getenv(configEnv))}
	})
	return cfgLoaded
}

func parseConfigEnv(s string) map[string]string {
	m := map[string]string{}
	if s != "" {
		_ = json.Unmarshal([]byte(s), &m)
	}
	return m
}

// String returns the value for key, or "" if it was not provided.
func (c *PluginConfig) String(key string) string { return c.values[key] }

// StringOr returns the value for key, or def if it was not provided.
func (c *PluginConfig) StringOr(key, def string) string {
	if v, ok := c.values[key]; ok {
		return v
	}
	return def
}

// Has reports whether key was provided.
func (c *PluginConfig) Has(key string) bool { _, ok := c.values[key]; return ok }

// Int returns key parsed as an int, or 0 if unset. The host validated the declared type, so a
// declared int key parses.
func (c *PluginConfig) Int(key string) int {
	n, _ := strconv.Atoi(c.values[key])
	return n
}

// Bool returns key parsed as a bool, or false if unset.
func (c *PluginConfig) Bool(key string) bool {
	b, _ := strconv.ParseBool(c.values[key])
	return b
}
