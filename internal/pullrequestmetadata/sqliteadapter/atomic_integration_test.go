package sqliteadapter

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	catalogsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
	"github.com/holark-ai/holark/internal/testfixture/branchfixture"
)

func TestMetadataAndCatalogCommitTogetherWithoutRestoringPublishedHead(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "metadata.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`create table repositories(id text primary key, repository_url text not null); insert into repositories values('repo', 'https://github.com/owner/repo')`); err != nil {
		t.Fatal(err)
	}
	catalog, err := catalogsqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	pr, err := branchfixture.Create(t.Context(), catalog, pullrequestlifecycle.PullRequest{RepositoryID: "repo", Title: "Before", Status: pullrequestlifecycle.StatusOpen, HeadCommit: "old-head"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(t.Context(), db, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := branchfixture.Accept(t.Context(), catalog, pr.ID, "base", "published-head", "base"); err != nil {
		t.Fatal(err)
	}
	operation, _, err := catalog.BeginOperation(t.Context(), pullrequestlifecycle.Operation{RequestID: "metadata-first", PullRequestID: pr.ID, Kind: "metadata", Groups: []pullrequestlifecycle.FieldGroup{pullrequestlifecycle.MetadataGroup}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(t.Context(), pr.ID, operation.RequestID, "Generated title", "Generated description", time.Now().UTC(), pullrequestmetadata.State{}); err != nil {
		t.Fatal(err)
	}
	completed, found, err := catalog.GetOperation(t.Context(), operation.RequestID)
	if err != nil || !found || completed.Status != "succeeded" {
		t.Fatalf("metadata completion=%+v err=%v", completed, err)
	}
	current, ok := catalog.GetPullRequest(pr.ID)
	if !ok || current.Title != "Generated title" || current.Summary != "Generated description" || current.HeadCommit != "published-head" || current.ViewRevision <= pr.ViewRevision {
		t.Fatalf("catalog after metadata: %+v", current)
	}
	snapshot, err := store.Get(t.Context(), pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.HeadCommit != current.HeadCommit || snapshot.Title != current.Title || snapshot.Description != current.Summary {
		t.Fatalf("representations disagree: %+v / %+v", current, snapshot)
	}
	// A failed state write must roll back both visible representations too.
	if _, err := db.Exec(`create trigger reject_metadata before update on pull_request_metadata begin select raise(abort, 'state write rejected'); end`); err != nil {
		t.Fatal(err)
	}
	operation, _, err = catalog.BeginOperation(t.Context(), pullrequestlifecycle.Operation{RequestID: "metadata-second", PullRequestID: pr.ID, Kind: "metadata", Groups: []pullrequestlifecycle.FieldGroup{pullrequestlifecycle.MetadataGroup}})
	if err != nil {
		t.Fatal(err)
	}
	current, _ = catalog.GetPullRequest(pr.ID)
	if err := store.Update(t.Context(), pr.ID, operation.RequestID, "Uncommitted title", "Uncommitted description", time.Now().UTC(), pullrequestmetadata.State{}); err == nil {
		t.Fatal("expected rejected state write")
	}
	pending, found, err := catalog.GetOperation(t.Context(), operation.RequestID)
	if err != nil || !found || !pending.Active() {
		t.Fatalf("operation escaped rollback=%+v err=%v", pending, err)
	}
	rolledBack, _ := catalog.GetPullRequest(pr.ID)
	snapshot, err = store.Get(t.Context(), pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Title != current.Title || rolledBack.ViewRevision != current.ViewRevision || snapshot.Title != current.Title || snapshot.HeadCommit != "published-head" {
		t.Fatalf("partial metadata commit: %+v / %+v", rolledBack, snapshot)
	}
}
