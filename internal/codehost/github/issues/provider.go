// Package issues adapts GitHub issue transport and DTOs to the issue workflow.
package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"github.com/holark-ai/holark/internal/githubidentity"
	"github.com/holark-ai/holark/internal/issues"
	issueworkflow "github.com/holark-ai/holark/internal/issues/workflow"
)

var ErrClientUnsupported = errors.New("configured GitHub client does not support the required issue operation")

type issueLister interface {
	ListIssues(context.Context, githubapi.Repository) ([]githubapi.Issue, error)
}

type openIssueReader interface {
	ListOpenIssues(context.Context, githubapi.Repository) ([]githubapi.Issue, error)
	GetIssue(context.Context, githubapi.Repository, int) (githubapi.Issue, error)
}

type issueCreator interface {
	CreateIssue(context.Context, githubapi.Repository, githubapi.CreateIssueRequest) (githubapi.Issue, error)
}

type issueUpdater interface {
	UpdateIssue(context.Context, githubapi.Repository, int, githubapi.UpdateIssueRequest) (githubapi.Issue, error)
}

type labelClient interface {
	ListLabels(context.Context, githubapi.Repository) ([]githubapi.Label, error)
	CreateLabel(context.Context, githubapi.Repository, githubapi.CreateLabelRequest) (githubapi.Label, error)
	ListIssueLabels(context.Context, githubapi.Repository, int) ([]githubapi.Label, error)
	AddIssueLabels(context.Context, githubapi.Repository, int, []string) ([]githubapi.Label, error)
	SetIssueLabels(context.Context, githubapi.Repository, int, []string) ([]githubapi.Label, error)
}
type issueAssigneeReplacer interface {
	ReplaceIssueAssignees(context.Context, githubapi.Repository, int, []string) (githubapi.Issue, error)
}

type issueAssigneeAdder interface {
	AddIssueAssignee(context.Context, githubapi.Repository, int, string) (githubapi.Issue, error)
}

type issueAssigneeRemover interface {
	RemoveIssueAssignee(context.Context, githubapi.Repository, int, string) (githubapi.Issue, error)
}

type Provider struct {
	client any
}

func New(client any) *Provider {
	if client == nil {
		client = githubapi.NewCLIClient()
	}
	return &Provider{client: client}
}

func (provider *Provider) Snapshot(ctx context.Context, projectID, repositoryURL string, syncedAt time.Time) ([]issueworkflow.RemoteIssue, error) {
	return provider.snapshot(ctx, projectID, repositoryURL, syncedAt, false)
}

func (provider *Provider) SnapshotOpen(ctx context.Context, projectID, repositoryURL string, syncedAt time.Time) ([]issueworkflow.RemoteIssue, error) {
	return provider.snapshot(ctx, projectID, repositoryURL, syncedAt, true)
}

func (provider *Provider) snapshot(ctx context.Context, projectID, repositoryURL string, syncedAt time.Time, open bool) ([]issueworkflow.RemoteIssue, error) {
	repository, err := githubapi.ParseRepositoryURL(repositoryURL)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	var remoteIssues []githubapi.Issue
	if open {
		client, clientErr := clientAs[openIssueReader](provider.client)
		if clientErr != nil {
			return nil, classifyTransportError(clientErr)
		}
		remoteIssues, err = client.ListOpenIssues(ctx, repository)
	} else {
		client, clientErr := clientAs[issueLister](provider.client)
		if clientErr != nil {
			return nil, classifyTransportError(clientErr)
		}
		remoteIssues, err = client.ListIssues(ctx, repository)
	}
	if err != nil {
		return nil, classifyTransportError(err)
	}
	result := make([]issueworkflow.RemoteIssue, 0, len(remoteIssues))
	for _, remote := range remoteIssues {
		if remote.PullRequest != nil {
			continue
		}
		converted, err := remoteIssueFromGitHub(projectID, repository, remote, syncedAt)
		if err != nil {
			return nil, classifyTransportError(err)
		}
		result = append(result, converted)
	}
	return result, nil
}

