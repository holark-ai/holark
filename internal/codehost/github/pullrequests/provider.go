// Package pullrequests implements GitHub pull-request transport and codecs.
package pullrequests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"github.com/holark-ai/holark/internal/githubidentity"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestmerge"
	"github.com/holark-ai/holark/internal/pullrequestparticipants"
)

type APIClient interface {
	ListActivePullRequests(context.Context, githubapi.Repository) ([]githubapi.PullRequest, error)
	CreatePullRequest(context.Context, githubapi.Repository, githubapi.CreatePullRequestRequest) (githubapi.PullRequest, error)
	ListPullRequests(context.Context, githubapi.Repository) ([]githubapi.PullRequest, error)
	PullRequest(context.Context, githubapi.Repository, int) (githubapi.PullRequest, error)
	UpdatePullRequestState(context.Context, githubapi.Repository, int, string) (githubapi.PullRequest, error)
	ConvertPullRequestToDraft(context.Context, string) (githubapi.PullRequest, error)
	MarkPullRequestReadyForReview(context.Context, string) (githubapi.PullRequest, error)
	PullRequestReadiness(context.Context, githubapi.Repository, int) (githubapi.PullRequestReadiness, error)
	MergePullRequest(context.Context, githubapi.Repository, int, githubapi.MergePullRequestRequest) (githubapi.MergePullRequestResponse, error)
}

type Provider struct{ client APIClient }

var (
	_ pullrequestlifecycle.GitHubRepositoryClassifier = (*Provider)(nil)
	_ pullrequestlifecycle.GitHubTransport            = (*Provider)(nil)
	_ pullrequestlifecycle.GitHubSyncDataCodec        = (*Provider)(nil)
	_ pullrequestmerge.GitHubProvider                 = (*Provider)(nil)
)

func New(client APIClient) *Provider { return &Provider{client: client} }

type syncDataEnvelope struct {
	GitHub githubSyncData `json:"github"`
}

type githubSyncData struct {
	HeadRepositoryURL string                                `json:"head_repository_url,omitempty"`
	BaseRepositoryURL string                                `json:"base_repository_url,omitempty"`
	Owner             string                                `json:"owner"`
	Repo              string                                `json:"repo"`
	Number            int                                   `json:"number"`
	URL               string                                `json:"url"`
	NodeID            string                                `json:"node_id,omitempty"`
	Draft             bool                                  `json:"draft"`
	Mergeable         *bool                                 `json:"mergeable,omitempty"`
	State             string                                `json:"state"`
	Merged            bool                                  `json:"merged"`
	MergedAt          *time.Time                            `json:"merged_at,omitempty"`
	MergeCommitSHA    string                                `json:"merge_commit_sha,omitempty"`
	Readiness         *pullrequestlifecycle.GitHubReadiness `json:"readiness,omitempty"`
}

func (provider *Provider) SupportsRepository(raw string) bool {
	_, err := githubapi.ParseRepositoryURL(raw)
	return err == nil
}

func (provider *Provider) Create(ctx context.Context, request pullrequestlifecycle.GitHubCreateRequest) (pullrequestlifecycle.GitHubPullRequest, error) {
	repository, err := parseRepository(request.RepositoryURL)
	if err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, err
	}
	payload := githubapi.CreatePullRequestRequest{Title: request.Title, Body: request.Body, Head: request.Head, Base: request.Base, Draft: request.Draft}
	created, err := provider.client.CreatePullRequest(ctx, repository, payload)
	if err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, lifecycleError(err)
	}
	return convertMutation(repository, created)
}

