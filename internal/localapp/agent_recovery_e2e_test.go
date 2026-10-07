//go:build claude_e2e || opencode_e2e

package localapp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
)

// Run the real Application in another process so SIGKILL skips every shutdown
// hook. The test process then opens the same database and real CLI conversation.
func runAgentCrashRecoveryE2E(t *testing.T, kind protocol.HarnessType, prepare func(*testing.T, string)) (*Application, holons.Holon) {
	t.Helper()
	if os.Getenv("HOLARK_RECOVERY_TEST_CHILD") == "1" {
		runInterruptedApplication(t, kind)
		return nil, holons.Holon{}
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	applicationLockGit(t, root, "init", "-b", "main")
	applicationLockGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	if prepare != nil {
		prepare(t, root)
	}
	home := t.TempDir()
	ready := filepath.Join(t.TempDir(), "ready.json")
	logPath := filepath.Join(t.TempDir(), "child.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = logFile.Close()
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			t.Logf("application child: %s", data)
		}
	})
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^"+t.Name()+"$", "-test.timeout=4m", "-test.v")
	child.Env = append(os.Environ(), "HOLARK_RECOVERY_TEST_CHILD=1", "HOLARK_RECOVERY_TEST_ROOT="+root, "HOLARK_RECOVERY_TEST_HOME="+home, "HOLARK_RECOVERY_TEST_READY="+ready)
	child.Stdout, child.Stderr = logFile, logFile
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = child.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		<-done
		// Also clean owned processes if readiness or reopening fails.
		directories, _ := filepath.Glob(filepath.Join(home, "runtimes", "processes", "*"))
		for _, directory := range directories {
			registry, err := terminalhost.OpenProcessRegistry(directory)
			if err == nil {
				err = registry.Recover()
			}
			if err != nil {
				t.Errorf("cleanup interrupted application: %v", err)
			}
		}
	})
	var before holons.Holon
	deadline := time.Now().Add(150 * time.Second)
	for {
		if data, err := os.ReadFile(ready); err == nil {
			if err := json.Unmarshal(data, &before); err != nil {
				t.Fatal(err)
			}
			break
		}
		select {
		case <-done:
			t.Fatal("application exited before saving a conversation")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("initial real CLI conversation timed out")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-done
	app, err := New(t.Context(), Options{RepositoryPath: root, HomeDirectory: home})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Error(err)
		}
	})
	after := waitRecoveryE2EAgent(t, app, before.ID, func(a holons.AgentSession) bool { return a.Status == "running" && a.ObservabilityStatus == "healthy" })
	assertRecoveryIdentity(t, before, after)
	a := after.AgentSessions[0]
	if a.TerminalID == "" || a.TerminalID == before.AgentSessions[0].TerminalID {
		t.Fatal("recovery did not replace the terminal")
	}
	// Resume requests arriving after automatic restoration must not launch again.
	for i := 0; i < 2; i++ {
		response := httptest.NewRecorder()
		app.Handler.ServeHTTP(response, httptest.NewRequest("POST", "/api/v1/holons/"+before.ID+"/agent-sessions/"+a.ID+"/resume", nil))
		if response.Code < 400 {
			t.Fatalf("already restored agent admitted another resume: %d", response.Code)
		}
	}
	current, err := app.Holons.Get(t.Context(), before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.AgentSessions[0].TerminalID != a.TerminalID {
		t.Fatal("repeat resume replaced the recovered terminal")
	}
	// A syntactically valid conversation ID absent from this isolated CLI home
	// must remain a retryable failure, never silently become a fresh session.
	missing, err := app.Holons.Create(t.Context(), holons.Create{Title: "Missing conversation", BaseBranch: "main", BaseCommit: before.BaseCommit, AgentType: string(kind)})
	if err != nil {
		t.Fatal(err)
	}
	missingTarget := "11111111-1111-4111-8111-111111111111"
	if kind == protocol.HarnessOpenCode {
		missingTarget = "ses_missing_recovery_test"
	}
	missingID := missing.AgentSessions[0].ID
	if _, err := app.Holons.UpdateAgentObservation(t.Context(), missing.ID, missingID, "", missingTarget, filepath.Join(t.TempDir(), "absent-transcript.jsonl"), "", "", "", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Holons.SetAgentSessionStatus(t.Context(), missing.ID, missingID, holons.StatusRecoveryFailed, "Interrupted", missingTarget); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		response := httptest.NewRecorder()
		app.Handler.ServeHTTP(response, httptest.NewRequest("POST", "/api/v1/holons/"+missing.ID+"/agent-sessions/"+missingID+"/resume", nil))
		failed := waitRecoveryE2EAgent(t, app, missing.ID, func(a holons.AgentSession) bool { return a.Status == "recovery_failed" })
		if len(failed.AgentSessions) != 1 || failed.AgentSessions[0].ID != missingID || failed.AgentSessions[0].ResumeTarget != missingTarget || failed.AgentSessions[0].Reason == "" || failed.ArchivedAt != nil {
			t.Fatalf("missing conversation was replaced or hidden: %+v", failed)
		}
	}
	return app, after
}