func (provider *Provider) Get(ctx context.Context, issue issues.Issue, repositoryURL string, syncedAt time.Time) (issueworkflow.RemoteIssue, error) {
	repository, err := githubapi.ParseRepositoryURL(repositoryURL)
	if err != nil {
		return issueworkflow.RemoteIssue{}, classifyTransportError(err)
	}
	reference, err := issueReference(issue)
	if err != nil {
		return issueworkflow.RemoteIssue{}, classifyTransportError(err)
	}
	client, err := clientAs[openIssueReader](provider.client)
	if err != nil {
		return issueworkflow.RemoteIssue{}, classifyTransportError(err)
	}
	remote, err := client.GetIssue(ctx, repository, reference.Number)
	if err != nil {
		return issueworkflow.RemoteIssue{}, classifyTransportError(err)
	}
	var stored syncDataEnvelope
	if err := json.Unmarshal(issue.SyncData, &stored); err != nil {
		return issueworkflow.RemoteIssue{}, classifyTransportError(err)
	}
	if remote.PullRequest != nil || remote.Number != reference.Number || (stored.GitHub.NodeID != "" && remote.NodeID != stored.GitHub.NodeID) {
		return issueworkflow.RemoteIssue{}, classifyTransportError(errors.New("GitHub returned a different issue"))
	}
	converted, err := remoteIssueFromGitHub(issue.RepositoryID, repository, remote, syncedAt)
	return converted, classifyTransportError(err)
}

func (provider *Provider) Create(ctx context.Context, projectID, repositoryURL, title, body string, syncedAt time.Time) (issueworkflow.RemoteIssue, error) {
	repository, err := githubapi.ParseRepositoryURL(repositoryURL)
	if err != nil {
		return issueworkflow.RemoteIssue{}, classifyTransportError(err)
	}
	client, err := clientAs[issueCreator](provider.client)
	if err != nil {
		return issueworkflow.RemoteIssue{}, classifyTransportError(err)
	}
	remote, err := client.CreateIssue(ctx, repository, githubapi.CreateIssueRequest{Title: title, Body: body})
	if err != nil {
		classified := classifyMutationError(err)
		if errors.Is(classified, issueworkflow.ErrMutationAccepted) {
			return issueworkflow.RemoteIssue{Reference: remoteReferenceFromGitHub(remote)}, classified
		}
		return issueworkflow.RemoteIssue{}, classified
	}
	converted, err := remoteIssueFromGitHub(projectID, repository, remote, syncedAt)
	if err != nil {
		return issueworkflow.RemoteIssue{Reference: remoteReferenceFromGitHub(remote)}, mutationAccepted(err)
	}
	return converted, nil
}

func (provider *Provider) ListLabels(ctx context.Context, projectID, repositoryURL string) ([]issues.Label, error) {
	repository, client, err := provider.labelClient(repositoryURL)
	if err != nil {
		return nil, err
	}
	remote, err := client.ListLabels(ctx, repository)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	return labelsFromGitHub(remote)
}

func (provider *Provider) CreateLabel(ctx context.Context, projectID, repositoryURL, name, color, description string) (issues.Label, error) {
	repository, client, err := provider.labelClient(repositoryURL)
	if err != nil {
		return issues.Label{}, err
	}
	remote, err := client.CreateLabel(ctx, repository, githubapi.CreateLabelRequest{Name: name, Color: color, Description: description})
	if err != nil {
		return issues.Label{}, classifyMutationError(err)
	}
	labels, err := labelsFromGitHub([]githubapi.Label{remote})
	if err != nil {
		return issues.Label{}, mutationAccepted(err)
	}
	return labels[0], nil
}

func (provider *Provider) ListIssueLabels(ctx context.Context, issue issues.Issue, repositoryURL string) ([]issues.Label, error) {
	return provider.issueLabels(ctx, issue, repositoryURL, "", nil)
}

func (provider *Provider) AddIssueLabels(ctx context.Context, issue issues.Issue, repositoryURL string, names []string) ([]issues.Label, error) {
	return provider.issueLabels(ctx, issue, repositoryURL, "POST", names)
}

func (provider *Provider) ReplaceIssueLabels(ctx context.Context, issue issues.Issue, repositoryURL string, names []string) ([]issues.Label, error) {
	return provider.issueLabels(ctx, issue, repositoryURL, "PUT", names)
}

