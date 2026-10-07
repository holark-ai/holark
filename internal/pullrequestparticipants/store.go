package pullrequestparticipants

import "context"

type Store interface {
	Get(context.Context, string) (Snapshot, error)
	GetMany(context.Context, []string) (map[string]Snapshot, error)
	ReplaceSnapshot(context.Context, Snapshot) (Snapshot, error)
	ReplaceAssignees(context.Context, string, []string) (Snapshot, error)
	AddAssignee(context.Context, string, string) (Snapshot, error)
	RemoveAssignee(context.Context, string, string) (Snapshot, error)
	ReplaceRequestedReviewers(context.Context, string, []string) (Snapshot, error)
	AddRequestedReviewer(context.Context, string, string) (Snapshot, error)
	RemoveRequestedReviewer(context.Context, string, string) (Snapshot, error)
}

type PullRequestTargetReader interface {
	GetParticipantTarget(context.Context, string) (PullRequestTarget, error)
}

type IdentityResolver interface {
	ResolveProjectMemberIDs(context.Context, string, []string) ([]string, error)
	ResolveProjectMemberLogins(context.Context, string, []string) ([]string, error)
}
