package abi

// PluginConfigEnv is the single environment variable the host sets on the PLUGIN process carrying
// its resolved config as a JSON object. It is the SHARED definition: the host writes it (see
// internal/plugin) and the public SDK's Config() reads it, so the two sides cannot drift — the
// pairing is one source of truth, not two strings that must agree. One var (not per-key) so a
// plugin never reconstructs the host's OSCTF_PLUGIN_<NAME>_<KEY> override names — it just asks for
// a key.
const PluginConfigEnv = "OSCTF_PLUGIN_CONFIG"
