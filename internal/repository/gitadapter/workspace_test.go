package gitadapter

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/repository"
)

func TestWorkspacePinnedToSelectedCommitAndIsolatesDirtyCheckout(t *testing.T) {
	repo, first := workspaceRepository(t)
	second := commitFile(t, repo, "selected.txt", "selected\n", "selected")
	gitTest(t, repo, "branch", "moving", second)
	if err := os.WriteFile(filepath.Join(repo, "selected.txt"), []byte("dirty source checkout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := openWorkspaceAdapter(t, repo)
	gitTest(t, repo, "branch", "-f", "moving", first)
	workspace, err := adapter.CreateWorkspace(context.Background(), "pinned", second)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Branch != "holark/pinned" || workspace.BaseCommit != second || gitText(t, workspace.Path, "rev-parse", "HEAD") != second {
		t.Fatalf("workspace = %+v, head = %s", workspace, gitText(t, workspace.Path, "rev-parse", "HEAD"))
	}
	content, err := os.ReadFile(filepath.Join(workspace.Path, "selected.txt"))
	if err != nil || string(content) != "selected\n" {
		t.Fatalf("worktree copied dirty checkout: %q, %v", content, err)
	}
}

func TestWorkspaceInspectionKeepsBranchAndWorkSessionBoundariesSeparate(t *testing.T) {
	repo, branchBase := workspaceRepository(t)
	gitTest(t, repo, "switch", "-c", "feature")
	workSessionStart := commitFile(t, repo, "branch.txt", "existing branch work\n", "existing branch work")
	gitTest(t, repo, "switch", "main")
	adapter := openWorkspaceAdapter(t, repo)
	workspace, err := adapter.CreateWorkspace(t.Context(), "continued", workSessionStart)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, workspace.Path, "current.txt", "current work\n", "current work")

	branch, err := adapter.InspectWorkspaceWithOptions(t.Context(), "continued", repository.InspectOptions{
		BaseBranch: "main", BranchBaseCommit: branchBase, BaseRef: "main", TargetRef: "worktree", SummaryOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if branch.BranchBaseCommit != branchBase || branch.WorkSessionStartCommit != workSessionStart {
		t.Fatalf("branch inspection boundaries = base %q start %q", branch.BranchBaseCommit, branch.WorkSessionStartCommit)
	}
	if len(branch.Files) != 2 || branch.Files[0].Path != "branch.txt" || branch.Files[1].Path != "current.txt" {
		t.Fatalf("branch files = %+v, want existing and current work", branch.Files)
	}

	current, err := adapter.InspectWorkspaceWithOptions(t.Context(), "continued", repository.InspectOptions{
		BaseBranch: "main", BranchBaseCommit: branchBase, BaseRef: "session-start", TargetRef: "worktree", SummaryOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Files) != 1 || current.Files[0].Path != "current.txt" {
		t.Fatalf("current-session files = %+v, want only current work", current.Files)
	}
}

func TestWorkspaceInspectionTracksManualRebases(t *testing.T) {
	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "provider with stale local main"
		}
		t.Run(name, func(t *testing.T) {
			repo, originalBase := workspaceRepository(t)
			provider := repo
			if remote {
				origin := filepath.Join(t.TempDir(), "origin.git")
				gitTest(t, repo, "init", "--bare", origin)
				gitTest(t, repo, "remote", "add", "origin", origin)
				gitTest(t, repo, "push", "origin", "main")
				provider = filepath.Join(t.TempDir(), "provider")
				gitTest(t, repo, "clone", "-b", "main", origin, provider)
				gitTest(t, provider, "config", "user.name", "Provider")
				gitTest(t, provider, "config", "user.email", "provider@example.com")
			}
			adapter := openWorkspaceAdapter(t, repo)
			if _, err := adapter.Refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			gitTest(t, repo, "switch", "-c", "parent")
			parent := commitFile(t, repo, "parent.txt", "parent work\n", "P")
			workspace, err := adapter.CreateWorkspace(t.Context(), "continued", parent)
			if err != nil {
				t.Fatal(err)
			}
			head := commitFile(t, workspace.Path, "holon.txt", "holon work\n", "H")
			options := repository.InspectOptions{
				BaseBranch: "main", BranchBaseCommit: originalBase, WorkSessionStartCommit: parent,
			}
			check := func(t *testing.T, base, sessionStart, parentHead, holonHead string) {
				t.Helper()
				for _, full := range []bool{false, true} {
					selected := options
					if full {
						selected.BaseRef = "main"
					}
					inspection, err := adapter.InspectWorkspaceWithOptions(t.Context(), workspace.ID, selected)
					if err != nil {
						t.Fatal(err)
					}
					if inspection.BranchBaseCommit != base || inspection.WorkSessionStartCommit != sessionStart {
						t.Fatalf("boundaries = %q, %q; want %q, %q", inspection.BranchBaseCommit, inspection.WorkSessionStartCommit, base, sessionStart)
					}
					if inspection.Workspace.BaseCommit != parent {
						t.Fatalf("recorded creation base changed: %q", inspection.Workspace.BaseCommit)
					}
					if len(inspection.Commits) != 2 || inspection.Commits[0].SHA != holonHead || inspection.Commits[1].SHA != parentHead || inspection.Commits[1].ParentCommit != base {
						t.Fatalf("timeline = %+v, want H then P above %s", inspection.Commits, base)
					}
					wantBase := sessionStart
					wantFiles := "holon.txt"
					if full || sessionStart == base {
						wantBase = base
						wantFiles += ",parent.txt"
					}
					var files []string
					for _, file := range inspection.Files {
						files = append(files, file.Path)
						if file.Diff == "" {
							t.Fatalf("missing diff for %s", file.Path)
						}
					}
					if inspection.SelectedBaseCommit != wantBase || strings.Join(files, ",") != wantFiles {
						t.Fatalf("full=%v: selected base=%q files=%v, want %q %s", full, inspection.SelectedBaseCommit, files, wantBase, wantFiles)
					}
				}
			}
			check(t, originalBase, parent, parent, head)

			gitTest(t, provider, "switch", "main")
			newBase := commitFile(t, provider, "main.txt", "main work\n", "D")
			if remote {
				gitTest(t, provider, "push", "origin", "main")
				if _, err := adapter.Refresh(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			gitTest(t, repo, "switch", "parent")
			gitTest(t, repo, "rebase", newBase)
			rebasedParent := gitText(t, repo, "rev-parse", "HEAD")
			gitTest(t, workspace.Path, "rebase", "--onto", "parent", parent)
			rebasedHead := gitText(t, workspace.Path, "rev-parse", "HEAD")
			check(t, newBase, newBase, rebasedParent, rebasedHead)

			gitTest(t, provider, "switch", "main")
			commitFile(t, provider, "later.txt", "later main work\n", "E")
			if remote {
				gitTest(t, provider, "push", "origin", "main")
				if _, err := adapter.Refresh(t.Context()); err != nil {
					t.Fatal(err)
				}
				if got := gitText(t, repo, "rev-parse", "main"); got != originalBase {
					t.Fatalf("local main moved: %q", got)
				}
				// Inspection must still work when the provider is offline.
				gitTest(t, repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "unavailable.git"))
			}
			check(t, newBase, newBase, rebasedParent, rebasedHead)
		})
	}
}

func TestWorkspaceInspectionFallsBackWhenBaseRefIsMissing(t *testing.T) {
	repo, base := workspaceRepository(t)
	adapter := openWorkspaceAdapter(t, repo)
	gitTest(t, repo, "switch", "-c", "parent")
	parent := commitFile(t, repo, "parent.txt", "parent\n", "parent")
	workspace, err := adapter.CreateWorkspace(t.Context(), "missing-base", parent)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, workspace.Path, "holon.txt", "holon\n", "holon")
	gitTest(t, repo, "branch", "-D", "main")
	for _, recordedBase := range []string{"", base} {
		inspection, err := adapter.InspectWorkspaceWithOptions(t.Context(), workspace.ID, repository.InspectOptions{
			BranchBaseCommit: recordedBase, BaseRef: "main",
		})
		if err != nil {
			t.Fatal(err)
		}
		wantBase, wantCount := base, 2
		if recordedBase == "" {
			wantBase, wantCount = parent, 1
		}
		if inspection.BranchBaseCommit != wantBase || inspection.WorkSessionStartCommit != parent || len(inspection.Commits) != wantCount || len(inspection.Files) != wantCount {
			t.Fatalf("fallback inspection = %+v", inspection)
		}
	}
}

func TestWorkspaceInspectionRejectsUnrelatedBaseHistory(t *testing.T) {
	repo, base := workspaceRepository(t)
	adapter := openWorkspaceAdapter(t, repo)
	workspace, err := adapter.CreateWorkspace(t.Context(), "unrelated", base)
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "switch", "--orphan", "replacement")
	commitFile(t, repo, "unrelated.txt", "unrelated\n", "unrelated root")
	gitTest(t, repo, "branch", "-f", "main", "HEAD")
	if _, err := adapter.InspectWorkspace(t.Context(), workspace.ID); !errors.Is(err, repository.ErrRepositoryUnavailable) {
		t.Fatalf("unrelated base error = %v, want repository unavailable", err)
	}
}

func TestProviderBranchResolutionPreservesFailures(t *testing.T) {
	repo, _ := workspaceRepository(t)
	adapter := openWorkspaceAdapter(t, repo)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := adapter.ResolveProviderBranch(ctx, "main"); !errors.Is(err, context.Canceled) || errors.Is(err, repository.ErrRefNotFound) {
		t.Fatalf("canceled resolution = %v", err)
	}
	if err := os.Rename(repo, repo+"-moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ResolveProviderBranch(t.Context(), "main"); !errors.Is(err, repository.ErrRepositoryUnavailable) || errors.Is(err, repository.ErrRefNotFound) {
		t.Fatalf("unavailable repository resolution = %v", err)
	}
}

func TestWorkspaceLocallyExcludesHolarkControlFiles(t *testing.T) {
	repo, base := workspaceRepository(t)
	adapter := openWorkspaceAdapter(t, repo)
	workspace, err := adapter.CreateWorkspace(t.Context(), "control-files", base)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(workspace.Path, ".holark", "comment-reply.json")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, []byte(`{"reply":"done"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := gitText(t, workspace.Path, "status", "--short", "--untracked-files=all"); status != "" {
		t.Fatalf("control artifact appears in status: %q", status)
	}
	if commandSucceeds(workspace.Path, "add", ".holark/comment-reply.json") {
		t.Fatal("explicit git add staged an excluded control artifact")
	}
	gitTest(t, workspace.Path, "add", ".")
	if tracked := gitText(t, workspace.Path, "ls-files", ".holark"); tracked != "" {
		t.Fatalf("control artifact was staged: %q", tracked)
	}
	if data, err := os.ReadFile(artifactPath); err != nil || string(data) != `{"reply":"done"}` {
		t.Fatalf("control artifact is not readable: %q, %v", data, err)
	}
}

func TestPublishRejectsForceAddedHolarkControlFile(t *testing.T) {
	repo, base := workspaceRepository(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	gitTest(t, t.TempDir(), "init", "--bare", remote)
	gitTest(t, repo, "remote", "add", "origin", remote)
	gitTest(t, repo, "push", "origin", base+":refs/heads/main")
	adapter := openWorkspaceAdapter(t, repo)
	workspace, err := adapter.CreateWorkspace(t.Context(), "forced-control-file", base)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(workspace.Path, ".holark", "comment-reply.json")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, []byte(`{"reply":"done"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, workspace.Path, "add", "-f", ".holark/comment-reply.json")
	gitTest(t, workspace.Path, "commit", "-m", "force add control artifact")
	_, err = adapter.PublishWorkspace(t.Context(), repository.PublishRequest{
		WorkspaceID: "forced-control-file", Remote: "origin", UpstreamBranch: "main", ExpectedRemoteHead: base,
		IgnoredPaths: []string{".holark/comment-reply.json"},
	})
	if !errors.Is(err, repository.ErrIgnoredPathCommitted) {
		t.Fatalf("publish error=%v, want ignored path committed", err)
	}
	if remoteHead := gitText(t, remote, "rev-parse", "refs/heads/main"); remoteHead != base {
		t.Fatalf("remote head=%q, want %q", remoteHead, base)
	}
}

func TestWorkspaceInspectPublishLeaseBackupAndRemoval(t *testing.T) {
	repo, base := workspaceRepository(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	gitTest(t, t.TempDir(), "init", "--bare", remote)
	gitTest(t, repo, "remote", "add", "origin", remote)
	gitTest(t, repo, "push", "origin", base+":refs/heads/main")
	adapter := openWorkspaceAdapter(t, repo)
	w, err := adapter.CreateWorkspace(context.Background(), "publish", base)
	if err != nil {
		t.Fatal(err)
	}
	head := commitFile(t, w.Path, "change.txt", "change\n", "change")
	artifactPath := ".holark/comment-reply.json"
	if err := os.MkdirAll(filepath.Join(w.Path, ".holark"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Path, artifactPath), []byte(`{"reply":"done"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	inspection, err := adapter.InspectWorkspaceWithOptions(context.Background(), "publish", repository.InspectOptions{IgnoredPaths: []string{artifactPath}})
	if err != nil || !inspection.Clean || inspection.Workspace.HeadCommit != head || len(inspection.Changes) != 1 {
		t.Fatalf("inspection = %+v, err = %v", inspection, err)
	}
	published, err := adapter.PublishWorkspace(context.Background(), repository.PublishRequest{WorkspaceID: "publish", Remote: "origin", UpstreamBranch: "main", ExpectedRemoteHead: base, IgnoredPaths: []string{artifactPath}})
	if err != nil || published.HeadCommit != head || gitText(t, remote, "rev-parse", "refs/heads/main") != head {
		t.Fatalf("published = %+v, err = %v", published, err)
	}
	if _, err := adapter.PublishWorkspace(context.Background(), repository.PublishRequest{WorkspaceID: "publish", Remote: "origin", UpstreamBranch: "main", ExpectedRemoteHead: base, IgnoredPaths: []string{artifactPath}}); err != nil {
		t.Fatalf("idempotent publication retry: %v", err)
	}
	other := filepath.Join(t.TempDir(), "other")
	gitTest(t, t.TempDir(), "clone", "-b", "main", remote, other)
	gitTest(t, other, "config", "user.name", "Test")
	gitTest(t, other, "config", "user.email", "test@example.com")
	newRemote := commitFile(t, other, "remote.txt", "remote\n", "remote")
	gitTest(t, other, "push", "origin", "HEAD:main")
	_, err = adapter.PublishWorkspace(context.Background(), repository.PublishRequest{WorkspaceID: "publish", Remote: "origin", UpstreamBranch: "main", ExpectedRemoteHead: head, IgnoredPaths: []string{artifactPath}})
	if !errors.Is(err, repository.ErrStaleHead) || gitText(t, remote, "rev-parse", "refs/heads/main") != newRemote {
		t.Fatalf("stale publish error = %v", err)
	}
	backup := "holark/backups/publish"
	if got, err := adapter.BackupWorkspace(context.Background(), "publish", "origin", backup); err != nil || got != head {
		t.Fatalf("backup = %s, %v", got, err)
	}
	if _, err := adapter.BackupWorkspace(context.Background(), "publish", "origin", backup); !errors.Is(err, repository.ErrBackupExists) {
		t.Fatalf("second backup error = %v", err)
	}
	if gitText(t, remote, "rev-parse", "refs/heads/"+backup) != head {
		t.Fatal("backup was overwritten")
	}
	if err := os.Remove(filepath.Join(w.Path, artifactPath)); err != nil {
		t.Fatal(err)
	}
	if err := adapter.RemoveWorkspace(context.Background(), "publish"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.RemoveWorkspace(context.Background(), "publish"); err != nil {
		t.Fatalf("idempotent removal: %v", err)
	}
	if _, err := os.Stat(w.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree remains: %v", err)
	}
	if commandSucceeds(repo, "show-ref", "--verify", "refs/heads/holark/publish") {
		t.Fatal("managed branch remains")
	}
}

func TestWorkspaceOwnershipContainmentAndCleanValidation(t *testing.T) {
	repo, base := workspaceRepository(t)
	adapter := openWorkspaceAdapter(t, repo)
	if _, err := adapter.CreateWorkspace(context.Background(), "../escape", base); !errors.Is(err, repository.ErrInvalidWorkspace) {
		t.Fatalf("escape create error = %v", err)
	}
	w, err := adapter.CreateWorkspace(context.Background(), "owned", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Path, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.PublishWorkspace(context.Background(), repository.PublishRequest{WorkspaceID: "owned", Remote: "origin", UpstreamBranch: "main"}); !errors.Is(err, repository.ErrWorkspaceDirty) {
		t.Fatalf("dirty publish error = %v", err)
	}
	if err := adapter.RemoveWorkspace(context.Background(), "owned"); !errors.Is(err, repository.ErrWorkspaceDirty) {
		t.Fatalf("dirty removal error = %v", err)
	}
	if err := os.Remove(filepath.Join(filepath.Dir(w.Path), "owner")); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.InspectWorkspace(context.Background(), "owned"); !errors.Is(err, repository.ErrWorkspaceOwner) {
		t.Fatalf("missing owner error = %v", err)
	}
	if err := adapter.ArchiveWorkspace(context.Background(), w.ID, w.Branch); !errors.Is(err, repository.ErrWorkspaceOwner) {
		t.Fatalf("archive with missing owner error = %v", err)
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Fatalf("unowned worktree was removed: %v", err)
	}
	if !commandSucceeds(repo, "show-ref", "--verify", "refs/heads/"+w.Branch) {
		t.Fatal("unowned workspace branch was removed")
	}
}

func TestWorkspaceArchiveKeepsHeadReachableAndRetries(t *testing.T) {
	repo, base := workspaceRepository(t)
	adapter := openWorkspaceAdapter(t, repo)
	w, err := adapter.CreateWorkspace(t.Context(), "archived", base)
	if err != nil {
		t.Fatal(err)
	}
	head := commitFile(t, w.Path, "change.txt", "committed\n", "committed work")
	if err := adapter.ArchiveWorkspace(t.Context(), w.ID, w.Branch); err != nil {
		t.Fatal(err)
	}
	if got := gitText(t, repo, "rev-parse", "refs/holark/holons/archived/head"); got != head {
		t.Fatalf("archived head = %q, want %q", got, head)
	}
	if commandSucceeds(repo, "show-ref", "--verify", "refs/holark/holons/archived/snapshot") {
		t.Fatal("clean workspace has a snapshot ref")
	}
	if _, err := os.Stat(w.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("parked worktree still exists: %v", err)
	}
	if commandSucceeds(repo, "show-ref", "--verify", "refs/heads/"+w.Branch) {
		t.Fatal("parked branch still exists")
	}
	gitTest(t, repo, "branch", w.Branch, head)
	if err = adapter.ArchiveWorkspace(t.Context(), w.ID, w.Branch); err != nil {
		t.Fatalf("partial archive retry: %v", err)
	}
	if commandSucceeds(repo, "show-ref", "--verify", "refs/heads/"+w.Branch) {
		t.Fatal("partial archive retry left branch")
	}
	gitTest(t, repo, "reflog", "expire", "--expire=now", "--all")
	gitTest(t, repo, "gc", "--prune=now")
	if got := gitText(t, repo, "rev-parse", "refs/holark/holons/archived/head"); got != head {
		t.Fatalf("archived head = %q, want %q", got, head)
	}
	if err = adapter.ArchiveWorkspace(t.Context(), w.ID, w.Branch); err != nil {
		t.Fatalf("archive retry: %v", err)
	}
}

func TestWorkspaceArchiveAndReopenRoundTrip(t *testing.T) {
	repo, base := workspaceRepository(t)
	adapter := openWorkspaceAdapter(t, repo)
	w, err := adapter.CreateWorkspace(t.Context(), "dirty-archive", base)
	if err != nil {
		t.Fatal(err)
	}
	head := commitFile(t, w.Path, "deleted.txt", "delete me\n", "committed workspace work")
	if err = os.WriteFile(filepath.Join(w.Path, "README.md"), []byte("modified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(w.Path, "untracked.sh"), []byte("#!/bin/sh\necho preserved\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, w.Path, "add", "README.md")
	if err = os.Remove(filepath.Join(w.Path, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	if err := adapter.ArchiveWorkspace(t.Context(), w.ID, w.Branch); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(w.Path); !errors.Is(err, os.ErrNotExist) || commandSucceeds(repo, "show-ref", "--verify", "refs/heads/"+w.Branch) {
		t.Fatalf("archive retained checkout or branch: stat=%v", err)
	}
	snapshot := gitText(t, repo, "rev-parse", "refs/holark/holons/dirty-archive/snapshot")
	if err = adapter.ReopenWorkspace(t.Context(), w.ID, w.Branch); err != nil {
		t.Fatal(err)
	}
	if got := gitText(t, w.Path, "rev-parse", "HEAD"); got != head {
		t.Fatalf("reopened head = %q, want %q", got, head)
	}
	if got := gitText(t, w.Path, "status", "--porcelain=v1", "--untracked-files=all"); got != "M README.md\n D deleted.txt\n?? untracked.sh" {
		t.Fatalf("reopened status = %q", got)
	}
	if data, readErr := os.ReadFile(filepath.Join(w.Path, "README.md")); readErr != nil || string(data) != "modified\n" {
		t.Fatalf("reopened modification = %q, %v", data, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(w.Path, "deleted.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("deleted file restored: %v", statErr)
	}
	if info, statErr := os.Stat(filepath.Join(w.Path, "untracked.sh")); statErr != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("reopened executable = %+v, %v", info, statErr)
	}
	if got := gitText(t, repo, "rev-parse", "refs/holark/holons/dirty-archive/head"); got != head {
		t.Fatalf("archive head ref changed = %q", got)
	}
	if got := gitText(t, repo, "rev-parse", "refs/holark/holons/dirty-archive/snapshot"); got != snapshot {
		t.Fatalf("archive snapshot ref changed = %q", got)
	}
	if err = adapter.ReopenWorkspace(t.Context(), w.ID, w.Branch); err != nil {
		t.Fatalf("retry reopened workspace: %v", err)
	}
}

func TestWorkspaceArchiveAndReopenAfterGeneratedBranchRename(t *testing.T) {
	repo, base := workspaceRepository(t)
	gitTest(t, repo, "config", "user.name", "Jane Doe")
	adapter := openWorkspaceAdapter(t, repo)
	workspace, err := adapter.CreateWorkspace(t.Context(), "renamed-archive", base)
	if err != nil {
		t.Fatal(err)
	}
	branch, err := adapter.RenameWorkspace(t.Context(), workspace.ID, workspace.Branch, "rebase-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	if branch != "jane-doe/rebase-cleanup" {
		t.Fatalf("renamed branch = %q", branch)
	}
	head := commitFile(t, workspace.Path, "rebased.txt", "rebased\n", "rebase work")
	if err = adapter.ArchiveWorkspace(t.Context(), workspace.ID, branch); err != nil {
		t.Fatal(err)
	}
	if commandSucceeds(repo, "show-ref", "--verify", "refs/heads/"+branch) {
		t.Fatal("renamed branch remains after archival")
	}
	if err = adapter.ReopenWorkspace(t.Context(), workspace.ID, branch); err != nil {
		t.Fatal(err)
	}
	if got := gitText(t, workspace.Path, "symbolic-ref", "--short", "HEAD"); got != branch {
		t.Fatalf("reopened branch = %q, want %q", got, branch)
	}
	if got := gitText(t, workspace.Path, "rev-parse", "HEAD"); got != head {
		t.Fatalf("reopened head = %q, want %q", got, head)
	}
}

func TestWorkspaceArchiveRejectsDirtySubmoduleWithSpaces(t *testing.T) {
	repo, base := workspaceRepository(t)
	adapter := openWorkspaceAdapter(t, repo)
	w, err := adapter.CreateWorkspace(t.Context(), "dirty-submodule", base)
	if err != nil {
		t.Fatal(err)
	}
	submodule := t.TempDir()
	gitTest(t, submodule, "init")
	gitTest(t, submodule, "config", "user.email", "test@example.com")
	gitTest(t, submodule, "config", "user.name", "Test")
	if err = os.WriteFile(filepath.Join(submodule, "tracked.txt"), []byte("clean\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, submodule, "add", "tracked.txt")
	gitTest(t, submodule, "commit", "-m", "submodule base")
	gitTest(t, w.Path, "-c", "protocol.file.allow=always", "submodule", "add", submodule, "module with spaces")
	gitTest(t, w.Path, "commit", "-am", "add submodule")
	if err = os.WriteFile(filepath.Join(w.Path, "module with spaces", "tracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = adapter.ArchiveWorkspace(t.Context(), w.ID, w.Branch); !errors.Is(err, repository.ErrWorkspaceDirty) {
		t.Fatalf("dirty submodule archive error = %v", err)
	}
	if _, err = os.Stat(w.Path); err != nil {
		t.Fatalf("dirty submodule worktree was removed: %v", err)
	}
}

func TestWorkspaceArchiveRejectsUntrackedEmbeddedRepository(t *testing.T) {
	repo, base := workspaceRepository(t)
	adapter := openWorkspaceAdapter(t, repo)
	w, err := adapter.CreateWorkspace(t.Context(), "embedded-repository", base)
	if err != nil {
		t.Fatal(err)
	}
	embedded := filepath.Join(w.Path, "embedded")
	gitTest(t, w.Path, "init", embedded)
	work := filepath.Join(embedded, "work.txt")
	if err = os.WriteFile(work, []byte("untracked work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = adapter.ArchiveWorkspace(t.Context(), w.ID, w.Branch); !errors.Is(err, repository.ErrWorkspaceDirty) {
		t.Fatalf("embedded repository archive error = %v", err)
	}
	if data, readErr := os.ReadFile(work); readErr != nil || string(data) != "untracked work\n" {
		t.Fatalf("embedded repository work was changed: %q, %v", data, readErr)
	}
	if !commandSucceeds(repo, "show-ref", "--verify", "refs/heads/"+w.Branch) {
		t.Fatal("embedded repository workspace branch was removed")
	}
}

func TestRenameWorkspaceAllocatesCollisionSafeGeneratedBranch(t *testing.T) {
	repo, base := workspaceRepository(t)
	gitTest(t, repo, "config", "user.name", "Jane Doe")
	adapter := openWorkspaceAdapter(t, repo)
	first, err := adapter.CreateWorkspace(t.Context(), "first", base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.CreateWorkspace(t.Context(), "second", base)
	if err != nil {
		t.Fatal(err)
	}
	one, err := adapter.RenameWorkspace(t.Context(), "first", first.Branch, "fix-tests")
	if err != nil {
		t.Fatal(err)
	}
	two, err := adapter.RenameWorkspace(t.Context(), "second", second.Branch, "fix-tests")
	if err != nil {
		t.Fatal(err)
	}
	if one != "jane-doe/fix-tests" || two != "jane-doe/fix-tests-2" {
		t.Fatalf("branches=%q,%q", one, two)
	}
	if got := gitText(t, first.Path, "symbolic-ref", "--short", "HEAD"); got != one {
		t.Fatalf("first branch=%q", got)
	}
	if inspected, err := adapter.InspectWorkspace(t.Context(), "first"); err != nil || inspected.Branch != one {
		t.Fatalf("inspection after rename = %+v, %v", inspected, err)
	}
	refs, err := adapter.Refs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ref := range refs {
		if ref.ShortName == one && ref.Kind == "holark_branch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("generated branch missing from refs: %+v", refs)
	}
}

func TestRenameWorkspaceFallsBackToHolarkWithoutGitUsername(t *testing.T) {
	repo, base := workspaceRepository(t)
	gitTest(t, repo, "config", "user.name", "")
	adapter := openWorkspaceAdapter(t, repo)
	workspace, err := adapter.CreateWorkspace(t.Context(), "fallback", base)
	if err != nil {
		t.Fatal(err)
	}
	branch, err := adapter.RenameWorkspace(t.Context(), "fallback", workspace.Branch, "fix-tests")
	if err != nil {
		t.Fatal(err)
	}
	if branch != "holark/fix-tests" {
		t.Fatalf("branch=%q", branch)
	}
}

func openWorkspaceAdapter(t *testing.T, path string) *Git {
	t.Helper()
	adapter, err := OpenWithWorktrees(context.Background(), path, filepath.Join(t.TempDir(), "holark-data", "worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func workspaceRepository(t *testing.T) (string, string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	gitTest(t, t.TempDir(), "init", "-b", "main", repo)
	gitTest(t, repo, "config", "user.name", "Test")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	return repo, commitFile(t, repo, "README.md", "base\n", "base")
}

func commitFile(t *testing.T, repo, name, content, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "add", name)
	gitTest(t, repo, "commit", "-m", message)
	return gitText(t, repo, "rev-parse", "HEAD")
}

func gitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func gitText(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func commandSucceeds(dir string, args ...string) bool {
	command := exec.Command("git", args...)
	command.Dir = dir
	return command.Run() == nil
}

func TestCommitRangeAndInspectionUseResolvedDiffBase(t *testing.T) {
	repo, base := workspaceRepository(t)
	firstMessage := "first\n\nExplain the first change.\nInclude another line.\n\nDescribe why it is needed."
	secondMessage := "second\n\nExplain the follow-up change.\n\nDescribe its impact."
	first := commitFile(t, repo, "first.txt", "first\n", firstMessage)
	head := commitFile(t, repo, "second.txt", "second\n", secondMessage)
	adapter := openWorkspaceAdapter(t, repo)
	commits, err := adapter.CommitRange(t.Context(), base, head)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 || commits[0].SHA != first || commits[1].SHA != head {
		t.Fatalf("commits=%+v", commits)
	}
	for i, wantMessage := range []string{firstMessage, secondMessage} {
		if commits[i].Message != wantMessage {
			t.Errorf("commit %d message=%q, want %q", i, commits[i].Message, wantMessage)
		}
		if commits[i].AuthorName != "Test" || commits[i].AuthorEmail != "test@example.com" || commits[i].AuthoredAt.IsZero() {
			t.Errorf("commit %d metadata=%+v", i, commits[i])
		}
	}
	inspection, err := adapter.InspectRange(t.Context(), "feature", "main", base, head)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Branch != "feature" || inspection.BaseCommit != base || inspection.HeadCommit != head || !inspection.HasChanges || inspection.Dirty || len(inspection.Files) != 2 {
		t.Fatalf("inspection=%+v", inspection)
	}
}

func TestInspectRangeLoadsAFileBeyondTheCombinedPatchLimit(t *testing.T) {
	repo, base := workspaceRepository(t)
	commitFile(t, repo, "first.txt", strings.Repeat("first line\n", 10000), "first")
	head := commitFile(t, repo, "second.txt", strings.Repeat("second line\n", 10000)+"second tail\n", "second")
	adapter := openWorkspaceAdapter(t, repo)

	combined, err := adapter.InspectRange(t.Context(), "feature", "main", base, head)
	if err != nil {
		t.Fatal(err)
	}
	if !combined.DiffTruncated || len(combined.Files) != 2 {
		t.Fatalf("combined inspection = %+v", combined)
	}

	selected, err := adapter.InspectRangeWithOptions(t.Context(), "feature", "main", base, head, repository.InspectOptions{Path: "second.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if selected.DiffTruncated || len(selected.Files) != 1 || selected.Files[0].DiffTruncated || !strings.Contains(selected.Files[0].Diff, "second tail") {
		t.Fatalf("selected inspection = %+v", selected)
	}
}
