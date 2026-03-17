package config

import "testing"

func TestApplyString(t *testing.T) {
	tests := []struct {
		name     string
		envVal   string
		setEnv   bool
		initial  string
		expected string
	}{
		{"set", "hello", true, "default", "hello"},
		{"unset", "", false, "default", "default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setEnv {
				t.Setenv("TEST_VAR", tt.envVal)
			}
			s := tt.initial
			applyString("TEST_VAR", &s)
			if s != tt.expected {
				t.Fatalf("got %q, want %q", s, tt.expected)
			}
		})
	}
}

func TestApplyInt(t *testing.T) {
	tests := []struct {
		name     string
		envVal   string
		initial  int
		expected int
		wantErr  bool
	}{
		{"valid", "9999", 3456, 9999, false},
		{"invalid", "notanumber", 3456, 3456, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_PORT", tt.envVal)
			n := tt.initial
			err := applyInt("TEST_PORT", &n)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if n != tt.expected {
				t.Fatalf("got %d, want %d", n, tt.expected)
			}
		})
	}
}

func TestApplyBool(t *testing.T) {
	tests := []struct {
		name     string
		envVal   string
		setEnv   bool
		initial  bool
		expected bool
		wantErr  bool
	}{
		{"1", "1", true, false, true, false},
		{"true", "true", true, false, true, false},
		{"false", "false", true, true, false, false},
		{"0", "0", true, true, false, false},
		{"invalid", "notabool", true, false, false, true},
		{"unset", "", false, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setEnv {
				t.Setenv("TEST_DEBUG", tt.envVal)
			}
			b := tt.initial
			err := applyBool("TEST_DEBUG", &b)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if b != tt.expected {
				t.Fatalf("got %v, want %v", b, tt.expected)
			}
		})
	}
}

func TestDefaultDBPathFor(t *testing.T) {
	tests := []struct {
		name string
		goos string
		home string
		want string
	}{
		{
			name: "darwin",
			goos: "darwin",
			home: "/Users/dkuro",
			want: "/Users/dkuro/Library/Application Support/kiro-cli/data.sqlite3",
		},
		{
			name: "linux",
			goos: "linux",
			home: "/home/dkuro",
			want: "/home/dkuro/.local/share/kiro-cli/data.sqlite3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DefaultDBPathFor(tt.goos, tt.home); got != tt.want {
				t.Fatalf("DefaultDBPathFor(%q, %q) = %q, want %q", tt.goos, tt.home, got, tt.want)
			}
		})
	}
}

func TestApplyEnvOverrides_LogFields(t *testing.T) {
	t.Setenv("KIROCC_LOG_FILE", "/tmp/test.log")
	t.Setenv("KIROCC_LOG_MAX_SIZE", "50")
	t.Setenv("KIROCC_LOG_MAX_BACKUPS", "10")
	t.Setenv("KIROCC_LOG_MAX_AGE", "30")
	t.Setenv("KIROCC_LOG_COMPRESS", "true")
	t.Setenv("KIROCC_LOG_CONSOLE", "true")

	cfg := Config{}
	if err := ApplyEnvOverrides(&cfg); err != nil {
		t.Fatalf("ApplyEnvOverrides: %v", err)
	}
	if cfg.LogFile.Path != "/tmp/test.log" {
		t.Errorf("LogFile.Path = %q, want %q", cfg.LogFile.Path, "/tmp/test.log")
	}
	if cfg.LogFile.MaxSize != 50 {
		t.Errorf("LogFile.MaxSize = %d, want 50", cfg.LogFile.MaxSize)
	}
	if cfg.LogFile.MaxBackups != 10 {
		t.Errorf("LogFile.MaxBackups = %d, want 10", cfg.LogFile.MaxBackups)
	}
	if cfg.LogFile.MaxAge != 30 {
		t.Errorf("LogFile.MaxAge = %d, want 30", cfg.LogFile.MaxAge)
	}
	if !cfg.LogFile.Compress {
		t.Error("LogFile.Compress = false, want true")
	}
	if !cfg.LogFile.Console {
		t.Error("LogFile.Console = false, want true")
	}
}
