package localapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/holons"
	holonssqlite "github.com/holark-ai/holark/internal/holons/sqliteadapter"
	"github.com/holark-ai/holark/internal/ide"
	idesqlite "github.com/holark-ai/holark/internal/ide/sqliteadapter"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/sessionterminals"
	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
)

type launchGateway struct {
	launches   int
	fail       bool
	closeErr   map[terminals.TerminalID]error
	closed     []terminals.TerminalID
	closeCheck func(terminals.TerminalID)
}

func (g *launchGateway) Launch(context.Context, string, terminals.LaunchSpec) error {
	g.launches++
	if g.fail {
		return errors.New("launch failed")
	}
	return nil
}
func (*launchGateway) Attach(context.Context, string, terminals.TerminalID) (terminals.LiveAttachment, error) {
	return terminals.LiveAttachment{}, terminals.ErrProcessLost
}
func (*launchGateway) Detach(context.Context, string, terminals.TerminalID, terminals.TerminalAttachment) error {
	return nil
}
func (*launchGateway) Input(context.Context, string, terminals.TerminalID, terminals.TerminalAttachment, []byte) error {
	return nil
}
func (*launchGateway) Resize(context.Context, string, terminals.TerminalID, terminals.TerminalAttachment, uint64, terminals.Dimensions) error {
	return nil
}
func (*launchGateway) Signal(context.Context, string, terminals.TerminalID, string) error { return nil }
func (g *launchGateway) Close(_ context.Context, _ string, id terminals.TerminalID, _ terminals.TerminalAttachment) error {
	g.closed = append(g.closed, id)
	if g.closeCheck != nil {
		g.closeCheck(id)
	}
	return g.closeErr[id]
}

type cancellationIDECloser struct {
	store      *idesqlite.Store
	calls      []string
	closeErr   error
	closeCheck func()
	resumeErr  error
}

type recordingAgentLauncher struct {
	launches []string
	options  []agentsessions.LaunchOptions
	prompts  []string
	err      error
}

func (r *recordingAgentLauncher) Launch(_ context.Context, holonID, agentID string, _ terminals.Dimensions, prompt string, options agentsessions.LaunchOptions) error {
	r.launches = append(r.launches, holonID+"/"+agentID)
	r.options = append(r.options, options)
	r.prompts = append(r.prompts, prompt)
	return r.err
}
func (*recordingAgentLauncher) Cancel(context.Context, string, string) error         { return nil }
func (*recordingAgentLauncher) Submit(context.Context, string, string, string) error { return nil }

func (c *cancellationIDECloser) Close(ctx context.Context, holonID, id string) (ide.IDE, error) {
	c.calls = append(c.calls, id)
	v, err := c.store.Get(ctx, holonID, id)
	if err != nil {
		return ide.IDE{}, err
	}
	now := time.Now().UTC()
	v.State = ide.Stopping
	v.UpdatedAt = now
	v, err = c.store.Update(ctx, v)
	if err != nil {
		return v, err
	}
	if c.closeCheck != nil {
		c.closeCheck()
	}
	if c.closeErr != nil {
		v.Reason = c.closeErr.Error()
		v, err = c.store.Update(ctx, v)
		return v, errors.Join(c.closeErr, err)
	}
	v.State = ide.Closed
	v.DesiredOpen = false
	v.ClosedAt = &now
	v, err = c.store.Update(ctx, v)
	return v, err
}

func (c *cancellationIDECloser) Suspend(ctx context.Context, holonID, id string) (ide.IDE, error) {
	c.calls = append(c.calls, id)
	v, err := c.store.Get(ctx, holonID, id)
	if err != nil {
		return ide.IDE{}, err
	}
	now := time.Now().UTC()
	v.State = ide.Stopping
	v.UpdatedAt = now
	v, err = c.store.Update(ctx, v)
	if err != nil {
		return v, err
	}
	if c.closeCheck != nil {
		c.closeCheck()
	}
	if c.closeErr != nil {
		v.Reason = c.closeErr.Error()
		v, err = c.store.Update(ctx, v)
		return v, errors.Join(c.closeErr, err)
	}
	v.State = ide.Suspended
	v.Reason = ""
	v.ClosedAt = &now
	v, err = c.store.Update(ctx, v)
	return v, err
}

