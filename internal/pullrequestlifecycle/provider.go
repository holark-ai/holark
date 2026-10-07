package pullrequestlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/holark-ai/holark/internal/pullrequestparticipants"
	"time"
)

var ErrGitHubClientUnsupported = errors.New("configured GitHub pull request provider does not support the required operation")

type GitHubErrorKind string

const (
	GitHubUnsupportedRepository GitHubErrorKind = "unsupported_repository"
	GitHubUnavailable           GitHubErrorKind = "gh_unavailable"
	GitHubSyncFailure           GitHubErrorKind = "github_sync_failed"
)

type GitHubError struct {
	UncertainOutcome  bool
	RateLimitExceeded bool
	RetryAfterDelay   time.Duration
	Kind              GitHubErrorKind
	Err               error
}

func (err *GitHubError) Error() string {
	if err == nil || err.Err == nil {
		return string(err.Kind)
	}
	return err.Err.Error()
}

func (err *GitHubError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

type GitHubPullRequestTarget struct {
	RepositoryURL string
	ExternalID    string
	SyncData      json.RawMessage
}

type GitHubCreateRequest struct {
	RepositoryURL string
	Title         string
	Body          string
	Head          string
	Base          string
	Draft         bool
}

type GitHubPullRequest struct {
	Participants      pullrequestparticipants.RemoteParticipantSnapshot
	HeadRepositoryURL string
	BaseRepositoryURL string
	Title             string
	Summary           string
	BaseBranch        string
	BaseCommit        string
	HeadBranch        string
	HeadCommit        string
	Status            Status
	ExternalID        string
	SyncData          json.RawMessage
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ClosedAt          *time.Time
	MergedAt          *time.Time
	MergedCommit      string
	MergeStrategy     string
}

type GitHubChecksState string

const (
	GitHubChecksPassing GitHubChecksState = "passing"
	GitHubChecksPending GitHubChecksState = "pending"
	GitHubChecksFailing GitHubChecksState = "failing"
	GitHubChecksError   GitHubChecksState = "error"
	GitHubChecksUnknown GitHubChecksState = "unknown"
)

type GitHubMergeabilityState string

const (
	GitHubMergeabilityMergeable   GitHubMergeabilityState = "mergeable"
	GitHubMergeabilityBlocked     GitHubMergeabilityState = "blocked"
	GitHubMergeabilityConflicting GitHubMergeabilityState = "conflicting"
	GitHubMergeabilityUnknown     GitHubMergeabilityState = "unknown"
)

type GitHubReadiness struct {
	HeadCommit        string                  `json:"head_commit"`
	ChecksState       GitHubChecksState       `json:"checks_state"`
	MergeabilityState GitHubMergeabilityState `json:"mergeability_state"`
	DetailsURL        string                  `json:"details_url,omitempty"`
	SyncedAt          time.Time               `json:"synced_at"`
	Error             string                  `json:"error,omitempty"`
}

type GitHubTransport interface {
	ListActive(context.Context, string) ([]GitHubPullRequest, error)
	Create(context.Context, GitHubCreateRequest) (GitHubPullRequest, error)
	List(context.Context, string) ([]GitHubPullRequest, error)
	Get(context.Context, GitHubPullRequestTarget) (GitHubPullRequest, error)
	UpdateState(context.Context, GitHubPullRequestTarget, string) (GitHubPullRequest, error)
	ConvertToDraft(context.Context, GitHubPullRequestTarget) (GitHubPullRequest, error)
	MarkReadyForReview(context.Context, GitHubPullRequestTarget) (GitHubPullRequest, error)
	RefreshReadiness(context.Context, GitHubPullRequestTarget) (GitHubReadiness, error)
}

// GitHubWorkStateReader observes current branch targets and PR lifecycle in one
// request. It is optional so other transports retain full synchronization.
type GitHubWorkStateReader interface {
	GetWorkState(context.Context, GitHubPullRequestTarget) (GitHubPullRequest, error)
}

type GitHubRepositoryClassifier interface {
	SupportsRepository(string) bool
}

type GitHubLifecycleFields struct {
	Draft *bool
	State *string
}

type GitHubSyncDataCodec interface {
	Number(GitHubPullRequestTarget) int
	ExternalID(GitHubPullRequestTarget) string
	UpdateLifecycleFields(json.RawMessage, GitHubLifecycleFields) (json.RawMessage, error)
	DecodeReadiness(json.RawMessage) (GitHubReadiness, bool)
	StoreReadiness(json.RawMessage, GitHubReadiness) (json.RawMessage, error)
	DetailsURL(json.RawMessage) string
}

// Uncertain reports whether a write may have reached the provider despite the error.
func (err *GitHubError) Uncertain() bool { return err != nil && err.UncertainOutcome }

// RateLimited reports whether GitHub supplied a quota retry deadline.
func (err *GitHubError) RateLimited() bool { return err != nil && err.RateLimitExceeded }

func (err *GitHubError) RetryAfter() time.Duration {
	if err == nil {
		return 0
	}
	return err.RetryAfterDelay
}

// GitHubObservation is the provider evidence shared by lifecycle, recovery and
// storage reconciliation. It decodes independently of transport implementation.
type GitHubObservation struct {
	HeadRepositoryURL string    `json:"head_repository_url"`
	BaseRepositoryURL string    `json:"base_repository_url"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func DecodeGitHubObservation(raw json.RawMessage) GitHubObservation {
	var envelope struct {
		GitHub GitHubObservation `json:"github"`
	}
	_ = json.Unmarshal(raw, &envelope)
	return envelope.GitHub
}
