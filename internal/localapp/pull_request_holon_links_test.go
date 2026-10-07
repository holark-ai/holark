package localapp

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

type pullRequestLinkCatalogStub struct {
	repositoryID string
	pullRequests []pullrequestlifecycle.PullRequest
}

func (catalog *pullRequestLinkCatalogStub) ListPullRequests(repositoryID string) []pullrequestlifecycle.PullRequest {
	catalog.repositoryID = repositoryID
	return catalog.pullRequests
}

type pullRequestActivityStub struct {
	activity map[string][]holons.Holon
	err      error
}

func (stub pullRequestActivityStub) PullRequestActivity(_ context.Context, pullRequestID string) ([]holons.Holon, error) {
	return stub.activity[pullRequestID], stub.err
}

func TestLocalPullRequestHolonLinksProjectsAllActivityWithoutDuplicates(t *testing.T) {
	catalog := &pullRequestLinkCatalogStub{pullRequests: []pullrequestlifecycle.PullRequest{
		{ID: "pr-two", LinkedHolonIDs: []string{"feature-two"}},
		{ID: "pr-one", LinkedHolonIDs: []string{"feature-one", "shared", ""}},
	}}
	projection := localPullRequestHolonLinks{catalog: catalog, holons: pullRequestActivityStub{activity: map[string][]holons.Holon{
		"pr-one": {
			{ID: "review", PullRequestID: "pr-one"},
			{ID: "worker", PullRequestID: "pr-one"},
			{ID: "rebase", PullRequestID: "pr-one"},
			{ID: "metadata", PullRequestID: "pr-one"},
			{ID: "shared", PullRequestID: "pr-one"},
			{ID: "wrong-owner", PullRequestID: "pr-two"},
		},
	}}}

	got, err := projection.List(t.Context(), "repository-one")
	if err != nil {
		t.Fatal(err)
	}
	want := []pullrequestlifecycle.HolonLink{
		{PullRequestID: "pr-one", HolonID: "feature-one"},
		{PullRequestID: "pr-one", HolonID: "metadata"},
		{PullRequestID: "pr-one", HolonID: "rebase"},
		{PullRequestID: "pr-one", HolonID: "review"},
		{PullRequestID: "pr-one", HolonID: "shared"},
		{PullRequestID: "pr-one", HolonID: "worker"},
		{PullRequestID: "pr-two", HolonID: "feature-two"},
	}
	if catalog.repositoryID != "repository-one" || !reflect.DeepEqual(got, want) {
		t.Fatalf("repository=%q links=%#v", catalog.repositoryID, got)
	}
}

func TestLocalPullRequestHolonLinksReturnsActivityFailure(t *testing.T) {
	want := errors.New("activity unavailable")
	projection := localPullRequestHolonLinks{
		catalog: &pullRequestLinkCatalogStub{pullRequests: []pullrequestlifecycle.PullRequest{{ID: "pr-one"}}},
		holons:  pullRequestActivityStub{err: want},
	}
	if _, err := projection.List(t.Context(), "repository-one"); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
}
