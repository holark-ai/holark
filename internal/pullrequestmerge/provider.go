package pullrequestmerge

import (
	"context"
	"encoding/json"
	"time"
)

type GitHubTarget struct {
	RepositoryURL string
	ExternalID    string
	SyncData      json.RawMessage
}

type ChecksState string

const (
	ChecksPassing ChecksState = "passing"
	ChecksPending ChecksState = "pending"
	ChecksFailing ChecksState = "failing"
	ChecksError   ChecksState = "error"
	ChecksUnknown ChecksState = "unknown"
)

type MergeabilityState string

const (
	MergeabilityMergeable   MergeabilityState = "mergeable"
	MergeabilityBlocked     MergeabilityState = "blocked"
	MergeabilityConflicting MergeabilityState = "conflicting"
	MergeabilityUnknown     MergeabilityState = "unknown"
)

type GitHubReadiness struct {
	HeadCommit        string
	ChecksState       ChecksState
	MergeabilityState MergeabilityState
	DetailsURL        string
	SyncedAt          time.Time
	Error             string
}

type GitHubMergeRequest struct {
	Target          GitHubTarget
	Title           string
	CommitMessage   string
	ExpectedHeadSHA string
}

type GitHubMergeResult struct {
	MergedCommit string
}

type GitHubErrorKind string

const (
	GitHubUnavailable GitHubErrorKind = "unavailable"
	GitHubHeadChanged GitHubErrorKind = "head_changed"
	GitHubBlocked     GitHubErrorKind = "blocked"
)

type GitHubError struct {
	Kind    GitHubErrorKind
	Message string
	Err     error
}

func (err *GitHubError) Error() string {
	if err == nil {
		return ""
	}
	if err.Message != "" {
		return err.Message
	}
	if err.Err != nil {
		return err.Err.Error()
	}
	return string(err.Kind)
}

func (err *GitHubError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

type GitHubProvider interface {
	Readiness(GitHubTarget) (GitHubReadiness, bool)
	Squash(context.Context, GitHubMergeRequest) (GitHubMergeResult, error)
}
