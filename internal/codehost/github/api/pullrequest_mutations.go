package api

import (
	"context"
	"errors"
	"fmt"
	"net/url"
)

// Mutations classify their outcome using the provider HTTP response.
func (client *CLIClient) CreatePullRequest(ctx context.Context, repository Repository, request CreatePullRequestRequest) (PullRequest, error) {
	var result PullRequest
	err := client.RequestMutation(ctx, "POST", fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(repository.Owner), url.PathEscape(repository.Name)), request, &result)
	return result, err
}
func (client *CLIClient) UpdatePullRequestState(ctx context.Context, repository Repository, number int, state string) (PullRequest, error) {
	if number <= 0 || state != "open" && state != "closed" {
		return PullRequest{}, &Error{Code: ErrorCodeSyncFailed, Err: errors.New("GitHub pull request number and valid state are required")}
	}
	var result PullRequest
	err := client.RequestMutation(ctx, "PATCH", fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(repository.Owner), url.PathEscape(repository.Name), number), updatePullRequestStateRequest{State: state}, &result)
	return result, err
}
func (client *CLIClient) UpdatePullRequest(ctx context.Context, repository Repository, number int, request UpdatePullRequestRequest) (PullRequest, error) {
	if number <= 0 {
		return PullRequest{}, &Error{Code: ErrorCodeSyncFailed, Err: errors.New("GitHub pull request number is required")}
	}
	var result PullRequest
	err := client.RequestMutation(ctx, "PATCH", fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(repository.Owner), url.PathEscape(repository.Name), number), request, &result)
	return result, err
}
func (client *CLIClient) MergePullRequest(ctx context.Context, repository Repository, number int, request MergePullRequestRequest) (MergePullRequestResponse, error) {
	if number <= 0 {
		return MergePullRequestResponse{}, &Error{Code: ErrorCodeSyncFailed, Err: errors.New("GitHub pull request number is required")}
	}
	var result struct {
		SHA     string `json:"sha"`
		Merged  *bool  `json:"merged"`
		Message string `json:"message"`
	}
	err := client.RequestMutation(ctx, "PUT", fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", url.PathEscape(repository.Owner), url.PathEscape(repository.Name), number), request, &result)
	if err != nil {
		return MergePullRequestResponse{}, err
	}
	if result.Merged == nil {
		return MergePullRequestResponse{}, &Error{Code: ErrorCodeMutationAccepted, Err: errors.New("GitHub merge returned no confirmed outcome")}
	}
	return MergePullRequestResponse{SHA: result.SHA, Merged: *result.Merged, Message: result.Message}, nil
}
