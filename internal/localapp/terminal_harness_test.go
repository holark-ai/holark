package localapp

import (
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/protocol"
)

func TestManualCreateResolvesDefaultBeforeDurableCreation(t *testing.T) {
	service, _ := terminalTestService(t)
	preferences := &harnessPreferencesStub{resolved: protocol.HarnessOpenCode}
	terminal := &terminalHolonService{Service: service, harnesses: preferences}
	h, err := terminal.Create(t.Context(), holons.Create{Prompt: "Work", Kind: holons.KindNormal, BaseCommit: "head"})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.AgentSessions) != 1 || h.AgentSessions[0].AgentType != "opencode" {
		t.Fatalf("holon = %+v", h)
	}
}

func TestResumeUsesStoredHarnessAfterDefaultChanges(t *testing.T) {
	service, store := terminalTestService(t)
	now := time.Now().UTC()
	if err := store.Create(t.Context(), holons.Holon{
		ID: "resume-harness", Title: "Resume", Kind: holons.KindNormal, Status: holons.StatusCancelled,
		BaseBranch: "main", BaseCommit: "head", WorktreeBranch: "holark/resume", WorktreePath: t.TempDir(), CreatedAt: now, FinishedAt: &now,
		AgentSessions: []holons.AgentSession{{Model: "saved-model", ID: "agent", HolonID: "resume-harness", AgentType: "claude-code", Status: string(holons.StatusCancelled), CreatedAt: now, UpdatedAt: now, FinishedAt: &now}},
	}); err != nil {
		t.Fatal(err)
	}
	preferences := &harnessPreferencesStub{resolved: protocol.HarnessOpenCode}
	terminal := &terminalHolonService{Service: service, harnesses: preferences}
	if _, err := terminal.Resume(t.Context(), "resume-harness"); err != nil {
		t.Fatal(err)
	}
	if len(preferences.validated) != 1 || preferences.validated[0] != protocol.HarnessClaudeCode {
		t.Fatalf("validated = %v", preferences.validated)
	}
	h, err := service.Get(t.Context(), "resume-harness")
	if err != nil || h.AgentSessions[0].AgentType != "claude-code" || h.AgentSessions[0].Model != "saved-model" {
		t.Fatalf("holon = %+v, %v", h, err)
	}
}
