//go:build claude_e2e

package localapp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/harness/claudecode"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
)

// Real native Claude, production hook binary, socket receiver and file observer.
// Marker scripts independently establish execution; no CLI or activity events
// are synthesized. Only the replacement/access-loss case mutates a presence file.
func TestClaudeCodeE2EActivityObservation(t *testing.T) {
	if os.Getenv("HOLARK_CLAUDE_E2E") != "1" {
		t.Skip("run make test-claude")
	}
	worktree, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	claudeE2EWorkspace(t, worktree)
	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = worktree
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git: %v: %s", err, data)
	}
	manager, err := terminalhost.NewManager(terminalhost.Options{MaximumTerminals: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close() })
	writeActivityFile(t, filepath.Join(worktree, "hold.sh"), "#!/bin/sh\ntouch command-started\nwhile [ ! -f command-release ]; do sleep 0.05; done\n")
	settingsPath := filepath.Join(worktree, ".claude", "settings.local.json")
	var settings map[string]any
	data, err := os.ReadFile(settingsPath)
	if err != nil || json.Unmarshal(data, &settings) != nil {
		t.Fatal("read workspace settings")
	}
	settings["permissions"] = map[string]any{"ask": []string{"Bash(sh ./hold.sh)"}}
	writeCommitForkE2EJSON(t, settingsPath, settings)

	primary := launchActivityClaude(t, manager, worktree, "activity-primary", true)
	peer := launchActivityClaude(t, manager, worktree, "activity-peer", false)
	primary.wait(t, "promptless startup", func(s activitySnapshot) bool { return s.healthy && s.activity == protocol.ActivityIdle })
	peer.wait(t, "peer startup", func(s activitySnapshot) bool { return s.healthy && s.activity == protocol.ActivityIdle })
	peerCheckpoint := peer.historyLength()

	t.Run("approval while command executes", func(t *testing.T) {
		primary.submit(t, "Use Bash to run exactly sh ./hold.sh and wait for it to finish. Do not run it in the background. Then reply DONE.")
		primary.waitActivity(t, protocol.ActivityNeedsInput)
		primary.input(t, "\r")
		primary.waitFile(t, filepath.Join(worktree, "command-started"))
		primary.waitActivity(t, protocol.ActivityWorking)
		primary.assertStable(t, protocol.ActivityWorking, 400*time.Millisecond)
		if _, err := os.Stat(filepath.Join(worktree, "command-release")); !os.IsNotExist(err) {
			t.Fatal("command was released before Working was verified")
		}
		writeActivityFile(t, filepath.Join(worktree, "command-release"), "")
		primary.waitActivity(t, protocol.ActivityCompleted)
	})
	if t.Failed() {
		return
	}
	t.Run("Escape interruption", func(t *testing.T) {
		primary.submit(t, "Without using tools, write a 10000-word essay about mathematics. Begin immediately.")
		primary.waitActivity(t, protocol.ActivityWorking)
		primary.input(t, "\x1b")
		primary.waitActivity(t, protocol.ActivityIdle)
		primary.assertStable(t, protocol.ActivityIdle, 500*time.Millisecond)
	})
	if t.Failed() {
		return
	}
	t.Run("held and blocking Stop", func(t *testing.T) {
		writeActivityFile(t, filepath.Join(primary.runtime, "hold-stop"), "block")
		primary.submit(t, "Reply exactly BEFORE_BLOCK. Do not use tools.")
		primary.waitFile(t, filepath.Join(primary.runtime, "stop-entered"))
		primary.waitActivity(t, protocol.ActivityWorking)
		checkpoint := primary.historyLength()
		primary.assertStable(t, protocol.ActivityWorking, 400*time.Millisecond)
		writeActivityFile(t, filepath.Join(primary.runtime, "stop-release"), "")
		primary.waitFile(t, filepath.Join(primary.runtime, "continuation-entered"))
		primary.waitActivity(t, protocol.ActivityWorking)
		primary.assertStable(t, protocol.ActivityWorking, 400*time.Millisecond)
		primary.assertNoActivitySince(t, checkpoint, protocol.ActivityCompleted)
		writeActivityFile(t, filepath.Join(primary.runtime, "continuation-release"), "")
		primary.waitActivity(t, protocol.ActivityCompleted)
	})
	if t.Failed() {
		return
	}
	t.Run("Escape during held Stop", func(t *testing.T) {
		for _, name := range []string{"stop-entered", "stop-release"} {
			if err := os.Remove(filepath.Join(primary.runtime, name)); err != nil {
				t.Fatal(err)
			}
		}
		writeActivityFile(t, filepath.Join(primary.runtime, "hold-stop"), "block")
		primary.submit(t, "Reply exactly PENDING_STOP. Do not use tools.")
		primary.waitFile(t, filepath.Join(primary.runtime, "stop-entered"))
		primary.waitActivity(t, protocol.ActivityWorking)
		checkpoint := primary.historyLength()
		primary.input(t, "\x1b")
		primary.waitActivity(t, protocol.ActivityIdle)
		primary.assertStable(t, protocol.ActivityIdle, 500*time.Millisecond)
		primary.assertNoActivitySince(t, checkpoint, protocol.ActivityCompleted)
		if err := os.Remove(filepath.Join(primary.runtime, "hold-stop")); err != nil {
			t.Fatal(err)
		}
	})
	if t.Failed() {
		return
	}
	t.Run("rejected prompt", func(t *testing.T) {
		primary.submit(t, "REJECT_THIS_PROMPT")
		primary.waitFile(t, filepath.Join(primary.runtime, "prompt-rejected"))
		primary.waitActivity(t, protocol.ActivityIdle)
		primary.assertStable(t, protocol.ActivityIdle, 500*time.Millisecond)
	})
	if t.Failed() {
		return
	}
	t.Run("socket delivery recovery", func(t *testing.T) {
		var runtime struct {
			SocketPath string `json:"socket_path"`
		}
		data, err := os.ReadFile(filepath.Join(primary.runtime, claudecode.MetadataFileName))
		if err != nil || json.Unmarshal(data, &runtime) != nil || runtime.SocketPath == "" {
			t.Fatal("read Claude socket metadata")
		}
		if err := os.Remove(runtime.SocketPath); err != nil {
			t.Fatal(err)
		}
		checkpoint := primary.historyLength()
		started := time.Now()
		primary.submit(t, "REJECT_SOCKET_OUTAGE_PROMPT")
		primary.waitFile(t, filepath.Join(primary.runtime, "socket-outage-hook"))
		if elapsed := time.Since(started); elapsed > 6*time.Second {
			t.Fatalf("Claude hook failure stalled the prompt for %s", elapsed)
		}
		primary.waitSince(t, checkpoint, "socket delivery loss", func(s activitySnapshot) bool {
			return s.degraded && s.activity == protocol.ActivityUnknown
		})
		primary.wait(t, "socket delivery recovery", func(s activitySnapshot) bool {
			return s.healthy && s.activity == protocol.ActivityIdle
		})
		primary.assertNoActivitySince(t, checkpoint, protocol.ActivityCompleted)
		markers, err := filepath.Glob(filepath.Join(primary.runtime, claudecode.LossFilePrefix+"*"))
		if err != nil || len(markers) != 0 {
			t.Fatalf("hook loss markers remained after recovery: %v: %v", markers, err)
		}
		primary.submit(t, "Reply exactly AFTER_SOCKET_RECOVERY. Do not use tools.")
		primary.waitActivity(t, protocol.ActivityWorking)
		primary.waitActivity(t, protocol.ActivityCompleted)
	})
	if t.Failed() {
		return
	}
	t.Run("presence replacement and access loss are isolated", func(t *testing.T) {
		configDir := os.Getenv("CLAUDE_CONFIG_DIR")
		if configDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				t.Fatal(err)
			}
			configDir = filepath.Join(home, ".claude")
		}
		path := filepath.Join(configDir, "sessions", strconv.Itoa(primary.pid)+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		replacement := path + ".replacement"
		if err := os.WriteFile(replacement, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
		// A later native transition proves the watcher follows the new inode.
		primary.submit(t, "Reply exactly AFTER_REPLACEMENT. Do not use tools.")
		primary.waitActivity(t, protocol.ActivityWorking)
		primary.waitActivity(t, protocol.ActivityCompleted)
		if err := os.Chmod(path, 0000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0600) })
		primary.wait(t, "presence access loss", func(s activitySnapshot) bool { return s.degraded && s.activity == protocol.ActivityUnknown })
		peer.assertStable(t, protocol.ActivityIdle, 400*time.Millisecond)
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
		// Lost completion evidence must not be resurrected from an idle snapshot.
		primary.wait(t, "presence access recovery", func(s activitySnapshot) bool { return s.healthy && s.activity == protocol.ActivityIdle })
		primary.submit(t, "Reply exactly RECOVERED. Do not use tools.")
		primary.waitActivity(t, protocol.ActivityWorking)
		primary.waitActivity(t, protocol.ActivityCompleted)
	})
	if t.Failed() {
		return
	}
	t.Run("background process survives completed turns", func(t *testing.T) {
		// The marker and signal check prove the real shell process outlives
		// both responses. Its release file cleans up only this test's work.
		script := filepath.Join(worktree, "background.sh")
		release := filepath.Join(worktree, "background-release")
		writeActivityFile(t, script, "#!/bin/sh\necho $$ > background-pid\nwhile [ ! -f background-release ]; do sleep 0.05; done\n")
		t.Cleanup(func() { writeActivityFile(t, release, "") })
		primary.submit(t, "Use Bash to run exactly sh ./background.sh with run_in_background=true. Leave it running and reply BACKGROUND_STARTED. Do not wait for or stop it.")
		primary.waitFile(t, filepath.Join(worktree, "background-pid"))
		primary.waitActivity(t, protocol.ActivityCompleted)
		primary.assertStable(t, protocol.ActivityCompleted, 500*time.Millisecond)
		data, err := os.ReadFile(filepath.Join(worktree, "background-pid"))
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 0 {
			t.Fatalf("invalid background PID: %q", data)
		}
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("background process exited before completion: %v", err)
		}
		checkpoint := primary.historyLength()
		primary.submit(t, "Reply exactly SECOND_BACKGROUND_TURN. Do not use tools or stop the background process.")
		primary.waitSince(t, checkpoint, "new turn while background process runs", func(s activitySnapshot) bool {
			return s.healthy && s.activity == protocol.ActivityWorking && s.inputState == protocol.InputNone
		})
		primary.waitActivity(t, protocol.ActivityCompleted)
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("background process exited during the next turn: %v", err)
		}
	})
	if t.Failed() {
		return
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	for _, state := range peer.history[peerCheckpoint:] {
		if state.activity != protocol.ActivityIdle || state.degraded {
			t.Fatalf("peer changed with primary observation: %+v", state)
		}
	}
}