func (provider *Provider) List(ctx context.Context, repositoryURL string) ([]pullrequestlifecycle.GitHubPullRequest, error) {
	repository, err := parseRepository(repositoryURL)
	if err != nil {
		return nil, err
	}
	remote, err := provider.client.ListPullRequests(ctx, repository)
	if err != nil {
		return nil, lifecycleError(err)
	}
	result := make([]pullrequestlifecycle.GitHubPullRequest, 0, len(remote))
	for _, pullRequest := range remote {
		converted, err := convert(repository, pullRequest)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return result, nil
}

func (provider *Provider) Get(ctx context.Context, target pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubPullRequest, error) {
	repository, number, err := provider.lifecycleTarget(target)
	if err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, err
	}
	remote, err := provider.client.PullRequest(ctx, repository, number)
	if err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, lifecycleError(err)
	}
	return convert(repository, remote)
}

func (provider *Provider) UpdateState(ctx context.Context, target pullrequestlifecycle.GitHubPullRequestTarget, state string) (pullrequestlifecycle.GitHubPullRequest, error) {
	repository, number, err := provider.lifecycleTarget(target)
	if err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, err
	}
	remote, err := provider.client.UpdatePullRequestState(ctx, repository, number, state)
	if err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, lifecycleError(err)
	}
	converted, err := convertMutation(repository, remote)
	if err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, err
	}
	return converted, nil
}

func (provider *Provider) RefreshReadiness(ctx context.Context, target pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubReadiness, error) {
	repository, number, err := provider.lifecycleTarget(target)
	if err != nil {
		return pullrequestlifecycle.GitHubReadiness{}, err
	}
	readiness, err := provider.client.PullRequestReadiness(ctx, repository, number)
	converted := pullrequestlifecycle.GitHubReadiness{
		HeadCommit: readiness.HeadCommit, ChecksState: pullrequestlifecycle.GitHubChecksState(readiness.ChecksState),
		MergeabilityState: pullrequestlifecycle.GitHubMergeabilityState(readiness.MergeabilityState), DetailsURL: readiness.DetailsURL,
	}
	if err != nil {
		return converted, lifecycleError(err)
	}
	return converted, nil
}

func (provider *Provider) Number(target pullrequestlifecycle.GitHubPullRequestTarget) int {
	return number(target.SyncData, target.ExternalID)
}

func (provider *Provider) ExternalID(target pullrequestlifecycle.GitHubPullRequestTarget) string {
	var data syncDataEnvelope
	if json.Unmarshal(target.SyncData, &data) != nil {
		return ""
	}
	return strings.TrimSpace(data.GitHub.NodeID)
}

func (provider *Provider) UpdateLifecycleFields(raw json.RawMessage, fields pullrequestlifecycle.GitHubLifecycleFields) (json.RawMessage, error) {
	return patchGitHub(raw, func(data map[string]json.RawMessage) error {
		values := make(map[string]any)
		if fields.Draft != nil {
			values["draft"] = *fields.Draft
		}
		if fields.State != nil {
			values["state"] = *fields.State
		}
		for key, value := range values {
			encoded, err := json.Marshal(value)
			if err != nil {
				return err
			}
			data[key] = encoded
		}
		return nil
	})
}

func (provider *Provider) DecodeReadiness(raw json.RawMessage) (pullrequestlifecycle.GitHubReadiness, bool) {
	var data syncDataEnvelope
	if json.Unmarshal(raw, &data) != nil || data.GitHub.Readiness == nil {
		return pullrequestlifecycle.GitHubReadiness{}, false
	}
	return *data.GitHub.Readiness, true
}

func (provider *Provider) StoreReadiness(raw json.RawMessage, readiness pullrequestlifecycle.GitHubReadiness) (json.RawMessage, error) {
	return patchGitHub(raw, func(github map[string]json.RawMessage) error {
		encoded, err := json.Marshal(readiness)
		if err != nil {
			return err
		}
		github["readiness"] = encoded
		return nil
	})
}

func (provider *Provider) DetailsURL(raw json.RawMessage) string {
	var data syncDataEnvelope
	if json.Unmarshal(raw, &data) != nil {
		return ""
	}
	return strings.TrimSpace(data.GitHub.URL)
}

