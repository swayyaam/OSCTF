package config

import (
	"testing"
	"time"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("OSCTF_BASE_URL", "http://localhost:8080")
	t.Setenv("OSCTF_DATABASE_URL", "postgres://osctf:osctf@localhost:5432/osctf?sslmode=disable")
	t.Setenv("OSCTF_REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("OSCTF_S3_ENDPOINT", "localhost:9000")
	t.Setenv("OSCTF_S3_ACCESS_KEY", "k")
	t.Setenv("OSCTF_S3_SECRET_KEY", "s")
}

func TestLoadDefaults(t *testing.T) {
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.SessionTTL != 168*time.Hour {
		t.Errorf("SessionTTL = %v, want 168h", cfg.SessionTTL)
	}
	if cfg.TeamMaxSize != 4 {
		t.Errorf("TeamMaxSize = %d, want 4", cfg.TeamMaxSize)
	}
	if cfg.PublicHost != "localhost" {
		t.Errorf("PublicHost = %q, want localhost (derived)", cfg.PublicHost)
	}
	if cfg.IsHTTPS() {
		t.Error("IsHTTPS() = true for http base URL")
	}
	if got := cfg.BaseOrigin(); got != "http://localhost:8080" {
		t.Errorf("BaseOrigin() = %q", got)
	}
}

func TestLoadMissingRequiredReportsAll(t *testing.T) {
	// Nothing set: parse must fail (do not want a silent zero-value config).
	t.Setenv("OSCTF_BASE_URL", "")
	t.Setenv("OSCTF_DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with no required vars set")
	}
}

func TestLoadRejectsBadPortRange(t *testing.T) {
	setRequired(t)
	t.Setenv("OSCTF_PORT_RANGE_START", "40000")
	t.Setenv("OSCTF_PORT_RANGE_END", "30000")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted start > end port range")
	}
}

func TestIsHTTPS(t *testing.T) {
	setRequired(t)
	t.Setenv("OSCTF_BASE_URL", "https://ctf.example.com")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.IsHTTPS() {
		t.Error("IsHTTPS() = false for https base URL")
	}
	if cfg.PublicHost != "ctf.example.com" {
		t.Errorf("PublicHost = %q", cfg.PublicHost)
	}
}
