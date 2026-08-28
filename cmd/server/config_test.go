package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	c, err := Load("") // 纯默认值
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":7865" {
		t.Fatalf("listen=%s", c.Listen)
	}
	if c.DefaultModel != "glm-5.2" {
		t.Fatalf("default_model=%s", c.DefaultModel)
	}
	if c.Upstream.TimeoutSeconds != 120 {
		t.Fatalf("timeout=%d", c.Upstream.TimeoutSeconds)
	}
	if c.SoftRateDur != 60*time.Second {
		t.Fatalf("soft=%s", c.SoftRateDur)
	}
	if c.ErrCooldownDur != 10*time.Minute {
		t.Fatalf("errcool=%s", c.ErrCooldownDur)
	}
	if !c.Quota.Enabled || c.Quota.ApplyMethod != "register" || c.Quota.ApplyThreshold != 50 {
		t.Fatalf("quota=%+v", c.Quota)
	}
}

func TestLoadMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.json")
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
	if !strings.Contains(err.Error(), "config file not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadJSONOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := `{
	  "listen": "9999",
	  "default_model": "deepseek-v4-flash",
	  "cooldown": {"soft_rate": "30s", "err_threshold": 5, "err_cooldown": "2m"},
	  "quota": {"enabled": false, "poll_minutes": 3, "apply_threshold": 10, "apply_method": "campaign", "apply_cooldown_hours": 1, "register_on_start": false},
	  "upstream": {"timeout_seconds": 45}
	}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9999" { // 自动补冒号
		t.Fatalf("listen=%s", c.Listen)
	}
	if c.DefaultModel != "deepseek-v4-flash" {
		t.Fatalf("model=%s", c.DefaultModel)
	}
	if c.SoftRateDur != 30*time.Second || c.ErrCooldownDur != 2*time.Minute || c.Cooldown.ErrThresh != 5 {
		t.Fatalf("cooldown=%+v dur=%s/%s", c.Cooldown, c.SoftRateDur, c.ErrCooldownDur)
	}
	if c.Upstream.TimeoutSeconds != 45 {
		t.Fatalf("timeout=%d", c.Upstream.TimeoutSeconds)
	}
	if c.Quota.Enabled || c.Quota.PollMinutes != 3 || c.Quota.ApplyThreshold != 10 || c.Quota.ApplyMethod != "campaign" {
		t.Fatalf("quota=%+v", c.Quota)
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("CP2A_API_KEY", "secret-key")
	t.Setenv("CP2A_LISTEN", ":8000")
	t.Setenv("CP2A_DEFAULT_MODEL", "deepseek-v4-flash")
	t.Setenv("CP2A_QUOTA_ENABLED", "false")
	t.Setenv("CP2A_QUOTA_THRESHOLD", "7")
	t.Setenv("CP2A_QUOTA_METHOD", "none")
	t.Setenv("CP2A_QUOTA_POLL_MINUTES", "21")

	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "secret-key" {
		t.Fatalf("api_key=%q", c.APIKey)
	}
	if c.Listen != ":8000" || c.DefaultModel != "deepseek-v4-flash" {
		t.Fatalf("listen=%s model=%s", c.Listen, c.DefaultModel)
	}
	if c.Quota.Enabled || c.Quota.ApplyThreshold != 7 || c.Quota.ApplyMethod != "none" || c.Quota.PollMinutes != 21 {
		t.Fatalf("quota=%+v", c.Quota)
	}
}

func TestNormalizeBadDuration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"cooldown":{"soft_rate":"not-a-duration"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for bad soft_rate")
	}
}

func TestBadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{invalid`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected parse error")
	}
}
