package sqliteadapter

import (
	"path/filepath"
	"testing"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	pullrequestsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
)

func TestStorePinsPullRequestsWithinTheirRepository(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "panel-pins.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`create table repositories(id text primary key); insert into repositories(id) values('repo'), ('other')`); err != nil {
		t.Fatal(err)
	}
	catalog, err := pullrequestsqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	first, err := catalog.CreatePullRequest(pullrequestlifecycle.PullRequest{RepositoryID: "repo", Title: "First", Status: pullrequestlifecycle.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	second, err := catalog.CreatePullRequest(pullrequestlifecycle.PullRequest{RepositoryID: "other", Title: "Second", Status: pullrequestlifecycle.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := store.Pin(t.Context(), first.ID); err != nil {
			t.Fatal(err)
		}
	}
	if pinned, err := store.Pinned(t.Context(), first.ID); err != nil || !pinned {
		t.Fatalf("pinned=%v err=%v", pinned, err)
	}
	if err := store.Pin(t.Context(), second.ID); err != nil {
		t.Fatal(err)
	}
	if pins, err := store.List(t.Context(), "repo"); err != nil || len(pins) != 1 || !pins[first.ID] {
		t.Fatalf("repo pins=%v err=%v", pins, err)
	}

	for range 2 {
		if err := store.Unpin(t.Context(), first.ID); err != nil {
			t.Fatal(err)
		}
	}
	if pinned, err := store.Pinned(t.Context(), first.ID); err != nil || pinned {
		t.Fatalf("pinned=%v err=%v", pinned, err)
	}
}
