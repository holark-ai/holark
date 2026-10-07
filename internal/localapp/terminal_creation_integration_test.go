package localapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/holons"
	holonssqlite "github.com/holark-ai/holark/internal/holons/sqliteadapter"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
	"github.com/holark-ai/holark/internal/sessionterminals"
	"github.com/holark-ai/holark/internal/terminalenv"
	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
	"github.com/holark-ai/holark/internal/terminals/localadapter"
)

type terminalCreationFixture struct {
	repositories *repository.Service
	store        *holonssqlite.Store
	service      *terminalHolonService
	manager      *terminalhost.Manager
	preparation  repository.Preparation
}

func newTerminalCreationFixture(t *testing.T) terminalCreationFixture {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	root := t.TempDir()
	repo := t.TempDir()
	git := func(args ...string) string { return strings.TrimSpace(applicationLockGit(t, repo, args...)) }
	git("init", "-b", "main")
	git("config", "user.name", "Terminal Test")
	git("config", "user.email", "terminal@invalid")
	git("commit", "--allow-empty", "-m", "main")
	git("switch", "-c", "feature/terminal")
	git("commit", "--allow-empty", "-m", "feature")
	git("switch", "main")
	git("clone", "--bare", repo, filepath.Join(root, "remote.git"))
	git("remote", "add", "origin", filepath.Join(root, "remote.git"))
	git("fetch", "origin")
	git("remote", "set-head", "origin", "main")
	repositoryStore, err := gitadapter.OpenWithWorktrees(t.Context(), repo, filepath.Join(root, "worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	repositories := repository.NewService(repositoryStore)
	prepared, err := repositories.PrepareBranch(t.Context(), "feature/terminal")
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := holonssqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	service := holons.NewServiceWithRepository(store, holonRepositoryCoordinator{repositories: repositories})
	manager, err := terminalhost.NewManager(terminalhost.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close() })
	coordinator, err := sessionterminals.New(localadapter.New(manager, terminalenv.Context{}), &localTerminalProducts{holons: service})
	if err != nil {
		t.Fatal(err)
	}
	// An unavailable naming agent must not prevent a real shell from starting.
	preferences := &harnessPreferencesStub{resolveErr: agentsettings.ErrNoAvailable}
	naming, err := agentsessions.New(harness.NewRegistry(nil), &localAgentState{holons: service}, localAgentLauncher{manager: manager}, nil,
		localAgentNamer{harnesses: preferences}, filepath.Join(root, "agents"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(naming.Close)
	return terminalCreationFixture{repositories: repositories, store: store, service: &terminalHolonService{Service: service, terminals: coordinator, naming: naming, harnesses: preferences}, manager: manager, preparation: prepared}
}

func (f terminalCreationFixture) create(t *testing.T, prompt string) holons.Holon {
	t.Helper()
	h, err := f.service.Create(t.Context(), holons.Create{StartupMode: "terminal", Prompt: prompt, BaseBranch: f.preparation.Branch, BaseCommit: f.preparation.Commit})
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != holons.StatusRunning || len(h.AgentSessions) != 0 || len(h.ManualTerminals) != 1 || h.LastSelectedTabID != h.ManualTerminals[0].ID {
		t.Fatalf("terminal Holon: %+v", h)
	}
	return h
}

func assertTerminalShellUsable(t *testing.T, manager *terminalhost.Manager, h holons.Holon) {
	t.Helper()
	terminal := h.ManualTerminals[len(h.ManualTerminals)-1]
	if err := manager.Input(t.Context(), terminals.TerminalID(terminal.TerminalID), []byte("printf usable > shell-proof\n")); err != nil {
		t.Fatal(err)
	}
	proof := filepath.Join(h.WorktreePath, "shell-proof")
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(proof)
		if err == nil && string(data) == "usable" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("shell did not execute in its worktree: %s, %v", data, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.Remove(proof); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalCreationFromPreparedBranchAndReopen(t *testing.T) {
	f := newTerminalCreationFixture(t)
	task := "touch task-must-not-execute\nTask details"
	h := f.create(t, task)
	if h.Prompt != task || h.Title != "touch task-must-not-execute" || h.BaseBranch != "feature/terminal" || h.BaseCommit != f.preparation.Commit || h.WorktreeBranch != "holark/"+h.ID {
		t.Fatalf("creation fallback or branch: %+v", h)
	}
	head := strings.TrimSpace(applicationLockGit(t, h.WorktreePath, "rev-parse", "HEAD"))
	if head != f.preparation.Commit {
		t.Fatalf("worktree HEAD=%s, want prepared commit %s", head, f.preparation.Commit)
	}
	assertTerminalShellUsable(t, f.manager, h)
	if _, err := os.Stat(filepath.Join(h.WorktreePath, "task-must-not-execute")); !os.IsNotExist(err) {
		t.Fatalf("task reached shell: %v", err)
	}
	ended, err := f.service.End(t.Context(), h.ID)
	if err != nil || ended.ArchivedAt == nil {
		t.Fatalf("end: %+v %v", ended, err)
	}
	reopened, err := f.service.Reopen(t.Context(), h.ID)
	if err != nil || reopened.Status != holons.StatusRunning || reopened.FinishedAt != nil || reopened.ArchivedAt != nil || len(reopened.AgentSessions) != 0 {
		t.Fatalf("reopen: %+v %v", reopened, err)
	}
	reopened, err = f.service.AddManualTerminal(t.Context(), h.ID, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	assertTerminalShellUsable(t, f.manager, reopened)
}

func TestTerminalCreationRecordsShellFailure(t *testing.T) {
	f := newTerminalCreationFixture(t)
	if h := f.create(t, ""); h.Title != "Holon" || h.WorktreeBranch != "holark/"+h.ID {
		t.Fatalf("empty task fallback: %+v", h)
	}
	f.manager.Close()
	h, err := f.service.Create(t.Context(), holons.Create{StartupMode: "terminal", BaseBranch: f.preparation.Branch, BaseCommit: f.preparation.Commit})
	if err == nil || h.Status != holons.StatusFailed || h.Reason == "" || h.FinishedAt == nil || len(h.AgentSessions) != 0 {
		t.Fatalf("failed creation: %+v %v", h, err)
	}
	if _, err := os.Stat(h.WorktreePath); err != nil {
		t.Fatalf("failed startup lost workspace: %v", err)
	}
	for _, terminal := range h.ManualTerminals {
		if terminal.ClosedAt == nil {
			t.Fatalf("failed shell remained open: %+v", terminal)
		}
	}
	stored, err := f.service.Get(t.Context(), h.ID)
	if err != nil || stored.Status != holons.StatusFailed || stored.Reason != h.Reason {
		t.Fatalf("persisted failure: %+v %v", stored, err)
	}
}
