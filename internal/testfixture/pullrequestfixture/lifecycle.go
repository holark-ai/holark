package pullrequestfixture

import (
	"context"
	"errors"

	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

func (github *GitHub) ConvertToDraft(context.Context, pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubPullRequest, error) {
	github.mu.Lock()
	defer github.mu.Unlock()
	if github.pullRequest == nil {
		return pullrequestlifecycle.GitHubPullRequest{}, errors.New("fixture pull request has not been published")
	}
	github.draft = true
	github.pullRequest.Status = pullrequestlifecycle.StatusDraft
	return *github.pullRequest, nil
}
func (github *GitHub) MarkReadyForReview(context.Context, pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubPullRequest, error) {
	github.mu.Lock()
	defer github.mu.Unlock()
	if github.readyError != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, github.readyError
	}
	if github.pullRequest == nil {
		return pullrequestlifecycle.GitHubPullRequest{}, errors.New("fixture pull request has not been published")
	}
	github.draft = false
	github.pullRequest.Status = pullrequestlifecycle.StatusOpen
	return *github.pullRequest, nil
}

func (github *GitHub) ListActive(ctx context.Context, repositoryID string) ([]pullrequestlifecycle.GitHubPullRequest, error) {
	all, err := github.List(ctx, repositoryID)
	active := all[:0]
	for _, current := range all {
		if current.Status.Active() {
			active = append(active, current)
		}
	}
	return active, err
}
