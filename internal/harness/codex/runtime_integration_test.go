package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/agentsettings/sqliteadapter"
	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/protocol"
)

func TestRuntimeCommandSwitchingPreservesSessions(t *testing.T) {
	ctx, runtime := newRuntimeIntegrationTest(t)
	a := commandprefix.New("codex", "-c", `model_reasoning_effort="low"`)
	b := commandprefix.New("codex", "-c", `model_reasoning_effort="high"`)
	a1, serverA := prepareRuntimeSession(t, ctx, runtime, a, "a-one")
	clientA := connectRuntimeSession(t, ctx, runtime, a1, "a-one")
	b1, serverB := prepareRuntimeSession(t, ctx, runtime, b, "b-one")
	b2, _ := prepareRuntimeSession(t, ctx, runtime, b, "b-two")
	a2, reusedA := prepareRuntimeSession(t, ctx, runtime, a, "a-two")
	if serverA == serverB || serverA != reusedA {
		t.Fatal("A/B/A must launch a separate B server and reuse the live A server")
	}
	// Attach B's monitors after A becomes current again: observation must route
	// by terminal ownership, not by whichever command was selected most recently.
	clientB1 := connectRuntimeSession(t, ctx, runtime, b1, "b-one")
	clientB2 := connectRuntimeSession(t, ctx, runtime, b2, "b-two")
	clientA2 := connectRuntimeSession(t, ctx, runtime, a2, "a-two")
	assertRuntimeSessionConfig(t, ctx, clientA, "low")
	assertRuntimeSessionConfig(t, ctx, clientB1, "high")
	assertRuntimeSessionConfig(t, ctx, clientB2, "high")
	assertRuntimeSessionConfig(t, ctx, clientA2, "low")

	if err := runtime.Stop(ctx, "b-one"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-clientB1.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("stopping B's session did not disconnect its client")
	}
	assertRuntimeServerRunning(t, serverB)
	assertRuntimeSessionConfig(t, ctx, clientB2, "high")
	if err := runtime.Stop(ctx, "b-two"); err != nil {
		t.Fatal(err)
	}
	assertRuntimeServerStopped(t, serverB)
	assertRuntimeSessionConfig(t, ctx, clientA, "low")
	assertRuntimeSessionConfig(t, ctx, clientA2, "low")
	for _, id := range []string{"a-one", "a-two"} {
		if err := runtime.Stop(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	assertRuntimeServerRunning(t, serverA)
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	assertRuntimeServerStopped(t, serverA)
}

func TestRuntimeFailedReplacementPreservesSessions(t *testing.T) {
	ctx, runtime := newRuntimeIntegrationTest(t)
	a := commandprefix.New("codex", "-c", `model_reasoning_effort="low"`)
	cmd, serverA := prepareRuntimeSession(t, ctx, runtime, a, "original")
	client := connectRuntimeSession(t, ctx, runtime, cmd, "original")
	// The installed Codex passes its version probe but rejects this missing
	// profile when starting the replacement app-server. No CLI is substituted.
	broken := commandprefix.New("codex", "--profile", "missing-runtime-test-profile")
	failed := broken.Command()
	failed.Dir, failed.Env = t.TempDir(), os.Environ()
	session := protocol.HarnessSession{ID: "replacement", SessionID: "replacement", TerminalID: "replacement"}
	if err := runtime.PrepareWithPrefix(ctx, failed, session, broken); err == nil {
		t.Fatal("replacement with a missing profile unexpectedly started")
	} else if !strings.Contains(err.Error(), "Codex app-server exited during startup") {
		t.Fatalf("expected replacement app-server startup failure, got %v", err)
	}
	assertRuntimeServerRunning(t, serverA)
	assertRuntimeSessionConfig(t, ctx, client, "low")
	// Reusing the failed terminal ID also verifies that preparation released its
	// reservation, allowing the user to recover by selecting A again.
	retry, recoveredA := prepareRuntimeSession(t, ctx, runtime, a, "replacement")
	if recoveredA != serverA {
		t.Fatal("failed replacement discarded the original server")
	}
	assertRuntimeSessionConfig(t, ctx, connectRuntimeSession(t, ctx, runtime, retry, "replacement"), "low")
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	assertRuntimeServerStopped(t, serverA)
}

func newRuntimeIntegrationTest(t *testing.T) (context.Context, *Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)
	if probe := Probe(ctx); !probe.Available {
		t.Skip(probe.Reason)
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	runtime := NewRuntime(ManagerOptions{}, nil)
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	return ctx, runtime
}

func prepareRuntimeSession(t *testing.T, ctx context.Context, runtime *Runtime, prefix commandprefix.Prefix, id string) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	cmd := prefix.Command()
	cmd.Dir, cmd.Env = t.TempDir(), os.Environ()
	session := protocol.HarnessSession{ID: id, SessionID: id, TerminalID: id}
	if err := runtime.PrepareWithPrefix(ctx, cmd, session, prefix); err != nil {
		t.Fatal(err)
	}
	return cmd, runtime.owners[id].manager.done
}

// Drive the real app-server through the prepared terminal's bridge. Starting a
// thread exercises observation and cleanup without authentication or inference.
func connectRuntimeSession(t *testing.T, ctx context.Context, runtime *Runtime, cmd *exec.Cmd, id string) *rpcConn {
	t.Helper()
	observations := make(chan Observation, 16)
	monitorCtx, cancel := context.WithCancel(ctx)
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		runtime.Monitor(monitorCtx, id, func(o Observation) {
			if o.Acknowledge != nil {
				o.Acknowledge(nil)
			}
			select {
			case observations <- o:
			case <-monitorCtx.Done():
			}
		})
	}()
	t.Cleanup(func() { cancel(); <-monitorDone })
	remote := slices.Index(cmd.Args, "--remote")
	if remote < 0 || remote+1 == len(cmd.Args) {
		t.Fatal("prepared command has no remote bridge")
	}
	var token string
	for _, entry := range cmd.Env {
		if value, ok := strings.CutPrefix(entry, "HOLARK_CODEX_BRIDGE_TOKEN="); ok {
			token = value
		}
	}
	client, err := connectRPC(ctx, cmd.Args[remote+1], token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.close)
	if err := client.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	var thread threadResponse
	if err := client.call(ctx, "thread/start", map[string]any{}, &thread); err != nil {
		t.Fatal(err)
	}
	identified, healthy := false, false
	for !identified || !healthy {
		select {
		case o := <-observations:
			if o.Failed || o.Status == protocol.ObservabilityDegraded {
				t.Fatalf("session %s observation failed: %s", id, o.Message)
			}
			identified = identified || (o.Identity != "" && o.Identity == thread.Thread.ID)
			healthy = healthy || o.Status == protocol.ObservabilityHealthy
		case <-ctx.Done():
			t.Fatalf("session %s did not become observable: %v", id, ctx.Err())
		}
	}
	return client
}

