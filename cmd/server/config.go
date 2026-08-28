package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 顶层配置。
type Config struct {
	Listen       string `json:"listen"`
	APIKey       string `json:"-"` // 只读 env CP2A_API_KEY
	AuthDir      string `json:"auth_dir"`
	StateFile    string `json:"state_file"`
	DefaultModel string `json:"default_model"`

	Cooldown struct {
		SoftRate    string `json:"soft_rate"`
		ErrThresh   int    `json:"err_threshold"`
		ErrCooldown string `json:"err_cooldown"`
	} `json:"cooldown"`

	Quota struct {
		Enabled           bool   `json:"enabled"`
		PollMinutes       int    `json:"poll_minutes"`
		ApplyThreshold    int64  `json:"apply_threshold"`
		ApplyMethod       string `json:"apply_method"`
		ApplyCooldownHour int    `json:"apply_cooldown_hours"`
		RegisterOnStart   bool   `json:"register_on_start"`
		// AutoRenew token 自动续期：剩余 < RenewThresholdHours 时发起 OAuth。
		AutoRenew          bool `json:"auto_renew"`
		RenewThresholdHours int  `json:"renew_threshold_hours"` // 默认 6
	} `json:"quota"`

	Upstream struct {
		TimeoutSeconds int `json:"timeout_seconds"`
	} `json:"upstream"`

	// 解析后的 duration
	SoftRateDur    time.Duration
	ErrCooldownDur time.Duration
}

// Default 默认配置。
func Default() *Config {
	c := &Config{
		Listen:       ":7865",
		AuthDir:      "./auths",
		StateFile:    "./data/state.json",
		DefaultModel: "glm-5.2",
	}
	c.Cooldown.SoftRate = "60s"
	c.Cooldown.ErrThresh = 3
	c.Cooldown.ErrCooldown = "10m"
	c.Quota.Enabled = true
	c.Quota.PollMinutes = 10
	c.Quota.ApplyThreshold = 50
	c.Quota.ApplyMethod = "register"
	c.Quota.ApplyCooldownHour = 6
	c.Quota.RegisterOnStart = true
	c.Quota.AutoRenew = true              // 默认开启自动续期
	c.Quota.RenewThresholdHours = 6       // 剩余 6h 触发续期
	c.Upstream.TimeoutSeconds = 120
	return c
}

// Load 加载配置 + CP2A_* env 覆盖。
// path 为空时使用纯默认值；path 指定的文件必须存在，否则报错。
func Load(path string) (*Config, error) {
	c := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("config file not found: %s (copy config.example.json to config.json first)", path)
			}
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := json.Unmarshal(raw, c); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}
	applyEnv(c)
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return c, nil
}

func applyEnv(c *Config) {
	if v := os.Getenv("CP2A_API_KEY"); v != "" {
		c.APIKey = v
	}
	if v := os.Getenv("CP2A_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("CP2A_AUTH_DIR"); v != "" {
		c.AuthDir = v
	}
	if v := os.Getenv("CP2A_STATE_FILE"); v != "" {
		c.StateFile = v
	}
	if v := os.Getenv("CP2A_DEFAULT_MODEL"); v != "" {
		c.DefaultModel = v
	}
	if v := os.Getenv("CP2A_QUOTA_ENABLED"); v != "" {
		c.Quota.Enabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("CP2A_QUOTA_POLL_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Quota.PollMinutes = n
		}
	}
	if v := os.Getenv("CP2A_QUOTA_THRESHOLD"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.Quota.ApplyThreshold = n
		}
	}
	if v := os.Getenv("CP2A_QUOTA_METHOD"); v != "" {
		c.Quota.ApplyMethod = v
	}
	if v := os.Getenv("CP2A_QUOTA_COOLDOWN_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Quota.ApplyCooldownHour = n
		}
	}
	if v := os.Getenv("CP2A_QUOTA_REGISTER_ON_START"); v != "" {
		c.Quota.RegisterOnStart = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("CP2A_QUOTA_AUTO_RENEW"); v != "" {
		c.Quota.AutoRenew = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("CP2A_QUOTA_RENEW_THRESHOLD_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Quota.RenewThresholdHours = n
		}
	}
}

func (c *Config) normalize() error {
	var err error
	if c.SoftRateDur, err = time.ParseDuration(c.Cooldown.SoftRate); err != nil {
		return fmt.Errorf("cooldown.soft_rate: %w", err)
	}
	if c.ErrCooldownDur, err = time.ParseDuration(c.Cooldown.ErrCooldown); err != nil {
		return fmt.Errorf("cooldown.err_cooldown: %w", err)
	}
	if c.Cooldown.ErrThresh <= 0 {
		c.Cooldown.ErrThresh = 3
	}
	if c.Upstream.TimeoutSeconds <= 0 {
		c.Upstream.TimeoutSeconds = 120
	}
	if c.DefaultModel == "" {
		c.DefaultModel = "auto"
	}
	if c.Listen == "" {
		c.Listen = ":7865"
	}
	if !strings.HasPrefix(c.Listen, ":") && !strings.Contains(c.Listen, ":") {
		c.Listen = ":" + c.Listen
	}
	if c.Quota.ApplyThreshold <= 0 {
		c.Quota.ApplyThreshold = 50
	}
	if c.Quota.PollMinutes <= 0 {
		c.Quota.PollMinutes = 10
	}
	if c.Quota.ApplyCooldownHour <= 0 {
		c.Quota.ApplyCooldownHour = 6
	}
	if c.Quota.RenewThresholdHours <= 0 {
		c.Quota.RenewThresholdHours = 6
	}
	if c.Quota.ApplyMethod == "" {
		c.Quota.ApplyMethod = "register"
	}
	return nil
}