func (provider *Provider) labelClient(repositoryURL string) (githubapi.Repository, labelClient, error) {
	repository, err := githubapi.ParseRepositoryURL(repositoryURL)
	if err != nil {
		return githubapi.Repository{}, nil, classifyTransportError(err)
	}
	client, err := clientAs[labelClient](provider.client)
	if err != nil {
		return githubapi.Repository{}, nil, classifyTransportError(err)
	}
	return repository, client, nil
}

func (provider *Provider) issueLabels(ctx context.Context, issue issues.Issue, repositoryURL, method string, names []string) ([]issues.Label, error) {
	repository, client, err := provider.labelClient(repositoryURL)
	if err != nil {
		return nil, err
	}
	reference, err := issueReference(issue)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	var remote []githubapi.Label
	switch method {
	case "":
		remote, err = client.ListIssueLabels(ctx, repository, reference.Number)
	case "POST":
		remote, err = client.AddIssueLabels(ctx, repository, reference.Number, names)
	case "PUT":
		remote, err = client.SetIssueLabels(ctx, repository, reference.Number, names)
	}
	if err != nil {
		if method != "" {
			return nil, classifyMutationError(err)
		}
		return nil, classifyTransportError(err)
	}
	labels, err := labelsFromGitHub(remote)
	if err != nil {
		if method != "" {
			return nil, mutationAccepted(err)
		}
		return nil, classifyTransportError(err)
	}
	return labels, nil
}

func (provider *Provider) Update(ctx context.Context, issue issues.Issue, repositoryURL string, request issueworkflow.UpdateRequest, syncedAt time.Time) (issueworkflow.RemoteIssue, error) {
	repository, err := githubapi.ParseRepositoryURL(repositoryURL)
	if err != nil {
		return issueworkflow.RemoteIssue{}, classifyTransportError(err)
	}
	reference, err := issueReference(issue)
	if err != nil {
		return issueworkflow.RemoteIssue{}, classifyTransportError(err)
	}
	client, err := clientAs[issueUpdater](provider.client)
	if err != nil {
		return issueworkflow.RemoteIssue{}, classifyTransportError(err)
	}
	remote, err := client.UpdateIssue(ctx, repository, reference.Number, githubapi.UpdateIssueRequest{
		Title: request.Title, Body: request.Body, State: request.State,
	})
	if err != nil {
		classified := classifyMutationError(err)
		if errors.Is(classified, issueworkflow.ErrMutationAccepted) {
			return issueworkflow.RemoteIssue{Reference: reference}, classified
		}
		return issueworkflow.RemoteIssue{}, classified
	}
	converted, err := remoteIssueFromGitHub(issue.RepositoryID, repository, remote, syncedAt)
	if err != nil {
		failedReference := remoteReferenceFromGitHub(remote)
		if failedReference.Number == 0 {
			failedReference.Number = reference.Number
		}
		if strings.TrimSpace(failedReference.URL) == "" {
			failedReference.URL = reference.URL
		}
		return issueworkflow.RemoteIssue{Reference: failedReference}, mutationAccepted(err)
	}
	return converted, nil
}

func (provider *Provider) ReplaceAssignees(ctx context.Context, issue issues.Issue, repositoryURL string, logins []string, syncedAt time.Time) (issueworkflow.RemoteIssue, error) {
	return provider.mutateAssignees(ctx, issue, repositoryURL, syncedAt, func(repository githubapi.Repository, number int) (githubapi.Issue, error) {
		client, err := clientAs[issueAssigneeReplacer](provider.client)
		if err != nil {
			return githubapi.Issue{}, err
		}
		return client.ReplaceIssueAssignees(ctx, repository, number, logins)
	})
}

func (provider *Provider) AddAssignee(ctx context.Context, issue issues.Issue, repositoryURL, login string, syncedAt time.Time) (issueworkflow.RemoteIssue, error) {
	return provider.mutateAssignees(ctx, issue, repositoryURL, syncedAt, func(repository githubapi.Repository, number int) (githubapi.Issue, error) {
		client, err := clientAs[issueAssigneeAdder](provider.client)
		if err != nil {
			return githubapi.Issue{}, err
		}
		return client.AddIssueAssignee(ctx, repository, number, login)
	})
}

