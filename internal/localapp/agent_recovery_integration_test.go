package localapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/holons"
	holonshttp "github.com/holark-ai/holark/internal/holons/httpapi"
)

type recoveryArchiveRepository struct{ holons.Repository }

func (recoveryArchiveRepository) ArchiveWorkspace(context.Context, string, string) error { return nil }
func (recoveryArchiveRepository) ReopenWorkspace(context.Context, string, string) error  { return nil }

func TestAgentRecoveryPreservesIdentityAndRespectsEndedTabs(t *testing.T) {
	service, store := terminalTestService(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	want := recoveryHolon(t)
	want.Status = holons.StatusCancelling // Aggregate status when just one tab is ending.
	want.AgentSessions = nil
	for i, status := range []holons.Status{holons.StatusRunning, holons.StatusRestoring, holons.StatusRecoveryFailed, holons.StatusCancelling, holons.StatusCompleted, holons.StatusCancelled} {
		want.AgentSessions = append(want.AgentSessions, holons.AgentSession{
			ID: string(status), HolonID: want.ID, AgentType: "codex", Status: string(status), TerminalID: "old-" + string(status),
			ResumeTarget: "conversation-" + string(status), RolloutPath: "rollout-" + string(status), TabOrder: i * 2,
			CreatedAt: now, UpdatedAt: now,
		})
	}
	closed := want.AgentSessions[0]
	closed.ID, closed.ClosedAt = "closed", &now
	want.AgentSessions = append(want.AgentSessions, closed)
	want.ManualTerminals = []holons.ManualTerminal{{ID: "shell", HolonID: want.ID, TerminalID: "old-shell", TabOrder: 1, CreatedAt: now, UpdatedAt: now}}
	if err := store.Create(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddManualTerminal(t.Context(), want.ID, want.ManualTerminals[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReorderTabs(t.Context(), want.ID, []holons.TabRef{
		{Type: "agent", ID: "running"}, {Type: "terminal", ID: "shell"},
		{Type: "agent", ID: "restoring"}, {Type: "agent", ID: "recovery_failed"},
		{Type: "agent", ID: "cancelling"}, {Type: "agent", ID: "completed"}, {Type: "agent", ID: "cancelled"},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := service.Get(t.Context(), want.ID)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := clearStaleTerminalBindings(t.Context(), service); err != nil {
			t.Fatal(err)
		}
		after, err := service.Get(t.Context(), want.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertRecoveryIdentity(t, before, after)
		for _, a := range after.AgentSessions {
			if a.ClosedAt != nil {
				if !reflect.DeepEqual(a, closed) {
					t.Fatalf("closed tab changed: %+v", a)
				}
				continue
			}
			expected := a.ID
			if a.ID == "running" || a.ID == "restoring" || a.ID == "recovery_failed" {
				expected = "restoring"
			}
			if a.ID == "cancelling" {
				expected = "cancelled"
			}
			if a.Status != expected {
				t.Fatalf("agent %s: status=%s, want %s", a.ID, a.Status, expected)
			}
			if expected == "restoring" && a.TerminalID != "" {
				t.Fatalf("restoring agent kept stale terminal: %+v", a)
			}
		}
		if after.ManualTerminals[0].TerminalID != "" || after.ManualTerminals[0].TabOrder != 1 {
			t.Fatalf("manual tab changed unexpectedly: %+v", after.ManualTerminals)
		}
	}
}

func TestAgentRecoveryFailuresRemainAvailableAndRetrySameRecord(t *testing.T) {
	for _, failure := range []string{"missing-conversation", "missing-worktree", "unavailable-harness"} {
		t.Run(failure, func(t *testing.T) {
			service, store := terminalTestService(t)
			service = holons.NewServiceWithRepository(store, recoveryArchiveRepository{})
			h := recoveryHolon(t)
			if failure == "missing-conversation" {
				h.AgentSessions[0].ResumeTarget = ""
			}
			if failure == "missing-worktree" {
				h.WorktreePath = filepath.Join(t.TempDir(), "missing")
			}
			if err := store.Create(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			// An empty real registry represents an installation with no available harnesses.
			runtime := &terminalHolonService{Service: service, harnesses: agentsettings.New(nil, harness.NewRegistry(nil))}
			if err := clearStaleTerminalBindings(t.Context(), service); err != nil {
				t.Fatal(err)
			}
			runtime.restoreAgents(t.Context())
			handler := holonshttp.New(runtime)
			read := func() holons.Holon {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/holons/"+h.ID, nil))
				if response.Code != 200 {
					t.Fatalf("read recovery state: %d %s", response.Code, response.Body.String())
				}
				var got holons.Holon
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				return got
			}
			failed := read()
			assertRecoveryIdentity(t, h, failed)
			a := failed.AgentSessions[0]
			if failed.Status != holons.StatusRecoveryFailed || a.Status != "recovery_failed" || a.Reason == "" || a.ClosedAt != nil || a.TerminalID != "" {
				t.Fatalf("failure not retained: %+v", failed)
			}
			// Repair the first prerequisite. Retry must evaluate the next prerequisite,
			// rather than reject recovery_failed or create a replacement agent.
			if failure == "missing-worktree" {
				if err := os.Mkdir(h.WorktreePath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "missing-conversation" {
				if _, err := service.UpdateAgentObservation(t.Context(), h.ID, a.ID, "", "saved-conversation", "", "", "", "", "", nil); err != nil {
					t.Fatal(err)
				}
				h.AgentSessions[0].ResumeTarget = "saved-conversation"
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("POST", "/api/v1/holons/"+h.ID+"/agent-sessions/"+a.ID+"/resume", nil))
			if response.Code < 400 {
				t.Fatalf("retry unexpectedly launched unavailable harness: %d", response.Code)
			}
			retried := read()
			assertRecoveryIdentity(t, h, retried)
			if retried.AgentSessions[0].Status != "recovery_failed" || retried.AgentSessions[0].Reason == "" {
				t.Fatalf("retry lost failure state: %+v", retried)
			}
			if failure != "unavailable-harness" && retried.AgentSessions[0].Reason == a.Reason {
				t.Fatal("retry did not reevaluate the repaired prerequisite")
			}
			if _, err := runtime.End(t.Context(), h.ID); err != nil {
				t.Fatal(err)
			}
			if err := clearStaleTerminalBindings(t.Context(), service); err != nil {
				t.Fatal(err)
			}
			runtime.restoreAgents(t.Context())
			if got := read(); got.ArchivedAt == nil || got.AgentSessions[0].Status != "recovery_failed" {
				t.Fatalf("ended agent restored: %+v", got)
			}
		})
	}
}

func TestAgentRecoveryEarlyExitAndStaleEvents(t *testing.T) {
	service, store := terminalTestService(t)
	h := recoveryHolon(t)
	if err := store.Create(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if err := clearStaleTerminalBindings(t.Context(), service); err != nil {
		t.Fatal(err)
	}
	a := h.AgentSessions[0]
	if _, err := service.UpdateAgentObservation(t.Context(), h.ID, a.ID, "replacement", "", "", "", "", "", "", nil); err != nil {
		t.Fatal(err)
	}
	// Late completion and observation from the previous terminal cannot finish
	// or mark the replacement ready.
	if _, err := service.CompleteAgentSession(t.Context(), h.ID, a.ID, a.TerminalID, 0); err != nil {
		t.Fatal(err)
	}
	got, err := service.UpdateAgentObservation(t.Context(), h.ID, a.ID, "", "other-conversation", "", "", "healthy", "", "", nil, a.TerminalID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentSessions[0].Status != "restoring" || got.AgentSessions[0].ResumeTarget != a.ResumeTarget {
		t.Fatalf("stale event changed recovery: %+v", got)
	}
	runtime := &terminalHolonService{Service: service}
	if _, err := runtime.restoreAgent(t.Context(), h.ID, a.ID); !errors.Is(err, holons.ErrNotResumable) {
		t.Fatalf("bound restoration admitted duplicate launch: %v", err)
	}
	failed, err := service.CompleteAgentSession(t.Context(), h.ID, a.ID, "replacement", 0)
	if err != nil {
		t.Fatal(err)
	}
	if failed.AgentSessions[0].Status != "recovery_failed" || failed.AgentSessions[0].Reason == "" {
		t.Fatalf("early exit not a recovery failure: %+v", failed)
	}
	late, err := service.UpdateAgentObservation(t.Context(), h.ID, a.ID, "", "", "", "", "healthy", "", "", nil, "replacement")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(failed, late) {
		t.Fatal("late readiness overwrote recovery failure")
	}
}

func TestAgentRecoveryHonorsEndAndCloseBeforeLaunch(t *testing.T) {
	for _, action := range []string{"end", "close"} {
		t.Run(action, func(t *testing.T) {
			service, store := terminalTestService(t)
			h := recoveryHolon(t)
			peer := h.AgentSessions[0]
			peer.ID, peer.TabOrder = "peer", 4
			h.AgentSessions = append(h.AgentSessions, peer)
			if err := store.Create(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			if err := clearStaleTerminalBindings(t.Context(), service); err != nil {
				t.Fatal(err)
			}
			runtime := &terminalHolonService{Service: service, harnesses: agentsettings.New(nil, harness.NewRegistry(nil))}
			var err error
			switch action {
			case "end":
				_, err = runtime.End(t.Context(), h.ID)
			case "close":
				_, err = runtime.CloseAgentSession(t.Context(), h.ID, h.AgentSessions[0].ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := clearStaleTerminalBindings(t.Context(), service); err != nil {
				t.Fatal(err)
			}
			runtime.restoreAgents(t.Context())
			got, err := service.Get(t.Context(), h.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range got.AgentSessions {
				if action == "close" && a.ID == "peer" {
					if a.Status != "recovery_failed" {
						t.Fatal("closing one tab prevented its peer from recovering")
					}
					continue
				}
				if a.ClosedAt == nil && a.Status != "cancelled" {
					t.Fatalf("ended tab reopened: %+v", a)
				}
				if _, err := runtime.restoreAgent(t.Context(), h.ID, a.ID); !errors.Is(err, holons.ErrNotResumable) {
					t.Fatalf("closed/ended tab admitted recovery: %v", err)
				}
			}
		})
	}
}

func recoveryHolon(t *testing.T) holons.Holon {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	return holons.Holon{ID: "recovery", Title: "Interrupted", Kind: holons.KindNormal, Status: holons.StatusRunning,
		BaseBranch: "main", BaseCommit: "head", WorktreeBranch: "holark/recovery", WorktreePath: t.TempDir(), CreatedAt: now,
		AgentSessions: []holons.AgentSession{{ID: "agent", HolonID: "recovery", AgentType: "codex", Status: "running", TerminalID: "old-terminal", ResumeTarget: "saved-conversation", RolloutPath: "saved-rollout", TabOrder: 3, CreatedAt: now, UpdatedAt: now}},
	}
}

func assertRecoveryIdentity(t *testing.T, before, after holons.Holon) {
	t.Helper()
	if before.ID != after.ID || before.WorktreePath != after.WorktreePath || before.WorktreeBranch != after.WorktreeBranch || !before.CreatedAt.Equal(after.CreatedAt) || len(before.AgentSessions) != len(after.AgentSessions) {
		t.Fatalf("holon identity changed: before=%+v after=%+v", before, after)
	}
	for _, a := range before.AgentSessions {
		found := false
		for _, b := range after.AgentSessions {
			if a.ID != b.ID {
				continue
			}
			found = true
			if a.ResumeTarget != b.ResumeTarget || a.RolloutPath != b.RolloutPath || a.TabOrder != b.TabOrder || !a.CreatedAt.Equal(b.CreatedAt) {
				t.Fatalf("agent identity changed: before=%+v after=%+v", a, b)
			}
		}
		if !found {
			t.Fatalf("agent %s disappeared", a.ID)
		}
	}
}
