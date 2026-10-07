// Package pullrequestwork owns durable review, comment-worker, and rebase work.
package pullrequestwork

import (
	"context"
	"errors"
	"time"
)

type Kind string

const (
	KindReview Kind = "review"
	KindWorker Kind = "worker"
	KindRebase Kind = "rebase"
)

type Mode string

const (
	ModeAuto     Mode = "auto"
	ModeAssisted Mode = "assisted"
	ModeContinue Mode = "continue"
)

type Status string

const (
	StatusCancelled  Status = "cancelled"
	StatusCancelling Status = "cancelling"
	StatusSkipped    Status = "skipped"
	StatusQueued     Status = "queued"
	StatusRunning    Status = "running"
	StatusWaiting    Status = "waiting_user"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
	StatusStale      Status = "stale"
)

// ReviewInput records the immutable Git range and its content fingerprints.
type ReviewInput struct {
	DiffBaseCommit      string `json:"diff_base_commit"`
	HeadCommit          string `json:"head_commit"`
	PatchFingerprint    string `json:"patch_fingerprint"`
	MessagesFingerprint string `json:"messages_fingerprint"`
}

type ReviewFreshness struct {
	Generated *ReviewInput `json:"generated"`
	Current   *ReviewInput `json:"current"`
	Outdated  bool         `json:"outdated"`
}

type ReviewChanges interface {
	Capture(context.Context, string, string) (*ReviewInput, error)
}

type PublicationOptions struct {
	RequestID    string
	TargetCommit string
}

type Publication struct {
	ExpectedHead string
	// CheckpointOnly identifies a verified prior push whose local completion is pending.
	CheckpointOnly bool
	HeadCommit     string
	OperationID    string
}

type Work struct {
	RequestID               string                    `json:"request_id,omitempty"`
	RequestedBaseCommit     string                    `json:"requested_base_commit,omitempty"`
	PublicationOperationID  string                    `json:"publication_operation_id,omitempty"`
	PendingCompletion       *PendingAddressCompletion `json:"pending_completion,omitempty"`
	PublicationTargetCommit string                    `json:"publication_target_commit,omitempty"`
	Provenance              *ReviewInput              `json:"provenance,omitempty"`
	Freshness               *ReviewFreshness          `json:"freshness,omitempty"`
	ID                      string                    `json:"id"`
	PullRequestID           string                    `json:"pull_request_id"`
	CommentID               string                    `json:"comment_id,omitempty"`
	SessionID               string                    `json:"session_id,omitempty"`
	Kind                    Kind                      `json:"kind"`
	Mode                    Mode                      `json:"mode,omitempty"`
	MechanicalOnly          bool                      `json:"mechanical_only,omitempty"`
	Prompt                  string                    `json:"prompt,omitempty"`
	Status                  Status                    `json:"status"`
	BaseBranch              string                    `json:"base_branch,omitempty"`
	BaseCommit              string                    `json:"base_commit,omitempty"`
	HeadBranch              string                    `json:"head_branch,omitempty"`
	HeadCommit              string                    `json:"head_commit,omitempty"`
	BaseHeadCommit          string                    `json:"base_head_commit,omitempty"`
	ResultHeadCommit        string                    `json:"result_head_commit,omitempty"`
	TargetBaseCommit        string                    `json:"target_base_commit,omitempty"`
	TargetDiffBaseCommit    string                    `json:"target_diff_base_commit,omitempty"`
	ReplyBody               string                    `json:"reply_body,omitempty"`
	Summary                 string                    `json:"summary,omitempty"`
	Error                   string                    `json:"error,omitempty"`
	BackupRef               string                    `json:"backup_ref,omitempty"`
	PublicationState        string                    `json:"publication_state,omitempty"`
	ArtifactImported        bool                      `json:"-"`
	CreatedAt               time.Time                 `json:"created_at"`
	StartedAt               *time.Time                `json:"started_at,omitempty"`
	CompletedAt             *time.Time                `json:"completed_at,omitempty"`
}

type PullRequest struct {
	RepositoryID, SyncProvider, SyncExternalID, Status                                 string
	LifecycleGeneration, TopologyGeneration                                            int64
	HeadRepositoryURL, BaseRepositoryURL                                               string
	ID, Title, Summary, BaseBranch, BaseCommit, DiffBaseCommit, HeadBranch, HeadCommit string
	Active                                                                             bool
}

// ContinueOptions configures an immediate, user-started PR follow-up.
// These choices are consumed at startup and are not persisted on queued work.
type ContinueOptions struct {
	Title       string `json:"title,omitempty"`
	AgentType   string `json:"agent_type,omitempty"`
	StartupMode string `json:"startup_mode,omitempty"`
}

type Start struct {
	RequestID          string
	Startup            *ContinueOptions
	PullRequestID      string
	Kind               Kind
	Mode               Mode
	CommentIDs         []string
	Prompt             string
	MechanicalOnly     bool
	ExpectedBaseCommit string
}
type Completion struct {
	PullRequestID    string          `json:"pull_request_id"`
	HeadCommit       string          `json:"head_commit"`
	ResultHeadCommit string          `json:"result_head_commit,omitempty"`
	ReplyBody        string          `json:"reply,omitempty"`
	Summary          string          `json:"summary,omitempty"`
	Comments         []ReviewComment `json:"comments,omitempty"`
}

type ReviewComment struct {
	Body  string `json:"body"`
	Scope string `json:"scope"`
	Path  string `json:"path,omitempty"`
	Side  string `json:"side,omitempty"`
	Line  *int   `json:"line,omitempty"`
}

type RebaseCompletionMetadata struct {
	TargetBaseCommit     string
	TargetDiffBaseCommit string
}

// CompletionCommit is the atomic durable transition for published work.
// Rebase is nil for work that only advances the pull request head.
type CompletionCommit struct {
	Work               Work
	ExpectedSourceHead string
	ResultingHead      string
	Rebase             *RebaseCompletionMetadata
}

var (
	ErrNotFound                   = errors.New("pull request work not found")
	ErrPullRequestNotFound        = errors.New("pull request not found")
	ErrPullRequestInactive        = errors.New("pull request inactive")
	ErrBusy                       = errors.New("pull request work already active")
	ErrInvalid                    = errors.New("invalid pull request work")
	ErrStaleHead                  = errors.New("pull request head changed")
	ErrRebaseReadinessUnavailable = errors.New("rebase readiness unavailable")
	ErrRebaseTargetChanged        = errors.New("rebase target changed")
	ErrRebaseConflicts            = errors.New("rebase requires conflict resolution")
	ErrNoNewCommit                = errors.New("pull request work did not produce a new commit")
	ErrPublish                    = errors.New("pull request work publication failed")
)

type Store interface {
	Create(context.Context, Work) error
	CreateBatch(context.Context, []Work) error
	Update(context.Context, Work) error
	Get(context.Context, string) (Work, error)
	List(context.Context, string) ([]Work, error)
}

// IsAddress identifies comment-addressing workers.
func (w Work) IsAddress() bool {
	return w.Kind == KindWorker && (w.Mode == ModeAuto || w.Mode == ModeAssisted)
}

// InQueue identifies work serialized in the durable per-PR FIFO.
func (w Work) InQueue() bool {
	return w.IsAddress() || w.Kind == KindRebase
}

func (w Work) Active() bool {
	return w.Status == StatusQueued || w.Status == StatusRunning || w.Status == StatusWaiting || w.Status == StatusCancelling
}

// IsAssistedReview excludes historical reviews, whose empty mode means automatic.
func (w Work) IsAssistedReview() bool { return w.Kind == KindReview && w.Mode == ModeAssisted }
