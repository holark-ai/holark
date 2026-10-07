package sqliteadapter

import (
	"path/filepath"
	"testing"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	pullrequestsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
)

func TestStorePersistsOpenPreparationTarget(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "metadata.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`create table repositories(id text primary key, repository_url text not null); insert into repositories values('repo', 'git@example.test/repo.git')`); err != nil {
		t.Fatal(err)
	}
	catalog, err := pullrequestsqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	pullRequest, err := catalog.CreatePullRequest(pullrequestlifecycle.PullRequest{
		RepositoryID: "repo", Title: "Local", Status: pullrequestlifecycle.StatusWIP,
		BaseBranch: "main", BaseCommit: "base", HeadBranch: "feature", HeadCommit: "head",
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(t.Context(), db, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.InitializePreparation(t.Context(), pullRequest.ID, pullrequestmetadata.PreparationTargetOpen); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Get(t.Context(), pullRequest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PreparationTarget != pullrequestmetadata.PreparationTargetOpen {
		t.Fatalf("preparation target = %q", snapshot.PreparationTarget)
	}
}
