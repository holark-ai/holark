package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/holons/sqliteadapter"
)

// HTTP contract tests control the application workflow port; source selection
// and native launching are covered by the localapp integration tests.
type commitAgentWorkflow struct {
	Service
	createCommitAgent func(context.Context, string, string) (holons.Holon, error)
}

type forkAgentWorkflow struct {
	Service
	forkAgent func(context.Context, string, string) (holons.AgentSession, error)
}

func (s forkAgentWorkflow) ForkAgentSession(ctx context.Context, id, sourceID string) (holons.AgentSession, error) {
	return s.forkAgent(ctx, id, sourceID)
}

func (s commitAgentWorkflow) CreateCommitAgent(ctx context.Context, id, sourceID string) (holons.Holon, error) {
	return s.createCommitAgent(ctx, id, sourceID)
}

func TestCommitAgentHTTPReturnsCreatedPublicAgent(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	service := holons.NewServiceWithRepository(store, inspectionRepository{})
	before, err := service.Create(t.Context(), holons.Create{Title: "Work", Prompt: "Original task", Kind: holons.KindNormal, BaseBranch: "main", BaseCommit: "base", AgentType: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	source := before.AgentSessions[0]
	const prompt = "Discuss the configured commit request."
	h := New(commitAgentWorkflow{Service: service, createCommitAgent: func(ctx context.Context, id, sourceID string) (holons.Holon, error) {
		created, _, err := service.AddCommitAgent(ctx, id, sourceID, prompt, "head", "")
		return created, err
	}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/holons/"+before.ID+"/commit-agent", strings.NewReader(`{"source_agent_id":"`+source.ID+`"}`)))
	if w.Code != http.StatusCreated || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("create commit agent: %d %s", w.Code, w.Body.String())
	}
	var got holons.AgentSession
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	after, err := service.Get(t.Context(), before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.AgentSessions) != 2 || got.ID == source.ID || got.ID != after.AgentSessions[1].ID || got.HolonID != before.ID || got.Title != "Commit" || got.AgentType != source.AgentType || got.Prompt != prompt || got.Status != string(holons.StatusQueued) || got.CommitPrompt == nil || got.CommitPrompt.State != "commit_discussion_started" {
		t.Fatalf("created public agent = %+v; persisted agents = %+v", got, after.AgentSessions)
	}
}

func TestCommitAgentHTTPProjectsWorkflowErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		status  int
		code    string
		message string
	}{
		{"missing Holon", holons.ErrNotFound, 404, "holon_not_found", "Holon not found."},
		{"missing source", holons.ErrCommitSourceMissing, 409, "commit_source_missing", "Select an open agent tab to commit."},
		{"unready source", holons.ErrCommitSourceNotForkable, 409, "commit_source_not_forkable", "This agent's conversation is not ready to fork yet."},
		{"active fork", holons.ErrCommitForkInProgress, 409, "commit_fork_in_progress", "A commit agent is already active."},
		{"unavailable harness", agentsessions.ErrUnavailable, 503, "harness_unavailable", "The source agent's harness is unavailable."},
		// Preparation retains the existing launch error projection. Its concrete
		// reason is persisted on the failed destination by the launch service.
		{"preparation failure", harness.ErrHarnessPreparation, 500, "holon_failed", "The Holon operation failed."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := New(commitAgentWorkflow{createCommitAgent: func(_ context.Context, id, sourceID string) (holons.Holon, error) {
				if id != "h" {
					t.Fatalf("Holon ID = %q", id)
				}
				return holons.Holon{}, fmt.Errorf("create commit agent: %w", tc.err)
			}})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/holons/h/commit-agent", strings.NewReader(`{"source_agent_id":"source"}`)))
			var got struct{ Code, Message string }
			if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || got.Code != tc.code || got.Message != tc.message {
				t.Fatalf("error response = %d %+v", w.Code, got)
			}
		})
	}
}

func TestCommitAgentHTTPRejectsClientChoices(t *testing.T) {
	h := New(commitAgentWorkflow{createCommitAgent: func(context.Context, string, string) (holons.Holon, error) {
		t.Fatal("client-supplied prompt or harness must not start the commit workflow")
		return holons.Holon{}, nil
	}})
	for _, body := range []string{
		`{"prompt":"Use this prompt instead"}`,
		`{"agent_type":"opencode"}`,
	} {
		t.Run(body, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/holons/h/commit-agent", bytes.NewBufferString(body)))
			var got struct{ Code string }
			if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusBadRequest || got.Code != "invalid_request" {
				t.Fatalf("client choices = %d %+v", w.Code, got)
			}
		})
	}
}

