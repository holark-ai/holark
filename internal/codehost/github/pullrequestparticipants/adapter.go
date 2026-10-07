// Package pullrequestparticipants implements participant provider operations through GitHub.
package pullrequestparticipants

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"github.com/holark-ai/holark/internal/githubidentity"
	"github.com/holark-ai/holark/internal/pullrequestparticipants"
)

type Client interface {
	Request(context.Context, string, string, any, any) error
}

type Adapter struct{ client Client }

func New(client Client) *Adapter { return &Adapter{client: client} }

type user struct {
	NodeID    string `json:"node_id"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	HTMLURL   string `json:"html_url"`
}

type issue struct {
	Assignees []user `json:"assignees"`
}

type reviewRequests struct {
	Users []user `json:"users"`
	Teams []struct {
		Slug string `json:"slug"`
	} `json:"teams"`
}

type pullRequest struct {
	RequestedReviewers []user `json:"requested_reviewers"`
}

func (adapter *Adapter) GetSnapshot(ctx context.Context, target pullrequestparticipants.ProviderTarget) (pullrequestparticipants.RemoteParticipantSnapshot, error) {
	repository, err := repository(target)
	if err != nil {
		return pullrequestparticipants.RemoteParticipantSnapshot{}, err
	}
	var currentIssue issue
	if err := adapter.request(ctx, "GET", issueEndpoint(repository, target.PullRequestNumber), nil, &currentIssue); err != nil {
		return pullrequestparticipants.RemoteParticipantSnapshot{}, err
	}
	requests, err := adapter.getReviewRequests(ctx, repository, target.PullRequestNumber)
	if err != nil {
		return pullrequestparticipants.RemoteParticipantSnapshot{}, err
	}
	if currentIssue.Assignees == nil || requests.Users == nil {
		return pullrequestparticipants.RemoteParticipantSnapshot{}, pullrequestparticipants.ErrIncompleteObservation
	}
	snapshot := pullrequestparticipants.RemoteParticipantSnapshot{
		Complete:                       true,
		AssigneeGitHubNodeIDs:          make([]string, 0, len(currentIssue.Assignees)),
		RequestedReviewerGitHubNodeIDs: make([]string, 0, len(requests.Users)),
	}
	for _, assignee := range currentIssue.Assignees {
		snapshot.Members = append(snapshot.Members, githubidentity.SourceMember{
			NodeID: assignee.NodeID, Login: assignee.Login, AvatarURL: assignee.AvatarURL, ProfileURL: assignee.HTMLURL,
		})
		snapshot.AssigneeGitHubNodeIDs = append(snapshot.AssigneeGitHubNodeIDs, assignee.NodeID)
	}
	for _, reviewer := range requests.Users {
		snapshot.Members = append(snapshot.Members, githubidentity.SourceMember{
			NodeID: reviewer.NodeID, Login: reviewer.Login, AvatarURL: reviewer.AvatarURL, ProfileURL: reviewer.HTMLURL,
		})
		snapshot.RequestedReviewerGitHubNodeIDs = append(snapshot.RequestedReviewerGitHubNodeIDs, reviewer.NodeID)
	}
	return snapshot, nil
}

func (adapter *Adapter) ReplaceAssignees(ctx context.Context, target pullrequestparticipants.ProviderTarget, logins []string) error {
	repository, err := repository(target)
	if err != nil {
		return err
	}
	var response issue
	if err := adapter.request(ctx, "PATCH", issueEndpoint(repository, target.PullRequestNumber), map[string]any{"assignees": logins}, &response); err != nil {
		return err
	}
	if !sameSet(userLogins(response.Assignees), logins) {
		return fmt.Errorf("%w: GitHub did not accept every requested assignee", pullrequestparticipants.ErrProviderFailed)
	}
	return nil
}

func (adapter *Adapter) AddAssignee(ctx context.Context, target pullrequestparticipants.ProviderTarget, login string) error {
	repository, err := repository(target)
	if err != nil {
		return err
	}
	var response issue
	endpoint := issueEndpoint(repository, target.PullRequestNumber) + "/assignees"
	if err := adapter.request(ctx, "POST", endpoint, map[string]any{"assignees": []string{login}}, &response); err != nil {
		return err
	}
	if !contains(userLogins(response.Assignees), login) {
		return fmt.Errorf("%w: GitHub did not accept the requested assignee", pullrequestparticipants.ErrProviderFailed)
	}
	return nil
}

func (adapter *Adapter) RemoveAssignee(ctx context.Context, target pullrequestparticipants.ProviderTarget, login string) error {
	repository, err := repository(target)
	if err != nil {
		return err
	}
	var response issue
	endpoint := issueEndpoint(repository, target.PullRequestNumber) + "/assignees"
	if err := adapter.request(ctx, "DELETE", endpoint, map[string]any{"assignees": []string{login}}, &response); err != nil {
		return err
	}
	if contains(userLogins(response.Assignees), login) {
		return fmt.Errorf("%w: GitHub retained the removed assignee", pullrequestparticipants.ErrProviderFailed)
	}
	return nil
}

func (adapter *Adapter) ReplaceRequestedReviewers(ctx context.Context, target pullrequestparticipants.ProviderTarget, logins []string) error {
	repository, err := repository(target)
	if err != nil {
		return err
	}
	current, err := adapter.getReviewRequests(ctx, repository, target.PullRequestNumber)
	if err != nil {
		return err
	}
	currentLogins := userLogins(current.Users)
	remove := difference(currentLogins, logins)
	add := difference(logins, currentLogins)
	endpoint := reviewEndpoint(repository, target.PullRequestNumber)
	if len(add) > 0 {
		var response pullRequest
		if err := adapter.request(ctx, "POST", endpoint, map[string]any{"reviewers": add}, &response); err != nil {
			return err
		}
	}
	if len(remove) > 0 {
		if err := adapter.request(ctx, "DELETE", endpoint, map[string]any{"reviewers": remove}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (adapter *Adapter) AddRequestedReviewer(ctx context.Context, target pullrequestparticipants.ProviderTarget, login string) error {
	repository, err := repository(target)
	if err != nil {
		return err
	}
	current, err := adapter.getReviewRequests(ctx, repository, target.PullRequestNumber)
	if err != nil {
		return err
	}
	if contains(userLogins(current.Users), login) {
		return nil
	}
	var response pullRequest
	return adapter.request(ctx, "POST", reviewEndpoint(repository, target.PullRequestNumber), map[string]any{"reviewers": []string{login}}, &response)
}

func (adapter *Adapter) RemoveRequestedReviewer(ctx context.Context, target pullrequestparticipants.ProviderTarget, login string) error {
	repository, err := repository(target)
	if err != nil {
		return err
	}
	current, err := adapter.getReviewRequests(ctx, repository, target.PullRequestNumber)
	if err != nil {
		return err
	}
	if !contains(userLogins(current.Users), login) {
		return nil
	}
	return adapter.request(ctx, "DELETE", reviewEndpoint(repository, target.PullRequestNumber), map[string]any{"reviewers": []string{login}}, nil)
}

func (adapter *Adapter) getReviewRequests(ctx context.Context, repository githubapi.Repository, number int) (reviewRequests, error) {
	result := reviewRequests{Users: []user{}}
	for page := 1; ; page++ {
		var response reviewRequests
		endpoint := fmt.Sprintf("%s?per_page=100&page=%d", reviewEndpoint(repository, number), page)
		if err := adapter.request(ctx, "GET", endpoint, nil, &response); err != nil {
			return reviewRequests{}, err
		}
		if response.Users == nil {
			return reviewRequests{}, pullrequestparticipants.ErrIncompleteObservation
		}
		result.Users = append(result.Users, response.Users...)
		result.Teams = append(result.Teams, response.Teams...)
		if len(response.Users) < 100 && len(response.Teams) < 100 {
			return result, nil
		}
	}
}

func (adapter *Adapter) request(ctx context.Context, method, endpoint string, input, output any) error {
	if adapter == nil || adapter.client == nil {
		return pullrequestparticipants.ErrProviderUnavailable
	}
	if err := adapter.client.Request(ctx, method, endpoint, input, output); err != nil {
		var githubError *githubapi.Error
		if errors.As(err, &githubError) && githubError.Code == githubapi.ErrorCodeGHUnavailable {
			return fmt.Errorf("%w: %w", pullrequestparticipants.ErrProviderUnavailable, err)
		}
		return fmt.Errorf("%w: %w", pullrequestparticipants.ErrProviderFailed, err)
	}
	return nil
}

func repository(target pullrequestparticipants.ProviderTarget) (githubapi.Repository, error) {
	if target.PullRequestNumber <= 0 {
		return githubapi.Repository{}, pullrequestparticipants.ErrProviderIdentityRequired
	}
	repository, err := githubapi.ParseRepositoryURL(target.RepositoryURL)
	if err != nil {
		return githubapi.Repository{}, fmt.Errorf("%w: %v", pullrequestparticipants.ErrUnsupportedProvider, err)
	}
	return repository, nil
}

func issueEndpoint(repository githubapi.Repository, number int) string {
	return fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(repository.Owner), url.PathEscape(repository.Name), number)
}

func reviewEndpoint(repository githubapi.Repository, number int) string {
	return fmt.Sprintf("/repos/%s/%s/pulls/%d/requested_reviewers", url.PathEscape(repository.Owner), url.PathEscape(repository.Name), number)
}

func userLogins(users []user) []string {
	result := make([]string, 0, len(users))
	for _, user := range users {
		result = append(result, user.Login)
	}
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func difference(values, remove []string) []string {
	result := make([]string, 0)
	for _, value := range values {
		if !contains(remove, value) {
			result = append(result, value)
		}
	}
	return result
}

func sameSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for _, value := range left {
		if !contains(right, value) {
			return false
		}
	}
	return true
}

var _ pullrequestparticipants.Provider = (*Adapter)(nil)
