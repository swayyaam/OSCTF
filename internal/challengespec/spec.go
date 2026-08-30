// Package challengespec parses and validates the on-disk challenge definition
// (challenge.yaml, docs/v0.1/13-example-challenges.md).
//
// It is shared on purpose: the SEEDER uses it to load the example challenges, and the `osctf`
// CLI uses it for offline `challenge validate` / `package`. One validator means an author gets
// the same verdict from their laptop with no network that the server would give them — if these
// were two implementations, "valid locally, rejected on create" would be a matter of time.
package challengespec

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidationError is a rule the spec broke, naming the fields responsible.
//
// The message is the SAME text this validator has always produced, so the seeder's output is
// unchanged; Fields is additive, and exists because the CLI's `challenge validate --json`
// promises `field_errors` and a bare sentence cannot be attributed to an input. Validation is
// fail-fast, as it always was: Fields names the fields of the FIRST broken rule, not every
// problem in the file.
type ValidationError struct {
	Fields  []string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// FieldErrors renders the failure as the CLI's field_errors map.
func (e *ValidationError) FieldErrors() map[string][]string {
	out := make(map[string][]string, len(e.Fields))
	for _, f := range e.Fields {
		out[f] = append(out[f], e.Message)
	}
	return out
}

func invalid(fields []string, msg string) error {
	return &ValidationError{Fields: fields, Message: msg}
}

// Spec is the on-disk challenge definition.
type Spec struct {
	Slug                string            `yaml:"slug"`
	Title               string            `yaml:"title"`
	Category            string            `yaml:"category"`
	Difficulty          string            `yaml:"difficulty"`
	Description         string            `yaml:"description"`
	Flag                string            `yaml:"flag"`
	FlagCaseInsensitive bool              `yaml:"flag_case_insensitive"`
	Scoring             string            `yaml:"scoring"`
	PointsInitial       int               `yaml:"points_initial"`
	PointsMin           *int              `yaml:"points_min"`
	Decay               *int              `yaml:"decay"`
	MaxAttempts         *int              `yaml:"max_attempts"`
	Visible             bool              `yaml:"visible"`
	Kind                string            `yaml:"kind"`
	Image               string            `yaml:"image"`
	InternalPort        *int              `yaml:"internal_port"`
	MemLimitMB          *int              `yaml:"mem_limit_mb"`
	CPUMillis           *int              `yaml:"cpu_millis"`
	ConnectionTemplate  string            `yaml:"connection_template"`
	InjectFlag          *bool             `yaml:"inject_flag"`
	ContainerEnv        map[string]string `yaml:"container_env"`
	Files               []string          `yaml:"files"`
	// v0.2 per-team instancing (container-only).
	Instancing         string   `yaml:"instancing"`
	FlagMode           string   `yaml:"flag_mode"`
	InstanceTTLSeconds *int     `yaml:"instance_ttl_seconds"`
	Egress             *bool    `yaml:"egress"`
	WritablePaths      []string `yaml:"writable_paths"`
}

// ParseFile reads and validates a challenge.yaml. dirName is the directory basename, which must
// equal the slug. Errors are wrapped with the path, as the seeder has always reported them.
func ParseFile(path, dirName string) (Spec, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // seeded paths come from the trusted examples dir
	if err != nil {
		return Spec{}, fmt.Errorf("reading %s: %w", path, err)
	}
	c, err := Parse(raw, dirName)
	if err != nil {
		return Spec{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Parse validates an already-read challenge.yaml. It sits alongside ParseFile so the CLI can
// validate bytes it already holds, and so the rules are testable without touching disk.
//
// A ValidationError from here is unwrappable with errors.As, which is how the CLI turns a
// failure into field_errors instead of re-deriving the rules.
func Parse(raw []byte, dirName string) (Spec, error) {
	var c Spec
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return Spec{}, fmt.Errorf("parsing: %w", err)
	}
	if c.Kind == "" {
		c.Kind = "standard"
	}
	if c.Scoring == "" {
		c.Scoring = "dynamic"
	}
	if err := c.Validate(dirName); err != nil {
		return Spec{}, err
	}
	return c, nil
}

func (c Spec) Validate(dirName string) error {
	if !slugRe.MatchString(c.Slug) {
		return invalid([]string{"slug"}, fmt.Sprintf("slug %q is not url-safe", c.Slug))
	}
	if c.Slug != dirName {
		return invalid([]string{"slug"}, fmt.Sprintf("slug %q must equal the directory name %q", c.Slug, dirName))
	}
	if c.Title == "" || c.Flag == "" {
		return invalid([]string{"title", "flag"}, "title and flag are required")
	}
	switch c.Kind {
	case "standard":
		if c.Image != "" || c.InternalPort != nil {
			return invalid([]string{"image", "internal_port"}, "standard challenges must not set image/internal_port")
		}
	case "container":
		if c.Image == "" || c.InternalPort == nil {
			return invalid([]string{"image", "internal_port"}, "container challenges require image and internal_port")
		}
	default:
		return invalid([]string{"kind"}, fmt.Sprintf("kind %q must be standard or container", c.Kind))
	}
	if c.Instancing != "" && c.Instancing != "shared" && c.Instancing != "per_team" {
		return invalid([]string{"instancing"}, fmt.Sprintf("instancing %q must be shared or per_team", c.Instancing))
	}
	if c.FlagMode != "" && c.FlagMode != "static" && c.FlagMode != "per_instance" {
		return invalid([]string{"flag_mode"}, fmt.Sprintf("flag_mode %q must be static or per_instance", c.FlagMode))
	}
	if c.Kind != "container" {
		if c.Instancing == "per_team" || c.FlagMode == "per_instance" ||
			c.InstanceTTLSeconds != nil || (c.Egress != nil && !*c.Egress) || len(c.WritablePaths) > 0 {
			return invalid([]string{"kind"}, "per-team instancing / flag_mode / ttl / egress / writable_paths require kind: container")
		}
	}
	if c.Scoring == "dynamic" && (c.PointsMin == nil || c.Decay == nil) {
		return invalid([]string{"points_min", "decay"}, "dynamic scoring requires points_min and decay")
	}
	if c.PointsInitial <= 0 {
		return invalid([]string{"points_initial"}, "points_initial must be positive")
	}
	return nil
}
