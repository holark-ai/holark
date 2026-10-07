package pullrequests

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

type workStateAPI struct {
	APIClient
	result githubapi.PullRequest
	calls  int
}

func (c *workStateAPI) PullRequestWorkState(context.Context, githubapi.Repository, int) (githubapi.PullRequest, error) {
	c.calls++
	return c.result, nil
}

// Provider conversion is tested at its boundary: the API decoder has separate
// CLI coverage, and embedding no other implementation catches extra requests.
func TestGetWorkStateNormalizesAndRequiresMatchingIdentity(t *testing.T) {
	for _, scenario := range []string{"valid", "node", "number", "external ID", "base repository", "head repository", "missing branch", "missing commit"} {
		t.Run(scenario, func(t *testing.T) {
			at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			client := &workStateAPI{result: githubapi.PullRequest{Number: 1, NodeID: "PR_1", Title: "  Title  ", Body: "Description", State: "open", Draft: true, Base: githubapi.Ref{Ref: "main", SHA: "base", Repository: &githubapi.RefRepository{FullName: "owner/repo", CloneURL: "https://github.com/owner/repo.git"}}, Head: githubapi.Ref{Ref: "main", SHA: "head", Repository: &githubapi.RefRepository{FullName: "fork/repo", CloneURL: "https://github.com/fork/repo.git"}}, UpdatedAt: at}}
			target := pr.GitHubPullRequestTarget{RepositoryURL: "git@github.com:owner/repo.git", ExternalID: "github:owner/repo#1", SyncData: json.RawMessage(`{"github":{"node_id":"PR_1","number":1}}`)}
			switch scenario {
			case "node":
				client.result.NodeID = "other"
			case "number":
				client.result.Number = 2
			case "external ID":
				target.ExternalID = "github:other/repo#1"
			case "base repository":
				client.result.Base.Repository = nil
			case "head repository":
				client.result.Head.Repository = &githubapi.RefRepository{}
			case "missing branch":
				client.result.Head.Ref = ""
			case "missing commit":
				client.result.Base.SHA = ""
			}
			result, err := New(client).GetWorkState(t.Context(), target)
			if client.calls != 1 {
				t.Fatalf("calls=%d", client.calls)
			}
			if scenario != "valid" {
				if err == nil {
					t.Fatalf("accepted mismatch: %+v", result)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			evidence := pr.DecodeGitHubObservation(result.SyncData)
			if result.Title != "Title" || result.Summary != "Description" || result.Status != pr.StatusDraft || evidence.HeadRepositoryURL != "https://github.com/fork/repo.git" || !result.UpdatedAt.Equal(at) {
				t.Fatalf("normalized observation=%+v evidence=%+v", result, evidence)
			}
		})
	}
}