func runInterruptedApplication(t *testing.T, kind protocol.HarnessType) {
	t.Helper()
	root := os.Getenv("HOLARK_RECOVERY_TEST_ROOT")
	app, err := New(t.Context(), Options{RepositoryPath: root, HomeDirectory: os.Getenv("HOLARK_RECOVERY_TEST_HOME")})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	h, err := app.Holons.Create(t.Context(), holons.Create{Title: "Crash recovery", Prompt: "Reply with exactly RECOVERY_ORIGINAL_READY. Do not use tools.", BaseBranch: "main", BaseCommit: strings.TrimSpace(applicationLockGit(t, root, "rev-parse", "HEAD")), AgentType: string(kind)})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.agents.Launch(t.Context(), h.ID, h.AgentSessions[0].ID, terminals.Dimensions{Columns: 120, Rows: 36}, "", agentsessions.LaunchOptions{}); err != nil {
		t.Fatal(err)
	}
	current := waitRecoveryE2EAgent(t, app, h.ID, func(a holons.AgentSession) bool {
		return a.ResumeTarget != "" && a.InputState == string(protocol.InputTaskComplete) && a.ObservabilityStatus == "healthy"
	})
	data, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	ready := os.Getenv("HOLARK_RECOVERY_TEST_READY")
	if err := os.WriteFile(ready+".tmp", data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(ready+".tmp", ready); err != nil {
		t.Fatal(err)
	}
	<-t.Context().Done() // The parent deliberately kills us without Close.
}

func waitRecoveryE2EAgent(t *testing.T, app *Application, id string, ready func(holons.AgentSession) bool) holons.Holon {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	attached := map[string]bool{}
	deadline := time.Now().Add(120 * time.Second)
	for {
		h, err := app.Holons.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		a := h.AgentSessions[0]
		if a.TerminalID != "" && !attached[a.TerminalID] {
			attached[a.TerminalID] = true
			startNativeTerminalPeer(ctx, app.Manager, terminals.TerminalID(a.TerminalID))
		}
		if ready(a) {
			return h
		}
		if holons.IsTerminal(holons.Status(a.Status)) || time.Now().After(deadline) {
			if restore, err := app.Manager.Attach(terminals.TerminalID(a.TerminalID)); err == nil {
				output := string(restore.Checkpoint.ReplayPayload)
				for _, notification := range restore.Tail {
					output += string(notification.Data)
				}
				if len(output) > 12000 {
					output = output[len(output)-12000:]
				}
				t.Logf("native terminal: %q", output)
				_ = app.Manager.Detach(terminals.TerminalID(a.TerminalID), restore.Attachment)
			}
			t.Fatalf("agent did not become ready: status=%s reason=%s observability=%s (%s) conversation=%s", a.Status, a.Reason, a.ObservabilityStatus, a.ObservabilityMessage, a.ResumeTarget)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
