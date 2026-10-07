package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"github.com/holark-ai/holark/internal/issues"
	"github.com/holark-ai/holark/internal/issues/comments"
	"github.com/holark-ai/holark/internal/issues/workflow"
)

type commentClientStub struct {
	pages            []string
	graphCalls       int
	mutationCalls    int
	mutationError    error
	graphError       error
	mutationResponse string
	query            string
	variables        map[string]any
	method           string
	endpoint         string
	body             string
}

func (s *commentClientStub) GraphQL(_ context.Context, _ string, vars map[string]any, out any) error {
	s.graphCalls++
	if s.graphError != nil {
		return s.graphError
	}
	if s.graphCalls > len(s.pages) {
		return errors.New("page fetch failed")
	}
	return json.Unmarshal([]byte(s.pages[s.graphCalls-1]), out)
}
func (s *commentClientStub) RequestMutation(_ context.Context, method, endpoint string, input, out any) error {
	s.mutationCalls++
	s.method = method
	s.endpoint = endpoint
	payload := input.(map[string]any)
	s.query = payload["query"].(string)
	s.variables = payload["variables"].(map[string]any)
	s.body, _ = s.variables["body"].(string)
	if s.mutationError != nil {
		return s.mutationError
	}
	response := s.mutationResponse
	if response == "" {
		response = `{"data":{"result":{"comment":` + graphNode + `}}}`
		if strings.Contains(s.query, "result:addComment") {
			response = `{"data":{"result":{"commentEdge":{"node":` + graphNode + `}}}}`
		} else if strings.Contains(s.query, "result:deleteIssueComment") {
			response = `{"data":{"result":{"clientMutationId":null}}}`
		}
	}
	return json.Unmarshal([]byte(response), out)
}

const graphNode = `{"id":"node123","fullDatabaseId":"123","body":"> original\n\nbody","url":"https://github.com/o/r/issues/7#issuecomment-123","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z","viewerCanUpdate":true,"viewerCanDelete":false,"author":{"login":"outsider","avatarUrl":"https://example.com/avatar","url":"https://github.com/outsider"}}`

