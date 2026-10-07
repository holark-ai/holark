//go:build claude_e2e

package localapp

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/harness/claudecode"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminalhost"
)

func TestClaudeCodeE2EWrapperMonitoring(t *testing.T) {
	if os.Getenv("HOLARK_CLAUDE_E2E") != "1" {
		t.Skip("run make test-claude")
	}
	worktree, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	claudeE2EWorkspace(t, worktree)
	manager, err := terminalhost.NewManager(terminalhost.Options{MaximumTerminals: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	// Both wrappers stay alive while the real Claude CLI runs below them,
	// exercising the process layout of package runners and non-exec scripts.
	runner := filepath.Join(t.TempDir(), "runner.cjs")
	writeActivityFile(t, runner, `const {spawn} = require("node:child_process");
const child = spawn(process.argv[2], process.argv.slice(3), {stdio: "inherit"});
child.on("error", () => process.exit(1));
child.on("exit", code => process.exit(code ?? 1));
`)
	// Restore the authenticated profile in Claude's environment while Holark
	// sees a different directory, as with an environment-setting wrapper.
	environment := []string{"-u", "CLAUDE_CONFIG_DIR"}
	if configDir, set := os.LookupEnv("CLAUDE_CONFIG_DIR"); set {
		environment = []string{"CLAUDE_CONFIG_DIR=" + configDir}
	}
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	prefix := commandprefix.New("env", append(environment, "sh", "-c", `"$@"; exit $?`, "wrapper", "node", runner, "claude")...)
	var source string
	for _, mode := range []string{"new", "resume", "fork"} {
		if !t.Run(mode, func(t *testing.T) {
			runtimeDir := t.TempDir()
			id := "wrapper-" + mode
			hook := os.Getenv("CLAUDE_E2E_HOOK_EXECUTABLE")
			var command *exec.Cmd
			var err error
			switch mode {
			case "new":
				command, err = claudecode.CommandWithPrefix(prefix, hook, runtimeDir, worktree, "wrapper-holon", id, "")
			case "resume":
				command, err = claudecode.ResumeCommand(prefix.Executable(), hook, runtimeDir, worktree, "wrapper-holon", id, source, "", prefix.Arguments()...)
			case "fork":
				command, err = claudecode.ForkCommand(prefix.Executable(), hook, runtimeDir, worktree, "wrapper-holon", id, source, "", prefix.Arguments()...)
			}
			if err != nil {
				t.Fatal(err)
			}
			claude := launchObservedActivityClaude(t, manager, worktree, id, runtimeDir, command)
			t.Cleanup(func() { _ = manager.CloseTerminal(claude.id, time.Second) })
			claude.waitActivity(t, protocol.ActivityIdle)
			session := claude.snapshot().resumeTarget
			if session == "" || (mode == "resume" && session != source) || (mode == "fork" && session == source) {
				t.Fatalf("%s session = %q, source = %q", mode, session, source)
			}
			if mode == "new" {
				source = session
			}
			checkpoint := claude.historyLength()
			claude.submit(t, "Reply exactly WRAPPER_OK. Do not use tools.")
			claude.waitSince(t, checkpoint, "working through wrapper", func(s activitySnapshot) bool {
				return s.healthy && s.activity == protocol.ActivityWorking
			})
			claude.wait(t, "completion and context through wrapper", func(s activitySnapshot) bool {
				return s.healthy && s.activity == protocol.ActivityCompleted && s.contextTokens > 0 && s.inputState == protocol.InputTaskComplete
			})
		}) {
			return
		}
	}
}