func (provider *Provider) Readiness(target pullrequestmerge.GitHubTarget) (pullrequestmerge.GitHubReadiness, bool) {
	readiness, ok := provider.DecodeReadiness(target.SyncData)
	if !ok {
		return pullrequestmerge.GitHubReadiness{}, false
	}
	return pullrequestmerge.GitHubReadiness{
		HeadCommit: readiness.HeadCommit, ChecksState: pullrequestmerge.ChecksState(readiness.ChecksState),
		MergeabilityState: pullrequestmerge.MergeabilityState(readiness.MergeabilityState), DetailsURL: readiness.DetailsURL,
		SyncedAt: readiness.SyncedAt, Error: readiness.Error,
	}, true
}

func (provider *Provider) Squash(ctx context.Context, request pullrequestmerge.GitHubMergeRequest) (pullrequestmerge.GitHubMergeResult, error) {
	repository, number, err := provider.mergeTarget(request.Target)
	if err != nil {
		return pullrequestmerge.GitHubMergeResult{}, err
	}
	payload := githubapi.MergePullRequestRequest{CommitTitle: suffixTitle(request.Title, number), CommitMessage: request.CommitMessage, MergeMethod: "squash", ExpectedHeadSHA: request.ExpectedHeadSHA}
	response, err := provider.client.MergePullRequest(ctx, repository, number, payload)
	if err != nil {
		return pullrequestmerge.GitHubMergeResult{}, mergeError(err)
	}
	if !response.Merged {
		message := strings.TrimSpace(response.Message)
		if message == "" {
			message = "GitHub did not merge the pull request."
		}
		return pullrequestmerge.GitHubMergeResult{}, &pullrequestmerge.GitHubError{Kind: pullrequestmerge.GitHubBlocked, Message: message}
	}
	mergedCommit := strings.TrimSpace(response.SHA)
	if mergedCommit == "" {
		return pullrequestmerge.GitHubMergeResult{}, &pullrequestmerge.GitHubError{Kind: pullrequestmerge.GitHubBlocked, Message: "GitHub did not return the merged commit.", Err: &pullrequestlifecycle.GitHubError{Kind: pullrequestlifecycle.GitHubSyncFailure, UncertainOutcome: true, Err: errors.New("GitHub accepted the merge but omitted its commit")}}
	}
	return pullrequestmerge.GitHubMergeResult{MergedCommit: mergedCommit}, nil
}

func (provider *Provider) lifecycleTarget(target pullrequestlifecycle.GitHubPullRequestTarget) (githubapi.Repository, int, error) {
	repository, err := parseRepository(target.RepositoryURL)
	if err != nil {
		return githubapi.Repository{}, 0, err
	}
	number := provider.Number(target)
	if number == 0 {
		return githubapi.Repository{}, 0, &pullrequestlifecycle.GitHubError{Kind: pullrequestlifecycle.GitHubSyncFailure, Err: errors.New("GitHub pull request number is required")}
	}
	return repository, number, nil
}

func (provider *Provider) mergeTarget(target pullrequestmerge.GitHubTarget) (githubapi.Repository, int, error) {
	repository, err := githubapi.ParseRepositoryURL(target.RepositoryURL)
	if err != nil {
		return githubapi.Repository{}, 0, &pullrequestmerge.GitHubError{Kind: pullrequestmerge.GitHubUnavailable, Message: "GitHub merge is unavailable.", Err: err}
	}
	number := number(target.SyncData, target.ExternalID)
	if number == 0 {
		return githubapi.Repository{}, 0, &pullrequestmerge.GitHubError{Kind: pullrequestmerge.GitHubUnavailable, Message: "GitHub pull request identity is unavailable."}
	}
	return repository, number, nil
}

func parseRepository(raw string) (githubapi.Repository, error) {
	repository, err := githubapi.ParseRepositoryURL(raw)
	if err != nil {
		return githubapi.Repository{}, &pullrequestlifecycle.GitHubError{Kind: pullrequestlifecycle.GitHubUnsupportedRepository, Err: err}
	}
	return repository, nil
}

