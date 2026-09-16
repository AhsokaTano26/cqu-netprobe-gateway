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
	ListenAddr      string
	MetricsAddr     string
	DataDir         string
	PublicBaseURL   string
	AdminUsername   string
	AdminPassword   string // empty means "generate one at startup"
	OnlineThreshold time.Duration
	RateLimit       time.Duration
	RateLimitBurst  int
	// RegisterLimit is how often one IP may create a probe through the public
	// registration page. Much looser than RateLimit: a person registers once or
	// twice, so this exists to stop scripts filling the probes table, not to
	// shape normal traffic.
	RegisterLimit      time.Duration
	RegisterLimitBurst int
	// MaxProbes caps how many probes the public registration page will create.
	// The register limiter bounds how fast one address can create them; this
	// bounds how many can exist, which is what protects Prometheus cardinality
	// over a semester. It does not apply to probes an administrator creates: a
	// full table must not stop the person who can empty it.
	MaxProbes           int
	MetricsAllowedCIDRs []*net.IPNet
	// TrustedProxyCIDRs are the networks whose X-Forwarded-For is believed.
	// Empty — the default — means the header is ignored and the peer address is
	// used, which is correct for a deployment with no proxy in front.
	TrustedProxyCIDRs []*net.IPNet
	LogLevel          slog.Level
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
	if cfg.OnlineThreshold <= 0 {
		return nil, fmt.Errorf("config: ONLINE_THRESHOLD must be positive, got %s", cfg.OnlineThreshold)
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
		return nil, fmt.Errorf("config: RATE_LIMIT_BURST %q is not an integer: %w", burst, err)
	}
	if cfg.RateLimitBurst < 1 {
		return nil, fmt.Errorf("config: RATE_LIMIT_BURST must be >= 1, got %d", cfg.RateLimitBurst)
	}

	if cfg.RegisterLimit, err = durationEnv("REGISTER_LIMIT", time.Hour); err != nil {
		return nil, err
	}
	if cfg.RegisterLimit <= 0 {
		return nil, fmt.Errorf("config: REGISTER_LIMIT must be positive, got %s", cfg.RegisterLimit)
	}

	regBurst := envOr("REGISTER_LIMIT_BURST", "3")
	cfg.RegisterLimitBurst, err = strconv.Atoi(regBurst)
	if err != nil {
		return nil, fmt.Errorf("config: REGISTER_LIMIT_BURST %q is not an integer: %w", regBurst, err)
	}
	if cfg.RegisterLimitBurst < 1 {
		return nil, fmt.Errorf("config: REGISTER_LIMIT_BURST must be >= 1, got %d", cfg.RegisterLimitBurst)
	}

	maxProbes := envOr("MAX_PROBES", "500")
	cfg.MaxProbes, err = strconv.Atoi(maxProbes)
	if err != nil {
		return nil, fmt.Errorf("config: MAX_PROBES %q is not an integer: %w", maxProbes, err)
	}
	if cfg.MaxProbes < 1 {
		return nil, fmt.Errorf("config: MAX_PROBES must be >= 1, got %d", cfg.MaxProbes)
	}

	if cfg.MetricsAllowedCIDRs, err = parseCIDRs(envOr("METRICS_ALLOWED_CIDRS", "")); err != nil {
		return nil, err
	}
	if cfg.TrustedProxyCIDRs, err = parseCIDRs(envOr("TRUSTED_PROXY_CIDRS", "")); err != nil {
		return nil, err
	}
	for _, n := range cfg.TrustedProxyCIDRs {
		// A catch-all here is always a mistake and a dangerous one: it makes
		// X-Forwarded-For authoritative for every caller, so anyone who can
		// reach the port directly picks their own source address — and every
		// limit keyed on it (token guessing, registration, the metrics
		// allowlist) stops meaning anything. Refusing to start says so; a
		// silently spoofable gateway does not.
		if ones, bits := n.Mask.Size(); ones == 0 && bits != 0 {
			return nil, fmt.Errorf(
				"config: TRUSTED_PROXY_CIDRS must not contain %s: it would trust X-Forwarded-For from anyone, letting callers choose their own source address. List the proxy's actual addresses.", n)
		}
	}
	if cfg.LogLevel, err = parseLevel(envOr("LOG_LEVEL", "info")); err != nil {
		return nil, err
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
