package pullrequestmerge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/repository"
)

var testNow = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

type storeStub struct {
	pullrequestlifecycle.ActionRegistry
	pullRequest pullrequestlifecycle.PullRequest
	mergeErr    error
	mergeCalls  int
	commit      string
	strategy    string
	mergedAt    time.Time
}

func (store *storeStub) GetPullRequest(id string) (pullrequestlifecycle.PullRequest, bool) {
	return store.pullRequest, id == store.pullRequest.ID
}

func (store *storeStub) CompleteTransition(_ context.Context, _ string, _, confirmed pullrequestlifecycle.PullRequest) (pullrequestlifecycle.PullRequest, error) {
	commit, strategy, at := confirmed.MergedCommit, confirmed.MergeStrategy, *confirmed.MergedAt
	store.mergeCalls++
	store.commit, store.strategy, store.mergedAt = commit, strategy, at
	if store.mergeErr != nil {
		return pullrequestlifecycle.PullRequest{}, store.mergeErr
	}
	store.pullRequest.Status = pullrequestlifecycle.StatusMerged
	store.pullRequest.MergedCommit = commit
	store.pullRequest.MergeStrategy = strategy
	store.pullRequest.MergedAt = &at
	return store.pullRequest, nil
}

type repositoryStub struct {
	refs        map[string]string
	errors      map[string]error
	mergeBase   string
	mergeErr    error
	onMergeBase func(string, string)
}

func (stub repositoryStub) Resolve(_ context.Context, ref string) (string, error) {
	if err := stub.errors[ref]; err != nil {
		return "", err
	}
	if resolved, ok := stub.refs[ref]; ok {
		return resolved, nil
	}
	for _, commit := range stub.refs {
		if commit == ref {
			return ref, nil
		}
	}
	return "", repository.ErrRefNotFound
}

func (stub repositoryStub) MergeBase(_ context.Context, base, head string) (string, error) {
	if stub.onMergeBase != nil {
		stub.onMergeBase(base, head)
	}
	return stub.mergeBase, stub.mergeErr
}

type refreshingRepositoryStub struct {
	repositoryStub
	head         string
	recoverHead  bool
	refreshed    bool
	refreshErr   error
	refreshCalls int
	resolveCalls []string
}

func (stub *refreshingRepositoryStub) Resolve(ctx context.Context, ref string) (string, error) {
	stub.resolveCalls = append(stub.resolveCalls, ref)
	if ref == stub.head {
		if stub.refreshed && stub.recoverHead {
			return ref, nil
		}
		return "", repository.ErrRefNotFound
	}
	return stub.repositoryStub.Resolve(ctx, ref)
}

func (stub *refreshingRepositoryStub) Refresh(context.Context) error {
	stub.refreshCalls++
	if stub.refreshErr == nil {
		stub.refreshed = true
	}
	return stub.refreshErr
}

type providerRepositoryStub struct {
	repositoryStub
	prepared             repository.Preparation
	prepareCalls         int
	providerResolveCalls int
}

func (stub *providerRepositoryStub) PrepareBranch(_ context.Context, branch string) (repository.Preparation, error) {
	stub.prepareCalls++
	if stub.prepared.Branch == "" {
		stub.prepared.Branch = branch
	}
	return stub.prepared, nil
}
func (stub *providerRepositoryStub) ResolveProviderBranch(_ context.Context, branch string) (string, error) {
	stub.providerResolveCalls++
	return stub.refs["refs/heads/"+branch], stub.errors["refs/heads/"+branch]
}

type githubStub struct {
	readiness GitHubReadiness
	ready     bool
	mergeErr  error
	request   GitHubMergeRequest
	calls     int
}

func (github *githubStub) Readiness(GitHubTarget) (GitHubReadiness, bool) {
	return github.readiness, github.ready
}
func (github *githubStub) Squash(_ context.Context, request GitHubMergeRequest) (GitHubMergeResult, error) {
	github.calls++
	github.request = request
	if github.mergeErr != nil {
		return GitHubMergeResult{}, github.mergeErr
	}
	return GitHubMergeResult{MergedCommit: "merged-sha"}, nil
}

