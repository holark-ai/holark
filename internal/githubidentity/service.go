package githubidentity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type Service struct {
	store  Store
	source MemberSource
	now    func() time.Time
	id     func() (string, error)
}

func NewService(store Store, source MemberSource) *Service {
	return &Service{store: store, source: source, now: func() time.Time { return time.Now().UTC() }, id: randomMemberID}
}

func (service *Service) ListProjectMembers(ctx context.Context, projectID string) ([]Member, error) {
	return service.store.ListProjectMembers(ctx, strings.TrimSpace(projectID))
}

func (service *Service) SearchProjectMembers(ctx context.Context, projectID, query string, limit int) ([]Member, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return service.store.SearchProjectMembers(ctx, strings.TrimSpace(projectID), strings.TrimSpace(query), limit)
}

func (service *Service) GetProjectMembers(ctx context.Context, projectID string, ids []string) (MemberResolution, error) {
	normalized := make([]string, len(ids))
	for index, id := range ids {
		normalized[index] = strings.TrimSpace(id)
	}
	return service.store.GetProjectMembers(ctx, strings.TrimSpace(projectID), normalized)
}

// ObserveProjectMember records an encountered GitHub identity without asserting
// collaborator permissions or whether it belongs to the authenticated user.
func (service *Service) ObserveProjectMember(ctx context.Context, projectID string, source SourceMember) (string, error) {
	projectID = strings.TrimSpace(projectID)
	source.NodeID = strings.TrimSpace(source.NodeID)
	source.Login = strings.TrimSpace(source.Login)
	if projectID == "" || source.NodeID == "" || source.Login == "" {
		return "", fmt.Errorf("%w: project ID, node ID, and login are required", ErrMalformedSnapshot)
	}
	id, err := service.id()
	if err != nil {
		return "", err
	}
	now := service.now().UTC()
	return service.store.ObserveProjectMember(ctx, projectID, StoredMember{
		Member: Member{ID: id, Login: source.Login, AvatarURL: strings.TrimSpace(source.AvatarURL), ProfileURL: strings.TrimSpace(source.ProfileURL)},
		NodeID: source.NodeID, LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	})
}

// ResolveProjectMemberIDs translates GitHub node IDs into Holark member IDs.
// Resolution is positional and all-or-nothing.
func (service *Service) ResolveProjectMemberIDs(ctx context.Context, projectID string, githubNodeIDs []string) ([]string, error) {
	return service.store.ResolveProjectMemberIDs(ctx, strings.TrimSpace(projectID), githubNodeIDs)
}

// ResolveProjectMemberLogins translates Holark member IDs into GitHub logins.
// Resolution is positional and all-or-nothing.
func (service *Service) ResolveProjectMemberLogins(ctx context.Context, projectID string, holarkIDs []string) ([]string, error) {
	return service.store.ResolveProjectMemberLogins(ctx, strings.TrimSpace(projectID), holarkIDs)
}

func (service *Service) SyncProjectMembers(ctx context.Context, projectID, repositoryURL string) ([]Member, error) {
	snapshot, err := service.source.ProjectMembers(ctx, repositoryURL)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(snapshot.Members))
	now := service.now().UTC()
	members := make([]StoredMember, 0, len(snapshot.Members))
	for index, source := range snapshot.Members {
		source.NodeID = strings.TrimSpace(source.NodeID)
		source.Login = strings.TrimSpace(source.Login)
		source.AvatarURL = strings.TrimSpace(source.AvatarURL)
		source.ProfileURL = strings.TrimSpace(source.ProfileURL)
		source.Permission = strings.TrimSpace(source.Permission)
		if source.NodeID == "" || source.Login == "" || source.Permission == "" {
			return nil, fmt.Errorf("%w: collaborator %d requires node ID, login, and permission", ErrMalformedSnapshot, index)
		}
		if _, duplicate := seen[source.NodeID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate collaborator node ID %q", ErrMalformedSnapshot, source.NodeID)
		}
		seen[source.NodeID] = struct{}{}
		id, err := service.id()
		if err != nil {
			return nil, err
		}
		members = append(members, StoredMember{
			Member: Member{ID: id, Login: source.Login, AvatarURL: source.AvatarURL, ProfileURL: source.ProfileURL, Permission: source.Permission},
			NodeID: source.NodeID, LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
		})
	}
	return service.store.SyncProjectMembers(ctx, strings.TrimSpace(projectID), members, now)
}

func randomMemberID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "github-member-" + hex.EncodeToString(value), nil
}
