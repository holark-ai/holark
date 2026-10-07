package pullrequestlifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	githubprovider "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/testfixture/branchfixture"
)

type publicationGitHub struct {
	*syncGitHub
	gets, readiness atomic.Int32
	get             func(int32) error
}

func (g *publicationGitHub) Get(ctx context.Context, target pr.GitHubPullRequestTarget) (pr.GitHubPullRequest, error) {
	n := g.gets.Add(1)
	if g.get != nil {
		if err := g.get(n); err != nil {
			return pr.GitHubPullRequest{}, err
		}
	}
	return g.syncGitHub.Get(ctx, target)
}
func (g *publicationGitHub) RefreshReadiness(ctx context.Context, target pr.GitHubPullRequestTarget) (pr.GitHubReadiness, error) {
	g.readiness.Add(1)
	return g.syncGitHub.RefreshReadiness(ctx, target)
}
func publicationFixture(t *testing.T) (*branchCacheFixture, *publicationGitHub) {
	t.Helper()
	f := newBranchCacheFixture(t)
	data, _ := json.Marshal(map[string]any{"github": map[string]string{"base_repository_url": f.remote, "head_repository_url": f.remote}})
	p := f.pull
	p.ID, p.SyncProvider, p.SyncExternalID, p.SyncData = "github-pr", "github", "github:owner/repo#1", data
	var err error
	f.pull, err = branchfixture.Create(t.Context(), f.store, p)
	if err != nil {
		t.Fatal(err)
	}
	g := &publicationGitHub{syncGitHub: &syncGitHub{pullRequests: []pr.GitHubPullRequest{{ExternalID: p.SyncExternalID, Title: p.Title, Status: p.Status, BaseBranch: p.BaseBranch, HeadBranch: p.HeadBranch, BaseCommit: f.base, HeadCommit: f.head, SyncData: data}}}}
	f.coordinator = publicationCoordinator(f, g, f.repo)
	if err := f.coordinator.SyncPullRequest(t.Context(), f.pull.ID); err != nil {
		t.Fatal(err)
	}
	g.gets.Store(0)
	g.readiness.Store(0)
	return f, g
}
func publicationCoordinator(f *branchCacheFixture, g pr.GitHubTransport, repo pr.Repository) *pr.Coordinator {
	return pr.New(f.store, pr.Options{Refresh: f.scheduler, Repository: repo, GitHubTransport: g, GitHubCodec: githubprovider.New(nil), Projects: pr.ProjectLookupFunc(func(id string) (pr.Project, bool) {
		return pr.Project{ID: "project", RepositoryURL: f.remote, DefaultBranch: "main", GitHubBacked: true}, id == "project"
	})})
}