func convert(repository githubapi.Repository, pullRequest githubapi.PullRequest) (pullrequestlifecycle.GitHubPullRequest, error) {
	if pullRequest.Number == 0 || strings.TrimSpace(pullRequest.Base.Ref) == "" || strings.TrimSpace(pullRequest.Base.SHA) == "" ||
		strings.TrimSpace(pullRequest.Head.Ref) == "" || strings.TrimSpace(pullRequest.Head.SHA) == "" {
		return pullrequestlifecycle.GitHubPullRequest{}, &pullrequestlifecycle.GitHubError{
			Kind: pullrequestlifecycle.GitHubSyncFailure, Err: errors.New("GitHub pull request response is missing branch or commit metadata"),
		}
	}
	merged := pullRequest.Merged || pullRequest.MergedAt != nil
	syncData, err := json.Marshal(syncDataEnvelope{GitHub: githubSyncData{
		Owner: repository.Owner, Repo: repository.Name, Number: pullRequest.Number, URL: pullRequest.HTMLURL, NodeID: pullRequest.NodeID,
		HeadRepositoryURL: refRepositoryURL(pullRequest.Head, githubapi.Repository{}), BaseRepositoryURL: refRepositoryURL(pullRequest.Base, repository),
		Draft: pullRequest.Draft, Mergeable: pullRequest.Mergeable, State: pullRequest.State,
		Merged: merged, MergedAt: pullRequest.MergedAt, MergeCommitSHA: pullRequest.MergeCommitSHA,
	}})
	if err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, &pullrequestlifecycle.GitHubError{Kind: pullrequestlifecycle.GitHubSyncFailure, Err: err}
	}
	status := pullrequestlifecycle.StatusOpen
	if merged {
		status = pullrequestlifecycle.StatusMerged
	} else if pullRequest.State == "closed" {
		status = pullrequestlifecycle.StatusClosed
	} else if pullRequest.Draft {
		status = pullrequestlifecycle.StatusDraft
	}
	closedAt := pullRequest.ClosedAt
	if status == pullrequestlifecycle.StatusClosed && closedAt == nil {
		closedAt = &pullRequest.UpdatedAt
	}
	mergedAt := pullRequest.MergedAt
	if status == pullrequestlifecycle.StatusMerged && mergedAt == nil {
		mergedAt = &pullRequest.UpdatedAt
	}
	mergeStrategy, mergedCommit := "", ""
	if status == pullrequestlifecycle.StatusMerged && pullRequest.MergeCommitSHA != "" {
		mergeStrategy, mergedCommit = "squash", pullRequest.MergeCommitSHA
	}
	return pullrequestlifecycle.GitHubPullRequest{
		Participants: participantSnapshot(pullRequest),
		Title:        strings.TrimSpace(pullRequest.Title), Summary: pullRequest.Body,
		HeadRepositoryURL: refRepositoryURL(pullRequest.Head, githubapi.Repository{}), BaseRepositoryURL: refRepositoryURL(pullRequest.Base, repository),
		BaseBranch: pullRequest.Base.Ref, BaseCommit: pullRequest.Base.SHA, HeadBranch: pullRequest.Head.Ref, HeadCommit: pullRequest.Head.SHA,
		Status: status, ExternalID: fmt.Sprintf("github:%s/%s#%d", repository.Owner, repository.Name, pullRequest.Number), SyncData: syncData,
		CreatedAt: pullRequest.CreatedAt, UpdatedAt: pullRequest.UpdatedAt, ClosedAt: closedAt, MergedAt: mergedAt,
		MergedCommit: mergedCommit, MergeStrategy: mergeStrategy,
	}, nil
}

