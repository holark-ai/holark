package localapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
)

func TestLocalMetadataArtifactIsConsumedFromHolarkPath(t *testing.T) {
	service, store := terminalTestService(t)
	worktree := t.TempDir()
	path := filepath.Join(worktree, pullrequestmetadata.MetadataArtifactPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"title":"Generated","description":"Summary"}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.Create(t.Context(), holons.Holon{
		ID: "metadata", Title: "Metadata", Kind: holons.KindPRMetadata, Status: holons.StatusExpired,
		BaseCommit: "base-1", WorkSessionStartCommit: "head-1", WorktreeBranch: "holark/metadata", WorktreePath: worktree, PullRequestID: "pr-1", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	adapter := localMetadataHolons{holons: &terminalHolonService{Service: service}}
	artifact, err := adapter.ConsumeArtifact(t.Context(), "metadata")
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Path != ".holark/pr-metadata.json" || string(artifact.Data) != string(data) || artifact.OwnerPullRequestID != "pr-1" {
		t.Fatalf("artifact = %+v", artifact)
	}
	if err = adapter.ClearTurnError(t.Context(), "metadata"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acknowledged artifact still exists: %v", err)
	}
	if _, err = adapter.ConsumeArtifact(t.Context(), "metadata"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second consume error = %v", err)
	}
}

func TestLocalMetadataResumeClearsPreviousArtifactBeforeLaunchingTurn(t *testing.T) {
	service, store := terminalTestService(t)
	worktree := t.TempDir()
	path := filepath.Join(worktree, pullrequestmetadata.MetadataArtifactPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"title":"Old","description":"Old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.Create(context.Background(), holons.Holon{
		ID: "metadata-resume", Title: "Metadata", Kind: holons.KindPRMetadata, Status: holons.StatusExpired,
		BaseCommit: "head-1", WorktreeBranch: "holark/metadata-resume", WorktreePath: worktree, PullRequestID: "pr-1", CreatedAt: now,
		AgentSessions: []holons.AgentSession{{ID: "old-agent", HolonID: "metadata-resume", AgentType: "codex", Title: "Agent", Status: string(holons.StatusExpired), CreatedAt: now, UpdatedAt: now}},
	}); err != nil {
		t.Fatal(err)
	}
	adapter := localMetadataHolons{holons: &terminalHolonService{Service: service}}
	resumed, err := adapter.Resume(t.Context(), pullrequestmetadata.ResumeAgentSessionRequest{PullRequestID: "pr-1", SessionID: "metadata-resume", Prompt: "Try again"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ID != "metadata-resume" || resumed.Status != string(holons.StatusQueued) {
		t.Fatalf("resumed = %+v", resumed)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous artifact still exists: %v", err)
	}
}

func TestLocalMetadataTurnErrorPreservesResumeTarget(t *testing.T) {
	service, store := terminalTestService(t)
	now := time.Now().UTC()
	const resumeTarget = "metadata-conversation"
	if err := store.Create(t.Context(), holons.Holon{
		ID: "metadata-error", Title: "Metadata", Kind: holons.KindPRMetadata, Status: holons.StatusRunning,
		BaseCommit: "head-1", WorktreeBranch: "holark/metadata-error", WorktreePath: t.TempDir(), PullRequestID: "pr-1", CreatedAt: now,
		AgentSessions: []holons.AgentSession{{ID: "agent", HolonID: "metadata-error", AgentType: "codex", Status: string(holons.StatusRunning), ResumeTarget: resumeTarget, CreatedAt: now, UpdatedAt: now}},
	}); err != nil {
		t.Fatal(err)
	}
	adapter := localMetadataHolons{holons: &terminalHolonService{Service: service}}
	if err := adapter.SetTurnError(t.Context(), "metadata-error", "Agent crashed"); err != nil {
		t.Fatal(err)
	}
	h, err := service.Get(t.Context(), "metadata-error")
	if err != nil {
		t.Fatal(err)
	}
	agent := h.AgentSession("agent")
	if agent.Status != string(holons.StatusFailed) || agent.ResumeTarget != resumeTarget {
		t.Fatalf("failed metadata agent=%+v", agent)
	}
}
