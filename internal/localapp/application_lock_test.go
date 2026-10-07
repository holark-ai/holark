package localapp

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/holons"
)

func TestApplicationLockCoversMainCheckoutAndLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	applicationLockGit(t, root, "init", "-b", "main")
	applicationLockGit(t, root, "config", "user.name", "Test")
	applicationLockGit(t, root, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	applicationLockGit(t, root, "add", "README.md")
	applicationLockGit(t, root, "commit", "-m", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	applicationLockGit(t, root, "worktree", "add", "-b", "linked", linked)

	home := t.TempDir()
	app, err := New(t.Context(), Options{RepositoryPath: root, HomeDirectory: home})
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(applicationLockGit(t, root, "rev-parse", "HEAD"))
	live, err := app.Holons.Create(t.Context(), holons.Create{
		Title: "Live", Prompt: "keep this state", BaseBranch: "main", BaseCommit: head, AgentType: "codex",
	})
	if err != nil {
		_ = app.Close()
		t.Fatal(err)
	}

	for _, repositoryPath := range []string{root, linked} {
		competing, competingErr := New(t.Context(), Options{RepositoryPath: repositoryPath, HomeDirectory: home})
		if competing != nil {
			_ = competing.Close()
			_ = app.Close()
			t.Fatalf("competing application for %q unexpectedly started", repositoryPath)
		}
		if !errors.Is(competingErr, ErrRepositoryAlreadyRunning) {
			_ = app.Close()
			t.Fatalf("competing application for %q: error = %v, want %v", repositoryPath, competingErr, ErrRepositoryAlreadyRunning)
		}
		current, getErr := app.Holons.Get(t.Context(), live.ID)
		if getErr != nil {
			_ = app.Close()
			t.Fatal(getErr)
		}
		if !reflect.DeepEqual(current, live) {
			_ = app.Close()
			t.Fatalf("competing launch changed live record:\n got %+v\nwant %+v", current, live)
		}
	}

	lockMatches, err := filepath.Glob(filepath.Join(home, "state", "repositories", "*.lock"))
	if err != nil || len(lockMatches) != 1 {
		_ = app.Close()
		t.Fatalf("repository locks = %v, error = %v", lockMatches, err)
	}
	lockPath := lockMatches[0]
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("persistent application lock: %v", err)
	}

	restarted, err := New(t.Context(), Options{RepositoryPath: linked, HomeDirectory: home})
	if err != nil {
		t.Fatalf("restart after release: %v", err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
}

func applicationLockGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return string(output)
}
