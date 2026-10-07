package pullrequestfixture

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Seed review data before the first PR read, so the initial page is populated
// even while description generation is still running.
func (scenario *Scenario) reviewHandler(next http.Handler) http.Handler {
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/pull-requests/") {
			id := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/pull-requests/"), "/")[0]
			mu.Lock()
			err := scenario.seedReview(r.Context(), id)
			mu.Unlock()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (scenario *Scenario) seedReview(ctx context.Context, id string) error {
	tx, err := scenario.Application.Database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var head string
	if err := tx.QueryRowContext(ctx, `select head_commit from pull_requests where id = ?`, id).Scan(&head); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	assigneeID, reviewerID := "scenario-assignee-"+id, "scenario-reviewer-"+id
	// The seeded member survives comment deletion and participant removal. Its
	// presence marks this transaction as complete, including after a restart.
	var seeded bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from github_repository_members where id = ?)`, assigneeID).Scan(&seeded); err != nil || seeded {
		return err
	}
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	for _, member := range []struct{ id, login string }{{assigneeID, "alex-scenario"}, {reviewerID, "sam-scenario"}} {
		if _, err := tx.ExecContext(ctx, `insert into github_repository_members
			(id, repository_id, github_node_id, login, permission, last_seen_at, created_at, updated_at)
			values (?, ?, ?, ?, 'write', ?, ?, ?)`, member.id, scenario.Application.RepositoryID, member.id, member.login, stamp, stamp, stamp); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into pull_request_assignees(pull_request_id, holark_id, position) values (?, ?, 0)`, id, assigneeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into pull_request_requested_reviewers(pull_request_id, holark_id, position) values (?, ?, 0)`, id, reviewerID); err != nil {
		return err
	}
	comments := []struct {
		key, parent, body, status string
	}{
		{"welcome", "", "The five commits are ready for review. Please check the checklist filtering and narrow-screen layout, then follow the repeated rebase walkthrough.", "resolved"},
		{"filter", "", "Could we confirm that completed items stay checked when switching between Overview and Changes in the review checklist?", "unresolved"},
		{"filter-reply", "filter", "The checklist keeps completion state while filters change. I’ll verify the keyboard flow before resolving this conversation.", "unresolved"},
		{"layout", "", "The completion counter was hard to spot on a narrow screen. Can we give it a little more emphasis?", "resolved"},
		{"layout-reply", "layout", "Updated the counter styling and checked the single-column layout. This is ready for another look.", "resolved"},
	}
	for index, comment := range comments {
		createdAt := now.Add(time.Duration(index-len(comments)) * 10 * time.Minute).Format(time.RFC3339Nano)
		var parent, resolvedAt any
		if comment.parent != "" {
			parent = "scenario-" + id + "-" + comment.parent
		}
		if comment.status == "resolved" {
			resolvedAt = stamp
		}
		if _, err := tx.ExecContext(ctx, `insert into pull_request_comments
			(id, pull_request_id, parent_comment_id, body, scope, original_head_commit, status, author_type, publication_state, created_at, updated_at, resolved_at)
			values (?, ?, ?, ?, 'pull_request', ?, ?, 'user', 'local', ?, ?, ?)`,
			"scenario-"+id+"-"+comment.key, id, parent, comment.body, head, comment.status, createdAt, createdAt, resolvedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
