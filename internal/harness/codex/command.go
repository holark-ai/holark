package codex

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminalenv"
)

func Command(executable, repositoryPath, sessionID, prompt string, model ...string) (*exec.Cmd, error) {
	return CommandWithPrefix(commandprefix.New(executable), repositoryPath, sessionID, prompt, model...)
}

func CommandWithPrefix(prefix commandprefix.Prefix, repositoryPath, sessionID, prompt string, model ...string) (*exec.Cmd, error) {
	selectedModel := ""
	if len(model) > 0 {
		selectedModel = model[0]
	}
	return CommandWithPermissions(prefix, repositoryPath, sessionID, prompt, selectedModel, "")
}

func CommandWithPermissions(prefix commandprefix.Prefix, repositoryPath, sessionID, prompt, model, permissions string) (*exec.Cmd, error) {
	if !protocol.ValidPermissions(protocol.HarnessCodex, permissions) {
		return nil, errors.New("invalid Codex permissions")
	}

	executable, prefixArguments := prefix.Executable(), prefix.Arguments()
	if executable == "" || !validRepositoryPath(repositoryPath) ||
		!protocol.ValidIdentifier(sessionID) || len(prompt) > protocol.MaxPromptBytes || !utf8.ValidString(prompt) {
		return nil, errors.New("invalid Codex command")
	}
	profile, err := prepareProjectTrustProfile(repositoryPath, sessionID)
	if err != nil {
		return nil, fmt.Errorf("prepare Codex project trust: %w", err)
	}
	arguments := []string{
		"--cd", repositoryPath,
		"--profile", profile,
	}
	switch permissions {
	case "", "workspace-write":
		arguments = append(arguments, "--sandbox", "workspace-write", "--ask-for-approval", "on-request")
	case "read-only":
		arguments = append(arguments, "--sandbox", "read-only", "--ask-for-approval", "on-request")
	case "full-access":
		arguments = append(arguments, "--sandbox", "danger-full-access", "--ask-for-approval", "never")
	}
	if model != "" {
		if !protocol.ValidModel(model) {
			return nil, errors.New("invalid Codex model")
		}
		arguments = append(arguments, "--model", model)
	}
	if prompt != "" {
		arguments = append(arguments, "--", prompt)
	}
	return command(executable, repositoryPath, sessionID, arguments, prefixArguments...), nil
}

func ResumeCommand(executable, repositoryPath, sessionID, codexSessionID, prompt string, prefixArguments ...string) (*exec.Cmd, error) {
	if executable == "" || !validRepositoryPath(repositoryPath) || !protocol.ValidIdentifier(sessionID) ||
		codexSessionID == "" || strings.Contains(codexSessionID, "\x00") || len(codexSessionID) > 128 ||
		len(prompt) > protocol.MaxPromptBytes || !utf8.ValidString(prompt) {
		return nil, errors.New("invalid Codex resume command")
	}
	profile, err := prepareProjectTrustProfile(repositoryPath, sessionID)
	if err != nil {
		return nil, fmt.Errorf("prepare Codex project trust: %w", err)
	}
	// Remote resumes inherit saved permissions; Codex rejects explicit overrides.
	arguments := []string{
		"resume",
		"--cd", repositoryPath,
		"--profile", profile,
		codexSessionID,
	}
	if prompt != "" {
		arguments = append(arguments, "--", prompt)
	}
	return command(executable, repositoryPath, sessionID, arguments, prefixArguments...), nil
}

func ForkCommand(executable, repositoryPath, sessionID, sourceCodexSessionID, prompt string, prefixArguments ...string) (*exec.Cmd, error) {
	if executable == "" || !validRepositoryPath(repositoryPath) || !protocol.ValidIdentifier(sessionID) ||
		!protocol.ValidIdentifier(sourceCodexSessionID) ||
		len(prompt) > protocol.MaxPromptBytes || !utf8.ValidString(prompt) {
		return nil, errors.New("invalid Codex fork command")
	}
	profile, err := prepareProjectTrustProfile(repositoryPath, sessionID)
	if err != nil {
		return nil, fmt.Errorf("prepare Codex project trust: %w", err)
	}
	// Remote forks inherit source permissions; Codex rejects explicit overrides.
	arguments := []string{
		"fork",
		"--cd", repositoryPath,
		"--profile", profile,
		sourceCodexSessionID,
	}
	if prompt != "" {
		arguments = append(arguments, "--", prompt)
	}
	return command(executable, repositoryPath, sessionID, arguments, prefixArguments...), nil
}

func command(executable, repositoryPath, sessionID string, arguments []string, prefixArguments ...string) *exec.Cmd {
	command := commandprefix.New(executable, prefixArguments...).Command(arguments...)
	command.Dir = repositoryPath
	command.Env = environment(os.Environ(), sessionID, repositoryPath)
	return command
}

func validRepositoryPath(repositoryPath string) bool {
	return filepath.IsAbs(repositoryPath) && filepath.Clean(repositoryPath) == repositoryPath
}

func prepareProjectTrustProfile(repositoryPath, sessionID string) (string, error) {
	codexHome, err := resolveCodexHome()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return "", err
	}

	profile := "holark-holon-" + sessionID
	profilePath := filepath.Join(codexHome, profile+".config.toml")
	contents := []byte("# Managed by Holark.\n[projects." + strconv.Quote(repositoryPath) + "]\ntrust_level = \"trusted\"\n")
	temporary, err := os.CreateTemp(codexHome, "."+profile+"-*.tmp")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return "", err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, profilePath); err != nil {
		return "", err
	}
	return profile, nil
}

func resolveCodexHome() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CODEX_HOME")); configured != "" {
		return filepath.Abs(configured)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

func environment(base []string, sessionID, repositoryPath string) []string {
	return terminalenv.HarnessEnvironment(base, sessionID, "", repositoryPath)
}
