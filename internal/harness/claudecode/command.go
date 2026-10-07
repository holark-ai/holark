package claudecode

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminalenv"
)

const (
	SettingsFileName = "settings.json"
	LossFileName     = "observation-lost"
	LossFilePrefix   = LossFileName + "-"
	MetadataFileName = "runtime.json"
)

var hookEvents = []string{
	"SessionStart",
	"UserPromptSubmit",
	"PermissionRequest",
	"PermissionDenied",
	"PreToolUse",
	"PostToolUse",
	"PostToolUseFailure",
	"Notification",
	"Stop",
	"StopFailure",
	"SubagentStop",
	"SessionEnd",
	"CwdChanged",
	"Elicitation",
	"ElicitationResult",
}

type runtimeMetadata struct {
	LaunchToken      string `json:"launch_token"`
	HarnessSessionID string `json:"harness_session_id"`
	SocketPath       string `json:"socket_path"`
}

type hookSettings struct {
	Hooks map[string][]hookMatcher `json:"hooks"`
}

type hookMatcher struct {
	Hooks []hookHandler `json:"hooks"`
}

type hookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

func Command(executable, hookExecutable, runtimeDir, repositoryPath, sessionID, harnessSessionID, prompt string, model ...string) (*exec.Cmd, error) {
	return CommandWithPrefix(commandprefix.New(executable), hookExecutable, runtimeDir, repositoryPath, sessionID, harnessSessionID, prompt, model...)
}

func CommandWithPrefix(prefix commandprefix.Prefix, hookExecutable, runtimeDir, repositoryPath, sessionID, harnessSessionID, prompt string, model ...string) (*exec.Cmd, error) {
	executable, prefixArguments := prefix.Executable(), prefix.Arguments()
	if executable == "" || !validCommandInputs(runtimeDir, repositoryPath, sessionID, harnessSessionID) ||
		len(prompt) > protocol.MaxPromptBytes || !utf8.ValidString(prompt) {
		return nil, errors.New("invalid Claude Code command")
	}
	claudeSessionID, err := newUUID()
	if err != nil {
		return nil, err
	}
	settingsPath, err := PrepareRuntime(runtimeDir, hookExecutable, harnessSessionID)
	if err != nil {
		return nil, err
	}
	arguments := []string{"--session-id", claudeSessionID, "--settings", settingsPath}
	if len(model) > 0 && model[0] != "" {
		if !protocol.ValidModel(model[0]) {
			return nil, errors.New("invalid Claude Code model")
		}
		arguments = append(arguments, "--model", model[0])
	}
	if prompt != "" {
		arguments = append(arguments, "--", prompt)
	}
	return command(executable, repositoryPath, sessionID, harnessSessionID, arguments, prefixArguments...), nil
}

func ResumeCommand(executable, hookExecutable, runtimeDir, repositoryPath, sessionID, harnessSessionID, claudeSessionID, prompt string, prefixArguments ...string) (*exec.Cmd, error) {
	if executable == "" || !validCommandInputs(runtimeDir, repositoryPath, sessionID, harnessSessionID) || !validUUID(claudeSessionID) ||
		len(prompt) > protocol.MaxPromptBytes || !utf8.ValidString(prompt) {
		return nil, errors.New("invalid Claude Code resume command")
	}
	settingsPath, err := PrepareRuntime(runtimeDir, hookExecutable, harnessSessionID)
	if err != nil {
		return nil, err
	}
	arguments := []string{"--resume", claudeSessionID, "--settings", settingsPath}
	if prompt != "" {
		arguments = append(arguments, "--", prompt)
	}
	return resumedCommand(executable, repositoryPath, sessionID, harnessSessionID, arguments, prefixArguments...), nil
}

func ForkCommand(executable, hookExecutable, runtimeDir, repositoryPath, sessionID, harnessSessionID, sourceClaudeSessionID, prompt string, prefixArguments ...string) (*exec.Cmd, error) {
	if executable == "" || !validCommandInputs(runtimeDir, repositoryPath, sessionID, harnessSessionID) ||
		!validUUID(sourceClaudeSessionID) ||
		len(prompt) > protocol.MaxPromptBytes || !utf8.ValidString(prompt) {
		return nil, errors.New("invalid Claude Code fork command")
	}
	settingsPath, err := PrepareRuntime(runtimeDir, hookExecutable, harnessSessionID)
	if err != nil {
		return nil, err
	}
	arguments := []string{"--resume", sourceClaudeSessionID, "--fork-session", "--settings", settingsPath}
	if prompt != "" {
		arguments = append(arguments, "--", prompt)
	}
	return resumedCommand(executable, repositoryPath, sessionID, harnessSessionID, arguments, prefixArguments...), nil
}

