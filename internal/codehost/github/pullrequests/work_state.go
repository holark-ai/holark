package pullrequests

import (
	"context"
	"errors"
	"strings"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

func (provider *Provider) GetWorkState(ctx context.Context, target pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubPullRequest, error) {
	client, ok := provider.client.(interface {
		PullRequestWorkState(context.Context, githubapi.Repository, int) (githubapi.PullRequest, error)
	})
	if !ok {
		return pullrequestlifecycle.GitHubPullRequest{}, pullrequestlifecycle.ErrGitHubClientUnsupported
	}
	repository, number, err := provider.lifecycleTarget(target)
	if err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, err
	}
	remote, err := client.PullRequestWorkState(ctx, repository, number)
	if err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, lifecycleError(err)
	}
	if remote.Number != number || remote.NodeID == "" || provider.ExternalID(target) != remote.NodeID || remote.Base.Repository == nil || remote.Head.Repository == nil {
		return pullrequestlifecycle.GitHubPullRequest{}, lifecycleError(errors.New("GitHub work state identity mismatch"))
	}
	if !strings.EqualFold(remote.Base.Repository.FullName, repository.Owner+"/"+repository.Name) || remote.Base.Repository.CloneURL == "" || remote.Head.Repository.FullName == "" || remote.Head.Repository.CloneURL == "" {
		return pullrequestlifecycle.GitHubPullRequest{}, lifecycleError(errors.New("GitHub work state repository identity missing or mismatched"))
	}
	result, err := convert(repository, remote)
	if err == nil && !strings.EqualFold(result.ExternalID, target.ExternalID) {
		return pullrequestlifecycle.GitHubPullRequest{}, lifecycleError(errors.New("GitHub work state identity mismatch"))
	}
	return result, err
}
