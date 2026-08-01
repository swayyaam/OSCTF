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

func TestLoadInstanceDefaults(t *testing.T) {
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.InstanceTTL != time.Hour {
		t.Errorf("InstanceTTL = %v, want 1h", cfg.InstanceTTL)
	}
	if cfg.InstanceExtend != 30*time.Minute {
		t.Errorf("InstanceExtend = %v, want 30m", cfg.InstanceExtend)
	}
	if cfg.InstanceMaxTTL != 4*time.Hour {
		t.Errorf("InstanceMaxTTL = %v, want 4h", cfg.InstanceMaxTTL)
	}
	if cfg.TeamInstanceQuota != 3 {
		t.Errorf("TeamInstanceQuota = %d, want 3", cfg.TeamInstanceQuota)
	}
	if cfg.InstanceReapAfter != 15*time.Minute {
		t.Errorf("InstanceReapAfter = %v, want 15m", cfg.InstanceReapAfter)
	}
	if cfg.FlagPrefix != "osctf" {
		t.Errorf("FlagPrefix = %q, want osctf", cfg.FlagPrefix)
	}
	if cfg.PortRangeEnd != 32767 {
		t.Errorf("PortRangeEnd = %d, want 32767", cfg.PortRangeEnd)
	}
}

func TestLoadRejectsMaxTTLBelowTTL(t *testing.T) {
	setRequired(t)
	t.Setenv("OSCTF_INSTANCE_TTL", "7200s")
	t.Setenv("OSCTF_INSTANCE_MAX_TTL", "3600s")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted max TTL below TTL")
	}
}

func TestLoadRejectsZeroQuota(t *testing.T) {
	setRequired(t)
	t.Setenv("OSCTF_TEAM_INSTANCE_QUOTA", "0")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted zero instance quota")
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
