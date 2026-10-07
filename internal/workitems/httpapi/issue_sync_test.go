package httpapi_test

import (
	"context"
	"testing"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	githubissues "github.com/holark-ai/holark/internal/codehost/github/issues"
	"github.com/holark-ai/holark/internal/githubidentity"
	issueworkflow "github.com/holark-ai/holark/internal/issues/workflow"
)

type issueSource struct{}

func (issueSource) ListIssues(context.Context, githubapi.Repository) ([]githubapi.Issue, error) {
	return []githubapi.Issue{{Number: 1, NodeID: "issue-node", Title: "New contributor", State: "open", User: githubapi.User{NodeID: "new-author", Login: "author"}, Assignees: []githubapi.User{{NodeID: "U_me", Login: "Alice"}, {NodeID: "new-assignee", Login: "assignee"}}}}, nil
}
func TestIssueSnapshotObservesUnseenIdentitiesBeforeResolving(t *testing.T) {
	f := setup(t)
	service := issueworkflow.New(func(string) (issueworkflow.Project, bool) {
		return issueworkflow.Project{ID: "repo", RepositoryURL: "https://github.com/owner/repo"}, true
	}, githubissues.New(issueSource{}), f.issues, githubidentity.NewService(f.members, nil))
	result, err := service.Sync(t.Context(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Issues) != 1 || result.Issues[0].IssuerHolarkID == "" || len(result.Issues[0].AssigneeHolarkIDs) != 2 {
		t.Fatal(result)
	}
	queue := f.get(t, "/api/v1/my-work")
	if queue.Counts["assigned_issues"] != 1 {
		t.Fatal(queue)
	}
}
