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
