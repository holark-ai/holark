package pullrequestmerge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/repository"
)

const ReadinessMaxAge = 5 * time.Minute

var (
	ErrInvalidRequest       = errors.New("invalid merge request")
	ErrNotFound             = errors.New("pull request not found")
	ErrPullRequestMerged    = errors.New("pull request is already merged")
	ErrPullRequestClosed    = errors.New("pull request is not open")
	ErrUnresolvedComments   = errors.New("pull request has unresolved comments")
	ErrCommentLookupFailed  = errors.New("pull request comments could not be read")
	ErrProviderUnsupported  = errors.New("pull request merge provider is unsupported")
	ErrReconciliationFailed = errors.New("merged pull request could not be recorded")
)

type Store interface {
	pullrequestlifecycle.ActionRegistry
	GetPullRequest(string) (pullrequestlifecycle.PullRequest, bool)
	CompleteTransition(context.Context, string, pullrequestlifecycle.PullRequest, pullrequestlifecycle.PullRequest) (pullrequestlifecycle.PullRequest, error)
}

type Repository interface {
	Resolve(context.Context, string) (string, error)
	MergeBase(context.Context, string, string) (string, error)
}

type Comments interface {
	UnresolvedCount(context.Context, string) (int, error)
}

type Clock interface{ Now() time.Time }
type ClockFunc func() time.Time

func (fn ClockFunc) Now() time.Time { return fn() }

// MergePreparer optionally reuses verified preparation and refreshes readiness.
// Synchronizers without this capability retain full synchronization.
type MergePreparer interface {
	PrepareMerge(context.Context, string) error
}

type Options struct {
	Sync          pullrequestlifecycle.Synchronizer
	RepositoryURL string
	Store         Store
	Repository    Repository
	GitHub        GitHubProvider
	Comments      Comments
	Clock         Clock
}

type Coordinator struct{ options Options }

func New(options Options) *Coordinator {
	if options.Clock == nil {
		options.Clock = ClockFunc(func() time.Time { return time.Now().UTC() })
	}
	return &Coordinator{options: options}
}

type Request struct {
	Strategy                 string
	BypassUnresolvedComments bool
}

type Result struct {
	PullRequest  pullrequestlifecycle.PullRequest
	MergedCommit string
}

type BlockedError struct{ Reason string }

func (err *BlockedError) Error() string {
	if err == nil || err.Reason == "" {
		return "pull request merge is blocked"
	}
	return fmt.Sprintf("pull request merge is blocked: %s", err.Reason)
}

type decision struct {
	mergeable bool
	reason    string
	readiness GitHubReadiness
}

// Project applies the same readiness policy used by Merge to the API model.
func (coordinator *Coordinator) Project(ctx context.Context, pullRequest pullrequestlifecycle.PullRequest) pullrequestlifecycle.PullRequest {
	pullRequest.MergeRequestStrategy = "squash"
	pullRequest.MergeProvider = "github"
	if pullRequest.SyncProvider != string(pullrequestlifecycle.SyncProviderGitHub) {
		pullRequest.MergeProvider = "local"
	}
	result := coordinator.readiness(ctx, pullRequest)
	pullRequest.Mergeable = new(bool)
	*pullRequest.Mergeable = result.mergeable
	pullRequest.MergeBlockedReason = result.reason
	return pullRequest
}

