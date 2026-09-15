// Package config loads gateway configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration. Sensitive values must never be logged.
type Config struct {
	ListenAddr          string
	MetricsAddr         string
	DataDir             string
	PublicBaseURL       string
	AdminUsername       string
	AdminPassword       string // empty means "generate one at startup"
	OnlineThreshold     time.Duration
	RateLimit           time.Duration
	RateLimitBurst      int
	MetricsAllowedCIDRs []*net.IPNet
	LogLevel            slog.Level
}

// DBPath returns the SQLite database path inside DataDir.
func (c *Config) DBPath() string { return c.DataDir + "/netprobe.db" }

// Load reads configuration from the environment, applying defaults.
// It returns an error rather than silently falling back on a malformed value:
// a gateway that starts with the wrong rate limit is worse than one that refuses to start.
func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:    envOr("LISTEN_ADDR", "0.0.0.0:8080"),
		MetricsAddr:   envOr("METRICS_ADDR", "127.0.0.1:9090"),
		DataDir:       envOr("DATA_DIR", "/data"),
		PublicBaseURL: envOr("PUBLIC_BASE_URL", ""),
		AdminUsername: envOr("ADMIN_USERNAME", "admin"),
		AdminPassword: envOr("ADMIN_PASSWORD", ""),
	}

	var err error
	if cfg.OnlineThreshold, err = durationEnv("ONLINE_THRESHOLD", 30*time.Second); err != nil {
		return nil, err
	}
	if cfg.RateLimit, err = durationEnv("RATE_LIMIT", 5*time.Second); err != nil {
		return nil, err
	}
	if cfg.RateLimit <= 0 {
		return nil, fmt.Errorf("config: RATE_LIMIT must be positive, got %s", cfg.RateLimit)
	}

	burst := envOr("RATE_LIMIT_BURST", "3")
	cfg.RateLimitBurst, err = strconv.Atoi(burst)
	if err != nil {
		return nil, fmt.Errorf("config: RATE_LIMIT_BURST %q is not an integer", burst)
	}
	if cfg.RateLimitBurst < 1 {
		return nil, fmt.Errorf("config: RATE_LIMIT_BURST must be >= 1, got %d", cfg.RateLimitBurst)
	}

	if cfg.MetricsAllowedCIDRs, err = parseCIDRs(envOr("METRICS_ALLOWED_CIDRS", "")); err != nil {
		return nil, err
	}
	if cfg.LogLevel, err = parseLevel(envOr("LOG_LEVEL", "info")); err != nil {
		return nil, err
	}
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("config: DATA_DIR must not be empty")
	}
	return cfg, nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func durationEnv(key string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s %q is not a valid duration: %w", key, raw, err)
	}
	return d, nil
}

func parseCIDRs(raw string) ([]*net.IPNet, error) {
	if raw == "" {
		return nil, nil
	}
	var out []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("config: METRICS_ALLOWED_CIDRS entry %q is not a valid CIDR: %w", part, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func parseLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(raw) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("config: LOG_LEVEL %q is not one of debug|info|warn|error", raw)
	}
}
