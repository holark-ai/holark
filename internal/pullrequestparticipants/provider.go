package pullrequestparticipants

import "context"

type Provider interface {
	GetSnapshot(context.Context, ProviderTarget) (RemoteParticipantSnapshot, error)
	ReplaceAssignees(context.Context, ProviderTarget, []string) error
	AddAssignee(context.Context, ProviderTarget, string) error
	RemoveAssignee(context.Context, ProviderTarget, string) error
	ReplaceRequestedReviewers(context.Context, ProviderTarget, []string) error
	AddRequestedReviewer(context.Context, ProviderTarget, string) error
	RemoveRequestedReviewer(context.Context, ProviderTarget, string) error
}
