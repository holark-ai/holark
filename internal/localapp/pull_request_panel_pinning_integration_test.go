package localapp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/holons"
	holonssqlite "github.com/holark-ai/holark/internal/holons/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	pinstore "github.com/holark-ai/holark/internal/pullrequesttracking/sqliteadapter"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
)

func TestSourceHolonActivityRetainsLinkedPullRequest(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(filepath.Join(t.TempDir(), "pinning.sqlite"))
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
	store, err := holonssqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	pins, err := pinstore.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, pr := range []pullrequestlifecycle.PullRequest{
		{ID: "linked", RepositoryID: "repo", Status: pullrequestlifecycle.StatusWIP, LinkedHolonIDs: []string{"source"}},
		{ID: "unrelated", RepositoryID: "repo", Status: pullrequestlifecycle.StatusWIP, LinkedHolonIDs: []string{"other"}},
	} {
		if _, err := catalog.CreatePullRequest(pr); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	h := holons.Holon{ID: "source", Kind: holons.KindNormal, Status: holons.StatusCompleted,
		BaseCommit: "abc", WorktreeBranch: "holark/source", WorktreePath: t.TempDir(), CreatedAt: now,
		AgentSessions: []holons.AgentSession{{ID: "agent", HolonID: "source", AgentType: "codex", Status: string(holons.StatusCompleted), CreatedAt: now, UpdatedAt: now}},
	}
	if err := store.Create(ctx, h); err != nil {
		t.Fatal(err)
	}
	runtime := &terminalHolonService{Service: holons.NewService(store), panelPins: pins, pullRequestCatalog: catalog, repositoryID: "repo"}
	// Exercise the service and real persistence without starting an agent CLI.
	for _, action := range []string{"resume", "add"} {
		t.Run(action, func(t *testing.T) {
			if err := pins.Pin(ctx, "linked"); err != nil {
				t.Fatal(err)
			}
			if err := pins.Unpin(ctx, "linked"); err != nil {
				t.Fatal(err)
			}
			var active holons.Holon
			if action == "resume" {
				active, err = runtime.ResumeAgentSession(ctx, h.ID, "agent", "saved-conversation")
			} else {
				active, err = runtime.AddAgentSession(ctx, h.ID, "codex", "Continue work")
			}
			if err != nil {
				t.Fatal(err)
			}
			if active.PullRequestID != "" {
				t.Fatalf("source Holon has a direct PR reference: %+v", active)
			}
			for _, agent := range active.AgentSessions {
				if _, err := runtime.SetAgentSessionStatus(ctx, h.ID, agent.ID, holons.StatusCompleted, "", ""); err != nil {
					t.Fatal(err)
				}
			}
			if retained, err := pins.List(ctx, "repo"); err != nil || len(retained) != 1 || !retained["linked"] {
				t.Fatalf("retained PRs after activity ended = %v, err = %v", retained, err)
			}
		})
	}
}

func TestAutomaticPullRequestPinsSurviveRequestCancellation(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(filepath.Join(t.TempDir(), "pinning.sqlite"))
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
	service, _ := terminalTestService(t)
	for _, activity := range []string{"creation", "launch", "failed launch"} {
		t.Run(activity, func(t *testing.T) {
			pr, err := catalog.CreatePullRequest(pullrequestlifecycle.PullRequest{
				RepositoryID: "repo", Status: pullrequestlifecycle.StatusWIP,
			})
			if err != nil {
				t.Fatal(err)
			}
			requestCtx, cancel := context.WithCancel(ctx)
			cancel()
			if activity == "creation" {
				creator := pinningPullRequestCreator{creator: successfulPullRequestCreator{pullRequest: pr}, pins: pins}
				if _, err := creator.Create(requestCtx, pullrequestlifecycle.CreatePullRequest{}); err != nil {
					t.Fatal(err)
				}
			} else {
				launcher := &recordingAgentLauncher{}
				if activity == "failed launch" {
					launcher.err = errors.New("launch failed")
				}
				runtime := &terminalHolonService{Service: service, agents: launcher, panelPins: pins}
				// A successful launch must pin before the request-scoped refresh fails.
				_, err := runtime.launchAgent(requestCtx, holons.Holon{ID: "holon", PullRequestID: pr.ID}, "agent", agentsessions.LaunchOptions{})
				wantErr := context.Canceled
				if launcher.err != nil {
					wantErr = launcher.err
				}
				if !errors.Is(err, wantErr) {
					t.Fatalf("launch error = %v, want %v", err, wantErr)
				}
			}
			if pinned, err := pins.Pinned(ctx, pr.ID); err != nil || pinned != (activity != "failed launch") {
				t.Fatalf("pinned = %v, err = %v", pinned, err)
			}
		})
	}
}

func TestRecoveredPullRequestCreationPinsPanel(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		return strings.TrimSpace(applicationLockGit(t, root, args...))
	}
	git("init", "-b", "main")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "Base")
	base := git("rev-parse", "HEAD")
	git("checkout", "-b", "feature")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "Feature")
	head := git("rev-parse", "HEAD")
	remote := filepath.Join(t.TempDir(), "remote.git")
	git("init", "--bare", remote)
	git("push", remote, "main", "feature")
	gitRepository, err := gitadapter.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	for _, saved := range []string{"not saved", "identity unrecorded", "identity recorded"} {
		t.Run(saved, func(t *testing.T) {
			db, err := database.Open(filepath.Join(t.TempDir(), "recovery.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			if _, err := db.ExecContext(ctx, `create table repositories(id text primary key); insert into repositories values('repo')`); err != nil {
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
			operation, _, err := catalog.BeginOperation(ctx, pullrequestlifecycle.Operation{
				RequestID: "create", HolonID: "source", Kind: "create", RequestedStatus: pullrequestlifecycle.StatusWIP,
				Groups: []pullrequestlifecycle.FieldGroup{pullrequestlifecycle.LifecycleGroup, pullrequestlifecycle.TopologyGroup},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := catalog.RecordOperationStep(ctx, operation.RequestID, "", "preparation", pullrequestlifecycle.OperationStep{
				Status: "succeeded", HeadCommit: head, Creation: &pullrequestlifecycle.CreationInputs{
					RepositoryID: "repo", Title: "Recovered PR", BaseBranch: "main", BaseCommit: base, HeadBranch: "feature", DiffBaseCommit: base,
				},
			}); err != nil {
				t.Fatal(err)
			}
			if saved != "not saved" {
				pr := pullrequestlifecycle.PullRequest{
					RepositoryID: "repo", Title: "Recovered PR", Status: pullrequestlifecycle.StatusWIP,
					BaseBranch: "main", BaseCommit: base, HeadBranch: "feature", HeadCommit: head, DiffBaseCommit: base, LinkedHolonIDs: []string{"source"},
				}
				if saved == "identity recorded" {
					_, err = catalog.CreateOperationPullRequest(ctx, operation.RequestID, pr)
				} else {
					_, err = catalog.CreatePullRequest(pr)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := catalog.CompleteOperation(ctx, operation.RequestID, "uncertain", "interrupted creation"); err != nil {
				t.Fatal(err)
			}
			coordinator := pullrequestlifecycle.New(catalog, pullrequestlifecycle.Options{
				Repository: localPullRequestRepository{Service: repository.NewService(gitRepository)},
				Projects: pullrequestlifecycle.ProjectLookupFunc(func(string) (pullrequestlifecycle.Project, bool) {
					return pullrequestlifecycle.Project{ID: "repo", RepositoryURL: remote, DefaultBranch: "main"}, true
				}),
				OnCreationRecovered: func(ctx context.Context, pr pullrequestlifecycle.PullRequest) {
					pinPullRequestToPanel(ctx, pins, nil, pr.ID, "creation recovery")
				},
			})
			if err := coordinator.RecoverOperations(ctx); err != nil {
				t.Fatal(err)
			}
			prs := catalog.ListPullRequests("repo")
			if len(prs) != 1 || prs[0].Status != pullrequestlifecycle.StatusWIP {
				t.Fatalf("recovered PRs = %+v", prs)
			}
			if pinned, err := pins.Pinned(ctx, prs[0].ID); err != nil || !pinned {
				t.Fatalf("recovered PR pinned = %v, err = %v", pinned, err)
			}
			// Once recovery completes, later passes must respect a manual unpin.
			if err := pins.Unpin(ctx, prs[0].ID); err != nil {
				t.Fatal(err)
			}
			if err := coordinator.RecoverOperations(ctx); err != nil {
				t.Fatal(err)
			}
			if pinned, err := pins.Pinned(ctx, prs[0].ID); err != nil || pinned {
				t.Fatalf("completed recovery repinned PR = %v, err = %v", pinned, err)
			}
		})
	}
}
