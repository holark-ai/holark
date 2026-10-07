package localapp

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/holark-ai/holark/internal/agentsettings"
	settingssqlite "github.com/holark-ai/holark/internal/agentsettings/sqliteadapter"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/issues/comments"
	"github.com/holark-ai/holark/internal/issues/holonworkflow"
	issueworkflow "github.com/holark-ai/holark/internal/issues/workflow"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
	"github.com/holark-ai/holark/internal/repository"
)

type modelSelectionCapabilities struct{}

func (modelSelectionCapabilities) Probe(context.Context) []protocol.HarnessCapability {
	return []protocol.HarnessCapability{{Type: protocol.HarnessCodex, Available: true, AutomatedWorkflows: true}, {Type: protocol.HarnessOpenCode, Available: true, AutomatedWorkflows: true}}
}

func TestMetadataFollowUpPreservesModelSelection(t *testing.T) {
	for _, model := range []string{"metadata-model", ""} {
		t.Run("model="+model, func(t *testing.T) {
			ctx := t.Context()
			db, err := database.Open(filepath.Join(t.TempDir(), "preferences.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			settingsStore, err := settingssqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			settings := agentsettings.New(settingsStore, modelSelectionCapabilities{})
			if err := settings.SavePreference(ctx, agentsettings.WorkflowDefault, protocol.HarnessCodex, "general-model"); err != nil {
				t.Fatal(err)
			}
			if err := settings.SavePreference(ctx, agentsettings.WorkflowPullRequestMetadata, protocol.HarnessCodex, model); err != nil {
				t.Fatal(err)
			}
			_, store := terminalTestService(t)
			service := holons.NewServiceWithRepository(store, &workLauncherRepository{path: t.TempDir()})
			runtime := &terminalHolonService{Service: service, harnesses: settings}
			adapter := localMetadataHolons{holons: runtime, harnesses: settings}
			started, err := adapter.Start(ctx, pullrequestmetadata.StartAgentSessionRequest{PullRequestID: "pr", HeadCommit: "head", Prompt: "Describe"})
			if err != nil {
				t.Fatal(err)
			}
			checkSelection := func(wantSessions int) {
				t.Helper()
				h, err := service.Get(ctx, started.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(h.AgentSessions) != wantSessions {
					t.Fatalf("agent sessions = %d, want %d", len(h.AgentSessions), wantSessions)
				}
				for _, agent := range h.AgentSessions {
					if agent.AgentType != "codex" || agent.Model != model {
						t.Fatalf("agent selection = %s/%q, want codex/%q", agent.AgentType, agent.Model, model)
					}
				}
			}
			checkSelection(1)
			for i := 0; i < 2; i++ {
				if _, err := adapter.Resume(ctx, pullrequestmetadata.ResumeAgentSessionRequest{PullRequestID: "pr", SessionID: started.ID, Prompt: "Improve"}); err != nil {
					t.Fatal(err)
				}
				checkSelection(i + 2)
				if err := settings.SavePreference(ctx, agentsettings.WorkflowPullRequestMetadata, protocol.HarnessOpenCode, "changed-model"); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestModelSelectionPersistsAcrossCreationTabsReservationAndFork(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(filepath.Join(t.TempDir(), "preferences.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	settingsStore, err := settingssqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	settings := agentsettings.New(settingsStore, modelSelectionCapabilities{})
	save := func(model string) {
		t.Helper()
		if err := settings.SavePreference(ctx, agentsettings.WorkflowDefault, protocol.HarnessCodex, model); err != nil {
			t.Fatal(err)
		}
	}
	save("selected-model")
	_, store := terminalTestService(t)
	service := holons.NewServiceWithRepository(store, &workLauncherRepository{path: t.TempDir()})
	runtime := &terminalHolonService{Service: service, harnesses: settings}
	created, err := runtime.Create(ctx, holons.Create{Kind: holons.KindNormal, Prompt: "Work", BaseCommit: "head", AgentType: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if created.AgentSessions[0].Model != "selected-model" {
		t.Fatalf("created: %+v", created.AgentSessions)
	}
	withTab, err := runtime.AddAgentSession(ctx, created.ID, "opencode", "Other agent")
	if err != nil {
		t.Fatal(err)
	}
	if withTab.AgentSessions[1].Model != "" {
		t.Fatal("different agent inherited model")
	}
	added, err := runtime.AddAgentSessionOnce(ctx, created.ID, "codex", "Another tab", "request-model")
	if err != nil || added.Model != "selected-model" {
		t.Fatalf("reserved tab: %+v %v", added, err)
	}
	save("changed-model")
	retried, err := runtime.AddAgentSessionOnce(ctx, created.ID, "codex", "Another tab", "request-model")
	if err != nil || retried.Model != "selected-model" {
		t.Fatalf("retried tab: %+v %v", retried, err)
	}
	// A prepared asynchronous launch retains the pair across later Settings edits.
	in := holons.Create{Kind: holons.KindNormal, Prompt: "Async", BaseCommit: "head"}
	if err := runtime.PreflightSelection(ctx, &in); err != nil {
		t.Fatal(err)
	}
	reservation, err := service.ReserveManual(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	save("latest-model")
	prepared, err := service.PrepareReserved(ctx, reservation.ID, in)
	if err != nil || prepared.AgentSessions[0].Model != "changed-model" {
		t.Fatalf("prepared: %+v %v", prepared, err)
	}
	fork, err := service.AddForkedAgent(ctx, created.ID, created.AgentSessions[0].ID)
	if err != nil || fork.Model != "selected-model" {
		t.Fatalf("fork: %+v %v", fork, err)
	}
	commit, id, err := service.AddCommitAgent(ctx, created.ID, created.AgentSessions[0].ID, "Commit", "head", "", "ignored-model")
	if err != nil || commit.AgentSession(id).Model != "selected-model" {
		t.Fatalf("commit: %+v %v", commit.AgentSession(id), err)
	}
}

// These callbacks change real persisted preferences during issue preparation.
type issueSelectionPreparation struct {
	syncComments  func()
	prepareBranch func()
}

func (p issueSelectionPreparation) SyncComments(context.Context, string) (comments.Discussion, error) {
	if p.syncComments != nil {
		p.syncComments()
	}
	return comments.Discussion{}, nil
}
func (issueSelectionPreparation) Snapshot(context.Context, string) (issueworkflow.Snapshot, error) {
	return issueworkflow.Snapshot{IssueID: "issue", Title: "Fix it"}, nil
}
func (p issueSelectionPreparation) PrepareBranch(context.Context, string) (repository.Preparation, error) {
	if p.prepareBranch != nil {
		p.prepareBranch()
	}
	return repository.Preparation{Branch: "main", Commit: "head"}, nil
}
func (issueSelectionPreparation) DefaultBranch() string { return "main" }

func TestIssueStartupPreservesPreflightModelSelection(t *testing.T) {
	for _, model := range []string{"issue-model", ""} {
		for _, stage := range []string{"comment-sync", "branch-preparation"} {
			t.Run(stage+"/model="+model, func(t *testing.T) {
				ctx := t.Context()
				db, err := database.Open(filepath.Join(t.TempDir(), "preferences.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				settingsStore, err := settingssqlite.New(ctx, db)
				if err != nil {
					t.Fatal(err)
				}
				settings := agentsettings.New(settingsStore, modelSelectionCapabilities{})
				if err := settings.SavePreference(ctx, agentsettings.WorkflowIssue, protocol.HarnessCodex, model); err != nil {
					t.Fatal(err)
				}
				preparation := issueSelectionPreparation{}
				changePreference := func(agent protocol.HarnessType) {
					if err := settings.SavePreference(ctx, agentsettings.WorkflowIssue, agent, "changed-model"); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "comment-sync" {
					preparation.syncComments = func() { changePreference(protocol.HarnessOpenCode) }
				} else {
					preparation.prepareBranch = func() { changePreference(protocol.HarnessCodex) }
				}
				_, store := terminalTestService(t)
				service := holons.NewServiceWithRepository(store, &workLauncherRepository{path: t.TempDir()})
				runtime := &terminalHolonService{Service: service, harnesses: settings}
				started, err := holonworkflow.New(preparation, preparation, runtime).Start(ctx, "issue")
				if err != nil {
					t.Fatal(err)
				}
				persisted, err := service.Get(ctx, started.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(persisted.AgentSessions) != 1 {
					t.Fatalf("agent sessions = %d, want 1", len(persisted.AgentSessions))
				}
				agent := persisted.AgentSessions[0]
				if agent.AgentType != "codex" || agent.Model != model {
					t.Fatalf("agent selection = %s/%q, want codex/%q", agent.AgentType, agent.Model, model)
				}
			})
		}
	}
}