type commentsStub struct {
	count int
	err   error
	calls int
}

func (comments *commentsStub) UnresolvedCount(context.Context, string) (int, error) {
	comments.calls++
	return comments.count, comments.err
}

func openGitHubPullRequest() pullrequestlifecycle.PullRequest {
	return pullrequestlifecycle.PullRequest{
		ID: "pr-12", Title: "Ship merge parity", Summary: "Keep GitHub authoritative.",
		BaseBranch: "main", BaseCommit: "base-sha", HeadBranch: "feature", HeadCommit: "head-sha", Status: pullrequestlifecycle.StatusOpen,
		SyncProvider: string(pullrequestlifecycle.SyncProviderGitHub), SyncExternalID: "github:owner/repo#12",
	}
}

func readyCoordinator(pullRequest pullrequestlifecycle.PullRequest) (*Coordinator, *storeStub, *githubStub, *commentsStub) {
	store := &storeStub{pullRequest: pullRequest}
	github := &githubStub{ready: true, readiness: GitHubReadiness{
		HeadCommit: pullRequest.HeadCommit, ChecksState: ChecksPassing, MergeabilityState: MergeabilityMergeable,
		DetailsURL: "https://github.com/owner/repo/pull/12", SyncedAt: testNow,
	}}
	comments := &commentsStub{}
	coordinator := New(Options{Sync: syncStub{},
		RepositoryURL: "https://github.com/owner/repo", Store: store,
		Repository: repositoryStub{refs: map[string]string{"refs/heads/main": "base-sha", "refs/heads/feature": "head-sha"}, mergeBase: "base-sha"},
		GitHub:     github, Comments: comments, Clock: ClockFunc(func() time.Time { return testNow }),
	})
	return coordinator, store, github, comments
}

func TestProjectionAndMergeUseTheAcceptedCachedBase(t *testing.T) {
	pullRequest := openGitHubPullRequest()
	store := &storeStub{pullRequest: pullRequest}
	github := &githubStub{ready: true, readiness: GitHubReadiness{
		HeadCommit: pullRequest.HeadCommit, ChecksState: ChecksPassing, MergeabilityState: MergeabilityMergeable, SyncedAt: testNow,
	}}
	repository := &providerRepositoryStub{
		repositoryStub: repositoryStub{refs: map[string]string{"refs/heads/main": "base-sha", "refs/heads/feature": "head-sha"}, mergeBase: "base-sha"},
		prepared:       repository.Preparation{Branch: "main", Commit: "base-sha"},
	}
	coordinator := New(Options{Sync: syncStub{}, Store: store, Repository: repository, GitHub: github, Clock: ClockFunc(func() time.Time { return testNow })})
	projected := coordinator.Project(t.Context(), pullRequest)
	if projected.Mergeable == nil || !*projected.Mergeable || repository.providerResolveCalls != 0 || repository.prepareCalls != 0 {
		t.Fatalf("projected=%+v provider resolves=%d prepares=%d", projected, repository.providerResolveCalls, repository.prepareCalls)
	}
	if _, err := coordinator.Merge(t.Context(), pullRequest.ID, Request{Strategy: "squash"}); err != nil {
		t.Fatal(err)
	}
	if repository.prepareCalls != 0 || repository.providerResolveCalls != 0 {
		t.Fatalf("provider resolves=%d prepares=%d", repository.providerResolveCalls, repository.prepareCalls)
	}
}

