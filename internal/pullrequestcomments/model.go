package pullrequestcomments

import (
	"errors"
	"time"
)

type Scope string

const (
	PublicationDraft PublicationState = "draft"
	ScopePullRequest Scope            = "pull_request"
	ScopeFile        Scope            = "file"
	ScopeLine        Scope            = "line"
)

type Status string

const (
	Unresolved Status = "unresolved"
	Resolved   Status = "resolved"
)

type AuthorType string

const (
	AuthorUser  AuthorType = "user"
	AuthorAgent AuthorType = "agent"
)

type PullRequestStatus string

const (
	PullRequestWIP    PullRequestStatus = "wip"
	PullRequestDraft  PullRequestStatus = "draft"
	PullRequestOpen   PullRequestStatus = "open"
	PullRequestClosed PullRequestStatus = "closed"
	PullRequestMerged PullRequestStatus = "merged"
)

type PublicationState string

const (
	PublicationLocal     PublicationState = "local"
	PublicationPending   PublicationState = "pending"
	PublicationPublished PublicationState = "published"
	PublicationFailed    PublicationState = "failed"
)

type ProviderIdentity struct {
	Provider   string
	ExternalID string
	Kind       string
	ReviewID   string
	ThreadID   string
}

type Comment struct {
	ID                 string     `json:"id"`
	PullRequestID      string     `json:"pull_request_id"`
	Body               string     `json:"body"`
	Scope              Scope      `json:"scope"`
	Path               string     `json:"path,omitempty"`
	OldPath            string     `json:"old_path,omitempty"`
	Side               string     `json:"side,omitempty"`
	Line               *int       `json:"line,omitempty"`
	DiffHunk           string     `json:"diff_hunk,omitempty"`
	OriginalHeadCommit string     `json:"original_head_commit"`
	Status             Status     `json:"status"`
	AuthorType         AuthorType `json:"author_type"`
	AuthorGitHubUserID string     `json:"author_github_user_id,omitempty"`
	SourceSessionID    string     `json:"source_session_id,omitempty"`
	SourceReviewID     string     `json:"source_review_id,omitempty"`
	SourceReviewIndex  int        `json:"-"`
	SourceWorkerID     string     `json:"source_worker_id,omitempty"`
	ParentCommentID    string     `json:"parent_comment_id,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	ResolvedAt         *time.Time `json:"resolved_at,omitempty"`

	ProviderIdentity ProviderIdentity `json:"-"`
	PublicationState PublicationState `json:"publication_state"`
}

type CreateComment struct {
	Draft           bool
	PullRequestID   string
	ParentCommentID string
	Body            string
	Scope           Scope
	Path            string
	OldPath         string
	Side            string
	Line            *int
	DiffHunk        string
	Origin          Origin
}

type ReviewComment struct {
	Body  string
	Scope Scope
	Path  string
	Side  string
	Line  *int
}

type CreateReviewComments struct {
	Draft         bool
	PullRequestID string
	Comments      []ReviewComment
	Origin        Origin
}

type Origin struct {
	authorType         AuthorType
	authorGitHubUserID string
	sourceSessionID    string
	sourceReviewID     string
	sourceWorkerID     string
	originalHeadCommit string
}

func UserOrigin() Origin { return Origin{authorType: AuthorUser} }

func GitHubUserOrigin(githubUserID string) Origin {
	return Origin{authorType: AuthorUser, authorGitHubUserID: githubUserID}
}

func ReviewOrigin(sessionID, reviewID, headCommit string) Origin {
	return Origin{authorType: AuthorAgent, sourceSessionID: sessionID, sourceReviewID: reviewID, originalHeadCommit: headCommit}
}

func WorkerOrigin(sessionID, workerID, publishedHeadCommit string) Origin {
	return Origin{authorType: AuthorAgent, sourceSessionID: sessionID, sourceWorkerID: workerID, originalHeadCommit: publishedHeadCommit}
}

type PullRequestTarget struct {
	ID                  string
	RepositoryID        string
	Status              PullRequestStatus
	HeadCommit          string
	ComparisonCurrent   bool
	SyncProvider        string
	RepositoryURL       string
	ProviderPullRequest int
}

var (
	ErrCommentNotFound     = errors.New("pull request comment not found")
	ErrPullRequestNotFound = errors.New("pull request not found")
	ErrInvalidComment      = errors.New("invalid pull request comment")
	ErrInvalidParent       = errors.New("invalid pull request comment parent")
	ErrReadOnly            = errors.New("this pull request is read-only")
	ErrStaleDraft          = errors.New("draft comments refer to an older revision; remove and recreate them on the latest changes before publishing")
	ErrComparisonNotReady  = errors.New("the PR comparison is updating; try again when it is ready")
)
