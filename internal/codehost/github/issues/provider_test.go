package issues

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"github.com/holark-ai/holark/internal/issues"
	issueworkflow "github.com/holark-ai/holark/internal/issues/workflow"
)

func TestSnapshotTranslatesCanonicalGitHubIssuesAndIdentities(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	client := &fakeClient{listed: []githubapi.Issue{{
		Number: 12, NodeID: "issue-node", Title: "  Fix sync  ", Body: "Body", HTMLURL: "https://github.com/o/r/issues/12",
		State: "open", CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
		User:      githubapi.User{NodeID: "creator-node"},
		Assignees: []githubapi.User{{NodeID: "second"}, {NodeID: "first"}, {NodeID: "second"}},
	}}}
	provider := New(client)
	snapshot, err := provider.Snapshot(t.Context(), "project", "https://github.com/o/r", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 1 || snapshot[0].Issue.Title != "Fix sync" ||
		snapshot[0].CreatorGitHubNodeID != "creator-node" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot[0].Reference.URL != "https://github.com/o/r/issues/12" || snapshot[0].Reference.Number != 12 {
		t.Fatalf("remote reference = %#v", snapshot[0].Reference)
	}
	got := snapshot[0].AssigneeGitHubNodeIDs
	if len(got) != 3 || got[0] != "second" || got[1] != "first" || got[2] != "second" {
		t.Fatalf("ordered assignees = %#v", got)
	}
	var syncData syncDataEnvelope
	if err := json.Unmarshal(snapshot[0].Issue.SyncData, &syncData); err != nil {
		t.Fatal(err)
	}
	if syncData.GitHub.Number != 12 || syncData.GitHub.URL == "" {
		t.Fatalf("sync data = %#v", syncData)
	}
}

func TestProviderClassifiesEveryFailureAtTheWorkflowBoundary(t *testing.T) {
	unavailableCause := errors.New("gh executable is unavailable")
	apiCause := errors.New("GitHub API rejected the request")
	tests := []struct {
		name       string
		provider   *Provider
		repository string
		want       error
		cause      error
	}{
		{name: "unsupported repository", provider: New(&fakeClient{}), repository: "https://gitlab.com/o/r", want: issueworkflow.ErrRepositoryUnsupported, cause: githubapi.ErrUnsupportedRepository},
		{name: "unavailable GitHub transport", provider: New(&fakeClient{listErr: &githubapi.Error{Code: githubapi.ErrorCodeGHUnavailable, Err: unavailableCause}}), repository: "https://github.com/o/r", want: issueworkflow.ErrIssueSourceUnavailable, cause: unavailableCause},
		{name: "GitHub API failure", provider: New(&fakeClient{listErr: &githubapi.Error{Code: githubapi.ErrorCodeSyncFailed, Err: apiCause}}), repository: "https://github.com/o/r", want: issueworkflow.ErrIssueSourceFailed, cause: apiCause},
		{name: "malformed GitHub response", provider: New(&fakeClient{listed: []githubapi.Issue{{Title: "missing number"}}}), repository: "https://github.com/o/r", want: issueworkflow.ErrIssueSourceFailed},
		{name: "unsupported client", provider: New(struct{}{}), repository: "https://github.com/o/r", want: issueworkflow.ErrIssueSourceFailed, cause: ErrClientUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.provider.Snapshot(t.Context(), "project", test.repository, time.Now().UTC())
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want classification %v", err, test.want)
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("error = %v, want preserved cause %v", err, test.cause)
			}
		})
	}
}

func TestUpdateForwardsOnlyRequestedFields(t *testing.T) {
	now := time.Now().UTC()
	client := &fakeClient{updated: githubapi.Issue{
		Number: 7, Title: "New", State: "open", CreatedAt: now, UpdatedAt: now,
		User: githubapi.User{NodeID: "creator"},
	}}
	provider := New(client)
	title := "New"
	_, err := provider.Update(t.Context(), issueFromNumber(7), "git@github.com:o/r.git", issueworkflow.UpdateRequest{Title: &title}, now)
	if err != nil {
		t.Fatal(err)
	}
	if client.updateRequest.Title == nil || *client.updateRequest.Title != "New" ||
		client.updateRequest.Body != nil || client.updateRequest.State != nil {
		t.Fatalf("request = %#v", client.updateRequest)
	}
}

