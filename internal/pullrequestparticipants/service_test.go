package pullrequestparticipants_test

import (
	"context"
	"errors"
	"testing"

	"github.com/holark-ai/holark/internal/pullrequestparticipants"
)

func TestDirectMutationEnforcesStatusBeforeProviderIdentityPolicy(t *testing.T) {
	valid := pullrequestparticipants.PullRequestTarget{
		ID: "pr-1", RepositoryID: "project-1", Status: pullrequestparticipants.PullRequestOpen,
		SyncProvider: "github", RepositoryURL: "https://github.com/acme/widgets", ProviderPullRequest: 1,
	}
	for _, test := range []struct {
		name   string
		change func(*pullrequestparticipants.PullRequestTarget)
		want   error
	}{
		{name: "wip before missing identity", change: func(target *pullrequestparticipants.PullRequestTarget) {
			target.Status, target.SyncProvider = pullrequestparticipants.PullRequestWIP, ""
		}, want: pullrequestparticipants.ErrPullRequestReadOnly},
		{name: "closed", change: func(target *pullrequestparticipants.PullRequestTarget) {
			target.Status = pullrequestparticipants.PullRequestClosed
		}, want: pullrequestparticipants.ErrPullRequestReadOnly},
		{name: "merged", change: func(target *pullrequestparticipants.PullRequestTarget) {
			target.Status = pullrequestparticipants.PullRequestMerged
		}, want: pullrequestparticipants.ErrPullRequestReadOnly},
		{name: "providerless draft", change: func(target *pullrequestparticipants.PullRequestTarget) {
			target.Status, target.SyncProvider = pullrequestparticipants.PullRequestDraft, ""
		}, want: pullrequestparticipants.ErrProviderIdentityRequired},
		{name: "unsupported provider", change: func(target *pullrequestparticipants.PullRequestTarget) {
			target.SyncProvider, target.RepositoryURL = "gitlab", ""
		}, want: pullrequestparticipants.ErrUnsupportedProvider},
		{name: "missing number", change: func(target *pullrequestparticipants.PullRequestTarget) { target.ProviderPullRequest = 0 }, want: pullrequestparticipants.ErrProviderIdentityRequired},
		{name: "unsupported repository", change: func(target *pullrequestparticipants.PullRequestTarget) {
			target.RepositoryURL = "https://example.com/acme/widgets"
		}, want: pullrequestparticipants.ErrUnsupportedProvider},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := valid
			test.change(&target)
			service := pullrequestparticipants.NewService(nil, fixedTarget{target: target}, nil, nil)
			_, err := service.ReplaceAssignees(t.Context(), target.ID, []string{"member-a"})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

type fixedTarget struct {
	target pullrequestparticipants.PullRequestTarget
}

func (reader fixedTarget) GetParticipantTarget(context.Context, string) (pullrequestparticipants.PullRequestTarget, error) {
	return reader.target, nil
}
