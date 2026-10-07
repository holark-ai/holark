package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/protocol"
)

func TestMonitorCompletesTurnsWithBackgroundWork(t *testing.T) {
	for _, counts := range []struct {
		name   string
		fields map[string]any
	}{
		{"absent counts", nil},
		{"background tasks", map[string]any{"background_tasks": []any{map[string]any{"id": "preview"}}}},
		{"scheduled jobs", map[string]any{"background_tasks": []any{}, "session_crons": []any{map[string]any{"id": "job"}}}},
	} {
		t.Run(counts.name, func(t *testing.T) {
			m := newActivityMonitor(t)
			m.hook("UserPromptSubmit", nil)
			m.presence("busy")
			m.sync(protocol.ActivityWorking, protocol.InputNone)
			m.hook("Stop", counts.fields)
			m.sync(protocol.ActivityWorking, protocol.InputNone)
			m.append(stopSummary(nil))
			m.sync(protocol.ActivityCompleted, protocol.InputTaskComplete)
			for _, status := range []string{"idle", "shell", "busy"} {
				m.presence(status)
				m.sync(protocol.ActivityCompleted, protocol.InputTaskComplete, protocol.ActivityWorking)
			}
			for _, event := range []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure", "Stop", "SubagentStop"} {
				m.hook(event, map[string]any{"agent_id": "background-agent", "tool_name": "Bash"})
				m.sync(protocol.ActivityCompleted, protocol.InputTaskComplete, protocol.ActivityWorking)
			}
			m.hook("Notification", map[string]any{"notification_type": "idle_prompt"})
			m.sync(protocol.ActivityCompleted, protocol.InputTaskComplete, protocol.ActivityWorking)
			for _, event := range []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure"} {
				m.hook(event, map[string]any{"tool_name": "Bash"})
				m.sync(protocol.ActivityWorking, protocol.InputNone)
				m.hook("Stop", counts.fields)
				m.append(stopSummary(nil))
				m.sync(protocol.ActivityCompleted, protocol.InputTaskComplete)
			}
		})
	}
}

func TestMonitorCompletedTurnRetainsExplicitAttention(t *testing.T) {
	for _, interaction := range []struct {
		event, tool, close string
		input              protocol.InputState
	}{
		{"PermissionRequest", "Bash", "PermissionDenied", protocol.InputPermissionRequired},
		{"PermissionRequest", "AskUserQuestion", "", protocol.InputUserRequired},
		{"Elicitation", "", "ElicitationResult", protocol.InputUserRequired},
	} {
		t.Run(interaction.event+interaction.tool, func(t *testing.T) {
			m := newActivityMonitor(t)
			m.hook("Stop", nil)
			m.append(stopSummary(nil))
			m.sync(protocol.ActivityCompleted, protocol.InputTaskComplete)
			fields := map[string]any{"tool_name": interaction.tool, "tool_use_id": interaction.tool, "elicitation_id": interaction.event}
			m.hook(interaction.event, fields)
			for _, status := range []string{"waiting", "busy", "shell", "idle"} {
				m.presence(status)
				m.sync(protocol.ActivityNeedsInput, interaction.input, protocol.ActivityCompleted, protocol.ActivityWorking)
			}
			if interaction.close == "" {
				m.actions <- protocol.TerminalAttentionSubmit
				m.wait(func(e MonitorEvent) bool { return e.InputState == protocol.InputTaskComplete })
			} else {
				m.hook(interaction.close, fields)
			}
			m.sync(protocol.ActivityCompleted, protocol.InputTaskComplete)
		})
	}
}