func TestAssigneeMutationsReturnCanonicalStateAndPreserveAcceptedReference(t *testing.T) {
	now := time.Now().UTC()
	stored := issueWithReference(7, "https://github.com/o/r/issues/7")
	stored.RepositoryID = "project"
	canonical := githubapi.Issue{
		Number: 7, HTMLURL: "https://github.com/o/r/issues/7", State: "open",
		CreatedAt: now, UpdatedAt: now, User: githubapi.User{NodeID: "creator"},
		Assignees: []githubapi.User{{NodeID: "node-a", Login: "a"}},
	}

	remote, err := New(&fakeClient{assigneeIssue: canonical}).ReplaceAssignees(t.Context(), stored, "https://github.com/o/r", []string{"a", "b"}, now)
	if err != nil || len(remote.AssigneeGitHubNodeIDs) != 1 || remote.AssigneeGitHubNodeIDs[0] != "node-a" {
		t.Fatalf("canonical remote=%#v error=%v", remote, err)
	}

	acceptedCause := &githubapi.Error{Code: githubapi.ErrorCodeMutationAccepted, Err: errors.New("decode response")}
	remote, err = New(&fakeClient{assigneeErr: acceptedCause}).RemoveAssignee(t.Context(), stored, "https://github.com/o/r", "a", now)
	if !errors.Is(err, issueworkflow.ErrMutationAccepted) || remote.Reference.Number != 7 || remote.Reference.URL != "https://github.com/o/r/issues/7" {
		t.Fatalf("accepted remote=%#v error=%v", remote, err)
	}
}

func TestMutationAcceptancePreservesAvailableReferencesAndCauses(t *testing.T) {
	acceptedCause := errors.New("decode mutation response")
	acceptedError := &githubapi.Error{Code: githubapi.ErrorCodeMutationAccepted, Err: acceptedCause}
	now := time.Now().UTC()

	for _, test := range []struct {
		name          string
		provider      *Provider
		invoke        func(*Provider) (issueworkflow.RemoteIssue, error)
		wantURL       string
		wantNumber    int
		wantGitHubErr error
	}{
		{
			name:     "create response decoding failure may have no reference",
			provider: New(&fakeClient{createErr: acceptedError}),
			invoke: func(provider *Provider) (issueworkflow.RemoteIssue, error) {
				return provider.Create(t.Context(), "project", "https://github.com/o/r", "Title", "Body", now)
			},
			wantGitHubErr: acceptedError,
		},
		{
			name:     "update response decoding failure uses stored reference",
			provider: New(&fakeClient{updateErr: acceptedError}),
			invoke: func(provider *Provider) (issueworkflow.RemoteIssue, error) {
				return provider.Update(t.Context(), issueWithReference(7, "https://github.com/o/r/issues/7"), "https://github.com/o/r", issueworkflow.UpdateRequest{}, now)
			},
			wantURL: "https://github.com/o/r/issues/7", wantNumber: 7, wantGitHubErr: acceptedError,
		},
		{
			name:     "create conversion failure uses returned DTO reference",
			provider: New(&fakeClient{created: malformedConvertedIssue(11)}),
			invoke: func(provider *Provider) (issueworkflow.RemoteIssue, error) {
				return provider.Create(t.Context(), "project", "https://github.com/o/r", "Title", "Body", now)
			},
			wantURL: "https://github.com/o/r/issues/11", wantNumber: 11,
		},
		{
			name:     "update conversion failure uses returned DTO reference",
			provider: New(&fakeClient{updated: malformedConvertedIssue(12)}),
			invoke: func(provider *Provider) (issueworkflow.RemoteIssue, error) {
				return provider.Update(t.Context(), issueWithReference(7, "https://github.com/o/r/issues/7"), "https://github.com/o/r", issueworkflow.UpdateRequest{}, now)
			},
			wantURL: "https://github.com/o/r/issues/12", wantNumber: 12,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote, err := test.invoke(test.provider)
			if !errors.Is(err, issueworkflow.ErrMutationAccepted) {
				t.Fatalf("error = %v, want mutation accepted", err)
			}
			if test.wantGitHubErr != nil && !errors.Is(err, test.wantGitHubErr) {
				t.Fatalf("error = %v, want preserved GitHub error %v", err, test.wantGitHubErr)
			}
			if remote.Reference.URL != test.wantURL || remote.Reference.Number != test.wantNumber {
				t.Fatalf("reference = %#v, want %q #%d", remote.Reference, test.wantURL, test.wantNumber)
			}
		})
	}
}

