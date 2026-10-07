package holarkclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type IssueCommentAuthor struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	URL       string `json:"url"`
}
type IssueComment struct {
	ID           string             `json:"id"`
	IssueID      string             `json:"issue_id"`
	Body         string             `json:"body"`
	Author       IssueCommentAuthor `json:"author"`
	GitHubID     string             `json:"github_id"`
	GitHubNodeID string             `json:"github_node_id"`
	URL          string             `json:"url"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
	CanEdit      bool               `json:"can_edit"`
	CanDelete    bool               `json:"can_delete"`
}
type IssueDiscussion struct {
	Comments   []IssueComment `json:"comments"`
	SyncedAt   *time.Time     `json:"synced_at"`
	CanComment *bool          `json:"can_comment"`
}

func (c Client) ListIssueComments(ctx context.Context, id string) (IssueDiscussion, error) {
	var d IssueDiscussion
	err := c.do(ctx, http.MethodGet, "/api/v1/issues/"+url.PathEscape(id)+"/comments", nil, &d)
	return d, err
}
func (c Client) SyncIssueComments(ctx context.Context, id string) (IssueDiscussion, error) {
	var d IssueDiscussion
	err := c.do(ctx, http.MethodPost, "/api/v1/issues/"+url.PathEscape(id)+"/comments/sync", nil, &d)
	return d, err
}
func (c Client) CreateIssueComment(ctx context.Context, id, body string) (IssueComment, error) {
	var comment IssueComment
	err := c.do(ctx, http.MethodPost, "/api/v1/issues/"+url.PathEscape(id)+"/comments", map[string]string{"body": body}, &comment)
	return comment, commentWriteError(err)
}
func (c Client) UpdateIssueComment(ctx context.Context, id, body string) (IssueComment, error) {
	var comment IssueComment
	err := c.do(ctx, http.MethodPatch, "/api/v1/issue-comments/"+url.PathEscape(id), map[string]string{"body": body}, &comment)
	return comment, commentWriteError(err)
}
func (c Client) DeleteIssueComment(ctx context.Context, id string) error {
	return commentWriteError(c.do(ctx, http.MethodDelete, "/api/v1/issue-comments/"+url.PathEscape(id), nil, nil))
}

// A lost local HTTP response also makes a comment write uncertain. Preserve
// structured server outcomes and never suggest an automatic retry of creation.
func commentWriteError(err error) error {
	if err == nil {
		return nil
	}
	var apiError APIError
	if errors.As(err, &apiError) {
		return err
	}
	return APIError{Code: "issue_comment_outcome_uncertain", Message: fmt.Sprintf("The comment change may have reached GitHub (%v). Sync the discussion and check GitHub before retrying.", err), ReconciliationRequired: true, SafeToRetry: false}
}
