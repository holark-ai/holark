package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/holons/sqliteadapter"
)

func TestSelectedTabHTTPPersistsAcrossDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	open := func() (*sql.DB, *sqliteadapter.Store, http.Handler) {
		t.Helper()
		db, err := database.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		store, err := sqliteadapter.New(t.Context(), db)
		if err != nil {
			t.Fatal(err)
		}
		return db, store, New(holons.NewService(store))
	}
	db, store, handler := open()
	created := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	if err := store.Create(t.Context(), holons.Holon{
		ID: "h", Title: "Work", Status: holons.StatusCompleted, ReadOnly: true, CreatedAt: created,
		AgentSessions: []holons.AgentSession{{ID: "agent", HolonID: "h", AgentType: "codex", CreatedAt: created, UpdatedAt: created}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		tab    string
		status int
	}{{"agent", http.StatusNoContent}, {"unknown", http.StatusBadRequest}} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/v1/holons/h/selected-tab", strings.NewReader(`{"tab_id":"`+tc.tab+`"}`)))
		if w.Code != tc.status {
			t.Fatalf("select %q: %d %s; want %d", tc.tab, w.Code, w.Body.String(), tc.status)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, handler = open()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/holons/h", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("get after reopen: %d %s", w.Code, w.Body.String())
	}
	var got holons.Holon
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.LastSelectedTabID != "agent" || got.Title != "Work" || got.Status != holons.StatusCompleted || !got.ReadOnly {
		t.Fatalf("selection or lifecycle state changed after reopen: %+v", got)
	}
}