func TestForkAgentHTTPReturnsCreatedDestination(t *testing.T) {
	want := holons.AgentSession{ID: "fork", HolonID: "h", AgentType: "codex", Title: "Fork", Status: string(holons.StatusRunning)}
	h := New(forkAgentWorkflow{forkAgent: func(_ context.Context, id, sourceID string) (holons.AgentSession, error) {
		if id != "h" || sourceID != "source" {
			t.Fatalf("fork path IDs = %q, %q", id, sourceID)
		}
		return want, nil
	}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/holons/h/agent-sessions/source/fork", nil))
	var got holons.AgentSession
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusCreated || got.ID != want.ID || got.HolonID != want.HolonID || got.Title != want.Title {
		t.Fatalf("fork response = %d %+v", w.Code, got)
	}
}

func TestForkAgentHTTPProjectsSourceErrors(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{holons.ErrAgentSessionNotFound, http.StatusNotFound, "agent_session_not_found"},
		{holons.ErrAgentSessionNotForkable, http.StatusConflict, "agent_session_not_forkable"},
		{agentsessions.ErrUnavailable, http.StatusServiceUnavailable, "harness_unavailable"},
	} {
		h := New(forkAgentWorkflow{forkAgent: func(context.Context, string, string) (holons.AgentSession, error) {
			return holons.AgentSession{}, test.err
		}})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/holons/h/agent-sessions/source/fork", nil))
		var got struct{ Code string }
		if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if w.Code != test.status || got.Code != test.code {
			t.Fatalf("fork error response = %d %+v", w.Code, got)
		}
	}
}

func handler(t *testing.T) (http.Handler, *sql.DB) {
	t.Helper()
	db, e := database.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	s, e := sqliteadapter.New(context.Background(), db)
	if e != nil {
		t.Fatal(e)
	}
	return New(holons.NewServiceWithRepository(s, inspectionRepository{})), db
}

type inspectionRepository struct{}

func (inspectionRepository) CreateWorkspace(_ context.Context, _, baseCommit string) (holons.Workspace, error) {
	return holons.Workspace{Branch: "holark/test", Path: "/tmp/test", BaseCommit: baseCommit}, nil
}
func (inspectionRepository) InspectWorkspace(context.Context, string) (holons.WorkspaceInspection, error) {
	return holons.WorkspaceInspection{Branch: "holark/h", BaseCommit: "base", HeadCommit: "head", Clean: false, Changes: []holons.WorkspaceChange{{Path: "main.go", Status: "M", Patch: "@@", Binary: true, Truncated: true}}}, nil
}
func (inspectionRepository) PublishWorkspace(context.Context, string, holons.Publish) (holons.PublishedWorkspace, error) {
	return holons.PublishedWorkspace{}, nil
}
func (inspectionRepository) RemoveWorkspace(context.Context, string) error { return nil }

func TestHarnessErrorsUseActionableHTTPResponses(t *testing.T) {
	tests := []struct {
		err        error
		wantStatus int
		wantBody   string
	}{
		{err: errors.Join(agentsettings.ErrUnavailable, errors.New("codex: executable not found")), wantStatus: http.StatusServiceUnavailable, wantBody: "executable not found"},
		{err: agentsettings.ErrNoAvailable, wantStatus: http.StatusServiceUnavailable, wantBody: "no agent harness is available"},
		{err: agentsettings.ErrInvalid, wantStatus: http.StatusBadRequest, wantBody: "Choose a supported agent harness."},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		fail(response, test.err)
		if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantBody) {
			t.Fatalf("fail(%v) = %d %s", test.err, response.Code, response.Body.String())
		}
	}
}

func TestEmptyHolonCollectionsSerializeAsArrays(t *testing.T) {
	h, db := handler(t)
	defer db.Close()

	for _, path := range []string{"/api/v1/holons", "/api/v1/pull-requests/missing/holon-activity"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		if got := w.Body.String(); got != "{\"holons\":[]}\n" {
			t.Fatalf("GET %s returned %s", path, got)
		}
	}
}

