package pullrequestlifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
	"github.com/holark-ai/holark/internal/testfixture/branchfixture"
)

type workGitHub struct {
	*publicationGitHub
	requests atomic.Int32
	observe  func(context.Context, pr.GitHubPullRequest) (pr.GitHubPullRequest, error)
}

// Done is first selected once PrepareWork has enqueued its scheduled pass.
// Signalling there makes the overlap deterministic without sleeps.
type waitingWorkContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *waitingWorkContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func TestWorkPreparationCancelledSharedPassDoesNotCancelOtherCaller(t *testing.T) {
	checkBothPreparations(t, checkPreparationCancelledSharedPassDoesNotCancelOtherCaller)
}

func checkPreparationCancelledSharedPassDoesNotCancelOtherCaller(t *testing.T, merge bool, prepare preparationMethod) {
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
	go func() { first <- prepare(c, t.Context(), f.pull.ID) }()
	<-entered

	secondCtx := &waitingWorkContext{Context: t.Context(), waiting: make(chan struct{})}
	second := make(chan error, 1)
	go func() { second <- prepare(c, secondCtx, f.pull.ID) }()
	<-secondCtx.waiting

	cancelCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	thirdCtx := &waitingWorkContext{Context: cancelCtx, waiting: make(chan struct{})}
	third := make(chan error, 1)
	go func() { third <- prepare(c, thirdCtx, f.pull.ID) }()
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

func TestWorkPreparationGitHubAliasesAndSameNamedForkBranches(t *testing.T) {
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
	if err = publicationCoordinator(f, g, f.repo).PrepareWork(t.Context(), saved.ID); err != nil {
		t.Fatal(err)
	}
	if g.requests.Load() != 1 || g.gets.Load() != 0 {
		t.Fatalf("requests=%d full sync=%d", g.requests.Load(), g.gets.Load())
	}
}

func TestWorkPreparationWithdrawnWIPRequiresSynchronization(t *testing.T) {
	f, g := publicationFixture(t)
	p, _ := f.store.GetPullRequest(f.pull.ID)
	if _, err := f.store.TransitionPullRequestStatus(p.ID, pr.StatusDraft, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.TransitionPullRequestStatus(p.ID, pr.StatusWIP, time.Now()); err != nil {
		t.Fatal(err)
	}
	g.pullRequests[0].Status = pr.StatusClosed
	reader := &workGitHub{publicationGitHub: g}
	if err := publicationCoordinator(f, reader, f.repo).PrepareWork(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	if reader.requests.Load() != 0 || g.gets.Load() != 1 {
		t.Fatalf("requests=%d sync=%d", reader.requests.Load(), g.gets.Load())
	}
}

func TestWorkPreparationChecksComparisonBaseObject(t *testing.T) {
	checkBothPreparations(t, checkPreparationChecksComparisonBaseObject)
}

func checkPreparationChecksComparisonBaseObject(t *testing.T, merge bool, prepare preparationMethod) {
	f, g := publicationFixture(t)
	missing := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := branchfixture.Accept(t.Context(), f.store, f.pull.ID, f.base, f.head, missing); err != nil {
		t.Fatal(err)
	}
	reader := &workGitHub{publicationGitHub: g}
	if err := prepare(publicationCoordinator(f, reader, f.repo), t.Context(), f.pull.ID); err != nil {
		t.Fatal(err)
	}
	p, _ := f.store.GetPullRequest(f.pull.ID)
	if reader.requests.Load() != 0 || g.gets.Load() != 1 || p.DiffBaseCommit != f.base {
		t.Fatalf("requests=%d sync=%d comparison base=%s", reader.requests.Load(), g.gets.Load(), p.DiffBaseCommit)
	}
}

func TestWorkPreparationReleasesSchedulerSlotsBeforeFallback(t *testing.T) {
	checkBothPreparations(t, checkPreparationReleasesSchedulerSlotsBeforeFallback)
}

func checkPreparationReleasesSchedulerSlotsBeforeFallback(t *testing.T, merge bool, prepare preparationMethod) {
	f, transport := publicationFixture(t)
	p, _ := f.store.GetPullRequest(f.pull.ID)
	p.ID, p.SyncExternalID = "second", "github:owner/repo#2"
	second, err := branchfixture.Create(t.Context(), f.store, p)
	if err != nil {
		t.Fatal(err)
	}
	ready, release := make(chan struct{}), make(chan struct{})
	var entered atomic.Int32
	var once sync.Once
	defer once.Do(func() { close(release) })
	g := &workGitHub{publicationGitHub: transport, observe: func(ctx context.Context, p pr.GitHubPullRequest) (pr.GitHubPullRequest, error) {
		if entered.Add(1) == 2 {
			close(ready)
		}
		select {
		case <-release:
			return p, errors.New("focused check failed")
		case <-ctx.Done():
			return p, ctx.Err()
		}
	}}
	syncFailure := errors.New("full synchronization failed")
	g.get = func(int32) error { return syncFailure }
	c := publicationCoordinator(f, g, f.repo)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- prepare(c, ctx, f.pull.ID) }()
	go func() { done <- prepare(c, ctx, second.ID) }()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("verification did not occupy both slots")
	}
	once.Do(func() { close(release) })
	for range 2 {
		if err := <-done; !errors.Is(err, syncFailure) {
			t.Fatalf("fallback result=%v", err)
		}
	}
	if g.requests.Load() != 2 || g.gets.Load() != 2 {
		t.Fatalf("requests=%d sync=%d", g.requests.Load(), g.gets.Load())
	}
}

func TestWorkPreparationLocalPullRequestUsesExistingSync(t *testing.T) {
	checkBothPreparations(t, checkPreparationLocalPullRequestUsesExistingSync)
}

func checkPreparationLocalPullRequestUsesExistingSync(t *testing.T, merge bool, prepare preparationMethod) {
	f := newBranchCacheFixture(t)
	if err := prepare(f.coordinator, t.Context(), f.pull.ID); err != nil {
		t.Fatal(err)
	}
	p, _ := f.store.GetPullRequest(f.pull.ID)
	if !p.HasCurrentComparison() || p.HeadCommit != f.head || p.BaseCommit != f.base {
		t.Fatalf("local sync did not prepare commits: %+v", p)
	}
}

func TestWorkPreparationCancellationWhileWaitingDoesNotQueryOrSync(t *testing.T) {
	checkBothPreparations(t, checkPreparationCancellationWhileWaitingDoesNotQueryOrSync)
}

func checkPreparationCancellationWhileWaitingDoesNotQueryOrSync(t *testing.T, merge bool, prepare preparationMethod) {
	f, transport := publicationFixture(t)
	g := &workGitHub{publicationGitHub: transport}
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	for _, section := range []string{"hold_one", "hold_two"} {
		f.scheduler.Trigger(t.Context(), pr.RefreshKey{RepositoryID: "project", Section: section}, func(ctx context.Context) error {
			entered <- struct{}{}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}
	<-entered
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- prepare(publicationCoordinator(f, g, f.repo), ctx, f.pull.ID) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preparation=%v", err)
	}
	once.Do(func() { close(release) })
	f.scheduler.Close()
	if g.requests.Load() != 0 || g.gets.Load() != 0 {
		t.Fatalf("cancelled request observed or synced: %d/%d", g.requests.Load(), g.gets.Load())
	}
}

func (g *workGitHub) GetWorkState(ctx context.Context, _ pr.GitHubPullRequestTarget) (pr.GitHubPullRequest, error) {
	g.requests.Add(1)
	p := g.pullRequests[0]
	if g.observe != nil {
		return g.observe(ctx, p)
	}
	return p, nil
}

func TestWorkPreparationReusesOrFallsBackOnce(t *testing.T) {
	checkBothPreparations(t, checkPreparationReusesOrFallsBackOnce)
}

func checkPreparationReusesOrFallsBackOnce(t *testing.T, merge bool, prepare preparationMethod) {
	for _, scenario := range []string{"open", "draft", "confirmed open", "confirmed draft", "conflicting confirmation", "active lifecycle action", "remote draft", "head", "base", "head destination", "base destination", "head repository", "base repository", "title", "description", "closed", "merged", "legacy lifecycle event", "identity", "transport error", "transport deadline", "incomplete observation", "deleted branch", "stale comparison", "missing commits", "cancel", "deadline", "missing PR", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			f, transport := publicationFixture(t)
			g := &workGitHub{publicationGitHub: transport}
			if scenario == "draft" || scenario == "confirmed draft" {
				g.pullRequests[0].Status = pr.StatusDraft
				if err := f.coordinator.SyncPullRequest(t.Context(), f.pull.ID); err != nil {
					t.Fatal(err)
				}
				g.gets.Store(0)
				g.readiness.Store(0)
			}
			if scenario == "confirmed open" || scenario == "confirmed draft" || scenario == "conflicting confirmation" {
				completeWorkLifecycleAction(t, f, "confirmation", g.pullRequests[0].Status)
				if scenario == "conflicting confirmation" {
					// Retain an open confirmation while the accepted local status is draft.
					if _, err := f.store.TransitionPullRequestStatus(f.pull.ID, pr.StatusDraft, time.Now()); err != nil {
						t.Fatal(err)
					}
					g.pullRequests[0].Status = pr.StatusDraft
				}
				p, _ := f.store.GetPullRequest(f.pull.ID)
				if p.LifecycleConfirmation == nil || !p.HasCurrentComparison() {
					t.Fatalf("expected a status confirmation and current comparison: %+v", p)
				}
			}
			var comparisons, writes, observations atomic.Int32
			repo := observedCacheRepository{syncRepository: f.repo, afterMergeBase: func() { comparisons.Add(1) }, afterCache: func(string, error) { writes.Add(1) }, afterObserve: func(map[string]repository.BranchObservation) { observations.Add(1) }}
			c := publicationCoordinator(f, g, repo)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			id := f.pull.ID
			wantRequests, wantSync := int32(1), int32(1)
			wantError := false
			switch scenario {
			case "open", "draft", "confirmed open", "confirmed draft":
				wantSync = 0
			case "legacy lifecycle event":
				wantSync = 0
				g.pullRequests[0].SyncData, _ = json.Marshal(map[string]any{"github": map[string]any{"base_repository_url": f.remote, "head_repository_url": f.remote, "lifecycle_event_id": "obsolete"}})
			case "conflicting confirmation":
				wantRequests = 0
			case "active lifecycle action":
				p, _ := f.store.GetPullRequest(id)
				if _, _, err := f.store.BeginOperation(ctx, pr.Operation{RequestID: "active", PullRequestID: id, Kind: "transition", RequestedStatus: pr.StatusDraft, ExpectedInputs: pr.CaptureMutationInputs(p), Groups: []pr.FieldGroup{pr.LifecycleGroup}}); err != nil {
					t.Fatal(err)
				}
				wantRequests = 0
				// A sentinel makes fallback observable without depending on how sync
				// reconciles an operation that is still running.
				g.get = func(int32) error { return errors.New("fallback reached") }
				wantError = true
			case "remote draft":
				g.pullRequests[0].Status = pr.StatusDraft
			case "head":
				g.pullRequests[0].HeadCommit = f.advance(t)
			case "base":
				gitSync(t, f.remote, "update-ref", "refs/heads/main", f.head)
				g.pullRequests[0].BaseCommit = f.head
			case "head destination":
				gitSync(t, f.remote, "update-ref", "refs/heads/other", f.head)
				g.pullRequests[0].HeadBranch = "other"
			case "base destination":
				gitSync(t, f.remote, "update-ref", "refs/heads/other", f.base)
				g.pullRequests[0].BaseBranch = "other"
			case "head repository", "base repository":
				fork := t.TempDir() + "/fork.git"
				gitSync(t, f.source, "clone", "--bare", f.remote, fork)
				base, head := f.remote, fork
				if scenario == "base repository" {
					base, head = fork, f.remote
				}
				g.pullRequests[0].SyncData, _ = json.Marshal(map[string]any{"github": map[string]string{"base_repository_url": base, "head_repository_url": head}})
			case "title":
				g.pullRequests[0].Title = "Changed title"
			case "description":
				g.pullRequests[0].Summary = "Changed description"
			case "closed":
				g.pullRequests[0].Status = pr.StatusClosed
			case "merged":
				g.pullRequests[0].Status = pr.StatusMerged
			case "identity":
				g.pullRequests[0].ExternalID = "github:owner/repo#2"
				wantError = true
			case "transport error", "transport deadline":
				g.observe = func(context.Context, pr.GitHubPullRequest) (pr.GitHubPullRequest, error) {
					if scenario == "transport deadline" {
						return pr.GitHubPullRequest{}, context.DeadlineExceeded
					}
					return pr.GitHubPullRequest{}, errors.New("GitHub partial/error response")
				}
			case "incomplete observation":
				g.observe = func(_ context.Context, p pr.GitHubPullRequest) (pr.GitHubPullRequest, error) {
					p.BaseCommit = ""
					return p, nil
				}
			case "deleted branch":
				gitSync(t, f.remote, "update-ref", "-d", "refs/heads/feature")
				g.observe = func(context.Context, pr.GitHubPullRequest) (pr.GitHubPullRequest, error) {
					return pr.GitHubPullRequest{}, errors.New("missing head ref")
				}
				wantError = true
			case "stale comparison":
				if err := f.store.InvalidateComparison(ctx, id); err != nil {
					t.Fatal(err)
				}
				wantRequests = 0
			case "missing commits":
				root := t.TempDir()
				gitSync(t, root, "init", "-b", "main")
				gitSync(t, root, "remote", "add", "origin", f.remote)
				adapter, err := gitadapter.Open(ctx, root)
				if err != nil {
					t.Fatal(err)
				}
				repo.syncRepository = syncRepository{repository.NewService(adapter)}
				c = publicationCoordinator(f, g, repo)
				wantRequests = 0
			case "cancel", "deadline":
				wantSync = 0
				wantError = true
				if scenario == "deadline" {
					var deadlineCancel context.CancelFunc
					ctx, deadlineCancel = context.WithTimeout(ctx, 100*time.Millisecond)
					defer deadlineCancel()
				}
				g.observe = func(ctx context.Context, p pr.GitHubPullRequest) (pr.GitHubPullRequest, error) {
					if scenario == "cancel" {
						cancel()
					}
					<-ctx.Done()
					return p, ctx.Err()
				}
			case "missing PR":
				id = "missing"
				wantRequests = 0
				wantSync = 0
				wantError = true
			case "unsupported":
				c = publicationCoordinator(f, transport, repo)
				wantRequests = 0
			}

			if merge && (scenario == "draft" || scenario == "confirmed draft" || scenario == "conflicting confirmation") {
				wantSync, wantRequests = 1, 0
			}
			before, _ := f.store.GetPullRequest(f.pull.ID)
			err := prepare(c, ctx, id)
			// Drain the cancelled verification before checking counters.
			f.scheduler.Close()
			if (err != nil) != wantError {
				t.Fatalf("error=%v want error=%t", err, wantError)
			}
			if scenario == "cancel" && !errors.Is(err, context.Canceled) || scenario == "deadline" && !errors.Is(err, context.DeadlineExceeded) || scenario == "missing PR" && !errors.Is(err, pr.ErrNotFound) {
				t.Fatalf("unexpected error: %v", err)
			}
			if g.requests.Load() != wantRequests || g.gets.Load() != wantSync {
				t.Fatalf("focused=%d sync=%d; want %d/%d", g.requests.Load(), g.gets.Load(), wantRequests, wantSync)
			}
			if wantSync == 0 {
				after, _ := f.store.GetPullRequest(f.pull.ID)
				wantReadiness := int32(0)
				if merge && !wantError {
					wantReadiness = 1
				}
				if (!merge && !reflect.DeepEqual(before, after)) || comparisons.Load() != 0 || writes.Load() != 0 || observations.Load() != 0 || g.readiness.Load() != wantReadiness {
					t.Fatalf("verification wrote or prepared state: comparisons=%d writes=%d observations=%d readiness=%d", comparisons.Load(), writes.Load(), observations.Load(), g.readiness.Load())
				}
			}
		})
	}
}

