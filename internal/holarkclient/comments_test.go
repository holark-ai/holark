package holarkclient

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCommentClientKeepsRecoveryOutcomesAndReferences(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body, code string
	}{
		{202, `{"code":"issue_comment_projection_pending","message":"Sync before retrying","github_comment_id":"123","github_comment_url":"https://github.com/comment","reconciliation_required":true,"safe_to_retry":false}`, "issue_comment_projection_pending"},
		{502, `{"code":"issue_comment_outcome_uncertain","message":"Sync before retrying","github_comment_id":"123","github_comment_url":"https://github.com/comment","reconciliation_required":true,"safe_to_retry":false}`, "issue_comment_outcome_uncertain"},
		{201, `{"id":`, "issue_comment_outcome_uncertain"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		c, err := (Client{BaseURL: server.URL}).CreateIssueComment(t.Context(), "issue", "body")
		server.Close()
		var e APIError
		if !errors.As(err, &e) || e.Code != tc.code || e.SafeToRetry || !e.ReconciliationRequired || c.ID != "" {
			t.Fatalf("result=%+v error=%+v", c, err)
		}
		if tc.status != 201 && (e.GitHubCommentID != "123" || e.GitHubCommentURL != "https://github.com/comment") {
			t.Fatalf("missing reference: %+v", e)
		}
	}
}
