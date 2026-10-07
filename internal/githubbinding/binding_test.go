package githubbinding

import (
	"errors"
	"github.com/holark-ai/holark/internal/database"
	"path/filepath"
	"testing"
)

func TestBindingRequiresConfirmationOnlyForDifferentGitHubRepository(t *testing.T) {
	db, e := database.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	store, e := New(t.Context(), db)
	if e != nil {
		t.Fatal(e)
	}
	first, e := store.Bind(t.Context(), "repo", "git@github.com:Holark-AI/Holark.git", false)
	if e != nil {
		t.Fatal(e)
	}
	if first.Owner != "Holark-AI" || first.Name != "Holark" {
		t.Fatalf("first=%+v", first)
	}
	if _, e = store.Bind(t.Context(), "repo", "https://github.com/holark-ai/holark", false); e != nil {
		t.Fatalf("equivalent binding: %v", e)
	}
	if _, e = store.Bind(t.Context(), "repo", "https://github.com/other/project", false); !errors.Is(e, ErrConfirmationRequired) {
		t.Fatalf("different binding error=%v", e)
	}
	changed, e := store.Bind(t.Context(), "repo", "https://github.com/other/project", true)
	if e != nil || changed.Owner != "other" {
		t.Fatalf("confirmed=%+v %v", changed, e)
	}
}
