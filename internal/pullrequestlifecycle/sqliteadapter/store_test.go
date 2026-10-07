package sqliteadapter

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

func TestCatalogIsDurableAndKeepsLocalLinksAcrossSync(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "catalog.sqlite")
	db, err := database.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`create table repositories(id text primary key); insert into repositories(id) values('repo')`); err != nil {
		t.Fatal(err)
	}
	store, err := New(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreatePullRequest(pullrequestlifecycle.PullRequest{RepositoryID: "repo", Title: "Local", Status: pullrequestlifecycle.StatusWIP, LinkedHolonIDs: []string{"holon-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetPullRequest(created.ID); !ok {
		t.Fatal("created pull request missing")
	}
	now := time.Now().UTC()
	incoming := pullrequestlifecycle.PullRequest{RepositoryID: "repo", Title: "GitHub", Status: pullrequestlifecycle.StatusOpen, SyncProvider: "github", SyncExternalID: "7", CreatedAt: now, UpdatedAt: now}
	token, err := store.BeginObservation(t.Context(), "repo", []pullrequestlifecycle.FieldGroup{pullrequestlifecycle.LifecycleGroup, pullrequestlifecycle.TopologyGroup, pullrequestlifecycle.MetadataGroup})
	if err != nil {
		t.Fatal(err)
	}
	if imported, _, err := store.UpsertObservedPullRequests("repo", []pullrequestlifecycle.PullRequest{incoming}, token); err != nil || imported != 1 {
		t.Fatalf("sync: imported=%d err=%v", imported, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = database.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reopened, err := New(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	items := reopened.ListPullRequests("repo")
	if len(items) != 2 {
		t.Fatalf("got %d pull requests", len(items))
	}
	for _, item := range items {
		if item.RepositoryID != "repo" {
			t.Fatalf("list repository ID for %q = %q", item.ID, item.RepositoryID)
		}
	}
	persisted, ok := reopened.GetPullRequest(created.ID)
	if !ok {
		t.Fatal("persisted pull request missing")
	}
	if persisted.RepositoryID != "repo" {
		t.Fatalf("get repository ID = %q", persisted.RepositoryID)
	}

	transitioned, err := reopened.TransitionPullRequestStatus(created.ID, pullrequestlifecycle.StatusDraft, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if transitioned.RepositoryID != "repo" {
		t.Fatalf("transition repository ID = %q", transitioned.RepositoryID)
	}
	var catalogRepositoryID string
	if err := db.QueryRow(`select repository_id from pull_request_catalog where id = ?`, created.ID).Scan(&catalogRepositoryID); err != nil {
		t.Fatal(err)
	}
	if catalogRepositoryID != "repo" {
		t.Fatalf("catalog repository ID = %q", catalogRepositoryID)
	}
	found := false
	for _, item := range reopened.ListPullRequests("repo") {
		if item.ID == created.ID {
			found = true
			if item.Status != pullrequestlifecycle.StatusDraft {
				t.Fatalf("persisted status = %q", item.Status)
			}
		}
	}
	if !found {
		t.Fatal("transitioned pull request missing from repository catalog")
	}
}

func TestSyncKeepsMetadataUpdatedAfterProviderSnapshot(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`create table repositories(id text primary key); insert into repositories(id) values('repo')`); err != nil {
		t.Fatal(err)
	}
	store, err := New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}

	providerUpdatedAt := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	providerSnapshot := pullrequestlifecycle.PullRequest{
		RepositoryID: "repo", Title: "Old title", Summary: "Old description", Status: pullrequestlifecycle.StatusOpen,
		SyncProvider: "github", SyncExternalID: "github:owner/repo#1", SyncData: []byte(`{"version":"old"}`),
		CreatedAt: providerUpdatedAt.Add(-time.Hour), UpdatedAt: providerUpdatedAt,
	}
	token, err := store.BeginObservation(t.Context(), "repo", []pullrequestlifecycle.FieldGroup{pullrequestlifecycle.LifecycleGroup, pullrequestlifecycle.TopologyGroup, pullrequestlifecycle.MetadataGroup})
	if err != nil {
		t.Fatal(err)
	}
	if imported, _, err := store.UpsertObservedPullRequests("repo", []pullrequestlifecycle.PullRequest{providerSnapshot}, token); err != nil || imported != 1 {
		t.Fatalf("initial sync: imported=%d err=%v", imported, err)
	}
	stored := store.ListPullRequests("repo")[0]
	token, err = store.BeginObservation(t.Context(), "repo", []pullrequestlifecycle.FieldGroup{pullrequestlifecycle.LifecycleGroup, pullrequestlifecycle.TopologyGroup, pullrequestlifecycle.MetadataGroup})
	if err != nil {
		t.Fatal(err)
	}
	metadataUpdatedAt := providerUpdatedAt.Add(time.Minute)
	if err := store.UpdatePullRequestMetadata(stored.ID, "Generated title", "Generated description", metadataUpdatedAt); err != nil {
		t.Fatal(err)
	}

	providerSnapshot.SyncData = []byte(`{"version":"refreshed"}`)
	if _, updated, err := store.UpsertObservedPullRequests("repo", []pullrequestlifecycle.PullRequest{providerSnapshot}, token); err != nil || updated != 1 {
		t.Fatalf("stale sync: updated=%d err=%v", updated, err)
	}
	persisted, ok := store.GetPullRequest(stored.ID)
	if !ok {
		t.Fatal("pull request missing after sync")
	}
	if persisted.Title != "Generated title" || persisted.Summary != "Generated description" || !persisted.UpdatedAt.Equal(metadataUpdatedAt) {
		t.Fatalf("metadata overwritten by stale sync: %+v", persisted)
	}
	if string(persisted.SyncData) != `{"version":"refreshed"}` {
		t.Fatalf("sync data = %s", persisted.SyncData)
	}
}

func TestCreatePullRequestRequiresRepositoryIDWithoutWritingRows(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`create table repositories(id text primary key)`); err != nil {
		t.Fatal(err)
	}
	store, err := New(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.CreatePullRequest(pullrequestlifecycle.PullRequest{Title: "Local", Status: pullrequestlifecycle.StatusWIP}); err == nil {
		t.Fatal("create without repository ID succeeded")
	}
	for _, table := range []string{"pull_request_catalog", "pull_requests"} {
		var count int
		if err := db.QueryRow(`select count(*) from ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s contains %d rows", table, count)
		}
	}
}

func TestLegacyOriginBaseBranchIsCanonicalizedOnReadAndWrite(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "legacy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`create table repositories(id text primary key); insert into repositories(id) values('repo')`); err != nil {
		t.Fatal(err)
	}
	store, err := New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreatePullRequest(pullrequestlifecycle.PullRequest{RepositoryID: "repo", Title: "Legacy", BaseBranch: "origin/main", Status: pullrequestlifecycle.StatusWIP})
	if err != nil || created.BaseBranch != "main" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	var stored string
	if err = db.QueryRow(`select base_branch from pull_requests where id=?`, created.ID).Scan(&stored); err != nil || stored != "main" {
		t.Fatalf("stored branch=%q err=%v", stored, err)
	}
}

func TestCatalogConflictKeepsRepositoryOwnership(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`create table repositories(id text primary key); insert into repositories(id) values('repo'), ('other')`); err != nil {
		t.Fatal(err)
	}
	store, err := New(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreatePullRequest(pullrequestlifecycle.PullRequest{RepositoryID: "repo", Title: "Original", Status: pullrequestlifecycle.StatusWIP})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePullRequest(pullrequestlifecycle.PullRequest{ID: created.ID, RepositoryID: "other", Title: "Updated", Status: pullrequestlifecycle.StatusWIP}); err != nil {
		t.Fatal(err)
	}

	persisted, ok := store.GetPullRequest(created.ID)
	if !ok {
		t.Fatal("persisted pull request missing")
	}
	if persisted.RepositoryID != "repo" {
		t.Fatalf("repository ID changed to %q", persisted.RepositoryID)
	}
	if len(store.ListPullRequests("other")) != 0 {
		t.Fatal("pull request moved to other repository")
	}
}

func TestActivityRetirementCompletionBelongsToOneLifecycle(t *testing.T) {
	for _, next := range []pullrequestlifecycle.Status{pullrequestlifecycle.StatusOpen, pullrequestlifecycle.StatusMerged} {
		t.Run(string(next), func(t *testing.T) {
			store, _ := operationCatalog(t)
			p, err := store.CreatePullRequest(pullrequestlifecycle.PullRequest{RepositoryID: "repo", Status: pullrequestlifecycle.StatusClosed, SyncProvider: "github", SyncExternalID: "7"})
			if err != nil {
				t.Fatal(err)
			}
			if err = store.MarkPullRequestActivityRetired(p.ID, p.LifecycleGeneration); err != nil {
				t.Fatal(err)
			}
			observe := func(status pullrequestlifecycle.Status) pullrequestlifecycle.PullRequest {
				t.Helper()
				token, err := store.BeginObservation(t.Context(), "repo", []pullrequestlifecycle.FieldGroup{pullrequestlifecycle.LifecycleGroup})
				if err != nil {
					t.Fatal(err)
				}
				incoming := p
				incoming.Status = status
				if _, _, err = store.UpsertObservedPullRequests("repo", []pullrequestlifecycle.PullRequest{incoming}, token); err != nil {
					t.Fatal(err)
				}
				current, ok := store.GetPullRequest(p.ID)
				if !ok || current.Status != status {
					t.Fatalf("observation not accepted: %+v", current)
				}
				return current
			}
			if current := observe(p.Status); !current.ActivityRetired || current.LifecycleGeneration != p.LifecycleGeneration {
				t.Fatalf("unchanged observation lost completion: %+v", current)
			}
			current := observe(next)
			if current.ActivityRetired || current.LifecycleGeneration <= p.LifecycleGeneration {
				t.Fatalf("status change did not reset completion: %+v", current)
			}
			if next.Active() {
				current = observe(pullrequestlifecycle.StatusClosed)
			}
			// A delayed cleanup from the first closed lifecycle cannot complete
			// the new terminal lifecycle, even when its status is closed again.
			if err = store.MarkPullRequestActivityRetired(p.ID, p.LifecycleGeneration); err != nil {
				t.Fatal(err)
			}
			if stored, _ := store.GetPullRequest(p.ID); stored.ActivityRetired {
				t.Fatal("old generation marked the new lifecycle complete")
			}
			if err = store.MarkPullRequestActivityRetired(p.ID, current.LifecycleGeneration); err != nil {
				t.Fatal(err)
			}
			if stored, _ := store.GetPullRequest(p.ID); !stored.ActivityRetired {
				t.Fatal("current generation could not complete cleanup")
			}
		})
	}
}
