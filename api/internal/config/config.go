// Package config parses the process environment into one typed Config struct.
// It imports stdlib + the env parser only; nothing else in the tree configures itself.
// A parse failure reports every missing/invalid variable at once (see Load).
package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config is the fully-resolved configuration. Field docs point at the env var.
// The complete reference lives in docs/v0.1/10-deployment.md.
type Config struct {
	BaseURL    string `env:"OSCTF_BASE_URL,required"` // public origin
	PublicHost string `env:"OSCTF_PUBLIC_HOST"`       // host used in connection info; derived if empty
	HTTPAddr   string `env:"OSCTF_HTTP_ADDR" envDefault:":8080"`

	DatabaseURL string `env:"OSCTF_DATABASE_URL,required"`
	RedisURL    string `env:"OSCTF_REDIS_URL,required"`

	S3Endpoint  string `env:"OSCTF_S3_ENDPOINT,required"`
	S3AccessKey string `env:"OSCTF_S3_ACCESS_KEY,required"`
	S3SecretKey string `env:"OSCTF_S3_SECRET_KEY,required"`
	S3Bucket    string `env:"OSCTF_S3_BUCKET" envDefault:"osctf"`
	S3UseSSL    bool   `env:"OSCTF_S3_USE_SSL" envDefault:"false"`

	// Admin seed credentials: required on first boot, validated by the seeder
	// (not at parse time, so `platform migrate` runs without them).
	AdminEmail    string `env:"OSCTF_ADMIN_EMAIL"`
	AdminPassword string `env:"OSCTF_ADMIN_PASSWORD"`

	SessionTTL       time.Duration `env:"OSCTF_SESSION_TTL" envDefault:"168h"`
	RegistrationOpen bool          `env:"OSCTF_REGISTRATION_OPEN" envDefault:"true"`
	TeamMaxSize      int           `env:"OSCTF_TEAM_MAX_SIZE" envDefault:"4"`
	MaxAttachmentMB  int           `env:"OSCTF_MAX_ATTACHMENT_MB" envDefault:"100"`

	PortRangeStart int `env:"OSCTF_PORT_RANGE_START" envDefault:"30000"`
	PortRangeEnd   int `env:"OSCTF_PORT_RANGE_END" envDefault:"32767"`

	// Per-team instance scheduler (v0.2). See docs/v0.2/08-deployment.md.
	InstanceTTL       time.Duration `env:"OSCTF_INSTANCE_TTL" envDefault:"3600s"`      // default per-team TTL
	InstanceExtend    time.Duration `env:"OSCTF_INSTANCE_EXTEND" envDefault:"1800s"`   // added per Extend
	InstanceMaxTTL    time.Duration `env:"OSCTF_INSTANCE_MAX_TTL" envDefault:"14400s"` // max total lifetime
	TeamInstanceQuota int           `env:"OSCTF_TEAM_INSTANCE_QUOTA" envDefault:"3"`   // concurrent per team
	FlagPrefix        string        `env:"OSCTF_FLAG_PREFIX" envDefault:"osctf"`       // per-instance flag prefix

	DockerHost   string `env:"OSCTF_DOCKER_HOST"`
	SeedExamples bool   `env:"OSCTF_SEED_EXAMPLES" envDefault:"true"`
	ExamplesDir  string `env:"OSCTF_EXAMPLES_DIR" envDefault:"examples"`
	TrustProxy   bool   `env:"OSCTF_TRUST_PROXY" envDefault:"false"`

	CORSDevOrigin string `env:"OSCTF_CORS_DEV_ORIGIN"`

	LogFormat string `env:"OSCTF_LOG_FORMAT" envDefault:"json"`
	LogLevel  string `env:"OSCTF_LOG_LEVEL" envDefault:"info"`

	// baseURL is the parsed form of BaseURL, populated by Load.
	baseURL *url.URL
}

// Load parses the environment into a Config, applies derived defaults, and
// validates cross-field constraints. Every problem found is returned in a single
// error so the operator can fix them all at once.
func Load() (*Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := cfg.finalize(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) finalize() error {
	var problems []string

	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		problems = append(problems, "OSCTF_BASE_URL must be an absolute URL like http://localhost:8080")
	} else {
		c.baseURL = u
		if c.PublicHost == "" {
			c.PublicHost = u.Hostname()
		}
	}

	if c.PortRangeStart < 1 || c.PortRangeEnd > 65535 || c.PortRangeStart > c.PortRangeEnd {
		problems = append(problems, "OSCTF_PORT_RANGE_START/END must satisfy 1 <= start <= end <= 65535")
	}
	if c.InstanceTTL < 0 {
		problems = append(problems, "OSCTF_INSTANCE_TTL must be >= 0")
	}
	if c.InstanceExtend <= 0 {
		problems = append(problems, "OSCTF_INSTANCE_EXTEND must be > 0")
	}
	if c.InstanceMaxTTL < c.InstanceTTL || c.InstanceMaxTTL < c.InstanceExtend {
		problems = append(problems, "OSCTF_INSTANCE_MAX_TTL must be >= OSCTF_INSTANCE_TTL and >= OSCTF_INSTANCE_EXTEND")
	}
	if c.TeamInstanceQuota < 1 {
		problems = append(problems, "OSCTF_TEAM_INSTANCE_QUOTA must be >= 1")
	}
	if c.TeamMaxSize < 1 {
		problems = append(problems, "OSCTF_TEAM_MAX_SIZE must be >= 1")
	}
	if c.MaxAttachmentMB < 1 {
		problems = append(problems, "OSCTF_MAX_ATTACHMENT_MB must be >= 1")
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		problems = append(problems, "OSCTF_LOG_FORMAT must be 'json' or 'text'")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, "OSCTF_LOG_LEVEL must be one of debug|info|warn|error")
	}

	if len(problems) > 0 {
		return fmt.Errorf("config: invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// BaseURL parsing helpers ------------------------------------------------------

// IsHTTPS reports whether the public base URL uses TLS (drives the cookie Secure flag).
func (c *Config) IsHTTPS() bool {
	return c.baseURL != nil && c.baseURL.Scheme == "https"
}

// BaseOrigin returns scheme://host of the base URL, used by the CSRF origin check.
func (c *Config) BaseOrigin() string {
	if c.baseURL == nil {
		return ""
	}
	return c.baseURL.Scheme + "://" + c.baseURL.Host
}
