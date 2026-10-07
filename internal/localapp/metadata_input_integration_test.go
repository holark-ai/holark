package localapp

import (
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
	metadatagit "github.com/holark-ai/holark/internal/pullrequestmetadata/gitadapter"
)

func TestMetadataCapturesActualNewAndReusedWorkspaceInput(t *testing.T) {
	path := t.TempDir()
	gitPlumbingCommand(t, path, "init", "-b", "main")
	gitPlumbingCommand(t, path, "config", "user.name", "Metadata Test")
	gitPlumbingCommand(t, path, "config", "user.email", "metadata@invalid")
	gitPlumbingWrite(t, path, "file.txt", "Original\n")
	gitPlumbingCommand(t, path, "add", ".")
	gitPlumbingCommand(t, path, "commit", "-m", "Initial")
	base := gitPlumbingOutput(t, path, "rev-parse", "HEAD")
	gitPlumbingWrite(t, path, "file.txt", "Feature\n")
	gitPlumbingCommand(t, path, "commit", "-am", "Feature")
	head := gitPlumbingOutput(t, path, "rev-parse", "HEAD")
	service, store := terminalTestService(t) // Real SQLite, with no external agent process.
	adapter := localMetadataHolons{holons: &terminalHolonService{Service: service}, changes: metadatagit.Changes{Path: path}}
	started, err := adapter.Start(t.Context(), pullrequestmetadata.StartAgentSessionRequest{PullRequestID: "pr", BaseBranch: "main", BaseCommit: base, HeadCommit: head, Prompt: "Describe"})
	if err != nil {
		t.Fatal(err)
	}
	if started.Input == nil || started.Input.DiffBaseCommit != base || started.Input.HeadCommit != head {
		t.Fatalf("new generation range: %+v", started.Input)
	}
	// A reused worktree can have advanced independently of both its original
	// start commit and the current PR snapshot. Capture its actual HEAD.
	gitPlumbingWrite(t, path, "file.txt", "Workspace amendment\n")
	gitPlumbingCommand(t, path, "commit", "-am", "Amendment")
	workspaceHead := gitPlumbingOutput(t, path, "rev-parse", "HEAD")
	now := time.Now().UTC()
	if err := store.Create(t.Context(), holons.Holon{ID: "reused", Title: "Metadata", Kind: holons.KindPRMetadata, Status: holons.StatusExpired,
		BaseCommit: base, WorkSessionStartCommit: head, WorktreeBranch: "main", WorktreePath: path, PullRequestID: "pr", CreatedAt: now,
		AgentSessions: []holons.AgentSession{{ID: "previous", HolonID: "reused", AgentType: "codex", Status: string(holons.StatusExpired), CreatedAt: now, UpdatedAt: now}},
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := adapter.Resume(t.Context(), pullrequestmetadata.ResumeAgentSessionRequest{PullRequestID: "pr", SessionID: "reused", Prompt: "Improve"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Input == nil || resumed.Input.DiffBaseCommit != base || resumed.Input.HeadCommit != workspaceHead {
		t.Fatalf("reused generation range: %+v", resumed.Input)
	}
}
