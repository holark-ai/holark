package pullrequestparticipants_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	githubpullrequestparticipants "github.com/holark-ai/holark/internal/codehost/github/pullrequestparticipants"
	"github.com/holark-ai/holark/internal/pullrequestparticipants"
)

func TestAdapterFetchesSnapshotInProviderOrder(t *testing.T) {
	client := &fakeClient{responses: []any{
		map[string]any{"assignees": []any{map[string]any{"node_id": "A"}, map[string]any{"node_id": "B"}}},
		map[string]any{"users": []any{map[string]any{"node_id": "R2"}, map[string]any{"node_id": "R1"}}, "teams": []any{map[string]any{"slug": "team"}}},
	}}
	adapter := githubpullrequestparticipants.New(client)
	got, err := adapter.GetSnapshot(context.Background(), pullrequestparticipants.ProviderTarget{RepositoryURL: "https://github.com/acme/widgets.git", PullRequestNumber: 7})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.AssigneeGitHubNodeIDs, []string{"A", "B"}) || !reflect.DeepEqual(got.RequestedReviewerGitHubNodeIDs, []string{"R2", "R1"}) {
		t.Fatalf("snapshot = %#v", got)
	}
	if gotCall := client.calls[0]; gotCall.method != "GET" || gotCall.endpoint != "/repos/acme/widgets/issues/7" {
		t.Fatalf("issue call = %#v", gotCall)
	}
	if gotCall := client.calls[1]; gotCall.endpoint != "/repos/acme/widgets/pulls/7/requested_reviewers?per_page=100&page=1" {
		t.Fatalf("review request call = %#v", gotCall)
	}
}

func TestAdapterReplacesReviewersByAddingThenRemovingAndLeavesTeamsAlone(t *testing.T) {
	client := &fakeClient{responses: []any{
		map[string]any{"users": []any{map[string]any{"login": "stale"}, map[string]any{"login": "kept"}}, "teams": []any{map[string]any{"slug": "do-not-touch"}}},
		map[string]any{"requested_reviewers": []any{map[string]any{"login": "kept"}, map[string]any{"login": "added"}}},
		nil,
	}}
	adapter := githubpullrequestparticipants.New(client)
	if err := adapter.ReplaceRequestedReviewers(context.Background(), pullrequestparticipants.ProviderTarget{RepositoryURL: "git@github.com:acme/widgets.git", PullRequestNumber: 8}, []string{"kept", "added"}); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 3 || client.calls[1].method != "POST" || client.calls[2].method != "DELETE" {
		t.Fatalf("calls = %#v", client.calls)
	}
	if !reflect.DeepEqual(client.calls[1].input, map[string]any{"reviewers": []string{"added"}}) {
		t.Fatalf("add payload = %#v", client.calls[1].input)
	}
	if !reflect.DeepEqual(client.calls[2].input, map[string]any{"reviewers": []string{"stale"}}) {
		t.Fatalf("remove payload = %#v", client.calls[2].input)
	}
}

func TestAdapterKeepsExistingReviewersWhenAddingReplacementFails(t *testing.T) {
	client := &fakeClient{
		responses: []any{
			map[string]any{"users": []any{map[string]any{"login": "stale"}, map[string]any{"login": "kept"}}},
		},
		methodErrors: map[string]error{"POST": errors.New("reviewer rejected")},
	}

	err := githubpullrequestparticipants.New(client).ReplaceRequestedReviewers(context.Background(), pullrequestparticipants.ProviderTarget{RepositoryURL: "https://github.com/acme/widgets", PullRequestNumber: 8}, []string{"kept", "added"})

	if !errors.Is(err, pullrequestparticipants.ErrProviderFailed) {
		t.Fatalf("error = %v", err)
	}
	if len(client.calls) != 2 || client.calls[0].method != "GET" || client.calls[1].method != "POST" {
		t.Fatalf("calls = %#v", client.calls)
	}
}

func TestAdapterRejectsSilentlyIgnoredAssigneeAndClassifiesUnavailable(t *testing.T) {
	t.Run("ignored", func(t *testing.T) {
		client := &fakeClient{responses: []any{map[string]any{"assignees": []any{map[string]any{"login": "accepted"}}}}}
		err := githubpullrequestparticipants.New(client).ReplaceAssignees(context.Background(), pullrequestparticipants.ProviderTarget{RepositoryURL: "https://github.com/acme/widgets", PullRequestNumber: 1}, []string{"ignored"})
		if !errors.Is(err, pullrequestparticipants.ErrProviderFailed) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		client := &fakeClient{err: &githubapi.Error{Code: githubapi.ErrorCodeGHUnavailable, Err: errors.New("missing gh")}}
		_, err := githubpullrequestparticipants.New(client).GetSnapshot(context.Background(), pullrequestparticipants.ProviderTarget{RepositoryURL: "https://github.com/acme/widgets", PullRequestNumber: 1})
		if !errors.Is(err, pullrequestparticipants.ErrProviderUnavailable) {
			t.Fatalf("error = %v", err)
		}
	})
}

type requestCall struct {
	method   string
	endpoint string
	input    any
}

type fakeClient struct {
	calls        []requestCall
	responses    []any
	methodErrors map[string]error
	err          error
}

func (client *fakeClient) Request(_ context.Context, method, endpoint string, input, output any) error {
	client.calls = append(client.calls, requestCall{method: method, endpoint: endpoint, input: input})
	if client.err != nil {
		return client.err
	}
	if err := client.methodErrors[method]; err != nil {
		return err
	}
	if len(client.responses) == 0 {
		return nil
	}
	response := client.responses[0]
	client.responses = client.responses[1:]
	if response == nil || output == nil {
		return nil
	}
	// JSON round-tripping keeps this fake independent of adapter-private DTOs.
	encoded, _ := json.Marshal(response)
	return json.Unmarshal(encoded, output)
}

func TestSnapshotRejectsMissingCollectionsAndReadsEveryReviewerPage(t *testing.T) {
	client := &fakeClient{responses: []any{map[string]any{"assignees": []any{}}, map[string]any{}}}
	target := pullrequestparticipants.ProviderTarget{RepositoryURL: "https://github.com/acme/widgets", PullRequestNumber: 7}
	if _, err := githubpullrequestparticipants.New(client).GetSnapshot(t.Context(), target); !errors.Is(err, pullrequestparticipants.ErrIncompleteObservation) {
		t.Fatal(err)
	}
	users := make([]any, 100)
	for i := range users {
		users[i] = map[string]any{"node_id": fmt.Sprintf("user-%d", i), "login": fmt.Sprintf("user-%d", i)}
	}
	client = &fakeClient{responses: []any{map[string]any{"assignees": []any{}}, map[string]any{"users": users}, map[string]any{"users": []any{map[string]any{"node_id": "last", "login": "last"}}}}}
	snapshot, err := githubpullrequestparticipants.New(client).GetSnapshot(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Complete || len(snapshot.RequestedReviewerGitHubNodeIDs) != 101 || len(client.calls) != 3 {
		t.Fatalf("snapshot=%+v calls=%d", snapshot, len(client.calls))
	}
}
