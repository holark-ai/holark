package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHookIngestionValidatesTokenAndRedactsContent(t *testing.T) {
	runtimeDir := privateRuntimeDir(t)
	if _, err := PrepareRuntime(runtimeDir, "/opt/holark-node", "harness-one"); err != nil {
		t.Fatal(err)
	}
	metadata, err := readRuntimeMetadata(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{
  "hook_event_name": "PreToolUse",
  "session_id": "123e4567-e89b-12d3-a456-426614174000",
  "prompt_id": "prompt-one",
  "transcript_path": "/tmp/claude.jsonl",
  "cwd": "/tmp/worktree",
  "tool_name": "Bash",
  "tool_use_id": "tool-one",
  "prompt": "PROMPT_SECRET",
  "tool_input": {"command": "TOOL_INPUT_SECRET"},
  "tool_response": "TOOL_OUTPUT_SECRET",
  "last_assistant_message": "ASSISTANT_SECRET",
  "message": "NOTIFICATION_SECRET"
}`)
	hookRecords := receiveHooks(t, runtimeDir)
	spec := IngestSpec{RuntimeDir: runtimeDir, LaunchToken: metadata.LaunchToken, HarnessSessionID: "harness-one"}
	if err := IngestHook(bytes.NewReader(payload), spec); err != nil {
		t.Fatal(err)
	}
	spool, err := collectHooks(t, hookRecords, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PROMPT_SECRET", "TOOL_INPUT_SECRET", "TOOL_OUTPUT_SECRET", "ASSISTANT_SECRET", "NOTIFICATION_SECRET"} {
		if bytes.Contains(spool, []byte(secret)) {
			t.Fatalf("spool retained %q: %s", secret, spool)
		}
	}
	var event NormalizedEvent
	if err := json.Unmarshal(bytes.TrimSpace(spool), &event); err != nil {
		t.Fatal(err)
	}
	if event.Event != "PreToolUse" || event.PromptID != "prompt-one" || event.ToolName != "Bash" || event.ToolUseID != "tool-one" || event.HarnessSessionID != "harness-one" {
		t.Fatalf("normalized event = %#v", event)
	}
	badSpec := spec
	badSpec.LaunchToken = strings.Repeat("0", 64)
	if err := IngestHook(bytes.NewReader(payload), badSpec); err == nil {
		t.Fatal("mismatched launch token was accepted")
	}
	if len(hookRecords) != 0 {
		t.Fatal("rejected hook reached receiver")
	}

}

func TestStartupHookReportsEffectiveConfigDirectory(t *testing.T) {
	for _, override := range []bool{true, false} {
		t.Run(fmt.Sprintf("config_override_%t", override), func(t *testing.T) {
			runtimeDir := privateRuntimeDir(t)
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			if _, err := PrepareRuntime(runtimeDir, "/opt/holark-node", "harness-one"); err != nil {
				t.Fatal(err)
			}
			metadata, err := readRuntimeMetadata(runtimeDir)
			if err != nil {
				t.Fatal(err)
			}
			hookRecords := receiveHooks(t, runtimeDir)

			// The hook runs with the environment inherited from Claude after
			// its wrapper changes the profile or resets it to HOME/.claude.
			want := t.TempDir()
			if override {
				t.Setenv("CLAUDE_CONFIG_DIR", want)
			} else {
				t.Setenv("CLAUDE_CONFIG_DIR", "")
				t.Setenv("HOME", want)
				want = filepath.Join(want, ".claude")
			}
			spec := IngestSpec{RuntimeDir: runtimeDir, LaunchToken: metadata.LaunchToken, HarnessSessionID: "harness-one"}
			payload := `{"hook_event_name":"SessionStart","session_id":"123e4567-e89b-12d3-a456-426614174000","config_dir":"/ignored-input"}`
			if err := IngestHook(strings.NewReader(payload), spec); err != nil {
				t.Fatal(err)
			}
			records, err := collectHooks(t, hookRecords, 1)
			if err != nil {
				t.Fatal(err)
			}
			var event NormalizedEvent
			if err := json.Unmarshal(bytes.TrimSpace(records), &event); err != nil {
				t.Fatal(err)
			}
			if event.ConfigDir != want {
				t.Fatalf("startup config directory = %q, want %q", event.ConfigDir, want)
			}
		})
	}
}

func TestHookIngestionAcceptsContentHeavyResolvingEvents(t *testing.T) {
	runtimeDir := privateRuntimeDir(t)
	if _, err := PrepareRuntime(runtimeDir, "/opt/holark-node", "harness-one"); err != nil {
		t.Fatal(err)
	}
	metadata, err := readRuntimeMetadata(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	hookRecords := receiveHooks(t, runtimeDir)
	spec := IngestSpec{RuntimeDir: runtimeDir, LaunchToken: metadata.LaunchToken, HarnessSessionID: "harness-one"}
	largeSecret := "LARGE_HOOK_CONTENT_SECRET_" + strings.Repeat("x", 256*1024)
	fixtures := []struct {
		name    string
		payload string
		check   func(*testing.T, NormalizedEvent)
	}{
		{
			name: "PostToolUse",
			payload: fmt.Sprintf(`{"hook_event_name":"PostToolUse","session_id":"123e4567-e89b-12d3-a456-426614174000","tool_name":"Write","tool_use_id":"tool-large","tool_input":{"content":%q},"tool_response":{"output":%q}}`,
				largeSecret, largeSecret),
			check: func(t *testing.T, event NormalizedEvent) {
				if event.Event != "PostToolUse" || event.ToolName != "Write" || event.ToolUseID != "tool-large" {
					t.Fatalf("normalized PostToolUse = %#v", event)
				}
			},
		},
		{
			name: "Stop",
			payload: fmt.Sprintf(`{"hook_event_name":"Stop","session_id":"123e4567-e89b-12d3-a456-426614174000","last_assistant_message":%q,"background_tasks":[],"session_crons":[]}`,
				largeSecret),
			check: func(t *testing.T, event NormalizedEvent) {
				if event.Event != "Stop" || !event.BackgroundTasksKnown || event.BackgroundTaskCount != 0 || !event.SessionCronsKnown || event.SessionCronCount != 0 {
					t.Fatalf("normalized Stop = %#v", event)
				}
			},
		},
	}
	for _, fixture := range fixtures {
		if len(fixture.payload) <= 128*1024 {
			t.Fatalf("%s fixture is not larger than the former hook limit", fixture.name)
		}
		if err := IngestHook(strings.NewReader(fixture.payload), spec); err != nil {
			t.Fatalf("ingest %s: %v", fixture.name, err)
		}
	}
	spool, err := collectHooks(t, hookRecords, len(fixtures))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(spool, []byte("LARGE_HOOK_CONTENT_SECRET_")) {
		t.Fatal("normalized spool retained oversized hook content")
	}
	lines := bytes.Split(bytes.TrimSpace(spool), []byte{'\n'})
	if len(lines) != len(fixtures) {
		t.Fatalf("record count = %d, want %d", len(lines), len(fixtures))
	}
	for index, line := range lines {
		var event NormalizedEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("decode %s: %v", fixtures[index].name, err)
		}
		fixtures[index].check(t, event)
	}
}

func TestHookIngestionBoundsAndRuntimePermissions(t *testing.T) {
	runtimeDir := privateRuntimeDir(t)
	if _, err := PrepareRuntime(runtimeDir, "/opt/holark-node", "harness-one"); err != nil {
		t.Fatal(err)
	}
	metadata, err := readRuntimeMetadata(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	hookRecords := receiveHooks(t, runtimeDir)
	spec := IngestSpec{RuntimeDir: runtimeDir, LaunchToken: metadata.LaunchToken, HarnessSessionID: "harness-one"}
	oversizedInput := io.MultiReader(
		strings.NewReader(`{"hook_event_name":"PostToolUse","session_id":"123e4567-e89b-12d3-a456-426614174000","tool_response":"`),
		strings.NewReader(strings.Repeat("x", MaxHookInputBytes)),
		strings.NewReader(`"}`),
	)
	if err := IngestHook(oversizedInput, spec); err == nil || !strings.Contains(err.Error(), "exceeds size limit") {
		t.Fatal("oversized hook input was accepted")
	}
	markers, err := lossMarkerPaths(runtimeDir)
	if err != nil || len(markers) != 1 {
		t.Fatalf("loss markers = %v: %v", markers, err)
	}
	loss, err := os.ReadFile(markers[0])
	if err != nil || string(loss) != metadata.LaunchToken {
		t.Fatalf("invalid loss diagnostic: %v", err)
	}
	if len(hookRecords) != 0 {
		t.Fatal("oversized hook reached receiver")
	}
	oversizedPrompt := fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"123e4567-e89b-12d3-a456-426614174000","prompt_id":%q}`, strings.Repeat("p", 257))
	if err := IngestHook(strings.NewReader(oversizedPrompt), spec); err == nil {
		t.Fatal("oversized prompt identity was accepted")
	}
	if err := os.Chmod(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := `{"hook_event_name":"SessionStart","session_id":"123e4567-e89b-12d3-a456-426614174000"}`
	if err := IngestHook(strings.NewReader(payload), spec); err == nil {
		t.Fatal("insecure runtime permissions were accepted")
	}
}

func TestHookIngestionDeliversConcurrentRecords(t *testing.T) {
	runtimeDir := privateRuntimeDir(t)
	if _, err := PrepareRuntime(runtimeDir, "/opt/holark-node", "harness-one"); err != nil {
		t.Fatal(err)
	}
	metadata, err := readRuntimeMetadata(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	hookRecords := receiveHooks(t, runtimeDir)
	spec := IngestSpec{RuntimeDir: runtimeDir, LaunchToken: metadata.LaunchToken, HarnessSessionID: "harness-one"}
	const records = 32
	var wait sync.WaitGroup
	errors := make(chan error, records)
	for index := 0; index < records; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			payload := fmt.Sprintf(`{"hook_event_name":"PreToolUse","session_id":"123e4567-e89b-12d3-a456-426614174000","tool_name":"Bash","tool_use_id":"tool-%d","tool_input":{"secret":"value-%d"}}`, index, index)
			errors <- IngestHook(strings.NewReader(payload), spec)
		}(index)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	spool, err := collectHooks(t, hookRecords, records)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(spool), []byte{'\n'})
	if len(lines) != records {
		t.Fatalf("record count = %d, want %d", len(lines), records)
	}
	seen := make(map[string]bool, records)
	for _, line := range lines {
		var event NormalizedEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("malformed atomic record %q: %v", line, err)
		}
		seen[event.ToolUseID] = true
		if bytes.Contains(line, []byte("value-")) {
			t.Fatalf("record retained tool input: %s", line)
		}
	}
	if len(seen) != records {
		t.Fatalf("unique tool records = %d, want %d", len(seen), records)
	}
}

// Exercise the production socket receiver without pretending to be a CLI.
func receiveHooks(t *testing.T, runtimeDir string) <-chan []byte {
	t.Helper()
	observer, err := PrepareObserver(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	deliveries := make(chan hookDelivery)
	records := make(chan []byte, 64)
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); observer.Close(); <-done })
	go observer.receive(ctx, deliveries)
	go func() {
		defer close(done)
		for delivery := range deliveries {
			data, _ := json.Marshal(delivery.event)
			records <- data
			delivery.ack <- delivery.err == nil
		}
	}()
	return records
}
func collectHooks(t *testing.T, records <-chan []byte, count int) ([]byte, error) {
	t.Helper()
	var result []byte
	for i := 0; i < count; i++ {
		select {
		case data := <-records:
			result = append(result, data...)
			result = append(result, '\n')
		case <-time.After(3 * time.Second):
			t.Fatal("socket delivery timed out")
		}
	}
	return result, nil
}