func TestRejectedMutationRemainsAnIssueSourceFailure(t *testing.T) {
	rejectedCause := errors.New("validation failed")
	provider := New(&fakeClient{createErr: &githubapi.Error{Code: githubapi.ErrorCodeSyncFailed, Err: rejectedCause}})
	_, err := provider.Create(t.Context(), "project", "https://github.com/o/r", "Title", "Body", time.Now().UTC())
	if !errors.Is(err, issueworkflow.ErrIssueSourceFailed) || errors.Is(err, issueworkflow.ErrMutationAccepted) || !errors.Is(err, rejectedCause) {
		t.Fatalf("error = %v, want ordinary source failure with preserved cause", err)
	}
}

func malformedConvertedIssue(number int) githubapi.Issue {
	return githubapi.Issue{
		Number: number, HTMLURL: "https://github.com/o/r/issues/" + jsonNumber(number), State: "open",
		Labels: []githubapi.Label{{Name: "missing identity"}},
	}
}

func issueFromNumber(number int) issues.Issue {
	return issues.Issue{RepositoryID: "project", SyncData: json.RawMessage(
		`{"github":{"number":` + jsonNumber(number) + `}}`)}
}

func issueWithReference(number int, url string) issues.Issue {
	return issues.Issue{RepositoryID: "project", SyncData: json.RawMessage(
		`{"github":{"number":` + jsonNumber(number) + `,"url":` + strconv.Quote(url) + `}}`)}
}

func jsonNumber(number int) string { return strconv.Itoa(number) }

type fakeClient struct {
	listed        []githubapi.Issue
	listErr       error
	created       githubapi.Issue
	createErr     error
	updated       githubapi.Issue
	updateErr     error
	updateRequest githubapi.UpdateIssueRequest
	assigneeIssue githubapi.Issue
	assigneeErr   error
}

func (client *fakeClient) ListIssues(context.Context, githubapi.Repository) ([]githubapi.Issue, error) {
	return append([]githubapi.Issue(nil), client.listed...), client.listErr
}

func (client *fakeClient) CreateIssue(context.Context, githubapi.Repository, githubapi.CreateIssueRequest) (githubapi.Issue, error) {
	return client.created, client.createErr
}

func (client *fakeClient) UpdateIssue(_ context.Context, _ githubapi.Repository, _ int, request githubapi.UpdateIssueRequest) (githubapi.Issue, error) {
	client.updateRequest = request
	return client.updated, client.updateErr
}

func (client *fakeClient) ReplaceIssueAssignees(context.Context, githubapi.Repository, int, []string) (githubapi.Issue, error) {
	return client.assigneeIssue, client.assigneeErr
}

func (client *fakeClient) AddIssueAssignee(context.Context, githubapi.Repository, int, string) (githubapi.Issue, error) {
	return client.assigneeIssue, client.assigneeErr
}

func (client *fakeClient) RemoveIssueAssignee(context.Context, githubapi.Repository, int, string) (githubapi.Issue, error) {
	return client.assigneeIssue, client.assigneeErr
}