func TestStoredPullRequestHeadIsAuthoritativeWhenLocalBranchHasNotMoved(t *testing.T) {
	pullRequest := openGitHubPullRequest()
	pullRequest.HeadCommit = "published-sha"
	store := &storeStub{pullRequest: pullRequest}
	github := &githubStub{ready: true, readiness: GitHubReadiness{
		HeadCommit: "published-sha", ChecksState: ChecksPassing, MergeabilityState: MergeabilityMergeable, SyncedAt: testNow,
	}}
	var mergeBaseCalls [][2]string
	repository := &providerRepositoryStub{
		repositoryStub: repositoryStub{
			refs:      map[string]string{"refs/heads/main": "base-sha", "refs/heads/feature": "old-sha", "published-sha": "published-sha"},
			mergeBase: "base-sha",
			onMergeBase: func(base, head string) {
				mergeBaseCalls = append(mergeBaseCalls, [2]string{base, head})
			},
		},
		prepared: repository.Preparation{Branch: "main", Commit: "base-sha"},
	}
	coordinator := New(Options{Sync: syncStub{},
		Store: store, Repository: repository, GitHub: github, Clock: ClockFunc(func() time.Time { return testNow }),
	})

	projected := coordinator.Project(t.Context(), pullRequest)
	if projected.Mergeable == nil || !*projected.Mergeable || projected.MergeBlockedReason != "" {
		t.Fatalf("projected=%+v", projected)
	}
	if _, err := coordinator.Merge(t.Context(), pullRequest.ID, Request{Strategy: "squash"}); err != nil {
		t.Fatal(err)
	}
	if len(mergeBaseCalls) != 2 {
		t.Fatalf("merge base calls=%v", mergeBaseCalls)
	}
	for _, call := range mergeBaseCalls {
		if call != [2]string{"base-sha", "published-sha"} {
			t.Fatalf("merge base call=%v", call)
		}
	}
	if repository.providerResolveCalls != 0 || repository.prepareCalls != 0 {
		t.Fatalf("provider resolves=%d prepares=%d", repository.providerResolveCalls, repository.prepareCalls)
	}
	if github.request.ExpectedHeadSHA != "published-sha" || store.commit != "merged-sha" {
		t.Fatalf("expected head=%q stored commit=%q", github.request.ExpectedHeadSHA, store.commit)
	}
}

func TestMissingExactHeadIsRecoveredByOneRefreshWithoutFollowingBranchTip(t *testing.T) {
	pullRequest := openGitHubPullRequest()
	var mergeBaseCalls [][2]string
	repository := &refreshingRepositoryStub{
		repositoryStub: repositoryStub{
			refs: map[string]string{
				"refs/heads/main":    "base-sha",
				"refs/heads/feature": "refreshed-tip-sha",
			},
			mergeBase: "base-sha",
			onMergeBase: func(base, head string) {
				mergeBaseCalls = append(mergeBaseCalls, [2]string{base, head})
			},
		},
		head:        pullRequest.HeadCommit,
		recoverHead: true,
	}
	github := &githubStub{ready: true, readiness: GitHubReadiness{
		HeadCommit: pullRequest.HeadCommit, MergeabilityState: MergeabilityMergeable, SyncedAt: testNow,
	}}
	coordinator := New(Options{Sync: syncStub{work: repository.Refresh}, Store: &storeStub{pullRequest: pullRequest}, Repository: repository, GitHub: github, Clock: ClockFunc(func() time.Time { return testNow })})

	projected := coordinator.Project(t.Context(), pullRequest)
	if projected.MergeBlockedReason != "head_not_found" || repository.refreshCalls != 0 {
		t.Fatalf("cached projection=%+v refreshes=%d", projected, repository.refreshCalls)
	}
	if _, err := coordinator.Merge(t.Context(), pullRequest.ID, Request{Strategy: "squash"}); err != nil {
		t.Fatal(err)
	}
	if repository.refreshCalls != 1 {
		t.Fatalf("refresh calls=%d", repository.refreshCalls)
	}
	if len(repository.resolveCalls) != 2 || repository.resolveCalls[0] != pullRequest.HeadCommit || repository.resolveCalls[1] != pullRequest.HeadCommit {
		t.Fatalf("resolve calls=%v", repository.resolveCalls)
	}
	if len(mergeBaseCalls) != 1 || mergeBaseCalls[0] != [2]string{"base-sha", pullRequest.HeadCommit} {
		t.Fatalf("merge base calls=%v", mergeBaseCalls)
	}
}

