package githubidentity

import (
	"context"
	"time"
)

type Store interface {
	ListProjectMembers(context.Context, string) ([]Member, error)
	SearchProjectMembers(context.Context, string, string, int) ([]Member, error)
	GetProjectMembers(context.Context, string, []string) (MemberResolution, error)
	ObserveProjectMember(context.Context, string, StoredMember) (string, error)
	SyncProjectMembers(context.Context, string, []StoredMember, time.Time) ([]Member, error)
	ResolveProjectMemberIDs(context.Context, string, []string) ([]string, error)
	ResolveProjectMemberLogins(context.Context, string, []string) ([]string, error)
}

type MemberSource interface {
	ProjectMembers(context.Context, string) (MemberSnapshot, error)
}
