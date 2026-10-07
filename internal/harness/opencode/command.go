package opencode

import (
	"context"
	"errors"
	"fmt"
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

// VerifyResumeTarget asks the installed CLI to resolve its own stored session.
// Observer initialization alone does not establish that --session can load it.
// Discard the exported transcript; only successful lookup matters here.
func VerifyResumeTarget(ctx context.Context, resume *exec.Cmd, target string, prefix commandprefix.Prefix) error {
	command := prefix.CommandContext(ctx, "export", target, "--pure")
	command.Dir, command.Env = resume.Dir, resume.Env
	if err := command.Run(); err != nil {
		return fmt.Errorf("saved OpenCode conversation is unavailable: %w", errors.Join(err, ctx.Err()))
	}
	return nil
}

type ConfigurationDirectoryConflictError struct{}

func (*ConfigurationDirectoryConflictError) Error() string {
	return "OpenCode configuration directory is already controlled by " + ConfigEnvironment
}

type UnsupportedResumeFollowUpPromptError struct{}

func (*UnsupportedResumeFollowUpPromptError) Error() string {
	return "OpenCode 1.18.x does not support a follow-up prompt when resuming a session"
}

func Command(executable, runtimeDir, repositoryPath, sessionID, harnessSessionID, prompt string, model ...string) (*exec.Cmd, error) {
	return CommandWithVersion(executable, "", runtimeDir, repositoryPath, sessionID, harnessSessionID, prompt, model...)
}

// CommandWithVersion threads the resolved installed version for per-operation
// v2 dispatch. An empty or unparseable version keeps the validated v1 path.
func CommandWithVersion(executable, version, runtimeDir, repositoryPath, sessionID, harnessSessionID, prompt string, model ...string) (*exec.Cmd, error) {
	return CommandWithPrefixAndVersion(commandprefix.New(executable), version, runtimeDir, repositoryPath, sessionID, harnessSessionID, prompt, model...)
}

func CommandWithPrefix(prefix commandprefix.Prefix, runtimeDir, repositoryPath, sessionID, harnessSessionID, prompt string, model ...string) (*exec.Cmd, error) {
	return CommandWithPrefixAndVersion(prefix, "", runtimeDir, repositoryPath, sessionID, harnessSessionID, prompt, model...)
}

func CommandWithPrefixAndVersion(prefix commandprefix.Prefix, version, runtimeDir, repositoryPath, sessionID, harnessSessionID, prompt string, model ...string) (*exec.Cmd, error) {
	executable, prefixArguments := prefix.Executable(), prefix.Arguments()
	if err := validateCommandInputs(executable, runtimeDir, repositoryPath, sessionID, harnessSessionID, prompt); err != nil {
		return nil, err
	}
	if _, exists := os.LookupEnv(ConfigEnvironment); exists && !IsV2Version(version) {
		return nil, &ConfigurationDirectoryConflictError{}
	}
	if err := PrepareRuntime(runtimeDir, version); err != nil {
		return nil, err
	}
	arguments := []string{repositoryPath}
	mini := false
	if len(model) > 0 && model[0] != "" {
		if !protocol.ValidModel(model[0]) {
			return nil, errors.New("invalid OpenCode model")
		}
		// V2's root TUI rejects --model; mini supports it and stays interactive.
		// Mini has no directory argument.
		if IsV2Version(version) {
			mini = true
			arguments = []string{"mini"}
		}
		arguments = append(arguments, "--model", model[0])
	}
	if prompt != "" {
		arguments = append(arguments, "--prompt="+prompt)
	}
	command, err := buildCommand(executable, runtimeDir, repositoryPath, sessionID, harnessSessionID, arguments, version, prefixArguments...)
	if err != nil {
		return nil, err
	}
	if mini {
		// Mini prefers PWD over the process working directory.
		command.Env = slices.DeleteFunc(command.Env, func(value string) bool { return strings.HasPrefix(value, "PWD=") })
		command.Env = append(command.Env, "PWD="+repositoryPath)
	}
	return command, nil
}

func ResumeCommand(executable, runtimeDir, repositoryPath, sessionID, harnessSessionID, resumeTarget, followUpPrompt string, prefixArguments ...string) (*exec.Cmd, error) {
	return ResumeCommandWithVersion(executable, "", runtimeDir, repositoryPath, sessionID, harnessSessionID, resumeTarget, followUpPrompt, prefixArguments...)
}

func ResumeCommandWithVersion(executable, version, runtimeDir, repositoryPath, sessionID, harnessSessionID, resumeTarget, followUpPrompt string, prefixArguments ...string) (*exec.Cmd, error) {
	if followUpPrompt != "" {
		return nil, &UnsupportedResumeFollowUpPromptError{}
	}
	if err := validateCommandInputs(executable, runtimeDir, repositoryPath, sessionID, harnessSessionID, followUpPrompt); err != nil {
		return nil, err
	}
	if !protocol.ValidIdentifier(resumeTarget) {
		return nil, errors.New("invalid OpenCode resume command")
	}
	if _, exists := os.LookupEnv(ConfigEnvironment); exists && !IsV2Version(version) {
		return nil, &ConfigurationDirectoryConflictError{}
	}
	if err := PrepareRuntime(runtimeDir, version); err != nil {
		return nil, err
	}
	return buildCommand(executable, runtimeDir, repositoryPath, sessionID, harnessSessionID, []string{repositoryPath, "--session", resumeTarget}, version, prefixArguments...)
}

func ForkCommand(executable, runtimeDir, repositoryPath, sessionID, harnessSessionID, sourceResumeTarget, prompt string, prefixArguments ...string) (*exec.Cmd, error) {
	return ForkCommandWithVersion(executable, "", runtimeDir, repositoryPath, sessionID, harnessSessionID, sourceResumeTarget, prompt, prefixArguments...)
}

func ForkCommandWithVersion(executable, version, runtimeDir, repositoryPath, sessionID, harnessSessionID, sourceResumeTarget, prompt string, prefixArguments ...string) (*exec.Cmd, error) {
	if err := validateCommandInputs(executable, runtimeDir, repositoryPath, sessionID, harnessSessionID, prompt); err != nil {
		return nil, err
	}
	if !protocol.ValidIdentifier(sourceResumeTarget) {
		return nil, errors.New("invalid OpenCode fork command")
	}
	if _, exists := os.LookupEnv(ConfigEnvironment); exists && !IsV2Version(version) {
		return nil, &ConfigurationDirectoryConflictError{}
	}
	if err := PrepareRuntime(runtimeDir, version); err != nil {
		return nil, err
	}
	// The full TUI can submit --prompt before its asynchronous fork finishes.
	// Mini mode waits for the fork before submitting, and remains interactive.
	arguments := []string{repositoryPath}
	if IsV2Version(version) {
		// V2 only supports --fork in mini, which has no directory argument.
		arguments = []string{"mini"}
	} else if prompt != "" {
		arguments = append(arguments, "--mini")
	}
	arguments = append(arguments, "--session", sourceResumeTarget, "--fork")
	if prompt != "" {
		arguments = append(arguments, "--prompt="+prompt)
	}
	command, err := buildCommand(executable, runtimeDir, repositoryPath, sessionID, harnessSessionID, arguments, version, prefixArguments...)
	if err != nil {
		return nil, err
	}
	if IsV2Version(version) {
		// Mini prefers PWD over the process working directory.
		command.Env = slices.DeleteFunc(command.Env, func(value string) bool { return strings.HasPrefix(value, "PWD=") })
		command.Env = append(command.Env, "PWD="+repositoryPath)
	}
	return command, nil
}

func validateCommandInputs(executable, runtimeDir, repositoryPath, sessionID, harnessSessionID, prompt string) error {
	if executable == "" || !validRuntimeDir(runtimeDir) || !cleanAbsolutePath(repositoryPath) ||
		!protocol.ValidIdentifier(sessionID) || !protocol.ValidIdentifier(harnessSessionID) ||
		len(prompt) > protocol.MaxPromptBytes || !utf8.ValidString(prompt) {
		return errors.New("invalid OpenCode command")
	}
	return nil
}

func buildCommand(executable, runtimeDir, repositoryPath, sessionID, harnessSessionID string, arguments []string, version string, prefixArguments ...string) (*exec.Cmd, error) {
	if IsV2Version(version) {
		// A shared server retains its startup observer mode and spool path.
		// Give every v2 launch a private server with this session's environment.
		arguments = slices.Insert(arguments, 1, "--standalone")
	}
	command := commandprefix.New(executable, prefixArguments...).Command(arguments...)
	command.Dir = repositoryPath
	command.Env = terminalenv.HarnessEnvironment(os.Environ(), sessionID, harnessSessionID, repositoryPath)
	if IsV2Version(version) {
		content, err := observerConfigContent(runtimeDir, version)
		if err != nil {
			return nil, err
		}
		command.Env = slices.DeleteFunc(command.Env, func(value string) bool { return strings.HasPrefix(value, "OPENCODE_CONFIG_CONTENT=") })
		command.Env = append(command.Env, "OPENCODE_CONFIG_CONTENT="+content)
	} else {
		command.Env = append(command.Env, ConfigEnvironment+"="+SharedConfigDirectory(runtimeDir, version))
	}
	command.Env = append(command.Env, EventsEnvironment+"="+filepath.Join(runtimeDir, SpoolFileName))
	mode := "terminal"
	if slices.Contains(arguments, "--mini") || (len(arguments) > 0 && arguments[0] == "mini") {
		mode = "server"
	}
	command.Env = append(command.Env, "HOLARK_OPENCODE_OBSERVER="+mode)
	return command, nil
}
