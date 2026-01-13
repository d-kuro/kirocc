package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// Config is the runtime configuration for kirocc.
type Config struct {
	Port   int
	Host   string
	DBPath string
	APIKey string
	Debug  bool
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
