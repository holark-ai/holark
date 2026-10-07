//go:build opencode_e2e

package opencode

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
)

// This checks real CLI startup and model selection without making a model request.
func TestOpenCodeE2EFreshSelectedModel(t *testing.T) {
	if os.Getenv("HOLARK_OPENCODE_E2E") != "1" {
		t.Skip("set HOLARK_OPENCODE_E2E=1 to run the real OpenCode smoke test")
	}
	probe := Probe(t.Context())
	if !probe.Available || !IsV2Version(probe.Version) {
		t.Skip("OpenCode v2 is required for the selected-model launch regression")
	}
	t.Setenv(ConfigEnvironment, "")
	if err := os.Unsetenv(ConfigEnvironment); err != nil {
		t.Fatal(err)
	}
	// Mini must use the requested worktree even when Holark's PWD differs.
	t.Setenv("PWD", t.TempDir())
	// Keep the path short enough to fit in the terminal header.
	worktree, err := os.MkdirTemp("", "holark-model-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(worktree) })
	worktree, err = filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	command, err := CommandWithVersion("opencode", probe.Version, openCodeE2ERuntime(t), worktree,
		"session-model-e2e", "harness-model-e2e", "", "openai/gpt-5.2")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := terminalhost.NewManager(terminalhost.Options{MaximumTerminals: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	terminalID := terminals.TerminalID("terminal-opencode-model-e2e")
	launchOpenCodeE2ETerminal(t, manager, terminalID, command.Path, command.Args[1:], command.Env, command.Dir)
	waitOpenCodeE2EScreenText(t, manager, terminalID, worktree, false, 30*time.Second)
	waitOpenCodeE2EScreenText(t, manager, terminalID, "gpt-5.2", false, 30*time.Second)
	waitOpenCodeE2EScreenText(t, manager, terminalID, "openai", false, 30*time.Second)
	waitOpenCodeE2EScreenText(t, manager, terminalID, "Ask anything", false, 30*time.Second)
	closeOpenCodeE2ETerminal(t, manager, terminalID)
}

func TestOpenCodeE2ENativeTUIFreshAndResume(t *testing.T) {
	if os.Getenv("HOLARK_OPENCODE_E2E") != "1" {
		t.Skip("set HOLARK_OPENCODE_E2E=1 to run the real OpenCode smoke test")
	}
	probe := Probe(t.Context())
	if !probe.Available {
		t.Skipf("supported OpenCode CLI is unavailable: %s", probe.Reason)
	}
	worktree := t.TempDir()
	manager, err := terminalhost.NewManager(terminalhost.Options{MaximumTerminals: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	const firstPrompt = "Reply with exactly: holark-opencode-first-ok"
	firstRuntime := openCodeE2ERuntime(t)
	command, err := Command("opencode", firstRuntime, worktree, "session-e2e", "harness-e2e", firstPrompt)
	if err != nil {
		t.Fatal(err)
	}
	firstEvents, stopFirstMonitor, firstMonitorDone := startOpenCodeE2EMonitor(firstRuntime, "", protocol.InputNone)
	firstTerminal := terminals.TerminalID("terminal-opencode-e2e-first")
	launchOpenCodeE2ETerminal(t, manager, firstTerminal, command.Path, command.Args[1:], command.Env, command.Dir)
	waitOpenCodeE2EEvent(t, firstEvents, func(event MonitorEvent) bool {
		return event.Type == MonitorObservability && event.ObservabilityStatus == protocol.ObservabilityHealthy
	})
	metadata := waitOpenCodeE2EEvent(t, firstEvents, func(event MonitorEvent) bool { return event.Type == MonitorMetadata })
	if metadata.ResumeTarget == "" {
		t.Fatal("OpenCode did not publish a root session")
	}
	waitOpenCodeE2EEvent(t, firstEvents, func(event MonitorEvent) bool {
		return event.Type == MonitorInputState && event.InputState == protocol.InputTaskComplete
	})
	firstFacts := readOpenCodeE2EFacts(t, firstRuntime)
	assertOpenCodeE2EFreshOrdering(t, firstFacts, metadata.ResumeTarget)
	assertOpenCodeE2ENoContent(t, firstRuntime, firstPrompt, "holark-opencode-first-ok")
	closeOpenCodeE2ETerminal(t, manager, firstTerminal)
	stopFirstMonitor()
	waitOpenCodeE2EDone(t, firstMonitorDone)

	secondRuntime := openCodeE2ERuntime(t)
	resume, err := ResumeCommand("opencode", secondRuntime, worktree, "session-e2e", "harness-e2e", metadata.ResumeTarget, "")
	if err != nil {
		t.Fatal(err)
	}
	secondEvents, stopSecondMonitor, secondMonitorDone := startOpenCodeE2EMonitor(secondRuntime, metadata.ResumeTarget, protocol.InputTaskComplete)
	secondTerminal := terminals.TerminalID("terminal-opencode-e2e-resume")
	launchOpenCodeE2ETerminal(t, manager, secondTerminal, resume.Path, resume.Args[1:], resume.Env, resume.Dir)
	waitOpenCodeE2EEvent(t, secondEvents, func(event MonitorEvent) bool {
		return event.Type == MonitorObservability && event.ObservabilityStatus == protocol.ObservabilityHealthy
	})
	reopened := waitOpenCodeE2EEvent(t, secondEvents, func(event MonitorEvent) bool {
		return event.Type == MonitorInputState
	})
	if reopened.InputState != protocol.InputTaskComplete {
		t.Fatalf("reopened input state = %q, want task_complete before another prompt", reopened.InputState)
	}
	// Observer initialization precedes the resumed editor mounting. Match the
	// rendered reply as a whole line so the original prompt cannot satisfy it.
	waitOpenCodeE2EScreenText(t, manager, secondTerminal, "holark-opencode-first-ok", true, 30*time.Second)
	secondReply := "holark-opencode-second-ok-" + rand.Text()
	secondPrompt := "Reply with exactly: " + secondReply
	if err := manager.Input(t.Context(), secondTerminal, []byte(secondPrompt)); err != nil {
		t.Fatal(err)
	}
	// This unique text cannot already be in the conversation. Its visibility
	// confirms typing, though a character grid cannot identify the input field.
	waitOpenCodeE2EScreenText(t, manager, secondTerminal, secondPrompt, false, 5*time.Second)
	// OpenCode enables the Kitty keyboard protocol; submit Enter using CSI-u.
	if err := manager.Input(t.Context(), secondTerminal, []byte("\x1b[13u")); err != nil {
		t.Fatal(err)
	}
	waitOpenCodeE2EEvent(t, secondEvents, func(event MonitorEvent) bool {
		return event.Type == MonitorInputState && event.InputState == protocol.InputNone
	})
	waitOpenCodeE2EEvent(t, secondEvents, func(event MonitorEvent) bool {
		return event.Type == MonitorInputState && event.InputState == protocol.InputTaskComplete
	})
	secondFacts := readOpenCodeE2EFacts(t, secondRuntime)
	assertOpenCodeE2EResumeCycle(t, secondFacts, metadata.ResumeTarget)
	assertOpenCodeE2ENoContent(t, secondRuntime, secondPrompt, secondReply)
	closeOpenCodeE2ETerminal(t, manager, secondTerminal)
	stopSecondMonitor()
	waitOpenCodeE2EDone(t, secondMonitorDone)
}

func assertOpenCodeE2EFreshOrdering(t *testing.T, facts []Fact, root string) {
	t.Helper()
	initialized := -1
	created := -1
	busy := -1
	idle := -1
	for index, fact := range facts {
		switch {
		case fact.Type == "observer_initialized" && initialized < 0:
			initialized = index
		case fact.Type == "session_created" && fact.SessionID == root && fact.ParentID == "" && created < 0:
			created = index
		case fact.Type == "session_status" && fact.SessionID == root && (fact.Status == "busy" || fact.Status == "retry"):
			busy = index
		case fact.Type == "session_status" && fact.SessionID == root && fact.Status == "idle" && busy >= 0:
			idle = index
		}
	}
	if initialized < 0 || created <= initialized || busy <= created || idle <= busy {
		t.Fatalf("normalized fresh ordering invalid: initialized=%d created=%d busy=%d idle=%d facts=%s", initialized, created, busy, idle, openCodeE2EDiagnostics(facts))
	}
}

func assertOpenCodeE2EResumeCycle(t *testing.T, facts []Fact, root string) {
	t.Helper()
	busy := -1
	idle := -1
	for index, fact := range facts {
		if fact.Type != "session_status" || fact.SessionID != root {
			continue
		}
		if fact.Status == "busy" || fact.Status == "retry" {
			busy = index
		}
		if fact.Status == "idle" && busy >= 0 {
			idle = index
		}
	}
	if busy < 0 || idle <= busy {
		t.Fatalf("normalized resume cycle invalid: busy=%d idle=%d facts=%s", busy, idle, openCodeE2EDiagnostics(facts))
	}
}

func readOpenCodeE2EFacts(t *testing.T, runtimeDir string) []Fact {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runtimeDir, SpoolFileName))
	if err != nil {
		t.Fatal(err)
	}
	var facts []Fact
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		fact, known, err := DecodeFact(line)
		if err != nil {
			t.Fatalf("decode normalized OpenCode fact: %v", err)
		}
		if known {
			facts = append(facts, fact)
		}
	}
	return facts
}

func assertOpenCodeE2ENoContent(t *testing.T, runtimeDir string, forbidden ...string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runtimeDir, SpoolFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range forbidden {
		if bytes.Contains(data, []byte(value)) {
			t.Fatalf("normalized spool contains prompt or response content %q", value)
		}
	}
}

func openCodeE2EDiagnostics(facts []Fact) string {
	diagnostics := make([]map[string]string, 0, len(facts))
	for _, fact := range facts {
		diagnostics = append(diagnostics, map[string]string{
			"type": fact.Type, "session_id": fact.SessionID, "parent_id": fact.ParentID,
			"request_id": fact.RequestID, "status": fact.Status, "outcome": fact.Outcome,
		})
	}
	data, _ := json.Marshal(diagnostics)
	return string(data)
}

func openCodeE2ERuntime(t *testing.T) string {
	t.Helper()
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return runtimeDir
}

func startOpenCodeE2EMonitor(runtimeDir, resumeTarget string, inputState protocol.InputState) (<-chan MonitorEvent, context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan MonitorEvent, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		Monitor(ctx, runtimeDir, resumeTarget, inputState, func(event MonitorEvent) { events <- event })
	}()
	return events, cancel, done
}

func waitOpenCodeE2EEvent(t *testing.T, events <-chan MonitorEvent, match func(MonitorEvent) bool) MonitorEvent {
	t.Helper()
	deadline := time.After(3 * time.Minute)
	for {
		select {
		case event := <-events:
			if match(event) {
				return event
			}
		case <-deadline:
			t.Fatal("timed out waiting for normalized OpenCode state")
		}
	}
}

func launchOpenCodeE2ETerminal(t *testing.T, manager *terminalhost.Manager, terminalID terminals.TerminalID, command string, arguments, environment []string, cwd string) {
	t.Helper()
	_, err := manager.Launch(t.Context(), terminals.LaunchSpec{
		TerminalID: terminalID, Command: command, Arguments: arguments, Environment: environment,
		CWD: cwd, Dimensions: terminals.Dimensions{Columns: 120, Rows: 40},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func closeOpenCodeE2ETerminal(t *testing.T, manager *terminalhost.Manager, terminalID terminals.TerminalID) {
	t.Helper()
	if err := manager.CloseTerminal(terminalID, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, err := manager.Completions(t.Context(), []terminals.TerminalID{terminalID}, 0)
		if err == nil {
			return
		}
		if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "still running") {
			return
		}
	}
	t.Fatal("OpenCode terminal did not stop")
}

func waitOpenCodeE2EDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("OpenCode monitor did not stop")
	}
}