func TestWorkspaceInspectionHTTPPreservesFileProjection(t *testing.T) {
	db, e := database.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	store, e := sqliteadapter.New(context.Background(), db)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	if e = store.Create(context.Background(), holons.Holon{ID: "h", Title: "H", Prompt: "p", Kind: holons.KindNormal, Status: holons.StatusRunning, BaseBranch: "main", BaseCommit: "base", WorktreeBranch: "holark/h", WorktreePath: "/w", CreatedAt: now}); e != nil {
		t.Fatal(e)
	}
	h := New(holons.NewServiceWithRepository(store, inspectionRepository{}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/holons/h/workspace", nil))
	if w.Code != 200 {
		t.Fatalf("workspace: %d %s", w.Code, w.Body.String())
	}
	var got holons.WorkspaceInspection
	if e = json.NewDecoder(w.Body).Decode(&got); e != nil {
		t.Fatal(e)
	}
	if got.BaseBranch != "main" || got.HeadCommit != "head" || !got.HasChanges || !got.Dirty || len(got.Changes) != 1 || got.Changes[0].Patch != "@@" || !got.Changes[0].Binary || !got.Changes[0].Truncated {
		t.Fatalf("inspection=%+v", got)
	}
}
func TestHolonHTTPProjectsFrontendCompatibilityFields(t *testing.T) {
	db, e := database.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	store, e := sqliteadapter.New(context.Background(), db)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	value := holons.Holon{
		ID: "h", Title: "H", Prompt: "p", Kind: holons.KindNormal, Status: holons.StatusRunning,
		BaseCommit: "base", WorktreeBranch: "holark/h", WorktreePath: "/w", CreatedAt: now,
		AgentSessions: []holons.AgentSession{
			{ID: "running", HolonID: "h", AgentType: "codex", Title: "Agent", Status: "running", InputState: "task_complete", CreatedAt: now, UpdatedAt: now},
			{ID: "permission", HolonID: "h", AgentType: "opencode", Title: "Agent 2", Status: "queued", InputState: "permission_required", CreatedAt: now, UpdatedAt: now.Add(time.Second)},
		},
	}
	if e = store.Create(context.Background(), value); e != nil {
		t.Fatal(e)
	}
	service := holons.NewService(store)
	open, e := service.AddManualTerminal(context.Background(), "h", "Shell", "/w", "pty-open")
	if e != nil {
		t.Fatal(e)
	}
	closed, e := service.AddManualTerminal(context.Background(), "h", "Closed", "/w", "pty-closed")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = service.CloseManualTerminal(context.Background(), "h", closed.ManualTerminals[len(closed.ManualTerminals)-1].ID); e != nil {
		t.Fatal(e)
	}
	if len(open.ManualTerminals) != 1 {
		t.Fatalf("open terminal fixture = %+v", open.ManualTerminals)
	}
	h := New(service, Options{RepositoryID: "repo-local", RuntimeID: "runtime-local"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/holons/h", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		RepositoryID           string                  `json:"repository_id"`
		RuntimeID              string                  `json:"runtime_id"`
		WorkSessionStartCommit string                  `json:"work_session_start_commit"`
		ProposedUpstreamBranch string                  `json:"proposed_upstream_branch"`
		AgentSession           *holons.AgentSession    `json:"agent_session"`
		AgentSessions          []holons.AgentSession   `json:"agent_sessions"`
		ManualTerminals        []holons.ManualTerminal `json:"manual_terminals"`
		IDEs                   []holons.IDE            `json:"ides"`
		InputState             string                  `json:"input_state"`
	}
	if e = json.NewDecoder(w.Body).Decode(&got); e != nil {
		t.Fatal(e)
	}
	if got.RepositoryID != "repo-local" || got.RuntimeID != "runtime-local" || got.WorkSessionStartCommit != "base" || got.ProposedUpstreamBranch != "holark/h" || got.InputState != "permission_required" {
		t.Fatalf("projection = %+v", got)
	}
	if got.AgentSession == nil || got.AgentSession.ID != "running" || len(got.AgentSessions) != 2 || got.ManualTerminals == nil || len(got.ManualTerminals) != 1 || got.ManualTerminals[0].TerminalID != "pty-open" || got.IDEs == nil {
		t.Fatalf("nested projection = %+v", got)
	}
}

func TestSingletonHolonAndNestedAgentSessionContracts(t *testing.T) {
	h, db := handler(t)
	defer db.Close()
	body := []byte(`{"title":"Work","prompt":"fix it","kind":"normal","base_branch":"main","base_commit":"abc","agent_type":"codex"}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/holons", bytes.NewReader(body)))
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created holons.Holon
	if e := json.NewDecoder(w.Body).Decode(&created); e != nil {
		t.Fatal(e)
	}
	if created.ID == "" || len(created.AgentSessions) != 1 {
		t.Fatalf("created = %+v", created)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/holons/"+created.ID+"/agent-sessions", nil))
	if w.Code != 200 {
		t.Fatalf("agents: %d %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("harness")) || !bytes.Contains(w.Body.Bytes(), []byte("agent_sessions")) {
		t.Fatalf("legacy contract leaked: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/holons/"+created.ID+"/agent-sessions", bytes.NewBufferString(`{"agent_type":"opencode","prompt":"continue"}`)))
	var added holons.AgentSession
	if w.Code != http.StatusCreated || json.NewDecoder(w.Body).Decode(&added) != nil || added.HolonID != created.ID || added.AgentType != "opencode" {
		t.Fatalf("added agent: %d %+v", w.Code, added)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/holons/"+created.ID+"/terminals", bytes.NewBufferString(`{"title":"Shell","cwd":"/tmp","terminal_id":"pty-1"}`)))
	var terminal holons.ManualTerminal
	if w.Code != http.StatusCreated || json.NewDecoder(w.Body).Decode(&terminal) != nil || terminal.HolonID != created.ID || terminal.TerminalID != "pty-1" {
		t.Fatalf("added terminal: %d %+v", w.Code, terminal)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/holons/"+created.ID+"/terminals", nil))
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"manual_terminals"`)) || !bytes.Contains(w.Body.Bytes(), []byte(`"pty-1"`)) {
		t.Fatalf("terminals: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/holons/"+created.ID+"/reopen", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("reopen conflict: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/holons/"+created.ID+"/end", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/holons/"+created.ID+"/end", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("second end: %d %s", w.Code, w.Body.String())
	}
}
