package pullrequestlifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/holark-ai/holark/internal/holons"
)

var (
	ErrPublicationUnavailable = errors.New("no pull request is available to publish from this Holon")
	ErrPublicationInactive    = errors.New("the pull request is closed or merged")
	ErrPublicationStale       = errors.New("the pull request head changed before publication")
	ErrPublicationNoChanges   = errors.New("there are no unpublished committed changes")
)

type PublicationCatalog interface {
	ActionRegistry
	ListPullRequests(string) []PullRequest
	GetPullRequest(string) (PullRequest, bool)
}

type PublicationHolons interface {
	Get(context.Context, string) (holons.Holon, error)
	InspectWorkspace(context.Context, string) (holons.WorkspaceInspection, error)
	PublishWithCommitter(context.Context, string, holons.Publish, func(context.Context, holons.Holon) error) (holons.Holon, error)
}

type HolonPublicationCommitter interface {
	CommitHolonPublication(context.Context, string, string, string, holons.Holon) error
}

// HolonPublisher publishes the normal or issue Holon that created a PR.
// Worker publication remains owned by the worker workflow.
type HolonPublisher struct {
	readiness    PublicationGate
	repositoryID string
	catalog      PublicationCatalog
	holons       PublicationHolons
	committer    HolonPublicationCommitter
}

type PublicationGate interface {
	PrepareManualPublication(context.Context, string) (holons.PublicationReadiness, error)
}

func NewHolonPublisher(repositoryID string, catalog PublicationCatalog, workspaces PublicationHolons, committer HolonPublicationCommitter, readiness PublicationGate) *HolonPublisher {
	return &HolonPublisher{repositoryID: repositoryID, catalog: catalog, holons: workspaces, committer: committer, readiness: readiness}
}

func (p *HolonPublisher) Publish(ctx context.Context, id, explicitTarget string) (result holons.Holon, resultErr error) {
	requestID := RequestID(ctx)
	started := time.Now()
	defer func() {
		slog.DebugContext(ctx, "Manual publication completed", "operation_id", requestID, "duration", time.Since(started), "error", resultErr)
	}()
	h, err := p.holons.Get(ctx, id)
	if err != nil {
		return holons.Holon{}, err
	}
	if h.Kind != holons.KindNormal && h.Kind != holons.KindIssue {
		return holons.Holon{}, ErrPublicationUnavailable
	}
	pr, ok := p.linkedPullRequest(h)
	if !ok {
		return holons.Holon{}, ErrPublicationUnavailable
	}
	previous, found, err := p.catalog.GetOperation(ctx, requestID)
	if err != nil {
		return holons.Holon{}, err
	}
	if found {
		if !MatchesOperationRequest(previous, Operation{Kind: "publish", PullRequestID: pr.ID, HolonID: id}) {
			return holons.Holon{}, OperationRequestConflict()
		}
		if previous.Status == "succeeded" {
			return p.holons.Get(ctx, id)
		}
		if previous.Status == "failed" {
			return holons.Holon{}, StoredOperationError(previous)
		}
		return holons.Holon{}, ErrOperationInProgress
	}

	expectedTarget := ExpectedPublicationTarget(h, explicitTarget, pr.HeadCommit)
	preparedAt := time.Now()
	r, err := p.readiness.PrepareManualPublication(ctx, id)
	slog.DebugContext(ctx, "Publication preparation completed", "operation_id", requestID, "duration", time.Since(preparedAt), "error", err)
	if err != nil {
		return holons.Holon{}, err
	}
	pr, ok = p.catalog.GetPullRequest(pr.ID)
	if !ok {
		return holons.Holon{}, ErrPublicationUnavailable
	}
	if !pr.Status.Active() {
		return holons.Holon{}, ErrPublicationInactive
	}
	if h.ReadOnly || h.ArchivedAt != nil || h.WorktreeBranch == "" {
		return holons.Holon{}, ErrPublicationUnavailable
	}
	if pr.HeadCommit != r.TargetCommit || pr.HeadBranch != r.TargetBranch || (r.TargetVersion != "" && r.TargetVersion != fmt.Sprintf("%#v", *CaptureMutationInputs(pr))) {
		return holons.Holon{}, ErrPublicationStale
	}

	branch, expected := r.TargetBranch, r.TargetCommit
	i, err := p.holons.InspectWorkspace(ctx, id)
	if err != nil {
		return holons.Holon{}, err
	}
	if err := ValidatePreparedPublication(h, r, i, expectedTarget, explicitTarget); err != nil {
		return holons.Holon{}, err
	}
	operation, created, err := p.catalog.BeginOperation(ctx, Operation{RequestID: requestID, PullRequestID: pr.ID, HolonID: id, Kind: "publish", ExpectedHead: pr.HeadCommit, ExpectedInputs: CaptureMutationInputs(pr), Groups: []FieldGroup{TopologyGroup, LifecycleGroup}})
	if err != nil {
		return holons.Holon{}, err
	}
	if !created {
		if operation.Status == "succeeded" {
			return p.holons.Get(ctx, id)
		}
		return holons.Holon{}, ErrOperationInProgress
	}
	defer func() {
		if resultErr == nil {
			return // Publication and completion committed together.
		}
		outcome := "failed"
		var uncertain interface{ Uncertain() bool }
		if errors.As(resultErr, &uncertain) && uncertain.Uncertain() {
			outcome = "uncertain"
		}
		_, completionErr := p.catalog.CompleteOperation(context.WithoutCancel(ctx), operation.RequestID, outcome, resultErr.Error())
		resultErr = errors.Join(resultErr, completionErr)
	}()
	if err := p.catalog.RecordOperationStep(ctx, operation.RequestID, pr.ID, "publication", OperationStep{Status: "running", HeadCommit: i.HeadCommit}); err != nil {
		return holons.Holon{}, err
	}
	gitStarted := time.Now()
	gitFinished := false
	defer func() {
		if !gitFinished {
			slog.DebugContext(ctx, "Git publication completed", "operation_id", requestID, "duration", time.Since(gitStarted), "error", resultErr)
		}
	}()
	return p.holons.PublishWithCommitter(ctx, id, holons.Publish{Remote: publicationRemote(pr), UpstreamBranch: branch, ExpectedRemoteHead: expected, RequireSynchronized: true, ExpectedWorkspaceHead: i.HeadCommit}, func(ctx context.Context, published holons.Holon) error {
		gitFinished = true
		slog.DebugContext(ctx, "Git publication completed", "operation_id", requestID, "duration", time.Since(gitStarted))
		if published.UpstreamBranch != branch || published.UpstreamHeadCommit == "" || (published.UpstreamHeadCommit == pr.HeadCommit && !r.RecoveringPublication) {
			return ErrPublicationNoChanges
		}
		checkpointStarted := time.Now()
		err := p.committer.CommitHolonPublication(ctx, pr.ID, pr.HeadCommit, operation.RequestID, published)
		slog.DebugContext(ctx, "Publication checkpoint completed", "operation_id", requestID, "duration", time.Since(checkpointStarted), "error", err)
		return err
	})
}

