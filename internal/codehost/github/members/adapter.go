// Package members obtains a complete project-member snapshot from GitHub.
package members

import (
	"context"
	"errors"
	"fmt"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"github.com/holark-ai/holark/internal/githubidentity"
)

type Client interface {
	CurrentUser(context.Context) (githubapi.AuthenticatedUser, error)
	ListCollaborators(context.Context, githubapi.Repository) ([]githubapi.Collaborator, error)
}

type Source struct{ client Client }

func New(client Client) *Source {
	if client == nil {
		client = githubapi.NewCLIClient()
	}
	return &Source{client: client}
}

func (source *Source) ProjectMembers(ctx context.Context, repositoryURL string) (githubidentity.MemberSnapshot, error) {
	repository, err := githubapi.ParseRepositoryURL(repositoryURL)
	if errors.Is(err, githubapi.ErrUnsupportedRepository) {
		return githubidentity.MemberSnapshot{}, fmt.Errorf("%w: %s", githubidentity.ErrUnsupportedRepository, repositoryURL)
	}
	if err != nil {
		return githubidentity.MemberSnapshot{}, classify(err)
	}
	collaborators, err := source.client.ListCollaborators(ctx, repository)
	if err != nil {
		return githubidentity.MemberSnapshot{}, classify(err)
	}
	result := githubidentity.MemberSnapshot{Members: make([]githubidentity.SourceMember, 0, len(collaborators))}
	for _, collaborator := range collaborators {
		result.Members = append(result.Members, githubidentity.SourceMember{
			NodeID: collaborator.NodeID, Login: collaborator.Login, AvatarURL: collaborator.AvatarURL,
			ProfileURL: collaborator.HTMLURL, Permission: collaborator.Permission,
		})
	}
	return result, nil
}

func classify(err error) error {
	var githubErr *githubapi.Error
	if errors.As(err, &githubErr) && githubErr.Code == githubapi.ErrorCodeGHUnavailable {
		return fmt.Errorf("%w: %w", githubidentity.ErrGitHubUnavailable, err)
	}
	return fmt.Errorf("%w: %w", githubidentity.ErrGitHubFailed, err)
}

var _ githubidentity.MemberSource = (*Source)(nil)

func (s *Source) AuthenticatedUser(ctx context.Context) (githubidentity.SourceMember, error) {
	current, err := s.client.CurrentUser(ctx)
	if err != nil {
		return githubidentity.SourceMember{}, classify(err)
	}
	return githubidentity.SourceMember{NodeID: current.NodeID, Login: current.Login, Name: current.Name, AvatarURL: current.AvatarURL, ProfileURL: current.HTMLURL}, nil
}
