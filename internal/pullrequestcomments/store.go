package pullrequestcomments

import "context"

type Repository interface {
	// Insert atomically saves a review import receipt when SourceReviewID is set.
	Insert(context.Context, Comment) error
	Get(context.Context, string) (Comment, error)
	Update(context.Context, Comment) error
	Delete(context.Context, string) error
	ListByPullRequest(context.Context, string) ([]Comment, error)
	UnresolvedCount(context.Context, string) (int, error)
	UnresolvedCounts(context.Context, []string) (map[string]int, error)
	HasChildReplies(context.Context, string) (bool, error)
	FindByExternalIdentity(context.Context, string, string) (Comment, error)
	UpsertExternal(context.Context, Comment) (Comment, error)
	FindBySourceWorkerID(context.Context, string) (Comment, error)
	// FindReviewImportReceipt returns the immutable comment snapshot saved by Insert,
	// independently of later comment updates or deletion.
	FindReviewImportReceipt(context.Context, string, int) (Comment, error)
}

// WorkerReferenceReader reports references owned by the pull request worker
// domain without exposing its persistence model to comments.
type WorkerReferenceReader interface {
	HasWorkerReference(context.Context, string) (bool, error)
}

type PullRequestTargetReader interface {
	GetCommentTarget(context.Context, string) (PullRequestTarget, error)
}

type ProviderAuthor struct {
	Provider   string
	ExternalID string
	Login      string
	Name       string
	AvatarURL  string
	ProfileURL string
}

type AuthorObserver interface {
	ObserveCommentAuthor(context.Context, string, ProviderAuthor) (string, error)
}