func TestMissingExactHeadRefreshFailuresAndStillMissingAreDistinct(t *testing.T) {
	for _, test := range []struct {
		name       string
		refreshErr error
		recover    bool
		reason     string
	}{
		{name: "refresh failure", refreshErr: errors.New("fetch failed"), reason: "repository_unavailable"},
		{name: "still missing", recover: false, reason: "head_not_found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pullRequest := openGitHubPullRequest()
			mergeBaseCalls := 0
			repository := &refreshingRepositoryStub{
				repositoryStub: repositoryStub{
					refs:      map[string]string{"refs/heads/main": "base-sha", "refs/heads/feature": "refreshed-tip-sha"},
					mergeBase: "base-sha",
					onMergeBase: func(string, string) {
						mergeBaseCalls++
					},
				},
				head: pullRequest.HeadCommit, recoverHead: test.recover, refreshErr: test.refreshErr,
			}
			coordinator := New(Options{Sync: syncStub{work: repository.Refresh}, Store: &storeStub{pullRequest: pullRequest}, Repository: repository})
			_, err := coordinator.Merge(t.Context(), pullRequest.ID, Request{Strategy: "squash"})
			var blocked *BlockedError
			if test.refreshErr != nil && !errors.Is(err, test.refreshErr) || test.refreshErr == nil && (!errors.As(err, &blocked) || blocked.Reason != test.reason) {
				t.Fatalf("merge error=%v", err)
			}
			if repository.refreshCalls != 1 || mergeBaseCalls != 0 {
				t.Fatalf("refresh calls=%d merge base calls=%d", repository.refreshCalls, mergeBaseCalls)
			}
		})
	}
}

func TestEmptyStoredPullRequestHeadIsNotFound(t *testing.T) {
	pullRequest := openGitHubPullRequest()
	pullRequest.HeadCommit = ""
	mergeBaseCalls := 0
	coordinator, store, github, _ := readyCoordinator(pullRequest)
	repository := coordinator.options.Repository.(repositoryStub)
	repository.onMergeBase = func(string, string) { mergeBaseCalls++ }
	coordinator.options.Repository = repository

	projected := coordinator.Project(t.Context(), pullRequest)
	if projected.Mergeable == nil || *projected.Mergeable || projected.MergeBlockedReason != "head_not_found" {
		t.Fatalf("projected=%+v", projected)
	}
	_, err := coordinator.Merge(t.Context(), pullRequest.ID, Request{Strategy: "squash"})
	var blocked *BlockedError
	if !errors.As(err, &blocked) || blocked.Reason != "head_not_found" {
		t.Fatalf("err=%v", err)
	}
	if mergeBaseCalls != 0 || github.calls != 0 || store.mergeCalls != 0 {
		t.Fatalf("merge bases=%d github=%d store=%d", mergeBaseCalls, github.calls, store.mergeCalls)
	}
}

func TestChecksAreInformationalWhenGitHubIsMergeable(t *testing.T) {
	for _, checks := range []ChecksState{ChecksUnknown, ChecksPending, ChecksFailing, ChecksError, ChecksPassing} {
		t.Run(string(checks), func(t *testing.T) {
			pullRequest := openGitHubPullRequest()
			coordinator, _, github, _ := readyCoordinator(pullRequest)
			github.readiness.ChecksState = checks
			projected := coordinator.Project(t.Context(), pullRequest)
			if projected.Mergeable == nil || !*projected.Mergeable || projected.MergeBlockedReason != "" {
				t.Fatalf("checks=%q mergeable=%v reason=%q", checks, projected.Mergeable, projected.MergeBlockedReason)
			}
			if _, err := coordinator.Merge(t.Context(), pullRequest.ID, Request{Strategy: "squash"}); err != nil {
				t.Fatalf("checks=%q merge error=%v", checks, err)
			}
		})
	}
}

