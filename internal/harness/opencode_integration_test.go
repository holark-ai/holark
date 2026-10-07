//go:build integration

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/harness/opencode"
	"github.com/holark-ai/holark/internal/protocol"
)

// Exercise Holark's production observer module, durable spool, driver monitor,
// and reducer. Native launch/resume is covered by make test-opencode; no agent
// CLI is substituted here.
func TestOpenCodeObserverAndDriverIntegration(t *testing.T) {
	driver := opencodeDriver{}
	harnessSession := protocol.HarnessSession{ID: "harness-open", HarnessType: protocol.HarnessOpenCode, InputState: protocol.InputNone}
	runtimeDir := openCodeIntegrationRuntime(t)
	if err := opencode.PrepareRuntime(runtimeDir); err != nil {
		t.Fatal(err)
	}
	events, stopMonitor, monitorDone := startOpenCodeDriverMonitor(driver, runtimeDir, harnessSession)
	defer stopMonitor()
	observe := func(events []map[string]any) { t.Helper(); runOpenCodeObserver(t, runtimeDir, events) }
	event := func(value map[string]any) { t.Helper(); observe([]map[string]any{value}) }
	observe(nil)
	waitOpenCodeDriverEvent(t, events, func(event Event) bool {
		return event.Type == EventObservabilityChanged && event.ObservabilityStatus == protocol.ObservabilityStarting
	})
	waitOpenCodeDriverEvent(t, events, func(event Event) bool {
		return event.Type == EventObservabilityChanged && event.ObservabilityStatus == protocol.ObservabilityHealthy
	})
	event(openCodeRawSessionCreated("ses_root", ""))
	metadata := waitOpenCodeDriverEvent(t, events, func(event Event) bool { return event.Type == EventSessionDiscovered })
	if metadata.ResumeTarget != "ses_root" || metadata.RolloutPath != "" || metadata.HarnessType != protocol.HarnessOpenCode {
		t.Fatalf("metadata = %+v", metadata)
	}
	event(openCodeRawStatus("ses_root", "idle"))
	event(openCodeRawStatus("ses_root", "busy"))
	event(openCodeRawSessionCreated("ses_child", "ses_root"))
	event(openCodeRawSessionCreated("ses_nested", "ses_child"))

	event(openCodeRawInteraction("question.asked", "ses_nested", "question_1"))
	assertOpenCodeDriverInput(t, events, protocol.InputUserRequired)
	observe([]map[string]any{
		openCodeRawInteraction("permission.asked", "ses_root", "permission_1"),
		openCodeRawInteraction("permission.asked", "ses_child", "permission_2"),
	})
	assertOpenCodeDriverInput(t, events, protocol.InputPermissionRequired)
	event(openCodeRawInteraction("permission.replied", "ses_root", "permission_1"))
	event(openCodeRawInteraction("permission.replied", "ses_child", "permission_2"))
	assertOpenCodeDriverInput(t, events, protocol.InputUserRequired)
	event(openCodeRawInteraction("question.replied", "ses_nested", "question_1"))
	assertOpenCodeDriverInput(t, events, protocol.InputNone)

	// Duplicates, an unknown event, and content-bearing irrelevant data are harmless.
	event(openCodeRawInteraction("permission.replied", "ses_child", "permission_2"))
	event(map[string]any{"type": "future.event", "properties": map[string]any{"secret": "fixture-secret-event"}})

	event(openCodeRawBackgroundStart("ses_root", "ses_background"))
	event(openCodeRawStatus("ses_root", "idle"))
	event(openCodeRawInteraction("permission.asked", "ses_background", "background_permission"))
	assertOpenCodeDriverInput(t, events, protocol.InputPermissionRequired)
	event(openCodeRawInteraction("permission.replied", "ses_background", "background_permission"))
	assertOpenCodeDriverInput(t, events, protocol.InputNone)

	// A user/model-authored lookalike cannot settle background work.
	event(openCodeRawSettlement("ses_background", false))
	event(openCodeRawStatus("ses_root", "idle"))
	event(openCodeRawSettlement("ses_background", true))
	event(openCodeRawStatus("ses_background", "idle"))
	event(openCodeRawStatus("ses_root", "busy"))
	event(openCodeRawStatus("ses_root", "idle"))
	assertOpenCodeDriverInput(t, events, protocol.InputTaskComplete)

	spool, err := os.ReadFile(filepath.Join(runtimeDir, opencode.SpoolFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{[]byte("fixture-secret-event"), []byte("task result secret")} {
		if bytes.Contains(spool, secret) {
			t.Fatalf("normalized spool retained secret %q: %s", secret, spool)
		}
	}

	file, err := os.OpenFile(filepath.Join(runtimeDir, opencode.SpoolFileName), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("{malformed-known-record\n")
	closeErr := file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	degraded := waitOpenCodeDriverEvent(t, events, func(event Event) bool {
		return event.Type == EventObservabilityChanged && event.ObservabilityStatus == protocol.ObservabilityDegraded
	})
	if degraded.ObservabilityMessage == "" {
		t.Fatalf("degraded event = %+v", degraded)
	}
	stopMonitor()
	waitOpenCodeIntegrationDone(t, monitorDone)

	resumeRuntime := openCodeIntegrationRuntime(t)
	if err := opencode.PrepareRuntime(resumeRuntime); err != nil {
		t.Fatal(err)
	}
	resumedHarness := harnessSession
	resumedHarness.ResumeTarget = "ses_root"
	resumedHarness.InputState = protocol.InputTaskComplete
	resumeEvents, stopResumeMonitor, resumeDone := startOpenCodeDriverMonitor(driver, resumeRuntime, resumedHarness)
	defer stopResumeMonitor()
	runOpenCodeObserver(t, resumeRuntime, []map[string]any{openCodeRawStatus("ses_root", "busy")})
	waitOpenCodeDriverEvent(t, resumeEvents, func(event Event) bool {
		return event.Type == EventObservabilityChanged && event.ObservabilityStatus == protocol.ObservabilityHealthy
	})
	assertOpenCodeDriverInput(t, resumeEvents, protocol.InputNone)
	runOpenCodeObserver(t, resumeRuntime, []map[string]any{openCodeRawStatus("ses_root", "idle")})
	assertOpenCodeDriverInput(t, resumeEvents, protocol.InputTaskComplete)
	stopResumeMonitor()
	waitOpenCodeIntegrationDone(t, resumeDone)
}

func runOpenCodeObserver(t *testing.T, runtimeDir string, events []map[string]any) {
	t.Helper()
	data, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(opencode.SharedConfigDirectory(runtimeDir), opencode.PluginsDirectoryName, opencode.PluginFileName)
	script := `
const { HolarkObserver } = await import(process.argv[1]);
const observer = await HolarkObserver();
await Promise.all((JSON.parse(process.argv[2]) ?? []).map(event => observer.event({event})));
`
	command := exec.Command("node", "--input-type=module", "-e", script, plugin, string(data))
	command.Env = append(os.Environ(), "HOLARK_OPENCODE_OBSERVER=server", opencode.EventsEnvironment+"="+filepath.Join(runtimeDir, opencode.SpoolFileName))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("observer: %v: %s", err, output)
	}
}

func openCodeRawSessionCreated(id, parent string) map[string]any {
	return map[string]any{"type": "session.created", "properties": map[string]any{"info": map[string]any{"id": id, "parentID": parent}}}
}

func openCodeRawStatus(id, status string) map[string]any {
	return map[string]any{"type": "session.status", "properties": map[string]any{"sessionID": id, "status": map[string]any{"type": status}}}
}

func openCodeRawInteraction(eventType, sessionID, requestID string) map[string]any {
	return map[string]any{"type": eventType, "properties": map[string]any{"sessionID": sessionID, "requestID": requestID}}
}

func openCodeRawBackgroundStart(parentID, childID string) map[string]any {
	return map[string]any{"type": "message.part.updated", "properties": map[string]any{"part": map[string]any{
		"type": "tool", "tool": "task", "sessionID": parentID,
		"state": map[string]any{"metadata": map[string]any{"background": true, "parentSessionId": parentID, "sessionId": childID}},
	}}}
}

func openCodeRawSettlement(sessionID string, synthetic bool) map[string]any {
	return map[string]any{"type": "message.part.updated", "properties": map[string]any{"part": map[string]any{
		"type": "text", "synthetic": synthetic,
		"text": fmt.Sprintf("<task id=\"%s\" state=\"completed\">\n<task_result>\ntask result secret\n</task_result>\n</task>", sessionID),
	}}}
}

func openCodeIntegrationRuntime(t *testing.T) string {
	t.Helper()
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return runtimeDir
}

func startOpenCodeDriverMonitor(driver opencodeDriver, runtimeDir string, harnessSession protocol.HarnessSession) (<-chan Event, context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan Event, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		driver.Monitor(ctx, MonitorSpec{RuntimeDir: runtimeDir, HarnessSession: harnessSession}, func(event Event) { events <- event })
	}()
	return events, cancel, done
}

func waitOpenCodeDriverEvent(t *testing.T, events <-chan Event, match func(Event) bool) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-events:
			if match(event) {
				return event
			}
		case <-deadline:
			t.Fatal("timed out waiting for OpenCode driver event")
		}
	}
}

func assertOpenCodeDriverInput(t *testing.T, events <-chan Event, want protocol.InputState) {
	t.Helper()
	event := waitOpenCodeDriverEvent(t, events, func(event Event) bool { return event.Type == EventInputStateChanged })
	if event.InputState != want {
		t.Fatalf("input event = %+v, want %q", event, want)
	}
}

func waitOpenCodeIntegrationDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OpenCode monitor did not stop")
	}
}
