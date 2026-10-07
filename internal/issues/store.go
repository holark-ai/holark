package issues

import "context"

type Store interface {
	Get(ctx context.Context, id string) (Issue, error)
	List(ctx context.Context, projectID string) ([]Issue, error)
	Insert(ctx context.Context, issue Issue) error
	Update(ctx context.Context, issue Issue) error
	UpsertSynced(ctx context.Context, issue Issue) (inserted bool, err error)
	UpsertLabelCatalog(ctx context.Context, projectID string, labels []Label) ([]Label, error)
	DeleteSyncedAbsent(ctx context.Context, projectID, syncProvider string, presentExternalIDs []string) (int, error)
	LinkPullRequest(ctx context.Context, issueID, pullRequestID string) error
}
