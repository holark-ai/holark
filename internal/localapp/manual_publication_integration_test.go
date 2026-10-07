package localapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	githubprovider "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/holons"
	holonsqlite "github.com/holark-ai/holark/internal/holons/sqliteadapter"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	worksqlite "github.com/holark-ai/holark/internal/pullrequestwork/sqliteadapter"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
)

type observedManualSync struct {
	*pr.Coordinator
	calls int
	after func()
}

func (s *observedManualSync) PreparePublication(ctx context.Context, id string) error {
	s.calls++
	if err := s.Coordinator.PreparePublication(ctx, id); err != nil {
		return err
	}
	if s.after != nil {
		s.after()
	}
	return nil
}

type observedPublicationGate struct {
	pr.PublicationGate
	after func()
}

func (g observedPublicationGate) PrepareManualPublication(ctx context.Context, id string) (holons.PublicationReadiness, error) {
	r, err := g.PublicationGate.PrepareManualPublication(ctx, id)
	if err == nil && g.after != nil {
		g.after()
	}
	return r, err
}

func TestManualPublicationPreparesOnceAndPinsTarget(t *testing.T) {
	for _, entry := range []string{"normal", "issue", "continue terminal", "continue direct", "github combined"} {
		t.Run(entry, func(t *testing.T) {
			ctx := t.Context()
			root := t.TempDir()
			gitPlumbingCommand(t, root, "init", "-b", "main")
			gitPlumbingCommand(t, root, "config", "user.name", "Publication Test")
			gitPlumbingCommand(t, root, "config", "user.email", "publication@invalid")
			gitPlumbingCommand(t, root, "commit", "--allow-empty", "-m", "base")
			base := gitPlumbingOutput(t, root, "rev-parse", "HEAD")
			gitPlumbingCommand(t, root, "switch", "-c", "feature")
			gitPlumbingCommand(t, root, "commit", "--allow-empty", "-m", "feature")
			head := gitPlumbingOutput(t, root, "rev-parse", "HEAD")
			remote := filepath.Join(t.TempDir(), "remote.git")
			gitPlumbingCommand(t, root, "clone", "--bare", root, remote)
			gitPlumbingCommand(t, root, "remote", "add", "origin", remote)
			adapter, err := gitadapter.OpenWithWorktrees(ctx, root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			repositories := repository.NewService(adapter)
			if _, err := repositories.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			db, err := database.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec("create table repositories(id text primary key); insert into repositories(id) values(?)", repositories.Descriptor().ID); err != nil {
				t.Fatal(err)
			}
			catalog, err := prsqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			hs, err := holonsqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			ws, err := worksqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			input := pr.PullRequest{ID: "pr", RepositoryID: repositories.Descriptor().ID, Title: "Manual push", Status: pr.StatusWIP, BaseRef: repository.PublishedBranchIdentity(remote, "main"), HeadRef: repository.PublishedBranchIdentity(remote, "feature"), BaseBranch: "main", BaseCommit: base, DiffBaseCommit: base, HeadBranch: "feature", HeadCommit: head}
			if entry == "github combined" {
				input.Status, input.SyncProvider, input.SyncExternalID = pr.StatusOpen, pr.SyncProviderGitHub, "github:owner/repo#1"
				input.SyncData, _ = json.Marshal(map[string]any{"github": map[string]string{"base_repository_url": remote, "head_repository_url": remote}})
			}
			p, err := catalog.CreatePullRequest(input)
			if err != nil {
				t.Fatal(err)
			}
			scheduler := pr.NewRefreshCoordinator()
			defer scheduler.Close()
			observations := &previewGitPlumbingObservations{gitPlumbingObservations: gitPlumbingObservations{repositories: repositories, catalog: catalog}}
			syncer := &observedManualSync{Coordinator: pr.New(catalog, pr.Options{Refresh: scheduler, Repository: localPullRequestRepository{Service: repositories}, GitHubTransport: observations, GitHubCodec: githubprovider.New(nil), Projects: pr.ProjectLookupFunc(func(id string) (pr.Project, bool) {
				return pr.Project{ID: id, RepositoryURL: remote, DefaultBranch: "main", GitHubBacked: entry == "github combined"}, true
			})})}
			if err := syncer.SyncPullRequest(ctx, p.ID); err != nil {
				t.Fatal(err)
			}
			hsrv := holons.NewServiceWithRepository(hs, holonRepositoryCoordinator{repositories: repositories})
			committer := prsqlite.NewHolonPublicationCommitter(catalog, hs)
			readers := &rebaseAgentCoordinator{holons: hsrv, catalog: catalog, sync: syncer, repositoryID: p.RepositoryID, reservations: committer}
			hsrv.SetPublicationTargets(readers)
			kind := holons.KindNormal
			if entry == "issue" {
				kind = holons.KindIssue
			}
			if entry == "continue terminal" || entry == "continue direct" {
				kind = holons.KindPullWorker
			}
			h, err := hsrv.Create(ctx, holons.Create{Title: "Manual", Kind: kind, BaseBranch: "main", BaseCommit: base, WorkSessionStartCommit: head, PullRequestID: p.ID})
			if err != nil {
				t.Fatal(err)
			}
			runtime := &terminalHolonService{Service: hsrv, rebaseAgents: readers, actions: catalog}
			runtime.publication = pr.NewHolonPublisher(p.RepositoryID, catalog, hsrv, committer, readers)
			workers := pullrequestwork.New(ws, localWorkCatalog{store: catalog}, nil, nil)
			workers.SetPublisher(localWorkPublisher{sync: syncer, holons: runtime, actions: catalog})
			workers.SetContinuePublicationCommitter(worksqlite.NewContinuePublicationCommitter(ws, catalog))
			runtime.work = workers
			if kind == holons.KindPullWorker {
				if err := ws.Create(ctx, pullrequestwork.Work{ID: "continue", SessionID: h.ID, PullRequestID: p.ID, Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeContinue, Status: pullrequestwork.StatusRunning, HeadBranch: p.HeadBranch, HeadCommit: head, BaseHeadCommit: head, CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			// Count completion notifications separately; they must remain asynchronous
			// in the application and must not become another awaited preparation.
			completions := 0
			catalog.SetPublicationCompletionHook(func(string) { completions++ })
			publish := func(id, explicit string) error {
				ctx := pr.WithRequestID(ctx, id)
				if entry == "continue direct" {
					_, err := workers.PublishLatestContinue(ctx, p.ID, id)
					return err
				}
				_, err := runtime.Publish(ctx, h.ID, holons.Publish{TargetCommit: explicit, Remote: "wrong", UpstreamBranch: "wrong", ExpectedRemoteHead: "wrong"})
				return err
			}
			commit := func(name string) string {
				gitPlumbingWrite(t, h.WorktreePath, name, name)
				gitPlumbingCommand(t, h.WorktreePath, "add", ".")
				gitPlumbingCommand(t, h.WorktreePath, "-c", "user.name=Test", "-c", "user.email=test@invalid", "commit", "-m", name)
				return gitPlumbingOutput(t, h.WorktreePath, "rev-parse", "HEAD")
			}
			for i := 0; i < 2; i++ {
				if i == 1 {
					// Model the durable checkpoint of a completed Holon rebase
					// while the accepted PR snapshot still contains the prior head.
					gitPlumbingCommand(t, root, "fetch", "origin", "feature")
					gitPlumbingCommand(t, root, "switch", "-C", "external", "FETCH_HEAD")
					gitPlumbingWrite(t, root, "external.txt", "external")
					gitPlumbingCommand(t, root, "add", ".")
					gitPlumbingCommand(t, root, "commit", "-m", "external")
					synchronized := gitPlumbingOutput(t, root, "rev-parse", "HEAD")
					gitPlumbingCommand(t, root, "push", "origin", "HEAD:feature")
					gitPlumbingCommand(t, h.WorktreePath, "fetch", "origin", "feature")
					gitPlumbingCommand(t, h.WorktreePath, "rebase", synchronized)
					saved, err := hs.Get(ctx, h.ID)
					if err != nil {
						t.Fatal(err)
					}
					saved.SynchronizedTargetCommit = synchronized
					if err := hs.Update(ctx, saved); err != nil {
						t.Fatal(err)
					}
				}
				result := commit(fmt.Sprintf("change-%d", i))
				before := syncer.calls
				id := fmt.Sprintf("publish-%d", i)
				if err := publish(id, ""); err != nil {
					t.Fatal(err)
				}
				if entry == "github combined" && i == 0 && observations.focused != 1 {
					t.Fatalf("publication did not use combined verification: %d", observations.focused)
				}
				if syncer.calls != before+1 || completions != i+1 {
					t.Fatalf("preparations=%d before=%d completions=%d", syncer.calls, before, completions)
				}
				if got := gitPlumbingOutput(t, remote, "rev-parse", "feature"); got != result {
					t.Fatalf("remote=%s want=%s", got, result)
				}
				if err := publish(id, ""); err != nil {
					t.Fatal(err)
				}
				if syncer.calls != before+1 {
					t.Fatal("replay prepared again")
				}
			}
			commit("pending")
			if entry != "continue direct" {
				before := syncer.calls
				if err := publish("explicit-mismatch", base); err == nil {
					t.Fatal("accepted explicit mismatch")
				}
				if syncer.calls != before+1 {
					t.Fatal("explicit mismatch refreshed twice")
				}
			}
			if kind != holons.KindPullWorker {
				for _, change := range []string{"workspace", "cache version"} {
					gate := observedPublicationGate{PublicationGate: readers, after: func() {
						if change == "workspace" {
							commit("changed-after-preparation")
							return
						}
						current, _ := catalog.GetPullRequest(p.ID)
						op, _, err := catalog.BeginOperation(ctx, pr.Operation{RequestID: "competing-writer", PullRequestID: p.ID, Kind: "publish", ExpectedHead: current.HeadCommit, ExpectedInputs: pr.CaptureMutationInputs(current), Groups: []pr.FieldGroup{pr.TopologyGroup}})
						if err != nil {
							t.Fatal(err)
						}
						if _, err := catalog.CompleteOperation(ctx, op.RequestID, "failed", "competing attempt"); err != nil {
							t.Fatal(err)
						}
					}}
					runtime.publication = pr.NewHolonPublisher(p.RepositoryID, catalog, hsrv, committer, gate)
					before := syncer.calls
					if err := publish("changed-"+change, ""); !errors.Is(err, pr.ErrPublicationStale) {
						t.Fatalf("%s: %v", change, err)
					}
					if syncer.calls != before+1 {
						t.Fatal("changed preparation refreshed again")
					}
				}
				runtime.publication = pr.NewHolonPublisher(p.RepositoryID, catalog, hsrv, committer, readers)
			}
			// Movement after preparation must be caught by the remote check/lease.
			syncer.after = func() { gitPlumbingCommand(t, remote, "update-ref", "refs/heads/feature", base) }
			before := syncer.calls
			if err := publish("remote-moved", ""); err == nil {
				t.Fatal("accepted remote movement")
			}
			if syncer.calls != before+1 {
				t.Fatal("remote conflict refreshed again")
			}
			syncer.after = nil
			// Movement before preparation is rejected against the pinned checkpoint.
			if err := publish("already-moved", ""); !errors.Is(err, pr.ErrPublicationStale) && !errors.Is(err, pullrequestwork.ErrStaleHead) {
				t.Fatalf("stale error=%v", err)
			}
		})
	}
}
