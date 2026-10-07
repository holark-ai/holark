package pullrequestmetadata

import (
	"context"
	"errors"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"strings"
	"testing"
	"time"
)

func TestImproveResumesTerminalLinkedSession(t *testing.T) {
	for _, status := range []string{"completed", "expired"} {
		t.Run(status, func(t *testing.T) {
			store := newAgentTestStore()
			store.snapshot.MetadataSessionID = "session-existing"
			agents := &agentTestSessions{state: AgentSession{ID: "session-existing", RuntimeID: "node-old", Status: status, InputState: "none"}}
			service := New(store, Options{Projection: store, AgentSessions: agents})

			metadata, err := service.StartAgent(t.Context(), "pr-1", AgentRequest{Action: ActionImprove, Instruction: "Shorter.", RuntimeID: "node-old", Terminal: TerminalSize{Columns: 120, Rows: 40}})
			if err != nil {
				t.Fatal(err)
			}
			if agents.resumed.SessionID != "session-existing" || agents.resumed.RuntimeID != "node-old" || agents.resumed.RepositoryID != "project-1" ||
				agents.resumed.RepositoryURL != "git@example.test/repo.git" || agents.resumed.BaseBranch != "main" || agents.resumed.ArtifactPath != MetadataArtifactPath {
				t.Fatalf("resume request = %+v", agents.resumed)
			}
			if metadata.AgentSession == nil || metadata.AgentSession.ID != "session-existing" || metadata.AgentSession.Status != "queued" {
				t.Fatalf("metadata = %+v", metadata)
			}
		})
	}
}

func TestMetadataActionsRejectEveryActiveSessionState(t *testing.T) {
	for _, state := range []AgentSession{
		{ID: "session-existing", Status: "queued", InputState: "none"},
		{ID: "session-existing", Status: "preparing", InputState: "none"},
		{ID: "session-existing", Status: "running", InputState: "none"},
		{ID: "session-existing", Status: "running", InputState: "task_complete"},
	} {
		store := newAgentTestStore()
		store.snapshot.MetadataSessionID = state.ID
		service := New(store, Options{Projection: store, AgentSessions: &agentTestSessions{state: state}})
		if _, err := service.Update(t.Context(), "pr-1", Request{Title: ptr("blocked")}); !errors.Is(err, ErrAgentTurnActive) {
			t.Fatalf("update in state %+v: %v", state, err)
		}
		if _, err := service.StartAgent(t.Context(), "pr-1", AgentRequest{Action: ActionImprove, RuntimeID: "node-old", Terminal: TerminalSize{Columns: 120, Rows: 40}}); !errors.Is(err, ErrAgentTurnActive) {
			t.Fatalf("action in state %+v: %v", state, err)
		}
	}
}

func TestImproveRequiresRegenerateForNonResumableLinkedSession(t *testing.T) {
	for _, status := range []string{"failed", "lost", "cancelled"} {
		store := newAgentTestStore()
		store.snapshot.MetadataSessionID = "session-existing"
		service := New(store, Options{Projection: store, AgentSessions: &agentTestSessions{state: AgentSession{ID: "session-existing", Status: status}}})
		_, err := service.StartAgent(t.Context(), "pr-1", AgentRequest{Action: ActionImprove, RuntimeID: "node-old", Terminal: TerminalSize{Columns: 120, Rows: 40}})
		if !errors.Is(err, ErrAgentSessionUnavailable) {
			t.Fatalf("StartAgent in status %q: %v", status, err)
		}
	}
}