func resumedCommand(executable, repositoryPath, sessionID, harnessSessionID string, arguments []string, prefixArguments ...string) *exec.Cmd {
	cmd := command(executable, repositoryPath, sessionID, harnessSessionID, arguments, prefixArguments...)
	// Let Claude restore its saved model, including in-conversation changes,
	// without an inherited startup override taking precedence.
	cmd.Env = slices.DeleteFunc(cmd.Env, func(value string) bool {
		return strings.HasPrefix(value, "ANTHROPIC_MODEL=")
	})
	return cmd
}

func command(executable, repositoryPath, sessionID, harnessSessionID string, arguments []string, prefixArguments ...string) *exec.Cmd {
	command := commandprefix.New(executable, prefixArguments...).Command(arguments...)
	command.Dir = repositoryPath
	command.Env = terminalenv.HarnessEnvironment(os.Environ(), sessionID, harnessSessionID, repositoryPath)
	return command
}

func PrepareRuntime(runtimeDir, hookExecutable, harnessSessionID string) (string, error) {
	if hookExecutable == "" || !validRuntimeDir(runtimeDir) || !protocol.ValidIdentifier(harnessSessionID) {
		return "", errors.New("invalid Claude Code hook runtime")
	}
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		return "", err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(tokenBytes)
	// Keep below the macOS Unix socket path limit even for long runtime roots.
	metadata := runtimeMetadata{LaunchToken: token, HarnessSessionID: harnessSessionID,
		SocketPath: filepath.Join("/tmp", "holark-claude-"+token[:32]+".sock")}
	metadataData, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	metadataData = append(metadataData, '\n')
	if err := writePrivateExclusive(filepath.Join(runtimeDir, MetadataFileName), metadataData); err != nil {
		return "", err
	}
	hookCommand := strings.Join([]string{
		shellQuote(hookExecutable),
		"claude-hook",
		"--runtime-dir", shellQuote(runtimeDir),
		"--launch-token", shellQuote(metadata.LaunchToken),
		"--harness-session-id", shellQuote(harnessSessionID),
	}, " ")
	settings := hookSettings{Hooks: make(map[string][]hookMatcher, len(hookEvents))}
	for _, event := range hookEvents {
		settings.Hooks[event] = []hookMatcher{{Hooks: []hookHandler{{Type: "command", Command: hookCommand, Timeout: 5}}}}
	}
	settingsData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	settingsData = append(settingsData, '\n')
	settingsPath := filepath.Join(runtimeDir, SettingsFileName)
	if err := writePrivateExclusive(settingsPath, settingsData); err != nil {
		return "", err
	}
	return settingsPath, nil
}

func readRuntimeMetadata(runtimeDir string) (runtimeMetadata, error) {
	path := filepath.Join(runtimeDir, MetadataFileName)
	info, err := os.Lstat(path)
	if err != nil {
		return runtimeMetadata{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > MaxRecordBytes {
		return runtimeMetadata{}, errors.New("insecure Claude runtime metadata")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return runtimeMetadata{}, err
	}
	var metadata runtimeMetadata
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return runtimeMetadata{}, err
	}
	if len(metadata.LaunchToken) != 64 || !protocol.ValidIdentifier(metadata.HarnessSessionID) {
		return runtimeMetadata{}, errors.New("invalid Claude Code runtime metadata")
	}
	if _, err := hex.DecodeString(metadata.LaunchToken); err != nil {
		return runtimeMetadata{}, errors.New("invalid Claude Code runtime token")
	}
	if metadata.SocketPath != filepath.Join("/tmp", "holark-claude-"+metadata.LaunchToken[:32]+".sock") {
		return runtimeMetadata{}, errors.New("invalid Claude observation paths")
	}
	return metadata, nil
}

func writePrivateExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func validCommandInputs(runtimeDir, repositoryPath, sessionID, harnessSessionID string) bool {
	return validRuntimeDir(runtimeDir) && filepath.IsAbs(repositoryPath) && filepath.Clean(repositoryPath) == repositoryPath &&
		protocol.ValidIdentifier(sessionID) && protocol.ValidIdentifier(harnessSessionID)
}

func validRuntimeDir(runtimeDir string) bool {
	if !filepath.IsAbs(runtimeDir) || filepath.Clean(runtimeDir) != runtimeDir {
		return false
	}
	info, err := os.Lstat(runtimeDir)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func newUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(value, "-", "")
	_, err := hex.DecodeString(compact)
	return err == nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
