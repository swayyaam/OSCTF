package seed

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ChallengeYAML is the on-disk challenge definition (docs/v0.1/13-example-challenges.md).
// The seeder parses it; the future CLI reuses it for validate/package.
type ChallengeYAML struct {
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

// parseChallengeYAML reads and validates a challenge.yaml. dirName is the
// directory basename, which must equal slug.
func parseChallengeYAML(path, dirName string) (ChallengeYAML, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // seeded paths come from the trusted examples dir
	if err != nil {
		return ChallengeYAML{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var c ChallengeYAML
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return ChallengeYAML{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if c.Kind == "" {
		c.Kind = "standard"
	}
	if c.Scoring == "" {
		c.Scoring = "dynamic"
	}
	if err := c.validate(dirName); err != nil {
		return ChallengeYAML{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c ChallengeYAML) validate(dirName string) error {
	if !slugRe.MatchString(c.Slug) {
		return fmt.Errorf("slug %q is not url-safe", c.Slug)
	}
	if c.Slug != dirName {
		return fmt.Errorf("slug %q must equal the directory name %q", c.Slug, dirName)
	}
	if c.Title == "" || c.Flag == "" {
		return fmt.Errorf("title and flag are required")
	}
	switch c.Kind {
	case "standard":
		if c.Image != "" || c.InternalPort != nil {
			return fmt.Errorf("standard challenges must not set image/internal_port")
		}
	case "container":
		if c.Image == "" || c.InternalPort == nil {
			return fmt.Errorf("container challenges require image and internal_port")
		}
	default:
		return fmt.Errorf("kind %q must be standard or container", c.Kind)
	}
	if c.Instancing != "" && c.Instancing != "shared" && c.Instancing != "per_team" {
		return fmt.Errorf("instancing %q must be shared or per_team", c.Instancing)
	}
	if c.FlagMode != "" && c.FlagMode != "static" && c.FlagMode != "per_instance" {
		return fmt.Errorf("flag_mode %q must be static or per_instance", c.FlagMode)
	}
	if c.Kind != "container" {
		if c.Instancing == "per_team" || c.FlagMode == "per_instance" ||
			c.InstanceTTLSeconds != nil || (c.Egress != nil && !*c.Egress) || len(c.WritablePaths) > 0 {
			return fmt.Errorf("per-team instancing / flag_mode / ttl / egress / writable_paths require kind: container")
		}
	}
	if c.Scoring == "dynamic" && (c.PointsMin == nil || c.Decay == nil) {
		return fmt.Errorf("dynamic scoring requires points_min and decay")
	}
	if c.PointsInitial <= 0 {
		return fmt.Errorf("points_initial must be positive")
	}
	return nil
}
