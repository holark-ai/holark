package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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

type stalledReadinessRepository struct {
	holons.Repository
	started, release chan struct{}
}

func (r stalledReadinessRepository) InspectSynchronization(ctx context.Context, _ string, _ string, _ []string) (holons.SynchronizationInspection, error) {
	close(r.started)
	select {
	case <-r.release:
		return holons.SynchronizationInspection{Branch: "holark/h", HeadCommit: "head", Incorporated: true}, nil
	case <-ctx.Done():
		return holons.SynchronizationInspection{}, ctx.Err()
	}
}

func TestStalledReadinessAllowsTabMutationsAndRejectsObsoleteResult(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	initial := holons.Holon{ID: "h", Title: "Work", Kind: holons.KindNormal, Status: holons.StatusRunning, BaseCommit: "base", WorktreeBranch: "holark/h", WorktreePath: "/workspace", CreatedAt: now, AgentSessions: []holons.AgentSession{{ID: "existing", HolonID: "h", AgentType: "codex", Status: "running", CreatedAt: now, UpdatedAt: now}}}
	if err := store.Create(t.Context(), initial); err != nil {
		t.Fatal(err)
	}
	repo := stalledReadinessRepository{started: make(chan struct{}), release: make(chan struct{})}
	service := holons.NewServiceWithRepository(store, repo)
	handler := New(service)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := service.PublicationReadiness(ctx, "h", "main", "target"); done <- err }()
	select {
	case <-repo.started:
	case <-time.After(3 * time.Second):
		t.Fatal("readiness did not start")
	}
	request := func(method, path, body, key string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		finished := make(chan struct{})
		go func() { handler.ServeHTTP(w, r); close(finished) }()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Fatal("unrelated HTTP operation blocked")
		}
		return w
	}
	if w := request(http.MethodPut, "/api/v1/holons/h/selected-tab", `{"tab_id":"existing"}`, ""); w.Code != 204 {
		t.Fatalf("select: %d %s", w.Code, w.Body.String())
	}
	// Two deliveries of one creation request must identify the same durable tab.
	var agentID string
	for i := 0; i < 2; i++ {
		w := request(http.MethodPost, "/api/v1/holons/h/agent-sessions", `{"agent_type":"codex","prompt":""}`, "retry-request")
		if w.Code != 201 {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		var agent holons.AgentSession
		if err := json.Unmarshal(w.Body.Bytes(), &agent); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			agentID = agent.ID
		} else if agent.ID != agentID {
			t.Fatal("retry created another agent")
		}
	}
	if _, err := service.End(t.Context(), "h"); err != nil {
		t.Fatal(err)
	}
	close(repo.release)
	select {
	case err := <-done:
		if !errors.Is(err, holons.ErrPublicationUnavailable) {
			t.Fatalf("stale readiness accepted: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("readiness did not return")
	}
	got, err := store.Get(t.Context(), "h")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AgentSessions) != 2 || got.LastSelectedTabID != "existing" || got.SynchronizedTargetCommit != "" || got.Status != holons.StatusCancelling {
		t.Fatalf("unexpected state: %+v", got)
	}
	// A new service instance recovers the same request identity from SQLite.
	recovered := New(holons.NewService(store))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/holons/h/agent-sessions", strings.NewReader(`{"agent_type":"codex","prompt":""}`))
	r.Header.Set("Idempotency-Key", "retry-request")
	w := httptest.NewRecorder()
	recovered.ServeHTTP(w, r)
	var agent holons.AgentSession
	_ = json.Unmarshal(w.Body.Bytes(), &agent)
	if w.Code != 201 || agent.ID != agentID {
		t.Fatalf("retry after restart: %d %s", w.Code, w.Body.String())
	}
}