func (c *cancellationIDECloser) Resume(ctx context.Context, holonID, id string) (ide.IDE, error) {
	if c.resumeErr != nil {
		return ide.IDE{}, c.resumeErr
	}
	v, err := c.store.Get(ctx, holonID, id)
	if err != nil {
		return ide.IDE{}, err
	}
	v.State = ide.Starting
	v.Reason = ""
	v.ReadyAt = nil
	v.ClosedAt = nil
	v.UpdatedAt = time.Now().UTC()
	return c.store.Update(ctx, v)
}

func terminalTestService(t *testing.T) (*holons.Service, *holonssqlite.Store) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := holonssqlite.New(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = store.Create(context.Background(), holons.Holon{ID: "h", Title: "H", Kind: holons.KindNormal, Status: holons.StatusRunning, BaseCommit: "abc", WorktreePath: t.TempDir(), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return holons.NewService(store), store
}
func terminalRuntime(t *testing.T, service *holons.Service, gateway *launchGateway) *terminalHolonService {
	t.Helper()
	coordinator, err := sessionterminals.New(gateway, &localTerminalProducts{holons: service})
	if err != nil {
		t.Fatal(err)
	}
	return &terminalHolonService{Service: service, terminals: coordinator}
}

func TestCreateSelectsIdentityNamingPolicy(t *testing.T) {
	service, store := terminalTestService(t)
	agents := &recordingAgentLauncher{}
	runtime := &terminalHolonService{Service: service, agents: agents}
	tests := []struct {
		name          string
		input         holons.Create
		wantTitle     string
		generateTitle bool
	}{
		{name: "blank new Holon title", input: holons.Create{Prompt: "  Fix race\nDetails", Kind: holons.KindNormal, BaseCommit: "abc", AgentType: "codex"}, wantTitle: "Fix race", generateTitle: true},
		{name: "explicit new Holon title", input: holons.Create{Title: "  Keep this title  ", Prompt: "Fix race", Kind: holons.KindNormal, BaseCommit: "abc", AgentType: "codex"}, wantTitle: "Keep this title"},
		{name: "Issue title", input: holons.Create{Title: "Issue title", Prompt: "Fix issue", Kind: holons.KindIssue, BaseCommit: "abc", AgentType: "codex"}, wantTitle: "Issue title"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := len(agents.options)
			h, err := runtime.Create(t.Context(), test.input)
			if err != nil {
				t.Fatal(err)
			}
			if h.Title != test.wantTitle {
				t.Fatalf("title=%q, want %q", h.Title, test.wantTitle)
			}
			if len(agents.options) != before+1 {
				t.Fatalf("launch options=%+v", agents.options)
			}
			options := agents.options[before]
			if !options.GenerateIdentity || options.GenerateTitle != test.generateTitle {
				t.Fatalf("launch options=%+v, generate title want=%v", options, test.generateTitle)
			}
		})
	}

	existing, err := service.Get(t.Context(), "h")
	if err != nil {
		t.Fatal(err)
	}
	existing.WorktreeBranch = "holark/h"
	if err = store.Update(t.Context(), existing); err != nil {
		t.Fatal(err)
	}
	before := len(agents.options)
	if _, err = runtime.AddAgentSession(t.Context(), "h", "codex", "Follow up"); err != nil {
		t.Fatal(err)
	}
	if len(agents.options) != before+1 || agents.options[before] != (agentsessions.LaunchOptions{}) {
		t.Fatalf("additional agent reruns identity naming with options=%+v", agents.options[before:])
	}
}

func TestManualTerminalLimitDoesNotLaunchOrPersistTheHundredFirst(t *testing.T) {
	service, _ := terminalTestService(t)
	gateway := &launchGateway{}
	runtime := terminalRuntime(t, service, gateway)
	for i := 0; i < 100; i++ {
		if _, err := runtime.AddManualTerminal(context.Background(), "h", "", "", " "); err != nil {
			t.Fatalf("terminal %d: %v", i, err)
		}
	}
	if _, err := runtime.AddManualTerminal(context.Background(), "h", "", "", ""); !errors.Is(err, holons.ErrTerminalLimit) {
		t.Fatalf("limit error=%v", err)
	}
	h, _ := service.Get(context.Background(), "h")
	if gateway.launches != 100 || len(h.ManualTerminals) != 100 {
		t.Fatalf("launches=%d records=%d", gateway.launches, len(h.ManualTerminals))
	}
}

func TestLaunchFailureLeavesNoOpenBinding(t *testing.T) {
	service, _ := terminalTestService(t)
	runtime := terminalRuntime(t, service, &launchGateway{fail: true})
	if _, err := runtime.AddManualTerminal(context.Background(), "h", "", "", ""); err == nil {
		t.Fatal("expected launch failure")
	}
	h, _ := service.Get(context.Background(), "h")
	open := 0
	for _, tab := range h.ManualTerminals {
		if tab.ClosedAt == nil || tab.TerminalID != "" {
			open++
		}
	}
	if open != 0 {
		t.Fatalf("live records after failure: %+v", h.ManualTerminals)
	}
}

func TestRestartClearsBindingWithoutLaunchAndExplicitRelaunchStartsReplacement(t *testing.T) {
	manager, err := terminalhost.NewManager(terminalhost.Options{})
	if err != nil {
		t.Fatal(err)
	}
	oldID, _ := terminals.NewID()
	pid, err := manager.Launch(context.Background(), terminals.LaunchSpec{TerminalID: oldID, Kind: terminals.LaunchCommand, Command: "/bin/sh", Arguments: []string{"-c", "while :; do sleep 1; done"}, Environment: os.Environ(), Dimensions: terminals.Dimensions{Columns: 80, Rows: 24}})
	if err != nil {
		t.Fatal(err)
	}
	manager.Close()
	if err = syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("old PTY child still exists after shutdown: %v", err)
	}
	service, _ := terminalTestService(t)
	gateway := &launchGateway{}
	runtime := terminalRuntime(t, service, gateway)
	created, err := runtime.AddManualTerminal(context.Background(), "h", "Shell", "", "")
	if err != nil {
		t.Fatal(err)
	}
	record := created.ManualTerminals[0]
	if err = clearStaleTerminalBindings(context.Background(), service); err != nil {
		t.Fatal(err)
	}
	reopened, _ := service.Get(context.Background(), "h")
	if reopened.ManualTerminals[0].TerminalID != "" || gateway.launches != 1 {
		t.Fatalf("startup state=%+v launches=%d", reopened.ManualTerminals, gateway.launches)
	}
	replacement, err := runtime.RelaunchManualTerminal(context.Background(), "h", record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.TerminalID == "" || replacement.TerminalID == record.TerminalID || gateway.launches != 2 {
		t.Fatalf("replacement=%+v launches=%d", replacement, gateway.launches)
	}
}

func TestHolonCancellationPersistsIntentClosesEveryResourceAndRedelivers(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "cancel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := holonssqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ids := make([]terminals.TerminalID, 3)
	for index := range ids {
		ids[index], err = terminals.NewID()
		if err != nil {
			t.Fatal(err)
		}
	}
	h := holons.Holon{ID: "cancel", Title: "Cancel", Kind: holons.KindNormal, Status: holons.StatusRunning, BaseCommit: "abc", WorktreePath: "/tmp", CreatedAt: now,
		AgentSessions: []holons.AgentSession{
			{ID: "one", HolonID: "cancel", TerminalID: string(ids[0]), AgentType: "codex", Status: string(holons.StatusRunning), CreatedAt: now, UpdatedAt: now},
			{ID: "two", HolonID: "cancel", TerminalID: string(ids[1]), AgentType: "codex", Status: string(holons.StatusRunning), CreatedAt: now, UpdatedAt: now},
		},
	}
	if err = store.Create(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if _, err = holons.NewService(store).AddManualTerminal(t.Context(), "cancel", "Shell", "/tmp", string(ids[2])); err != nil {
		t.Fatal(err)
	}
	ideStore, err := idesqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ideStore.Create(t.Context(), ide.IDE{ID: "editor", HolonID: "cancel", Provider: "vscode", State: ide.Ready, DesiredOpen: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	service := holons.NewService(store)
	assertIntent := func() {
		current, getErr := service.Get(t.Context(), "cancel")
		if getErr != nil || current.Status != holons.StatusCancelling {
			t.Fatalf("shutdown observed before durable intent: holon=%+v err=%v", current, getErr)
		}
		for _, agent := range current.AgentSessions {
			if agent.Status != string(holons.StatusCancelling) {
				t.Fatalf("shutdown observed agent before durable intent: %+v", agent)
			}
		}
	}
	gateway := &launchGateway{closeErr: map[terminals.TerminalID]error{ids[0]: errors.New("first close failed")}, closeCheck: func(terminals.TerminalID) { assertIntent() }}
	coordinator, err := sessionterminals.New(gateway, &localTerminalProducts{holons: service})
	if err != nil {
		t.Fatal(err)
	}
	assertIDEIntent := func() {
		assertIntent()
		current, getErr := service.Get(t.Context(), "cancel")
		if getErr != nil || len(current.IDEs) != 1 || current.IDEs[0].State != string(ide.Stopping) || !current.IDEs[0].DesiredOpen {
			t.Fatalf("IDE shutdown observed before durable stopping intent: IDEs=%+v err=%v", current.IDEs, getErr)
		}
	}
	editors := &cancellationIDECloser{store: ideStore, closeErr: errors.New("IDE close failed"), closeCheck: assertIDEIntent}
	runtime := &terminalHolonService{Service: service, terminals: coordinator, ides: editors}
	got, cancelErr := runtime.End(t.Context(), "cancel")
	if cancelErr == nil {
		t.Fatal("expected joined shutdown errors")
	}
	if got.Status != holons.StatusCancelling || len(gateway.closed) != 3 || len(editors.calls) != 1 || got.ManualTerminals[0].ClosedAt == nil || !got.IDEs[0].DesiredOpen || got.IDEs[0].State != string(ide.Stopping) {
		t.Fatalf("first cancellation holon=%+v terminals=%v IDEs=%v err=%v", got, gateway.closed, editors.calls, cancelErr)
	}
	editors.closeErr = nil
	delete(gateway.closeErr, ids[0])
	if got, err = runtime.End(t.Context(), "cancel"); err != nil || got.Status != holons.StatusCancelling || got.IDEs[0].State != string(ide.Closed) || got.IDEs[0].DesiredOpen {
		t.Fatalf("redelivery did not close IDE: holon=%+v err=%v", got, err)
	}
	if len(gateway.closed) != 5 {
		t.Fatalf("redelivery closed terminals=%v, want both harnesses twice and shell once", gateway.closed)
	}
	if err = (&localTerminalProducts{holons: service}).ApplyTerminalCompletion(localTerminalHost, terminals.ProcessCompletion{TerminalID: ids[0], ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	mid, _ := service.Get(t.Context(), "cancel")
	if mid.Status != holons.StatusCancelling || mid.AgentSessions[0].Status != string(holons.StatusCancelled) {
		t.Fatalf("first completion=%+v", mid)
	}
	if err = (&localTerminalProducts{holons: service}).ApplyTerminalCompletion(localTerminalHost, terminals.ProcessCompletion{TerminalID: ids[1], ExitCode: 9}); err != nil {
		t.Fatal(err)
	}
	got, _ = service.Get(t.Context(), "cancel")
	if got.Status != holons.StatusCancelled || got.AgentSessions[1].Status != string(holons.StatusCancelled) {
		t.Fatalf("completed cancellation=%+v", got)
	}
}

func TestHolonReopenRelaunchesRetainedAgentsWithoutPromptAndDesiredSuspendedIDE(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "resume.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := holonssqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	h := holons.Holon{ID: "resume", Title: "Resume", Kind: holons.KindNormal, Status: holons.StatusCancelled, BaseBranch: "main", BaseCommit: "abc", WorktreeBranch: "holark/resume", WorktreePath: "/tmp/worktree", CreatedAt: now, FinishedAt: &now, ArchivedAt: &now, AgentSessions: []holons.AgentSession{{ID: "agent", HolonID: "resume", AgentType: "codex", Status: string(holons.StatusCancelled), ResumeTarget: "saved-conversation", CreatedAt: now, UpdatedAt: now, FinishedAt: &now}}}
	if err = store.Create(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	ideStore, err := idesqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ideStore.Create(t.Context(), ide.IDE{ID: "editor", HolonID: "resume", Provider: "vscode", State: ide.Suspended, DesiredOpen: true, CreatedAt: now, UpdatedAt: now, ClosedAt: &now}); err != nil {
		t.Fatal(err)
	}
	agents := &recordingAgentLauncher{}
	editors := &cancellationIDECloser{store: ideStore}
	repository := &workLauncherRepository{}
	runtime := &terminalHolonService{Service: holons.NewServiceWithRepository(store, repository), agents: agents, ides: editors}
	got, err := runtime.Reopen(t.Context(), "resume")
	if err != nil || got.Status != holons.StatusQueued || len(agents.launches) != 1 || agents.launches[0] != "resume/agent" || got.AgentSessions[0].Status != string(holons.StatusQueued) {
		t.Fatalf("resume holon=%+v launches=%v err=%v", got, agents.launches, err)
	}
	if agents.options[0] != (agentsessions.LaunchOptions{RequireResume: true}) {
		t.Fatalf("resume launch options=%+v", agents.options[0])
	}
	if agents.prompts[0] != "" || repository.reopened != "holark/resume" || got.ArchivedAt != nil {
		t.Fatalf("reopen prompt=%q repository=%+v holon=%+v", agents.prompts[0], repository, got)
	}
	if len(got.IDEs) != 1 || got.IDEs[0].ID != "editor" || got.IDEs[0].State != string(ide.Starting) || !got.IDEs[0].DesiredOpen || got.IDEs[0].ClosedAt != nil {
		t.Fatalf("resumed IDE record=%+v", got.IDEs)
	}
}

func TestAgentResumeRequiresSavedConversation(t *testing.T) {
	service, store := terminalTestService(t)
	h, err := service.Get(t.Context(), "h")
	if err != nil {
		t.Fatal(err)
	}
	h.WorktreeBranch = "holark/h"
	if err = store.Update(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	const resumeTarget = "saved-agent-conversation"
	if _, err = store.AddAgentSession(t.Context(), h.ID, holons.AgentSession{ID: "agent", HolonID: h.ID, AgentType: "codex", Status: string(holons.StatusFailed), ResumeTarget: resumeTarget, CreatedAt: now, UpdatedAt: now, FinishedAt: &now}); err != nil {
		t.Fatal(err)
	}
	agents := &recordingAgentLauncher{}
	runtime := &terminalHolonService{Service: service, agents: agents}
	got, err := runtime.ResumeAgentSession(t.Context(), h.ID, "agent", "")
	if err != nil {
		t.Fatal(err)
	}
	agent := got.AgentSession("agent")
	if agent.Status != string(holons.StatusQueued) || agent.ResumeTarget != resumeTarget {
		t.Fatalf("resumed agent=%+v", agent)
	}
	if len(agents.options) != 1 || agents.options[0] != (agentsessions.LaunchOptions{RequireResume: true}) {
		t.Fatalf("resume launch options=%+v", agents.options)
	}
}

func TestLostTerminalPreservesAgentResumeTarget(t *testing.T) {
	service, store := terminalTestService(t)
	now := time.Now().UTC()
	terminalID, err := terminals.NewID()
	if err != nil {
		t.Fatal(err)
	}
	const resumeTarget = "lost-agent-conversation"
	if _, err = store.AddAgentSession(t.Context(), "h", holons.AgentSession{ID: "agent", HolonID: "h", TerminalID: string(terminalID), AgentType: "codex", Status: string(holons.StatusRunning), ResumeTarget: resumeTarget, CreatedAt: now, UpdatedAt: now, StartedAt: &now}); err != nil {
		t.Fatal(err)
	}
	products := &localTerminalProducts{holons: service}
	if err = products.ApplyTerminalLost(localTerminalHost, terminalID); err != nil {
		t.Fatal(err)
	}
	h, err := service.Get(t.Context(), "h")
	if err != nil {
		t.Fatal(err)
	}
	agent := h.AgentSession("agent")
	if agent.Status != string(holons.StatusLost) || agent.ResumeTarget != resumeTarget {
		t.Fatalf("lost agent=%+v", agent)
	}
}

func TestResumeLogsFailures(t *testing.T) {
	for _, action := range []string{"holon", "agent", "ide"} {
		t.Run(action, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			db, err := database.Open(filepath.Join(t.TempDir(), "resume.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			store, err := holonssqlite.New(t.Context(), db)
			if err != nil {
				t.Fatal(err)
			}
			service := holons.NewService(store)
			now := time.Now().UTC()
			h := holons.Holon{ID: "resume-log", Title: "Resume", Kind: holons.KindNormal, Status: holons.StatusFailed, BaseBranch: "main", BaseCommit: "abc", WorktreeBranch: "holark/resume-log", WorktreePath: t.TempDir(), CreatedAt: now, AgentSessions: []holons.AgentSession{{ID: "agent", HolonID: "resume-log", AgentType: "codex", Status: string(holons.StatusFailed), CreatedAt: now, UpdatedAt: now}}}
			if err := store.Create(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("test resume failure")
			launcher := &recordingAgentLauncher{err: failure}
			runtime := &terminalHolonService{Service: service, agents: launcher}
			wantMessage, idKey, componentID := "Resume agent failed", "agent_session_id", "agent"
			if action == "ide" {
				launcher.err = nil
				editors, err := idesqlite.New(t.Context(), db)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := editors.Create(t.Context(), ide.IDE{ID: "editor", HolonID: h.ID, Provider: "vscode", State: ide.Suspended, DesiredOpen: true, CreatedAt: now, UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
				runtime.ides = &cancellationIDECloser{store: editors, resumeErr: failure}
				wantMessage, idKey, componentID = "Resume IDE failed", "ide_id", "editor"
			}
			if action == "agent" {
				_, err = runtime.ResumeAgentSession(t.Context(), h.ID, "agent", "resume-target")
			} else {
				_, err = runtime.Resume(t.Context(), h.ID)
			}
			if action == "ide" {
				if err != nil {
					t.Fatalf("resume error=%v, want success", err)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("resume error=%v", err)
			}
			var record map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
				t.Fatalf("expected one log record: %s (%v)", &output, err)
			}
			if record["msg"] != wantMessage || record["level"] != "ERROR" || record["holon_id"] != h.ID || record[idKey] != componentID || record["error"] != failure.Error() {
				t.Fatalf("unexpected log: %s", &output)
			}
		})
	}
}

func TestAgentCancelAndCloseUseBoundedHarnessShutdownWithoutTouchingSibling(t *testing.T) {
	for _, action := range []string{"cancel", "close"} {
		t.Run(action, func(t *testing.T) {
			db, err := database.Open(filepath.Join(t.TempDir(), "agent.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			store, err := holonssqlite.New(t.Context(), db)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			one, _ := terminals.NewID()
			two, _ := terminals.NewID()
			h := holons.Holon{ID: "h", Title: "H", Kind: holons.KindNormal, Status: holons.StatusRunning, BaseCommit: "abc", CreatedAt: now, AgentSessions: []holons.AgentSession{
				{ID: "one", HolonID: "h", TerminalID: string(one), AgentType: "codex", Status: string(holons.StatusRunning), CreatedAt: now, UpdatedAt: now},
				{ID: "two", HolonID: "h", TerminalID: string(two), AgentType: "codex", Status: string(holons.StatusRunning), CreatedAt: now, UpdatedAt: now},
			}}
			if err = store.Create(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			service := holons.NewService(store)
			gateway := &launchGateway{closeCheck: func(id terminals.TerminalID) {
				current, getErr := service.Get(t.Context(), "h")
				if getErr != nil || id != one || current.AgentSessions[0].Status != string(holons.StatusCancelling) || current.AgentSessions[0].ClosedAt != nil {
					t.Fatalf("shutdown ordering id=%s holon=%+v err=%v", id, current, getErr)
				}
			}}
			coordinator, _ := sessionterminals.New(gateway, &localTerminalProducts{holons: service})
			runtime := &terminalHolonService{Service: service, terminals: coordinator}
			var got holons.Holon
			if action == "cancel" {
				got, err = runtime.CancelAgentSession(t.Context(), "h", "one")
			} else {
				got, err = runtime.CloseAgentSession(t.Context(), "h", "one")
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(gateway.closed) != 1 || gateway.closed[0] != one || got.AgentSessions[1].Status != string(holons.StatusRunning) || got.AgentSessions[1].ClosedAt != nil {
				t.Fatalf("action=%s holon=%+v closed=%v", action, got, gateway.closed)
			}
			if action == "close" && got.AgentSessions[0].ClosedAt == nil {
				t.Fatalf("closed agent remains visible: %+v", got.AgentSessions[0])
			}
			if action == "close" {
				products := &localTerminalProducts{holons: service}
				if _, ok := products.ResolveTerminalBinding(one); ok {
					t.Fatal("closed agent remained available for ordinary attachment")
				}
				if completionErr := products.ApplyTerminalCompletion(localTerminalHost, terminals.ProcessCompletion{TerminalID: one, ExitCode: 9}); completionErr != nil {
					t.Fatal(completionErr)
				}
				settled, getErr := service.Get(t.Context(), "h")
				if getErr != nil || settled.AgentSessions[0].ClosedAt == nil || settled.AgentSessions[0].Status != string(holons.StatusCancelled) || settled.AgentSessions[0].TerminalID != "" {
					t.Fatalf("late closed-agent completion=%+v err=%v", settled, getErr)
				}
			}
		})
	}
}

func TestAgentCloseFailureKeepsProcessVisible(t *testing.T) {
	service, store := terminalTestService(t)
	now := time.Now().UTC()
	one, _ := terminals.NewID()
	h, err := store.AddAgentSession(t.Context(), "h", holons.AgentSession{ID: "one", HolonID: "h", TerminalID: string(one), AgentType: "codex", Status: string(holons.StatusRunning), CreatedAt: now, UpdatedAt: now})
	if err != nil || len(h.AgentSessions) != 1 {
		t.Fatalf("fixture=%+v err=%v", h, err)
	}
	gateway := &launchGateway{closeErr: map[terminals.TerminalID]error{one: errors.New("close failed")}}
	coordinator, _ := sessionterminals.New(gateway, &localTerminalProducts{holons: service})
	runtime := &terminalHolonService{Service: service, terminals: coordinator}
	got, err := runtime.CloseAgentSession(t.Context(), "h", "one")
	if err == nil || got.AgentSessions[0].ClosedAt != nil || got.AgentSessions[0].Status != string(holons.StatusCancelling) {
		t.Fatalf("failed close hid process: holon=%+v err=%v", got, err)
	}
}

func TestUnavailableAgentStartupPersistsFailure(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancelled=%t", cancelled), func(t *testing.T) {
			service, store := terminalTestService(t)
			now := time.Now().UTC()
			if err := store.Create(t.Context(), holons.Holon{ID: "unavailable", Title: "Unavailable", Kind: holons.KindNormal, Status: holons.StatusQueued, CreatedAt: now,
				AgentSessions: []holons.AgentSession{{ID: "agent", HolonID: "unavailable", AgentType: "codex", Title: "Agent", Status: "queued", Activity: protocol.ActivityStarting, CreatedAt: now, UpdatedAt: now}},
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			state := startupCancellationState{localAgentState: localAgentState{holons: service}}
			if cancelled {
				state.cancel = cancel
			}
			// An empty production registry is an unavailable installation, not a fake CLI.
			agents, err := agentsessions.New(harness.NewRegistry(nil), state, localAgentLauncher{}, nil, nil, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer agents.Close()
			launchErr := agents.Launch(ctx, "unavailable", "agent", terminals.Dimensions{}, "", agentsessions.LaunchOptions{})
			if !errors.Is(launchErr, agentsessions.ErrUnavailable) {
				t.Fatalf("launch error=%v", launchErr)
			}
			h, err := service.Get(t.Context(), "unavailable")
			if err != nil {
				t.Fatal(err)
			}
			a := h.AgentSessions[0]
			if h.Status != holons.StatusFailed || a.Status != "failed" || a.Reason != launchErr.Error() || a.FinishedAt == nil {
				t.Fatalf("stranded startup: %+v", a)
			}
		})
	}
}

type startupCancellationState struct {
	localAgentState
	cancel context.CancelFunc
}

func (s startupCancellationState) Session(ctx context.Context, hid, aid string) (agentsessions.Session, error) {
	session, err := s.localAgentState.Session(ctx, hid, aid)
	if s.cancel != nil {
		s.cancel()
	}
	return session, err
}
