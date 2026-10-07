package pullrequestlifecycle_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	githubprovider "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
	"github.com/holark-ai/holark/internal/testfixture/branchfixture"
)

type branchCacheFixture struct {
	source, remote, root, base, head string
	repo                             syncRepository
	store                            *prsqlite.Store
	coordinator                      *pr.Coordinator
	scheduler                        *pr.RefreshCoordinator
	pull                             pr.PullRequest
}

func newBranchCacheFixture(t *testing.T) *branchCacheFixture {
	t.Helper()
	f := &branchCacheFixture{source: t.TempDir(), root: t.TempDir(), remote: filepath.Join(t.TempDir(), "remote.git")}
	gitSync(t, f.source, "init", "-b", "main")
	gitSync(t, f.source, "config", "user.name", "Cache Test")
	gitSync(t, f.source, "config", "user.email", "cache@invalid")
	gitSync(t, f.source, "commit", "--allow-empty", "-m", "base")
	f.base = gitSync(t, f.source, "rev-parse", "HEAD")
	gitSync(t, f.source, "init", "--bare", "-b", "main", f.remote)
	gitSync(t, f.source, "remote", "add", "origin", f.remote)
	gitSync(t, f.source, "push", "origin", "main")
	gitSync(t, f.root, "clone", f.remote, ".")
	gitSync(t, f.source, "switch", "-c", "feature")
	gitSync(t, f.source, "commit", "--allow-empty", "-m", "feature")
	f.head = gitSync(t, f.source, "rev-parse", "HEAD")
	gitSync(t, f.source, "push", "origin", "feature")
	adapter, err := gitadapter.Open(t.Context(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.repo = syncRepository{repository.NewService(adapter)}
	db, err := database.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`create table repositories(id text primary key); insert into repositories values('project')`); err != nil {
		t.Fatal(err)
	}
	f.store, err = prsqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	f.pull, err = branchfixture.Create(t.Context(), f.store, pr.PullRequest{RepositoryID: "project", Title: "Cache", Status: pr.StatusOpen, BaseBranch: "main", HeadBranch: "feature", BaseCommit: f.base, HeadCommit: f.head, BaseRef: repository.PublishedBranchIdentity(f.remote, "main"), HeadRef: repository.PublishedBranchIdentity(f.remote, "feature")})
	if err != nil {
		t.Fatal(err)
	}
	f.scheduler = pr.NewRefreshCoordinator()
	t.Cleanup(f.scheduler.Close)
	f.coordinator = f.coordinatorWith(f.repo)
	return f
}
func (f *branchCacheFixture) coordinatorWith(repo pr.Repository) *pr.Coordinator {
	return pr.New(f.store, pr.Options{Repository: repo, Refresh: f.scheduler, Projects: pr.ProjectLookupFunc(func(id string) (pr.Project, bool) {
		return pr.Project{ID: "project", RepositoryURL: f.remote, DefaultBranch: "main"}, id == "project"
	})})
}
func (f *branchCacheFixture) advance(t *testing.T) string {
	t.Helper()
	gitSync(t, f.source, "commit", "--allow-empty", "-m", "advance")
	gitSync(t, f.source, "push", "origin", "feature")
	return gitSync(t, f.source, "rev-parse", "HEAD")
}
func cacheRef(branch string) string { return "refs/holark/browse/origin/" + branch }
func assertCached(t *testing.T, root, ref, want string) {
	t.Helper()
	if got := gitSync(t, root, "rev-parse", "--verify", ref); got != want {
		t.Fatalf("%s = %s, want %s", ref, got, want)
	}
}
func assertMissing(t *testing.T, root, ref string) {
	t.Helper()
	cmd := exec.Command("git", "-C", root, "rev-parse", "--verify", ref)
	if err := cmd.Run(); err == nil {
		t.Fatalf("unexpected local ref/object: %s", ref)
	}
}

func TestBranchCacheSyncRepairsMissingRefsAndWriteFailures(t *testing.T) {
	f := newBranchCacheFixture(t)
	assertMissing(t, f.root, f.head+"^{commit}")
	if err := f.coordinator.SyncPullRequest(t.Context(), f.pull.ID); err != nil {
		t.Fatal(err)
	}
	assertCached(t, f.root, cacheRef("feature"), f.head)
	assertCached(t, f.root, cacheRef("main"), f.base)
	assertCached(t, f.root, "refs/heads/main", f.base)
	assertCached(t, f.root, "refs/remotes/origin/main", f.base)
	gitSync(t, f.root, "update-ref", "-d", cacheRef("feature"))
	if err := f.coordinator.SyncPullRequest(t.Context(), f.pull.ID); err != nil {
		t.Fatal(err)
	}
	assertCached(t, f.root, cacheRef("feature"), f.head)
	next := f.advance(t)
	lock := filepath.Join(f.root, ".git", cacheRef("feature")+".lock")
	if err := os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.SyncPullRequest(t.Context(), f.pull.ID); err == nil {
		t.Fatal("ref failure reported successful synchronization")
	}
	inputs, err := f.store.CaptureComparison(t.Context(), f.pull.ID)
	if err != nil || inputs.Head.Commit != next {
		t.Fatalf("accepted evidence rolled back: %+v %v", inputs, err)
	}
	current, _ := f.store.GetPullRequest(f.pull.ID)
	if current.HasCurrentComparison() {
		t.Fatal("failed preparation left current comparison")
	}
	assertCached(t, f.root, cacheRef("feature"), f.head)
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.SyncPullRequest(t.Context(), f.pull.ID); err != nil {
		t.Fatal(err)
	}
	assertCached(t, f.root, cacheRef("feature"), next)
	gitSync(t, f.source, "push", "origin", "--delete", "feature")
	if err := f.coordinator.SyncPullRequest(t.Context(), f.pull.ID); err == nil {
		t.Fatal("deleted head prepared a comparison")
	}
	assertMissing(t, f.root, cacheRef("feature"))
	assertCached(t, f.root, cacheRef("main"), f.base)
}

type observedCacheRepository struct {
	syncRepository
	observe        func(context.Context, string, []string) (map[string]repository.BranchObservation, error)
	afterObserve   func(map[string]repository.BranchObservation)
	beforeCache    func(string)
	afterCache     func(string, error)
	afterMergeBase func()
}

func (r observedCacheRepository) ObserveBranches(ctx context.Context, source string, refs []string) (map[string]repository.BranchObservation, error) {
	var observed map[string]repository.BranchObservation
	var err error
	if r.observe != nil {
		observed, err = r.observe(ctx, source, refs)
	} else {
		observed, err = r.Service.ObserveBranches(ctx, source, refs)
	}
	if err == nil && r.afterObserve != nil {
		r.afterObserve(observed)
	}
	return observed, err
}
func (r observedCacheRepository) CachePublishedBranch(ctx context.Context, source, ref string, observed repository.BranchObservation) error {
	if r.beforeCache != nil {
		r.beforeCache(ref)
	}
	err := r.Service.CachePublishedBranch(ctx, source, ref, observed)
	if r.afterCache != nil {
		r.afterCache(ref, err)
	}
	return err
}

func (r observedCacheRepository) MergeBase(ctx context.Context, base, head string) (string, error) {
	mergeBase, err := r.Service.MergeBase(ctx, base, head)
	if err == nil && r.afterMergeBase != nil {
		r.afterMergeBase()
	}
	return mergeBase, err
}

func TestRejectedBranchObservationDoesNotOverwriteCache(t *testing.T) {
	f := newBranchCacheFixture(t)
	newer := f.advance(t)
	// Publish a newer accepted observation while the older pass is in flight.
	c := f.coordinatorWith(observedCacheRepository{syncRepository: f.repo, afterObserve: func(observed map[string]repository.BranchObservation) {
		observed[f.pull.HeadRef.Ref] = repository.BranchObservation{Commit: f.head, Exists: true}
		read, err := f.store.BeginBranchObservation(t.Context(), []repository.BranchIdentity{f.pull.HeadRef})
		if err != nil {
			t.Fatal(err)
		}
		fresh := repository.BranchObservation{Commit: newer, Exists: true}
		if err = f.store.AcceptBranchObservation(t.Context(), read, map[string]repository.BranchObservation{f.pull.HeadRef.Ref: fresh}); err != nil {
			t.Fatal(err)
		}
		if err = f.repo.CachePublishedBranch(t.Context(), f.remote, f.pull.HeadRef.Ref, fresh); err != nil {
			t.Fatal(err)
		}
	}})
	if err := c.SyncPullRequest(t.Context(), f.pull.ID); err == nil {
		t.Fatal("stale observation accepted")
	}
	assertCached(t, f.root, cacheRef("feature"), newer)
	assertCached(t, f.root, cacheRef("main"), f.base)
}

func TestBranchCachePassesWaitThroughCacheWrite(t *testing.T) {
	f := newBranchCacheFixture(t)
	second := f.pull
	second.ID = "second"
	var err error
	second, err = f.store.CreatePullRequest(second)
	if err != nil {
		t.Fatal(err)
	}
	entered, release, observed := make(chan struct{}), make(chan struct{}), make(chan struct{}, 2)
	var once sync.Once
	c := f.coordinatorWith(observedCacheRepository{syncRepository: f.repo, afterObserve: func(map[string]repository.BranchObservation) { observed <- struct{}{} }, beforeCache: func(ref string) {
		if ref == f.pull.HeadRef.Ref {
			once.Do(func() { close(entered); <-release })
		}
	}})
	firstDone := make(chan error, 1)
	go func() { firstDone <- c.SyncPullRequest(t.Context(), f.pull.ID) }()
	<-entered
	<-observed
	newer := f.advance(t)
	secondDone := make(chan error, 1)
	go func() { secondDone <- c.SyncPullRequest(t.Context(), second.ID) }()
	select {
	case <-observed:
		close(release)
		t.Fatal("new observation began before previous cache update finished")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-firstDone; err != nil && !errors.Is(err, pr.ErrComparisonUnavailable) {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	assertCached(t, f.root, cacheRef("feature"), newer)
}

func TestBranchCacheForkIdentityAndExactObservedCommit(t *testing.T) {
	f := newBranchCacheFixture(t)
	fork := filepath.Join(t.TempDir(), "fork.git")
	gitSync(t, f.source, "clone", "--bare", f.remote, fork)
	newer := f.advance(t)
	// The origin has advanced, but caching its accepted older observation must
	// import that exact commit rather than querying the branch again.
	for _, v := range []struct{ source, commit string }{{f.remote, f.head}, {fork, f.base}} {
		if err := f.repo.CachePublishedBranch(t.Context(), v.source, "refs/heads/feature", repository.BranchObservation{Commit: v.commit, Exists: true}); err != nil {
			t.Fatal(err)
		}
	}
	forkRef := fmt.Sprintf("refs/holark/browse/repositories/%x/feature", sha256.Sum256([]byte(repository.RepositoryIdentity(fork))))
	assertCached(t, f.root, cacheRef("feature"), f.head)
	assertCached(t, f.root, forkRef, f.base)
	if _, err := f.repo.Service.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertCached(t, f.root, cacheRef("feature"), newer)
	assertCached(t, f.root, forkRef, f.base)
	if err := f.repo.CachePublishedBranch(t.Context(), fork, "refs/heads/feature", repository.BranchObservation{}); err != nil {
		t.Fatal(err)
	}
	assertMissing(t, f.root, forkRef)
	assertCached(t, f.root, cacheRef("feature"), newer)
}

func TestBranchCacheForkConflictingRefs(t *testing.T) {
	for _, tc := range []struct {
		name, previous, next, remoteState string
	}{
		{"nested branch", "topic", "topic/sub", "deleted"},
		{"parent branch", "topic/sub", "topic", "deleted"},
		{"live conflict", "topic", "topic/sub", "exists"},
		{"unavailable fork", "topic", "topic/sub", "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBranchCacheFixture(t)
			fork := filepath.Join(t.TempDir(), "fork.git")
			gitSync(t, f.source, "clone", "--bare", f.remote, fork)
			gitSync(t, fork, "update-ref", "refs/heads/"+tc.previous, f.head)
			observed := repository.BranchObservation{Commit: f.head, Exists: true}
			if err := f.repo.CachePublishedBranch(t.Context(), fork, "refs/heads/"+tc.previous, observed); err != nil {
				t.Fatal(err)
			}
			prefix := fmt.Sprintf("refs/holark/browse/repositories/%x/", sha256.Sum256([]byte(repository.RepositoryIdentity(fork))))
			// Closed PRs stop refreshing their cached branches. Packing refs must
			// not prevent recovery when a later PR reuses a branch name prefix.
			gitSync(t, f.root, "pack-refs", "--all", "--prune")
			switch tc.remoteState {
			case "deleted":
				gitSync(t, fork, "update-ref", "-d", "refs/heads/"+tc.previous)
				gitSync(t, fork, "update-ref", "refs/heads/"+tc.next, f.base)
			case "unavailable":
				if err := os.Rename(fork, fork+".unavailable"); err != nil {
					t.Fatal(err)
				}
			}
			observed.Commit = f.base
			err := f.repo.CachePublishedBranch(t.Context(), fork, "refs/heads/"+tc.next, observed)
			if tc.remoteState != "deleted" {
				if err == nil {
					t.Fatal("cache write succeeded without confirming conflict was deleted")
				}
				assertCached(t, f.root, prefix+tc.previous, f.head)
				assertMissing(t, f.root, prefix+tc.next)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertMissing(t, f.root, prefix+tc.previous)
			assertCached(t, f.root, prefix+tc.next, f.base)
		})
	}
}

func TestPublicationCompletionRefreshFailureKeepsSuccessfulOutcome(t *testing.T) {
	f := newBranchCacheFixture(t)
	if err := f.coordinator.SyncPullRequest(t.Context(), f.pull.ID); err != nil {
		t.Fatal(err)
	}
	writes := make(chan error, 2)
	c := f.coordinatorWith(observedCacheRepository{syncRepository: f.repo, afterCache: func(ref string, err error) {
		if ref == f.pull.HeadRef.Ref {
			writes <- err
		}
	}})
	f.store.SetActionCompletionHook(func(id string) { c.RequestPullRequestRefresh(t.Context(), id) })
	current, _ := f.store.GetPullRequest(f.pull.ID)
	operation, _, err := f.store.BeginOperation(t.Context(), pr.Operation{RequestID: "publish", PullRequestID: current.ID, Kind: "work_publish", ExpectedHead: f.head, ExpectedInputs: pr.CaptureMutationInputs(current), Groups: []pr.FieldGroup{pr.TopologyGroup}})
	if err != nil {
		t.Fatal(err)
	}
	newer := f.advance(t)
	if err = f.store.RecordOperationStep(t.Context(), operation.RequestID, current.ID, "publication", pr.OperationStep{Status: "succeeded", HeadCommit: newer}); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(f.root, ".git", cacheRef("feature")+".lock")
	if err = os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, matched, err := f.store.CompleteRecoveredHead(t.Context(), operation.RequestID, current.ID, f.head, newer); err != nil || !matched {
		t.Fatalf("recovered completion: matched=%t err=%v", matched, err)
	}
	select {
	case err := <-writes:
		if err == nil {
			t.Fatal("expected cache write failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completion did not schedule cache refresh")
	}
	completed, _, err := f.store.GetOperation(t.Context(), operation.RequestID)
	if err != nil || completed.Status != "succeeded" {
		t.Fatalf("publication outcome changed: %+v %v", completed, err)
	}
	if err = os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err = f.coordinator.SyncPullRequest(t.Context(), current.ID); err != nil {
		t.Fatal(err)
	}
	assertCached(t, f.root, cacheRef("feature"), newer)
}

func TestBranchCacheFetchFailureRetriesAcceptedCommit(t *testing.T) {
	f := newBranchCacheFixture(t)
	offline := f.remote + ".offline"
	var once sync.Once
	c := f.coordinatorWith(observedCacheRepository{syncRepository: f.repo, beforeCache: func(ref string) {
		if ref == f.pull.HeadRef.Ref {
			once.Do(func() {
				if err := os.Rename(f.remote, offline); err != nil {
					t.Error(err)
				}
			})
		}
	}})
	if err := c.SyncPullRequest(t.Context(), f.pull.ID); err == nil {
		t.Fatal("unavailable commit reported successful preparation")
	}
	assertMissing(t, f.root, cacheRef("feature"))
	assertCached(t, f.root, cacheRef("main"), f.base)
	inputs, err := f.store.CaptureComparison(t.Context(), f.pull.ID)
	if err != nil || inputs.Head.Commit != f.head {
		t.Fatalf("fetch failure lost accepted evidence: %+v %v", inputs, err)
	}
	if err := os.Rename(offline, f.remote); err != nil {
		t.Fatal(err)
	}
	if err := f.coordinator.SyncPullRequest(t.Context(), f.pull.ID); err != nil {
		t.Fatal(err)
	}
	assertCached(t, f.root, cacheRef("feature"), f.head)
}

func TestRecoveredPublicationRefreshFollowsLatePrePublicationObservation(t *testing.T) {
	f := newBranchCacheFixture(t)
	ctx := t.Context()
	if err := f.coordinator.SyncPullRequest(ctx, f.pull.ID); err != nil {
		t.Fatal(err)
	}
	entered, release, prepared := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var holdOnce, releaseOnce, preparedOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	c := f.coordinatorWith(observedCacheRepository{syncRepository: f.repo, afterObserve: func(map[string]repository.BranchObservation) {
		holdOnce.Do(func() { close(entered); <-release })
	}, afterCache: func(ref string, err error) {
		if ref == f.pull.HeadRef.Ref && err == nil {
			preparedOnce.Do(func() { close(prepared) })
		}
	}})
	f.store.SetActionCompletionHook(func(id string) {
		if _, err := f.store.CaptureComparison(ctx, id); err != nil {
			t.Errorf("refresh before ownership release: %v", err)
		}
		c.RequestPullRequestRefresh(ctx, id)
	})
	firstDone := make(chan error, 1)
	go func() { firstDone <- c.SyncPullRequest(ctx, f.pull.ID) }()
	<-entered
	current, _ := f.store.GetPullRequest(f.pull.ID)
	op, _, err := f.store.BeginOperation(ctx, pr.Operation{RequestID: "publish", PullRequestID: current.ID, Kind: "work_publish", ExpectedHead: f.head, ExpectedInputs: pr.CaptureMutationInputs(current), Groups: []pr.FieldGroup{pr.TopologyGroup}})
	if err != nil {
		t.Fatal(err)
	}
	newer := f.advance(t)
	if err := f.store.RecordOperationStep(ctx, op.RequestID, current.ID, "publication", pr.OperationStep{Status: "succeeded", HeadCommit: newer}); err != nil {
		t.Fatal(err)
	}
	if _, matched, err := f.store.CompleteRecoveredHead(ctx, op.RequestID, current.ID, f.head, newer); err != nil || !matched {
		t.Fatalf("recovery: %t %v", matched, err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-firstDone; err == nil {
		t.Fatal("late observation accepted")
	}
	select {
	case <-prepared:
	case <-time.After(5 * time.Second):
		t.Fatal("completion refresh lost behind old sync")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, _ = f.store.GetPullRequest(f.pull.ID)
		if current.HasCurrentComparison() && current.HeadCommit == newer {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("targeted refresh did not prepare comparison: %+v", current)
		}
		time.Sleep(time.Millisecond)
	}
	assertCached(t, f.root, cacheRef("feature"), newer)
}

func TestBranchCacheRejectsPreparationAfterAcceptedInputsChange(t *testing.T) {
	for _, change := range []string{"head", "base", "relationship"} {
		t.Run(change, func(t *testing.T) {
			f := newBranchCacheFixture(t)
			ctx := t.Context()
			coordinatorWith := f.coordinatorWith
			var github *syncGitHub
			if change == "relationship" {
				data, _ := json.Marshal(map[string]any{"github": map[string]string{"base_repository_url": f.remote, "head_repository_url": f.remote}})
				p := f.pull
				p.ID, p.SyncProvider, p.SyncExternalID, p.SyncData = "provider-pr", "github", "github:owner/repo#1", data
				var err error
				f.pull, err = branchfixture.Create(ctx, f.store, p)
				if err != nil {
					t.Fatal(err)
				}
				github = &syncGitHub{pullRequests: []pr.GitHubPullRequest{{ExternalID: p.SyncExternalID, Status: p.Status, BaseBranch: p.BaseBranch, HeadBranch: p.HeadBranch, HeadCommit: f.head, SyncData: data}}}
				coordinatorWith = func(repo pr.Repository) *pr.Coordinator {
					return pr.New(f.store, pr.Options{Repository: repo, Refresh: f.scheduler, GitHubTransport: github, GitHubCodec: githubprovider.New(nil), Projects: pr.ProjectLookupFunc(func(id string) (pr.Project, bool) {
						return pr.Project{ID: "project", RepositoryURL: f.remote, DefaultBranch: "main", GitHubBacked: true}, true
					})})
				}
				f.coordinator = coordinatorWith(f.repo)
			}
			if err := f.coordinator.SyncPullRequest(ctx, f.pull.ID); err != nil {
				t.Fatal(err)
			}
			next := f.advance(t)
			if github != nil {
				github.pullRequests[0].HeadCommit = next
			}
			var once sync.Once
			c := coordinatorWith(observedCacheRepository{syncRepository: f.repo, afterMergeBase: func() {
				once.Do(func() {
					branch := f.pull.HeadRef
					if change != "head" {
						gitSync(t, f.source, "switch", "main")
						branch = f.pull.BaseRef
					}
					if change == "relationship" {
						gitSync(t, f.source, "switch", "-c", "retargeted")
						branch = repository.PublishedBranchIdentity(f.remote, "retargeted")
					}
					gitSync(t, f.source, "commit", "--allow-empty", "-m", "change during preparation")
					gitSync(t, f.source, "push", "origin", "HEAD:"+branch.Ref)
					head := gitSync(t, f.source, "rev-parse", "HEAD")
					if change == "relationship" {
						current, _ := f.store.GetPullRequest(f.pull.ID)
						current.BaseBranch, current.BaseRef = "retargeted", branch
						github.pullRequests[0].BaseBranch = "retargeted"
						token, err := f.store.BeginObservation(ctx, "project", []pr.FieldGroup{pr.TopologyGroup})
						if err != nil {
							t.Fatal(err)
						}
						if _, _, err := f.store.UpsertObservedPullRequests("project", []pr.PullRequest{current}, token); err != nil {
							t.Fatal(err)
						}
					}
					read, err := f.store.BeginBranchObservation(ctx, []repository.BranchIdentity{branch})
					if err != nil {
						t.Fatal(err)
					}
					if err := f.store.AcceptBranchObservation(ctx, read, map[string]repository.BranchObservation{branch.Ref: {Commit: head, Exists: true}}); err != nil {
						t.Fatal(err)
					}
				})
			}})
			if err := c.SyncPullRequest(ctx, f.pull.ID); !errors.Is(err, pr.ErrComparisonUnavailable) {
				t.Fatalf("obsolete preparation: %v", err)
			}
			current, _ := f.store.GetPullRequest(f.pull.ID)
			if current.HasCurrentComparison() || current.HeadCommit != f.head || current.BaseCommit != f.base {
				t.Fatalf("lost retained comparison: %+v", current)
			}
			if _, err := f.repo.Service.CommitRange(ctx, current.DiffBaseCommit, current.HeadCommit); err != nil {
				t.Fatalf("old details unreadable: %v", err)
			}
			if err := f.coordinator.SyncPullRequest(ctx, f.pull.ID); err != nil {
				t.Fatal(err)
			}
			current, _ = f.store.GetPullRequest(f.pull.ID)
			inputs, err := f.store.CaptureComparison(ctx, f.pull.ID)
			if err != nil || !current.HasCurrentComparison() || !pr.SameComparisonVersion(current.Comparison.Inputs, inputs) {
				t.Fatalf("retry did not prepare accepted inputs: %+v %v", current, err)
			}
			if change == "relationship" && current.BaseBranch != "retargeted" {
				t.Fatalf("retarget overwritten: %+v", current)
			}
		})
	}
}