type activitySnapshot struct {
	activity          protocol.AgentActivity
	healthy, degraded bool
	resumeTarget      string
	inputState        protocol.InputState
	contextTokens     int64
}
type activityClaude struct {
	runtime string
	pid     int
	id      terminals.TerminalID
	manager *terminalhost.Manager
	mu      sync.Mutex
	state   activitySnapshot
	history []activitySnapshot
	wake    chan struct{}
}

func launchActivityClaude(t *testing.T, manager *terminalhost.Manager, worktree, id string, controls bool) *activityClaude {
	t.Helper()
	runtimeDir := t.TempDir()
	command, err := claudecode.Command("claude", os.Getenv("CLAUDE_E2E_HOOK_EXECUTABLE"), runtimeDir, worktree, "activity-holon", id, "")
	if err != nil {
		t.Fatal(err)
	}
	if controls {
		script := filepath.Join(runtimeDir, "control.py")
		writeActivityFile(t, script, claudeActivityControl)
		path := filepath.Join(runtimeDir, claudecode.SettingsFileName)
		var settings map[string]any
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &settings) != nil {
			t.Fatal("read hook settings")
		}
		hooks := settings["hooks"].(map[string]any)
		for _, event := range []string{"Stop", "UserPromptSubmit"} {
			matchers := hooks[event].([]any)
			handlers := matchers[0].(map[string]any)
			handlers["hooks"] = append(handlers["hooks"].([]any), map[string]any{"type": "command", "command": "python3 '" + script + "' '" + runtimeDir + "'", "timeout": 120})
		}
		writeCommitForkE2EJSON(t, path, settings)
	}
	return launchObservedActivityClaude(t, manager, worktree, id, runtimeDir, command)
}