func TestImproveRejectsEmptyMetadata(t *testing.T) {
	store := newAgentTestStore()
	store.snapshot.Title = ""
	store.snapshot.Description = ""
	service := New(store, Options{Projection: store, AgentSessions: &agentTestSessions{}})

	if _, err := service.StartAgent(t.Context(), "pr-1", AgentRequest{
		Action: ActionImprove, RuntimeID: "node-1", Terminal: TerminalSize{Columns: 120, Rows: 40},
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("StartAgent error = %v", err)
	}
}

func TestArtifactFailuresPreserveMetadataAndBecomeTurnErrors(t *testing.T) {
	tests := []struct {
		name       string
		artifact   Artifact
		consumeErr error
	}{
		{name: "missing", consumeErr: errors.New("artifact not found")},
		{name: "wrong path", artifact: Artifact{Path: ".holark/other.json", Data: []byte(`{"title":"New","description":"Body"}`)}},
		{name: "malformed", artifact: Artifact{Path: MetadataArtifactPath, Data: []byte(`{"title":`)}},
		{name: "unknown field", artifact: Artifact{Path: MetadataArtifactPath, Data: []byte(`{"title":"New","description":"Body","extra":true}`)}},
		{name: "oversized", artifact: Artifact{Path: MetadataArtifactPath, Data: []byte(strings.Repeat("x", MaxArtifactBytes+1))}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newAgentTestStore()
			store.snapshot.MetadataSessionID = "session-1"
			agents := &agentTestSessions{state: AgentSession{ID: "session-1", Status: "running", InputState: "task_complete"}, artifact: test.artifact, consumeErr: test.consumeErr}
			service := New(store, Options{Projection: store, AgentSessions: agents})
			if err := service.ImportArtifact(t.Context(), "session-1"); err == nil {
				t.Fatal("expected import error")
			}
			if store.snapshot.Title != "Current title" || store.snapshot.Description != "Current description" || agents.turnError == "" {
				t.Fatalf("snapshot = %+v error = %q", store.snapshot, agents.turnError)
			}
		})
	}
}

func TestProviderFailureDuringImportPreservesCanonicalMetadata(t *testing.T) {
	store := newAgentTestStore()
	store.snapshot.MetadataSessionID = "session-1"
	store.snapshot.SyncProvider = "github"
	agents := &agentTestSessions{
		state:    AgentSession{ID: "session-1", Status: "running", InputState: "task_complete"},
		artifact: Artifact{Path: MetadataArtifactPath, Data: []byte(`{"title":"New","description":"Body"}`)},
	}
	service := New(store, Options{Projection: store, AgentSessions: agents, Provider: failingAgentProvider{}})

	if err := service.ImportArtifact(t.Context(), "session-1"); !errors.Is(err, ErrProviderFailed) {
		t.Fatalf("ImportArtifact error = %v", err)
	}
	if store.snapshot.Title != "Current title" || store.snapshot.Description != "Current description" || agents.turnError != "" || store.snapshot.State.ApplicationError == nil {
		t.Fatalf("snapshot = %+v error = %q", store.snapshot, agents.turnError)
	}
}

func TestPreparedArtifactProviderFailurePreservesGeneratedMetadataAndPreparation(t *testing.T) {
	store := newAgentTestStore()
	store.snapshot.Status = "wip"
	store.snapshot.MetadataSessionID = "session-1"
	store.snapshot.PreparationTarget = PreparationTargetOpen
	agents := &agentTestSessions{
		state:    AgentSession{ID: "session-1", Status: "running", InputState: "task_complete"},
		artifact: Artifact{Path: MetadataArtifactPath, Data: []byte(`{"title":"Generated","description":"Ready"}`)},
	}
	lifecycle := &agentTestLifecycle{err: errors.New("provider failed")}
	service := New(store, Options{Projection: store, AgentSessions: agents, Lifecycle: lifecycle})

	if err := service.ImportArtifact(t.Context(), "session-1"); err == nil {
		t.Fatal("expected transition failure")
	}
	if store.snapshot.Title != "Generated" || store.snapshot.Description != "Ready" || store.snapshot.PreparationTarget != PreparationTargetOpen {
		t.Fatalf("snapshot = %+v", store.snapshot)
	}
	if agents.turnError != "" || store.snapshot.State.ApplicationError == nil {
		t.Fatal("expected application error without agent failure")
	}
}

