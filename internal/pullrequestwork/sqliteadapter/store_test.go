package sqliteadapter

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

func TestHasWorkerReferenceFindsDurableAddressWork(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "work.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(t.Context(), pullrequestwork.Work{
		ID: "worker", PullRequestID: "pr", Kind: pullrequestwork.KindWorker,
		CommentID: "comment", Status: pullrequestwork.StatusCompleted, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		commentID string
		want      bool
	}{
		{commentID: "comment", want: true},
		{commentID: "other", want: false},
	} {
		got, err := store.HasWorkerReference(t.Context(), test.commentID)
		if err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Fatalf("HasWorkerReference(%q) = %t, want %t", test.commentID, got, test.want)
		}
	}
}