func patchGitHub(raw json.RawMessage, update func(map[string]json.RawMessage) error) (json.RawMessage, error) {
	envelope := make(map[string]json.RawMessage)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, err
		}
	}
	github := make(map[string]json.RawMessage)
	if value := envelope["github"]; len(value) > 0 {
		if err := json.Unmarshal(value, &github); err != nil {
			return nil, err
		}
	}
	if envelope == nil {
		envelope = make(map[string]json.RawMessage)
	}
	if github == nil {
		github = make(map[string]json.RawMessage)
	}
	if err := update(github); err != nil {
		return nil, err
	}
	encodedGitHub, err := json.Marshal(github)
	if err != nil {
		return nil, err
	}
	envelope["github"] = encodedGitHub
	encoded, err := json.Marshal(envelope)
	return json.RawMessage(encoded), err
}

func number(raw json.RawMessage, externalID string) int {
	var data syncDataEnvelope
	if json.Unmarshal(raw, &data) == nil && data.GitHub.Number > 0 {
		return data.GitHub.Number
	}
	parts := strings.Split(strings.TrimSpace(externalID), "#")
	if len(parts) == 2 {
		value, err := strconv.Atoi(parts[1])
		if err == nil && value > 0 {
			return value
		}
	}
	return 0
}

func suffixTitle(title string, number int) string {
	title = strings.TrimSpace(title)
	suffix := fmt.Sprintf(" (#%d)", number)
	if strings.HasSuffix(title, suffix) {
		return title
	}
	return title + suffix
}

func lifecycleError(err error) error {
	if err == nil {
		return nil
	}
	var githubErr *githubapi.Error
	if errors.As(err, &githubErr) {
		kind := pullrequestlifecycle.GitHubSyncFailure
		if githubErr.Code == githubapi.ErrorCodeGHUnavailable {
			kind = pullrequestlifecycle.GitHubUnavailable
		}
		retryAfter := time.Duration(0)
		var retry interface{ RetryAfter() time.Duration }
		if errors.As(err, &retry) {
			retryAfter = retry.RetryAfter()
		}
		if retryAfter == 0 {
			retryAfter = providerRetryDelay(githubErr.Error())
		}
		var limited interface{ RateLimited() bool }
		rateLimited := errors.As(err, &limited) && limited.RateLimited()
		return &pullrequestlifecycle.GitHubError{
			Kind: kind, Err: err, UncertainOutcome: uncertainProviderOutcome(githubErr),
			RateLimitExceeded: rateLimited || retryAfter > 0, RetryAfterDelay: retryAfter,
		}
	}
	uncertain := uncertainProviderOutcome(&githubapi.Error{Err: err})
	var outcome interface{ Uncertain() bool }
	if errors.As(err, &outcome) {
		uncertain = outcome.Uncertain()
	}
	return &pullrequestlifecycle.GitHubError{Kind: pullrequestlifecycle.GitHubSyncFailure, Err: err, UncertainOutcome: uncertain}
}

func mergeError(err error) error {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "GitHub did not merge the pull request."
	}
	kind := pullrequestmerge.GitHubBlocked
	lower := strings.ToLower(message)
	var githubErr *githubapi.Error
	if strings.Contains(lower, "sha") || strings.Contains(lower, "head") {
		kind = pullrequestmerge.GitHubHeadChanged
	} else if errors.As(err, &githubErr) && (strings.Contains(lower, "executable was not found") ||
		strings.Contains(lower, "auth") || strings.Contains(lower, "network") || strings.Contains(lower, "timed out") ||
		strings.Contains(lower, "connection") || strings.Contains(lower, "could not resolve")) {
		kind = pullrequestmerge.GitHubUnavailable
	}
	return &pullrequestmerge.GitHubError{Kind: kind, Message: message, Err: lifecycleError(err)}
}

func refRepositoryURL(ref githubapi.Ref, fallback githubapi.Repository) string {
	if ref.Repository != nil {
		if ref.Repository.CloneURL != "" {
			return ref.Repository.CloneURL
		}
		if ref.Repository.FullName != "" {
			return "https://github.com/" + ref.Repository.FullName + ".git"
		}
	}
	if fallback.Owner == "" || fallback.Name == "" {
		return ""
	}
	return "https://github.com/" + fallback.Owner + "/" + fallback.Name + ".git"
}

