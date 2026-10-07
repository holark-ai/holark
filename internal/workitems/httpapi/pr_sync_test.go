package httpapi_test

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	githubprs "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	"github.com/holark-ai/holark/internal/githubidentity"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestparticipants"
)

// Unused provider operations deliberately remain unavailable. Bulk sync must
// obtain collaboration metadata from the listing itself.
type listingClient struct {
	githubprs.APIClient
	items []githubapi.PullRequest
	calls int
}

func (c *listingClient) ListPullRequests(context.Context, githubapi.Repository) ([]githubapi.PullRequest, error) {
	c.calls++
	return c.items, nil
}
func (c *listingClient) ListActivePullRequests(context.Context, githubapi.Repository) ([]githubapi.PullRequest, error) {
	c.calls++
	return c.items, nil
}

type catalogTargets struct{ f *fixture }

func (c catalogTargets) GetParticipantTarget(_ context.Context, id string) (pullrequestparticipants.PullRequestTarget, error) {
	p, ok := c.f.catalog.GetPullRequest(id)
	if !ok {
		return pullrequestparticipants.PullRequestTarget{}, pullrequestparticipants.ErrPullRequestNotFound
	}
	var data struct {
		GitHub struct {
			Number int `json:"number"`
		} `json:"github"`
	}
	if err := json.Unmarshal(p.SyncData, &data); err != nil {
		return pullrequestparticipants.PullRequestTarget{}, err
	}
	return pullrequestparticipants.PullRequestTarget{ID: p.ID, RepositoryID: p.RepositoryID, Status: pullrequestparticipants.PullRequestStatus(p.Status), SyncProvider: p.SyncProvider, RepositoryURL: "https://github.com/owner/repo", ProviderPullRequest: data.GitHub.Number}, nil
}
func TestBulkSyncProjectsMetadataAfterCatalogImportWithoutParticipantFetches(t *testing.T) {
	f := setup(t)
	f.add(t, "human-pr", "repo", pr.StatusOpen, 43)
	if _, err := f.participants.ReplaceSnapshot(t.Context(), pullrequestparticipants.Snapshot{PullRequestID: "human-pr", AuthorHolarkID: f.me}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	client := &listingClient{items: []githubapi.PullRequest{{Number: 42, NodeID: "PR_42", Title: "Imported assignment", State: "open", CreatedAt: now, UpdatedAt: now, Base: githubapi.Ref{Ref: "main", SHA: "base"}, Head: githubapi.Ref{Ref: "topic", SHA: "head"}, User: githubapi.User{NodeID: "author-node", Login: "dependabot[bot]"}, Assignees: []githubapi.User{{NodeID: "U_me", Login: "Alice"}}, RequestedReviewers: []githubapi.User{{NodeID: "U_me", Login: "Alice"}}}}}
	provider := githubprs.New(client)
	participants := pullrequestparticipants.NewService(f.participants, catalogTargets{f}, githubidentity.NewService(f.members, nil), nil)
	scheduler := pr.NewRefreshCoordinator()
	defer scheduler.Close()
	lifecycle := pr.New(f.catalog, pr.Options{Refresh: scheduler, Projects: pr.ProjectLookupFunc(func(id string) (pr.Project, bool) {
		return pr.Project{ID: "repo", RepositoryURL: "https://github.com/owner/repo", GitHubBacked: true}, id == "repo"
	}), GitHubTransport: provider, GitHubCodec: provider, Participants: participants, SyncRecorder: f.work})
	for _, active := range []bool{false, true} {
		var err error
		if active {
			_, err = lifecycle.SyncActive(t.Context(), "repo")
		} else {
			_, err = lifecycle.Sync(t.Context(), "repo")
		}
		if err != nil {
			t.Fatal(err)
		}
		r := f.get(t, "/api/v1/my-work")
		if r.Total != 1 || r.Counts["review_requested"] != 1 || r.Rows[0].AuthorID == "" {
			t.Fatal(r)
		}
	}
	members, err := f.members.ListProjectMembers(t.Context(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	var botLogin, botID string
	for _, member := range members {
		if member.Login == "dependabot[bot]" {
			botLogin, botID = member.Login, member.ID
		}
	}
	if botLogin == "" {
		t.Fatal("imported bot author missing from filter options")
	}
	if client.calls != 2 {
		t.Fatalf("unexpected remote calls: %d", client.calls)
	}
	for i := 0; i < 3; i++ {
		r := f.get(t, "/api/v1/pull-requests/search?q="+url.QueryEscape("author:"+botLogin))
		if r.Total != 1 || len(r.Rows) != 1 || r.Rows[0].Number != 42 || r.Rows[0].AuthorID != botID || r.Query != "author:dependabot[bot] is:active sort:created-desc" {
			t.Fatalf("bot author filter: %+v", r)
		}
		f.get(t, "/api/v1/my-work?view=review_requested")
	}
	if client.calls != 2 {
		t.Fatal("browsing invoked provider")
	}
	client.items[0].Assignees = []githubapi.User{}
	client.items[0].RequestedReviewers = []githubapi.User{}
	if _, err := lifecycle.SyncActive(t.Context(), "repo"); err != nil {
		t.Fatal(err)
	}
	if r := f.get(t, "/api/v1/my-work"); r.Total != 0 {
		t.Fatal(r)
	}
}