func launchObservedActivityClaude(t *testing.T, manager *terminalhost.Manager, worktree, id, runtimeDir string, command *exec.Cmd) *activityClaude {
	t.Helper()
	observer, err := claudecode.PrepareObserver(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { observer.Close() })
	launchedAt := time.Now()
	pid, err := manager.Launch(t.Context(), terminals.LaunchSpec{TerminalID: terminals.TerminalID(id), Kind: terminals.LaunchCommand, Command: command.Path, Arguments: command.Args[1:], Environment: command.Env, CWD: worktree, Dimensions: terminals.Dimensions{Columns: 120, Rows: 36}})
	if err != nil {
		t.Fatal(err)
	}
	c := &activityClaude{runtime: runtimeDir, pid: pid, id: terminals.TerminalID(id), manager: manager, wake: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		observer.Close()
		<-done
		if t.Failed() {
			if restore, err := manager.Attach(c.id); err == nil {
				output := string(restore.Checkpoint.ReplayPayload)
				for _, item := range restore.Tail {
					output += string(item.Data)
				}
				if len(output) > 8000 {
					output = output[len(output)-8000:]
				}
				t.Logf("native terminal %s: %q", c.id, output)
				_ = manager.Detach(c.id, restore.Attachment)
			}
		}
	})
	go func() {
		defer close(done)
		claudecode.MonitorWithOptions(ctx, runtimeDir, worktree, id, claudecode.MonitorOptions{Observer: observer, ProcessID: pid, LaunchedAt: launchedAt}, func(event claudecode.MonitorEvent) {
			c.mu.Lock()
			if event.Type == claudecode.MonitorMetadata {
				c.state.resumeTarget = event.ResumeTarget
			}
			if event.Type == claudecode.MonitorInputState {
				c.state.inputState = event.InputState
			}
			if event.ContextTokens != nil {
				c.state.contextTokens = *event.ContextTokens
			}
			if event.Activity != "" {
				c.state.activity = event.Activity
			}
			if event.Type == claudecode.MonitorObservability {
				c.state.healthy = event.ObservabilityStatus == protocol.ObservabilityHealthy
				c.state.degraded = event.ObservabilityStatus == protocol.ObservabilityDegraded
				if c.state.degraded {
					t.Logf("%s observation: %s", id, event.ObservabilityMessage)
				}
			}
			c.history = append(c.history, c.state)
			c.mu.Unlock()
			select {
			case c.wake <- struct{}{}:
			default:
			}
		})
	}()
	return c
}