func TestMonitorCancelsBackgroundApprovalAfterCompletedTurn(t *testing.T) {
	for _, action := range []protocol.TerminalAttentionAction{protocol.TerminalAttentionCancel, protocol.TerminalAttentionInterrupt} {
		t.Run(string(action), func(t *testing.T) {
			m := newActivityMonitor(t)
			m.hook("Stop", nil)
			m.append(stopSummary(nil))
			m.sync(protocol.ActivityCompleted, protocol.InputTaskComplete)
			fields := map[string]any{"agent_id": "background-agent", "tool_name": "Bash", "tool_use_id": "approval"}
			m.hook("PermissionRequest", fields)
			m.presence("waiting")
			m.sync(protocol.ActivityNeedsInput, protocol.InputPermissionRequired)
			m.actions <- action
			// Manual denial emits no closing hook. Completion must be restored
			// immediately, while the subagent and its background work stay alive.
			m.wait(func(e MonitorEvent) bool {
				return e.Activity == protocol.ActivityCompleted && e.InputState == protocol.InputTaskComplete
			})
			for _, status := range []string{"idle", "shell", "busy"} {
				m.presence(status)
				m.sync(protocol.ActivityCompleted, protocol.InputTaskComplete, protocol.ActivityNeedsInput, protocol.ActivityWorking)
			}
			fields["notification_type"] = "permission_prompt"
			m.hook("Notification", fields)
			m.sync(protocol.ActivityCompleted, protocol.InputTaskComplete, protocol.ActivityNeedsInput)
		})
	}
}

func TestMonitorRequiresSuccessfulUninterruptedStop(t *testing.T) {
	for _, scenario := range []string{"no main Stop", "blocking hook", "continuation", "additional context", "prevented continuation", "unverified output", "interrupted", "incomplete append", "Stop failure", "monitoring loss"} {
		t.Run(scenario, func(t *testing.T) {
			m := newActivityMonitor(t)
			m.presence("busy")
			m.hook("UserPromptSubmit", nil)
			m.sync(protocol.ActivityWorking, protocol.InputNone)
			if scenario != "no main Stop" {
				m.hook("Stop", nil)
			}
			switch scenario {
			case "blocking hook":
				m.append(stopSummary(map[string]any{"hookErrors": []string{"blocked"}}))
			case "continuation":
				m.append(map[string]any{"attachment": map[string]any{"hookEvent": "Stop", "type": "hook_non_blocking_error"}}, stopSummary(nil))
			case "additional context":
				m.append(stopSummary(map[string]any{"hookAdditionalContext": []string{"continue working"}}))
			case "prevented continuation":
				m.append(stopSummary(map[string]any{"preventedContinuation": true}))
			case "unverified output":
				m.append(stopSummary(map[string]any{"hasOutput": true}))
			case "interrupted":
				// Put the interruption beyond one read budget to ensure a summary
				// in an earlier chunk cannot publish premature completion.
				m.append(stopSummary(nil), map[string]any{"padding": strings.Repeat("x", transcriptReadBudget)}, map[string]any{"interruptedMessageId": "interrupted"})
			case "incomplete append":
				interruption := m.record(map[string]any{"interruptedMessageId": "interrupted"})
				m.appendBytes(append(m.record(stopSummary(nil)), interruption[:len(interruption)-1]...))
				// A hook round trip gives the monitor a refresh opportunity while
				// an interruption after the summary is still incomplete.
				m.hook("Notification", map[string]any{"notification_type": "idle_prompt"})
				m.hook("Notification", map[string]any{"notification_type": "idle_prompt"})
				m.appendBytes([]byte{'\n'})
			case "Stop failure":
				m.hook("StopFailure", nil)
				m.presence("idle")
				m.sync(protocol.ActivityFailed, protocol.InputNone, protocol.ActivityCompleted)
				return
			case "monitoring loss":
				m.append(stopSummary(nil))
				m.sync(protocol.ActivityCompleted, protocol.InputTaskComplete)
				if err := os.Chmod(m.presencePath, 0); err != nil {
					t.Fatal(err)
				}
				m.wait(func(e MonitorEvent) bool {
					return e.ObservabilityStatus == protocol.ObservabilityDegraded && e.Activity == protocol.ActivityUnknown
				})
				m.presence("idle")
				m.sync(protocol.ActivityIdle, protocol.InputNone, protocol.ActivityCompleted)
				return
			default:
				m.append(stopSummary(nil))
			}
			m.presence("idle")
			m.sync(protocol.ActivityIdle, protocol.InputNone, protocol.ActivityCompleted)
		})
	}
}

