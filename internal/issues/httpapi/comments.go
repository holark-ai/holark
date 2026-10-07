package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/holark-ai/holark/internal/issues/comments"
	"github.com/holark-ai/holark/internal/issues/workflow"
)

type CommentWorkflow interface {
	ListComments(context.Context, string) (comments.Discussion, error)
	SyncComments(context.Context, string) (comments.Discussion, error)
	CreateComment(context.Context, string, string) (comments.Comment, error)
	UpdateComment(context.Context, string, string) (comments.Comment, error)
	DeleteComment(context.Context, string) error
}

func (h *handler) commentWorkflow(w http.ResponseWriter) (CommentWorkflow, bool) {
	c, ok := h.options.Workflow.(CommentWorkflow)
	if !ok {
		writeError(w, 503, "issue_comments_unavailable", "Issue comments are unavailable.")
	}
	return c, ok
}
func (h *handler) listComments(w http.ResponseWriter, r *http.Request) { h.readComments(w, r, false) }
func (h *handler) syncComments(w http.ResponseWriter, r *http.Request) { h.readComments(w, r, true) }
func (h *handler) readComments(w http.ResponseWriter, r *http.Request, sync bool) {
	c, ok := h.commentWorkflow(w)
	if !ok {
		return
	}
	var d comments.Discussion
	var err error
	if sync {
		d, err = c.SyncComments(r.Context(), r.PathValue("id"))
	} else {
		d, err = c.ListComments(r.Context(), r.PathValue("id"))
	}
	if err != nil {
		writeCommentError(w, err)
		return
	}
	writeJSON(w, 200, d)
}
func (h *handler) createComment(w http.ResponseWriter, r *http.Request) { h.writeComment(w, r, true) }
func (h *handler) updateComment(w http.ResponseWriter, r *http.Request) { h.writeComment(w, r, false) }
func (h *handler) writeComment(w http.ResponseWriter, r *http.Request, create bool) {
	if !requireJSON(w, r) {
		return
	}
	var request struct {
		Body string `json:"body"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	c, ok := h.commentWorkflow(w)
	if !ok {
		return
	}
	var out comments.Comment
	var err error
	status := http.StatusOK
	if create {
		out, err = c.CreateComment(r.Context(), r.PathValue("id"), request.Body)
		status = 201
	} else {
		out, err = c.UpdateComment(r.Context(), r.PathValue("id"), request.Body)
	}
	if err != nil {
		writeCommentError(w, err)
		return
	}
	writeJSON(w, status, out)
}
func (h *handler) deleteComment(w http.ResponseWriter, r *http.Request) {
	c, ok := h.commentWorkflow(w)
	if !ok {
		return
	}
	if err := c.DeleteComment(r.Context(), r.PathValue("id")); err != nil {
		writeCommentError(w, err)
		return
	}
	w.WriteHeader(204)
}
func writeCommentError(w http.ResponseWriter, err error) {
	var outcome *workflow.CommentMutationError
	var rejected *workflow.CommentRejectedError
	switch {
	case errors.As(err, &outcome):
		status, code, message := 202, "issue_comment_projection_pending", "GitHub accepted the comment change. Refresh the discussion or run holark issue comment sync ISSUE_ID before taking further action."
		if outcome.Outcome == workflow.CommentUncertain {
			status, code, message = 502, "issue_comment_outcome_uncertain", "The comment change may have reached GitHub. Refresh the discussion or check GitHub before retrying; do not automatically repost."
		}
		writeJSON(w, status, map[string]any{"code": code, "message": message, "github_comment_id": outcome.GitHubID, "github_comment_url": outcome.URL, "reconciliation_required": true, "safe_to_retry": false})
	case errors.As(err, &rejected):
		status := rejected.Status
		if status < 400 || status >= 500 {
			status = 422
		}
		// GitHub authentication failures must not invalidate the local Holark session.
		if status == http.StatusUnauthorized {
			status = http.StatusBadGateway
		}
		writeError(w, status, "issue_comment_rejected", rejected.Message)
	case errors.Is(err, comments.ErrInvalidBody):
		writeError(w, 400, "invalid_request", err.Error())
	case errors.Is(err, comments.ErrNotFound):
		writeError(w, 404, "issue_comment_not_found", err.Error())
	default:
		writeWorkflowError(w, err, "The discussion could not be updated.")
	}
}
