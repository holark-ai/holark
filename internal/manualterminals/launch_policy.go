package manualterminals

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/holark-ai/holark/internal/terminalenv"
)

// ShellLaunchPolicy is the node-local half of the manual-terminal product
// policy. It selects an executable shell and strips parent terminal-emulator
// state from the environment before the canonical TerminalHost launch.
type ShellLaunchPolicy struct {
	TerminalContext terminalenv.Context
}

func (policy ShellLaunchPolicy) Resolve(baseEnvironment []string, configuredShell string) (string, []string) {
	command := strings.TrimSpace(configuredShell)
	if !executableFile(command) {
		command = "/bin/sh"
	}
	return command, policy.TerminalContext.Environment(baseEnvironment)
}

func executableFile(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
