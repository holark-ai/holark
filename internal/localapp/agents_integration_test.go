package localapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/harness/claudecode"
	"github.com/holark-ai/holark/internal/harness/codex"
	"github.com/holark-ai/holark/internal/harness/opencode"
	"github.com/holark-ai/holark/internal/holons"
	holonshttp "github.com/holark-ai/holark/internal/holons/httpapi"
	holonssqlite "github.com/holark-ai/holark/internal/holons/sqliteadapter"
	"github.com/holark-ai/holark/internal/prompt_templates"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/terminalenv"
	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
	"github.com/holark-ai/holark/internal/terminals/localadapter"
)

func TestLocalAgentLauncherProjectsFakeProcessCompletion(t *testing.T) {
	manager, err := terminalhost.NewManager(terminalhost.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	launcher := localAgentLauncher{manager: manager, terminalContext: terminalenv.Context{
		ExecutablePath:   "/opt/holark/bin/holark",
		RuntimeDirectory: "/run/holark-custom",
	}}
	id, err := terminals.NewID()
	if err != nil {
		t.Fatal(err)
	}
	assertEnvironment := `test "$HOLARK_RUNTIME_DIR" = /run/holark-custom &&
test "${PATH%%:*}" = /opt/holark/bin &&
test -z "$HOLARK_SERVER_URL" &&
test -z "$HOLARK_CLI_AUTH_TOKEN" &&
test "$HOLARK_HOLON_ID" = holon-one &&
test "$HOLARK_AGENT_SESSION_ID" = agent-one &&
test "$HOLARK_REPO_PATH" = /work/repository &&
exit 7
exit 9`
	if _, err = launcher.Launch(t.Context(), terminals.LaunchSpec{
		TerminalID: id, Kind: terminals.LaunchCommand, Command: "/bin/sh", Arguments: []string{"-c", assertEnvironment},
		Environment: []string{
			"PATH=/bin:/usr/bin",
			"HOLARK_SERVER_URL=http://127.0.0.1:8080",
			"HOLARK_CLI_AUTH_TOKEN=stale-token",
			"HOLARK_HOLON_ID=holon-one",
			"HOLARK_AGENT_SESSION_ID=agent-one",
			"HOLARK_REPO_PATH=/work/repository",
		}, CWD: t.TempDir(), Dimensions: terminals.Dimensions{Columns: 80, Rows: 24},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var completions []terminals.ProcessCompletion
	for len(completions) == 0 && ctx.Err() == nil {
		completions, err = manager.Completions(ctx, nil, 50*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(completions) != 1 || completions[0].TerminalID != id || completions[0].ExitCode != 7 {
		t.Fatalf("completions=%+v", completions)
	}
}

type fixedPromptReader string

func (reader fixedPromptReader) Read(context.Context, string) string { return string(reader) }

func TestLocalAgentStateProjectsPersistedInputState(t *testing.T) {
	service, store := terminalTestService(t)
	now := time.Now().UTC()
	if err := store.Create(t.Context(), holons.Holon{
		ID: "idle-holon", Title: "Idle", Kind: holons.KindNormal, Status: holons.StatusRunning,
		BaseCommit: "head-1", WorktreeBranch: "holark/idle", WorktreePath: t.TempDir(), CreatedAt: now,
		AgentSessions: []holons.AgentSession{{
			ID: "idle-agent", HolonID: "idle-holon", AgentType: "codex", Title: "Agent", Status: string(holons.StatusRunning),
			InputState: string(protocol.InputTaskComplete), CreatedAt: now, UpdatedAt: now,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	session, err := (&localAgentState{holons: service}).Session(t.Context(), "idle-holon", "idle-agent")
	if err != nil {
		t.Fatal(err)
	}
	if session.InputState != protocol.InputTaskComplete {
		t.Fatalf("input state = %q, want %q", session.InputState, protocol.InputTaskComplete)
	}
}

func TestPullRequestCommitFollowUpUsesSavedTemplateAndArtifactPath(t *testing.T) {
	state := &localAgentState{templates: fixedPromptReader("CUSTOM {{artifact_path}}")}
	prompt := state.commitFollowUp(t.Context(), pullrequestwork.Work{Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto})
	if prompt != "CUSTOM .holark/comment-reply.json" {
		t.Fatalf("prompt=%q", prompt)
	}
	prompt = state.commitFollowUp(t.Context(), pullrequestwork.Work{Kind: pullrequestwork.KindRebase})
	if prompt != "CUSTOM .holark/rebase_complete" {
		t.Fatalf("rebase prompt=%q", prompt)
	}
}

type metadataImporterRecorder struct {
	sessionID string
	err       error
}

func (recorder *metadataImporterRecorder) ImportArtifact(_ context.Context, sessionID string) error {
	recorder.sessionID = sessionID
	return recorder.err
}

type localAgentRuntimeRecorder struct {
	cancelledHolon string
	cancelledAgent string
	inputs         []string
	inputErr       error
	cancelInput    context.CancelFunc
}

func (*localAgentRuntimeRecorder) Launch(context.Context, string, string, terminals.Dimensions, string, agentsessions.LaunchOptions) error {
	return nil
}

func (recorder *localAgentRuntimeRecorder) Cancel(_ context.Context, holonID, agentID string) error {
	recorder.cancelledHolon = holonID
	recorder.cancelledAgent = agentID
	return nil
}

func (recorder *localAgentRuntimeRecorder) Submit(_ context.Context, _, _ string, input string) error {
	recorder.inputs = append(recorder.inputs, input)
	if recorder.cancelInput != nil {
		recorder.cancelInput()
		recorder.cancelInput = nil
	}
	return recorder.inputErr
}

func TestMetadataTaskCompletionImportsExpiresAndStopsAgent(t *testing.T) {
	service, store := terminalTestService(t)
	now := time.Now().UTC()
	const resumeTarget = "codex-thread-metadata"
	if err := store.Create(t.Context(), holons.Holon{
		ID: "metadata-holon", Title: "Metadata", Kind: holons.KindPRMetadata, Status: holons.StatusRunning,
		BaseCommit: "head-1", WorktreeBranch: "holark/metadata", WorktreePath: t.TempDir(), PullRequestID: "pr-1", CreatedAt: now,
		AgentSessions: []holons.AgentSession{{
			ID: "metadata-agent", HolonID: "metadata-holon", TerminalID: "terminal-metadata", AgentType: "codex", Title: "Agent",
			Status: string(holons.StatusRunning), ResumeTarget: resumeTarget, CreatedAt: now, UpdatedAt: now, StartedAt: &now,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	importer := &metadataImporterRecorder{}
	runtime := &localAgentRuntimeRecorder{}
	state := &localAgentState{holons: service, metadata: importer, agents: runtime}
	if err := state.Observed(t.Context(), "metadata-holon", "metadata-agent", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
		t.Fatal(err)
	}
	got, err := service.Get(t.Context(), "metadata-holon")
	if err != nil {
		t.Fatal(err)
	}
	if importer.sessionID != got.ID || runtime.cancelledHolon != got.ID || runtime.cancelledAgent != "metadata-agent" {
		t.Fatalf("imported=%q cancelled=%q/%q", importer.sessionID, runtime.cancelledHolon, runtime.cancelledAgent)
	}
	if got.Status != holons.StatusExpired || got.AgentSessions[0].Status != string(holons.StatusExpired) || got.AgentSessions[0].InputState != string(protocol.InputTaskComplete) || got.AgentSessions[0].ResumeTarget != resumeTarget {
		t.Fatalf("metadata holon after completion = %+v", got)
	}

	products := &localTerminalProducts{holons: service}
	if err = products.ApplyTerminalCompletion("local", terminals.ProcessCompletion{TerminalID: "terminal-metadata", ExitCode: -1}); err != nil {
		t.Fatal(err)
	}
	got, err = service.Get(t.Context(), "metadata-holon")
	if err != nil || got.Status != holons.StatusExpired || got.AgentSessions[0].Status != string(holons.StatusExpired) {
		t.Fatalf("metadata completion was overwritten: holon=%+v err=%v", got, err)
	}
}

func TestMetadataTaskCompletionExpiresWithImportError(t *testing.T) {
	service, store := terminalTestService(t)
	now := time.Now().UTC()
	if err := store.Create(t.Context(), holons.Holon{
		ID: "metadata-error", Title: "Metadata", Kind: holons.KindPRMetadata, Status: holons.StatusRunning,
		BaseCommit: "head-1", WorktreeBranch: "holark/metadata-error", WorktreePath: t.TempDir(), PullRequestID: "pr-1", CreatedAt: now,
		AgentSessions: []holons.AgentSession{{ID: "agent-error", HolonID: "metadata-error", TerminalID: "terminal-error", AgentType: "codex", Title: "Agent", Status: string(holons.StatusRunning), CreatedAt: now, UpdatedAt: now}},
	}); err != nil {
		t.Fatal(err)
	}
	state := &localAgentState{holons: service, metadata: &metadataImporterRecorder{err: errors.New("missing artifact")}}
	if err := state.Observed(t.Context(), "metadata-error", "agent-error", harness.Event{InputState: protocol.InputTaskComplete}); err == nil {
		t.Fatal("expected import error")
	}
	got, err := service.Get(t.Context(), "metadata-error")
	if err != nil || got.Status != holons.StatusExpired || got.AgentSessions[0].Reason == "" {
		t.Fatalf("metadata error completion = %+v, err=%v", got, err)
	}
}

type agentWorkCompletionRecorder struct {
	calls       int
	disposition pullRequestWorkCompletionDisposition
}

func (r *agentWorkCompletionRecorder) Complete(context.Context, holons.Holon) (pullRequestWorkCompletionDisposition, error) {
	r.calls++
	return r.disposition, nil
}

func specializedAgentState(t *testing.T, kind holons.Kind, mode pullrequestwork.Mode, inspection holons.WorkspaceInspection) (*localAgentState, *holons.Service, *localAgentRuntimeRecorder, *agentWorkCompletionRecorder) {
	return specializedAgentStateWithRepository(t, kind, mode, artifactRepository{inspection: inspection})
}

func specializedAgentStateWithRepository(t *testing.T, kind holons.Kind, mode pullrequestwork.Mode, repository holons.Repository) (*localAgentState, *holons.Service, *localAgentRuntimeRecorder, *agentWorkCompletionRecorder) {
	t.Helper()
	_, store := terminalTestService(t)
	now := time.Now().UTC()
	h := holons.Holon{ID: "specialized", Title: "Specialized", Kind: kind, Status: holons.StatusRunning, BaseCommit: "head", WorktreeBranch: "holark/specialized", WorktreePath: t.TempDir(), PullRequestID: "pr-1", CreatedAt: now, AgentSessions: []holons.AgentSession{{ID: "agent", HolonID: "specialized", TerminalID: "terminal", AgentType: "codex", Title: "Agent", Status: string(holons.StatusRunning), CreatedAt: now, UpdatedAt: now}}}
	if err := store.Create(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	service := holons.NewServiceWithRepository(store, repository)
	runtime := &localAgentRuntimeRecorder{}
	completion := &agentWorkCompletionRecorder{disposition: pullRequestWorkCompletionDisposition{Terminal: true}}
	workKind := pullrequestwork.KindWorker
	if kind == holons.KindRebase {
		workKind = pullrequestwork.KindRebase
	}
	work := &completionWorkStub{work: pullrequestwork.Work{SessionID: h.ID, Kind: workKind, Mode: mode, Status: pullrequestwork.StatusRunning}}
	return &localAgentState{holons: service, work: work, workCompletion: completion, agents: runtime}, service, runtime, completion
}

func TestSpecializedDirtyTaskCompletionRequestsCommitBeforeFinalizing(t *testing.T) {
	for _, test := range []struct {
		name       string
		kind       holons.Kind
		mode       pullrequestwork.Mode
		wantInputs int
		wantPrompt string
	}{
		{name: "auto", kind: holons.KindPullWorker, mode: pullrequestwork.ModeAuto, wantInputs: 1, wantPrompt: "continued_for_now"},
		{name: "assisted", kind: holons.KindPullWorker, mode: pullrequestwork.ModeAssisted, wantPrompt: "dirty_prompt_visible"},
		{name: "rebase", kind: holons.KindRebase, wantInputs: 1, wantPrompt: "continued_for_now"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, service, runtime, completion := specializedAgentState(t, test.kind, test.mode, holons.WorkspaceInspection{HeadCommit: "head", Dirty: true, Changes: []holons.WorkspaceChange{{Path: "file.go", Status: "M"}}})
			if test.kind == holons.KindRebase {
				h, _ := service.Get(t.Context(), "specialized")
				if err := os.MkdirAll(filepath.Join(h.WorktreePath, ".holark"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(h.WorktreePath, pullRequestWorkArtifactPath(pullrequestwork.KindRebase)), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if err := state.Observed(t.Context(), "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
				t.Fatal(err)
			}
			h, _ := service.Get(t.Context(), "specialized")
			if completion.calls != 0 || len(runtime.inputs) != test.wantInputs || h.AgentSessions[0].CommitPrompt == nil || h.AgentSessions[0].CommitPrompt.State != test.wantPrompt {
				t.Fatalf("completion=%d inputs=%d agent=%+v", completion.calls, len(runtime.inputs), h.AgentSessions[0])
			}
		})
	}
}

func TestRebaseTurnCompletionWaitsForMarker(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		name := "clean"
		if dirty {
			name = "dirty"
		}
		t.Run(name, func(t *testing.T) {
			inspection := holons.WorkspaceInspection{HeadCommit: "rebased-head", Dirty: dirty, Clean: !dirty}
			if dirty {
				inspection.Changes = []holons.WorkspaceChange{{Path: "file.go", Status: "M"}}
			}
			state, service, runtime, _ := specializedAgentState(t, holons.KindRebase, "", inspection)
			work := state.work.(*completionWorkStub)
			state.workCompletion = localPullRequestWorkCompletionCoordinator{holons: service, work: work}
			event := harness.Event{Type: harness.EventInputStateChanged, InputState: protocol.InputTaskComplete, Activity: protocol.ActivityCompleted}
			// Ordinary conversational questions can finish multiple turns.
			for range 2 {
				if err := state.Observed(t.Context(), "specialized", "agent", event); err != nil {
					t.Fatal(err)
				}
				h, err := service.Get(t.Context(), "specialized")
				if err != nil {
					t.Fatal(err)
				}
				agent := h.AgentSession("agent")
				if agent.InputState != string(protocol.InputUserRequired) || agent.Activity != protocol.ActivityNeedsInput || agent.Status != string(holons.StatusRunning) || h.Status != holons.StatusRunning {
					t.Fatalf("missing marker did not preserve running session waiting for input: holon=%+v agent=%+v", h, agent)
				}
			}
			// Codex loses observation, then refreshes completed activity without
			// replaying the already completed turn's input state.
			for _, observation := range []harness.Event{
				{Type: harness.EventObservabilityChanged, TerminalID: "terminal", ObservabilityStatus: protocol.ObservabilityDegraded, ObservabilityMessage: "Codex observation disconnected; reconnecting.", Activity: protocol.ActivityUnknown},
				{Type: harness.EventObservabilityChanged, TerminalID: "terminal", ObservabilityStatus: protocol.ObservabilityHealthy, Activity: protocol.ActivityCompleted},
			} {
				if err := state.Observed(t.Context(), "specialized", "agent", observation); err != nil {
					t.Fatal(err)
				}
				h, err := service.Get(t.Context(), "specialized")
				if err != nil {
					t.Fatal(err)
				}
				agent := h.AgentSession("agent")
				wantActivity := protocol.ActivityNeedsInput
				if observation.ObservabilityStatus == protocol.ObservabilityDegraded {
					wantActivity = protocol.ActivityUnknown
				}
				if agent.InputState != string(protocol.InputUserRequired) || agent.Activity != wantActivity || agent.ObservabilityStatus != string(observation.ObservabilityStatus) || agent.Status != string(holons.StatusRunning) || h.Status != holons.StatusRunning {
					t.Fatalf("reconnect lost paused rebase state: observation=%+v holon=%+v agent=%+v", observation, h, agent)
				}
			}
			for _, observation := range []harness.Event{
				{Type: harness.EventInputStateChanged, InputState: protocol.InputNone, Activity: protocol.ActivityWorking},
				{Type: harness.EventInputStateChanged, InputState: protocol.InputUserRequired, Activity: protocol.ActivityNeedsInput},
				{Type: harness.EventInputStateChanged, InputState: protocol.InputPermissionRequired, Activity: protocol.ActivityNeedsInput},
			} {
				if err := state.Observed(t.Context(), "specialized", "agent", observation); err != nil {
					t.Fatal(err)
				}
				h, err := service.Get(t.Context(), "specialized")
				if err != nil {
					t.Fatal(err)
				}
				agent := h.AgentSession("agent")
				if agent.InputState != string(observation.InputState) || agent.Activity != observation.Activity || agent.Status != string(holons.StatusRunning) {
					t.Fatalf("observation=%+v agent=%+v", observation, agent)
				}
			}
			h, err := service.Get(t.Context(), "specialized")
			if err != nil {
				t.Fatal(err)
			}
			if work.completeCalls != 0 || work.failCalls != 0 || len(runtime.inputs) != 0 || runtime.cancelledHolon != "" || h.AgentSessions[0].CommitPrompt != nil || work.work.Status != pullrequestwork.StatusRunning {
				t.Fatalf("pause changed work: work=%+v inputs=%v agent=%+v", work, runtime.inputs, h.AgentSessions[0])
			}
			marker := filepath.Join(h.WorktreePath, pullRequestWorkArtifactPath(pullrequestwork.KindRebase))
			if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := state.Observed(t.Context(), "specialized", "agent", event); err != nil {
				t.Fatal(err)
			}
			if dirty {
				if len(runtime.inputs) != 1 || work.completeCalls != 0 || work.failCalls != 0 {
					t.Fatalf("marker did not request commit: inputs=%v work=%+v", runtime.inputs, work)
				}
			} else {
				if work.completeCalls != 1 || work.failCalls != 0 {
					t.Fatalf("marker did not complete work: %+v", work)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("completed marker remains: %v", err)
				}
			}
		})
	}
}

func TestCompletedRebasePreservesAgentResumeTarget(t *testing.T) {
	state, service, _, _ := specializedAgentState(t, holons.KindRebase, "", holons.WorkspaceInspection{HeadCommit: "rebased-head", Clean: true})
	const resumeTarget = "codex-thread-rebase"
	if _, err := service.SetAgentSessionStatus(t.Context(), "specialized", "agent", holons.StatusRunning, "", resumeTarget); err != nil {
		t.Fatal(err)
	}
	h, err := service.Get(t.Context(), "specialized")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(h.WorktreePath, pullRequestWorkArtifactPath(pullrequestwork.KindRebase))
	if err = os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = state.Observed(t.Context(), h.ID, "agent", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
		t.Fatal(err)
	}
	h, err = service.Get(t.Context(), h.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent := h.AgentSession("agent")
	if agent.Status != string(holons.StatusExpired) || agent.ResumeTarget != resumeTarget {
		t.Fatalf("completed rebase agent = %+v", agent)
	}
}

func TestCommitDiscussionCompletionDoesNotReopenPromptForSameChanges(t *testing.T) {
	inspection := holons.WorkspaceInspection{
		HeadCommit: "head",
		Dirty:      true,
		Changes:    []holons.WorkspaceChange{{Path: "file.go", Status: "M"}},
	}
	state, service, runtime, _ := specializedAgentState(t, holons.KindNormal, "", inspection)
	if err := state.Observed(t.Context(), "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
		t.Fatal(err)
	}
	actions := &terminalHolonService{Service: service, agents: runtime}
	if err := actions.AgentCommitPromptAction(t.Context(), "specialized", "agent", "discuss_commit_message"); err != nil {
		t.Fatal(err)
	}
	if err := actions.AgentCommitPromptAction(t.Context(), "specialized", "agent", "discuss_commit_message"); err != nil {
		t.Fatal(err)
	}
	if err := state.Observed(t.Context(), "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
		t.Fatal(err)
	}

	h, err := service.Get(t.Context(), "specialized")
	if err != nil {
		t.Fatal(err)
	}
	agent := h.AgentSessions[0]
	if len(runtime.inputs) != 1 || agent.CommitPrompt == nil || agent.CommitPrompt.State != string(protocol.CommitPromptDiscussionStarted) {
		t.Fatalf("inputs=%d commit prompt=%+v", len(runtime.inputs), agent.CommitPrompt)
	}
}

type commitPromptInspectionSequence struct {
	mu     sync.Mutex
	values []holons.WorkspaceInspection
}

func (*commitPromptInspectionSequence) CreateWorkspace(context.Context, string, string) (holons.Workspace, error) {
	return holons.Workspace{}, nil
}
func (r *commitPromptInspectionSequence) InspectWorkspace(context.Context, string) (holons.WorkspaceInspection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value := r.values[0]
	if len(r.values) > 1 {
		r.values = r.values[1:]
	}
	return value, nil
}
func (*commitPromptInspectionSequence) PublishWorkspace(context.Context, string, holons.Publish) (holons.PublishedWorkspace, error) {
	return holons.PublishedWorkspace{}, nil
}
func (*commitPromptInspectionSequence) RemoveWorkspace(context.Context, string) error { return nil }

func TestCommitDiscussionActionIsConcurrentAndFailureSafe(t *testing.T) {
	dirty := holons.WorkspaceInspection{HeadCommit: "head", Dirty: true, Changes: []holons.WorkspaceChange{{Path: "file.go", Status: "M"}}}

	t.Run("concurrent", func(t *testing.T) {
		state, service, runtime, _ := specializedAgentState(t, holons.KindNormal, "", dirty)
		if err := state.Observed(t.Context(), "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
			t.Fatal(err)
		}
		actions := &terminalHolonService{Service: service, agents: runtime}
		var wait sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				errs <- actions.AgentCommitPromptAction(t.Context(), "specialized", "agent", "discuss_commit_message")
			}()
		}
		wait.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if len(runtime.inputs) != 1 {
			t.Fatalf("inputs=%d, want 1", len(runtime.inputs))
		}
	})

	t.Run("input failure", func(t *testing.T) {
		state, service, runtime, _ := specializedAgentState(t, holons.KindNormal, "", dirty)
		if err := state.Observed(t.Context(), "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
			t.Fatal(err)
		}
		actions := &terminalHolonService{Service: service, agents: runtime}
		runtime.inputErr = errors.New("input failed")
		if err := actions.AgentCommitPromptAction(t.Context(), "specialized", "agent", "discuss_commit_message"); err == nil {
			t.Fatal("expected input failure")
		}
		h, _ := service.Get(t.Context(), "specialized")
		if h.AgentSessions[0].CommitPrompt == nil || h.AgentSessions[0].CommitPrompt.State != string(protocol.CommitPromptDirtyPromptVisible) {
			t.Fatalf("prompt=%+v", h.AgentSessions[0].CommitPrompt)
		}
		runtime.inputErr = nil
		if err := actions.AgentCommitPromptAction(t.Context(), "specialized", "agent", "discuss_commit_message"); err != nil {
			t.Fatal(err)
		}
		if len(runtime.inputs) != 2 {
			t.Fatalf("input attempts=%d, want 2", len(runtime.inputs))
		}
	})

	t.Run("persistence failure", func(t *testing.T) {
		state, service, runtime, _ := specializedAgentState(t, holons.KindNormal, "", dirty)
		if err := state.Observed(t.Context(), "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
			t.Fatal(err)
		}
		actions := &terminalHolonService{Service: service, agents: runtime}
		ctx, cancel := context.WithCancel(t.Context())
		runtime.cancelInput = cancel
		if err := actions.AgentCommitPromptAction(ctx, "specialized", "agent", "discuss_commit_message"); err == nil {
			t.Fatal("expected persistence failure")
		}
		if err := actions.AgentCommitPromptAction(t.Context(), "specialized", "agent", "discuss_commit_message"); err != nil {
			t.Fatal(err)
		}
		if len(runtime.inputs) != 1 {
			t.Fatalf("inputs=%d, want 1", len(runtime.inputs))
		}
	})
}

func TestCommitPromptActionUsesCurrentWorkspaceSnapshot(t *testing.T) {
	dirty := holons.WorkspaceInspection{HeadCommit: "head", Dirty: true, Changes: []holons.WorkspaceChange{{Path: "one.go", Status: "M"}}}

	t.Run("clean", func(t *testing.T) {
		repository := &commitPromptInspectionSequence{values: []holons.WorkspaceInspection{dirty, {HeadCommit: "head", Clean: true}}}
		state, service, runtime, _ := specializedAgentStateWithRepository(t, holons.KindNormal, "", repository)
		if err := state.Observed(t.Context(), "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
			t.Fatal(err)
		}
		if err := (&terminalHolonService{Service: service, agents: runtime}).AgentCommitPromptAction(t.Context(), "specialized", "agent", "keep_discussing"); err != nil {
			t.Fatal(err)
		}
		h, _ := service.Get(t.Context(), "specialized")
		if h.AgentSessions[0].CommitPrompt != nil || h.AgentSessions[0].CommitPromptChangeHash != "" {
			t.Fatalf("prompt=%+v hash=%q", h.AgentSessions[0].CommitPrompt, h.AgentSessions[0].CommitPromptChangeHash)
		}
	})

	t.Run("changed", func(t *testing.T) {
		changed := holons.WorkspaceInspection{HeadCommit: "head", Dirty: true, Changes: []holons.WorkspaceChange{{Path: "two.go", Status: "A"}}}
		repository := &commitPromptInspectionSequence{values: []holons.WorkspaceInspection{dirty, changed}}
		state, service, runtime, _ := specializedAgentStateWithRepository(t, holons.KindNormal, "", repository)
		if err := state.Observed(t.Context(), "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
			t.Fatal(err)
		}
		before, _ := service.Get(t.Context(), "specialized")
		if err := (&terminalHolonService{Service: service, agents: runtime}).AgentCommitPromptAction(t.Context(), "specialized", "agent", "skip_for_change"); err != nil {
			t.Fatal(err)
		}
		after, _ := service.Get(t.Context(), "specialized")
		agent := after.AgentSessions[0]
		if agent.CommitPrompt == nil || agent.CommitPrompt.State != string(protocol.CommitPromptSkippedForChange) || agent.CommitPromptChangeHash == before.AgentSessions[0].CommitPromptChangeHash {
			t.Fatalf("agent=%+v", agent)
		}
	})
}

func TestKeepDiscussingReopensOnlyAfterAnotherTurnCompletes(t *testing.T) {
	state, service, runtime, _ := specializedAgentState(t, holons.KindNormal, "", holons.WorkspaceInspection{HeadCommit: "head", Dirty: true, Changes: []holons.WorkspaceChange{{Path: "file.go", Status: "M"}}})
	if err := state.Observed(t.Context(), "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
		t.Fatal(err)
	}
	actions := &terminalHolonService{Service: service, agents: runtime}
	if err := actions.AgentCommitPromptAction(t.Context(), "specialized", "agent", "keep_discussing"); err != nil {
		t.Fatal(err)
	}
	if err := state.Observed(t.Context(), "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
		t.Fatal(err)
	}
	h, _ := service.Get(t.Context(), "specialized")
	agent := h.AgentSessions[0]
	if agent.CommitPrompt == nil || agent.CommitPrompt.State != string(protocol.CommitPromptContinuedForNow) {
		t.Fatalf("duplicate completion commit prompt=%+v", agent.CommitPrompt)
	}
	if err := state.Observed(t.Context(), "specialized", "agent", harness.Event{Type: harness.EventInputStateChanged, InputState: protocol.InputNone}); err != nil {
		t.Fatal(err)
	}
	h, _ = service.Get(t.Context(), "specialized")
	if h.AgentSessions[0].CommitPrompt != nil {
		t.Fatalf("new turn commit prompt=%+v", h.AgentSessions[0].CommitPrompt)
	}
	if err := state.Observed(t.Context(), "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
		t.Fatal(err)
	}
	h, _ = service.Get(t.Context(), "specialized")
	if h.AgentSessions[0].CommitPrompt == nil || h.AgentSessions[0].CommitPrompt.State != string(protocol.CommitPromptDirtyPromptVisible) {
		t.Fatalf("later completion commit prompt=%+v", h.AgentSessions[0].CommitPrompt)
	}
}

func TestContinueWorkerAndOrdinaryHolonsRemainRunningAtTaskComplete(t *testing.T) {
	for _, test := range []struct {
		name string
		kind holons.Kind
		mode pullrequestwork.Mode
	}{
		{name: "continue", kind: holons.KindPullWorker, mode: pullrequestwork.ModeContinue},
		{name: "normal", kind: holons.KindNormal},
		{name: "issue", kind: holons.KindIssue},
	} {
		t.Run(test.name, func(t *testing.T) {
			var currentService *holons.Service
			inspected := false
			state, service, runtime, completion := specializedAgentStateWithRepository(t, test.kind, test.mode, artifactRepository{inspection: holons.WorkspaceInspection{HeadCommit: "head", Clean: true}, onInspect: func() {
				inspected = true
				h, err := currentService.Get(t.Context(), "specialized")
				if err != nil || h.ApplicationPhase != "" {
					t.Errorf("interactive turn entered finalization: %+v %v", h, err)
				}
			}})
			currentService = service
			if err := state.Observed(t.Context(), "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
				t.Fatal(err)
			}
			h, _ := service.Get(t.Context(), "specialized")
			if !inspected || h.ApplicationPhase != "" || completion.calls != 0 || runtime.cancelledAgent != "" || h.AgentSessions[0].Status != string(holons.StatusRunning) {
				t.Fatalf("completion=%d runtime=%+v agent=%+v", completion.calls, runtime, h.AgentSessions[0])
			}
		})
	}
}

func TestReviewCompletionBypassesInspectionExpiresAndStopsRuntime(t *testing.T) {
	inspectCalls := 0
	state, service, runtime, completion := specializedAgentStateWithRepository(t, holons.KindPullReview, pullrequestwork.ModeAuto, artifactRepository{
		inspectErr:   errors.New("inspection failed"),
		inspectCalls: &inspectCalls,
	})
	if _, _, err := service.TransitionAgentCommitPrompt(t.Context(), "specialized", "agent", holons.CommitPromptComplete, "stale-hash"); err != nil {
		t.Fatal(err)
	}
	if err := state.Observed(t.Context(), "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
		t.Fatal(err)
	}
	h, _ := service.Get(t.Context(), "specialized")
	if inspectCalls != 0 || completion.calls != 1 || runtime.cancelledAgent != "agent" || h.Status != holons.StatusExpired || h.AgentSessions[0].Status != string(holons.StatusExpired) || h.AgentSessions[0].CommitPrompt != nil {
		t.Fatalf("inspections=%d completion=%d runtime=%+v holon=%+v", inspectCalls, completion.calls, runtime, h)
	}
	products := &localTerminalProducts{holons: service}
	if err := products.ApplyTerminalCompletion("local", terminals.ProcessCompletion{TerminalID: "terminal", ExitCode: -1}); err != nil {
		t.Fatal(err)
	}
	h, _ = service.Get(t.Context(), "specialized")
	if h.Status != holons.StatusExpired || h.AgentSessions[0].Status != string(holons.StatusExpired) {
		t.Fatalf("terminal completion overwrote expiry: %+v", h)
	}
}

// These ports control process launch and observed events. Command construction
// uses the production adapters; no agent CLI is replaced or executed.
type forkLaunchRecorder struct {
	spec            terminals.LaunchSpec
	beforeLaunch    func()
	err             error
	inputs, signals int
}

func (l *forkLaunchRecorder) Launch(_ context.Context, spec terminals.LaunchSpec) (int, error) {
	if l.beforeLaunch != nil {
		l.beforeLaunch()
	}
	l.spec = spec
	return 42, l.err
}
func (l *forkLaunchRecorder) Signal(context.Context, terminals.TerminalID) error {
	l.signals++
	return nil
}
func (l *forkLaunchRecorder) Input(context.Context, terminals.TerminalID, []byte) error {
	l.inputs++
	return nil
}

type forkObservedDriver struct {
	harness.Driver
	ctx       context.Context
	monitored chan harness.MonitorSpec
	events    chan []harness.Event
	done      chan struct{}
}

func (d *forkObservedDriver) Probe(context.Context) protocol.HarnessCapability {
	return protocol.HarnessCapability{Available: true}
}
func (d *forkObservedDriver) RequiresPrivateRuntime() bool {
	private, ok := d.Driver.(harness.PrivateRuntimeDriver)
	return ok && private.RequiresPrivateRuntime()
}
func (d *forkObservedDriver) Monitor(_ context.Context, spec harness.MonitorSpec, send func(harness.Event)) {
	defer func() { d.done <- struct{}{} }()
	d.monitored <- spec
	select {
	case events := <-d.events:
		for _, event := range events {
			send(event)
		}
	case <-d.ctx.Done():
	}
}

type agentForkFixture struct {
	holons      *holons.Service
	store       *holonssqlite.Store
	agents      *agentsessions.Service
	driver      *forkObservedDriver
	launcher    *forkLaunchRecorder
	source      holons.AgentSession
	runtimeRoot string
	worktree    string
}

func newAgentForkFixture(t *testing.T, kind protocol.HarnessType, repository ...holons.Repository) agentForkFixture {
	t.Helper()
	service, store := terminalTestService(t)
	service = holons.NewServiceWithRepository(store, &commitAgentInspectionRecorder{})
	if len(repository) > 0 {
		service = holons.NewServiceWithRepository(store, repository[0])
	}
	runtimeRoot, worktree := t.TempDir(), t.TempDir()
	now := time.Now().UTC()
	source := holons.AgentSession{
		ID: "source-agent", HolonID: "fork-holon", TerminalID: "source-terminal", AgentType: string(kind), Title: "Source", Prompt: "Original task",
		ResumeTarget: "11111111-1111-4111-8111-111111111111",
		Status:       string(holons.StatusRunning), InputState: string(protocol.InputTaskComplete), CreatedAt: now, UpdatedAt: now, StartedAt: &now,
	}
	if kind == protocol.HarnessClaudeCode {
		source.RolloutPath = filepath.Join(t.TempDir(), "source.jsonl")
		if err := os.WriteFile(source.RolloutPath, []byte("source transcript\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := holons.Holon{
		ID: source.HolonID, Title: "Fork", Kind: holons.KindNormal, Status: holons.StatusRunning, BaseCommit: "head", WorktreeBranch: "holark/fork", WorktreePath: worktree, CreatedAt: now,
		AgentSessions: []holons.AgentSession{source, {
			ID: "destination-agent", HolonID: source.HolonID, AgentType: string(kind), Title: "Commit", Prompt: "Inspect the worktree and discuss a commit.",
			Status: string(holons.StatusQueued), CreatedAt: now, UpdatedAt: now.Add(time.Second),
		}},
	}
	if err := store.Create(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	native, ok := harness.RegistryWithCodex(forkCommandServer{}).Driver(kind)
	if !ok {
		t.Fatal("missing native adapter")
	}
	// Prepare a real source runtime so accidentally reusing it fails or changes
	// files checked by the isolation scenario below.
	if private, ok := native.(harness.PrivateRuntimeDriver); ok && private.RequiresPrivateRuntime() {
		sourceRuntime := filepath.Join(runtimeRoot, source.ID)
		if err := os.Mkdir(sourceRuntime, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := native.Command(harness.StartSpec{RepositoryPath: worktree, SessionID: h.ID, Prompt: source.Prompt, HarnessSession: protocol.HarnessSession{ID: source.ID}, RuntimeDir: sourceRuntime}); err != nil {
			t.Fatal(err)
		}
	}
	driver := &forkObservedDriver{Driver: native, ctx: t.Context(), monitored: make(chan harness.MonitorSpec, 16), events: make(chan []harness.Event, 1), done: make(chan struct{}, 16)}
	launcher := &forkLaunchRecorder{}
	agents, err := agentsessions.New(harness.NewRegistry(map[protocol.HarnessType]harness.Driver{kind: driver}), &localAgentState{holons: service}, launcher, nil, nil, runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := service.Get(t.Context(), h.ID)
	if err != nil {
		t.Fatal(err)
	}
	return agentForkFixture{store: store, holons: service, agents: agents, driver: driver, launcher: launcher, source: persisted.AgentSessions[0], runtimeRoot: runtimeRoot, worktree: worktree}
}

func (f agentForkFixture) sessions(t *testing.T) (holons.AgentSession, holons.AgentSession) {
	t.Helper()
	h, err := f.holons.Get(t.Context(), f.source.HolonID)
	if err != nil {
		t.Fatal(err)
	}
	var source, destination holons.AgentSession
	for _, agent := range h.AgentSessions {
		switch agent.ID {
		case f.source.ID:
			source = agent
		case "destination-agent":
			destination = agent
		}
	}
	return source, destination
}

func readAgentRuntime(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		files[path] = string(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestOpenCodeResumeUsesFreshRuntime(t *testing.T) {
	testResumeUsesFreshRuntime(t, protocol.HarnessOpenCode)
}

func TestClaudeTerminalCancellationReachesLaunchMonitor(t *testing.T) {
	f := newAgentForkFixture(t, protocol.HarnessClaudeCode)
	t.Cleanup(f.agents.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := f.agents.Launch(ctx, f.source.HolonID, "destination-agent", terminals.Dimensions{Columns: 80, Rows: 24}, "", agentsessions.LaunchOptions{}); err != nil {
		t.Fatal(err)
	}
	var monitored harness.MonitorSpec
	select {
	case monitored = <-f.driver.monitored:
	case <-ctx.Done():
		t.Fatal("monitor did not start")
	}
	if monitored.AttentionActions == nil {
		t.Fatal("production launch did not supply terminal attention actions")
	}
	manager, err := terminalhost.NewManager(terminalhost.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	gateway := localadapter.New(manager, terminalenv.Context{})
	gateway.InputDelivered = f.agents.ObserveTerminalInput
	id := terminals.TerminalID(monitored.HarnessSession.TerminalID)
	// Use a real shell PTY to verify successful and fenced writes. The agent
	// launch above only records its command; no agent CLI is simulated.
	if _, err := manager.Launch(ctx, terminals.LaunchSpec{
		TerminalID: id, Kind: terminals.LaunchCommand, Command: "/bin/sh",
		Arguments: []string{"-c", "sleep 30"}, Environment: os.Environ(),
		Dimensions: terminals.Dimensions{Columns: 80, Rows: 24},
	}); err != nil {
		t.Fatal(err)
	}
	first, err := manager.Attach(id)
	if err != nil {
		t.Fatal(err)
	}
	current, err := manager.Attach(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.Input(ctx, "local", id, first.Attachment, []byte("\x1b")); !errors.Is(err, terminals.ErrStaleAttachment) {
		t.Fatalf("stale input error = %v", err)
	}
	for _, data := range []string{"text", "\x1b[A", "\x1b[200~pasted\x1b[201~", "\r"} {
		if err := gateway.Input(ctx, "local", id, current.Attachment, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	f.agents.ObserveTerminalInput("unrelated-terminal", []byte("\x1b"))
	select {
	case action := <-monitored.AttentionActions:
		t.Fatalf("non-cancellation or undelivered input generated %q", action)
	default:
	}
	for _, key := range []struct {
		data   string
		action protocol.TerminalAttentionAction
	}{{"\x1b", protocol.TerminalAttentionCancel}, {"\x03", protocol.TerminalAttentionInterrupt}} {
		if err := gateway.Input(ctx, "local", id, current.Attachment, []byte(key.data)); err != nil {
			t.Fatal(err)
		}
		select {
		case action := <-monitored.AttentionActions:
			if action != key.action {
				t.Fatalf("action = %q, want %q", action, key.action)
			}
		case <-ctx.Done():
			t.Fatal("delivered cancellation did not reach monitor")
		}
	}
	if err := f.agents.StopTerminal(ctx, string(id)); err != nil {
		t.Fatal(err)
	}
	f.agents.ObserveTerminalInput(id, []byte("\x1b"))
	select {
	case action := <-monitored.AttentionActions:
		t.Fatalf("retired launch received %q", action)
	default:
	}
}

func TestClaudeCodeResumeUsesFreshRuntime(t *testing.T) {
	testResumeUsesFreshRuntime(t, protocol.HarnessClaudeCode)
}

func testResumeUsesFreshRuntime(t *testing.T, kind protocol.HarnessType) {
	t.Helper()
	for _, legacy := range []bool{false, true} {
		name := "new agent"
		if legacy {
			name = "existing runtime from before upgrade"
		}
		t.Run(name, func(t *testing.T) {
			f := newAgentForkFixture(t, kind)
			t.Cleanup(f.agents.Close)
			agentID := "destination-agent"
			if legacy {
				agentID = f.source.ID
			}
			previous := readAgentRuntime(t, f.runtimeRoot)
			for attempt := 0; attempt < 3; attempt++ {
				if attempt == 2 {
					// Recreate the service while retaining persisted sessions and runtime files.
					f.agents.Close()
					agents, err := agentsessions.New(harness.NewRegistry(map[protocol.HarnessType]harness.Driver{kind: f.driver}), &localAgentState{holons: f.holons}, f.launcher, nil, nil, f.runtimeRoot)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(agents.Close)
					f.agents = agents
				}
				if attempt == 0 && !legacy {
					if err := f.agents.Launch(t.Context(), f.source.HolonID, agentID, terminals.Dimensions{Columns: 80, Rows: 24}, "", agentsessions.LaunchOptions{}); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := f.holons.SetAgentSessionStatus(t.Context(), f.source.HolonID, agentID, holons.StatusCompleted, "", f.source.ResumeTarget); err != nil {
						t.Fatal(err)
					}
					if _, err := f.holons.ResumeAgentSession(t.Context(), f.source.HolonID, agentID, f.source.ResumeTarget); err != nil {
						t.Fatalf("prepare resume attempt %d: %v", attempt, err)
					}
					if err := f.agents.Launch(t.Context(), f.source.HolonID, agentID, terminals.Dimensions{Columns: 80, Rows: 24}, "", agentsessions.LaunchOptions{}); err != nil {
						t.Fatalf("resume attempt %d: %v", attempt, err)
					}
					resumeFlag := "--session"
					if kind == protocol.HarnessClaudeCode {
						resumeFlag = "--resume"
					}
					if !slices.Contains(f.launcher.spec.Arguments, resumeFlag) || !slices.Contains(f.launcher.spec.Arguments, f.source.ResumeTarget) {
						t.Fatalf("resume lost conversation: %v", f.launcher.spec.Arguments)
					}
				}
				var monitored harness.MonitorSpec
				select {
				case monitored = <-f.driver.monitored:
				case <-time.After(5 * time.Second):
					t.Fatal("monitor did not start")
				}
				if kind == protocol.HarnessClaudeCode {
					markers, err := filepath.Glob(filepath.Join(monitored.RuntimeDir, claudecode.LossFilePrefix+"*"))
					if err != nil || len(markers) != 0 {
						t.Fatalf("new observer inherited loss markers %v: %v", markers, err)
					}
					settings := filepath.Join(monitored.RuntimeDir, claudecode.SettingsFileName)
					if !slices.Contains(f.launcher.spec.Arguments, settings) {
						t.Fatalf("command and monitor runtime differ: missing settings %q", settings)
					}
				} else {
					spool := filepath.Join(monitored.RuntimeDir, opencode.SpoolFileName)
					data, err := os.ReadFile(spool)
					if err != nil || len(data) != 0 {
						t.Fatalf("new observer spool is not empty: %q, %v", data, err)
					}
					for _, env := range []string{opencode.EventsEnvironment + "=" + spool, opencode.ConfigEnvironment + "=" + opencode.SharedConfigDirectory(monitored.RuntimeDir)} {
						if !slices.Contains(f.launcher.spec.Environment, env) {
							t.Fatalf("command and monitor runtime differ: missing %q", env)
						}
					}
				}
				current := readAgentRuntime(t, f.runtimeRoot)
				for path, contents := range previous {
					if got, exists := current[path]; !exists || got != contents {
						t.Fatalf("previous runtime changed: %s", path)
					}
				}
				// Leave old observer output behind to catch accidental replay on resume.
				outputName := opencode.SpoolFileName
				if kind == protocol.HarnessClaudeCode {
					outputName = claudecode.LossFilePrefix + "fixture"
				}
				if err := os.WriteFile(filepath.Join(monitored.RuntimeDir, outputName), []byte("{\"type\":\"observer_initialized\"}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				previous = readAgentRuntime(t, f.runtimeRoot)
				f.driver.events <- []harness.Event{{
					Type: harness.EventSessionDiscovered, HarnessSessionID: agentID, HarnessType: kind,
					ResumeTarget: f.source.ResumeTarget, RolloutPath: f.source.RolloutPath,
				}}
				select {
				case <-f.driver.done:
				case <-time.After(5 * time.Second):
					t.Fatal("monitor did not finish")
				}
			}
		})
	}
}

func TestAgentForkLaunchOwnsDestinationAndDiscoveredConversation(t *testing.T) {
	for _, kind := range []protocol.HarnessType{protocol.HarnessCodex, protocol.HarnessClaudeCode, protocol.HarnessOpenCode} {
		t.Run(string(kind), func(t *testing.T) {
			f := newAgentForkFixture(t, kind)
			beforeRuntime := readAgentRuntime(t, f.runtimeRoot)
			sourceTarget := f.source.ResumeTarget
			dimensions := terminals.Dimensions{Columns: 100, Rows: 30}
			if err := f.agents.Fork(t.Context(), f.source.HolonID, f.source.ID, "destination-agent", dimensions); err != nil {
				t.Fatal(err)
			}
			source, destination := f.sessions(t)
			if !reflect.DeepEqual(source, f.source) {
				t.Fatalf("source changed: before=%+v after=%+v", f.source, source)
			}
			if destination.TerminalID == "" || destination.TerminalID == source.TerminalID || destination.Status != string(holons.StatusRunning) || destination.StartedAt == nil {
				t.Fatalf("destination did not acquire its own running terminal: %+v", destination)
			}
			if destination.ResumeTarget != "" || destination.RolloutPath != "" {
				t.Fatalf("destination inherited source identity: %+v", destination)
			}
			launch := f.launcher.spec
			promptArgument := destination.Prompt
			if kind == protocol.HarnessOpenCode {
				promptArgument = "--prompt=" + destination.Prompt
			}
			if string(launch.TerminalID) != destination.TerminalID || launch.CWD != f.worktree || launch.Dimensions != dimensions || !slices.Contains(launch.Arguments, sourceTarget) || !slices.Contains(launch.Arguments, promptArgument) {
				t.Fatalf("incorrect destination launch: terminal=%s cwd=%s dimensions=%+v arguments=%q", launch.TerminalID, launch.CWD, launch.Dimensions, launch.Arguments)
			}
			var monitored harness.MonitorSpec
			select {
			case monitored = <-f.driver.monitored:
			case <-time.After(5 * time.Second):
				t.Fatal("destination monitor did not start")
			}
			if monitored.HarnessSession.ID != destination.ID || monitored.HarnessSession.TerminalID != destination.TerminalID || monitored.HarnessSession.ResumeTarget != "" || monitored.HarnessSession.RolloutPath != "" {
				t.Fatalf("incorrect destination monitor: %+v", monitored)
			}
			runtimeDir := monitored.RuntimeDir
			if kind != protocol.HarnessCodex && filepath.Dir(runtimeDir) != filepath.Join(f.runtimeRoot, destination.ID) {
				t.Fatalf("destination runtime does not belong to its own launch: %q", runtimeDir)
			}
			switch kind {
			case protocol.HarnessClaudeCode:
				settings := filepath.Join(runtimeDir, claudecode.SettingsFileName)
				if !slices.Contains(launch.Arguments, settings) {
					t.Fatalf("missing destination hooks: %+v", launch.Arguments)
				}
				metadata, err := os.ReadFile(filepath.Join(runtimeDir, claudecode.MetadataFileName))
				if err != nil || !strings.Contains(string(metadata), `"harness_session_id":"destination-agent"`) {
					t.Fatalf("destination metadata=%s err=%v", metadata, err)
				}
			case protocol.HarnessOpenCode:
				for _, env := range []string{opencode.ConfigEnvironment + "=" + opencode.SharedConfigDirectory(runtimeDir), opencode.EventsEnvironment + "=" + filepath.Join(runtimeDir, opencode.SpoolFileName)} {
					if !slices.Contains(launch.Environment, env) {
						t.Fatalf("missing destination runtime environment %q", env)
					}
				}
			}
			afterRuntime := readAgentRuntime(t, f.runtimeRoot)
			for path, contents := range beforeRuntime {
				if afterRuntime[path] != contents {
					t.Fatalf("source runtime changed: %s", path)
				}
			}
			rollout := ""
			if kind == protocol.HarnessClaudeCode {
				rollout = "fork-rollout.jsonl"
			}
			f.driver.events <- []harness.Event{
				{Type: harness.EventSessionDiscovered, HarnessSessionID: destination.ID, HarnessType: kind, ResumeTarget: "fork-conversation", RolloutPath: rollout},
				{Type: harness.EventInputStateChanged, HarnessSessionID: destination.ID, InputState: protocol.InputUserRequired},
				{Type: harness.EventObservabilityChanged, HarnessSessionID: destination.ID, ObservabilityStatus: protocol.ObservabilityHealthy},
			}
			select {
			case <-f.driver.done:
			case <-time.After(5 * time.Second):
				t.Fatal("destination events did not finish")
			}
			source, destination = f.sessions(t)
			if !reflect.DeepEqual(source, f.source) || f.launcher.inputs != 0 || f.launcher.signals != 0 {
				t.Fatalf("source was affected: %+v", source)
			}
			if destination.ResumeTarget != "fork-conversation" || destination.RolloutPath != rollout || destination.InputState != string(protocol.InputUserRequired) || destination.ObservabilityStatus != string(protocol.ObservabilityHealthy) {
				t.Fatalf("destination discovery was not persisted: %+v", destination)
			}
		})
	}
}

func TestAgentForkLaunchFailurePersistsOnlyOnDestination(t *testing.T) {
	for _, preparationFailure := range []bool{true, false} {
		name := "process launch after binding"
		if preparationFailure {
			name = "missing source transcript before binding"
		}
		t.Run(name, func(t *testing.T) {
			f := newAgentForkFixture(t, protocol.HarnessClaudeCode)
			if preparationFailure {
				if err := os.Remove(f.source.RolloutPath); err != nil {
					t.Fatal(err)
				}
			} else {
				f.launcher.err = errors.New("terminal launch refused")
			}
			err := f.agents.Fork(t.Context(), f.source.HolonID, f.source.ID, "destination-agent", terminals.Dimensions{Columns: 80, Rows: 24})
			if err == nil {
				t.Fatal("expected launch failure")
			}
			source, destination := f.sessions(t)
			if !reflect.DeepEqual(source, f.source) || f.launcher.inputs != 0 || f.launcher.signals != 0 {
				t.Fatalf("failure affected source: %+v", source)
			}
			if destination.Status != string(holons.StatusFailed) || destination.Reason != err.Error() || destination.FinishedAt == nil || destination.ResumeTarget != "" || destination.RolloutPath != "" {
				t.Fatalf("failure was not persisted on destination: %+v err=%v", destination, err)
			}
			if preparationFailure {
				if !errors.Is(err, harness.ErrHarnessPreparation) || destination.TerminalID != "" || f.launcher.spec.TerminalID != "" {
					t.Fatalf("preparation unexpectedly launched: %+v err=%v", destination, err)
				}
			} else if !errors.Is(err, f.launcher.err) || destination.TerminalID == "" || string(f.launcher.spec.TerminalID) != destination.TerminalID {
				t.Fatalf("bound launch failure: %+v err=%v", destination, err)
			}
			select {
			case <-f.driver.monitored:
				t.Fatal("monitor started for a failed launch")
			default:
			}
		})
	}
}

// Repository calls are counted independently from the launch path, including
// the ordinary inspection on later task completion.
type commitAgentInspectionRecorder struct {
	holons.Repository
	calls     int
	head      string
	onInspect func()
}

func (r *commitAgentInspectionRecorder) InspectWorkspace(context.Context, string) (holons.WorkspaceInspection, error) {
	r.calls++
	if r.onInspect != nil {
		r.onInspect()
	}
	head := r.head
	if head == "" {
		head = "head"
	}
	return holons.WorkspaceInspection{HeadCommit: head, Clean: true}, nil
}

func observeCommitAgent(t *testing.T, f agentForkFixture, id string, event harness.Event) {
	t.Helper()
	if err := (&localAgentState{holons: f.holons, commitCloser: &terminalHolonService{Service: f.holons, agents: f.agents}}).Observed(t.Context(), f.source.HolonID, id, event); err != nil {
		t.Fatal(err)
	}
}
func latestCreatedAgent(t *testing.T, before, after holons.Holon) holons.AgentSession {
	t.Helper()
	for _, a := range after.AgentSessions {
		if !slices.ContainsFunc(before.AgentSessions, func(old holons.AgentSession) bool { return old.ID == a.ID }) {
			return a
		}
	}
	t.Fatal("no new destination")
	return holons.AgentSession{}
}
func commitWorkflow(t *testing.T, f agentForkFixture) *commitAgentCoordinator {
	t.Helper()
	return &commitAgentCoordinator{holons: f.holons, agents: f.agents, templates: fixedPromptReader("CUSTOM commit request")}
}

func TestForkAgentSessionCreatesPromptlessManagedDestination(t *testing.T) {
	for _, kind := range []protocol.HarnessType{protocol.HarnessCodex, protocol.HarnessClaudeCode, protocol.HarnessOpenCode} {
		t.Run(string(kind), func(t *testing.T) {
			f := newAgentForkFixture(t, kind)
			before, err := f.holons.Get(t.Context(), f.source.HolonID)
			if err != nil {
				t.Fatal(err)
			}
			created, err := (&forkAgentCoordinator{holons: f.holons, agents: f.agents}).ForkAgentSession(t.Context(), f.source.HolonID, f.source.ID)
			if err != nil {
				t.Fatal(err)
			}
			after, err := f.holons.Get(t.Context(), f.source.HolonID)
			if err != nil {
				t.Fatal(err)
			}
			if created.ID == "" || created.ID == f.source.ID || created.Title != "Fork" || created.Prompt != "" || created.AgentType != string(kind) || created.Status != string(holons.StatusRunning) || created.TerminalID == "" || created.ResumeTarget != "" || created.RolloutPath != "" {
				t.Fatalf("created fork = %+v", created)
			}
			if len(after.AgentSessions) != len(before.AgentSessions)+1 || !reflect.DeepEqual(before.AgentSession(f.source.ID), after.AgentSession(f.source.ID)) || f.launcher.inputs != 0 {
				t.Fatalf("fork changed source or submitted input: before=%+v after=%+v inputs=%d", before.AgentSession(f.source.ID), after.AgentSession(f.source.ID), f.launcher.inputs)
			}
		})
	}
}

func TestForkAgentSessionRejectsUnreadySourceWithoutCreatingTab(t *testing.T) {
	f := newAgentForkFixture(t, protocol.HarnessCodex)
	before, _ := f.holons.Get(t.Context(), f.source.HolonID)
	_, err := (&forkAgentCoordinator{holons: f.holons, agents: f.agents}).ForkAgentSession(t.Context(), f.source.HolonID, "destination-agent")
	after, _ := f.holons.Get(t.Context(), f.source.HolonID)
	if !errors.Is(err, holons.ErrAgentSessionNotForkable) || !reflect.DeepEqual(before, after) || f.launcher.spec.Command != "" {
		t.Fatalf("err=%v before=%+v after=%+v launch=%+v", err, before, after, f.launcher.spec)
	}
}

func TestCreateCommitAgentUsesSelectedIdleOpenTab(t *testing.T) {
	repository := &commitAgentInspectionRecorder{}
	f := newAgentForkFixture(t, protocol.HarnessCodex, repository)
	coordinator := commitWorkflow(t, f)
	observeCommitAgent(t, f, "destination-agent", harness.Event{Type: harness.EventInputStateChanged, InputState: protocol.InputNone})
	if _, err := f.holons.RenameAgentSession(t.Context(), f.source.HolonID, "destination-agent", "Selected tab"); err != nil {
		t.Fatal(err)
	}
	h, _ := f.holons.Get(t.Context(), f.source.HolonID)
	selected, err := commitSource(h, f.source.ID)
	if err != nil || selected.ID != f.source.ID {
		t.Fatalf("source=%+v err=%v", selected, err)
	}
	other := f.source
	other.ID, other.TerminalID, other.ResumeTarget = "active-a", "other-terminal", "22222222-2222-4222-8222-222222222222"
	if _, err = f.store.AddAgentSession(t.Context(), f.source.HolonID, other); err != nil {
		t.Fatal(err)
	}
	closed := other
	closed.ID = "closed-agent"
	closedAt := time.Now().UTC()
	closed.ClosedAt = &closedAt
	if _, err = f.store.AddAgentSession(t.Context(), f.source.HolonID, closed); err != nil {
		t.Fatal(err)
	}
	before, _ := f.holons.Get(t.Context(), f.source.HolonID)
	created, err := coordinator.CreateCommitAgent(t.Context(), f.source.HolonID, f.source.ID)
	if err != nil {
		t.Fatal(err)
	}
	destination := latestCreatedAgent(t, before, created)
	if !slices.Contains(f.launcher.spec.Arguments, f.source.ResumeTarget) || destination.Title != "Commit" || destination.AgentType != f.source.AgentType || destination.Prompt != "CUSTOM commit request" || destination.CommitPrompt == nil || destination.CommitPrompt.State != "commit_discussion_started" || destination.ResumeTarget != "" || destination.Status != "running" || destination.TerminalID == "" {
		t.Fatalf("destination=%+v launch=%+v", destination, f.launcher.spec)
	}
	for _, old := range before.AgentSessions {
		actual := created.AgentSessions[slices.IndexFunc(created.AgentSessions, func(a holons.AgentSession) bool { return a.ID == old.ID })]
		if !reflect.DeepEqual(old, actual) {
			t.Fatalf("source tab changed: before=%+v after=%+v", old, actual)
		}
	}
	if destination.TabOrder <= other.TabOrder || repository.calls != 1 || destination.CommitStartHead != "head" || f.launcher.inputs != 0 {
		t.Fatalf("order=%d inspection=%d inputs=%d", destination.TabOrder, repository.calls, f.launcher.inputs)
	}
}

func TestCommitWithoutSourceStartsFreshConfiguredAgent(t *testing.T) {
	f := newAgentForkFixture(t, protocol.HarnessClaudeCode)
	t.Cleanup(f.agents.Close)
	h, err := f.holons.Get(t.Context(), f.source.HolonID)
	if err != nil {
		t.Fatal(err)
	}
	h.ID, h.AgentSessions = "commit-without-source", nil
	if err := f.store.Create(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	coordinator := commitWorkflow(t, f)
	coordinator.harnesses = &harnessPreferencesStub{resolved: protocol.HarnessClaudeCode}
	handler := holonshttp.New(&terminalHolonService{Service: f.holons, commitAgents: coordinator})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/holons/"+h.ID+"/commit-agent", strings.NewReader(`{}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("commit without source = %d %s", response.Code, response.Body.String())
	}
	var created holons.AgentSession
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	updated, err := f.holons.Get(t.Context(), h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.AgentSessions) != 1 || updated.AgentSessions[0].ID != created.ID {
		t.Fatalf("expected one persisted commit agent: %+v", updated.AgentSessions)
	}
	agent := updated.AgentSessions[0]
	if agent.Title != "Commit" || agent.AgentType != string(protocol.HarnessClaudeCode) || agent.Status != "running" || agent.TerminalID == "" || agent.ResumeTarget != "" || agent.CommitStartHead != "head" || !updated.HasActiveCommitAgent() {
		t.Fatalf("fresh commit agent = %+v", agent)
	}
	launch := f.launcher.spec
	if launch.CWD != h.WorktreePath || !slices.Contains(launch.Arguments, "CUSTOM commit request") || slices.Contains(launch.Arguments, "--resume") || slices.Contains(launch.Arguments, "--fork-session") {
		t.Fatalf("expected a fresh launch with the commit prompt: %+v", launch)
	}
}

func TestCommitWithoutSourceLaunchesReservedAgentWhenAnotherTabIsCreated(t *testing.T) {
	repository := &commitAgentInspectionRecorder{}
	f := newAgentForkFixture(t, protocol.HarnessCodex, repository)
	t.Cleanup(f.agents.Close)
	coordinator := commitWorkflow(t, f)
	coordinator.harnesses = &harnessPreferencesStub{resolved: protocol.HarnessCodex}
	var beforeCommit holons.Holon
	var other holons.AgentSession
	// Create and launch another tab after Commit's initial snapshot, before its
	// reservation. The stale snapshot must not redirect Commit to that running tab.
	repository.onInspect = func() {
		var err error
		other, err = f.holons.AddAgentSessionOnce(t.Context(), f.source.HolonID, "codex", "Another task", "other-tab")
		if err != nil {
			t.Fatal(err)
		}
		if err := launchActionAgent(t.Context(), f.agents, f.source.HolonID, "", other.ID); err != nil {
			t.Fatal(err)
		}
		beforeCommit, err = f.holons.Get(t.Context(), f.source.HolonID)
		if err != nil {
			t.Fatal(err)
		}
		other = beforeCommit.AgentSession(other.ID)
	}
	created, err := coordinator.CreateCommitAgent(t.Context(), f.source.HolonID, "")
	if err != nil {
		t.Fatal(err)
	}
	commit := latestCreatedAgent(t, beforeCommit, created)
	if commit.Title != "Commit" || commit.Status != "running" || commit.TerminalID == "" || commit.TerminalID != string(f.launcher.spec.TerminalID) || !slices.Contains(f.launcher.spec.Arguments, commit.Prompt) {
		t.Fatalf("reserved commit was not launched: agent=%+v terminal=%s arguments=%v", commit, f.launcher.spec.TerminalID, f.launcher.spec.Arguments)
	}
	if actual := created.AgentSession(other.ID); other.Status != "running" || !reflect.DeepEqual(other, actual) {
		t.Fatalf("another running tab changed: before=%+v after=%+v", other, actual)
	}
}

func TestCreateCommitAgentRejectsInvalidSelectedSource(t *testing.T) {
	for _, scenario := range []string{"missing", "closed", "foreign", "missing conversation", "unknown harness", "missing Claude transcript"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAgentForkFixture(t, protocol.HarnessCodex)
			sourceID := "invalid-source"
			if scenario != "missing" && scenario != "foreign" {
				candidate := f.source
				candidate.ID = sourceID
				switch scenario {
				case "closed":
					stamp := time.Now()
					candidate.ClosedAt = &stamp
				case "missing conversation":
					candidate.ResumeTarget = ""
				case "unknown harness":
					candidate.AgentType = "unsupported"
				case "missing Claude transcript":
					candidate.AgentType = string(protocol.HarnessClaudeCode)
					candidate.RolloutPath = ""
				}
				if _, err := f.store.AddAgentSession(t.Context(), f.source.HolonID, candidate); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := f.holons.Get(t.Context(), f.source.HolonID)
			_, err := commitWorkflow(t, f).CreateCommitAgent(t.Context(), f.source.HolonID, sourceID)
			want := holons.ErrCommitSourceNotForkable
			if scenario == "missing" || scenario == "closed" || scenario == "foreign" {
				want = holons.ErrCommitSourceMissing
			}
			after, _ := f.holons.Get(t.Context(), f.source.HolonID)
			if !errors.Is(err, want) || !reflect.DeepEqual(before, after) || f.launcher.spec.Command != "" {
				t.Fatalf("err=%v before=%+v after=%+v", err, before, after)
			}
		})
	}
}

type commitTemplateReader map[string]string

func (r commitTemplateReader) Read(_ context.Context, key string) string { return r[key] }
func TestCreateCommitAgentRendersConfiguredSpecializedPrompt(t *testing.T) {
	for _, test := range []struct {
		name       string
		kind       holons.Kind
		mode       pullrequestwork.Mode
		key        string
		artifact   string
		autoCommit bool
		lookupErr  error
	}{
		{name: "main", kind: holons.KindNormal, key: prompttemplates.CommitAgentKey, autoCommit: true},
		{name: "issue", kind: holons.KindIssue, key: prompttemplates.CommitAgentKey, autoCommit: true},
		{name: "continue", kind: holons.KindPullWorker, mode: pullrequestwork.ModeContinue, key: prompttemplates.CommitAgentKey, autoCommit: true},
		{name: "address assisted", kind: holons.KindPullWorker, mode: pullrequestwork.ModeAssisted, key: prompttemplates.PullRequestWorkerAssistedCommitKey, artifact: pullRequestWorkArtifactPath(pullrequestwork.KindWorker)},
		{name: "address auto", kind: holons.KindPullWorker, mode: pullrequestwork.ModeAuto, key: prompttemplates.PullRequestWorkerAssistedCommitKey, artifact: pullRequestWorkArtifactPath(pullrequestwork.KindWorker)},
		{name: "rebase", kind: holons.KindRebase, key: prompttemplates.PullRequestRebaseCommitKey, artifact: pullRequestWorkArtifactPath(pullrequestwork.KindRebase)},
		{name: "review", kind: holons.KindPullReview, key: prompttemplates.CommitFollowUpKey},
		{name: "metadata", kind: holons.KindPRMetadata, key: prompttemplates.CommitFollowUpKey},
		{name: "work lookup failure", kind: holons.KindPullWorker, key: prompttemplates.CommitAgentKey, lookupErr: errors.New("work lookup failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &commitAgentInspectionRecorder{}
			f := newAgentForkFixture(t, protocol.HarnessClaudeCode, repository)
			h, _ := f.holons.Get(t.Context(), f.source.HolonID)
			h.Kind = test.kind
			// Kind is immutable in store.Update; seed the specialized Holon using the
			// same persisted launch fixture under a new ID.
			h.ID = "specialized-commit"
			for i := range h.AgentSessions {
				h.AgentSessions[i].ID += "-specialized"
				h.AgentSessions[i].HolonID = h.ID
			}
			if err := f.store.Create(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			f.source = h.AgentSessions[0]
			c := commitWorkflow(t, f)
			workKind := pullrequestwork.KindWorker
			if test.kind == holons.KindRebase {
				workKind = pullrequestwork.KindRebase
			}
			c.templates = commitTemplateReader{test.key: "SELECTED {{artifact_path}}"}
			c.work = &completionWorkStub{work: pullrequestwork.Work{Kind: workKind, Mode: test.mode}, lookupErr: test.lookupErr}
			created, err := c.CreateCommitAgent(t.Context(), h.ID, f.source.ID)
			if test.lookupErr != nil {
				if !errors.Is(err, test.lookupErr) {
					t.Fatalf("creation error=%v", err)
				}
				unchanged, err := f.holons.Get(t.Context(), h.ID)
				if err != nil || len(unchanged.AgentSessions) != len(h.AgentSessions) || repository.calls != 0 {
					t.Fatalf("failed lookup created a fork: %+v, %v", unchanged, err)
				}
				created, _, err = f.holons.AddCommitAgent(t.Context(), h.ID, f.source.ID, "Commit", "head", "")
				if err != nil {
					t.Fatal(err)
				}
				destination := latestCreatedAgent(t, h, created)
				state := localAgentState{holons: f.holons, work: c.work, commitCloser: &terminalHolonService{Service: f.holons}}
				if err = state.Observed(t.Context(), h.ID, destination.ID, harness.Event{InputState: protocol.InputTaskComplete}); !errors.Is(err, test.lookupErr) {
					t.Fatalf("completion error=%v", err)
				}
				unchanged, err = f.holons.Get(t.Context(), h.ID)
				if err != nil || unchanged.AgentSession(destination.ID).ClosedAt != nil || repository.calls != 0 {
					t.Fatalf("failed lookup completed a fork: %+v, %v", unchanged, err)
				}
				if agent := unchanged.AgentSession(destination.ID); agent.InputState != string(protocol.InputUserRequired) || agent.Activity != protocol.ActivityNeedsInput {
					t.Fatalf("failed lookup must wait for input: %+v", agent)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			destination := latestCreatedAgent(t, h, created)
			if destination.Prompt != "SELECTED "+test.artifact || !slices.Contains(f.launcher.spec.Arguments, destination.Prompt) || destination.AgentType != string(protocol.HarnessClaudeCode) {
				t.Fatalf("destination=%+v", destination)
			}
			wantHead, wantInspections := "", 0
			if test.autoCommit {
				wantHead, wantInspections = "head", 1
			}
			if destination.CommitStartHead != wantHead || repository.calls != wantInspections {
				t.Fatalf("starting HEAD=%q inspections=%d", destination.CommitStartHead, repository.calls)
			}
		})
	}
}

func TestCreateCommitAgentSequentialActiveCompletionAndFailure(t *testing.T) {
	repository := &commitAgentInspectionRecorder{}
	f := newAgentForkFixture(t, protocol.HarnessCodex, repository)
	c := commitWorkflow(t, f)
	before, _ := f.holons.Get(t.Context(), f.source.HolonID)
	f.launcher.beforeLaunch = func() {
		queued, err := f.holons.Get(t.Context(), before.ID)
		if err != nil {
			t.Fatal(err)
		}
		destination := latestCreatedAgent(t, before, queued)
		if destination.Status != "queued" || destination.CommitStartHead != "head" || !queued.HasActiveCommitAgent() {
			t.Fatalf("queued destination=%+v", destination)
		}
		if _, err = c.CreateCommitAgent(t.Context(), before.ID, f.source.ID); !errors.Is(err, holons.ErrCommitForkInProgress) {
			t.Fatalf("queued duplicate=%v", err)
		}
	}
	first, err := c.CreateCommitAgent(t.Context(), before.ID, f.source.ID)
	f.launcher.beforeLaunch = nil
	if err != nil {
		t.Fatal(err)
	}
	destination := latestCreatedAgent(t, before, first)
	for _, input := range []protocol.InputState{protocol.InputNone, protocol.InputUserRequired, protocol.InputPermissionRequired} {
		observeCommitAgent(t, f, destination.ID, harness.Event{Type: harness.EventInputStateChanged, InputState: input})
		// Banner snapshot cleanup cannot erase the uninspected active-fork marker.
		if _, accepted, err := f.holons.TransitionAgentCommitPrompt(t.Context(), before.ID, destination.ID, holons.CommitPromptClean, ""); err != nil || accepted {
			t.Fatalf("active marker cleared: accepted=%v err=%v", accepted, err)
		}
		if _, err = c.CreateCommitAgent(t.Context(), before.ID, f.source.ID); !errors.Is(err, holons.ErrCommitForkInProgress) {
			t.Fatalf("input=%s err=%v", input, err)
		}
	}
	after, _ := f.holons.Get(t.Context(), before.ID)
	if len(after.AgentSessions) != len(first.AgentSessions) || repository.calls != 1 {
		t.Fatalf("duplicate=%+v inspection=%d", after, repository.calls)
	}
	repository.head = "new-head"
	observeCommitAgent(t, f, destination.ID, harness.Event{Type: harness.EventInputStateChanged, InputState: protocol.InputTaskComplete})
	completed, _ := f.holons.Get(t.Context(), before.ID)
	if completed.HasActiveCommitAgent() || repository.calls != 2 || completed.AgentSession(destination.ID).ClosedAt == nil {
		t.Fatalf("completed=%+v inspection=%d", completed, repository.calls)
	}
	second, err := c.CreateCommitAgent(t.Context(), before.ID, f.source.ID)
	if err != nil {
		t.Fatal(err)
	}
	failed := latestCreatedAgent(t, completed, second)
	if _, err = f.holons.SetAgentSessionStatus(t.Context(), before.ID, failed.ID, holons.StatusFailed, "process failed", ""); err != nil {
		t.Fatal(err)
	}
	third, err := c.CreateCommitAgent(t.Context(), before.ID, f.source.ID)
	if err != nil || len(third.AgentSessions) != len(second.AgentSessions)+1 || repository.calls != 4 {
		t.Fatalf("third=%+v err=%v inspection=%d", third, err, repository.calls)
	}
}

// The launch recorder verifies the runtime boundary without an agent CLI.
type forkCommandServer struct{}

func (forkCommandServer) Prepare(context.Context, *exec.Cmd, protocol.HarnessSession) error {
	return nil
}
func (forkCommandServer) Monitor(context.Context, string, func(codex.Observation)) {}
func (forkCommandServer) Stop(context.Context, string) error                       { return nil }
