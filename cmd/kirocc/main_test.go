package main

import (
	"context"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/d-kuro/kirocc/internal/config"
	"github.com/d-kuro/kirocc/internal/kiroclient"
)

func TestRun_HelpFlagReturnsNoError(t *testing.T) {
	if err := run(context.Background(), []string{"-h"}); err != nil {
		t.Errorf("run with -h: got err %v; want nil", err)
	}
}

func TestParseFlags_KiroAPIRegion(t *testing.T) {
	t.Run("default is empty so the credential region is used", func(t *testing.T) {
		cfg, _, err := parseFlags(nil)
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if cfg.KiroAPIRegion != "" {
			t.Fatalf("KiroAPIRegion = %q, want empty", cfg.KiroAPIRegion)
		}
	})

	t.Run("flag", func(t *testing.T) {
		cfg, _, err := parseFlags([]string{"-kiro-api-region", "us-east-1"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if cfg.KiroAPIRegion != "us-east-1" {
			t.Fatalf("KiroAPIRegion = %q, want us-east-1", cfg.KiroAPIRegion)
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("invalid region is rejected by Validate", func(t *testing.T) {
		cfg, _, err := parseFlags([]string{"-kiro-api-region", "evil.example.com"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if err := cfg.Validate(); err == nil {
			t.Fatal("Validate: want error for a non-region value")
		}
	})
}

func TestParseFlags_ModelDiscovery(t *testing.T) {
	t.Run("default enabled", func(t *testing.T) {
		cfg, _, err := parseFlags(nil)
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if !cfg.ModelDiscovery {
			t.Fatal("ModelDiscovery = false, want true")
		}
	})

	t.Run("disabled by flag", func(t *testing.T) {
		cfg, _, err := parseFlags([]string{"-model-discovery=false"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if cfg.ModelDiscovery {
			t.Fatal("ModelDiscovery = true, want false")
		}
	})
}

func TestParseFlags_ResponseHeaderTimeout(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		cfg, _, err := parseFlags(nil)
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if cfg.ResponseHeaderTimeout != kiroclient.DefaultResponseHeaderTimeout {
			t.Fatalf("ResponseHeaderTimeout = %v, want %v", cfg.ResponseHeaderTimeout, kiroclient.DefaultResponseHeaderTimeout)
		}
	})

	t.Run("flag", func(t *testing.T) {
		cfg, _, err := parseFlags([]string{"-response-header-timeout", "90s"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if cfg.ResponseHeaderTimeout != 90*time.Second {
			t.Fatalf("ResponseHeaderTimeout = %v, want 90s", cfg.ResponseHeaderTimeout)
		}
	})
}

func TestParseFlags_KeepAliveInterval(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		cfg, _, err := parseFlags(nil)
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if cfg.KeepAliveInterval != config.DefaultKeepAliveInterval {
			t.Fatalf("KeepAliveInterval = %v, want %v", cfg.KeepAliveInterval, config.DefaultKeepAliveInterval)
		}
	})

	t.Run("flag", func(t *testing.T) {
		cfg, _, err := parseFlags([]string{"-keepalive-interval", "30s"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if cfg.KeepAliveInterval != 30*time.Second {
			t.Fatalf("KeepAliveInterval = %v, want 30s", cfg.KeepAliveInterval)
		}
	})
}

func TestRun_HelpMentionsVersion(t *testing.T) {
	got := captureFile(t, &os.Stderr, func() {
		if err := run(context.Background(), []string{"-h"}); err != nil {
			t.Errorf("run with -h: got err %v; want nil", err)
		}
	})
	if !strings.Contains(got, "-version") || !strings.Contains(got, "print version and exit") {
		t.Fatalf("help = %q, want -version usage", got)
	}
}

func TestParseFlags_Version(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		_, showVersion, err := parseFlags(nil)
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if showVersion {
			t.Fatal("showVersion = true, want false")
		}
	})

	t.Run("flag", func(t *testing.T) {
		_, showVersion, err := parseFlags([]string{"-version"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if !showVersion {
			t.Fatal("showVersion = false, want true")
		}
	})

	t.Run("long flag", func(t *testing.T) {
		_, showVersion, err := parseFlags([]string{"--version"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if !showVersion {
			t.Fatal("showVersion = false, want true")
		}
	})

	t.Run("explicitly off", func(t *testing.T) {
		_, showVersion, err := parseFlags([]string{"-version=false"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if showVersion {
			t.Fatal("showVersion = true, want false")
		}
	})

	t.Run("kiro-cli form", func(t *testing.T) {
		_, showVersion, err := parseFlags([]string{"version"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if !showVersion {
			t.Fatal("showVersion = false, want true")
		}
	})

	t.Run("positional version does not override a listen command", func(t *testing.T) {
		cfg, showVersion, err := parseFlags([]string{"-port", "3456", "version"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if showVersion {
			t.Fatal("showVersion = true, want false")
		}
		if cfg.Port != 3456 {
			t.Fatalf("Port = %d, want 3456", cfg.Port)
		}
	})
}

func TestVersionLine(t *testing.T) {
	if version != "dev" {
		t.Fatalf("go test build version = %q, want dev", version)
	}
	orig := version
	t.Cleanup(func() { version = orig })
	version = "9.9.9"
	if got := versionLine(); got != "kirocc 9.9.9" {
		t.Fatalf("versionLine() = %q, want kirocc 9.9.9", got)
	}
}

func TestRun_VersionExitsBeforeStartup(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	orig := version
	t.Cleanup(func() { version = orig })
	version = "9.9.9"

	// A busy port plus a region Validate would reject. Returning the version
	// line means startup stopped before either check.
	cases := [][]string{
		{"-version", "-port", strconv.Itoa(port), "-kiro-api-region", "not a region"},
		{"--version", "-port", strconv.Itoa(port), "-kiro-api-region", "not a region"},
		{"version"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var runErr error
			got := captureFile(t, &os.Stdout, func() {
				runErr = run(context.Background(), args)
			})
			if runErr != nil {
				t.Fatalf("run: got err %v; want nil", runErr)
			}
			if got != "kirocc 9.9.9\n" {
				t.Fatalf("stdout = %q, want %q", got, "kirocc 9.9.9\n")
			}
		})
	}
}

func TestRun_ExplicitVersionOffStillValidates(t *testing.T) {
	err := run(context.Background(), []string{"-version=false", "-kiro-api-region", "not a region"})
	if err == nil {
		t.Fatal("run: got nil, want a config error")
	}
	if !strings.Contains(err.Error(), "kiro-api-region") {
		t.Fatalf("run: got %v, want a region validation error", err)
	}
}

func captureFile(t *testing.T, target **os.File, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := *target
	*target = w
	t.Cleanup(func() { *target = orig })

	fn()

	*target = orig
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(out)
}
