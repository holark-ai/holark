package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

type projectedHolonLinks struct {
	links        []pullrequestlifecycle.HolonLink
	repositoryID string
}

func (links *projectedHolonLinks) List(_ context.Context, repositoryID string) ([]pullrequestlifecycle.HolonLink, error) {
	links.repositoryID = repositoryID
	return links.links, nil
}

func TestHolonLinksUsesCompleteProjection(t *testing.T) {
	links := &projectedHolonLinks{links: []pullrequestlifecycle.HolonLink{
		{PullRequestID: "pr-1", HolonID: "feature"},
		{PullRequestID: "pr-1", HolonID: "review"},
		{PullRequestID: "pr-1", HolonID: "worker"},
	}}
	handler := pullRequestHTTP(Options{RepositoryID: "repo", HolonLinks: links})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/pull-request-holon-links", nil))
	if response.Code != http.StatusOK || links.repositoryID != "repo" {
		t.Fatalf("status=%d repository=%q body=%s", response.Code, links.repositoryID, response.Body.String())
	}
	var got []pullrequestlifecycle.HolonLink
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, links.links) {
		t.Fatalf("links=%#v", got)
	}
}
