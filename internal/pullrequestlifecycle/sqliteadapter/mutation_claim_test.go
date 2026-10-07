package sqliteadapter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	githubprovider "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

type mutationClaimRegistry struct {
	*Store
	captured chan struct{}
	release  chan struct{}
}

func (registry mutationClaimRegistry) BeginOperation(ctx context.Context, input pr.Operation) (pr.Operation, bool, error) {
	operation, created, err := registry.Store.BeginOperation(ctx, input)
	registry.captured <- struct{}{}
	<-registry.release
	return operation, created, err
}

type mutationClaimProvider struct {
	pr.GitHubTransport
	entered chan struct{}
	release chan struct{}
}

func (provider mutationClaimProvider) MarkReadyForReview(context.Context, pr.GitHubPullRequestTarget) (pr.GitHubPullRequest, error) {
	provider.entered <- struct{}{}
	<-provider.release
	return pr.GitHubPullRequest{Status: pr.StatusOpen}, nil
}

func TestOverlappingTransitionExecutorsClaimMutationOnce(t *testing.T) {
	ctx := pr.WithRequestID(t.Context(), "shared-opening")
	catalog, _ := operationCatalog(t)
	current, err := catalog.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Title: "Ready to open", Summary: "Existing description", Status: pr.StatusDraft, SyncProvider: "github", SyncExternalID: "PR_7", SyncData: json.RawMessage(`{"github":{"number":7,"node_id":"PR_7","draft":true,"state":"open"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	registry := mutationClaimRegistry{Store: catalog, captured: make(chan struct{}, 2), release: make(chan struct{})}
	provider := mutationClaimProvider{entered: make(chan struct{}, 2), release: make(chan struct{})}
	defer func() {
		select {
		case <-provider.release:
		default:
			close(provider.release)
		}
	}()
	coordinator := pr.New(registry, pr.Options{GitHubTransport: provider, GitHubCodec: githubprovider.New(nil)})
	results := make(chan error, 2)
	for range 2 {
		go func() { _, err := coordinator.Transition(ctx, current, pr.StatusOpen); results <- err }()
	}
	// Both executions captured the same operation before either can claim it.
	<-registry.captured
	<-registry.captured
	close(registry.release)
	<-provider.entered
	select {
	case err := <-results:
		if !errors.Is(err, pr.ErrOperationInProgress) {
			t.Fatalf("competing executor = %v, want pending", err)
		}
	case <-provider.entered:
		t.Fatal("both executors mutated the provider")
	}
	pending, found, err := catalog.GetOperation(ctx, "shared-opening")
	if err != nil || !found || pending.Status != "running" {
		t.Fatalf("competing executor completed the owner's operation: %+v, %v", pending, err)
	}
	close(provider.release)
	if err = <-results; err != nil {
		t.Fatal(err)
	}
	finished, _ := catalog.GetPullRequest(current.ID)
	operation, _, err := catalog.GetOperation(ctx, "shared-opening")
	if err != nil || finished.Status != pr.StatusOpen || len(finished.Operations) != 0 || operation.Status != "succeeded" {
		t.Fatalf("opening did not complete once: pr=%+v operation=%+v err=%v", finished, operation, err)
	}
}

func TestMutationClaimsRejectChangedAcceptedInputs(t *testing.T) {
	changes := map[string]func(*pr.PullRequest){
		"head":                 func(p *pr.PullRequest) { p.HeadCommit = "new-head" },
		"base":                 func(p *pr.PullRequest) { p.BaseCommit = "new-base" },
		"head branch":          func(p *pr.PullRequest) { p.HeadBranch = "other" },
		"base branch":          func(p *pr.PullRequest) { p.BaseBranch = "other" },
		"diff base":            func(p *pr.PullRequest) { p.DiffBaseCommit = "other" },
		"lifecycle":            func(p *pr.PullRequest) { p.Status = pr.StatusClosed },
		"lifecycle generation": func(p *pr.PullRequest) { p.LifecycleGeneration++ },
		"topology generation":  func(p *pr.PullRequest) { p.TopologyGeneration++ },
		"identity":             func(p *pr.PullRequest) { p.SyncExternalID = "github:other/repo#8" },
		"fork": func(p *pr.PullRequest) {
			p.SyncData = json.RawMessage(`{"github":{"head_repository_url":"https://github.com/other/fork"}}`)
		},
	}
	for _, kind := range []string{"publish", "work_publish", "rebase", "merge"} {
		for name, change := range changes {
			t.Run(kind+"/"+name, func(t *testing.T) {
				catalog, _ := operationCatalog(t)
				current, err := catalog.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusOpen, BaseBranch: "main", HeadBranch: "feature", BaseCommit: "base", HeadCommit: "head", DiffBaseCommit: "base", SyncProvider: "github", SyncExternalID: "github:owner/repo#7"})
				if err != nil {
					t.Fatal(err)
				}
				request := pr.Operation{RequestID: "claim", PullRequestID: current.ID, Kind: kind, ExpectedHead: current.HeadCommit, ExpectedInputs: pr.CaptureMutationInputs(current), Groups: []pr.FieldGroup{pr.TopologyGroup, pr.LifecycleGroup}}
				if _, err = catalog.mutate(current.ID, func(p *pr.PullRequest) error { change(p); return nil }); err != nil {
					t.Fatal(err)
				}
				if _, created, err := catalog.BeginOperation(t.Context(), request); created || !errors.Is(err, pr.ErrSynchronizationStale) {
					t.Fatalf("claim created=%v err=%v", created, err)
				}
				if _, found, err := catalog.GetOperation(t.Context(), request.RequestID); found || err != nil {
					t.Fatalf("rejected claim persisted: found=%v err=%v", found, err)
				}
			})
		}
	}
}

func TestMutationReplayPrecedesExpectedInputValidation(t *testing.T) {
	catalog, _ := operationCatalog(t)
	current, err := catalog.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusOpen, HeadCommit: "head"})
	if err != nil {
		t.Fatal(err)
	}
	current = seedComparison(t, catalog, current)
	request := pr.Operation{RequestID: "replay", PullRequestID: current.ID, Kind: "publish", ExpectedHead: current.HeadCommit, ExpectedInputs: pr.CaptureMutationInputs(current), Groups: []pr.FieldGroup{pr.TopologyGroup}}
	if _, created, err := catalog.BeginOperation(t.Context(), request); err != nil || !created {
		t.Fatalf("claim=%v %v", created, err)
	}
	if _, err := catalog.CompleteOperation(t.Context(), request.RequestID, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	request.ExpectedHead = "stale"
	if operation, created, err := catalog.BeginOperation(t.Context(), request); err != nil || created || operation.Status != "succeeded" {
		t.Fatalf("replay=%+v created=%v err=%v", operation, created, err)
	}
}