func (provider *Provider) RemoveAssignee(ctx context.Context, issue issues.Issue, repositoryURL, login string, syncedAt time.Time) (issueworkflow.RemoteIssue, error) {
	return provider.mutateAssignees(ctx, issue, repositoryURL, syncedAt, func(repository githubapi.Repository, number int) (githubapi.Issue, error) {
		client, err := clientAs[issueAssigneeRemover](provider.client)
		if err != nil {
			return githubapi.Issue{}, err
		}
		return client.RemoveIssueAssignee(ctx, repository, number, login)
	})
}

type issueAssigneeMutation func(githubapi.Repository, int) (githubapi.Issue, error)

func (provider *Provider) mutateAssignees(ctx context.Context, issue issues.Issue, repositoryURL string, syncedAt time.Time, mutate issueAssigneeMutation) (issueworkflow.RemoteIssue, error) {
	repository, err := githubapi.ParseRepositoryURL(repositoryURL)
	if err != nil {
		return issueworkflow.RemoteIssue{}, classifyTransportError(err)
	}
	reference, err := issueReference(issue)
	if err != nil {
		return issueworkflow.RemoteIssue{}, classifyTransportError(err)
	}
	remote, err := mutate(repository, reference.Number)
	if err != nil {
		classified := classifyMutationError(err)
		if errors.Is(classified, issueworkflow.ErrMutationAccepted) {
			return issueworkflow.RemoteIssue{Reference: reference}, classified
		}
		return issueworkflow.RemoteIssue{}, classified
	}
	converted, err := remoteIssueFromGitHub(issue.RepositoryID, repository, remote, syncedAt)
	if err != nil {
		failedReference := remoteReferenceFromGitHub(remote)
		if failedReference.Number == 0 {
			failedReference.Number = reference.Number
		}
		if strings.TrimSpace(failedReference.URL) == "" {
			failedReference.URL = reference.URL
		}
		return issueworkflow.RemoteIssue{Reference: failedReference}, mutationAccepted(err)
	}
	return converted, nil
}

func clientAs[T any](configured any) (T, error) {
	client, ok := configured.(T)
	if !ok {
		var zero T
		return zero, ErrClientUnsupported
	}
	return client, nil
}

func remoteIssueFromGitHub(projectID string, repository githubapi.Repository, remote githubapi.Issue, syncedAt time.Time) (issueworkflow.RemoteIssue, error) {
	issue, err := issueFromGitHub(projectID, repository, remote, syncedAt)
	if err != nil {
		return issueworkflow.RemoteIssue{}, err
	}
	members := []githubidentity.SourceMember{{NodeID: remote.User.NodeID, Login: remote.User.Login, AvatarURL: remote.User.AvatarURL, ProfileURL: remote.User.HTMLURL}}
	assignees := make([]string, len(remote.Assignees))
	for index := range remote.Assignees {
		assignees[index] = strings.TrimSpace(remote.Assignees[index].NodeID)
		u := remote.Assignees[index]
		members = append(members, githubidentity.SourceMember{NodeID: u.NodeID, Login: u.Login, AvatarURL: u.AvatarURL, ProfileURL: u.HTMLURL})
	}
	return issueworkflow.RemoteIssue{
		Members:   members,
		Reference: issueworkflow.RemoteReference{URL: remote.HTMLURL, Number: remote.Number},
		Issue:     issue, CreatorGitHubNodeID: strings.TrimSpace(remote.User.NodeID), AssigneeGitHubNodeIDs: assignees,
	}, nil
}

func remoteReferenceFromGitHub(remote githubapi.Issue) issueworkflow.RemoteReference {
	return issueworkflow.RemoteReference{URL: remote.HTMLURL, Number: remote.Number}
}

func classifyTransportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, githubapi.ErrUnsupportedRepository) {
		return fmt.Errorf("%w: %w", issueworkflow.ErrRepositoryUnsupported, err)
	}
	var githubErr *githubapi.Error
	if errors.As(err, &githubErr) && githubErr.Code == githubapi.ErrorCodeGHUnavailable {
		return fmt.Errorf("%w: %w", issueworkflow.ErrIssueSourceUnavailable, err)
	}
	return fmt.Errorf("%w: %w", issueworkflow.ErrIssueSourceFailed, err)
}

