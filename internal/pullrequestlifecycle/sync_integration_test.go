package pullrequestlifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/holark-ai/holark/internal/testfixture/branchfixture"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	githubprovider "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	pullrequestssqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
	"github.com/holark-ai/holark/internal/repositorybrowser"
)

func TestCoordinatorSyncReconcilesImportedAndRebasedPullRequestTopology(t *testing.T) {
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repository")
	remotePath := filepath.Join(root, "remote.git")
	if err := os.Mkdir(repositoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSyncFile(t, repositoryPath, "README.md", "pull request sync\n")
	gitSync(t, repositoryPath, "init", "-b", "main")
	gitSync(t, repositoryPath, "add", "README.md")
	gitSync(t, repositoryPath, "-c", "user.name=Holark Sync", "-c", "user.email=sync@invalid", "commit", "-m", "Initial commit")
	originalBase := gitSync(t, repositoryPath, "rev-parse", "HEAD")
	gitSync(t, repositoryPath, "init", "--bare", "-b", "main", remotePath)
	gitSync(t, repositoryPath, "remote", "add", "origin", remotePath)
	gitSync(t, repositoryPath, "push", "-u", "origin", "main")

	gitSync(t, repositoryPath, "checkout", "-b", "feature")
	writeSyncFile(t, repositoryPath, "feature.txt", "feature work\n")
	gitSync(t, repositoryPath, "add", "feature.txt")
	gitSync(t, repositoryPath, "-c", "user.name=Holark Sync", "-c", "user.email=sync@invalid", "commit", "-m", "Add feature")
	featureHead := gitSync(t, repositoryPath, "rev-parse", "HEAD")
	gitSync(t, repositoryPath, "push", "-u", "origin", "feature")

	gitSync(t, repositoryPath, "checkout", "main")
	writeSyncFile(t, repositoryPath, "upstream.txt", "upstream work\n")
	gitSync(t, repositoryPath, "add", "upstream.txt")
	gitSync(t, repositoryPath, "-c", "user.name=Holark Sync", "-c", "user.email=sync@invalid", "commit", "-m", "Advance main")
	mainHead := gitSync(t, repositoryPath, "rev-parse", "HEAD")
	gitSync(t, repositoryPath, "push", "origin", "main")

	gitRepository, err := gitadapter.Open(t.Context(), repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	repositories := repository.NewService(gitRepository)
	db, err := database.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`create table repositories(id text primary key)`); err != nil {
		t.Fatal(err)
	}
	repositoryID := repositories.Descriptor().ID
	if _, err = db.Exec(`insert into repositories(id) values(?)`, repositoryID); err != nil {
		t.Fatal(err)
	}
	store, err := pullrequestssqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	github := &syncGitHub{pullRequests: []pullrequestlifecycle.GitHubPullRequest{{
		Title: "Imported pull request", BaseBranch: "main", BaseCommit: originalBase,
		HeadBranch: "feature", HeadCommit: featureHead, Status: pullrequestlifecycle.StatusOpen,
		ExternalID: "github:owner/repository#1", SyncData: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
	}}}
	for i := range github.pullRequests {
		github.pullRequests[i].SyncData, _ = json.Marshal(map[string]any{"github": map[string]string{"head_repository_url": remotePath, "base_repository_url": remotePath}})
	}
	projects := pullrequestlifecycle.ProjectLookupFunc(func(id string) (pullrequestlifecycle.Project, bool) {
		return pullrequestlifecycle.Project{ID: repositoryID, RepositoryURL: remotePath, DefaultBranch: "main", GitHubBacked: true}, id == repositoryID
	})
	scheduler := pullrequestlifecycle.NewRefreshCoordinator()
	t.Cleanup(scheduler.Close)
	coordinator := pullrequestlifecycle.New(store, pullrequestlifecycle.Options{Refresh: scheduler,
		Projects: projects, Repository: syncRepository{Service: repositories}, GitHubTransport: github, GitHubCodec: githubprovider.New(nil),
	})

	first, err := coordinator.Sync(t.Context(), repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Imported != 1 || len(first.PullRequests) != 1 {
		t.Fatalf("first sync = %+v", first)
	}
	imported, err := syncCached(coordinator, t.Context(), first.PullRequests[0])
	if err != nil {
		t.Fatal(err)
	}
	if imported.BaseCommit != mainHead || imported.DiffBaseCommit != originalBase || imported.HeadCommit != featureHead {
		t.Fatalf("imported commits: base=%s diff base=%s head=%s; want base=%s diff base=%s head=%s",
			imported.BaseCommit, imported.DiffBaseCommit, imported.HeadCommit, mainHead, originalBase, featureHead)
	}

	gitSync(t, repositoryPath, "checkout", "feature")
	gitSync(t, repositoryPath, "fetch", "origin", "main")
	gitSync(t, repositoryPath, "rebase", "origin/main")
	rebasedHead := gitSync(t, repositoryPath, "rev-parse", "HEAD")
	gitSync(t, repositoryPath, "push", "--force", "origin", "feature")
	github.pullRequests[0].HeadCommit = rebasedHead
	github.pullRequests[0].UpdatedAt = now.Add(time.Minute)

	second, err := coordinator.Sync(t.Context(), repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Updated != 1 || len(second.PullRequests) != 1 {
		t.Fatalf("second sync = %+v", second)
	}
	updated, err := syncCached(coordinator, t.Context(), second.PullRequests[0])
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != imported.ID || updated.BaseCommit != mainHead || updated.DiffBaseCommit != mainHead || updated.HeadCommit != rebasedHead {
		t.Fatalf("updated commits: id=%s base=%s diff base=%s head=%s; want id=%s base=%s diff base=%s head=%s",
			updated.ID, updated.BaseCommit, updated.DiffBaseCommit, updated.HeadCommit, imported.ID, mainHead, mainHead, rebasedHead)
	}

	// A branch with the same name in the local checkout may contain unpublished
	// work. The provider head must continue to describe the published PR.
	writeSyncFile(t, repositoryPath, "unpublished.txt", "local work only\n")
	gitSync(t, repositoryPath, "add", "unpublished.txt")
	gitSync(t, repositoryPath, "-c", "user.name=Holark Sync", "-c", "user.email=sync@invalid", "commit", "-m", "Unpublished local work")
	unpublishedHead := gitSync(t, repositoryPath, "rev-parse", "HEAD")
	third, err := coordinator.Sync(t.Context(), repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if third.PullRequests[0].HeadCommit != rebasedHead || third.PullRequests[0].HeadCommit == unpublishedHead {
		t.Fatalf("unpublished local head replaced remote observation: %+v", third.PullRequests[0])
	}

	// A base branch may advance after the provider observation was captured.
	// Historical discovery retains display; full synchronization reads the actual ref.
	gitSync(t, repositoryPath, "checkout", "main")
	writeSyncFile(t, repositoryPath, "later-base.txt", "base advanced after observation\n")
	gitSync(t, repositoryPath, "add", "later-base.txt")
	gitSync(t, repositoryPath, "-c", "user.name=Holark Sync", "-c", "user.email=sync@invalid", "commit", "-m", "Later base commit")
	gitSync(t, repositoryPath, "push", "origin", "main")
	laterBase := gitSync(t, repositoryPath, "rev-parse", "HEAD")
	fourth, err := coordinator.Sync(t.Context(), repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if fourth.PullRequests[0].BaseCommit != mainHead || fourth.PullRequests[0].DiffBaseCommit != mainHead {
		t.Fatalf("moving base replaced pinned inputs: %+v", fourth.PullRequests[0])
	}
	var overlapping sync.WaitGroup
	results := make(chan error, 12)
	for range 6 {
		overlapping.Add(2)
		go func() {
			defer overlapping.Done()
			_, err := coordinator.SyncActive(t.Context(), repositoryID)
			results <- err
		}()
		go func() {
			defer overlapping.Done()
			_, err := syncCached(coordinator, t.Context(), updated)
			results <- err
		}()
	}
	overlapping.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("overlapping unchanged inputs failed to converge: %v", err)
		}
	}
	accepted, _ := store.GetPullRequest(updated.ID)
	if !accepted.HasCurrentComparison() || accepted.HeadCommit != rebasedHead || accepted.BaseCommit != laterBase || accepted.DiffBaseCommit != mainHead {
		t.Fatalf("accepted overlap: %+v", accepted)
	}

}

func TestCoordinatorSyncContinuesAfterPullRequestTopologyFailure(t *testing.T) {
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repository")
	remotePath := filepath.Join(root, "remote.git")
	if err := os.Mkdir(repositoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSyncFile(t, repositoryPath, "README.md", "pull request sync\n")
	gitSync(t, repositoryPath, "init", "-b", "main")
	gitSync(t, repositoryPath, "add", "README.md")
	gitSync(t, repositoryPath, "-c", "user.name=Holark Sync", "-c", "user.email=sync@invalid", "commit", "-m", "Initial commit")
	mainHead := gitSync(t, repositoryPath, "rev-parse", "HEAD")
	gitSync(t, repositoryPath, "init", "--bare", "-b", "main", remotePath)
	gitSync(t, repositoryPath, "remote", "add", "origin", remotePath)
	gitSync(t, repositoryPath, "push", "-u", "origin", "main")

	gitSync(t, repositoryPath, "checkout", "-b", "feature")
	writeSyncFile(t, repositoryPath, "feature.txt", "feature work\n")
	gitSync(t, repositoryPath, "add", "feature.txt")
	gitSync(t, repositoryPath, "-c", "user.name=Holark Sync", "-c", "user.email=sync@invalid", "commit", "-m", "Add feature")
	featureHead := gitSync(t, repositoryPath, "rev-parse", "HEAD")
	gitSync(t, repositoryPath, "push", "-u", "origin", "feature")

	gitSync(t, repositoryPath, "checkout", "--orphan", "disconnected")
	gitSync(t, repositoryPath, "rm", "-rf", ".")
	writeSyncFile(t, repositoryPath, "historical.txt", "disconnected history\n")
	gitSync(t, repositoryPath, "add", "historical.txt")
	gitSync(t, repositoryPath, "-c", "user.name=Holark Sync", "-c", "user.email=sync@invalid", "commit", "-m", "Historical root")
	disconnectedHead := gitSync(t, repositoryPath, "rev-parse", "HEAD")
	gitSync(t, repositoryPath, "checkout", "main")

	gitRepository, err := gitadapter.Open(t.Context(), repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	repositories := repository.NewService(gitRepository)
	db, err := database.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`create table repositories(id text primary key)`); err != nil {
		t.Fatal(err)
	}
	repositoryID := repositories.Descriptor().ID
	if _, err = db.Exec(`insert into repositories(id) values(?)`, repositoryID); err != nil {
		t.Fatal(err)
	}
	store, err := pullrequestssqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	previous := pullrequestlifecycle.PullRequest{
		RepositoryID: repositoryID, Title: "Historical draft", BaseBranch: "old-main", BaseCommit: "stored-base",
		DiffBaseCommit: "stored-diff-base", HeadBranch: "historical", HeadCommit: disconnectedHead,
		Status: pullrequestlifecycle.StatusDraft, SyncProvider: string(pullrequestlifecycle.SyncProviderGitHub),
		SyncExternalID: "github:owner/repository#5", SyncData: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
	}
	previous.SyncData, _ = json.Marshal(map[string]any{"github": map[string]string{"head_repository_url": remotePath, "base_repository_url": remotePath}})
	if _, err := branchfixture.Create(t.Context(), store, previous); err != nil {
		t.Fatal(err)
	}

	github := &syncGitHub{pullRequests: []pullrequestlifecycle.GitHubPullRequest{
		{
			Title: "Historical open pull request", BaseBranch: "main", BaseCommit: mainHead,
			HeadBranch: "historical", HeadCommit: disconnectedHead, Status: pullrequestlifecycle.StatusOpen,
			ExternalID: previous.SyncExternalID, SyncData: []byte(`{}`), CreatedAt: now, UpdatedAt: now.Add(time.Minute),
		},
		{
			Title: "Current pull request", BaseBranch: "main", BaseCommit: mainHead,
			HeadBranch: "feature", HeadCommit: featureHead, Status: pullrequestlifecycle.StatusOpen,
			ExternalID: "github:owner/repository#12", SyncData: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
		},
	}}
	for i := range github.pullRequests {
		github.pullRequests[i].SyncData, _ = json.Marshal(map[string]any{"github": map[string]string{"head_repository_url": remotePath, "base_repository_url": remotePath}})
	}
	projects := pullrequestlifecycle.ProjectLookupFunc(func(id string) (pullrequestlifecycle.Project, bool) {
		return pullrequestlifecycle.Project{ID: repositoryID, RepositoryURL: remotePath, DefaultBranch: "main", GitHubBacked: true}, id == repositoryID
	})
	scheduler := pullrequestlifecycle.NewRefreshCoordinator()
	t.Cleanup(scheduler.Close)
	coordinator := pullrequestlifecycle.New(store, pullrequestlifecycle.Options{Refresh: scheduler,
		Projects: projects, Repository: syncRepository{Service: repositories}, GitHubTransport: github, GitHubCodec: githubprovider.New(nil),
	})

	// Explicit topology preparation preserves known inputs if this historical
	// head cannot be reconciled; the basic import can still process every PR.
	historicalBefore := store.ListPullRequests(repositoryID)[0]
	historicalPrepared, err := syncCached(coordinator, t.Context(), historicalBefore)
	if err == nil {
		t.Fatal("missing comparison preparation must fail synchronization")
	}
	if historicalPrepared.BaseCommit != previous.BaseCommit || historicalPrepared.DiffBaseCommit != previous.DiffBaseCommit {
		t.Fatalf("failed topology preparation replaced known inputs: %+v", historicalPrepared)
	}

	result, err := coordinator.Sync(t.Context(), repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 1 || result.Updated != 1 || len(result.PullRequests) != 2 {
		t.Fatalf("sync result = %+v", result)
	}
	byExternalID := make(map[string]pullrequestlifecycle.PullRequest)
	for _, pullRequest := range result.PullRequests {
		byExternalID[pullRequest.SyncExternalID] = pullRequest
	}
	historical := byExternalID[previous.SyncExternalID]
	if historical.Status != pullrequestlifecycle.StatusOpen || historical.Title != "Historical open pull request" {
		t.Fatalf("historical lifecycle = %+v", historical)
	}
	if historical.BaseBranch != "main" || historical.BaseCommit != previous.BaseCommit || historical.DiffBaseCommit != previous.DiffBaseCommit {
		t.Fatalf("historical topology = base branch %q, base %q, diff base %q; want observed %q, %q, %q",
			historical.BaseBranch, historical.BaseCommit, historical.DiffBaseCommit, "main", mainHead, "")
	}
	current, err := syncCached(coordinator, t.Context(), byExternalID["github:owner/repository#12"])
	if err != nil {
		t.Fatal(err)
	}
	if current.BaseCommit != mainHead || current.DiffBaseCommit != mainHead || current.HeadCommit != featureHead {
		t.Fatalf("current topology: base=%s diff base=%s head=%s; want base=%s diff base=%s head=%s",
			current.BaseCommit, current.DiffBaseCommit, current.HeadCommit, mainHead, mainHead, featureHead)
	}
}

type syncRepository struct{ *repository.Service }

func (value syncRepository) Refresh(ctx context.Context, _ repositorybrowser.Repository) error {
	_, err := value.Service.Refresh(ctx)
	return err
}

func (value syncRepository) ResolveRef(ctx context.Context, _ repositorybrowser.Repository, ref string) (string, error) {
	return value.Service.Resolve(ctx, ref)
}

type syncGitHub struct {
	pullRequests []pullrequestlifecycle.GitHubPullRequest
}

func (github *syncGitHub) List(context.Context, string) ([]pullrequestlifecycle.GitHubPullRequest, error) {
	return append([]pullrequestlifecycle.GitHubPullRequest(nil), github.pullRequests...), nil
}

func (github *syncGitHub) Get(_ context.Context, target pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubPullRequest, error) {
	for _, current := range github.pullRequests {
		if current.ExternalID == target.ExternalID {
			return current, nil
		}
	}
	return pullrequestlifecycle.GitHubPullRequest{}, errors.New("pull request not found")
}

func (*syncGitHub) Create(context.Context, pullrequestlifecycle.GitHubCreateRequest) (pullrequestlifecycle.GitHubPullRequest, error) {
	return pullrequestlifecycle.GitHubPullRequest{}, errors.New("not implemented")
}

func (*syncGitHub) UpdateState(context.Context, pullrequestlifecycle.GitHubPullRequestTarget, string) (pullrequestlifecycle.GitHubPullRequest, error) {
	return pullrequestlifecycle.GitHubPullRequest{}, errors.New("not implemented")
}

func (*syncGitHub) ConvertToDraft(context.Context, pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubPullRequest, error) {
	return pullrequestlifecycle.GitHubPullRequest{}, errors.New("not implemented")
}

func (*syncGitHub) MarkReadyForReview(context.Context, pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubPullRequest, error) {
	return pullrequestlifecycle.GitHubPullRequest{}, errors.New("not implemented")
}

func (g *syncGitHub) RefreshReadiness(_ context.Context, target pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubReadiness, error) {
	for _, p := range g.pullRequests {
		if p.ExternalID == target.ExternalID {
			return pullrequestlifecycle.GitHubReadiness{HeadCommit: p.HeadCommit, ChecksState: pullrequestlifecycle.GitHubChecksPassing, MergeabilityState: pullrequestlifecycle.GitHubMergeabilityMergeable}, nil
		}
	}
	return pullrequestlifecycle.GitHubReadiness{}, errors.New("missing PR")
}

func writeSyncFile(t *testing.T, root, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitSync(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func (github *syncGitHub) ListActive(ctx context.Context, repositoryID string) ([]pullrequestlifecycle.GitHubPullRequest, error) {
	all, err := github.List(ctx, repositoryID)
	active := all[:0]
	for _, current := range all {
		if current.Status.Active() {
			active = append(active, current)
		}
	}
	return active, err
}

func syncCached(c *pullrequestlifecycle.Coordinator, ctx context.Context, p pullrequestlifecycle.PullRequest) (pullrequestlifecycle.PullRequest, error) {
	err := c.SyncPullRequest(ctx, p.ID)
	current, _ := c.GetPullRequest(p.ID)
	return current, err
}
