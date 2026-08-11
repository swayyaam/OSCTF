package plugin

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is a plugin's plugin.yaml. Names map to registry keys (auth provider id, scoring
// mode, challenge-type id), so identity is validated strictly and a collision fails loudly.
type Manifest struct {
	Name        string               `yaml:"name"`
	Type        string               `yaml:"type"`
	ABI         string               `yaml:"abi"`
	Version     string               `yaml:"version"`
	Executable  string               `yaml:"executable"`
	Description string               `yaml:"description"`
	Override    bool                 `yaml:"override"` // opt-in to replace a protected built-in key
	Config      map[string]ConfigKey `yaml:"config"`
}

// ConfigKey declares one config value's type and resolution rules. Secret keys resolve ONLY from
// env; a value in the manifest is a validation error (secrets never live in a file the plugin
// dir might expose).
type ConfigKey struct {
	Type     string `yaml:"type"`
	Required bool   `yaml:"required"`
	Secret   bool   `yaml:"secret"`
	Default  string `yaml:"default"`
}

// pluginTypes are the four dispense keys a manifest `type` may name.
var pluginTypes = map[string]string{
	"auth":           KeyAuth,
	"scoring":        KeyScoring,
	"notification":   KeyNotification,
	"challenge_type": KeyChallengeType,
}

var (
	nameRE       = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	configTypeOK = map[string]bool{"string": true, "int": true, "bool": true}
)

// parseManifest unmarshals plugin.yaml bytes (strictly — unknown fields are rejected so a typo'd
// key does not silently no-op).
func parseManifest(data []byte) (Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	return m, nil
}

// dispenseKey returns the go-plugin dispense key for the manifest's type (valid only after
// validate).
func (m Manifest) dispenseKey() string { return pluginTypes[m.Type] }

// validate checks a manifest in isolation (identity, type, ABI major, config/secret rules). It
// does NOT check the executable on disk (that needs the plugin dir — done in discovery) nor
// cross-plugin name collisions (that needs the whole set). Fails the plugin, never the host.
func (m Manifest) validate() error {
	if !nameRE.MatchString(m.Name) {
		return fmt.Errorf("name %q must match ^[a-z0-9]+(-[a-z0-9]+)*$", m.Name)
	}
	if _, ok := pluginTypes[m.Type]; !ok {
		return fmt.Errorf("type %q is not one of auth/scoring/notification/challenge_type", m.Type)
	}
	if err := checkABIMajor(m.ABI); err != nil {
		return err
	}
	if strings.TrimSpace(m.Executable) == "" {
		return fmt.Errorf("executable is required")
	}
	for key, c := range m.Config {
		if c.Type != "" && !configTypeOK[c.Type] {
			return fmt.Errorf("config %q: type %q must be string/int/bool", key, c.Type)
		}
		if c.Secret && c.Default != "" {
			return fmt.Errorf("config %q: a secret key must not carry a value in the manifest (env-only)", key)
		}
	}
	return nil
}

// checkABIMajor requires the manifest's ABI major to equal the host's (a minor difference is
// forward-compatible and accepted).
func checkABIMajor(abi string) error {
	major := abi
	if i := strings.IndexByte(abi, '.'); i >= 0 {
		major = abi[:i]
	}
	n, err := strconv.Atoi(strings.TrimSpace(major))
	if err != nil {
		return fmt.Errorf("abi %q is not a valid major.minor", abi)
	}
	if n != ABIMajor {
		return fmt.Errorf("abi major %d != host ABI major %d", n, ABIMajor)
	}
	return nil
}
