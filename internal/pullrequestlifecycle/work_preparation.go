package pullrequestlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"time"

	"github.com/holark-ai/holark/internal/repository"
)

// PrepareWork reuses an accepted comparison only after a read-only observation.
// A cache miss delegates to the unchanged synchronization path, outside the
// verification scheduler slot. The caller must reload the PR before launching.
func (c *Coordinator) PrepareWork(ctx context.Context, id string) (resultErr error) {
	ctx = WithRequestID(ctx, RequestID(ctx))
	started := time.Now()
	reason := "none"
	defer func() {
		slog.DebugContext(ctx, "Work preparation completed", "pull_request_id", id, "request_id", RequestID(ctx), "duration", time.Since(started), "fallback_reason", reason, "error", resultErr)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	current, ok := c.registry.GetPullRequest(id)
	if !ok {
		return ErrNotFound
	}
	reader, supported := c.options.GitHubTransport.(GitHubWorkStateReader)
	if current.SyncProvider != SyncProviderGitHub || !supported {
		reason = "unsupported_provider"
	} else {
		_, err := c.schedulePreparationVerification(ctx, current, reader, "work_verification")
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A coalesced pass can belong to a different, cancelled caller. Only
		// this caller's context stops preparation; other failures fall back.
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		if err == nil {
			return nil
		}
		var miss workVerificationMiss
		reason = "verification_failed"
		if errors.As(err, &miss) {
			reason = string(miss)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fallbackStarted := time.Now()
	err := c.SyncPullRequest(ctx, id)
	slog.DebugContext(ctx, "Work preparation fallback sync completed", "pull_request_id", id, "request_id", RequestID(ctx), "duration", time.Since(fallbackStarted), "fallback_reason", reason, "error", err)
	return err
}

// Only fixed reasons are logged, never remote text or branch names.
type workVerificationMiss string

func (e workVerificationMiss) Error() string { return string(e) }

func (c *Coordinator) workComparisonCurrent(ctx context.Context, p PullRequest) bool {
	if !p.Status.Active() || !c.currentComparison(ctx, p) {
		return false
	}
	for _, operation := range p.Operations {
		if operation.Active() {
			return false
		}
	}
	return p.BaseCommit != "" && p.HeadCommit != "" && p.DiffBaseCommit != "" &&
		p.BaseCommit == p.Comparison.Inputs.Base.Commit && p.HeadCommit == p.Comparison.Inputs.Head.Commit && p.DiffBaseCommit == p.Comparison.MergeBase
}

func (c *Coordinator) verifyPreparation(ctx context.Context, id string, reader GitHubWorkStateReader) (PullRequest, error) {
	if err := ctx.Err(); err != nil {
		return PullRequest{}, err
	}
	current, ok := c.registry.GetPullRequest(id)
	if !ok {
		return PullRequest{}, ErrNotFound
	}
	if current.SyncProvider != SyncProviderGitHub || current.SyncExternalID == "" || !json.Valid(current.SyncData) || c.options.GitHubCodec == nil {
		return PullRequest{}, workVerificationMiss("unsupported_identity")
	}
	if current.Status != StatusOpen && current.Status != StatusDraft || !c.workComparisonCurrent(ctx, current) {
		return PullRequest{}, workVerificationMiss("local_preparation_stale")
	}
	// Capture generations before the read. Actions advance them at both start
	// and completion, even when the resulting values are unchanged.
	inputs := *CaptureMutationInputs(current)
	target := c.githubTarget(current)
	if marker := current.LifecycleConfirmation; marker != nil && marker.Status != current.Status {
		return PullRequest{}, workVerificationMiss("lifecycle_uncertain")
	}
	if c.options.Repository == nil || c.options.Projects == nil {
		return PullRequest{}, workVerificationMiss("local_repository_unavailable")
	}
	project, ok := c.options.Projects.Project(current.RepositoryID)
	if !ok {
		return PullRequest{}, workVerificationMiss("local_repository_unavailable")
	}
	seen := map[string]bool{}
	for _, commit := range []string{current.BaseCommit, current.HeadCommit, current.DiffBaseCommit} {
		if seen[commit] {
			continue
		}
		seen[commit] = true
		resolved, err := c.options.Repository.ResolveRef(ctx, browserRepository(project), commit)
		if err != nil || resolved != commit {
			return PullRequest{}, workVerificationMiss("local_commit_missing")
		}
	}
	remote, err := reader.GetWorkState(ctx, target)
	if err != nil {
		return PullRequest{}, workVerificationMiss("remote_observation_failed")
	}
	observed := pullRequestFromGitHub(current.RepositoryID, remote, "", c.now())
	other := CaptureMutationInputs(observed)
	remoteSource := DecodeGitHubObservation(observed.SyncData)
	if inputs.SyncExternalID != other.SyncExternalID || inputs.Status != other.Status || inputs.BaseBranch != other.BaseBranch || inputs.HeadBranch != other.HeadBranch || inputs.BaseRepositoryURL == "" || inputs.HeadRepositoryURL == "" || inputs.BaseRepositoryURL != other.BaseRepositoryURL || inputs.HeadRepositoryURL != other.HeadRepositoryURL ||
		current.BaseRef != repository.PublishedBranchIdentity(remoteSource.BaseRepositoryURL, other.BaseBranch) || current.HeadRef != repository.PublishedBranchIdentity(remoteSource.HeadRepositoryURL, other.HeadBranch) ||
		inputs.BaseCommit != other.BaseCommit || inputs.HeadCommit != other.HeadCommit || current.Title != observed.Title || current.Summary != observed.Summary {
		return PullRequest{}, workVerificationMiss("remote_content_changed")
	}
	if err := c.preparationStillCurrent(ctx, current); err != nil {
		return PullRequest{}, err
	}
	return current, ctx.Err()
}

// Ignore readiness and observation counters, but retain content, generation,
// confirmation and accepted-comparison checks across every remote request.
func (c *Coordinator) preparationStillCurrent(ctx context.Context, current PullRequest) error {
	latest, ok := c.registry.GetPullRequest(current.ID)
	if !ok {
		return ErrNotFound
	}
	// Generations also reject local actions that finished during the request,
	// even when the status and branch contents have returned to their old values.
	// Identical observations may advance read sequences without changing the
	// accepted state. Reuse is safe only if generations and content still match
	// and no action is active; this verification does not publish an observation.
	if *CaptureMutationInputs(current) != *CaptureMutationInputs(latest) || current.MetadataGeneration != latest.MetadataGeneration || current.Title != latest.Title || current.Summary != latest.Summary ||
		c.options.GitHubCodec.ExternalID(c.githubTarget(current)) != c.options.GitHubCodec.ExternalID(c.githubTarget(latest)) ||
		!reflect.DeepEqual(current.LifecycleConfirmation, latest.LifecycleConfirmation) || !reflect.DeepEqual(current.MetadataConfirmation, latest.MetadataConfirmation) || !reflect.DeepEqual(current.TopologyConfirmation, latest.TopologyConfirmation) ||
		!c.workComparisonCurrent(ctx, latest) {
		return workVerificationMiss("local_state_changed")
	}
	return ctx.Err()
}

func (c *Coordinator) schedulePreparationVerification(ctx context.Context, current PullRequest, reader GitHubWorkStateReader, section string) (PullRequest, error) {
	queued := time.Now()
	return refreshResult(ctx, c.options.Refresh, RefreshKey{RepositoryID: current.RepositoryID, PullRequestID: current.ID, Section: section}, func(passCtx context.Context) (PullRequest, error) {
		if err := ctx.Err(); err != nil {
			return PullRequest{}, err
		}
		// Verification has no side effects to finish after its caller leaves.
		// Other callers sharing this pass fall back if its owner cancels.
		passCtx, cancel := context.WithCancel(passCtx)
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		defer cancel()
		passCtx = WithRequestID(passCtx, RequestID(ctx))
		slog.DebugContext(passCtx, "Preparation verification scheduler acquired", "pull_request_id", current.ID, "request_id", RequestID(ctx), "section", section, "duration", time.Since(queued))
		started := time.Now()
		verified, err := c.verifyPreparation(passCtx, current.ID, reader)
		slog.DebugContext(passCtx, "Preparation verification completed", "pull_request_id", current.ID, "request_id", RequestID(ctx), "section", section, "duration", time.Since(started), "error", err)
		return verified, err
	})
}