func commentPage(node string, next bool, cursor string) string {
	return fmt.Sprintf(`{"data":{"repository":{"issue":{"locked":false,"comments":{"nodes":[%s],"pageInfo":{"hasNextPage":%t,"endCursor":%q}}}}}}`, node, next, cursor)
}
func commentIssue() issues.Issue {
	return issues.Issue{ID: "local-issue", SyncData: json.RawMessage(`{"github":{"number":7,"node_id":"issue7"}}`)}
}
func TestDiscussionPaginationAndDeletedAuthor(t *testing.T) {
	deleted := strings.Replace(graphNode, `"author":{"login":"outsider","avatarUrl":"https://example.com/avatar","url":"https://github.com/outsider"}`, `"author":null`, 1)
	client := &commentClientStub{pages: []string{commentPage(graphNode, true, "next"), commentPage(deleted, false, "")}}
	result, can, err := New(client).ListComments(t.Context(), commentIssue(), "https://github.com/o/r")
	if err != nil || can != nil || len(result) != 2 || result[0].Author.Login != "outsider" || result[1].Author.Login != "" || result[0].Body != "> original\n\nbody" || !result[0].CanEdit || result[0].CanDelete {
		t.Fatalf("snapshot: %+v %v", result, err)
	}
	if client.graphCalls != 2 {
		t.Fatal("did not fetch all pages")
	}
}
func TestDiscussionIncompleteFetchReturnsNoSnapshot(t *testing.T) {
	for _, pages := range [][]string{{commentPage(graphNode, true, "next")}, {commentPage(graphNode, true, "next"), commentPage(graphNode, true, "next")}, {`{"data":{"repository":null}}`}, {`{"errors":[{"message":"denied"}],"data":null}`}} {
		result, _, err := New(&commentClientStub{pages: pages}).ListComments(t.Context(), commentIssue(), "https://github.com/o/r")
		if err == nil || result != nil {
			t.Fatalf("partial snapshot returned: %+v %v", result, err)
		}
	}
}
func TestCommentMutationsReturnCanonicalFieldsWithoutFollowupRead(t *testing.T) {
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			client := &commentClientStub{}
			p := New(client)
			var c comments.Comment
			var err error
			switch method {
			case "POST":
				c, err = p.CreateComment(t.Context(), commentIssue(), "https://github.com/o/r", "submitted")
			case "PATCH":
				c, err = p.UpdateComment(t.Context(), commentIssue(), "https://github.com/o/r", comments.Comment{GitHubID: "123", GitHubNodeID: "node123"}, "submitted")
			case "DELETE":
				err = p.DeleteComment(t.Context(), commentIssue(), "https://github.com/o/r", comments.Comment{GitHubID: "123", GitHubNodeID: "node123"})
			}
			if err != nil || client.method != "POST" || client.endpoint != "graphql" || client.mutationCalls != 1 || client.graphCalls != 0 {
				t.Fatalf("mutation %s: %v %+v", method, err, client)
			}
			operation, id := "addComment", "issue7"
			if method == "PATCH" {
				operation, id = "updateIssueComment", "node123"
			}
			if method == "DELETE" {
				operation, id = "deleteIssueComment", "node123"
			}
			if !strings.Contains(client.query, "result:"+operation+"(") || client.variables["id"] != id {
				t.Fatalf("wrong mutation target: %s %+v", client.query, client.variables)
			}
			if method != "DELETE" && (c.Body != "\u003e original\n\nbody" || !c.CanEdit || c.CanDelete || client.body != "submitted") {
				t.Fatalf("canonical: %+v", c)
			}
		})
	}
}
func TestCommentMutationFailureClassificationAndNoRetry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		response  string
		pending   bool
		uncertain bool
	}{
		{name: "rejected", err: &githubapi.RejectionError{Status: 422, Message: "body too long"}},
		{name: "uncertain", err: &githubapi.Error{Code: githubapi.ErrorCodeMutationUncertain, Err: errors.New("EOF")}, uncertain: true},
		{name: "malformed HTTP success", err: &githubapi.Error{Code: githubapi.ErrorCodeMutationAccepted, Err: errors.New("bad JSON")}, uncertain: true},
		{name: "incomplete accepted comment", response: `{"data":{"result":{"comment":{"id":"node123","fullDatabaseId":"123"}}}}`, pending: true},
		{name: "missing result", response: `{"data":null}`, uncertain: true},
		{name: "provider failure", response: `{"errors":[{"type":"INTERNAL","message":"internal error"}],"data":{"result":null}}`, uncertain: true},
		{name: "permission denied", response: `{"errors":[{"type":"FORBIDDEN","message":"permission denied"}],"data":{"result":null}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &commentClientStub{mutationError: tc.err, mutationResponse: tc.response}
			_, err := New(client).CreateComment(t.Context(), commentIssue(), "https://github.com/o/r", "body")
			var outcome *workflow.CommentMutationError
			if tc.pending || tc.uncertain {
				if !errors.As(err, &outcome) || (outcome.Outcome == workflow.CommentAccepted) != tc.pending || (outcome.Outcome == workflow.CommentUncertain) != tc.uncertain {
					t.Fatalf("outcome: %v", err)
				}
			} else {
				var rejected *workflow.CommentRejectedError
				if !errors.As(err, &rejected) || rejected.Status != 422 {
					t.Fatalf("rejection: %v", err)
				}
			}
			if client.mutationCalls != 1 {
				t.Fatal("creation was retried")
			}
		})
	}
}

func TestLargeCommentIdentities(t *testing.T) {
	var node graphComment
	if err := json.Unmarshal([]byte(strings.Replace(graphNode, `"123"`, `"5123456789"`, 1)), &node); err != nil {
		t.Fatal(err)
	}
	c, err := node.local("issue")
	if err != nil || c.GitHubID != "5123456789" {
		t.Fatalf("large identity: %+v %v", c, err)
	}
}

func TestCommentCreateResolvesLegacyIssueNodeBeforeWriting(t *testing.T) {
	issue := commentIssue()
	issue.SyncData = json.RawMessage(`{"github":{"number":7}}`)
	client := &commentClientStub{pages: []string{`{"data":{"repository":{"issue":{"id":"legacy7"}}}}`}}
	if _, err := New(client).CreateComment(t.Context(), issue, "https://github.com/o/r", "body"); err != nil {
		t.Fatal(err)
	}
	if client.graphCalls != 1 || client.mutationCalls != 1 || client.variables["id"] != "legacy7" {
		t.Fatalf("legacy lookup: %+v", client)
	}
	client = &commentClientStub{graphError: errors.New("offline")}
	if _, err := New(client).CreateComment(t.Context(), issue, "https://github.com/o/r", "body"); err == nil || client.mutationCalls != 0 {
		t.Fatal("failed identity lookup must not publish")
	}
}

func TestUnavailableRepositoryDisablesPosting(t *testing.T) {
	for _, field := range []string{"isArchived", "isDisabled"} {
		page := strings.Replace(commentPage(graphNode, false, ""), `"repository":{`, `"repository":{"`+field+`":true,`, 1)
		_, can, err := New(&commentClientStub{pages: []string{page}}).ListComments(t.Context(), commentIssue(), "https://github.com/o/r")
		if err != nil || can == nil || *can {
			t.Fatalf("%s: can=%v err=%v", field, can, err)
		}
	}
}
