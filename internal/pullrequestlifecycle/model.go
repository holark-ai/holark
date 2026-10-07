package pullrequestlifecycle

import (
	"encoding/json"
	"errors"
	"github.com/holark-ai/holark/internal/repository"
	"strings"
	"time"
)

func CanonicalBaseBranch(branch string) string {
	return strings.TrimPrefix(strings.TrimSpace(branch), "origin/")
}

type Status string

const (
	StatusWIP          Status = "wip"
	StatusDraft        Status = "draft"
	StatusOpen         Status = "open"
	StatusClosed       Status = "closed"
	StatusMerged       Status = "merged"
	SyncProviderGitHub        = "github"
)

func (s Status) Active() bool { return s == StatusWIP || s == StatusDraft || s == StatusOpen }
func (s Status) CanTransitionTo(t Status) bool {
	switch s {
	case StatusWIP:
		return t == StatusDraft || t == StatusOpen || t == StatusClosed
	case StatusDraft:
		return t == StatusWIP || t == StatusOpen || t == StatusClosed
	case StatusOpen:
		return t == StatusDraft || t == StatusClosed
	}
	return false
}
func (s Status) MergeableByLifecycle() bool { return s == StatusOpen }

type MetadataConfirmation struct {
	Title             string    `json:"title"`
	Summary           string    `json:"summary"`
	ProviderUpdatedAt time.Time `json:"provider_updated_at"`
}

type PullRequest struct {
	ActivityRetired        bool                      `json:"activity_retired,omitempty"`
	RelationshipGeneration int64                     `json:"relationship_generation"`
	BaseRef                repository.BranchIdentity `json:"base_ref"`
	HeadRef                repository.BranchIdentity `json:"head_ref"`
	Comparison             *ComparisonSnapshot       `json:"comparison,omitempty"`
	ComparisonState        ComparisonState           `json:"comparison_state"`
	MetadataConfirmation   *MetadataConfirmation     `json:"metadata_confirmation,omitempty"`
	VerifiedHead           bool                      `json:"-"`
	ViewRevision           int64                     `json:"view_revision"`
	LifecycleGeneration    int64                     `json:"lifecycle_generation"`
	TopologyGeneration     int64                     `json:"topology_generation"`
	MetadataGeneration     int64                     `json:"metadata_generation"`
	Operations             []ActiveOperation         `json:"operations,omitempty"`
	LifecycleObservation   int64                     `json:"lifecycle_observation,omitempty"`
	TopologyObservation    int64                     `json:"topology_observation,omitempty"`
	MetadataObservation    int64                     `json:"metadata_observation,omitempty"`
	ReadinessObservation   int64                     `json:"readiness_observation,omitempty"`
	LifecycleConfirmation  *Confirmation             `json:"lifecycle_confirmation,omitempty"`
	TopologyConfirmation   *Confirmation             `json:"topology_confirmation,omitempty"`

	ID                     string          `json:"id"`
	RepositoryID           string          `json:"-"`
	Title                  string          `json:"title"`
	Summary                string          `json:"summary"`
	BaseBranch             string          `json:"base_branch"`
	BaseCommit             string          `json:"base_commit"`
	HeadBranch             string          `json:"head_branch"`
	HeadCommit             string          `json:"head_commit"`
	DiffBaseCommit         string          `json:"diff_base_commit,omitempty"`
	Status                 Status          `json:"status"`
	SyncProvider           string          `json:"sync_provider,omitempty"`
	SyncExternalID         string          `json:"sync_external_id,omitempty"`
	SyncData               json.RawMessage `json:"sync_data"`
	LinkedHolonIDs         []string        `json:"linked_session_ids"`
	CreatedAt              time.Time       `json:"created_at"`
	UpdatedAt              time.Time       `json:"updated_at"`
	ClosedAt               *time.Time      `json:"closed_at,omitempty"`
	MergedAt               *time.Time      `json:"merged_at,omitempty"`
	MergedCommit           string          `json:"merged_commit,omitempty"`
	MergeStrategy          string          `json:"merge_strategy,omitempty"`
	MergeProvider          string          `json:"merge_provider,omitempty"`
	MergeRequestStrategy   string          `json:"merge_request_strategy,omitempty"`
	Mergeable              *bool           `json:"mergeable,omitempty"`
	MergeBlockedReason     string          `json:"merge_blocked_reason,omitempty"`
	UnresolvedCommentCount int             `json:"unresolved_comment_count,omitempty"`
	SyncedAt               *time.Time      `json:"synced_at,omitempty"`
}

type HolonLink struct {
	PullRequestID string `json:"pull_request_id"`
	HolonID       string `json:"session_id"`
}

var (
	ErrNotFound          = errors.New("pull request not found")
	ErrInvalidTransition = errors.New("invalid pull request transition")
	ErrClosed            = errors.New("pull request is closed")
)