func classifyMutationError(err error) error {
	if err == nil {
		return nil
	}
	var githubErr *githubapi.Error
	if errors.As(err, &githubErr) && githubErr.Code == githubapi.ErrorCodeMutationAccepted {
		return mutationAccepted(err)
	}
	return classifyTransportError(err)
}

func mutationAccepted(err error) error {
	return fmt.Errorf("%w: %w", issueworkflow.ErrMutationAccepted, err)
}

func issueFromGitHub(projectID string, repository githubapi.Repository, issue githubapi.Issue, syncedAt time.Time) (issues.Issue, error) {
	if issue.Number == 0 {
		return issues.Issue{}, &githubapi.Error{Code: githubapi.ErrorCodeSyncFailed, Err: errors.New("GitHub issue response is missing issue number")}
	}
	syncData, err := issueSyncData(issue)
	if err != nil {
		return issues.Issue{}, err
	}
	labels, err := labelsFromGitHub(issue.Labels)
	if err != nil {
		return issues.Issue{}, err
	}
	status := issues.IssueOpen
	if issue.State == "closed" {
		status = issues.IssueClosed
	}
	closedAt := issue.ClosedAt
	if status == issues.IssueClosed && closedAt == nil {
		closedAt = &issue.UpdatedAt
	}
	return issues.Issue{
		RepositoryID: projectID, Title: strings.TrimSpace(issue.Title), Body: issue.Body, Status: status,
		SyncProvider:   string(issues.IssueSyncProviderGitHub),
		SyncExternalID: fmt.Sprintf("github:%s/%s#%d", repository.Owner, repository.Name, issue.Number),
		SyncData:       syncData, Labels: labels, AssigneeHolarkIDs: []string{},
		LinkedPullRequestIDs: []string{},
		CreatedAt:            issue.CreatedAt, UpdatedAt: issue.UpdatedAt, ClosedAt: closedAt, SyncedAt: &syncedAt,
	}, nil
}

func labelsFromGitHub(remote []githubapi.Label) ([]issues.Label, error) {
	labels := make([]issues.Label, 0, len(remote))
	for _, label := range remote {
		externalID := strings.TrimSpace(label.NodeID)
		if externalID == "" && label.ID != 0 {
			externalID = strconv.FormatInt(label.ID, 10)
		}
		if externalID == "" {
			return nil, &githubapi.Error{Code: githubapi.ErrorCodeSyncFailed, Err: errors.New("GitHub label response is missing node ID and numeric ID")}
		}
		labels = append(labels, issues.Label{
			Name: label.Name, Color: label.Color, Description: label.Description,
			SyncProvider: string(issues.IssueSyncProviderGitHub), SyncExternalID: externalID,
		})
	}
	return labels, nil
}

type syncDataEnvelope struct {
	GitHub syncDataPayload `json:"github"`
}

type syncDataPayload struct {
	Number    int       `json:"number"`
	URL       string    `json:"url"`
	NodeID    string    `json:"node_id,omitempty"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
}

func issueSyncData(issue githubapi.Issue) (json.RawMessage, error) {
	encoded, err := json.Marshal(syncDataEnvelope{GitHub: syncDataPayload{
		Number: issue.Number, URL: issue.HTMLURL, NodeID: issue.NodeID, State: issue.State, UpdatedAt: issue.UpdatedAt,
	}})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

func issueReference(issue issues.Issue) (issueworkflow.RemoteReference, error) {
	var syncData syncDataEnvelope
	if err := json.Unmarshal(issue.SyncData, &syncData); err != nil {
		return issueworkflow.RemoteReference{}, &githubapi.Error{Code: githubapi.ErrorCodeSyncFailed, Err: fmt.Errorf("decode issue sync data: %w", err)}
	}
	if syncData.GitHub.Number == 0 {
		return issueworkflow.RemoteReference{}, &githubapi.Error{Code: githubapi.ErrorCodeSyncFailed, Err: errors.New("issue sync data is missing GitHub issue number")}
	}
	return issueworkflow.RemoteReference{URL: syncData.GitHub.URL, Number: syncData.GitHub.Number}, nil
}

var _ issueworkflow.Transport = (*Provider)(nil)
var _ issueworkflow.OpenTransport = (*Provider)(nil)
var _ issueworkflow.LabelTransport = (*Provider)(nil)