func TestWorkPreparationConcurrentChangesAndIdenticalObservations(t *testing.T) {
	checkBothPreparations(t, checkPreparationConcurrentChangesAndIdenticalObservations)
}

func checkPreparationConcurrentChangesAndIdenticalObservations(t *testing.T, merge bool, prepare preparationMethod) {
	for _, scenario := range []string{"metadata", "mutation", "protected operation", "lifecycle started", "lifecycle completed", "lifecycle round trip", "confirmation changed", "comparison", "identical observation"} {
		t.Run(scenario, func(t *testing.T) {
			f, transport := publicationFixture(t)
			if scenario == "confirmation changed" || scenario == "lifecycle round trip" {
				completeWorkLifecycleAction(t, f, "initial confirmation", pr.StatusOpen)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
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
			done := make(chan error, 1)
			go func() { done <- prepare(c, t.Context(), f.pull.ID) }()
			<-entered
			p, _ := f.store.GetPullRequest(f.pull.ID)
			switch scenario {
			case "lifecycle started", "lifecycle completed":
				op, _, err := f.store.BeginOperation(t.Context(), pr.Operation{RequestID: "lifecycle", PullRequestID: p.ID, Kind: "transition", RequestedStatus: pr.StatusDraft, ExpectedInputs: pr.CaptureMutationInputs(p), Groups: []pr.FieldGroup{pr.LifecycleGroup}})
				if err != nil {
					t.Error(err)
				}
				if scenario == "lifecycle completed" {
					if _, err = f.store.CompleteOperation(t.Context(), op.RequestID, "failed", "cancelled"); err != nil {
						t.Error(err)
					}
				}
			case "lifecycle round trip":
				completeWorkLifecycleAction(t, f, "draft", pr.StatusDraft)
				completeWorkLifecycleAction(t, f, "open", pr.StatusOpen)
				after, _ := f.store.GetPullRequest(p.ID)
				if after.Status != p.Status || !reflect.DeepEqual(after.LifecycleConfirmation, p.LifecycleConfirmation) || after.LifecycleGeneration == p.LifecycleGeneration || !after.HasCurrentComparison() {
					t.Errorf("round trip did not restore reusable content with a new generation: before=%+v after=%+v", p, after)
				}
			case "metadata":
				if err := f.store.UpdatePullRequestMetadata(p.ID, "Edited while verifying", p.Summary, time.Now()); err != nil {
					t.Error(err)
				}
			case "mutation", "protected operation":
				op, _, err := f.store.BeginOperation(t.Context(), pr.Operation{RequestID: "concurrent", PullRequestID: p.ID, Kind: "publish", ExpectedHead: p.HeadCommit, ExpectedInputs: pr.CaptureMutationInputs(p), Groups: []pr.FieldGroup{pr.TopologyGroup}})
				if err != nil {
					t.Error(err)
				}
				if scenario == "mutation" {
					if _, err = f.store.CompleteOperation(t.Context(), op.RequestID, "failed", "cancelled"); err != nil {
						t.Error(err)
					}
				}
			case "comparison":
				if err := f.store.InvalidateComparison(t.Context(), p.ID); err != nil {
					t.Error(err)
				}
			case "identical observation", "confirmation changed":
				token, err := f.store.BeginObservation(t.Context(), p.RepositoryID, []pr.FieldGroup{pr.LifecycleGroup, pr.TopologyGroup, pr.MetadataGroup})
				if err != nil {
					t.Error(err)
				}
				if _, _, err = f.store.UpsertObservedPullRequests(p.RepositoryID, []pr.PullRequest{p}, token); err != nil {
					t.Error(err)
				}
				read, err := f.store.BeginBranchObservation(t.Context(), []repository.BranchIdentity{p.BaseRef, p.HeadRef})
				if err != nil {
					t.Error(err)
				}
				if err = f.store.AcceptBranchObservation(t.Context(), read, map[string]repository.BranchObservation{p.BaseRef.Ref: {Commit: p.BaseCommit, Exists: true}, p.HeadRef.Ref: {Commit: p.HeadCommit, Exists: true}}); err != nil {
					t.Error(err)
				}
				if scenario == "confirmation changed" {
					after, _ := f.store.GetPullRequest(p.ID)
					if after.LifecycleConfirmation != nil || *pr.CaptureMutationInputs(p) != *pr.CaptureMutationInputs(after) {
						t.Errorf("observation must clear only the confirmation without changing mutation inputs: before=%+v after=%+v", p, after)
					}
				}
			}
			releaseOnce.Do(func() { close(release) })
			err := <-done
			if err != nil && scenario != "protected operation" && scenario != "lifecycle started" {
				t.Fatal(err)
			}
			wantSync := int32(1)
			if scenario == "identical observation" {
				wantSync = 0
			}
			if g.requests.Load() != 1 || g.gets.Load() != wantSync {
				t.Fatalf("focused=%d sync=%d", g.requests.Load(), g.gets.Load())
			}
		})
	}
}

func completeWorkLifecycleAction(t *testing.T, f *branchCacheFixture, requestID string, status pr.Status) {
	t.Helper()
	p, _ := f.store.GetPullRequest(f.pull.ID)
	op, _, err := f.store.BeginOperation(t.Context(), pr.Operation{RequestID: requestID, PullRequestID: p.ID, Kind: "transition", RequestedStatus: status, ExpectedInputs: pr.CaptureMutationInputs(p), Groups: []pr.FieldGroup{pr.LifecycleGroup}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != status {
		if _, err = f.store.TransitionPullRequestStatus(p.ID, status, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.store.CompleteOperation(t.Context(), op.RequestID, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
}

// Both callers share the verifier, but retain distinct scheduling and follow-up work.
type preparationMethod func(*pr.Coordinator, context.Context, string) error

func checkBothPreparations(t *testing.T, check func(*testing.T, bool, preparationMethod)) {
	t.Helper()
	for _, merge := range []bool{false, true} {
		name, prepare := "work", (*pr.Coordinator).PrepareWork
		if merge {
			name, prepare = "merge", (*pr.Coordinator).PrepareMerge
		}
		t.Run(name, func(t *testing.T) { check(t, merge, prepare) })
	}
}
