package localapp

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/prompt_templates"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	pinstore "github.com/holark-ai/holark/internal/pullrequesttracking/sqliteadapter"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
)

func TestHolonRebaseReservesAgentBeforeChangingGit(t *testing.T) {
	for _, conflicts := range []bool{false, true} {
		name := "clean rebase"
		if conflicts {
			name = "conflicting rebase"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			root := t.TempDir()
			remote := filepath.Join(root, "remote.git")
			source := filepath.Join(root, "source")
			gitPlumbingCommand(t, root, "init", "--bare", remote)
			gitPlumbingCommand(t, root, "clone", remote, source)
			gitPlumbingCommand(t, source, "switch", "-c", "main")
			gitPlumbingCommand(t, source, "config", "user.name", "Holark Test")
			gitPlumbingCommand(t, source, "config", "user.email", "holark@example.test")
			gitPlumbingWrite(t, source, "shared.txt", "initial\n")
			gitPlumbingCommand(t, source, "add", ".")
			gitPlumbingCommand(t, source, "commit", "-m", "initial")
			base := gitPlumbingOutput(t, source, "rev-parse", "HEAD")
			gitPlumbingCommand(t, source, "switch", "-c", "feature")
			file := "feature.txt"
			if conflicts {
				file = "shared.txt"
			}
			gitPlumbingWrite(t, source, file, "feature\n")
			gitPlumbingCommand(t, source, "add", ".")
			gitPlumbingCommand(t, source, "commit", "-m", "feature")
			head := gitPlumbingOutput(t, source, "rev-parse", "HEAD")
			gitPlumbingCommand(t, source, "switch", "main")
			gitPlumbingWrite(t, source, "shared.txt", "upstream\n")
			gitPlumbingCommand(t, source, "commit", "-am", "upstream")
			target := gitPlumbingOutput(t, source, "rev-parse", "HEAD")
			gitPlumbingCommand(t, source, "push", "origin", "main", "feature")
			gitPlumbingCommand(t, root, "--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/main")

			git, err := gitadapter.OpenWithWorktrees(ctx, source, filepath.Join(root, "worktrees"))
			if err != nil {
				t.Fatal(err)
			}
			_, store := terminalTestService(t)
			service := holons.NewServiceWithRepository(store, holonRepositoryCoordinator{repository.NewService(git)})
			h, err := service.Create(ctx, holons.Create{Title: "Feature", BaseBranch: "main", BaseCommit: base, WorkSessionStartCommit: head, AgentType: "claude-code", Model: "source-model"})
			if err != nil {
				t.Fatal(err)
			}
			readiness, err := service.PublicationReadiness(ctx, h.ID, "main", target)
			if err != nil || !readiness.RebaseAvailable || readiness.TargetCommit != target {
				t.Fatalf("readiness=%+v err=%v", readiness, err)
			}
			sourceID := h.AgentSessions[0].ID
			h, err = service.Synchronize(ctx, h.ID, "", &readiness)
			if err != nil {
				t.Fatal(err)
			}
			definition, _ := prompttemplates.DefinitionByKey(prompttemplates.HolonRebaseKey)
			prompt := prompttemplates.Render(definition.DefaultValue, map[string]string{"source_branch": h.WorktreeBranch, "target_branch": "main", "target_commit": target})
			h, err = service.AddRebaseAgent(ctx, h.ID, "", prompt, h.RebaseAttempt.ID, "codex")
			if err != nil {
				t.Fatal(err)
			}
			attemptID, agentID := h.RebaseAttempt.ID, h.RebaseAttempt.AgentID
			for _, retry := range []struct {
				sourceID, agentType string
			}{
				{agentType: "opencode"},                        // Retry with a different default harness.
				{sourceID: sourceID, agentType: "claude-code"}, // Retry by selecting another harness's conversation.
			} {
				if err = service.ReserveRebaseLaunch(ctx, h.ID, attemptID, agentID); err != nil {
					t.Fatal(err)
				}
				if err = service.DeferRebaseLaunch(ctx, h.ID, attemptID, "CLI unavailable"); err != nil {
					t.Fatal(err)
				}
				retryPrompt := prompt + "\nRetry with " + retry.agentType
				h, err = service.AddRebaseAgent(ctx, h.ID, retry.sourceID, retryPrompt, attemptID, retry.agentType)
				if err != nil {
					t.Fatal(err)
				}
				if agent := h.AgentSession(agentID); agent.AgentType != retry.agentType || agent.Prompt != retryPrompt {
					t.Fatalf("retry returned stale agent: %+v", agent)
				}
				h, err = service.Get(ctx, h.ID)
				if err != nil {
					t.Fatal(err)
				}
				if agent := h.AgentSession(agentID); agent.AgentType != retry.agentType || agent.Prompt != retryPrompt {
					t.Fatalf("retry did not persist agent: %+v", agent)
				}
				if h.RebaseAttempt.ID != attemptID || h.RebaseAttempt.AgentID != agentID || h.RebaseAttempt.SourceAgentID != retry.sourceID {
					t.Fatalf("retry changed reservation or lost source: %+v", h.RebaseAttempt)
				}
			}
			// Restart before launch must preserve the reservation and the original Git state.
			h, err = service.Synchronize(ctx, h.ID, sourceID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = service.ReserveRebaseLaunch(ctx, h.ID, h.RebaseAttempt.ID, h.RebaseAttempt.AgentID); err != nil {
				t.Fatal(err)
			}
			i, err := git.InspectSynchronization(ctx, h.ID, target, nil)
			if err != nil || i.HeadCommit != head || i.Branch != h.WorktreeBranch || i.Rebasing || i.Dirty || i.Incorporated {
				t.Fatalf("workspace changed before agent launch: %+v err=%v", i, err)
			}
			if err = service.ReserveRebaseLaunch(ctx, h.ID, h.RebaseAttempt.ID, h.RebaseAttempt.AgentID); !errors.Is(err, holons.ErrRebaseActive) {
				t.Fatalf("duplicate launch accepted: %v", err)
			}

			// A deferred retry must survive closing its saved source conversation.
			if err = service.DeferRebaseLaunch(ctx, h.ID, attemptID, "CLI unavailable"); err != nil {
				t.Fatal(err)
			}
			if _, err = service.CloseAgentSession(ctx, h.ID, sourceID); err != nil {
				t.Fatal(err)
			}
			native, _ := harness.DefaultRegistry().Driver(protocol.HarnessClaudeCode)
			driver := &forkObservedDriver{Driver: native, ctx: ctx, monitored: make(chan harness.MonitorSpec, 1), events: make(chan []harness.Event), done: make(chan struct{}, 1)}
			launcher := &forkLaunchRecorder{}
			agents, err := agentsessions.New(harness.NewRegistry(map[protocol.HarnessType]harness.Driver{protocol.HarnessClaudeCode: driver}), &localAgentState{holons: service}, launcher, nil, nil, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(agents.Close)
			db, err := database.Open(filepath.Join(root, "pins.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			if _, err := db.ExecContext(ctx, `create table repositories(id text primary key); insert into repositories(id) values('repo')`); err != nil {
				t.Fatal(err)
			}
			catalog, err := prsqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			pins, err := pinstore.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			pr, err := catalog.CreatePullRequest(pullrequestlifecycle.PullRequest{RepositoryID: "repo", Status: pullrequestlifecycle.StatusWIP, LinkedHolonIDs: []string{h.ID}})
			if err != nil {
				t.Fatal(err)
			}
			if err := pins.Pin(ctx, pr.ID); err != nil {
				t.Fatal(err)
			}
			if err := pins.Unpin(ctx, pr.ID); err != nil {
				t.Fatal(err)
			}
			runtime := &terminalHolonService{Service: service, panelPins: pins, pullRequestCatalog: catalog, repositoryID: "repo"}
			coordinator := &rebaseAgentCoordinator{holons: service, agents: agents, harnesses: &harnessPreferencesStub{resolved: protocol.HarnessOpenCode}, pinPullRequests: runtime.pinHolonPullRequests}
			h, err = coordinator.CreateRebaseAgent(ctx, h.ID, "")
			if err != nil {
				t.Fatalf("retry after closing source: %v", err)
			}
			if pinned, err := pins.Pinned(ctx, pr.ID); err != nil || !pinned {
				t.Fatalf("recovered rebase did not pin its linked PR: pinned=%v err=%v", pinned, err)
			}
			if a := h.RebaseAttempt; a.ID != attemptID || a.AgentID != agentID || a.TargetCommit != target || a.StartHeadCommit != head || a.SourceAgentID != "" {
				t.Fatalf("fresh retry changed reservation or retained source: %+v", a)
			}
			agent := h.AgentSession(agentID)
			if agent.AgentType != "claude-code" || agent.Model != "source-model" || agent.Status != "running" || agent.TerminalID == "" || agent.ResumeTarget != "" || len(h.AgentSessions) != 2 {
				t.Fatalf("retry did not retain the reserved agent/model pair: %+v", h.AgentSessions)
			}
			args := launcher.spec.Arguments
			if !slices.Contains(args, "--model") || !slices.Contains(args, "source-model") {
				t.Fatalf("fresh recovery lost model: %q", args)
			}
			if launcher.spec.CWD != h.WorktreePath || !slices.Contains(args, agent.Prompt) || !strings.Contains(agent.Prompt, target) || slices.Contains(args, "--resume") || slices.Contains(args, "--fork-session") {
				t.Fatalf("expected fresh rebase command: %+v", launcher.spec)
			}
		})
	}
}
