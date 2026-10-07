// Package pullrequestparticipants owns pull request participant policy and workflows.
package pullrequestparticipants

import (
	"errors"
	"github.com/holark-ai/holark/internal/githubidentity"
)

type Snapshot struct {
	AuthorHolarkID             string   `json:"author_holark_id,omitempty"`
	PullRequestID              string   `json:"pull_request_id"`
	AssigneeHolarkIDs          []string `json:"assignee_holark_ids"`
	RequestedReviewerHolarkIDs []string `json:"requested_reviewer_holark_ids"`
}

type PullRequestStatus string

const (
	PullRequestWIP    PullRequestStatus = "wip"
	PullRequestDraft  PullRequestStatus = "draft"
	PullRequestOpen   PullRequestStatus = "open"
	PullRequestClosed PullRequestStatus = "closed"
	PullRequestMerged PullRequestStatus = "merged"
)

type PullRequestTarget struct {
	ID                  string
	RepositoryID        string
	Status              PullRequestStatus
	SyncProvider        string
	RepositoryURL       string
	ProviderPullRequest int
}

type ProviderTarget struct {
	RepositoryURL     string
	PullRequestNumber int
}

type RemoteParticipantSnapshot struct {
	Author                         *githubidentity.SourceMember
	Members                        []githubidentity.SourceMember
	Complete                       bool
	AssigneeGitHubNodeIDs          []string
	RequestedReviewerGitHubNodeIDs []string
}

var (
	ErrPullRequestNotFound      = errors.New("pull request not found")
	ErrInvalidRequest           = errors.New("invalid participant request")
	ErrPullRequestReadOnly      = errors.New("pull request participants are read-only")
	ErrProviderIdentityRequired = errors.New("provider identity is required")
	ErrUnsupportedProvider      = errors.New("unsupported participant provider")
	ErrProjectMemberUnresolved  = errors.New("project member identity could not be resolved")
	ErrProviderUnavailable      = errors.New("participant provider is unavailable")
	ErrProviderFailed           = errors.New("participant provider request failed")
	ErrReadFailed               = errors.New("participant snapshot read failed")
	ErrUpdateFailed             = errors.New("participant snapshot update failed")
)

type SyncStage string

const (
	SyncStageTargetRead         SyncStage = "target_read"
	SyncStageTargetValidation   SyncStage = "target_validation"
	SyncStageProviderFetch      SyncStage = "provider_fetch"
	SyncStageIdentityResolution SyncStage = "identity_resolution"
	SyncStageSnapshotReplace    SyncStage = "snapshot_replace"
)

type SyncError struct {
	PullRequestID string
	Stage         SyncStage
	Err           error
}

func (err *SyncError) Error() string {
	if err == nil || err.Err == nil {
		return "pull request participant sync failed"
	}
	return err.Err.Error()
}

func (err *SyncError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

func EmptySnapshot(id string) Snapshot {
	return Snapshot{PullRequestID: id, AssigneeHolarkIDs: []string{}, RequestedReviewerHolarkIDs: []string{}}
}