func (provider *Provider) ListActive(ctx context.Context, repositoryURL string) ([]pullrequestlifecycle.GitHubPullRequest, error) {
	repository, err := parseRepository(repositoryURL)
	if err != nil {
		return nil, err
	}
	remote, err := provider.client.ListActivePullRequests(ctx, repository)
	if err != nil {
		return nil, lifecycleError(err)
	}
	result := make([]pullrequestlifecycle.GitHubPullRequest, 0, len(remote))
	for _, pr := range remote {
		value, err := convert(repository, pr)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (provider *Provider) ConvertToDraft(ctx context.Context, target pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubPullRequest, error) {
	return provider.lifecycleMutationResult(ctx, target, true)
}
func (provider *Provider) MarkReadyForReview(ctx context.Context, target pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubPullRequest, error) {
	return provider.lifecycleMutationResult(ctx, target, false)
}
func (provider *Provider) lifecycleMutationResult(ctx context.Context, target pullrequestlifecycle.GitHubPullRequestTarget, draft bool) (pullrequestlifecycle.GitHubPullRequest, error) {
	repository, _, err := provider.lifecycleTarget(target)
	if err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, err
	}
	var remote githubapi.PullRequest
	if draft {
		remote, err = provider.client.ConvertPullRequestToDraft(ctx, provider.ExternalID(target))
	} else {
		remote, err = provider.client.MarkPullRequestReadyForReview(ctx, provider.ExternalID(target))
	}
	if err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, lifecycleError(err)
	}
	return convertMutation(repository, remote)
}

func uncertainProviderOutcome(err *githubapi.Error) bool { return err.Uncertain() }

var retryAfterHeader = regexp.MustCompile(`(?i)retry-after:\s*(\d+)`)
var rateResetHeader = regexp.MustCompile(`(?i)x-ratelimit-reset:\s*(\d+)`)

func providerRetryDelay(message string) time.Duration {
	if match := retryAfterHeader.FindStringSubmatch(message); len(match) == 2 {
		seconds, _ := strconv.ParseInt(match[1], 10, 64)
		return time.Duration(seconds) * time.Second
	}
	if match := rateResetHeader.FindStringSubmatch(message); len(match) == 2 {
		seconds, _ := strconv.ParseInt(match[1], 10, 64)
		if delay := time.Until(time.Unix(seconds, 0)); delay > 0 {
			return delay
		}
	}
	return 0
}

func convertMutation(repository githubapi.Repository, remote githubapi.PullRequest) (pullrequestlifecycle.GitHubPullRequest, error) {
	result, err := convert(repository, remote)
	if err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, &pullrequestlifecycle.GitHubError{Kind: pullrequestlifecycle.GitHubSyncFailure, Err: err, UncertainOutcome: true}
	}
	return result, nil
}

func participantSnapshot(pr githubapi.PullRequest) pullrequestparticipants.RemoteParticipantSnapshot {
	snapshot := pullrequestparticipants.RemoteParticipantSnapshot{Complete: pr.Assignees != nil && pr.RequestedReviewers != nil}
	member := func(u githubapi.User) githubidentity.SourceMember {
		return githubidentity.SourceMember{NodeID: u.NodeID, Login: u.Login, AvatarURL: u.AvatarURL, ProfileURL: u.HTMLURL}
	}
	if pr.User.NodeID != "" {
		author := member(pr.User)
		snapshot.Author = &author
	}
	for _, u := range pr.Assignees {
		snapshot.AssigneeGitHubNodeIDs = append(snapshot.AssigneeGitHubNodeIDs, u.NodeID)
		snapshot.Members = append(snapshot.Members, member(u))
	}
	for _, u := range pr.RequestedReviewers {
		snapshot.RequestedReviewerGitHubNodeIDs = append(snapshot.RequestedReviewerGitHubNodeIDs, u.NodeID)
		snapshot.Members = append(snapshot.Members, member(u))
	}
	return snapshot
}