func (p *HolonPublisher) linkedPullRequest(h holons.Holon) (PullRequest, bool) {
	return LinkedPublicationTarget(p.catalog, p.repositoryID, h)
}

// LinkedPublicationTarget gives readiness and publication the same active PR.
func LinkedPublicationTarget(catalog PublicationCatalog, repositoryID string, h holons.Holon) (PullRequest, bool) {
	if h.PullRequestID != "" {
		pr, ok := catalog.GetPullRequest(h.PullRequestID)
		return pr, ok && pr.RepositoryID == repositoryID
	}
	var inactive *PullRequest
	for _, pr := range catalog.ListPullRequests(repositoryID) {
		for _, id := range pr.LinkedHolonIDs {
			if id != h.ID {
				continue
			}
			if pr.Status.Active() {
				return pr, true
			}
			copy := pr
			inactive = &copy
		}
	}
	if inactive != nil {
		return *inactive, true
	}
	return PullRequest{}, false
}

func publicationRemote(pr PullRequest) string {
	if source := DecodeGitHubObservation(pr.SyncData).HeadRepositoryURL; source != "" {
		return source
	}
	return "origin"
}

// ExpectedPublicationTarget pins a manual attempt before remote preparation.
func ExpectedPublicationTarget(h holons.Holon, explicit, accepted string) string {
	if explicit != "" {
		return explicit
	}
	if h.SynchronizedTargetCommit != "" {
		return h.SynchronizedTargetCommit
	}
	if h.UpstreamHeadCommit != "" {
		return h.UpstreamHeadCommit
	}
	return accepted
}

// ValidatePreparedPublication checks local state without refreshing the target.
func ValidatePreparedPublication(h holons.Holon, r holons.PublicationReadiness, i holons.WorkspaceInspection, expected, explicit string) error {
	recovering := r.RecoveringPublication && r.WorkspaceHead == r.TargetCommit
	if r.TargetCommit != "" && ((explicit != "" && explicit != r.TargetCommit && !recovering) || (expected != r.TargetCommit && r.WorkspaceHead != r.TargetCommit)) {
		return ErrPublicationStale
	}
	if r.Reason == ErrPublicationInactive.Error() {
		return ErrPublicationInactive
	}
	if i.Branch != h.WorktreeBranch || i.HeadCommit != r.WorkspaceHead {
		return ErrPublicationStale
	}
	if i.Dirty {
		return ErrWorkspaceDirty
	}
	if r.Reason != "" || r.RebaseRequired {
		return fmt.Errorf("%w: %s", holons.ErrPublicationUnavailable, r.Reason)
	}
	if !r.PublicationAvailable {
		return ErrPublicationNoChanges
	}
	return nil
}