func TestPublicationVerificationReusesOrFallsBackOnce(t *testing.T) {
	for _, scenario := range []string{"unchanged", "title", "comparison", "head", "base", "destination", "closed", "identity", "transport", "transport timeout", "mutation", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			f, g := publicationFixture(t)
			var comparisons, cacheWrites atomic.Int32
			repo := observedCacheRepository{syncRepository: f.repo, afterMergeBase: func() { comparisons.Add(1) }, afterCache: func(string, error) { cacheWrites.Add(1) }}
			c := publicationCoordinator(f, g, repo)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "title":
				g.pullRequests[0].Title = "New remote title"
				g.get = func(int32) error {
					return f.store.UpdatePullRequestMetadata(f.pull.ID, "Concurrent title edit", "", time.Now())
				}
			case "comparison":
				if err := f.store.InvalidateComparison(ctx, f.pull.ID); err != nil {
					t.Fatal(err)
				}
			case "head":
				g.pullRequests[0].HeadCommit = f.advance(t)
			case "base":
				gitSync(t, f.remote, "update-ref", "refs/heads/main", f.head)
			case "destination":
				gitSync(t, f.remote, "update-ref", "refs/heads/other", f.head)
				g.pullRequests[0].HeadBranch = "other"
			case "closed":
				g.pullRequests[0].Status = pr.StatusClosed
			case "identity":
				g.pullRequests[0].ExternalID = "github:owner/repo#2"
			case "transport", "transport timeout":
				g.get = func(n int32) error {
					if n == 1 {
						if scenario == "transport timeout" {
							return context.DeadlineExceeded
						}
						return errors.New("temporary GitHub failure")
					}
					return nil
				}
			case "mutation":
				var once sync.Once
				repo.afterObserve = func(map[string]repository.BranchObservation) {
					once.Do(func() {
						p, _ := f.store.GetPullRequest(f.pull.ID)
						op, _, err := f.store.BeginOperation(ctx, pr.Operation{RequestID: "competing", PullRequestID: p.ID, Kind: "publish", ExpectedHead: p.HeadCommit, ExpectedInputs: pr.CaptureMutationInputs(p), Groups: []pr.FieldGroup{pr.TopologyGroup}})
						if err != nil {
							t.Error(err)
							return
						}
						if _, err := f.store.CompleteOperation(ctx, op.RequestID, "failed", "cancelled"); err != nil {
							t.Error(err)
						}
					})
				}
				c = publicationCoordinator(f, g, repo)
			case "cancel":
				g.get = func(int32) error { cancel(); return context.Canceled }
			}
			err := c.PreparePublication(ctx, f.pull.ID)
			if scenario == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation: %v", err)
				}
				return
			}
			if scenario == "identity" {
				if err == nil {
					t.Fatal("accepted wrong identity")
				}
				if g.gets.Load() != 2 {
					t.Fatalf("gets=%d", g.gets.Load())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "unchanged" || scenario == "title" {
				if g.gets.Load() != 1 || g.readiness.Load() != 0 || comparisons.Load() != 0 || cacheWrites.Load() != 0 {
					t.Fatalf("fast path did preparation: gets=%d readiness=%d comparisons=%d cache writes=%d", g.gets.Load(), g.readiness.Load(), comparisons.Load(), cacheWrites.Load())
				}
				return
			}
			wantGets := int32(2)
			if scenario == "comparison" {
				wantGets = 1
			}
			wantWork := int32(1)
			if scenario == "closed" {
				wantWork = 0
			}
			if g.gets.Load() != wantGets || g.readiness.Load() != wantWork || comparisons.Load() != wantWork {
				t.Fatalf("fallback: gets=%d readiness=%d comparisons=%d", g.gets.Load(), g.readiness.Load(), comparisons.Load())
			}
			p, _ := f.store.GetPullRequest(f.pull.ID)
			if scenario == "head" && p.HeadCommit != g.pullRequests[0].HeadCommit {
				t.Fatal("head not repaired")
			}
			if scenario == "destination" && p.HeadBranch != "other" {
				t.Fatal("destination not accepted")
			}
			if scenario == "closed" && p.Status != pr.StatusClosed {
				t.Fatal("inactive state not accepted")
			}
		})
	}
}

