package localapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
)

type movedRebaseWorkspace struct {
	holonRepositoryCoordinator
	moveTo string
}

func (r movedRebaseWorkspace) CreateWorkspace(ctx context.Context, id, commit string) (holons.Workspace, error) {
	// Create a real worktree at the wrong commit while returning the originally
	// requested metadata, so the guard must inspect Git rather than trust metadata.
	w, err := r.holonRepositoryCoordinator.CreateWorkspace(ctx, id, r.moveTo)
	w.BaseCommit = commit
	return w, err
}

func TestPRRebasePreparationPinsWorktreeAndInstructions(t *testing.T) {
	for _, scenario := range []string{"default", "custom", "missing target", "moved source"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			gitPlumbingCommand(t, root, "init", "-b", "main")
			gitPlumbingCommand(t, root, "config", "user.name", "Rebase Test")
			gitPlumbingCommand(t, root, "config", "user.email", "rebase@invalid")
			gitPlumbingCommand(t, root, "commit", "--allow-empty", "-m", "base")
			base := gitPlumbingOutput(t, root, "rev-parse", "HEAD")
			gitPlumbingCommand(t, root, "switch", "-c", "feature")
			gitPlumbingCommand(t, root, "commit", "--allow-empty", "-m", "source")
			head := gitPlumbingOutput(t, root, "rev-parse", "HEAD")
			gitPlumbingCommand(t, root, "switch", "main")
			gitPlumbingCommand(t, root, "commit", "--allow-empty", "-m", "target")
			target := gitPlumbingOutput(t, root, "rev-parse", "HEAD")
			remote := filepath.Join(t.TempDir(), "origin.git")
			gitPlumbingCommand(t, root, "clone", "--bare", root, remote)
			gitPlumbingCommand(t, root, "remote", "add", "origin", remote)
			adapter, err := gitadapter.OpenWithWorktrees(t.Context(), root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			repositories := repository.NewService(adapter)
			repo := holonRepositoryCoordinator{repositories}
			_, store := terminalTestService(t)
			service := holons.NewServiceWithRepository(store, repo)
			if scenario == "moved source" {
				service = holons.NewServiceWithRepository(store, movedRebaseWorkspace{repo, target})
			}
			if scenario == "missing target" {
				target = strings.Repeat("a", 40)
			}
			agents := &recordingAgentLauncher{}
			launcher := localWorkLauncher{holons: &terminalHolonService{Service: service, agents: agents}}
			custom := ""
			if scenario == "custom" {
				custom = "Keep the public interface compatible."
			}
			work := pullrequestwork.Work{SessionID: "reserved-rebase", Kind: pullrequestwork.KindRebase, HeadCommit: head, TargetBaseCommit: target}
			// A newer PR projection must not override the work's captured source.
			p := pullrequestwork.PullRequest{ID: "pr", HeadBranch: "feature", BaseBranch: "main", BaseCommit: target, DiffBaseCommit: base, HeadCommit: target}
			if err := launcher.Reserve(t.Context(), p, work); err != nil {
				t.Fatal(err)
			}
			reserved, err := service.Get(t.Context(), work.SessionID)
			if err != nil || reserved.WorktreePath != "" || len(reserved.AgentSessions) != 0 {
				t.Fatalf("invalid rebase reservation: %+v %v", reserved, err)
			}
			executionErr, persistenceErr := launcher.StartReserved(t.Context(), p, work, custom)
			id, err := work.SessionID, errors.Join(executionErr, persistenceErr)
			if scenario == "missing target" || scenario == "moved source" {
				if err == nil || !strings.Contains(err.Error(), "rebase preparation failed") || len(agents.options) != 0 {
					t.Fatalf("mismatched preparation launched: id=%s err=%v calls=%v", id, err, agents.options)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			h, err := service.Get(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if got := gitPlumbingOutput(t, h.WorktreePath, "rev-parse", "HEAD"); got != head {
				t.Fatalf("HEAD=%s want %s", got, head)
			}
			for _, part := range []string{"current prepared worktree", head, target, "published PR branch feature", "not a local branch to check out", "Do not fetch, query GitHub, switch branches or worktrees, or push", "preparation mismatch", ".holark/rebase_complete"} {
				if !strings.Contains(h.Prompt, part) {
					t.Fatalf("prompt missing %q: %s", part, h.Prompt)
				}
			}
			if custom != "" && !strings.Contains(h.Prompt, custom) {
				t.Fatal("custom instructions lost")
			}
			if _, err := os.Stat(filepath.Join(h.WorktreePath, ".holark/rebase_complete")); !os.IsNotExist(err) {
				t.Fatalf("completion marker already present: %v", err)
			}
			gitPlumbingCommand(t, root, "commit", "--allow-empty", "-m", "later target")
			laterTarget := gitPlumbingOutput(t, root, "rev-parse", "HEAD")
			gitPlumbingCommand(t, root, "push", "origin", "main")
			gitPlumbingCommand(t, root, "switch", "feature")
			gitPlumbingCommand(t, root, "commit", "--allow-empty", "-m", "later source")
			laterSource := gitPlumbingOutput(t, root, "rev-parse", "HEAD")
			gitPlumbingCommand(t, root, "push", "origin", "feature")
			if _, err := repositories.Refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := gitPlumbingOutput(t, root, "rev-parse", "refs/holark/browse/origin/main"); got != laterTarget {
				t.Fatal("browsing did not refresh target")
			}
			if got := gitPlumbingOutput(t, root, "rev-parse", "refs/holark/browse/origin/feature"); got != laterSource {
				t.Fatal("browsing did not refresh source")
			}
			inspection, err := adapter.InspectSynchronization(t.Context(), id, target, nil)
			if err != nil || inspection.HeadCommit != head {
				t.Fatalf("browsing changed prepared inputs: %+v %v", inspection, err)
			}
			stored, _ := service.Get(t.Context(), id)
			if stored.Prompt != h.Prompt || work.TargetBaseCommit != target {
				t.Fatal("prepared target changed")
			}
		})
	}
}