func TestMonitorPresenceTimestampValidation(t *testing.T) {
	for _, invalid := range []string{"stale status", "future status", "backward status", "wrong PID", "wrong session", "changed start", "stale start", "future start"} {
		t.Run(invalid, func(t *testing.T) {
			// Every monitor starts with the observed 57 ms timestamp inversion.
			m := newActivityMonitor(t)
			m.sync(protocol.ActivityIdle, protocol.InputNone)
			p := m.presenceRecord("idle")
			switch invalid {
			case "stale status":
				p["statusUpdatedAt"] = m.launchedAt.Add(-3 * time.Second).UnixMilli()
			case "future status":
				p["statusUpdatedAt"] = time.Now().Add(time.Minute).UnixMilli()
			case "backward status":
				p["statusUpdatedAt"] = m.launchedAt.Add(-58 * time.Millisecond).UnixMilli()
			case "wrong PID":
				p["pid"] = os.Getpid() + 1
			case "wrong session":
				p["sessionId"] = "other-session"
			case "changed start":
				p["startedAt"] = m.launchedAt.Add(time.Millisecond).UnixMilli()
			case "stale start":
				p["startedAt"] = m.launchedAt.Add(-3 * time.Second).UnixMilli()
			case "future start":
				p["startedAt"] = time.Now().Add(time.Minute).UnixMilli()
			}
			m.writePresence(p)
			m.wait(func(e MonitorEvent) bool {
				return e.ObservabilityStatus == protocol.ObservabilityDegraded && e.Activity == protocol.ActivityUnknown
			})
			m.presence("idle")
			m.sync(protocol.ActivityIdle, protocol.InputNone)
		})
	}
}

func TestMonitorWaitsForUninitializedPresence(t *testing.T) {
	for _, fields := range []map[string]any{{"statusUpdatedAt": 0}, {"status": ""}} {
		m := newActivityMonitor(t, fields)
		m.wait(func(e MonitorEvent) bool { return e.Type == MonitorMetadata })
		// The startup hook is acknowledged while presence is still pending.
		// Initializing it must recover without ever reporting monitoring loss.
		m.hook("Notification", map[string]any{"notification_type": "idle_prompt"})
		m.presence("idle")
		m.sync(protocol.ActivityIdle, protocol.InputNone, protocol.ActivityUnknown)
	}
}

// These tests exercise the real hook socket and file observer with structural
// records. No agent executable is replaced or simulated.
type activityMonitor struct {
	t                            *testing.T
	spec                         IngestSpec
	transcriptPath, presencePath string
	launchedAt                   time.Time
	events                       chan MonitorEvent
	actions                      chan protocol.TerminalAttentionAction
	activity                     protocol.AgentActivity
	input                        protocol.InputState
	barrier                      int64
}

