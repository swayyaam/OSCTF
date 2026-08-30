package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is ~/.config/osctf/config.yaml: named contexts, like kubectl. A context is a target
// plus the credential for it, so switching events is one flag rather than re-authenticating.
type Config struct {
	CurrentContext string             `yaml:"current-context"`
	Contexts       map[string]Context `yaml:"contexts"`
}

// Context is one deployment this CLI can talk to.
type Context struct {
	URL string `yaml:"url"`
	// Token is stored here ONLY when the OS keychain is unavailable; the file is then written
	// 0600 and `login` says so out loud. It is never printed back.
	Token string `yaml:"token,omitempty"`
	// KeychainRef marks that the token lives in the OS keychain under this context's name.
	KeychainRef bool `yaml:"keychain,omitempty"`
}

func configDir() (string, error) {
	if d := os.Getenv("OSCTF_CONFIG_DIR"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating the config directory: %w", err)
	}
	return filepath.Join(base, "osctf"), nil
}

func configPath() (string, error) {
	d, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.yaml"), nil
}

// loadConfig reads the config, treating "absent" as "empty" — a first run is not an error.
func loadConfig() (Config, error) {
	p, err := configPath()
	if err != nil {
		return Config{}, err
	}
	raw, err := os.ReadFile(p) //nolint:gosec // the user's own config path
	if errors.Is(err, os.ErrNotExist) {
		return Config{Contexts: map[string]Context{}}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading %s: %w", p, err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", p, err)
	}
	if c.Contexts == nil {
		c.Contexts = map[string]Context{}
	}
	return c, nil
}

// saveConfig writes the config 0600 — it may hold a token when no keychain is available.
func saveConfig(c Config) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	raw, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", p, err)
	}
	return nil
}

// target is the resolved {url, token} a command will use.
type target struct {
	url   string
	token string
	// from records where the credential came from, for `whoami` and for error messages that
	// would otherwise leave someone guessing which of four sources won.
	from string
}

// resolveTarget applies the documented precedence: explicit flags → environment → the selected
// context. Each source is all-or-nothing per field, so a flag URL with an env token is legal and
// unsurprising.
func resolveTarget(flagURL, flagToken, flagContext string) (target, error) {
	t := target{url: flagURL, token: flagToken, from: "flags"}

	if t.url == "" {
		if v := os.Getenv("OSCTF_URL"); v != "" {
			t.url, t.from = v, "environment"
		}
	}
	if t.token == "" {
		if v := os.Getenv("OSCTF_TOKEN"); v != "" {
			t.token, t.from = v, "environment"
		}
	}
	if t.url != "" && t.token != "" {
		return t, nil
	}

	cfg, err := loadConfig()
	if err != nil {
		return target{}, err
	}
	name := flagContext
	if name == "" {
		name = os.Getenv("OSCTF_CONTEXT")
	}
	if name == "" {
		name = cfg.CurrentContext
	}
	if name == "" {
		if t.url == "" {
			return target{}, errf(exitAuth, "no target configured: pass --url, set OSCTF_URL, or run `osctf login`")
		}
		return t, nil
	}
	ctx, ok := cfg.Contexts[name]
	if !ok {
		return target{}, errf(exitUsage, "no such context %q (see `osctf context list`)", name)
	}
	if t.url == "" {
		t.url, t.from = ctx.URL, "context "+name
	}
	if t.token == "" {
		tok, terr := readToken(name, ctx)
		if terr != nil {
			return target{}, terr
		}
		t.token, t.from = tok, "context "+name
	}
	return t, nil
}

// normalizeURL trims a trailing slash so joining paths is unambiguous.
func normalizeURL(u string) string { return strings.TrimRight(u, "/") }
