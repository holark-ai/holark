package sqliteadapter

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/protocol"
)

func TestDefaultHarnessPersistsAcrossStoreInstancesAndIsRepositoryIsolated(t *testing.T) {
	open := func(path string) (*sql.DB, *Store) {
		db, err := database.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		store, err := New(t.Context(), db)
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		return db, store
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "first.sqlite")
	db, first := open(path)
	if err := first.SaveDefault(t.Context(), agentsettings.WorkflowDefault, protocol.HarnessOpenCode); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedDB, reopened := open(path)
	defer reopenedDB.Close()
	value, saved, err := reopened.LoadDefault(t.Context(), agentsettings.WorkflowDefault)
	if err != nil || !saved || value != protocol.HarnessOpenCode {
		t.Fatalf("reloaded = %q, %v, %v", value, saved, err)
	}

	isolatedDB, isolated := open(filepath.Join(dir, "second.sqlite"))
	defer isolatedDB.Close()
	value, saved, err = isolated.LoadDefault(t.Context(), agentsettings.WorkflowDefault)
	if err != nil || saved || value != "" {
		t.Fatalf("isolated repository = %q, %v, %v", value, saved, err)
	}
}
