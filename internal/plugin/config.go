package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// configEnvVar is the HOST-side override name for one config key: OSCTF_PLUGIN_<NAME>_<KEY>,
// upper-cased with every non-alphanumeric rune folded to '_'. This is where an operator sets a
// secret (a secret has no other source) or overrides a manifest default.
func configEnvVar(pluginName, key string) string {
	return "OSCTF_PLUGIN_" + envToken(pluginName) + "_" + envToken(key)
}

func envToken(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// resolveConfig produces a plugin's runtime config from its manifest plus the host environment,
// validated against the declared schema. Rules:
//
//   - Precedence: an OSCTF_PLUGIN_<NAME>_<KEY> env value WINS over the manifest default.
//   - SECRETS resolve from env ONLY (a manifest value for a secret is already a manifest-validate
//     error, so a secret has no default to fall back to).
//   - A missing REQUIRED key, or a value that does not parse for its declared type, returns an
//     error — so the caller quarantines at LOAD, never launching a plugin that would then fail
//     every call because a URL was empty.
//
// The returned map contains only keys that have a value; optional-and-unset keys are omitted.
func resolveConfig(m Manifest, lookupEnv func(string) (string, bool)) (map[string]string, error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	out := map[string]string{}
	keys := make([]string, 0, len(m.Config))
	for k := range m.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic error ordering
	for _, key := range keys {
		decl := m.Config[key]
		env := configEnvVar(m.Name, key)
		val, fromEnv := lookupEnv(env)
		switch {
		case fromEnv:
			// env override — the only source for a secret, and it wins for anything else.
		case decl.Secret:
			if decl.Required {
				return nil, fmt.Errorf("plugin %q: required secret config %q is unset — set %s in the host environment", m.Name, key, env)
			}
			continue // optional secret, unset
		default:
			val = decl.Default
		}
		if val == "" {
			if decl.Required {
				return nil, fmt.Errorf("plugin %q: required config %q has no value — set %s or a manifest default", m.Name, key, env)
			}
			continue // optional, unset
		}
		if err := checkConfigType(decl.Type, val); err != nil {
			if decl.Secret {
				// NEVER echo a secret's value in an error (it lands in logs + the quarantine reason).
				return nil, fmt.Errorf("plugin %q: secret config %q is not a valid %s", m.Name, key, decl.Type)
			}
			return nil, fmt.Errorf("plugin %q: config %q %w", m.Name, key, err)
		}
		out[key] = val
	}
	return out, nil
}

// checkConfigType validates a value against a declared config type (empty type == string).
func checkConfigType(typ, val string) error {
	switch typ {
	case "", "string":
		return nil
	case "int":
		if _, err := strconv.Atoi(val); err != nil {
			return fmt.Errorf("must be an int, got %q", val)
		}
	case "bool":
		if _, err := strconv.ParseBool(val); err != nil {
			return fmt.Errorf("must be a bool, got %q", val)
		}
	}
	return nil
}

// encodeConfig serialises a resolved config map for the plugin's OSCTF_PLUGIN_CONFIG env var.
// json.Marshal sorts map keys, so the encoding is stable across launches (a reload that re-reads
// the same config produces the same bytes — no spurious swap).
func encodeConfig(cfg map[string]string) string {
	if len(cfg) == 0 {
		return ""
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "" // map[string]string never fails to marshal; be defensive rather than panic in launch
	}
	return string(b)
}