func TestReadinessBlocksMissingStaleMismatchedAndUnmergeableStates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*githubStub)
		reason string
	}{
		{name: "missing", mutate: func(g *githubStub) { g.ready = false }, reason: "github_status_missing"},
		{name: "stale", mutate: func(g *githubStub) { g.readiness.SyncedAt = testNow.Add(-ReadinessMaxAge - time.Second) }, reason: "github_status_stale"},
		{name: "mismatched head", mutate: func(g *githubStub) { g.readiness.HeadCommit = "other" }, reason: "github_status_stale"},
		{name: "unknown", mutate: func(g *githubStub) { g.readiness.MergeabilityState = MergeabilityUnknown }, reason: "github_mergeability_unknown"},
		{name: "blocked", mutate: func(g *githubStub) { g.readiness.MergeabilityState = MergeabilityBlocked }, reason: "github_merge_blocked"},
		{name: "conflicting", mutate: func(g *githubStub) { g.readiness.MergeabilityState = MergeabilityConflicting }, reason: "github_merge_blocked"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pullRequest := openGitHubPullRequest()
			coordinator, _, github, _ := readyCoordinator(pullRequest)
			test.mutate(github)
			projected := coordinator.Project(t.Context(), pullRequest)
			if projected.Mergeable == nil || *projected.Mergeable || projected.MergeBlockedReason != test.reason {
				t.Fatalf("mergeable=%v reason=%q", projected.Mergeable, projected.MergeBlockedReason)
			}
		})
	}
}

func TestGitHubFailuresDoNotRecordMergedState(t *testing.T) {
	for _, kind := range []GitHubErrorKind{GitHubUnavailable, GitHubBlocked, GitHubHeadChanged} {
		t.Run(string(kind), func(t *testing.T) {
			pullRequest := openGitHubPullRequest()
			coordinator, store, github, _ := readyCoordinator(pullRequest)
			github.mergeErr = &GitHubError{Kind: kind}
			_, err := coordinator.Merge(t.Context(), pullRequest.ID, Request{Strategy: "squash"})
			var got *GitHubError
			if !errors.As(err, &got) || got.Kind != kind || store.mergeCalls != 0 || store.pullRequest.Status != pullrequestlifecycle.StatusOpen {
				t.Fatalf("err=%v calls=%d status=%q", err, store.mergeCalls, store.pullRequest.Status)
			}
		})
	}
}

func TestUnavailableGitHubProviderIsTyped(t *testing.T) {
	pullRequest := openGitHubPullRequest()
	coordinator, store, _, _ := readyCoordinator(pullRequest)
	coordinator.options.GitHub = nil
	_, err := coordinator.Merge(t.Context(), pullRequest.ID, Request{Strategy: "squash"})
	var githubErr *GitHubError
	if !errors.As(err, &githubErr) || githubErr.Kind != GitHubUnavailable || store.mergeCalls != 0 {
		t.Fatalf("err=%v merge calls=%d", err, store.mergeCalls)
	}
}

func TestMergeRequestAndLifecycleFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   pullrequestlifecycle.Status
		strategy string
		want     error
	}{
		{name: "invalid strategy", status: pullrequestlifecycle.StatusOpen, strategy: "merge", want: ErrInvalidRequest},
		{name: "already merged", status: pullrequestlifecycle.StatusMerged, strategy: "squash", want: ErrPullRequestMerged},
		{name: "draft", status: pullrequestlifecycle.StatusDraft, strategy: "squash", want: ErrPullRequestClosed},
		{name: "wip", status: pullrequestlifecycle.StatusWIP, strategy: "squash", want: ErrPullRequestClosed},
		{name: "closed", status: pullrequestlifecycle.StatusClosed, strategy: "squash", want: ErrPullRequestClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			pullRequest := openGitHubPullRequest()
			pullRequest.Status = test.status
			coordinator, store, github, _ := readyCoordinator(pullRequest)
			_, err := coordinator.Merge(t.Context(), pullRequest.ID, Request{Strategy: test.strategy})
			if !errors.Is(err, test.want) || store.mergeCalls != 0 || github.calls != 0 {
				t.Fatalf("err=%v store calls=%d github calls=%d", err, store.mergeCalls, github.calls)
			}
		})
	}
}

