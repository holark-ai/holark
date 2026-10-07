package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/protocol"
)

func TestMonitorAcceptsInitialPresenceBeforeStartAndRejectsTimestampRewind(t *testing.T) {
	runtimeDir := privateRuntimeDir(t)
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	if _, err := PrepareRuntime(runtimeDir, "/opt/holark-node", "presence-test"); err != nil {
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
	defer observer.Close()
	const sessionID = "123e4567-e89b-12d3-a456-426614174000"
	launchedAt := time.Now()
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcript, nil, 0600); err != nil {
		t.Fatal(err)
	}
	presenceDir := filepath.Join(configDir, "sessions")
	if err := os.MkdirAll(presenceDir, 0700); err != nil {
		t.Fatal(err)
	}
	writePresence := func(updatedAt time.Time, status string) {
		t.Helper()
		data, err := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": sessionID,
			"startedAt": launchedAt.UnixMilli(), "statusUpdatedAt": updatedAt.UnixMilli(), "status": status})
		if err != nil {
			t.Fatal(err)
		}
		temporary := filepath.Join(presenceDir, "presence.tmp")
		if err := os.WriteFile(temporary, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(temporary, filepath.Join(presenceDir, processPresenceName(os.Getpid()))); err != nil {
			t.Fatal(err)
		}
	}
	// Actual Claude startup records can lag the initial status by tens of milliseconds.
	writePresence(launchedAt.Add(-75*time.Millisecond), "idle")
	ctx, cancel := context.WithCancel(t.Context())
	events := make(chan MonitorEvent, 32)
	done := make(chan struct{})
	worktree := t.TempDir()
	go func() {
		defer close(done)
		MonitorWithOptions(ctx, runtimeDir, worktree, "presence-test", MonitorOptions{
			StartupTimeout: time.Second, Observer: observer, ProcessID: os.Getpid(), LaunchedAt: launchedAt,
		}, func(event MonitorEvent) { events <- event })
	}()
	defer func() { cancel(); <-done }()
	spec := IngestSpec{RuntimeDir: runtimeDir, LaunchToken: metadata.LaunchToken, HarnessSessionID: "presence-test"}
	start := `{"hook_event_name":"SessionStart","session_id":"` + sessionID + `","transcript_path":` + quoteJSON(t, transcript) + `}`
	if err := IngestHook(strings.NewReader(start), spec); err != nil {
		t.Fatal(err)
	}
	waitClaudeMonitorEvent(t, events, func(event MonitorEvent) bool {
		return event.Type == MonitorObservability && event.ObservabilityStatus == protocol.ObservabilityHealthy
	})
	waitClaudeMonitorEvent(t, events, func(event MonitorEvent) bool { return event.Activity == protocol.ActivityIdle })
	writePresence(launchedAt, "busy")
	waitClaudeMonitorEvent(t, events, func(event MonitorEvent) bool { return event.Activity == protocol.ActivityWorking })
	// A record older than the last accepted one must still invalidate observation.
	writePresence(launchedAt.Add(-50*time.Millisecond), "idle")
	waitClaudeMonitorEvent(t, events, func(event MonitorEvent) bool {
		return event.Type == MonitorObservability && event.ObservabilityStatus == protocol.ObservabilityDegraded
	})
	waitClaudeMonitorEvent(t, events, func(event MonitorEvent) bool { return event.Activity == protocol.ActivityUnknown })
}
