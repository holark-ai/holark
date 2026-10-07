//go:build claude_e2e || opencode_e2e

package localapp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/harness/cliprobe"
	"github.com/holark-ai/holark/internal/harness/codex"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
)

type commitForkE2ESpec struct {
	unsupportedVersion bool
	tabFork            bool
	nativeFork         func(*testing.T, context.Context, *terminalhost.Manager, *codex.Manager, holons.AgentSession) string
	kind               protocol.HarnessType
	prepare            func(*testing.T, string)
	afterTabFork       func(*testing.T, context.Context, *terminalhost.Manager, *agentsessions.Service, *holons.Service, *commitForkE2EState, string, holons.AgentSession, holons.AgentSession)
	permissionPrompt   string
	permissionAnswer   string
}

// Uses native CLIs and PTYs, the fork coordinator, real monitors, and SQLite
// runtime projection. No harness events or conversation files are synthesized.
func runCommitForkE2E(t *testing.T, spec commitForkE2ESpec) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	codexManager := codex.NewManager()
	t.Cleanup(func() { _ = codexManager.Close() })
	probeOptions := harness.ProbeOptions{}
	if spec.unsupportedVersion {
		// Only support policy changes: discover and launch the installed CLI.
		probeOptions.SupportPolicies = map[protocol.HarnessType]cliprobe.SupportPolicy{spec.kind: {Versions: []string{"0.0.0"}, Latest: "0.0.0"}}
	}
	driver, _ := harness.RegistryWithProbeOptions(codexManager, probeOptions).Driver(spec.kind)
	if capability := driver.Probe(ctx); !capability.Available {
		t.Fatalf("real %s CLI unavailable: %+v", spec.kind, capability)
	} else {
		t.Logf("%s capability: %+v", spec.kind, capability)
		if spec.unsupportedVersion && capability.SupportStatus != "unsupported" {
			t.Fatalf("expected an unsupported installed CLI: %+v", capability)
		}
	}
	_, store := terminalTestService(t)
	// Workspace inspection is outside this regression. An unchanged HEAD keeps
	// the commit fork waiting for input after each native turn.
	service := holons.NewServiceWithRepository(store, &commitAgentInspectionRecorder{})
	worktree, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if spec.prepare != nil {
		spec.prepare(t, worktree)
	}
	runtimeRoot := t.TempDir()
	manager, err := terminalhost.NewManager(terminalhost.Options{MaximumTerminals: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close() })
	observed := &commitForkE2EState{localAgentState: localAgentState{holons: service}, events: make(chan commitForkE2EObservation, 1024)}
	liveDriver := &commitForkE2EDriver{Driver: driver, ctx: ctx}
	t.Cleanup(func() { cancel(); liveDriver.monitors.Wait() })
	launcher := commitForkE2ELauncher{localAgentLauncher: localAgentLauncher{manager: manager}, ctx: ctx}
	agents, err := agentsessions.New(harness.NewRegistry(map[protocol.HarnessType]harness.Driver{spec.kind: liveDriver}), observed, launcher, nil, nil, runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agents.Close)
	for _, args := range [][]string{{"init", "-b", "main"}, {"-c", "user.name=Holark Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "Test workspace"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = worktree
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	now := time.Now().UTC()
	const holonID, originalID = "commit-fork-e2e", "original"
	if err := store.Create(ctx, holons.Holon{
		ID: holonID, Title: "Commit fork isolation", Kind: holons.KindNormal, Status: holons.StatusRunning,
		WorktreePath: worktree, WorktreeBranch: "main", BaseCommit: "HEAD", CreatedAt: now,
		AgentSessions: []holons.AgentSession{{ID: originalID, HolonID: holonID, AgentType: string(spec.kind), Title: "Original", Activity: protocol.ActivityStarting, Status: string(holons.StatusQueued), CreatedAt: now, UpdatedAt: now}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			h, _ := service.Get(context.Background(), holonID)
			for _, a := range h.AgentSessions {
				t.Logf("agent %s: input=%s conversation=%s rollout=%s", a.ID, a.InputState, a.ResumeTarget, a.RolloutPath)
				if restore, err := manager.Attach(terminals.TerminalID(a.TerminalID)); err == nil {
					output := string(restore.Checkpoint.ReplayPayload)
					for _, n := range restore.Tail {
						output += string(n.Data)
					}
					// Keep native startup/approval diagnostics bounded on failure.
					if len(output) > 12000 {
						output = output[len(output)-12000:]
					}
					t.Logf("terminal %s: %q", a.ID, output)
					_ = manager.Detach(terminals.TerminalID(a.TerminalID), restore.Attachment)
				}
			}
		}
	})
	// Every observed transition is checked, including transient identity/status
	// changes that a periodic final-state assertion could miss.
	identities := map[string]holons.AgentSession{}
	idle := map[string]bool{}
	wait := func(agentID string, input protocol.InputState) holons.AgentSession {
		t.Helper()
		deadline := time.NewTimer(90 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case observation := <-observed.events:
				if observation.err != nil {
					t.Fatal(observation.err)
				}
				a := observation.agent
				if a.ObservabilityStatus == string(protocol.ObservabilityDegraded) {
					t.Fatalf("observation degraded: %s", a.ObservabilityMessage)
				}
				if identity, ok := identities[a.ID]; ok && (identity.ResumeTarget != a.ResumeTarget || identity.RolloutPath != a.RolloutPath || identity.TerminalID != a.TerminalID) {
					t.Fatalf("agent %s changed identity while its peer ran: conversation %s -> %s, rollout %s -> %s", a.ID, identity.ResumeTarget, a.ResumeTarget, identity.RolloutPath, a.RolloutPath)
				}
				if idle[a.ID] && (a.InputState != identities[a.ID].InputState || a.Activity != identities[a.ID].Activity) {
					t.Fatalf("idle agent %s followed its peer into %s", a.ID, a.InputState)
				}
				matchingEvent := observation.event.Type == harness.EventInputStateChanged || (input == protocol.InputTaskComplete && observation.event.Type == harness.EventContextUsageChanged)
				if a.ID == agentID && matchingEvent && a.InputState == string(input) {
					if input == protocol.InputTaskComplete && (a.ContextTokens == nil || *a.ContextTokens <= 0) {
						continue
					}
					wantActivity := protocol.ActivityWorking
					switch input {
					case protocol.InputTaskComplete:
						wantActivity = protocol.ActivityCompleted
					case protocol.InputPermissionRequired, protocol.InputUserRequired:
						wantActivity = protocol.ActivityNeedsInput
					}
					if a.Activity != wantActivity {
						t.Fatalf("input %s activity=%s, want %s", input, a.Activity, wantActivity)
					}
					t.Logf("agent %s reached %s, activity=%s (%s)", a.ID, input, a.Activity, a.ResumeTarget)
					return a
				}
			case <-deadline.C:
				t.Fatalf("timed out waiting for %s to reach %s", agentID, input)
			}
		}
	}
	submit := func(agentID, prompt string) {
		t.Helper()
		if err := agents.Submit(ctx, holonID, agentID, prompt); err != nil {
			t.Fatal(err)
		}
	}
	if err := agents.Launch(ctx, holonID, originalID, terminals.Dimensions{Columns: 120, Rows: 36}, "", agentsessions.LaunchOptions{}); err != nil {
		t.Fatal(err)
	}
	// Observe the complete promptless startup before any user task is submitted.
	startup := time.NewTimer(90 * time.Second)
	defer startup.Stop()
initialized:
	for {
		select {
		case observation := <-observed.events:
			if observation.err != nil {
				t.Fatal(observation.err)
			}
			if observation.agent.Activity == protocol.ActivityWorking {
				t.Fatal("promptless initialization established work")
			}
			if (observation.agent.ResumeTarget != "" || spec.kind == protocol.HarnessOpenCode) && observation.agent.ObservabilityStatus == "healthy" {
				break initialized
			}
		case <-startup.C:
			t.Fatal("promptless startup did not initialize")
		}
	}
	if spec.kind == protocol.HarnessOpenCode {
		// The plugin initializes before the native editor mounts. Wait for the
		// actual prompt before typing; this output is never activity evidence.
		deadline := time.NewTimer(30 * time.Second)
		defer deadline.Stop()
		poll := time.NewTicker(25 * time.Millisecond)
		defer poll.Stop()
	ready:
		for {
			select {
			case <-poll.C:
				h, err := service.Get(ctx, holonID)
				if err != nil {
					t.Fatal(err)
				}
				a := h.AgentSessions[0]
				if a.Activity == protocol.ActivityWorking {
					t.Fatal("promptless editor initialization established working")
				}
				restore, err := manager.Attach(terminals.TerminalID(a.TerminalID))
				if err != nil {
					t.Fatal(err)
				}
				output := string(restore.Checkpoint.ReplayPayload)
				for _, n := range restore.Tail {
					output += string(n.Data)
				}
				_ = manager.Detach(terminals.TerminalID(a.TerminalID), restore.Attachment)
				if strings.Contains(output, "Ask anything") {
					break ready
				}
			case <-deadline.C:
				t.Fatal("OpenCode promptless editor did not mount")
			}
		}
	}
	submit(originalID, "Reply with exactly ORIGINAL_READY. Do not use tools.")
	original := wait(originalID, protocol.InputTaskComplete)
	if original.ResumeTarget == "" || (spec.kind == protocol.HarnessClaudeCode && original.RolloutPath == "") {
		t.Fatal("original conversation was not discovered")
	}
	identities[originalID], idle[originalID] = original, true
	var forkID string
	if spec.tabFork {
		fork, err := (&forkAgentCoordinator{holons: service, agents: agents}).ForkAgentSession(ctx, holonID, originalID)
		if err != nil {
			t.Fatal(err)
		}
		forkID = fork.ID
	} else {
		coordinator := commitAgentCoordinator{holons: service, agents: agents, templates: fixedPromptReader("Reply with exactly COMMIT_READY. Do not use tools or commit anything.")}
		forked, err := coordinator.CreateCommitAgent(ctx, holonID, originalID)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range forked.AgentSessions {
			if a.ID != originalID {
				forkID = a.ID
			}
		}
	}
	if forkID == "" {
		t.Fatal("fork coordinator did not create a fork")
	}
	var fork holons.AgentSession
	if spec.tabFork {
		deadline := time.NewTimer(90 * time.Second)
		defer deadline.Stop()
		for fork.ResumeTarget == "" || fork.ObservabilityStatus != string(protocol.ObservabilityHealthy) {
			select {
			case observation := <-observed.events:
				if observation.err != nil {
					t.Fatal(observation.err)
				}
				if observation.agent.ID == forkID {
					fork = observation.agent
					if fork.ObservabilityStatus == string(protocol.ObservabilityDegraded) {
						t.Fatalf("fork observation degraded: %s", fork.ObservabilityMessage)
					}
				}
			case <-deadline.C:
				t.Fatal("promptless tab fork did not establish a monitored conversation")
			}
		}
	} else {
		fork = wait(forkID, protocol.InputUserRequired)
	}
	if fork.ResumeTarget == "" || fork.ResumeTarget == original.ResumeTarget || fork.TerminalID == original.TerminalID || (spec.kind == protocol.HarnessClaudeCode && (fork.RolloutPath == "" || fork.RolloutPath == original.RolloutPath)) {
		t.Fatal("fork did not get a distinct conversation and terminal")
	}
	identities[forkID] = fork
	if spec.tabFork {
		successor := spec.nativeFork(t, ctx, manager, codexManager, fork)
		poll := time.NewTicker(250 * time.Millisecond)
		defer poll.Stop()
		// Thread discovery can precede the bridge persisting the new binding.
		bindingDeadline := time.NewTimer(90 * time.Second)
		defer bindingDeadline.Stop()
		var current holons.Holon
	forkBound:
		for {
			current, err = service.Get(ctx, holonID)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range current.AgentSessions {
				if a.ID == forkID && a.ResumeTarget == successor {
					break forkBound
				}
			}
			select {
			case <-poll.C:
			case <-bindingDeadline.C:
				t.Fatalf("TUI fork tab did not persist successor %s", successor)
			}
		}
		if len(current.AgentSessions) != 2 {
			t.Fatalf("TUI fork created %d agent tabs, want 2", len(current.AgentSessions))
		}
		for _, a := range current.AgentSessions {
			if a.ID == originalID && (a.ResumeTarget != original.ResumeTarget || a.TerminalID != original.TerminalID) {
				t.Fatalf("TUI fork changed original tab: %+v", a)
			}
			if a.ID == forkID {
				if a.ResumeTarget != successor || a.TerminalID != fork.TerminalID {
					t.Fatalf("TUI fork tab binding: conversation=%s terminal=%s, want conversation=%s terminal=%s", a.ResumeTarget, a.TerminalID, successor, fork.TerminalID)
				}
				fork = a
				identities[forkID] = a
			}
		}
		submit(forkID, spec.permissionPrompt)
		workingDeadline := time.NewTimer(90 * time.Second)
		defer workingDeadline.Stop()
	working:
		for {
			select {
			case observation := <-observed.events:
				if observation.err != nil {
					t.Fatal(observation.err)
				}
				a := observation.agent
				if a.ID == forkID && a.Activity == protocol.ActivityWorking {
					if a.ResumeTarget != fork.ResumeTarget {
						t.Fatalf("working fork changed conversation: %s -> %s", fork.ResumeTarget, a.ResumeTarget)
					}
					break working
				}
			case <-workingDeadline.C:
				t.Fatal("tab fork did not report working after submission")
			}
		}
		wait(forkID, protocol.InputPermissionRequired)
		if err := manager.Input(ctx, terminals.TerminalID(fork.TerminalID), []byte(spec.permissionAnswer)); err != nil {
			t.Fatal(err)
		}
		wait(forkID, protocol.InputTaskComplete)
		if spec.afterTabFork != nil {
			spec.afterTabFork(t, ctx, manager, agents, service, observed, holonID, original, fork)
		}
		return
	}
	script := "#!/bin/sh\nprintf started > activity-started\nwhile [ ! -f activity-release ]; do sleep 0.05; done\n"
	if err := os.WriteFile(filepath.Join(worktree, "holark-activity.sh"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	submit(forkID, "First use the shell tool to run exactly sh ./holark-activity.sh and wait for it to finish. Then follow these instructions: "+spec.permissionPrompt)
	markerDeadline := time.NewTimer(90 * time.Second)
	defer markerDeadline.Stop()
	markerPoll := time.NewTicker(25 * time.Millisecond)
	defer markerPoll.Stop()
executing:
	for {
		select {
		case <-markerPoll.C:
			if _, err := os.Stat(filepath.Join(worktree, "activity-started")); err == nil {
				break executing
			}
		case <-markerDeadline.C:
			t.Fatal("real tool did not write its started marker")
		}
	}
	current, err := service.Get(ctx, holonID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range current.AgentSessions {
		want := protocol.ActivityCompleted
		if a.ID == forkID {
			want = protocol.ActivityWorking
		}
		if a.Activity != want {
			t.Fatalf("executing marker: agent %s activity=%s, want %s", a.ID, a.Activity, want)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, "activity-release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	// The native permission prompt must leave the original agent unchanged.
	wait(forkID, protocol.InputPermissionRequired)
	if err := manager.Input(ctx, terminals.TerminalID(fork.TerminalID), []byte(spec.permissionAnswer)); err != nil {
		t.Fatal(err)
	}
	wait(forkID, protocol.InputUserRequired)
	idle[forkID], idle[originalID] = true, false
	submit(originalID, "Run the shell command sleep 2, then reply ORIGINAL_TURN_DONE.")
	wait(originalID, protocol.InputNone)
	original = wait(originalID, protocol.InputTaskComplete)
	// A subsequent fork/resume must still resolve the original conversation.
	resumed, err := driver.ResumeCommand(harness.ResumeSpec{RepositoryPath: worktree, SessionID: holonID, RuntimeDir: t.TempDir(), HarnessSession: protocol.HarnessSession{ID: originalID, ResumeTarget: original.ResumeTarget, RolloutPath: original.RolloutPath}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(resumed.Args, original.ResumeTarget) {
		t.Fatalf("resume redirected to the commit fork: %v", resumed.Args)
	}
}

type commitForkE2EObservation struct {
	event harness.Event
	agent holons.AgentSession
	err   error
}
type commitForkE2EState struct {
	localAgentState
	mu     sync.Mutex
	events chan commitForkE2EObservation
}

func (s *commitForkE2EState) Observed(ctx context.Context, hid, aid string, event harness.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.localAgentState.Observed(ctx, hid, aid, event)
	h, getErr := s.holons.Get(ctx, hid)
	if err == nil {
		err = getErr
	}
	for _, a := range h.AgentSessions {
		if a.ID == aid {
			s.events <- commitForkE2EObservation{event: event, agent: a, err: err}
			return err
		}
	}
	s.events <- commitForkE2EObservation{err: fmt.Errorf("observed missing agent %s: %v", aid, err)}
	return err
}

// This adapter only scopes the production monitor's lifetime to the test.
// Command construction, CLI execution and event interpretation are unchanged.
type commitForkE2EDriver struct {
	harness.Driver
	ctx      context.Context
	monitors sync.WaitGroup
}

func (d *commitForkE2EDriver) RequiresPrivateRuntime() bool {
	private, ok := d.Driver.(harness.PrivateRuntimeDriver)
	return ok && private.RequiresPrivateRuntime()
}

func (d *commitForkE2EDriver) PrepareMonitor(runtimeDir string) (harness.MonitorRuntime, error) {
	if preparer, ok := d.Driver.(harness.PreparedMonitorDriver); ok {
		return preparer.PrepareMonitor(runtimeDir)
	}
	return nil, nil
}

func (d *commitForkE2EDriver) Monitor(ctx context.Context, spec harness.MonitorSpec, send func(harness.Event)) {
	d.monitors.Add(1)
	defer d.monitors.Done()
	monitorCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(d.ctx, cancel)
	defer stop()
	defer cancel()
	d.Driver.Monitor(monitorCtx, spec, send)
}

type commitForkE2ELauncher struct {
	localAgentLauncher
	ctx context.Context
}

func (l commitForkE2ELauncher) Launch(ctx context.Context, spec terminals.LaunchSpec) (int, error) {
	pid, err := l.localAgentLauncher.Launch(ctx, spec)
	if err != nil {
		return pid, err
	}
	startNativeTerminalPeer(l.ctx, l.manager, spec.TerminalID)
	return pid, nil
}

func startNativeTerminalPeer(ctx context.Context, manager *terminalhost.Manager, terminalID terminals.TerminalID) {
	// Answer terminal capability queries normally handled by the browser. This
	// is a terminal peer, not a replacement for any part of an agent CLI.
	go func() {
		restore, err := manager.Attach(terminalID)
		if err != nil {
			return
		}
		defer manager.Detach(terminalID, restore.Attachment)
		cursor := terminalhost.Cursor{TerminalID: terminalID, Sequence: restore.LastSequence, CheckpointSequence: restore.Checkpoint.Sequence}
		respond := func(data []byte) {
			for _, pair := range [][2]string{{"\x1b[6n", "\x1b[1;1R"}, {"\x1b[c", "\x1b[?1;2c"}, {"\x1b]10;?", "\x1b]10;rgb:ffff/ffff/ffff\x1b\\"}, {"\x1b]11;?", "\x1b]11;rgb:0000/0000/0000\x1b\\"}} {
				if strings.Contains(string(data), pair[0]) {
					_ = manager.Input(ctx, terminalID, []byte(pair[1]))
				}
			}
		}
		for _, n := range restore.Tail {
			respond(n.Data)
		}
		for ctx.Err() == nil {
			stream, err := manager.AttachmentUpdates(ctx, terminalID, restore.Attachment, cursor, 256*1024)
			if err != nil {
				return
			}
			if stream.Checkpoint != nil {
				cursor.CheckpointSequence = stream.Checkpoint.Sequence
				cursor.Sequence = max(cursor.Sequence, stream.Checkpoint.Sequence)
			}
			for _, n := range stream.Notifications {
				cursor.Sequence = n.Sequence
				respond(n.Data)
			}
		}
	}()
}