func TestUnresolvedCommentsCanBeBypassedAndLookupFailuresAreTyped(t *testing.T) {
	pullRequest := openGitHubPullRequest()
	coordinator, store, github, comments := readyCoordinator(pullRequest)
	comments.count = 2
	if _, err := coordinator.Merge(t.Context(), pullRequest.ID, Request{Strategy: "squash"}); !errors.Is(err, ErrUnresolvedComments) {
		t.Fatalf("err=%v", err)
	}
	if _, err := coordinator.Merge(t.Context(), pullRequest.ID, Request{Strategy: "squash", BypassUnresolvedComments: true}); err != nil {
		t.Fatal(err)
	}
	if comments.calls != 1 || store.mergeCalls != 1 || github.calls != 1 {
		t.Fatalf("comments=%d store=%d github=%d", comments.calls, store.mergeCalls, github.calls)
	}

	pullRequest = openGitHubPullRequest()
	coordinator, _, _, comments = readyCoordinator(pullRequest)
	comments.err = errors.New("database unavailable")
	if _, err := coordinator.Merge(t.Context(), pullRequest.ID, Request{Strategy: "squash"}); !errors.Is(err, ErrCommentLookupFailed) {
		t.Fatalf("err=%v", err)
	}
}

func TestReconciliationFailureOccursAfterGitHubSuccess(t *testing.T) {
	pullRequest := openGitHubPullRequest()
	coordinator, store, github, _ := readyCoordinator(pullRequest)
	store.mergeErr = errors.New("write failed")
	_, err := coordinator.Merge(t.Context(), pullRequest.ID, Request{Strategy: "squash"})
	if !errors.Is(err, ErrReconciliationFailed) || github.calls != 1 || store.mergeCalls != 1 {
		t.Fatalf("err=%v github=%d store=%d", err, github.calls, store.mergeCalls)
	}
}

func TestLocalOnlyPullRequestsAreUnsupported(t *testing.T) {
	pullRequest := openGitHubPullRequest()
	pullRequest.SyncProvider = ""
	coordinator, store, github, _ := readyCoordinator(pullRequest)
	projected := coordinator.Project(t.Context(), pullRequest)
	if projected.Mergeable == nil || *projected.Mergeable || projected.MergeBlockedReason != "merge_provider_unsupported" || projected.MergeProvider != "local" {
		t.Fatalf("projected=%+v", projected)
	}
	_, err := coordinator.Merge(t.Context(), pullRequest.ID, Request{Strategy: "squash"})
	if !errors.Is(err, ErrProviderUnsupported) || store.mergeCalls != 0 || github.calls != 0 {
		t.Fatalf("err=%v store=%d github=%d", err, store.mergeCalls, github.calls)
	}
}

func (*storeStub) BeginOperation(_ context.Context, operation pullrequestlifecycle.Operation) (pullrequestlifecycle.Operation, bool, error) {
	operation.Status = "running"
	return operation, true, nil
}
func (*storeStub) GetOperation(context.Context, string) (pullrequestlifecycle.Operation, bool, error) {
	return pullrequestlifecycle.Operation{}, false, nil
}
func (*storeStub) CompleteOperation(context.Context, string, string, string) (pullrequestlifecycle.Operation, error) {
	return pullrequestlifecycle.Operation{}, nil
}
func (stub repositoryStub) Refresh(context.Context) error { return nil }
func (stub repositoryStub) PrepareBranch(ctx context.Context, branch string) (repository.Preparation, error) {
	commit, err := stub.ResolveProviderBranch(ctx, branch)
	return repository.Preparation{Branch: branch, Commit: commit}, err
}
func (stub repositoryStub) ResolveProviderBranch(ctx context.Context, branch string) (string, error) {
	return stub.Resolve(ctx, "refs/heads/"+branch)
}

type syncStub struct{ work func(context.Context) error }

func (s syncStub) SyncPullRequest(ctx context.Context, _ string) error {
	if s.work != nil {
		return s.work(ctx)
	}
	return nil
}