func newActivityMonitor(t *testing.T, initialPresence ...map[string]any) *activityMonitor {
	t.Helper()
	runtimeDir := privateRuntimeDir(t)
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	if _, err := PrepareRuntime(runtimeDir, "/opt/holark-node", "activity-monitor"); err != nil {
		t.Fatal(err)
	}
	metadata, err := readRuntimeMetadata(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := PrepareObserver(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	m := &activityMonitor{t: t, spec: IngestSpec{RuntimeDir: runtimeDir, LaunchToken: metadata.LaunchToken, HarnessSessionID: "activity-monitor"},
		transcriptPath: filepath.Join(t.TempDir(), "transcript.jsonl"), presencePath: filepath.Join(configDir, "sessions", processPresenceName(os.Getpid())),
		launchedAt: time.Now(), events: make(chan MonitorEvent, 256), actions: make(chan protocol.TerminalAttentionAction, 1)}
	if err := os.MkdirAll(filepath.Dir(m.presencePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.transcriptPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	p := m.presenceRecord("idle")
	p["statusUpdatedAt"] = m.launchedAt.Add(-57 * time.Millisecond).UnixMilli()
	for _, fields := range initialPresence {
		for key, value := range fields {
			p[key] = value
		}
	}
	m.writePresence(p)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	worktree := t.TempDir()
	go func() {
		defer close(done)
		MonitorWithOptions(ctx, runtimeDir, worktree, "activity-monitor", MonitorOptions{StartupTimeout: time.Second, Observer: observer, ProcessID: os.Getpid(), LaunchedAt: m.launchedAt, AttentionActions: m.actions}, func(e MonitorEvent) {
			select {
			case m.events <- e:
			case <-ctx.Done():
			}
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	m.hook("SessionStart", map[string]any{"transcript_path": m.transcriptPath})
	if len(initialPresence) == 0 {
		m.sync(protocol.ActivityIdle, protocol.InputNone)
	}
	return m
}

func (m *activityMonitor) hook(event string, fields map[string]any) {
	m.t.Helper()
	p := map[string]any{"hook_event_name": event, "session_id": reducerSessionID}
	for k, v := range fields {
		p[k] = v
	}
	data, err := json.Marshal(p)
	if err != nil {
		m.t.Fatal(err)
	}
	if err := IngestHook(bytes.NewReader(data), m.spec); err != nil {
		m.t.Fatal(err)
	}
}

func (m *activityMonitor) presenceRecord(status string) map[string]any {
	return map[string]any{"pid": os.Getpid(), "sessionId": reducerSessionID, "startedAt": m.launchedAt.UnixMilli(), "statusUpdatedAt": time.Now().UnixMilli(), "status": status}
}

func (m *activityMonitor) presence(status string) { m.writePresence(m.presenceRecord(status)) }

func (m *activityMonitor) writePresence(p map[string]any) {
	m.t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		m.t.Fatal(err)
	}
	// Atomic replacement also verifies that the observer follows new inodes.
	if err := os.WriteFile(m.presencePath+".new", data, 0600); err != nil {
		m.t.Fatal(err)
	}
	if err := os.Rename(m.presencePath+".new", m.presencePath); err != nil {
		m.t.Fatal(err)
	}
}

func stopSummary(fields map[string]any) map[string]any {
	p := map[string]any{"type": "system", "subtype": "stop_hook_summary", "hasOutput": false, "hookErrors": []any{}}
	for k, v := range fields {
		p[k] = v
	}
	return p
}

func (m *activityMonitor) record(p map[string]any) []byte {
	p["sessionId"], p["timestamp"] = reducerSessionID, time.Now().UTC()
	data, err := json.Marshal(p)
	if err != nil {
		m.t.Fatal(err)
	}
	return append(data, '\n')
}

func (m *activityMonitor) append(records ...map[string]any) {
	var data []byte
	for _, p := range records {
		data = append(data, m.record(p)...)
	}
	m.appendBytes(data)
}

func (m *activityMonitor) appendBytes(data []byte) {
	m.t.Helper()
	f, err := os.OpenFile(m.transcriptPath, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		m.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		m.t.Fatal(err)
	}
}

func (m *activityMonitor) wait(match func(MonitorEvent) bool) {
	m.t.Helper()
	waitClaudeMonitorEvent(m.t, m.events, func(e MonitorEvent) bool {
		if e.Activity != "" {
			m.activity = e.Activity
		}
		if e.Type == MonitorInputState {
			m.input = e.InputState
		}
		return match(e)
	})
}

func (m *activityMonitor) sync(activity protocol.AgentActivity, input protocol.InputState, forbidden ...protocol.AgentActivity) {
	m.t.Helper()
	// A refresh already in progress can read the old presence before a test
	// updates it, then consume a newly appended context record. Wait for that
	// refresh to publish before appending a second record: consuming the second
	// requires a new refresh, which reads the current presence first.
	for range 2 {
		m.barrier++
		m.append(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "usage": map[string]any{"input_tokens": m.barrier}}})
		m.wait(func(e MonitorEvent) bool {
			for _, state := range forbidden {
				if e.Activity == state {
					m.t.Fatalf("unexpected %s: %+v", state, e)
				}
			}
			return e.ContextTokens != nil && *e.ContextTokens == m.barrier
		})
	}
	if m.activity != activity || m.input != input {
		m.t.Fatalf("monitor = %s/%s, want %s/%s", m.activity, m.input, activity, input)
	}
}
