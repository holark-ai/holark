package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/protocol"
)

func TestMonitorRecoversEveryConcurrentHookLossMarker(t *testing.T) {
	runtimeDir := privateRuntimeDir(t)
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	if _, err := PrepareRuntime(runtimeDir, "/opt/holark-node", "harness-one"); err != nil {
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

	const sessionID = "123e4567-e89b-12d3-a456-426614174000"
	launchedAt := time.Now()
	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	presenceDir := filepath.Join(configDir, "sessions")
	if err := os.MkdirAll(presenceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	presence, err := json.Marshal(map[string]any{
		"pid": os.Getpid(), "sessionId": sessionID, "startedAt": launchedAt.UnixMilli(),
		"statusUpdatedAt": launchedAt.UnixMilli(), "status": "idle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(presenceDir, processPresenceName(os.Getpid())), presence, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan MonitorEvent, 32)
	done := make(chan struct{})
	worktree := t.TempDir()
	go func() {
		defer close(done)
		MonitorWithOptions(ctx, runtimeDir, worktree, "harness-one", MonitorOptions{
			StartupTimeout: time.Second, Observer: observer, ProcessID: os.Getpid(), LaunchedAt: launchedAt,
		}, func(event MonitorEvent) { events <- event })
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	spec := IngestSpec{RuntimeDir: runtimeDir, LaunchToken: metadata.LaunchToken, HarnessSessionID: "harness-one"}
	start := `{"hook_event_name":"SessionStart","session_id":"` + sessionID + `","transcript_path":` + quoteJSON(t, transcriptPath) + `}`
	if err := IngestHook(strings.NewReader(start), spec); err != nil {
		t.Fatal(err)
	}
	waitClaudeMonitorEvent(t, events, func(event MonitorEvent) bool {
		return event.Type == MonitorObservability && event.ObservabilityStatus == protocol.ObservabilityHealthy
	})

	observer.mu.Lock()
	locked := true
	defer func() {
		if locked {
			observer.mu.Unlock()
		}
	}()

	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := IngestHook(strings.NewReader("invalid"), spec); err == nil {
				t.Error("invalid hook did not record a delivery loss")
			}
		}()
	}
	wait.Wait()
	waitClaudeMonitorEvent(t, events, func(event MonitorEvent) bool {
		return event.Type == MonitorObservability && event.ObservabilityStatus == protocol.ObservabilityDegraded
	})
	markers, err := lossMarkerPaths(runtimeDir)
	if err != nil || len(markers) != 2 {
		t.Fatalf("concurrent loss markers = %v: %v", markers, err)
	}

	observer.mu.Unlock()
	locked = false
	waitClaudeMonitorEvent(t, events, func(event MonitorEvent) bool {
		return event.Type == MonitorObservability && event.ObservabilityStatus == protocol.ObservabilityHealthy
	})
	markers, err = lossMarkerPaths(runtimeDir)
	if err != nil || len(markers) != 0 {
		t.Fatalf("loss markers after recovery = %v: %v", markers, err)
	}
}

func processPresenceName(pid int) string {
	return fmt.Sprintf("%d.json", pid)
}

func quoteJSON(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func waitClaudeMonitorEvent(t *testing.T, events <-chan MonitorEvent, match func(MonitorEvent) bool) MonitorEvent {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if match(event) {
				return event
			}
		case <-timer.C:
			t.Fatal("Claude monitor event timed out")
		}
	}
}