func (coordinator *Coordinator) Merge(ctx context.Context, id string, request Request) (result Result, resultErr error) {
	ctx = pullrequestlifecycle.WithRequestID(ctx, pullrequestlifecycle.RequestID(ctx))
	started := time.Now()
	defer func() {
		slog.DebugContext(ctx, "Pull request merge completed", "pull_request_id", id, "request_id", pullrequestlifecycle.RequestID(ctx), "duration", time.Since(started), "error", resultErr)
	}()
	if strings.TrimSpace(id) == "" || request.Strategy != "squash" {
		return Result{}, ErrInvalidRequest
	}
	pullRequest, ok := coordinator.options.Store.GetPullRequest(id)
	if !ok {
		return Result{}, ErrNotFound
	}
	requestID := pullrequestlifecycle.RequestID(ctx)
	registry := coordinator.options.Store
	previous, found, err := registry.GetOperation(ctx, requestID)
	if err != nil {
		return Result{}, err
	}
	if found {
		if previous.PullRequestID != id || previous.Kind != "merge" {
			return Result{}, ErrInvalidRequest
		}
		if previous.Status == "succeeded" {
			return Result{PullRequest: pullRequest, MergedCommit: pullRequest.MergedCommit}, nil
		}
		if previous.Status == "failed" {
			return Result{}, errors.New(previous.Error)
		}
		return Result{}, pullrequestlifecycle.ErrOperationInProgress
	}
	if coordinator.options.Sync == nil {
		return Result{}, &BlockedError{Reason: "repository_unavailable"}
	}
	prepare := coordinator.options.Sync.SyncPullRequest
	if preparer, ok := coordinator.options.Sync.(MergePreparer); ok {
		prepare = preparer.PrepareMerge
	}
	if err := prepare(ctx, id); err != nil {
		return Result{}, err
	}
	pullRequest, ok = coordinator.options.Store.GetPullRequest(id)
	if !ok {
		return Result{}, ErrNotFound
	}
	switch pullRequest.Status {
	case pullrequestlifecycle.StatusMerged:
		return Result{}, ErrPullRequestMerged
	case pullrequestlifecycle.StatusOpen:
	default:
		return Result{}, ErrPullRequestClosed
	}
	if pullRequest.SyncProvider != string(pullrequestlifecycle.SyncProviderGitHub) {
		return Result{}, ErrProviderUnsupported
	}
	mutationAttempted := false
	operation, created, err := registry.BeginOperation(ctx, pullrequestlifecycle.Operation{
		RequestID: requestID, PullRequestID: id, Kind: "merge",
		RequestedStatus: pullrequestlifecycle.StatusMerged, ExpectedHead: pullRequest.HeadCommit, ExpectedInputs: pullrequestlifecycle.CaptureMutationInputs(pullRequest),
		Groups: []pullrequestlifecycle.FieldGroup{pullrequestlifecycle.LifecycleGroup, pullrequestlifecycle.TopologyGroup},
	})
	if err != nil {
		return Result{}, err
	}
	if !created {
		if operation.Status == "succeeded" {
			current, found := coordinator.options.Store.GetPullRequest(id)
			if found {
				return Result{PullRequest: current, MergedCommit: current.MergedCommit}, nil
			}
		}
		return Result{}, pullrequestlifecycle.ErrOperationInProgress
	}
	defer func() {
		if resultErr == nil {
			return
		}
		outcome, message := "failed", resultErr.Error()
		var remote *GitHubError
		var uncertain interface{ Uncertain() bool }
		if mutationAttempted {
			if errors.As(resultErr, &uncertain) {
				if uncertain.Uncertain() {
					outcome = "uncertain"
				}
			} else if !(errors.As(resultErr, &remote) && (remote.Kind == GitHubHeadChanged || remote.Kind == GitHubBlocked)) {
				outcome = "uncertain"
			}
		}
		_, err := registry.CompleteOperation(context.WithoutCancel(ctx), operation.RequestID, outcome, message)
		if err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	if !request.BypassUnresolvedComments && coordinator.options.Comments != nil {
		count, err := coordinator.options.Comments.UnresolvedCount(ctx, pullRequest.ID)
		if err != nil {
			return Result{}, fmt.Errorf("%w: %w", ErrCommentLookupFailed, err)
		}
		if count > 0 {
			return Result{}, ErrUnresolvedComments
		}
	}
	if coordinator.options.Repository == nil {
		return Result{}, &BlockedError{Reason: "repository_unavailable"}
	}
	readiness := coordinator.readiness(ctx, pullRequest)
	if !readiness.mergeable {
		switch readiness.reason {
		case "merge_provider_unsupported":
			return Result{}, ErrProviderUnsupported
		case "github_unavailable":
			return Result{}, &GitHubError{Kind: GitHubUnavailable, Message: "GitHub merge is unavailable."}
		default:
			return Result{}, &BlockedError{Reason: readiness.reason}
		}
	}
	body := strings.TrimSpace(pullRequest.Summary)
	if url := strings.TrimSpace(readiness.readiness.DetailsURL); url != "" {
		if body != "" {
			body += "\n\n"
		}
		body += url
	}
	mutationAttempted = true
	mutationStarted := time.Now()
	merged, err := coordinator.options.GitHub.Squash(ctx, GitHubMergeRequest{
		Target:          target(coordinator.options.RepositoryURL, pullRequest),
		Title:           pullRequest.Title,
		CommitMessage:   body,
		ExpectedHeadSHA: pullRequest.HeadCommit,
	})
	slog.DebugContext(ctx, "Pull request merge mutation completed", "pull_request_id", id, "request_id", requestID, "duration", time.Since(mutationStarted), "error", err)
	if err != nil {
		return Result{}, err
	}
	confirmed := pullRequest
	now := coordinator.options.Clock.Now().UTC()
	confirmed.Status, confirmed.MergedCommit, confirmed.MergeStrategy = pullrequestlifecycle.StatusMerged, merged.MergedCommit, "squash"
	confirmed.MergedAt, confirmed.ClosedAt, confirmed.UpdatedAt = &now, nil, now
	recorded, err := coordinator.options.Store.CompleteTransition(context.WithoutCancel(ctx), requestID, pullRequest, confirmed)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrReconciliationFailed, err)
	}
	return Result{PullRequest: recorded, MergedCommit: merged.MergedCommit}, nil
}

