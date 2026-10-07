package harness

import (
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"strings"

	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/protocol"
)

func applyClaudePermissions(command *exec.Cmd, prefix commandprefix.Prefix, permissions string) (*exec.Cmd, error) {
	if !protocol.ValidPermissions(protocol.HarnessClaudeCode, permissions) {
		return nil, errors.New("invalid Claude Code permissions")
	}
	if permissions != "" && permissions != "inherit" {
		// Insert after the launcher prefix and before any positional prompt.
		offset := 1 + len(prefix.Arguments())
		command.Args = slices.Insert(command.Args, offset, "--permission-mode", permissions)
	}
	return command, nil
}

func applyOpenCodePermissions(command *exec.Cmd, permissions string) (*exec.Cmd, error) {
	if !protocol.ValidPermissions(protocol.HarnessOpenCode, permissions) {
		return nil, errors.New("invalid OpenCode permissions")
	}
	if permissions != "" && permissions != "inherit" {
		// OpenCode's dedicated override keeps all other native configuration intact.
		value, err := json.Marshal(map[string]string{"*": permissions})
		if err != nil {
			return nil, err
		}
		command.Env = slices.DeleteFunc(command.Env, func(entry string) bool {
			return strings.HasPrefix(entry, "OPENCODE_PERMISSION=")
		})
		command.Env = append(command.Env, "OPENCODE_PERMISSION="+string(value))
	}
	return command, nil
}
