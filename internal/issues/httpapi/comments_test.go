package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/issues/comments"
	"github.com/holark-ai/holark/internal/issues/workflow"
)

type commentWorkflowStub struct {
	*fakeWorkflow
	body string
	id   string
	err  error
}

func (s *commentWorkflowStub) ListComments(context.Context, string) (comments.Discussion, error) {
	return comments.Discussion{Comments: []comments.Comment{}}, s.err
}
func (s *commentWorkflowStub) SyncComments(context.Context, string) (comments.Discussion, error) {
	return comments.Discussion{Comments: []comments.Comment{}}, s.err
}
func (s *commentWorkflowStub) CreateComment(_ context.Context, id, body string) (comments.Comment, error) {
	s.id = id
	s.body = body
	return comments.Comment{ID: "comment", Body: body}, s.err
}
func (s *commentWorkflowStub) UpdateComment(ctx context.Context, id, body string) (comments.Comment, error) {
	return s.CreateComment(ctx, id, body)
}
func (s *commentWorkflowStub) DeleteComment(_ context.Context, id string) error {
	s.id = id
	return s.err
}
func TestCommentRoutesAndRecoveryResponses(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/api/v1/issues/issue/comments", "", 200},
		{"POST", "/api/v1/issues/issue/comments/sync", "", 200},
		{"POST", "/api/v1/issues/issue/comments", `{"body":"> quote\n\nbody"}`, 201},
		{"PATCH", "/api/v1/issue-comments/comment", `{"body":"edit"}`, 200},
		{"DELETE", "/api/v1/issue-comments/comment", "", 204},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			s := &commentWorkflowStub{fakeWorkflow: &fakeWorkflow{}}
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			routes(s).ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if tc.status == 201 && s.body != "> quote\n\nbody" {
				t.Fatal("Markdown rewritten")
			}
			if tc.status == 204 && w.Body.Len() != 0 {
				t.Fatal("delete should be empty")
			}
		})
	}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{&workflow.CommentMutationError{Outcome: workflow.CommentAccepted, GitHubID: "123", URL: "https://github.com/comment", Err: errors.New("disk")}, 202, "issue_comment_projection_pending"},
		{&workflow.CommentMutationError{Outcome: workflow.CommentUncertain, Err: errors.New("EOF")}, 502, "issue_comment_outcome_uncertain"},
		{&workflow.CommentRejectedError{Status: 403, Message: "GitHub rejected"}, 403, "issue_comment_rejected"},
		{&workflow.CommentRejectedError{Status: 401, Message: "Bad GitHub credentials"}, 502, "issue_comment_rejected"},
		{comments.ErrInvalidBody, 400, "invalid_request"},
		{comments.ErrNotFound, 404, "issue_comment_not_found"},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/issues/issue/comments", strings.NewReader(`{"body":"body"}`))
		r.Header.Set("Content-Type", "application/json")
		routes(&commentWorkflowStub{fakeWorkflow: &fakeWorkflow{}, err: tc.err}).ServeHTTP(w, r)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("error=%v status=%d body=%s", tc.err, w.Code, w.Body)
		}
	}
}
