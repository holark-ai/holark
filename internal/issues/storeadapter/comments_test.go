package storeadapter

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/issues"
	"github.com/holark-ai/holark/internal/issues/comments"
)

func TestCommentMigrationReconciliationAndCascade(t *testing.T) {
	canPost, cannotPost := true, false
	ctx := t.Context()
	db, err := database.Open(filepath.Join(t.TempDir(), "issues.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `create table repositories(id text primary key);insert into repositories values('repo');create table sessions(id text primary key);create table pull_requests(id text primary key);`)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	original := issues.Issue{ID: "issue", RepositoryID: "repo", Title: "Existing issue", Body: "Preserve me", Status: issues.IssueOpen, CreatedAt: at, UpdatedAt: at}
	if err = s.Insert(ctx, original); err != nil {
		t.Fatal(err)
	}
	before, err := s.Get(ctx, "issue")
	if err != nil {
		t.Fatal(err)
	}
	s, err = New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, "issue")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed issue: %+v, %v", after, err)
	}
	d, err := s.ListComments(ctx, "issue")
	if err != nil || d.Comments == nil || d.CanComment != nil || d.SyncedAt != nil {
		t.Fatalf("unknown discussion: %+v %v", d, err)
	}
	first := comments.Comment{IssueID: "issue", GitHubID: "9", GitHubNodeID: "node9", Body: "> untouched quote\n\nbody", Author: comments.Author{Login: "outsider"}, CreatedAt: at, UpdatedAt: at, CanEdit: true}
	second := first
	second.GitHubID = "10"
	second.GitHubNodeID = "node10"
	second.Author = comments.Author{}
	second.CreatedAt = at.Add(time.Nanosecond)
	if err = s.ReconcileComments(ctx, "issue", []comments.Comment{second, first}, &canPost, at); err != nil {
		t.Fatal(err)
	}
	d, err = s.ListComments(ctx, "issue")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Comments) != 2 || d.Comments[0].GitHubID != "9" || d.Comments[1].Author.Login != "" || !*d.CanComment {
		t.Fatalf("discussion: %+v", d)
	}
	id := d.Comments[0].ID
	first.Body = "Remote edit"
	first.CanEdit = false
	first.Author = comments.Author{}
	if err = s.ReconcileComments(ctx, "issue", []comments.Comment{first}, &cannotPost, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	d, err = s.ListComments(ctx, "issue")
	if err != nil || len(d.Comments) != 1 || d.Comments[0].ID != id || d.Comments[0].Body != "Remote edit" || d.Comments[0].CanEdit || *d.CanComment {
		t.Fatalf("reconcile: %+v %v", d, err)
	}
	invalid := second
	invalid.GitHubID = ""
	first.Body = "must roll back"
	if err = s.ReconcileComments(ctx, "issue", []comments.Comment{first, invalid}, &canPost, at); err == nil {
		t.Fatal("expected malformed snapshot failure")
	}
	unchanged, err := s.ListComments(ctx, "issue")
	if err != nil || !reflect.DeepEqual(d, unchanged) {
		t.Fatalf("partial reconcile committed: %+v %v", unchanged, err)
	}
	if _, err = New(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `delete from issues where id='issue'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetComment(ctx, id); !errors.Is(err, comments.ErrNotFound) {
		t.Fatalf("cascade: %v", err)
	}
	d, err = s.ListComments(ctx, "issue")
	if err != nil || len(d.Comments) != 0 || d.SyncedAt != nil {
		t.Fatalf("sync cascade: %+v %v", d, err)
	}
}