func (coordinator *Coordinator) readiness(ctx context.Context, pullRequest pullrequestlifecycle.PullRequest) decision {
	if pullRequest.Status != pullrequestlifecycle.StatusOpen {
		return decision{reason: string(pullRequest.Status)}
	}
	if pullRequest.SyncProvider != string(pullrequestlifecycle.SyncProviderGitHub) {
		return decision{reason: "merge_provider_unsupported"}
	}
	if pullRequest.ComparisonState != "" && !pullRequest.HasCurrentComparison() {
		return decision{reason: "comparison_stale"}
	}
	if coordinator.options.Repository == nil {
		return decision{reason: "repository_unavailable"}
	}
	base := pullRequest.BaseCommit
	if strings.TrimSpace(base) == "" {
		return decision{reason: "base_not_found"}
	}
	head := pullRequest.HeadCommit
	if strings.TrimSpace(head) == "" {
		return decision{reason: "head_not_found"}
	}
	if reason := coordinator.resolveHead(ctx, head); reason != "" {
		return decision{reason: reason}
	}
	mergeBase, err := coordinator.options.Repository.MergeBase(ctx, base, head)
	if err != nil || mergeBase != base {
		return decision{reason: "not_up_to_date"}
	}
	if coordinator.options.GitHub == nil {
		return decision{reason: "github_unavailable"}
	}
	providerReadiness, ok := coordinator.options.GitHub.Readiness(target(coordinator.options.RepositoryURL, pullRequest))
	if !ok {
		return decision{reason: "github_status_missing"}
	}
	if providerReadiness.HeadCommit != pullRequest.HeadCommit || providerReadiness.SyncedAt.Before(coordinator.options.Clock.Now().UTC().Add(-ReadinessMaxAge)) {
		return decision{reason: "github_status_stale", readiness: providerReadiness}
	}
	switch providerReadiness.MergeabilityState {
	case MergeabilityMergeable:
		return decision{mergeable: true, readiness: providerReadiness}
	case MergeabilityBlocked, MergeabilityConflicting:
		return decision{reason: "github_merge_blocked", readiness: providerReadiness}
	default:
		return decision{reason: "github_mergeability_unknown", readiness: providerReadiness}
	}
}

func (coordinator *Coordinator) resolveHead(ctx context.Context, head string) string {
	resolved, err := coordinator.options.Repository.Resolve(ctx, head)
	if err == nil && strings.TrimSpace(resolved) != "" {
		return ""
	}
	if !errors.Is(err, repository.ErrRefNotFound) {
		return "repository_unavailable"
	}
	return "head_not_found"
}

func target(repositoryURL string, pullRequest pullrequestlifecycle.PullRequest) GitHubTarget {
	return GitHubTarget{RepositoryURL: repositoryURL, ExternalID: pullRequest.SyncExternalID, SyncData: pullRequest.SyncData}
}
