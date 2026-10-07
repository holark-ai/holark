//go:build opencode_e2e

package localapp

import (
	"context"
	"encoding/json"
	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/harness/codex"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/protocol"
)

func TestOpenCodeE2ECommitForkStatusIsolation(t *testing.T) {
	if os.Getenv("HOLARK_OPENCODE_E2E") != "1" {
		t.Skip("run make test-opencode to use the installed OpenCode CLI")
	}
	runCommitForkE2E(t, commitForkE2ESpec{
		kind: protocol.HarnessOpenCode, prepare: openCodeCommitE2EHome,
		permissionAnswer: "\r",
		permissionPrompt: "Use the bash tool to run exactly pwd. Do not use any other tool. After approval is answered, reply PERMISSION_DONE.",
	})
}

func TestOpenCodeE2ECrashRecovery(t *testing.T) {
	if os.Getenv("HOLARK_OPENCODE_E2E") != "1" {
		t.Skip("requires the installed, authenticated OpenCode CLI")
	}
	runAgentCrashRecoveryE2E(t, protocol.HarnessOpenCode, openCodeCommitE2EHome)
}

func openCodeCommitE2EHome(t *testing.T, _ string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	// Copy credentials and the recent model choice into temporary XDG roots;
	// sessions, permissions, plugins and user configuration stay isolated.
	for _, root := range []struct{ env, fallback, file string }{
		{"XDG_DATA_HOME", ".local/share", "auth.json"},
		{"XDG_STATE_HOME", ".local/state", "model.json"},
		{"XDG_CONFIG_HOME", ".config", ""},
		{"XDG_CACHE_HOME", ".cache", ""},
	} {
		source := os.Getenv(root.env)
		if source == "" {
			source = filepath.Join(home, root.fallback)
		}
		isolated := t.TempDir()
		if root.file != "" {
			data, err := os.ReadFile(filepath.Join(source, "opencode", root.file))
			if err == nil {
				if err := os.Mkdir(filepath.Join(isolated, "opencode"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(isolated, "opencode", root.file), data, 0600); err != nil {
					t.Fatal(err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
		t.Setenv(root.env, isolated)
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"autoupdate":false,"permission":{"bash":{"*":"ask","sleep 2":"allow","sh ./holark-activity.sh":"allow"}}}`)
}

func TestOpenCodeE2ESameTUIForkAndSelection(t *testing.T) {
	if os.Getenv("HOLARK_OPENCODE_E2E") != "1" {
		t.Skip("run make test-opencode")
	}
	runCommitForkE2E(t, commitForkE2ESpec{
		kind: protocol.HarnessOpenCode, prepare: openCodeCommitE2EHome, tabFork: true,
		nativeFork: openCodeNativeFork, afterTabFork: openCodeSelectionE2E,
		permissionAnswer: "\r", permissionPrompt: "Use the bash tool to run exactly pwd. Do not use any other tool. After approval is answered, reply PERMISSION_DONE.",
	})
}

func openCodeTerminalText(t *testing.T, manager *terminalhost.Manager, terminal string) string {
	t.Helper()
	restore, err := manager.Attach(terminals.TerminalID(terminal))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Detach(terminals.TerminalID(terminal), restore.Attachment)
	output := string(restore.Checkpoint.ReplayPayload)
	for _, n := range restore.Tail {
		output += string(n.Data)
	}
	return output
}

func openCodeWaitText(t *testing.T, manager *terminalhost.Manager, terminal, text string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(openCodeTerminalText(t, manager, terminal), text) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("OpenCode terminal did not show %q", text)
}

func openCodeKeys(t *testing.T, ctx context.Context, manager *terminalhost.Manager, terminal, keys string) {
	t.Helper()
	if err := manager.Input(ctx, terminals.TerminalID(terminal), []byte(keys)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond) // Native composer render cycle.
}

func openCodeSlash(t *testing.T, ctx context.Context, manager *terminalhost.Manager, terminal, command string) {
	t.Helper()
	openCodeKeys(t, ctx, manager, terminal, command)
	openCodeKeys(t, ctx, manager, terminal, "\r")
}

type openCodeStoredSession struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	ParentID string `json:"parentID"`
}

func openCodeSessions(t *testing.T) []openCodeStoredSession {
	t.Helper()
	output, err := exec.Command("opencode", "db", "SELECT id, title, parent_id AS parentID FROM session", "--format", "json", "--pure").Output()
	if err != nil {
		t.Fatal(err)
	}
	var sessions []openCodeStoredSession
	if err := json.Unmarshal(output, &sessions); err != nil {
		t.Fatal(err)
	}
	return sessions
}

func openCodeNativeFork(t *testing.T, ctx context.Context, manager *terminalhost.Manager, _ *codex.Manager, fork holons.AgentSession) string {
	t.Helper()
	openCodeWaitText(t, manager, fork.TerminalID, "commands")
	known := map[string]bool{}
	for _, s := range openCodeSessions(t) {
		known[s.ID] = true
	}
	openCodeSlash(t, ctx, manager, fork.TerminalID, "/fork")
	openCodeWaitText(t, manager, fork.TerminalID, "Full session")
	openCodeKeys(t, ctx, manager, fork.TerminalID, "\r")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range openCodeSessions(t) {
			if !known[s.ID] && s.ParentID == "" {
				return s.ID
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("native /fork did not create a successor")
	return ""
}

func openCodeSelectionE2E(t *testing.T, ctx context.Context, manager *terminalhost.Manager, agents *agentsessions.Service, service *holons.Service, observed *commitForkE2EState, holonID string, original, fork holons.AgentSession) {
	t.Helper()
	current := func() holons.AgentSession {
		h, err := service.Get(ctx, holonID)
		if err != nil {
			t.Fatal(err)
		}
		if len(h.AgentSessions) != 2 {
			t.Fatalf("navigation changed tab count: %d", len(h.AgentSessions))
		}
		peer := h.AgentSession(original.ID)
		if peer.ResumeTarget != original.ResumeTarget || peer.TerminalID != original.TerminalID || peer.Activity != protocol.ActivityCompleted {
			t.Fatalf("navigation changed peer: %+v", peer)
		}
		return h.AgentSession(fork.ID)
	}
	waitBinding := func(id string) holons.AgentSession {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			a := current()
			if a.ObservabilityStatus == "degraded" {
				t.Fatal(a.ObservabilityMessage)
			}
			if a.ResumeTarget == id && a.ObservabilityStatus == "healthy" {
				return a
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("selection %s was not persisted", id)
		return holons.AgentSession{}
	}
	// Select the earlier Holark fork, which has a distinct native title.
	var target openCodeStoredSession
	for _, s := range openCodeSessions(t) {
		if s.ID != fork.ResumeTarget && s.ID != original.ResumeTarget && s.ParentID == "" {
			target = s
			break
		}
	}
	if target.ID == "" {
		t.Fatal("missing earlier fork")
	}
	openCodeSlash(t, ctx, manager, fork.TerminalID, "/sessions")
	openCodeKeys(t, ctx, manager, fork.TerminalID, target.Title)
	openCodeKeys(t, ctx, manager, fork.TerminalID, "\r")
	selected := waitBinding(target.ID)
	if selected.TerminalID != fork.TerminalID {
		t.Fatal("selection replaced terminal")
	}
	// Close and reopen before another prompt: only the saved binding can resume it.
	lifecycle := &terminalHolonService{Service: service, agents: agents}
	if err := agents.StopTerminal(ctx, fork.TerminalID); err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseTerminal(terminals.TerminalID(fork.TerminalID), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	completions, err := manager.Completions(ctx, nil, 5*time.Second)
	if err != nil || len(completions) != 1 {
		t.Fatalf("terminal close: %v, %v", completions, err)
	}
	if _, err := service.CompleteAgentSession(ctx, holonID, fork.ID, fork.TerminalID, completions[0].ExitCode); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.ResumeAgentSession(ctx, holonID, fork.ID, ""); err != nil {
		t.Fatal(err)
	}
	selected = waitBinding(target.ID)
	if selected.TerminalID == fork.TerminalID {
		t.Fatal("reopen did not create a terminal")
	}
	openCodeWaitText(t, manager, selected.TerminalID, "commands")
	openCodeWaitText(t, manager, selected.TerminalID, "ORIGINAL_READY")
	if strings.Contains(openCodeTerminalText(t, manager, selected.TerminalID), "PERMISSION_DONE") {
		t.Fatal("reopened the successor instead of the selected earlier conversation")
	}
	for len(observed.events) > 0 {
		<-observed.events
	}
	if err := agents.Submit(ctx, holonID, fork.ID, "Reply with exactly SELECTED_READY. Do not use tools."); err != nil {
		t.Fatal(err)
	}
	selectedDeadline := time.NewTimer(90 * time.Second)
	defer selectedDeadline.Stop()
	selectedWorking := false
selectedTurn:
	for {
		select {
		case o := <-observed.events:
			if o.err != nil {
				t.Fatal(o.err)
			}
			if o.agent.ID != fork.ID {
				continue
			}
			if o.agent.ResumeTarget != target.ID {
				t.Fatalf("selected activity followed %s", o.agent.ResumeTarget)
			}
			if o.agent.Activity == protocol.ActivityWorking {
				selectedWorking = true
			}
			if o.event.Type == harness.EventInputStateChanged && o.agent.InputState == string(protocol.InputTaskComplete) {
				if !selectedWorking {
					t.Fatal("selected conversation never reported working")
				}
				current()
				break selectedTurn
			}
		case <-selectedDeadline.C:
			t.Fatal("selected conversation did not complete")
		}
	}
	// A new route retains the prior identity until OpenCode creates a real session.
	openCodeSlash(t, ctx, manager, selected.TerminalID, "/new")
	time.Sleep(200 * time.Millisecond)
	if a := current(); a.ResumeTarget != target.ID {
		t.Fatalf("home lost resumable identity: %+v", a)
	}
	// Drain old observations before checking subsequent activity attribution.
	for len(observed.events) > 0 {
		<-observed.events
	}
	if err := agents.Submit(ctx, holonID, fork.ID, "Use the task tool to ask a general subagent to reply CHILD_READY without tools. Wait for the subagent, then reply NEW_READY."); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	var newID string
	working, child := false, false
	for {
		select {
		case o := <-observed.events:
			if o.err != nil {
				t.Fatal(o.err)
			}
			if o.agent.ID != fork.ID {
				continue
			}
			a := o.agent
			if a.ObservabilityStatus == "degraded" {
				t.Fatal(a.ObservabilityMessage)
			}
			if a.ResumeTarget == target.ID {
				continue
			}
			if newID == "" {
				if a.ContextTokens != nil {
					t.Fatalf("new conversation inherited context: %d", *a.ContextTokens)
				}
				newID = a.ResumeTarget
			}
			if a.ResumeTarget != newID {
				t.Fatalf("subagent replaced root: %s -> %s", newID, a.ResumeTarget)
			}
			if a.Activity == protocol.ActivityWorking {
				working = true
			}
			if o.event.Type == harness.EventInputStateChanged && a.InputState == string(protocol.InputTaskComplete) {
				for _, s := range openCodeSessions(t) {
					if s.ParentID == newID {
						child = true
					}
				}
				if !working || !child {
					t.Fatalf("new session coverage: working=%v child=%v", working, child)
				}
				// The native child route must not become the tab's resumable identity.
				openCodeKeys(t, ctx, manager, selected.TerminalID, "\x18")
				openCodeKeys(t, ctx, manager, selected.TerminalID, "\x1b[B")
				openCodeWaitText(t, manager, selected.TerminalID, "Parent")
				time.Sleep(150 * time.Millisecond)
				if a := current(); a.ResumeTarget != newID || a.Activity != protocol.ActivityCompleted {
					t.Fatalf("viewing child replaced selected root: %+v", a)
				}
				return
			}
		case <-deadline.C:
			t.Fatal("new conversation/subagent did not complete")
		}
	}
}
