package localapp

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/harness/claudecode"
	"github.com/holark-ai/holark/internal/holons"
	holonshttp "github.com/holark-ai/holark/internal/holons/httpapi"
	holonssqlite "github.com/holark-ai/holark/internal/holons/sqliteadapter"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
	"github.com/holark-ai/holark/internal/sessionterminals"
	"github.com/holark-ai/holark/internal/terminals"
)

// Exercise completion against real Git and SQLite without launching an agent CLI.
func TestCommitAgentCompletionAndPersistentWaiting(t *testing.T) {
	var cases []struct {
		kind    holons.Kind
		outcome string
	}
	for _, kind := range []holons.Kind{holons.KindNormal, holons.KindIssue, holons.KindPullWorker} {
		for _, outcome := range []string{"new commit", "no commit clean", "no commit dirty", "new commit dirty", "inspection failure",
			"restart new commit:running", "restart new commit:cancelling", "restart new commit:cancelled", "restart new commit:shutdown failure",
			"restart new commit", "restart no commit clean", "restart no commit dirty", "restart new commit dirty", "restart inspection failure"} {
			cases = append(cases, struct {
				kind    holons.Kind
				outcome string
			}{kind, outcome})
		}
	}
	for _, test := range cases {
		t.Run(string(test.kind)+"/"+test.outcome, func(t *testing.T) {
			kind, outcome := test.kind, test.outcome
			restart := strings.HasPrefix(outcome, "restart ")
			outcome = strings.TrimPrefix(outcome, "restart ")
			outcome, interruptedClose, _ := strings.Cut(outcome, ":")
			agentType := protocol.HarnessClaudeCode
			if restart {
				agentType = protocol.HarnessCodex
			}
			ctx := t.Context()
			root := t.TempDir()
			git := func(path string, args ...string) string {
				t.Helper()
				command := exec.CommandContext(ctx, "git", args...)
				command.Dir = path
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, output)
				}
				return strings.TrimSpace(string(output))
			}
			git(root, "init", "-b", "main")
			git(root, "config", "user.name", "Test")
			git(root, "config", "user.email", "test@example.com")
			git(root, "commit", "--allow-empty", "-m", "Initial")
			baseline := git(root, "rev-parse", "HEAD")
			adapter, err := gitadapter.OpenWithWorktrees(ctx, root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			repos := holonRepositoryCoordinator{repositories: repository.NewServiceWithContext(ctx, adapter)}
			workspace, err := repos.CreateWorkspace(ctx, "commit-completion", baseline)
			if err != nil {
				t.Fatal(err)
			}
			dbPath := filepath.Join(t.TempDir(), "state.sqlite")
			db, err := database.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			store, err := holonssqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			service := holons.NewServiceWithRepository(store, repos)
			now := time.Now().UTC()
			h := holons.Holon{ID: "commit-completion", Kind: kind, Status: holons.StatusRunning, BaseBranch: "main", BaseCommit: baseline, WorktreePath: workspace.Path, WorktreeBranch: workspace.Branch, CreatedAt: now,
				AgentSessions: []holons.AgentSession{{ID: "source", HolonID: "commit-completion", AgentType: string(agentType), Status: "running", ResumeTarget: "source-conversation", CreatedAt: now, UpdatedAt: now}},
			}
			if err := store.Create(ctx, h); err != nil {
				t.Fatal(err)
			}
			_, agentID, err := service.AddCommitAgent(ctx, h.ID, "source", "Inspect and commit the changes", baseline, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.SetAgentSessionStatus(ctx, h.ID, agentID, holons.StatusRunning, "", "conversation"); err != nil {
				t.Fatal(err)
			}
			work := &completionWorkStub{work: pullrequestwork.Work{Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeContinue}}
			state := localAgentState{holons: service, work: work, commitCloser: &terminalHolonService{Service: service}}
			if strings.HasPrefix(outcome, "new commit") {
				git(workspace.Path, "commit", "--allow-empty", "-m", "Requested change")
			}
			if strings.HasSuffix(outcome, "dirty") {
				if err := os.WriteFile(filepath.Join(workspace.Path, "remaining.txt"), []byte("unfinished\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if outcome == "inspection failure" {
				if err := os.Rename(workspace.Path, workspace.Path+"-unavailable"); err != nil {
					t.Fatal(err)
				}
			}
			complete := harness.Event{Type: harness.EventInputStateChanged, InputState: protocol.InputTaskComplete, Activity: protocol.ActivityCompleted}
			if restart {
				// Simulate a crash after saving completion, before checking HEAD.
				_, err = service.UpdateAgentObservation(ctx, h.ID, agentID, "", "", "", string(complete.InputState), "", "", complete.Activity, nil)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				err = state.Observed(ctx, h.ID, agentID, complete)
				if (err != nil) != (outcome == "inspection failure") {
					t.Fatalf("completion error: %v", err)
				}
			}
			if interruptedClose != "" {
				if interruptedClose == "shutdown failure" {
					terminalID, err := terminals.NewID()
					if err != nil {
						t.Fatal(err)
					}
					if _, err := service.UpdateAgentObservation(ctx, h.ID, agentID, string(terminalID), "", "", "", "", "", protocol.ActivityCompleted, nil); err != nil {
						t.Fatal(err)
					}
					shutdownErr := errors.New("runtime shutdown failed")
					gateway := &launchGateway{closeErr: map[terminals.TerminalID]error{terminalID: shutdownErr}}
					coordinator, err := sessionterminals.New(gateway, &localTerminalProducts{holons: service})
					if err != nil {
						t.Fatal(err)
					}
					state.commitCloser = &terminalHolonService{Service: service, terminals: coordinator}
					if err := state.Observed(ctx, h.ID, agentID, complete); !errors.Is(err, shutdownErr) {
						t.Fatalf("completion shutdown error: %v", err)
					}
				} else {
					// Simulate exits before cancellation, during shutdown, and after
					// cancellation has settled but before ClosedAt is saved.
					if _, _, err := service.TransitionAgentCommitPrompt(ctx, h.ID, agentID, holons.CommitPromptClose, ""); err != nil {
						t.Fatal(err)
					}
					if _, err := service.SetAgentSessionStatus(ctx, h.ID, agentID, holons.Status(interruptedClose), "", "conversation"); err != nil {
						t.Fatal(err)
					}
				}
				// Late completion cleanup must not erase the pending close.
				if _, _, err := service.TransitionAgentCommitPrompt(ctx, h.ID, agentID, holons.CommitPromptClean, ""); err != nil {
					t.Fatal(err)
				}
				pending, err := service.Get(ctx, h.ID)
				if err != nil || !pending.AgentSession(agentID).CommitClosePending() {
					t.Fatalf("close intent lost: %+v, %v", pending.AgentSession(agentID), err)
				}
				// The saved verification is sufficient even if HEAD later changes.
				git(workspace.Path, "reset", "--hard", baseline)
			}
			// Reopen the database and recreate the application state, as on restart.
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = database.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			store, err = holonssqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			service = holons.NewServiceWithRepository(store, repos)
			state = localAgentState{holons: service, work: work, commitCloser: &terminalHolonService{Service: service}}
			if restart {
				if err := clearStaleTerminalBindings(ctx, service); err != nil {
					t.Fatal(err)
				}
				// No agent CLI or new completion event is needed to reconcile.
				runtime := &terminalHolonService{Service: service, work: work}
				if interruptedClose != "" {
					runtime.restoreAgents(ctx)
					// Retrying recovery must also leave the already-closed tab alone.
					runtime.restoreAgents(ctx)
				} else {
					_, err = runtime.restoreAgent(ctx, h.ID, agentID)
				}
				if (err != nil) != (outcome == "inspection failure") {
					t.Fatalf("recovery error: %v", err)
				}
			}
			if outcome == "inspection failure" {
				if err := os.Rename(workspace.Path+"-unavailable", workspace.Path); err != nil {
					t.Fatal(err)
				}
			}
			readAgent := func() holons.AgentSession {
				t.Helper()
				// Read the public projection used by refresh/reconnection, too.
				response := httptest.NewRecorder()
				holonshttp.New(service).ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/holons/"+h.ID, nil))
				var public holons.Holon
				if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &public) != nil {
					t.Fatalf("read holon: %d %s", response.Code, response.Body.String())
				}
				return public.AgentSession(agentID)
			}
			if strings.HasPrefix(outcome, "new commit") {
				if agent := readAgent(); agent.ClosedAt == nil {
					t.Fatalf("successful commit stayed open: %+v", agent)
				}
				if _, _, err := service.AddCommitAgent(ctx, h.ID, "source", "Commit next edits", git(workspace.Path, "rev-parse", "HEAD"), ""); err != nil {
					t.Fatalf("completed fork blocked the next commit: %v", err)
				}
				if outcome == "new commit dirty" {
					content, err := os.ReadFile(filepath.Join(workspace.Path, "remaining.txt"))
					if err != nil || string(content) != "unfinished\n" {
						t.Fatalf("concurrent changes were altered: %q, %v", content, err)
					}
				}
				return
			}
			assertWaiting := func() {
				t.Helper()
				if agent := readAgent(); agent.ClosedAt != nil || agent.InputState != string(protocol.InputUserRequired) || agent.Activity != protocol.ActivityNeedsInput {
					t.Fatalf("commit must wait for input: %+v", agent)
				}
			}
			assertWaiting()
			if restart && outcome == "inspection failure" {
				// Once the worktree is available, retry recovery with the saved pause.
				if _, err := (&terminalHolonService{Service: service, work: work}).restoreAgent(ctx, h.ID, agentID); err != nil {
					t.Fatal(err)
				}
				assertWaiting()
			}
			persisted, err := service.Get(ctx, h.ID)
			if err != nil || persisted.AgentSession(agentID).CommitStartHead != baseline || !persisted.HasActiveCommitAgent() {
				t.Fatalf("commit baseline or active state lost: %+v, %v", persisted, err)
			}
			if _, _, err := service.AddCommitAgent(ctx, h.ID, "source", "Commit", baseline, ""); !errors.Is(err, holons.ErrCommitForkInProgress) {
				t.Fatalf("waiting commit allowed a duplicate: %v", err)
			}
			if _, err := service.PrepareAgentRecovery(ctx, h.ID, agentID); err != nil {
				t.Fatal(err)
			}
			// Resume through Claude's real reducer: SessionStart clears its input,
			// but that initialization must not clear the commit's saved pause.
			reducer := claudecode.NewReducer(protocol.InputUserRequired)
			observeClaude := func(projection claudecode.Projection) {
				t.Helper()
				event := harness.Event{Type: harness.EventActivityChanged, Activity: projection.Activity, InputState: projection.InputState,
					ResumeTarget: projection.ResumeTarget, RolloutPath: projection.RolloutPath}
				if projection.InputChanged {
					event.Type = harness.EventInputStateChanged
				}
				if err := state.Observed(ctx, h.ID, agentID, event); err != nil {
					t.Fatal(err)
				}
			}
			observeClaude(reducer.Apply(claudecode.NormalizedEvent{Event: "SessionStart", Source: "resume", SessionID: "conversation"}))
			assertWaiting()
			observeClaude(reducer.ApplyPresence("idle"))
			assertWaiting()
			// Recovery may also replay activity without input, or the completed turn.
			for _, event := range []harness.Event{
				{Activity: protocol.ActivityCompleted, ObservabilityStatus: "healthy"}, complete,
				{Activity: protocol.ActivityIdle},
			} {
				if err := state.Observed(ctx, h.ID, agentID, event); err != nil {
					t.Fatal(err)
				}
				assertWaiting()
			}
			// Input is already None in the reducer, so the new prompt can report
			// working activity without another input-state change.
			observeClaude(reducer.Apply(claudecode.NormalizedEvent{Event: "UserPromptSubmit", SessionID: "conversation"}))
			if agent := readAgent(); agent.InputState != string(protocol.InputNone) || agent.Activity != protocol.ActivityWorking {
				t.Fatalf("new turn did not clear waiting: %+v", agent)
			}
			git(workspace.Path, "add", "-A")
			git(workspace.Path, "commit", "--allow-empty", "-m", "Finish requested commit")
			if err := state.Observed(ctx, h.ID, agentID, complete); err != nil {
				t.Fatal(err)
			}
			if agent := readAgent(); agent.ClosedAt == nil {
				t.Fatalf("successful retry stayed open: %+v", agent)
			}
		})
	}
}

func TestAddressCommitAgentStaysOpenWhilePublicationPending(t *testing.T) {
	for _, test := range []struct {
		name  string
		head  string
		dirty bool
	}{
		{name: "unchanged HEAD", head: "head"},
		{name: "changed HEAD", head: "new-head"},
		{name: "changed HEAD with follow-up edits", head: "new-head", dirty: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, service, _, completion := specializedAgentState(t, holons.KindPullWorker, pullrequestwork.ModeAuto, holons.WorkspaceInspection{HeadCommit: test.head, Dirty: test.dirty})
			state.work.(*completionWorkStub).work.PendingCompletion = &pullrequestwork.PendingAddressCompletion{State: "ready"}
			state.commitCloser = &terminalHolonService{Service: service}
			before, err := service.Get(t.Context(), "specialized")
			if err != nil {
				t.Fatal(err)
			}
			_, agentID, err := service.AddCommitAgent(t.Context(), before.ID, "agent", "Commit follow-up edits", "head", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.SetAgentSessionStatus(t.Context(), before.ID, agentID, holons.StatusRunning, "", "saved-address-conversation"); err != nil {
				t.Fatal(err)
			}
			if err := state.Observed(t.Context(), before.ID, agentID, harness.Event{Type: harness.EventInputStateChanged, InputState: protocol.InputTaskComplete, Activity: protocol.ActivityCompleted}); err != nil {
				t.Fatal(err)
			}
			after, err := service.Get(t.Context(), before.ID)
			if err != nil {
				t.Fatal(err)
			}
			agent := after.AgentSession(agentID)
			if agent.ClosedAt != nil || agent.InputState != string(protocol.InputTaskComplete) || agent.Activity != protocol.ActivityCompleted {
				t.Fatalf("pending Address fork changed completion lifecycle: %+v", agent)
			}
			if completion.calls != 0 {
				t.Fatalf("pending Address completion was published again: %d calls", completion.calls)
			}
			// A restart must apply the same eligibility rules to older Address
			// forks, even when they carry a saved starting HEAD.
			if err := clearStaleTerminalBindings(t.Context(), service); err != nil {
				t.Fatal(err)
			}
			runtime := &terminalHolonService{Service: service, work: state.work}
			restored, err := runtime.restoreAgent(t.Context(), before.ID, agentID)
			if err != nil {
				t.Fatal(err)
			}
			agent = restored.AgentSession(agentID)
			if agent.ClosedAt != nil || agent.InputState != string(protocol.InputTaskComplete) || agent.ResumeTarget != "saved-address-conversation" {
				t.Fatalf("recovery changed pending Address completion lifecycle: %+v", agent)
			}
		})
	}
}
