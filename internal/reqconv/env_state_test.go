package reqconv

import (
	"testing"

	"github.com/d-kuro/kirocc/internal/kiroproto"
)

func TestParseEnvState(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   *kiroproto.EnvState
	}{
		{
			name: "claude code env block",
			prompt: "You are Claude Code.\n\n" +
				"Here is useful information about the environment you are running in:\n" +
				"<env>\n" +
				"Working directory: /Users/dkuro/ghq/github.com/d-kuro/kirocc\n" +
				"Is directory a git repo: Yes\n" +
				"Platform: darwin\n" +
				"OS Version: Darwin 25.5.0\n" +
				"Today's date: 2026-06-03\n" +
				"</env>\n",
			want: &kiroproto.EnvState{
				OperatingSystem:         "macos",
				CurrentWorkingDirectory: "/Users/dkuro/ghq/github.com/d-kuro/kirocc",
			},
		},
		{
			name:   "linux platform maps verbatim",
			prompt: "<env>\nWorking directory: /home/user/proj\nPlatform: linux\n</env>",
			want: &kiroproto.EnvState{
				OperatingSystem:         "linux",
				CurrentWorkingDirectory: "/home/user/proj",
			},
		},
		{
			name:   "cwd only",
			prompt: "<env>\nWorking directory: /tmp/x\n</env>",
			want: &kiroproto.EnvState{
				CurrentWorkingDirectory: "/tmp/x",
			},
		},
		{
			name:   "platform only",
			prompt: "<env>\nPlatform: darwin\n</env>",
			want: &kiroproto.EnvState{
				OperatingSystem: "macos",
			},
		},
		{
			name:   "no env block returns nil",
			prompt: "You are a helpful assistant with no environment info.",
			want:   nil,
		},
		{
			name:   "empty prompt returns nil",
			prompt: "",
			want:   nil,
		},
		{
			name:   "windows platform maps to windows",
			prompt: "<env>\nWorking directory: C:\\Users\\x\nPlatform: win32\n</env>",
			want: &kiroproto.EnvState{
				OperatingSystem:         "windows",
				CurrentWorkingDirectory: "C:\\Users\\x",
			},
		},
		{
			name:   "trailing whitespace trimmed",
			prompt: "<env>\nWorking directory:    /tmp/y   \nPlatform:   darwin  \n</env>",
			want: &kiroproto.EnvState{
				OperatingSystem:         "macos",
				CurrentWorkingDirectory: "/tmp/y",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseEnvState(tt.prompt)
			switch {
			case tt.want == nil && got != nil:
				t.Fatalf("ParseEnvState() = %+v, want nil", got)
			case tt.want == nil && got == nil:
				return
			case got == nil:
				t.Fatalf("ParseEnvState() = nil, want %+v", tt.want)
			}
			if got.OperatingSystem != tt.want.OperatingSystem {
				t.Errorf("OperatingSystem = %q, want %q", got.OperatingSystem, tt.want.OperatingSystem)
			}
			if got.CurrentWorkingDirectory != tt.want.CurrentWorkingDirectory {
				t.Errorf("CurrentWorkingDirectory = %q, want %q", got.CurrentWorkingDirectory, tt.want.CurrentWorkingDirectory)
			}
		})
	}
}
