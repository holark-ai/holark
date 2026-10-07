package pullrequestfixture

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

// completeReviewWork simulates the external agent while retaining real work
// completion, comment delivery, Git publication, and head invalidation.
func (agents *RebaseAgents) completeReviewWork(ctx context.Context, h holons.Holon, work pullrequestwork.Work) error {
	completion := pullrequestwork.Completion{PullRequestID: work.PullRequestID, HeadCommit: work.HeadCommit}
	if work.Kind == pullrequestwork.KindReview {
		completion.Summary = "Review complete: three comments to address."
		completion.Comments = []pullrequestwork.ReviewComment{
			{Body: "Keep completed checklist items checked when changing filters.", Scope: "pull_request"},
			{Body: "Make the completion counter readable on narrow screens.", Scope: "pull_request"},
			{Body: "Document the keyboard navigation for the review checklist.", Scope: "pull_request"},
		}
	} else {
		directory := filepath.Join(h.WorktreePath, "scenario-resolutions")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
		name := filepath.Join("scenario-resolutions", work.ID+".txt")
		if err := os.WriteFile(filepath.Join(h.WorktreePath, name), []byte("Applied the requested checklist improvement.\n"), 0o600); err != nil {
			return err
		}
		for _, args := range [][]string{
			{"add", name},
			{"-c", "user.name=Holark Scenario", "-c", "user.email=scenario@invalid", "commit", "-m", "Address review checklist feedback"},
		} {
			if _, err := gitOutputContext(ctx, h.WorktreePath, args...); err != nil {
				return err
			}
		}
		head, err := gitOutputContext(ctx, h.WorktreePath, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		completion.ResultHeadCommit = strings.TrimSpace(head)
		completion.ReplyBody = "Applied the requested checklist improvement and committed the result."
	}
	if _, err := agents.application.PullRequestWork.Complete(ctx, work.ID, work.HeadCommit, completion); err != nil {
		return err
	}
	if work.CommentID != "" {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/pull-request-comments/"+work.CommentID+"/resolve", nil).WithContext(ctx)
		agents.application.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			return fmt.Errorf("resolve scenario comment: %d: %s", response.Code, response.Body.String())
		}
	}
	return nil
}
