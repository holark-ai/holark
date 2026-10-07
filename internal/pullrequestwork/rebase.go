package pullrequestwork

import (
	"context"
	"strings"
)

// RebasePreparation is the immutable snapshot used to create and finish
// durable rebase work.
type RebasePreparation struct {
	OperationID          string
	PullRequest          PullRequest
	HeadCommit           string
	TargetBaseCommit     string
	TargetDiffBaseCommit string
	Rebased              bool
	Conflicts            bool
}

type RebaseRepository interface {
	SyncPullRequest(context.Context, string) error
	PullRequest(string) (PullRequest, bool)
}

// RebasePreviewPreparer validates the accepted pair for read-only preview.
// Execution retains SyncPullRequest and its mutation checks.
type RebasePreviewPreparer interface {
	PrepareRebasePreview(context.Context, string) error
}

type PinnedRebaser interface {
	RebasePinned(context.Context, PullRequest, string, string, string) (RebasePreparation, error)
}

type RebasePreview struct {
	UpToDate         bool
	Conflicts        bool
	BaseCommitsAhead int
}

type PinnedRebasePreviewer interface {
	PreviewRebasePinned(context.Context, PullRequest, string, string) (RebasePreview, error)
}

type BranchFreshness string

const (
	BranchUpToDate    BranchFreshness = "up_to_date"
	BranchNotUpToDate BranchFreshness = "not_up_to_date"
)

type RebaseConflictState string

const (
	RebaseNotApplicable RebaseConflictState = "not_applicable"
	RebaseClean         RebaseConflictState = "clean"
	RebaseConflicting   RebaseConflictState = "conflicting"
)

type RebaseReadiness struct {
	BaseCommit          string              `json:"base_commit"`
	HeadCommit          string              `json:"head_commit"`
	BaseCommitsAhead    int                 `json:"base_commits_ahead"`
	BranchFreshness     BranchFreshness     `json:"branch_freshness"`
	RebaseConflictState RebaseConflictState `json:"rebase_conflict_state"`
}

// RebaseCoordinator selects an accepted cached pair before pinned execution.
type RebaseCoordinator struct {
	repository RebaseRepository
	direct     PinnedRebaser
	preview    PinnedRebasePreviewer
}

func NewRebaseCoordinator(repository RebaseRepository, direct PinnedRebaser, previews ...PinnedRebasePreviewer) *RebaseCoordinator {
	coordinator := &RebaseCoordinator{repository: repository, direct: direct}
	if len(previews) > 0 {
		coordinator.preview = previews[0]
	}
	return coordinator
}

func (c *RebaseCoordinator) RebaseReadiness(ctx context.Context, original PullRequest) (RebaseReadiness, error) {
	if c == nil || c.repository == nil || !original.Active {
		return RebaseReadiness{}, ErrPullRequestInactive
	}
	if c.preview == nil {
		return RebaseReadiness{}, ErrRebaseReadinessUnavailable
	}
	prepare := c.repository.SyncPullRequest
	if preparer, ok := c.repository.(RebasePreviewPreparer); ok {
		prepare = preparer.PrepareRebasePreview
	}
	if err := prepare(ctx, original.ID); err != nil {
		return RebaseReadiness{}, err
	}
	original, ok := c.repository.PullRequest(original.ID)
	if !ok || !original.Active || original.BaseCommit == "" || original.HeadCommit == "" {
		return RebaseReadiness{}, ErrStaleHead
	}
	baseCommit, headCommit := original.BaseCommit, original.HeadCommit
	preview, err := c.preview.PreviewRebasePinned(ctx, original, baseCommit, headCommit)
	if err != nil {
		return RebaseReadiness{}, err
	}
	readiness := RebaseReadiness{BaseCommit: baseCommit, HeadCommit: headCommit, BaseCommitsAhead: preview.BaseCommitsAhead, BranchFreshness: BranchNotUpToDate, RebaseConflictState: RebaseClean}
	if preview.UpToDate {
		readiness.BranchFreshness = BranchUpToDate
		readiness.RebaseConflictState = RebaseNotApplicable
	} else if preview.Conflicts {
		readiness.RebaseConflictState = RebaseConflicting
	}
	return readiness, nil
}

func (c *RebaseCoordinator) Rebase(ctx context.Context, original PullRequest, expectedBaseCommit, operationID string) (RebasePreparation, error) {
	if c == nil || c.repository == nil || !original.Active {
		return RebasePreparation{}, ErrStaleHead
	}
	if err := c.repository.SyncPullRequest(ctx, original.ID); err != nil {
		return RebasePreparation{}, err
	}
	reconciled, ok := c.repository.PullRequest(original.ID)
	if !ok || !reconciled.Active || reconciled.BaseCommit == "" || reconciled.HeadCommit == "" {
		return RebasePreparation{}, ErrStaleHead
	}
	baseCommit, headCommit := reconciled.BaseCommit, reconciled.HeadCommit
	if expectedBaseCommit = strings.TrimSpace(expectedBaseCommit); expectedBaseCommit != "" && baseCommit != expectedBaseCommit {
		return RebasePreparation{}, ErrRebaseTargetChanged
	}
	prepared := RebasePreparation{
		PullRequest: reconciled, HeadCommit: headCommit,
		TargetBaseCommit: baseCommit, TargetDiffBaseCommit: baseCommit,
	}
	if c.direct == nil {
		prepared.Conflicts = true
		return prepared, nil
	}
	result, err := c.direct.RebasePinned(ctx, reconciled, baseCommit, headCommit, operationID)
	if err != nil {
		return RebasePreparation{}, err
	}
	prepared.OperationID = result.OperationID
	prepared.HeadCommit = result.HeadCommit
	prepared.Rebased = result.Rebased
	prepared.Conflicts = result.Conflicts
	if prepared.HeadCommit == "" {
		prepared.HeadCommit = headCommit
	}
	return prepared, nil
}
