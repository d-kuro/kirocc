package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/d-kuro/kirocc/internal/logging"
)

const (
	// DefaultOTelBodyLimit is the default max bytes of request body to capture in OTel spans.
	DefaultOTelBodyLimit = 32 * 1024
	// DefaultKeepAliveInterval is the default idle time between SSE keep-alive comments.
	DefaultKeepAliveInterval = 15 * time.Second
)

// Config is the runtime configuration for kirocc.
type Config struct {
	Port              int
	Host              string
	DBPath            string
	APIKey            string
	Debug             bool
	OTel              bool
	OTelBodyLimit     int
	KeepAliveInterval time.Duration
	LogFile           logging.LogFileConfig
}

// DefaultDBPath returns the default kiro-cli SQLite database location.
func DefaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return DefaultDBPathFor(runtime.GOOS, home)
}

// DefaultDBPathFor returns the default database location for the given OS and home directory.
func DefaultDBPathFor(goos, home string) string {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "kiro-cli", "data.sqlite3")
	default:
		return filepath.Join(home, ".local", "share", "kiro-cli", "data.sqlite3")
	}
}

// ApplyEnvOverrides mutates cfg using KIROCC_* environment variables.
func ApplyEnvOverrides(cfg *Config) error {
	applyString("KIROCC_DB_PATH", &cfg.DBPath)
	applyString("KIROCC_API_KEY", &cfg.APIKey)
	applyString("KIROCC_HOST", &cfg.Host)
	if err := applyInt("KIROCC_PORT", &cfg.Port); err != nil {
		return err
	}
	if err := applyBool("KIROCC_DEBUG", &cfg.Debug); err != nil {
		return err
	}
	if err := applyBool("KIROCC_OTEL", &cfg.OTel); err != nil {
		return err
	}
	if err := applyInt("KIROCC_OTEL_BODY_LIMIT", &cfg.OTelBodyLimit); err != nil {
		return err
	}
	if err := applyDuration("KIROCC_KEEPALIVE_INTERVAL", &cfg.KeepAliveInterval); err != nil {
		return err
	}
	applyString("KIROCC_LOG_FILE", &cfg.LogFile.Path)
	if err := applyInt("KIROCC_LOG_MAX_SIZE", &cfg.LogFile.MaxSize); err != nil {
		return err
	}
	if err := applyInt("KIROCC_LOG_MAX_BACKUPS", &cfg.LogFile.MaxBackups); err != nil {
		return err
	}
	if err := applyInt("KIROCC_LOG_MAX_AGE", &cfg.LogFile.MaxAge); err != nil {
		return err
	}
	if err := applyBool("KIROCC_LOG_COMPRESS", &cfg.LogFile.Compress); err != nil {
		return err
	}
	if err := applyBool("KIROCC_LOG_CONSOLE", &cfg.LogFile.Console); err != nil {
		return err
	}
	return nil
}

// Validate checks that the config is internally consistent. Returns an error
// describing the first violation found. Called after flag parsing and env
// overrides, before the server starts.
func (c *Config) Validate() error {
	if c.Host == "" {
		return fmt.Errorf("host must not be empty")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("port must be in 1..65535, got %d", c.Port)
	}
	if c.OTelBodyLimit < 0 {
		return fmt.Errorf("otel-body-limit must be >= 0, got %d", c.OTelBodyLimit)
	}
	if c.KeepAliveInterval != 0 && c.KeepAliveInterval < time.Second {
		return fmt.Errorf("keepalive-interval must be 0 or >= 1s, got %s", c.KeepAliveInterval)
	}
	return nil
}

func applyString(key string, dst *string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

func applyInt(key string, dst *int) error {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid %s=%q: %w", key, v, err)
		}
		*dst = n
	}
	return nil
}

func applyBool(key string, dst *bool) error {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("invalid %s=%q: %w", key, v, err)
		}
		*dst = b
	}
	return nil
}

func applyDuration(key string, dst *time.Duration) error {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid %s=%q: %w", key, v, err)
		}
		*dst = d
	}
	return nil
}