func assertRuntimeSessionConfig(t *testing.T, ctx context.Context, client *rpcConn, effort string) {
	t.Helper()
	var result struct {
		Config struct {
			Effort string `json:"model_reasoning_effort"`
		} `json:"config"`
	}
	if err := client.call(ctx, "config/read", map[string]any{"includeLayers": false}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Config.Effort != effort {
		t.Fatalf("session's server configuration = %q, want %q", result.Config.Effort, effort)
	}
}

func assertRuntimeServerRunning(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
		t.Fatal("server exited while still in use")
	default:
	}
}

func assertRuntimeServerStopped(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	default:
		t.Fatal("retired server process is still running")
	}
}

func TestRuntimeRetiresServerUsingSavedCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	if probe := Probe(ctx); !probe.Available {
		t.Skip(probe.Reason)
	}
	for _, tt := range []struct {
		name       string
		repository int
	}{
		{"unchanged command stays warm", -1},
		{"command saved locally", 0},
		{"command saved by another repository", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CODEX_HOME", t.TempDir())
			path := filepath.Join(t.TempDir(), "settings.sqlite")
			var commands []*agentsettings.LaunchCommands
			for range 2 {
				db, err := database.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				store, err := sqliteadapter.New(ctx, db)
				if err != nil {
					t.Fatal(err)
				}
				commands = append(commands, agentsettings.NewLaunchCommands(store, nil))
			}
			runtime := NewRuntime(ManagerOptions{}, commands[0])
			t.Cleanup(func() {
				if err := runtime.Close(); err != nil {
					t.Error(err)
				}
			})
			for _, id := range []string{"one", "two"} {
				cmd := exec.Command("codex")
				cmd.Dir, cmd.Env = t.TempDir(), os.Environ()
				session := protocol.HarnessSession{ID: id, SessionID: id, TerminalID: id}
				if err := runtime.Prepare(ctx, cmd, session); err != nil {
					t.Fatal(err)
				}
			}
			serverDone := runtime.owners["one"].manager.done
			if tt.repository >= 0 {
				if err := commands[tt.repository].Save(ctx, agentsettings.Commands{protocol.HarnessCodex: "codex --profile replacement"}); err != nil {
					t.Fatal(err)
				}
			}
			// No new launch or model lookup occurs after saving the command.
			if err := runtime.Stop(ctx, "one"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-serverDone:
				t.Fatal("server exited while another session still owns it")
			default:
			}
			if err := runtime.Stop(ctx, "two"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-serverDone:
				if tt.repository < 0 {
					t.Fatal("current server was not kept warm")
				}
			default:
				if tt.repository >= 0 {
					t.Fatal("outdated server survived its final session")
				}
			}
		})
	}
}