func TestRegeneratePromptExcludesRequestState(t *testing.T) {
	store := newAgentTestStore()
	store.snapshot.Status = "wip"
	store.snapshot.PreparationTarget = PreparationTargetOpen
	agents := &agentTestSessions{}
	service := New(store, Options{Projection: store, AgentSessions: agents})

	if _, err := service.StartAgent(t.Context(), "pr-1", AgentRequest{
		Action: ActionRegenerate, Instruction: "Emphasize the migration.", RuntimeID: "node-1", Terminal: TerminalSize{Columns: 120, Rows: 40},
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(agents.startedPrompt, "preparation_target") || strings.Contains(agents.startedPrompt, "auto_transition") ||
		strings.Contains(agents.startedPrompt, "Current title") || strings.Contains(agents.startedPrompt, "Current description") ||
		strings.Contains(agents.startedPrompt, "Emphasize the migration.") || !strings.Contains(agents.startedPrompt, store.snapshot.DiffBaseCommit) {
		t.Fatalf("invalid regenerate prompt: %q", agents.startedPrompt)
	}
}

func TestRegenerateRetirementFailureDoesNotBlockReplacement(t *testing.T) {
	store := newAgentTestStore()
	store.snapshot.MetadataSessionID = "session-old"
	agents := &agentTestSessions{retireErr: errors.New("old node unavailable")}
	service := New(store, Options{Projection: store, AgentSessions: agents})

	metadata, err := service.StartAgent(t.Context(), "pr-1", AgentRequest{
		Action: ActionRegenerate, RuntimeID: "node-1", Terminal: TerminalSize{Columns: 120, Rows: 40},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(agents.retired) != 1 || agents.retired[0] != "session-old" || metadata.AgentSession == nil || metadata.AgentSession.ID != "session-1" || store.snapshot.MetadataSessionID != "session-1" {
		t.Fatalf("retired = %+v metadata = %+v snapshot = %+v", agents.retired, metadata, store.snapshot)
	}
}

func TestRetireInactiveClearsLinkOnlyAfterSuccessfulDispatch(t *testing.T) {
	for _, test := range []struct {
		name      string
		retireErr error
		wantLink  string
	}{
		{name: "success"},
		{name: "dispatch failure retains link", retireErr: errors.New("node unavailable"), wantLink: "session-old"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newAgentTestStore()
			store.snapshot.Status = "closed"
			store.snapshot.MetadataSessionID = "session-old"
			agents := &agentTestSessions{retireErr: test.retireErr}
			service := New(store, Options{Projection: store, AgentSessions: agents})

			err := service.RetireInactive(t.Context(), "pr-1")
			if (test.retireErr != nil) != (err != nil) {
				t.Fatalf("RetireInactive error = %v", err)
			}
			if store.snapshot.MetadataSessionID != test.wantLink || len(agents.retired) != 1 {
				t.Fatalf("link = %q retired = %+v", store.snapshot.MetadataSessionID, agents.retired)
			}
		})
	}
}

func newAgentTestStore() *agentTestStore {
	return &agentTestStore{snapshot: Snapshot{
		ID: "pr-1", RepositoryID: "project-1", RepositoryURL: "git@example.test/repo.git", Title: "Current title", Description: "Current description",
		Status: "open", BaseBranch: "main", BaseCommit: "base-1", HeadBranch: "feature", HeadCommit: "head-1", DiffBaseCommit: "base-1", UpdatedAt: time.Now(),
	}}
}

type agentTestStore struct {
	pullrequestlifecycle.ActionRegistry
	snapshot Snapshot
}

func (store *agentTestStore) Get(_ context.Context, id string) (Snapshot, error) {
	if id != store.snapshot.ID {
		return Snapshot{}, ErrPullRequestNotFound
	}
	return store.snapshot, nil
}
func (store *agentTestStore) GetByMetadataSession(_ context.Context, id string) (Snapshot, error) {
	if id != store.snapshot.MetadataSessionID {
		return Snapshot{}, ErrPullRequestNotFound
	}
	return store.snapshot, nil
}
func (store *agentTestStore) Update(_ context.Context, id, requestID, title, description string, updatedAt time.Time, state State) error {
	store.snapshot.State = state
	store.snapshot.Title, store.snapshot.Description, store.snapshot.UpdatedAt = title, description, updatedAt
	return nil
}
func (store *agentTestStore) SetMetadataSession(_ context.Context, id, sessionID string) error {
	store.snapshot.MetadataSessionID = sessionID
	return nil
}
func (store *agentTestStore) ClearMetadataSession(_ context.Context, id, expectedSessionID string) (bool, error) {
	if id == store.snapshot.ID && store.snapshot.MetadataSessionID == expectedSessionID {
		store.snapshot.MetadataSessionID = ""
		return true, nil
	}
	return false, nil
}

func (store *agentTestStore) MarkPrepared(_ context.Context, id, headCommit string) error {
	store.snapshot.PreparedHeadCommit = headCommit
	return nil
}

func (store *agentTestStore) InitializePreparation(_ context.Context, id string, target PreparationTarget) error {
	store.snapshot.PreparationTarget = target
	return nil
}

type agentTestTransition struct {
	id     string
	target PreparationTarget
}

type agentTestLifecycle struct {
	transitions []agentTestTransition
	err         error
}

func (lifecycle *agentTestLifecycle) Transition(_ context.Context, id string, target PreparationTarget) error {
	lifecycle.transitions = append(lifecycle.transitions, agentTestTransition{id: id, target: target})
	return lifecycle.err
}

type agentTestSessions struct {
	state                    AgentSession
	artifact                 Artifact
	consumeErr               error
	startedPrompt, turnError string
	resumed                  ResumeAgentSessionRequest
	retired                  []string
	retireErr                error
}

func (sessions *agentTestSessions) Start(_ context.Context, request StartAgentSessionRequest) (AgentSession, error) {
	sessions.startedPrompt = request.Prompt
	sessions.state = AgentSession{ID: "session-1", RuntimeID: request.RuntimeID, Status: "queued", InputState: "none"}
	return sessions.state, nil
}
func (sessions *agentTestSessions) Resume(_ context.Context, request ResumeAgentSessionRequest) (AgentSession, error) {
	sessions.resumed = request
	sessions.state.Status, sessions.state.InputState, sessions.state.Error = "queued", "none", ""
	return sessions.state, nil
}
func (sessions *agentTestSessions) Inspect(_ context.Context, id string) (AgentSession, error) {
	return sessions.state, nil
}
func (sessions *agentTestSessions) ConsumeArtifact(_ context.Context, id string) (Artifact, error) {
	return sessions.artifact, sessions.consumeErr
}
func (sessions *agentTestSessions) SetTurnError(_ context.Context, id, message string) error {
	sessions.turnError = message
	sessions.state.Error = message
	return nil
}
func (sessions *agentTestSessions) ClearTurnError(_ context.Context, id string) error {
	sessions.turnError = ""
	sessions.state.Error = ""
	return nil
}
func (sessions *agentTestSessions) Retire(_ context.Context, id string) error {
	sessions.retired = append(sessions.retired, id)
	return sessions.retireErr
}

type failingAgentProvider struct{}

func (failingAgentProvider) Update(context.Context, Snapshot, string, string) (time.Time, error) {
	return time.Time{}, ErrProviderFailed
}
func ptr(value string) *string { return &value }

func (store *agentTestStore) SaveState(_ context.Context, id string, state State) error {
	store.snapshot.State = state
	return nil
}

func (store *agentTestStore) ClearPreparation(context.Context, string) error {
	store.snapshot.PreparationTarget = ""
	store.snapshot.PreparedHeadCommit = ""
	return nil
}

func (store *agentTestStore) BeginOperation(_ context.Context, operation pullrequestlifecycle.Operation) (pullrequestlifecycle.Operation, bool, error) {
	return operation, true, nil
}
func (store *agentTestStore) CompleteOperation(context.Context, string, string, string) (pullrequestlifecycle.Operation, error) {
	return pullrequestlifecycle.Operation{}, nil
}
func (store *agentTestStore) GetPullRequest(id string) (pullrequestlifecycle.PullRequest, bool) {
	snapshot := store.snapshot
	return pullrequestlifecycle.PullRequest{ID: snapshot.ID, Title: snapshot.Title, Summary: snapshot.Description, HeadCommit: snapshot.HeadCommit, DiffBaseCommit: snapshot.DiffBaseCommit, ViewRevision: 1}, id == snapshot.ID
}

func (store *agentTestStore) GetOperation(context.Context, string) (pullrequestlifecycle.Operation, bool, error) {
	return pullrequestlifecycle.Operation{}, false, nil
}
