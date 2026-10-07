//go:build integration

package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestImmediateWorkspaceAndDeferredBranchLifecycle(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	gitSSH := GitSSHOptions{
		KeyPath:        filepath.Join(manager.root, "git_ssh", "id_ed25519"),
		KnownHostsPath: filepath.Join(manager.root, "git_ssh", "known_hosts"),
	}
	firstID := "session-8f3c2a91e47b6d102348b4f63f5a729c"
	secondID := "session-8f3c2a91ffff6d102348b4f63f5a729c"

	first, err := manager.Prepare(t.Context(), "project", firstID, remote, "main", remote, gitSSH)
	if err != nil {
		t.Fatal(err)
	}
	if first.Branch != "holark/session-8f3c2a91" || filepath.ToSlash(first.Path) != filepath.ToSlash(filepath.Join(manager.root, "sessions", "session-8f3c2a91", "repo")) {
		t.Fatalf("first workspace = %+v", first)
	}

	second, err := manager.Prepare(t.Context(), "project", secondID, remote, "main", remote, gitSSH)
	if err != nil {
		t.Fatal(err)
	}
	if second.Branch != "holark/session-8f3c2a91ffff" || filepath.ToSlash(second.Path) != filepath.ToSlash(filepath.Join(manager.root, "sessions", "session-8f3c2a91ffff", "repo")) {
		t.Fatalf("colliding workspace = %+v", second)
	}
	for _, prepared := range []struct {
		id string
		ws Prepared
	}{{firstID, first}, {secondID, second}} {
		assertWorktreeConfig(t, prepared.ws.Path, "holark.sessionId", prepared.id)
		assertWorktreeConfig(t, prepared.ws.Path, "holark.autopushRef", "refs/holark/sessions/"+prepared.id)
		if refExists(t, filepath.Join(manager.root, "mirrors", "project.git"), "refs/holark/fetch/"+prepared.id) {
			t.Fatalf("full-ID staging ref remained for %s", prepared.id)
		}
	}

	firstPath, secondPath := first.Path, second.Path
	firstCommit, secondCommit := first.HeadCommit, second.HeadCommit
	firstBranch, err := manager.RenameProviderBranch(context.Background(), "project", first.Path, first.Branch, remote, "fix-login-flow", remote, gitSSH)
	if err != nil {
		t.Fatal(err)
	}
	secondBranch, err := manager.RenameProviderBranch(context.Background(), "project", second.Path, second.Branch, remote, "fix-login-flow", remote, gitSSH)
	if err != nil {
		t.Fatal(err)
	}
	if firstBranch != "holark/fix-login-flow" || secondBranch != "holark/fix-login-flow-2" {
		t.Fatalf("renamed branches = %q, %q", firstBranch, secondBranch)
	}
	if first.Path != firstPath || second.Path != secondPath {
		t.Fatal("branch rename changed a worktree path")
	}
	for _, prepared := range []struct {
		ws     Prepared
		branch string
		commit string
	}{{first, firstBranch, firstCommit}, {second, secondBranch, secondCommit}} {
		if got := strings.TrimSpace(worktreeGitOutput(t, prepared.ws.Path, "symbolic-ref", "--short", "HEAD")); got != prepared.branch {
			t.Fatalf("current branch = %q, want %q", got, prepared.branch)
		}
		if got := strings.TrimSpace(worktreeGitOutput(t, prepared.ws.Path, "rev-parse", "HEAD")); got != prepared.commit {
			t.Fatalf("HEAD = %q, want unchanged %q", got, prepared.commit)
		}
		assertWorktreeConfig(t, prepared.ws.Path, "holark.proposedBranch", prepared.branch)
		if refExists(t, remote, "refs/heads/"+prepared.branch) {
			t.Fatalf("rename created provider branch %q", prepared.branch)
		}
	}

	if err := os.WriteFile(filepath.Join(first.Path, "agent.txt"), []byte("agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, first.Path, "add", "agent.txt")
	runGit(t, first.Path, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "agent change")
	head := strings.TrimSpace(worktreeGitOutput(t, first.Path, "rev-parse", "HEAD"))
	if got := strings.TrimSpace(gitOutput(t, remote, "rev-parse", "refs/holark/sessions/"+firstID)); got != head {
		t.Fatalf("autopush ref = %q, want %q", got, head)
	}
	if refExists(t, remote, "refs/holark/sessions/session-8f3c2a91") || refExists(t, remote, "refs/heads/"+firstBranch) {
		t.Fatal("commit pushed a shortened or readable provider ref")
	}
}

func TestDeferredRenamePreservesManualBranch(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	gitSSH := GitSSHOptions{
		KeyPath:        filepath.Join(manager.root, "git_ssh", "id_ed25519"),
		KnownHostsPath: filepath.Join(manager.root, "git_ssh", "known_hosts"),
	}
	prepared, err := manager.Prepare(t.Context(), "project", "session-acde1234aaaabbbbccccddddeeeeffff", remote, "main", remote, gitSSH)
	if err != nil {
		t.Fatal(err)
	}

	runGit(t, prepared.Path, "branch", "-m", "holark/manual-choice")
	_, err = manager.RenameProviderBranch(context.Background(), "project", prepared.Path, prepared.Branch, remote, "ignored-name", remote, gitSSH)
	if !errors.Is(err, ErrBranchMismatch) {
		t.Fatalf("manual branch rename error = %v, want branch mismatch", err)
	}
	if got := strings.TrimSpace(worktreeGitOutput(t, prepared.Path, "symbolic-ref", "--short", "HEAD")); got != "holark/manual-choice" {
		t.Fatalf("manual branch = %q", got)
	}
	if output, err := worktreeGitOutputErr(prepared.Path, "config", "--worktree", "--get", "holark.proposedBranch"); err == nil || strings.TrimSpace(output) != "" {
		t.Fatalf("manual branch gained proposal metadata: %q, %v", output, err)
	}
}

func worktreeGitOutputErr(path string, arguments ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", path}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	return string(output), err
}
