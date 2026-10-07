//go:build integration

package pullrequestcomments_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	"github.com/holark-ai/holark/internal/pullrequestcomments/sqliteadapter"
	_ "modernc.org/sqlite"
)

type targetReader struct {
	targets map[string]pullrequestcomments.PullRequestTarget
}

type interruptingRepository struct {
	pullrequestcomments.Repository
	inserts int
	failAt  int
	failing bool
}

func (repository *interruptingRepository) Insert(ctx context.Context, comment pullrequestcomments.Comment) error {
	if repository.failing && repository.inserts == repository.failAt {
		return errors.New("interrupted insert")
	}
	repository.inserts++
	return repository.Repository.Insert(ctx, comment)
}

func (reader targetReader) GetCommentTarget(_ context.Context, id string) (pullrequestcomments.PullRequestTarget, error) {
	target, ok := reader.targets[id]
	if !ok {
		return pullrequestcomments.PullRequestTarget{}, pullrequestcomments.ErrPullRequestNotFound
	}
	return target, nil
}

func TestCommentLifecyclePersistsAcrossRestart(t *testing.T) {
	db := openCommentsDB(t)
	ctx := t.Context()
	store, err := sqliteadapter.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	targets := targetReader{targets: map[string]pullrequestcomments.PullRequestTarget{
		"pr-one": {ID: "pr-one", RepositoryID: "project-one", Status: pullrequestcomments.PullRequestOpen, HeadCommit: "head-one"},
		"pr-two": {ID: "pr-two", RepositoryID: "project-one", Status: pullrequestcomments.PullRequestOpen, HeadCommit: "head-two"},
	}}
	service := pullrequestcomments.NewService(store, targets)

	parent, err := service.Create(ctx, pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "  Review this  ", Origin: pullrequestcomments.UserOrigin()})
	if err != nil {
		t.Fatal(err)
	}
	if parent.Body != "Review this" || parent.OriginalHeadCommit != "head-one" || parent.Status != pullrequestcomments.Unresolved || parent.AuthorType != pullrequestcomments.AuthorUser {
		t.Fatalf("unexpected parent: %#v", parent)
	}
	if len(parent.ID) < 5 || parent.ID[:4] != "prc-" || parent.CreatedAt.Location() != time.UTC {
		t.Fatalf("unexpected generated identity or time: %#v", parent)
	}

	reply, err := service.Create(ctx, pullrequestcomments.CreateComment{
		PullRequestID: "pr-one", ParentCommentID: parent.ID, Body: "Fixed.",
		Origin: pullrequestcomments.WorkerOrigin("session-one", "worker-one", "published-head"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Status != pullrequestcomments.Resolved || reply.OriginalHeadCommit != "published-head" || reply.SourceWorkerID != "worker-one" {
		t.Fatalf("unexpected reply: %#v", reply)
	}
	userReply, err := service.Create(ctx, pullrequestcomments.CreateComment{
		PullRequestID: "pr-one", ParentCommentID: parent.ID, Body: "User follow-up", Origin: pullrequestcomments.UserOrigin(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if userReply.Status != pullrequestcomments.Resolved {
		t.Fatalf("user reply status = %q, want resolved", userReply.Status)
	}
	if _, err := service.Resolve(ctx, userReply.ID); !errors.Is(err, pullrequestcomments.ErrInvalidComment) {
		t.Fatalf("resolve reply error = %v, want invalid comment", err)
	}
	if _, err := service.Reopen(ctx, userReply.ID); !errors.Is(err, pullrequestcomments.ErrInvalidComment) {
		t.Fatalf("reopen reply error = %v, want invalid comment", err)
	}
	current, err := service.Get(ctx, parent.ID)
	if err != nil || current.Status != pullrequestcomments.Unresolved {
		t.Fatalf("worker reply resolved parent: %#v, %v", current, err)
	}

	if _, err := service.Create(ctx, pullrequestcomments.CreateComment{PullRequestID: "pr-two", ParentCommentID: parent.ID, Body: "wrong PR", Origin: pullrequestcomments.UserOrigin()}); !errors.Is(err, pullrequestcomments.ErrInvalidParent) {
		t.Fatalf("cross-PR parent error = %v", err)
	}
	if _, err := service.Create(ctx, pullrequestcomments.CreateComment{PullRequestID: "pr-one", ParentCommentID: reply.ID, Body: "nested", Origin: pullrequestcomments.UserOrigin()}); !errors.Is(err, pullrequestcomments.ErrInvalidParent) {
		t.Fatalf("nested parent error = %v", err)
	}

	restartedStore, err := sqliteadapter.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	restarted := pullrequestcomments.NewService(restartedStore, targets)
	comments, err := restarted.ListByPullRequest(ctx, "pr-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 3 || comments[0].ID != parent.ID || comments[1].ID != reply.ID || comments[2].ID != userReply.ID {
		t.Fatalf("unexpected restart ordering: %#v", comments)
	}
	count, err := restarted.UnresolvedCount(ctx, "pr-one")
	if err != nil || count != 1 {
		t.Fatalf("unresolved count = %d, %v", count, err)
	}
	if _, err := restarted.Create(ctx, pullrequestcomments.CreateComment{PullRequestID: "pr-two", Body: "Second pull request", Origin: pullrequestcomments.UserOrigin()}); err != nil {
		t.Fatal(err)
	}
	counts, err := restarted.UnresolvedCounts(ctx, []string{"pr-one", "pr-two", "pr-without-comments"})
	if err != nil {
		t.Fatal(err)
	}
	if counts["pr-one"] != 1 || counts["pr-two"] != 1 || counts["pr-without-comments"] != 0 {
		t.Fatalf("unresolved counts = %#v", counts)
	}
}

func TestWorkerReplyCreationIsIdempotent(t *testing.T) {
	db := openCommentsDB(t)
	store, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	targets := targetReader{targets: map[string]pullrequestcomments.PullRequestTarget{
		"pr-one": {ID: "pr-one", Status: pullrequestcomments.PullRequestOpen, HeadCommit: "head"},
	}}
	service := pullrequestcomments.NewService(store, targets)
	parent, err := service.Create(t.Context(), pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "Fix", Origin: pullrequestcomments.UserOrigin()})
	if err != nil {
		t.Fatal(err)
	}
	request := pullrequestcomments.CreateComment{PullRequestID: "pr-one", ParentCommentID: parent.ID, Body: "Done", Origin: pullrequestcomments.WorkerOrigin("session", "worker", "new-head")}
	first, err := service.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("worker retry duplicated reply: %s != %s", first.ID, second.ID)
	}
}

func TestReviewCommentBatchValidationAndRetry(t *testing.T) {
	db := openCommentsDB(t)
	store, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	repository := &interruptingRepository{Repository: store, failAt: 1, failing: true}
	service := pullrequestcomments.NewService(repository, targetReader{targets: map[string]pullrequestcomments.PullRequestTarget{
		"pr-one": {ID: "pr-one", Status: pullrequestcomments.PullRequestOpen, HeadCommit: "current-head"},
	}})
	line := 12
	request := pullrequestcomments.CreateReviewComments{
		PullRequestID: "pr-one", Origin: pullrequestcomments.ReviewOrigin("session", "review", "reviewed-head"),
		Comments: []pullrequestcomments.ReviewComment{
			{Body: "General", Scope: pullrequestcomments.ScopePullRequest},
			{Body: "Inline", Scope: pullrequestcomments.ScopeLine, Path: "main.go", Side: "RIGHT", Line: &line},
		},
	}
	invalid := request
	invalid.Comments = append([]pullrequestcomments.ReviewComment(nil), request.Comments...)
	invalid.Comments[1].Line = nil
	if _, err = service.CreateReviewComments(t.Context(), invalid); !errors.Is(err, pullrequestcomments.ErrInvalidComment) || repository.inserts != 0 {
		t.Fatalf("invalid batch: inserts=%d err=%v", repository.inserts, err)
	}
	if _, err = service.CreateReviewComments(t.Context(), request); err == nil || repository.inserts != 1 {
		t.Fatalf("interrupted batch: inserts=%d err=%v", repository.inserts, err)
	}
	repository.failing = false
	created, err := service.CreateReviewComments(t.Context(), request)
	if err != nil || len(created) != 2 || created[0].OriginalHeadCommit != "reviewed-head" || created[1].Scope != pullrequestcomments.ScopeLine {
		t.Fatalf("retried batch: comments=%+v err=%v", created, err)
	}
	conflict := request
	conflict.Comments = append([]pullrequestcomments.ReviewComment(nil), request.Comments...)
	conflict.Comments[0].Body = "Different"
	if _, err = service.CreateReviewComments(t.Context(), conflict); !errors.Is(err, pullrequestcomments.ErrInvalidComment) {
		t.Fatalf("conflicting retry error=%v", err)
	}
	listed, err := service.ListByPullRequest(t.Context(), "pr-one")
	if err != nil || len(listed) != 2 || listed[0].Body != "General" {
		t.Fatalf("persisted comments=%+v err=%v", listed, err)
	}
}

func TestSuccessfulPublicationRetiresOnlySupersededFailedEdits(t *testing.T) {
	for _, test := range []struct {
		name               string
		failedOperation    string
		completedOperation string
		failureIsNewer     bool
		wantState          pullrequestcomments.PublicationState
	}{
		{"replacement edit", "update", "update", false, pullrequestcomments.PublicationPublished},
		{"unfulfilled resolution", "resolve", "update", false, pullrequestcomments.PublicationFailed},
		{"unfulfilled reopening", "reopen", "update", false, pullrequestcomments.PublicationFailed},
		{"newer failed edit", "update", "update", true, pullrequestcomments.PublicationFailed},
		{"resolution does not replace edit", "update", "resolve", false, pullrequestcomments.PublicationFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			store, err := sqliteadapter.New(ctx, openCommentsDB(t))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			identity := pullrequestcomments.ProviderIdentity{Provider: "github", ExternalID: "conversation:7", Kind: "conversation"}
			gateway := &lifecycleGateway{remote: []pullrequestcomments.RemoteComment{{
				ProviderIdentity: identity, Body: "Remote original", Scope: pullrequestcomments.ScopePullRequest,
				CreatedAt: now, UpdatedAt: now,
			}}}
			service := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil))
			comments, err := service.Sync(ctx, "pr-one")
			if err != nil || len(comments) != 1 {
				t.Fatalf("initial sync: %+v, %v", comments, err)
			}
			commentID := comments[0].ID
			failed := pullrequestcomments.PublicationJob{CommentID: commentID, PullRequestID: "pr-one", Operation: test.failedOperation, PermanentError: "rejected", CreatedAt: now, NextAttemptAt: now}
			completed := pullrequestcomments.PublicationJob{CommentID: commentID, PullRequestID: "pr-one", Operation: test.completedOperation, CreatedAt: now, NextAttemptAt: now}
			jobs := []pullrequestcomments.PublicationJob{failed, completed}
			completedIndex := 1
			if test.failureIsNewer {
				jobs = []pullrequestcomments.PublicationJob{completed, failed}
				completedIndex = 0
			}
			for _, job := range jobs {
				if err := store.EnqueuePublication(ctx, job); err != nil {
					t.Fatal(err)
				}
			}
			jobs, err = store.PendingPublications(ctx, "pr-one")
			if err != nil || len(jobs) != 2 {
				t.Fatalf("queued jobs: %+v, %v", jobs, err)
			}
			if err := store.CompletePublication(ctx, jobs[completedIndex].ID); err != nil {
				t.Fatal(err)
			}
			comment, err := store.Get(ctx, commentID)
			if err != nil || comment.PublicationState != test.wantState {
				t.Fatalf("completed publication: %+v, %v; want %s", comment, err, test.wantState)
			}
			remaining, err := store.PendingPublications(ctx, "pr-one")
			if err != nil {
				t.Fatal(err)
			}
			if test.wantState == pullrequestcomments.PublicationFailed {
				if len(remaining) != 1 || remaining[0].ID != jobs[1-completedIndex].ID {
					t.Fatalf("unfulfilled operation was removed: %+v", remaining)
				}
				return
			}
			if len(remaining) != 0 {
				t.Fatalf("superseded edit remains queued: %+v", remaining)
			}
			gateway.remote[0].Body = "Later remote edit"
			comments, err = service.Sync(ctx, "pr-one")
			if err != nil || len(comments) != 1 || comments[0].Body != "Later remote edit" || comments[0].PublicationState != pullrequestcomments.PublicationPublished {
				t.Fatalf("reconciliation after replacement: %+v, %v", comments, err)
			}
		})
	}
}

func openCommentsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "comments.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`create table repositories (id text primary key)`,
		`create table pull_requests (id text primary key, repository_id text not null references repositories(id))`,
		`insert into repositories(id) values ('project-one')`,
		`insert into pull_requests(id, repository_id) values ('pr-one', 'project-one'), ('pr-two', 'project-one')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