func (c *activityClaude) input(t *testing.T, text string) {
	t.Helper()
	if err := c.manager.Input(t.Context(), c.id, []byte(text)); err != nil {
		t.Fatal(err)
	}
}
func (c *activityClaude) submit(t *testing.T, text string) {
	t.Helper()
	c.input(t, "\x1b[200~"+text+"\x1b[201~")
	time.Sleep(250 * time.Millisecond)
	c.input(t, "\r")
}
func (c *activityClaude) snapshot() activitySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}
func (c *activityClaude) historyLength() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.history) }
func (c *activityClaude) wait(t *testing.T, label string, match func(activitySnapshot) bool) {
	t.Helper()
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	for {
		if match(c.snapshot()) {
			return
		}
		select {
		case <-c.wake:
		case <-deadline.C:
			t.Fatalf("%s: state=%+v", label, c.snapshot())
		}
	}
}
func (c *activityClaude) waitSince(t *testing.T, checkpoint int, label string, match func(activitySnapshot) bool) {
	t.Helper()
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		for _, state := range c.history[checkpoint:] {
			if match(state) {
				c.mu.Unlock()
				return
			}
		}
		c.mu.Unlock()
		select {
		case <-c.wake:
		case <-deadline.C:
			t.Fatalf("%s was not observed; state=%+v", label, c.snapshot())
		}
	}
}
func (c *activityClaude) waitActivity(t *testing.T, want protocol.AgentActivity) {
	t.Helper()
	c.wait(t, "waiting for "+string(want), func(s activitySnapshot) bool { return s.healthy && s.activity == want })
	t.Logf("%s reached %s", c.id, want)
}
func (c *activityClaude) waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("marker %s was not created; state=%+v", filepath.Base(path), c.snapshot())
}
func (c *activityClaude) assertStable(t *testing.T, activity protocol.AgentActivity, duration time.Duration) {
	t.Helper()
	checkpoint := c.historyLength()
	time.Sleep(duration)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, state := range append([]activitySnapshot{c.state}, c.history[checkpoint:]...) {
		if state.activity != activity || !state.healthy {
			t.Fatalf("expected stable %s: %+v", activity, state)
		}
	}
}
func (c *activityClaude) assertNoActivitySince(t *testing.T, checkpoint int, activity protocol.AgentActivity) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, state := range c.history[checkpoint:] {
		if state.activity == activity {
			t.Fatalf("premature %s", activity)
		}
	}
}
func writeActivityFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

// These are actual user hooks run by Claude. They control only the diagnostic
// scenario, independently of Holark's hook and observer.
const claudeActivityControl = `import json, pathlib, sys, time
root = pathlib.Path(sys.argv[1])
event = json.load(sys.stdin)
if event['hook_event_name'] == 'UserPromptSubmit':
    if event.get('prompt') == 'REJECT_THIS_PROMPT':
        (root/'prompt-rejected').touch()
        print('Rejected for the observation test.', file=sys.stderr)
        sys.exit(2)
    elif event.get('prompt') == 'REJECT_SOCKET_OUTAGE_PROMPT':
        (root/'socket-outage-hook').touch()
        print('Rejected for the socket recovery test.', file=sys.stderr)
        sys.exit(2)
elif event['hook_event_name'] == 'Stop':
    if (root/'hold-stop').exists():
        (root/'stop-entered').touch()
        while not (root/'stop-release').exists(): time.sleep(.025)
        (root/'hold-stop').unlink()
        (root/'hold-continuation').touch()
        print(json.dumps({'decision':'block','reason':'Reply exactly AFTER_BLOCK without tools.'}))
    elif (root/'hold-continuation').exists():
        (root/'continuation-entered').touch()
        while not (root/'continuation-release').exists(): time.sleep(.025)
        (root/'hold-continuation').unlink()
`