func TestPublicationCompletionPreparesOnlyChangedComparison(t *testing.T) {
	f, g := publicationFixture(t)
	var comparisons atomic.Int32
	var metadata sync.Once
	var first, second atomic.Bool
	c := publicationCoordinator(f, g, observedCacheRepository{syncRepository: f.repo, afterMergeBase: func() { comparisons.Add(1) }, afterCache: func(string, error) {
		metadata.Do(func() { first.Store(true) })
	}})
	g.pullRequests[0].Title = "Accepted after publication"
	c.RequestPublicationRefresh(t.Context(), f.pull.ID)
	waitPublication(t, func() bool {
		p, _ := f.store.GetPullRequest(f.pull.ID)
		return first.Load() && p.Title == g.pullRequests[0].Title
	})
	// Wait for this section's next pass, which also verifies an unchanged refresh.
	done := make(chan struct{})
	f.scheduler.Trigger(t.Context(), pr.RefreshKey{RepositoryID: "project", PullRequestID: f.pull.ID, Section: "publication_completion"}, func(context.Context) error { close(done); return nil })
	<-done
	if comparisons.Load() != 0 || g.readiness.Load() != 0 {
		t.Fatal("unchanged completion did expensive preparation")
	}
	next := f.advance(t)
	g.pullRequests[0].HeadCommit = next
	c = publicationCoordinator(f, g, observedCacheRepository{syncRepository: f.repo, afterMergeBase: func() { comparisons.Add(1); second.Store(true) }})
	c.RequestPublicationRefresh(t.Context(), f.pull.ID)
	waitPublication(t, func() bool {
		p, _ := f.store.GetPullRequest(f.pull.ID)
		return second.Load() && p.HasCurrentComparison() && p.HeadCommit == next
	})
	if comparisons.Load() != 1 || g.readiness.Load() != 0 {
		t.Fatalf("completion comparisons=%d readiness=%d", comparisons.Load(), g.readiness.Load())
	}
	if err := c.SyncPullRequest(t.Context(), f.pull.ID); err != nil {
		t.Fatal(err)
	}
	if g.readiness.Load() != 1 || comparisons.Load() != 2 {
		t.Fatal("full sync no longer performs full preparation")
	}
}
func waitPublication(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("publication refresh did not complete")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPublicationRequestsDoNotReplacePendingFullSync(t *testing.T) {
	f, g := publicationFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	g.get = func(n int32) error {
		if n == 1 {
			close(entered)
			<-release
		}
		return nil
	}
	c := publicationCoordinator(f, g, f.repo)
	first := make(chan error, 1)
	go func() { first <- c.SyncPullRequest(t.Context(), f.pull.ID) }()
	<-entered
	// The full pass has a pending follow-up before either lighter request arrives.
	c.RequestPullRequestRefresh(t.Context(), f.pull.ID)
	c.RequestPublicationRefresh(t.Context(), f.pull.ID)
	verified := make(chan error, 1)
	go func() { verified <- c.PreparePublication(t.Context(), f.pull.ID) }()
	waitPublication(t, func() bool { return g.gets.Load() >= 2 })
	once.Do(func() { close(release) })
	// The first observation may be superseded, but the queued full pass must run.
	<-first
	if err := <-verified; err != nil {
		t.Fatal(err)
	}
	waitPublication(t, func() bool { return g.readiness.Load() >= 2 })
	p, _ := f.store.GetPullRequest(f.pull.ID)
	if !p.HasCurrentComparison() || p.HeadCommit != f.head {
		t.Fatal("overlap lost accepted comparison")
	}
}

func TestPublicationCombinedVerification(t *testing.T) {
	for _, scenario := range []string{"unchanged", "metadata", "unsupported", "head", "base", "destination", "repository", "identity", "closed", "deleted", "deleted remote", "failure", "shared cancellation", "cancel", "mutation"} {
		t.Run(scenario, func(t *testing.T) {
			f, transport := publicationFixture(t)
			var observations atomic.Int32
			repo := observedCacheRepository{syncRepository: f.repo, afterObserve: func(map[string]repository.BranchObservation) { observations.Add(1) }}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			g := &workGitHub{publicationGitHub: transport}
			g.observe = func(ctx context.Context, p pr.GitHubPullRequest) (pr.GitHubPullRequest, error) {
				switch scenario {
				case "metadata":
					p.Title, p.Summary = "Remote title", "Remote body"
					if err := f.store.UpdatePullRequestMetadata(f.pull.ID, "Local title", "Local body", time.Now()); err != nil {
						t.Fatal(err)
					}
				case "head":
					p.HeadCommit = f.advance(t)
					transport.pullRequests[0].HeadCommit = p.HeadCommit
				case "base":
					p.BaseCommit = f.head
					gitSync(t, f.remote, "update-ref", "refs/heads/main", f.head)
				case "destination":
					p.HeadBranch = "other"
				case "repository":
					p.SyncData = json.RawMessage(`{"github":{"base_repository_url":"https://github.com/other/repo","head_repository_url":"https://github.com/other/fork"}}`)
				case "identity":
					p.ExternalID = "github:owner/repo#2"
				case "closed":
					p.Status = pr.StatusClosed
				case "deleted", "deleted remote":
					p.HeadCommit = ""
					if scenario == "deleted remote" {
						gitSync(t, f.remote, "update-ref", "-d", "refs/heads/feature")
					}
				case "unsupported":
					return p, pr.ErrGitHubClientUnsupported
				case "failure":
					return p, errors.New("incomplete observation")
				case "shared cancellation":
					return p, context.Canceled
				case "cancel":
					cancel()
					return p, ctx.Err()
				case "mutation":
					completeWorkLifecycleAction(t, f, "during-publication-verification", pr.StatusOpen)
				}
				return p, nil
			}
			c := publicationCoordinator(f, g, repo)
			err := c.PreparePublication(ctx, f.pull.ID)
			if scenario == "cancel" {
				if !errors.Is(err, context.Canceled) || g.gets.Load() != 0 {
					t.Fatalf("cancel=%v sync=%d", err, g.gets.Load())
				}
				return
			}
			if scenario == "deleted remote" {
				current, _ := f.store.GetPullRequest(f.pull.ID)
				if err == nil || current.HasCurrentComparison() || g.requests.Load() != 1 || g.gets.Load() != 1 || observations.Load() != 1 {
					t.Fatalf("deleted branch accepted: err=%v current=%t combined=%d fallback=%d branches=%d", err, current.HasCurrentComparison(), g.requests.Load(), g.gets.Load(), observations.Load())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "unsupported" && g.readiness.Load() != 0 {
				t.Fatal("unsupported capability used full sync instead of legacy verification")
			}
			reuse := scenario == "unchanged" || scenario == "metadata"
			want := int32(1)
			if reuse {
				want = 0
			}
			if g.requests.Load() != 1 || g.gets.Load() != want || observations.Load() != want {
				t.Fatalf("combined=%d fallback=%d branch reads=%d", g.requests.Load(), g.gets.Load(), observations.Load())
			}
		})
	}
}

func TestPublicationCancelledSharedPassDoesNotCancelOtherCaller(t *testing.T) {
	f, transport := publicationFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	g := &workGitHub{publicationGitHub: transport, observe: func(ctx context.Context, p pr.GitHubPullRequest) (pr.GitHubPullRequest, error) {
		close(entered)
		select {
		case <-release:
			return p, nil
		case <-ctx.Done():
			return p, ctx.Err()
		}
	}}
	c := publicationCoordinator(f, g, f.repo)
	first := make(chan error, 1)
	go func() { first <- c.PreparePublication(t.Context(), f.pull.ID) }()
	<-entered

	secondCtx := &waitingWorkContext{Context: t.Context(), waiting: make(chan struct{})}
	second := make(chan error, 1)
	go func() { second <- c.PreparePublication(secondCtx, f.pull.ID) }()
	<-secondCtx.waiting

	cancelCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	thirdCtx := &waitingWorkContext{Context: cancelCtx, waiting: make(chan struct{})}
	third := make(chan error, 1)
	go func() { third <- c.PreparePublication(thirdCtx, f.pull.ID) }()
	<-thirdCtx.waiting
	cancel()
	if err := <-third; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller: %v", err)
	}
	once.Do(func() { close(release) })
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatalf("another caller's cancellation escaped instead of falling back: %v", err)
	}
	if g.requests.Load() != 1 || g.gets.Load() != 1 {
		t.Fatalf("focused=%d fallback sync=%d; want one of each", g.requests.Load(), g.gets.Load())
	}
}

func TestPublicationGitHubAliasesAndSameNamedForkBranches(t *testing.T) {
	f, transport := publicationFixture(t)
	p, _ := f.store.GetPullRequest(f.pull.ID)
	p.ID, p.SyncExternalID = "fork-pr", "github:owner/repo#2"
	p.HeadBranch = p.BaseBranch
	p.SyncData = json.RawMessage(`{"github":{"base_repository_url":"git@github.com:owner/repo.git","head_repository_url":"git@github.com:contributor/fork.git"}}`)
	p.BaseRef, p.HeadRef, p.Comparison = repository.BranchIdentity{}, repository.BranchIdentity{}, nil
	// Seed accepted branch versions independently: matching ref names in two
	// repositories must retain different commits.
	saved, err := f.store.CreatePullRequest(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, branch := range []struct {
		id     repository.BranchIdentity
		commit string
	}{{saved.BaseRef, f.base}, {saved.HeadRef, f.head}} {
		read, err := f.store.BeginBranchObservation(t.Context(), []repository.BranchIdentity{branch.id})
		if err != nil {
			t.Fatal(err)
		}
		if err = f.store.AcceptBranchObservation(t.Context(), read, map[string]repository.BranchObservation{branch.id.Ref: {Commit: branch.commit, Exists: true}}); err != nil {
			t.Fatal(err)
		}
	}
	pair, err := f.store.CaptureComparison(t.Context(), saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.AcceptComparison(t.Context(), saved.ID, pair, f.base); err != nil {
		t.Fatal(err)
	}
	transport.pullRequests[0] = pr.GitHubPullRequest{ExternalID: saved.SyncExternalID, Title: p.Title, Summary: p.Summary, Status: p.Status, BaseBranch: p.BaseBranch, HeadBranch: p.HeadBranch, BaseCommit: f.base, HeadCommit: f.head, SyncData: json.RawMessage(`{"github":{"base_repository_url":"https://github.com/owner/repo.git","head_repository_url":"https://github.com/contributor/fork.git"}}`)}
	transport.get = func(int32) error { return errors.New("unexpected fallback") }
	g := &workGitHub{publicationGitHub: transport}
	if err = publicationCoordinator(f, g, f.repo).PreparePublication(t.Context(), saved.ID); err != nil {
		t.Fatal(err)
	}
	if g.requests.Load() != 1 || g.gets.Load() != 0 {
		t.Fatalf("requests=%d full sync=%d", g.requests.Load(), g.gets.Load())
	}
}
