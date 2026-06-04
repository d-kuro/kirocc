package reqconv

import (
	"regexp"
	"strings"

	"github.com/d-kuro/kirocc/internal/kiroproto"
)

// envBlockRe isolates the <env>...</env> block Claude Code embeds in the system
// prompt. We only parse inside this block to avoid matching stray "Platform:"
// lines elsewhere in the prompt.
var envBlockRe = regexp.MustCompile(`(?s)<env>(.*?)</env>`)

var (
	workingDirRe = regexp.MustCompile(`(?m)^Working directory:\s*(.+?)\s*$`)
	platformRe   = regexp.MustCompile(`(?m)^Platform:\s*(.+?)\s*$`)
)

// ParseEnvState extracts the Kiro envState from a Claude Code system prompt.
//
// Claude Code embeds an <env> block with "Working directory:" and "Platform:"
// lines. We map the Go-style platform value to kiro-cli's operatingSystem
// vocabulary (darwin→macos, win32→windows; others pass through). Only fields
// present in the prompt are populated. If neither field is found (no <env>
// block, or an empty one), it returns nil so the caller omits envState entirely
// — there is no host-derived fallback.
func ParseEnvState(systemPrompt string) *kiroproto.EnvState {
	if systemPrompt == "" {
		return nil
	}
	m := envBlockRe.FindStringSubmatch(systemPrompt)
	if m == nil {
		return nil
	}
	block := m[1]

	var env kiroproto.EnvState
	if wd := workingDirRe.FindStringSubmatch(block); wd != nil {
		env.CurrentWorkingDirectory = strings.TrimSpace(wd[1])
	}
	if pf := platformRe.FindStringSubmatch(block); pf != nil {
		env.OperatingSystem = normalizePlatform(strings.TrimSpace(pf[1]))
	}

	if env.OperatingSystem == "" && env.CurrentWorkingDirectory == "" {
		return nil
	}
	return &env
}

// normalizePlatform converts a Go runtime.GOOS-style platform string to the
// operatingSystem vocabulary kiro-cli sends over the wire.
func normalizePlatform(platform string) string {
	switch platform {
	case "darwin":
		return "macos"
	case "win32", "windows":
		return "windows"
	default:
		return platform
	}
}
