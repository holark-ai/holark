package localapp

import (
	"context"

	"github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	"github.com/holark-ai/holark/internal/githubidentity"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestparticipants"
	"github.com/holark-ai/holark/internal/pullrequestreviews"
)

type localCommentAuthors struct {
	members *githubidentity.Service
}

func (authors localCommentAuthors) CurrentCommentAuthor(ctx context.Context, repositoryID string) (string, error) {
	members, err := authors.members.ListProjectMembers(ctx, repositoryID)
	if err != nil {
		return "", err
	}
	for _, member := range members {
		if member.IsMe {
			return member.ID, nil
		}
	}
	return "", nil
}

func (authors localCommentAuthors) ObserveCommentAuthor(ctx context.Context, repositoryID string, author pullrequestcomments.ProviderAuthor) (string, error) {
	return authors.members.ObserveProjectMember(ctx, repositoryID, githubidentity.SourceMember{
		NodeID: author.ExternalID, Login: author.Login, AvatarURL: author.AvatarURL, ProfileURL: author.ProfileURL,
	})
}

type localPullRequestTargets struct {
	catalog interface {
		GetPullRequest(string) (pullrequestlifecycle.PullRequest, bool)
	}
	provider      *pullrequests.Provider
	repositoryURL string
}

func (t localPullRequestTargets) target(id string) (pullrequestlifecycle.PullRequest, int, bool) {
	pr, ok := t.catalog.GetPullRequest(id)
	if !ok {
		return pr, 0, false
	}
	number := t.provider.Number(pullrequestlifecycle.GitHubPullRequestTarget{RepositoryURL: t.repositoryURL, ExternalID: pr.SyncExternalID, SyncData: pr.SyncData})
	return pr, number, true
}
func (t localPullRequestTargets) GetCommentTarget(_ context.Context, id string) (pullrequestcomments.PullRequestTarget, error) {
	pr, n, ok := t.target(id)
	if !ok {
		return pullrequestcomments.PullRequestTarget{}, pullrequestcomments.ErrPullRequestNotFound
	}
	return pullrequestcomments.PullRequestTarget{ID: pr.ID, RepositoryID: pr.RepositoryID, Status: pullrequestcomments.PullRequestStatus(pr.Status), HeadCommit: pr.HeadCommit, ComparisonCurrent: pr.HasCurrentComparison(), SyncProvider: pr.SyncProvider, RepositoryURL: t.repositoryURL, ProviderPullRequest: n}, nil
}
func (t localPullRequestTargets) GetParticipantTarget(_ context.Context, id string) (pullrequestparticipants.PullRequestTarget, error) {
	pr, n, ok := t.target(id)
	if !ok {
		return pullrequestparticipants.PullRequestTarget{}, pullrequestparticipants.ErrPullRequestNotFound
	}
	return pullrequestparticipants.PullRequestTarget{ID: pr.ID, RepositoryID: pr.RepositoryID, Status: pullrequestparticipants.PullRequestStatus(pr.Status), SyncProvider: pr.SyncProvider, RepositoryURL: t.repositoryURL, ProviderPullRequest: n}, nil
}

func (t localPullRequestTargets) GetReviewTarget(_ context.Context, id string) (pullrequestreviews.Target, error) {
	pr, n, ok := t.target(id)
	if !ok {
		return pullrequestreviews.Target{}, pullrequestreviews.ErrPullRequestNotFound
	}
	return pullrequestreviews.Target{ID: pr.ID, Status: string(pr.Status), HeadCommit: pr.HeadCommit,
		ComparisonCurrent: pr.HasCurrentComparison(), Provider: pr.SyncProvider, RepositoryURL: t.repositoryURL, Number: n}, nil
}
