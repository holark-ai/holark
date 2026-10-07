//go:build claude_e2e

package localapp

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/holark-ai/holark/internal/protocol"
)

// The real driver uses os.Executable for hook ingestion. Forward that internal
// command to the production binary built by make test-claude.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "claude-hook" {
		cmd := exec.Command(os.Getenv("CLAUDE_E2E_HOOK_EXECUTABLE"), os.Args[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestClaudeCodeE2ECommitForkStatusIsolation(t *testing.T) {
	if os.Getenv("HOLARK_CLAUDE_E2E") != "1" {
		t.Skip("run make test-claude to use the installed, authenticated Claude Code CLI")
	}
	if _, err := os.Stat(os.Getenv("CLAUDE_E2E_HOOK_EXECUTABLE")); err != nil {
		t.Fatal("run make test-claude to build the production hook executable")
	}
	runCommitForkE2E(t, commitForkE2ESpec{
		kind: protocol.HarnessClaudeCode, prepare: claudeE2EWorkspace,
		permissionAnswer: "\r",
		permissionPrompt: "Use the Bash tool to run exactly pwd. Do not use any other tool. After approval is answered, reply PERMISSION_DONE.",
	})
}

func TestClaudeCodeE2ECrashRecovery(t *testing.T) {
	if os.Getenv("HOLARK_CLAUDE_E2E") != "1" {
		t.Skip("requires the installed, authenticated Claude CLI")
	}
	if _, err := os.Stat(os.Getenv("CLAUDE_E2E_HOOK_EXECUTABLE")); err != nil {
		t.Fatal("build the production hook executable before running")
	}
	runAgentCrashRecoveryE2E(t, protocol.HarnessClaudeCode, trustClaudeE2EWorkspace)
}

// Keep test permission rules local to its disposable workspace. Claude uses
// its normal configuration and authentication, including native token refresh.
func claudeE2EWorkspace(t *testing.T, worktree string) {
	t.Helper()
	trustClaudeE2EWorkspace(t, worktree)
	directory := filepath.Join(worktree, ".claude")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	writeCommitForkE2EJSON(t, filepath.Join(directory, "settings.local.json"), map[string]any{
		"permissions": map[string]any{"allow": []string{"Bash(sleep 2)", "Bash(sh ./holark-activity.sh)"}, "ask": []string{"Bash(pwd)"}},
	})
}

// Claude keys worktree trust on the main checkout, which both callers create
// as a fresh temporary directory. Keep authentication in the normal CLI home.
func trustClaudeE2EWorkspace(t *testing.T, root string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if configured := os.Getenv("CLAUDE_CONFIG_DIR"); configured != "" {
		home = configured
	}
	path := filepath.Join(home, ".claude.json")
	update := func(trust bool) error {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		config := map[string]json.RawMessage{}
		if len(data) != 0 {
			if err := json.Unmarshal(data, &config); err != nil {
				return err
			}
		}
		projects := map[string]json.RawMessage{}
		if data := config["projects"]; len(data) != 0 {
			if err := json.Unmarshal(data, &projects); err != nil {
				return err
			}
		}
		if trust {
			projects[root] = json.RawMessage(`{"hasTrustDialogAccepted":true}`)
		} else {
			// Reread the current file so cleanup preserves changes Claude made
			// during the test. Only this test's temporary project is removed.
			delete(projects, root)
		}
		config["projects"], err = json.Marshal(projects)
		if err != nil {
			return err
		}
		data, err = json.MarshalIndent(config, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(path), ".holark-claude-trust-*")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		if _, err := file.Write(append(data, '\n')); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		return os.Rename(file.Name(), path)
	}
	if err := update(true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := update(false); err != nil {
			t.Errorf("remove temporary Claude workspace trust: %v", err)
		}
	})
}

func writeCommitForkE2EJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
