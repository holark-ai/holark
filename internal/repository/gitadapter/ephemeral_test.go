package gitadapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanupWorktreeRootRepairsMissingMarkerWithoutRemovingUnrelatedWorktree(t *testing.T) {
	repository, _ := workspaceRepository(t)
	adapter, err := Open(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	worktreesRoot := filepath.Join(t.TempDir(), "home", "worktrees")
	ephemeralPath := filepath.Join(worktreesRoot, "holon_111111111111111111111111", "repo")
	unrelatedPath := filepath.Join(t.TempDir(), "unrelated")
	gitTest(t, repository, "worktree", "add", "-b", "test/ephemeral-recovery", "--", ephemeralPath, "HEAD")
	gitTest(t, repository, "worktree", "add", "-b", "unrelated-stale", "--", unrelatedPath, "HEAD")
	registeredEphemeralPath, err := filepath.EvalSymlinks(ephemeralPath)
	if err != nil {
		t.Fatal(err)
	}
	registeredUnrelatedPath, err := filepath.EvalSymlinks(unrelatedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(ephemeralPath, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := CleanupWorktreeRoot(t.Context(), adapter.Descriptor().CommonDir, worktreesRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ephemeralPath); !os.IsNotExist(err) {
		t.Fatalf("ephemeral worktree remains: %v", err)
	}
	registrations := gitText(t, repository, "worktree", "list", "--porcelain")
	if strings.Contains(registrations, "worktree "+registeredEphemeralPath) {
		t.Fatal("ephemeral worktree registration remains")
	}
	if !strings.Contains(registrations, "worktree "+registeredUnrelatedPath) {
		t.Fatal("unrelated worktree registration was removed")
	}
	if commandSucceeds(repository, "show-ref", "--verify", "--quiet", "refs/heads/test/ephemeral-recovery") {
		t.Fatal("ephemeral branch remains")
	}
	if !commandSucceeds(repository, "show-ref", "--verify", "--quiet", "refs/heads/unrelated-stale") {
		t.Fatal("unrelated branch was removed")
	}
	if _, err := os.Stat(filepath.Join(unrelatedPath, ".git")); err != nil {
		t.Fatalf("unrelated worktree was damaged: %v", err)
	}
}
