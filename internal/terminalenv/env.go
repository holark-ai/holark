package terminalenv

import (
	"path/filepath"
	"strings"
)

var ownedHolarkNames = map[string]bool{
	"HOLARK_HOLON_ID":             true,
	"HOLARK_AGENT_SESSION_ID":     true,
	"HOLARK_REPO_PATH":            true,
	"HOLARK_CODEX_BRIDGE_TOKEN":   true,
	"HOLARK_OPENCODE_EVENTS_PATH": true,
	"HOLARK_OPENCODE_OBSERVER":    true,
}

var sanitizedNames = map[string]bool{
	"TERM":         true,
	"COLORTERM":    true,
	"TERM_PROGRAM": true,
	"TMUX":         true,
	"TMUX_PANE":    true,
	"STY":          true,
	"WINDOW":       true,
}

// SanitizeForBrowserPTY removes parent terminal-emulator state before starting
// a process attached to Holark's xterm.js terminal.
func SanitizeForBrowserPTY(base []string) []string {
	result := make([]string, 0, len(base)+3)
	for _, value := range base {
		name, _, _ := strings.Cut(value, "=")
		if sanitizedNames[name] {
			continue
		}
		result = append(result, value)
	}
	return append(result, "TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=holark")
}

// StripHolark removes all Holark-owned values from an inherited environment.
func StripHolark(base []string) []string {
	result := make([]string, 0, len(base))
	for _, value := range base {
		name, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(name, "HOLARK_") {
			result = append(result, value)
		}
	}
	return result
}

// Context contains the process-local values that every terminal launched by
// one Holark composition must use.
type Context struct {
	ExecutablePath   string
	RuntimeDirectory string
}

// Environment removes inherited Holark configuration, preserves identities and
// launch values assigned by the application and harnesses, and publishes the
// running binary and runtime directory without exposing the CLI bearer token.
func (context Context) Environment(base []string) []string {
	owned := make(map[string]string, len(ownedHolarkNames))
	result := make([]string, 0, len(base)+7)
	pathIndex := -1
	for _, value := range base {
		name, setting, _ := strings.Cut(value, "=")
		if strings.HasPrefix(name, "HOLARK_") {
			if ownedHolarkNames[name] {
				owned[name] = setting
			}
			continue
		}
		if name == "PATH" {
			if pathIndex < 0 {
				pathIndex = len(result)
				result = append(result, value)
			} else {
				result[pathIndex] = value
			}
			continue
		}
		result = append(result, value)
	}
	if directory := filepath.Dir(strings.TrimSpace(context.ExecutablePath)); directory != "." && directory != "" {
		pathValue := directory
		if pathIndex >= 0 {
			_, inherited, _ := strings.Cut(result[pathIndex], "=")
			if inherited != "" {
				pathValue += string(filepath.ListSeparator) + inherited
			}
			result[pathIndex] = "PATH=" + pathValue
		} else {
			result = append(result, "PATH="+pathValue)
		}
	}
	result = SanitizeForBrowserPTY(result)
	for _, name := range []string{"HOLARK_HOLON_ID", "HOLARK_AGENT_SESSION_ID", "HOLARK_REPO_PATH", "HOLARK_CODEX_BRIDGE_TOKEN", "HOLARK_OPENCODE_EVENTS_PATH", "HOLARK_OPENCODE_OBSERVER"} {
		if value := strings.TrimSpace(owned[name]); value != "" {
			result = append(result, name+"="+value)
		}
	}
	if runtimeDirectory := strings.TrimSpace(context.RuntimeDirectory); runtimeDirectory != "" {
		result = append(result, "HOLARK_RUNTIME_DIR="+runtimeDirectory)
	}
	return result
}

// HarnessEnvironment removes inherited Holark and terminal-emulator state,
// then adds the identities a harness process may safely pass to child tools.
func HarnessEnvironment(base []string, sessionID, harnessSessionID, repositoryPath string) []string {
	result := StripHolark(base)
	result = SanitizeForBrowserPTY(result)
	result = append(result, "HOLARK_HOLON_ID="+sessionID)
	if harnessSessionID != "" {
		result = append(result, "HOLARK_AGENT_SESSION_ID="+harnessSessionID)
	}
	return append(result, "HOLARK_REPO_PATH="+repositoryPath)
}
