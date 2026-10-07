package githubidentity

import (
	"context"
	"fmt"
	"strings"
)

type AuthenticatedSource interface {
	AuthenticatedUser(context.Context) (SourceMember, error)
}
type AuthenticatedStore interface {
	SetAuthenticatedMember(context.Context, string, string) error
}

func (s *Service) AuthenticatedProfile(ctx context.Context) (Profile, error) {
	source, ok := s.source.(AuthenticatedSource)
	if !ok {
		return Profile{}, fmt.Errorf("%w: authenticated identity source unavailable", ErrGitHubUnavailable)
	}
	member, err := source.AuthenticatedUser(ctx)
	if err != nil {
		return Profile{}, err
	}
	if strings.TrimSpace(member.NodeID) == "" || strings.TrimSpace(member.Login) == "" {
		return Profile{}, fmt.Errorf("%w: authenticated user requires node ID and login", ErrMalformedSnapshot)
	}
	return Profile{
		Login: strings.TrimSpace(member.Login), Name: strings.TrimSpace(member.Name),
		AvatarURL: strings.TrimSpace(member.AvatarURL), ProfileURL: strings.TrimSpace(member.ProfileURL),
	}, nil
}

// SyncAuthenticatedUser alone owns is_me, independently of collaborator access.
func (s *Service) SyncAuthenticatedUser(ctx context.Context, repo string) error {
	source, ok := s.source.(AuthenticatedSource)
	if !ok {
		return fmt.Errorf("authenticated identity source unavailable")
	}
	store, ok := s.store.(AuthenticatedStore)
	if !ok {
		return fmt.Errorf("authenticated identity store unavailable")
	}
	member, err := source.AuthenticatedUser(ctx)
	if err != nil {
		return err
	}
	id, err := s.ObserveProjectMember(ctx, repo, member)
	if err != nil {
		return err
	}
	return store.SetAuthenticatedMember(ctx, repo, id)
}
