package localapp

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/holons"
	holonssqlite "github.com/holark-ai/holark/internal/holons/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	commentssqlite "github.com/holark-ai/holark/internal/pullrequestcomments/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	metadatagit "github.com/holark-ai/holark/internal/pullrequestmetadata/gitadapter"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	worksqlite "github.com/holark-ai/holark/internal/pullrequestwork/sqliteadapter"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
	"github.com/holark-ai/holark/internal/testfixture/branchfixture"
)

func TestReviewDispatchSettlesManuallyClosedWorkspace(t *testing.T) {
	ctx := t.Context()
	path := t.TempDir()
	gitPlumbingCommand(t, path, "init", "-b", "main")
	gitPlumbingCommand(t, path, "config", "user.name", "Review Test")
	gitPlumbingCommand(t, path, "config", "user.email", "review@invalid")
	gitPlumbingCommand(t, path, "commit", "--allow-empty", "-m", "Initial")
	head := gitPlumbingOutput(t, path, "rev-parse", "HEAD")
	git, err := gitadapter.OpenWithWorktrees(ctx, path, filepath.Join(t.TempDir(), "worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Join(t.TempDir(), "review-dispatch.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, "create table repositories(id text primary key); insert into repositories(id) values('repo')"); err != nil {
		t.Fatal(err)
	}
	prs, err := prsqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	pr, err := branchfixture.Create(ctx, prs, pullrequestlifecycle.PullRequest{ID: "pr", RepositoryID: "repo", Status: pullrequestlifecycle.StatusOpen, BaseBranch: "main", BaseCommit: head, HeadBranch: "topic", HeadCommit: head})
	if err != nil {
		t.Fatal(err)
	}
	store, err := worksqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	holonStore, err := holonssqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &terminalHolonService{Service: holons.NewServiceWithRepository(holonStore, holonRepositoryCoordinator{repositories: repository.NewService(git)})}
	svc := pullrequestwork.New(store, localWorkCatalog{store: prs}, localWorkLauncher{holons: runtime}, nil)
	svc.SetReviewChanges(localReviewChanges{changes: metadatagit.Changes{Path: path}})
	svc.SetWorkerRuntime(localWorkRuntime{holons: runtime})
	start := pullrequestwork.Start{PullRequestID: pr.ID, Kind: pullrequestwork.KindReview, Mode: pullrequestwork.ModeAuto}
	completed, err := svc.Start(ctx, start)
	if err != nil || len(completed) != 1 {
		t.Fatalf("start completed review: %+v, %v", completed, err)
	}
	saved, err := svc.Complete(ctx, completed[0].ID, head, pullrequestwork.Completion{Summary: "Saved review"})
	if err != nil || saved.Status != pullrequestwork.StatusCompleted || saved.CompletedAt == nil {
		t.Fatalf("complete review: %+v, %v", saved, err)
	}
	saved, err = store.Get(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.End(ctx, saved.SessionID); err != nil {
		t.Fatal(err)
	}
	running, err := svc.Start(ctx, start)
	if err != nil || len(running) != 1 || running[0].Status != pullrequestwork.StatusRunning {
		t.Fatalf("start running review: %+v, %v", running, err)
	}
	closed, err := runtime.End(ctx, running[0].SessionID)
	if err != nil || closed.Status != holons.StatusCancelled || closed.ArchivedAt == nil {
		t.Fatalf("close review workspace: %+v, %v", closed, err)
	}
	// Closing the workspace leaves durable review work running until dispatch.
	before, err := store.Get(ctx, running[0].ID)
	if err != nil || before.Status != pullrequestwork.StatusRunning || before.CompletedAt != nil {
		t.Fatalf("review settled before dispatch: %+v, %v", before, err)
	}
	// No queued workers, recovery, or explicit work cancellation can trigger this scan.
	for range 2 {
		if err := svc.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		settled, err := store.Get(ctx, running[0].ID)
		if err != nil || settled.Status != pullrequestwork.StatusCancelled || settled.CompletedAt == nil || settled.Error != "" {
			t.Fatalf("closed review did not settle: %+v, %v", settled, err)
		}
		history, err := store.Get(ctx, saved.ID)
		if err != nil || !reflect.DeepEqual(history, saved) {
			t.Fatalf("dispatch changed completed review: %+v, want %+v, err=%v", history, saved, err)
		}
	}
	replacement, err := svc.Start(ctx, start)
	if err != nil || len(replacement) != 1 || replacement[0].Status != pullrequestwork.StatusRunning || replacement[0].SessionID == "" || replacement[0].SessionID == running[0].SessionID {
		t.Fatalf("replacement review did not start: %+v, %v", replacement, err)
	}
}

type reviewLaunchFunc func(context.Context, pullrequestwork.PullRequest, pullrequestwork.Work, string) (string, error)

func (f reviewLaunchFunc) Start(ctx context.Context, pr pullrequestwork.PullRequest, w pullrequestwork.Work, prompt string) (string, error) {
	return f(ctx, pr, w, prompt)
}

type countedReviewChanges struct {
	pullrequestwork.ReviewChanges
	calls int
}

func (c *countedReviewChanges) Capture(ctx context.Context, base, head string) (*pullrequestwork.ReviewInput, error) {
	c.calls++
	return c.ReviewChanges.Capture(ctx, base, head)
}

func TestReviewPinsPersistsAndImportsOriginalRevision(t *testing.T) {
	for _, test := range []struct {
		name, content, message string
		outdated               bool
	}{
		{"neither changed", "Feature\n", "Feature", false},
		{"patch only", "Amended\n", "Feature", false},
		{"messages only", "Feature\n", "New explanation", false},
		{"both changed", "Amended\n", "New explanation", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			path := t.TempDir()
			gitPlumbingCommand(t, path, "init", "-b", "main")
			gitPlumbingCommand(t, path, "config", "user.name", "Review Test")
			gitPlumbingCommand(t, path, "config", "user.email", "review@invalid")
			gitPlumbingWrite(t, path, "file.txt", "Original\n")
			gitPlumbingCommand(t, path, "add", ".")
			gitPlumbingCommand(t, path, "commit", "-m", "Initial")
			base := gitPlumbingOutput(t, path, "rev-parse", "HEAD")
			gitPlumbingWrite(t, path, "file.txt", "Feature\n")
			gitPlumbingCommand(t, path, "commit", "-am", "Feature")
			head := gitPlumbingOutput(t, path, "rev-parse", "HEAD")
			git, err := gitadapter.OpenWithWorktrees(ctx, path, filepath.Join(t.TempDir(), "worktrees"))
			if err != nil {
				t.Fatal(err)
			}
			dbPath := filepath.Join(t.TempDir(), "state.sqlite")
			db, err := database.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			if _, err = db.ExecContext(ctx, "create table repositories(id text primary key); insert into repositories(id) values('repo')"); err != nil {
				t.Fatal(err)
			}
			prs, err := prsqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			pr, err := branchfixture.Create(ctx, prs, pullrequestlifecycle.PullRequest{ID: "pr", RepositoryID: "repo", Title: "Review feature", Status: pullrequestlifecycle.StatusOpen, BaseBranch: "main", BaseCommit: base, HeadBranch: "topic", HeadCommit: head})
			if err != nil {
				t.Fatal(err)
			}
			// Exercise both the explicit diff base and its historical base-commit fallback.
			if test.name == "both changed" {
				pr.DiffBaseCommit = base
				pr.BaseCommit = head
				if _, err = branchfixture.Accept(ctx, prs, pr.ID, head, head, base); err != nil {
					t.Fatal(err)
				}
			}
			store, err := worksqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			holonStore, err := holonssqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			hs := holons.NewServiceWithRepository(holonStore, holonRepositoryCoordinator{repositories: repository.NewService(git)})
			launcher := localWorkLauncher{holons: &terminalHolonService{Service: hs}}
			changes := localReviewChanges{changes: metadatagit.Changes{Path: path}}
			launches := 0
			advanced := ""
			start := reviewLaunchFunc(func(ctx context.Context, input pullrequestwork.PullRequest, work pullrequestwork.Work, prompt string) (string, error) {
				launches++
				persisted, err := store.Get(ctx, work.ID)
				if err != nil || persisted.Provenance == nil || *persisted.Provenance != *work.Provenance || persisted.Provenance.HeadCommit != head || persisted.Provenance.DiffBaseCommit != base {
					t.Fatalf("input before launch: %+v, %v", persisted, err)
				}
				// Advance the PR between capture and workspace creation. The launcher must
				// use durable provenance even when handed a newer catalog snapshot.
				gitPlumbingWrite(t, path, "file.txt", test.content)
				gitPlumbingCommand(t, path, "commit", "-am", test.message, "--amend", "--allow-empty")
				advanced = gitPlumbingOutput(t, path, "rev-parse", "HEAD")
				if _, err := branchfixture.Accept(ctx, prs, pr.ID, pr.BaseCommit, advanced, base); err != nil {
					t.Fatalf("advance: %v", err)
				}
				input.HeadCommit, input.DiffBaseCommit = advanced, advanced
				return launcher.Start(ctx, input, work, prompt)
			})
			svc := pullrequestwork.New(store, localWorkCatalog{store: prs}, start, nil)
			svc.SetReviewChanges(changes)
			works, err := svc.Start(ctx, pullrequestwork.Start{PullRequestID: pr.ID, Kind: pullrequestwork.KindReview, Mode: pullrequestwork.ModeAuto})
			if err != nil {
				t.Fatal(err)
			}
			w := works[0]
			h, err := hs.Get(ctx, w.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if h.BaseCommit != base || h.WorkSessionStartCommit != head || gitPlumbingOutput(t, h.WorktreePath, "rev-parse", "HEAD") != head || !strings.Contains(h.Prompt, base+".."+head) {
				t.Fatalf("unpinned workspace/prompt: %+v", h)
			}
			if _, err = svc.Start(ctx, pullrequestwork.Start{PullRequestID: pr.ID, Kind: pullrequestwork.KindReview, Mode: pullrequestwork.ModeAuto}); !errors.Is(err, pullrequestwork.ErrBusy) || launches != 1 {
				t.Fatalf("duplicate admission: %v, %d launches", err, launches)
			}
			// Reopen the actual database so subsequent completion cannot rely on memory.
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = database.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			prs, err = prsqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			store, err = worksqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			cs, err := commentssqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			comments := pullrequestcomments.NewService(cs, localPullRequestTargets{catalog: prs})
			svc = pullrequestwork.New(store, localWorkCatalog{store: prs}, start, localReviewComments{comments: comments})
			svc.SetReviewChanges(changes)
			for _, invalid := range []struct {
				expected   string
				completion pullrequestwork.Completion
				want       error
			}{
				{head, pullrequestwork.Completion{PullRequestID: "other", HeadCommit: head}, pullrequestwork.ErrInvalid},
				{head, pullrequestwork.Completion{HeadCommit: "wrong"}, pullrequestwork.ErrStaleHead},
				{"wrong", pullrequestwork.Completion{HeadCommit: head}, pullrequestwork.ErrStaleHead},
			} {
				if _, err = svc.Complete(ctx, w.ID, invalid.expected, invalid.completion); !errors.Is(err, invalid.want) {
					t.Fatalf("invalid completion: %v", err)
				}
			}
			line := 1
			completion := pullrequestwork.Completion{PullRequestID: pr.ID, HeadCommit: head, Summary: "Completed review", Comments: []pullrequestwork.ReviewComment{
				{Body: "First comment", Scope: "pull_request"},
				{Body: "Second comment", Scope: "line", Path: "file.txt", Side: "RIGHT", Line: &line},
			}}
			for range 2 {
				completed, err := svc.Complete(ctx, w.ID, head, completion)
				if err != nil || completed.Status != pullrequestwork.StatusCompleted {
					t.Fatalf("completion: %+v %v", completed, err)
				}
			}
			findings, err := comments.ListByPullRequest(ctx, pr.ID)
			if err != nil || len(findings) != 2 {
				t.Fatalf("findings: %+v %v", findings, err)
			}
			for index, comment := range findings {
				if comment.OriginalHeadCommit != head || comment.SourceReviewID != w.ID || comment.SourceSessionID != w.SessionID || comment.SourceReviewIndex != index {
					t.Fatalf("attribution: %+v", comment)
				}
			}
			if findings[1].Scope != pullrequestcomments.ScopeLine || findings[1].Path != "file.txt" || findings[1].Side != "RIGHT" || findings[1].Line == nil || *findings[1].Line != 1 {
				t.Fatalf("line comment: %+v", findings[1])
			}
			listed, err := svc.List(ctx, pr.ID, pullrequestwork.KindReview)
			if err != nil || len(listed) != 1 {
				t.Fatalf("list: %+v %v", listed, err)
			}
			fresh := listed[0].Freshness
			if fresh == nil || fresh.Outdated != test.outdated || fresh.Generated.HeadCommit != head || fresh.Current.HeadCommit != advanced || fresh.Generated.DiffBaseCommit != base {
				t.Fatalf("freshness: %+v", fresh)
			}
			// Derived freshness never becomes a persisted completion status or document.
			persisted, err := store.Get(ctx, w.ID)
			if err != nil || persisted.Freshness != nil || persisted.Provenance == nil || persisted.Status != pullrequestwork.StatusCompleted {
				t.Fatalf("stored review: %+v %v", persisted, err)
			}
			// Legacy records preserve their information without invented provenance.
			legacy := pullrequestwork.Work{ID: "legacy", PullRequestID: pr.ID, Kind: pullrequestwork.KindReview, Status: pullrequestwork.StatusCompleted, Summary: "Historical review", HeadCommit: head}
			if err := store.Create(ctx, legacy); err != nil {
				t.Fatal(err)
			}
			// Several recorded reviews share one current Git capture per listing,
			// while retaining their own generated inputs and freshness results.
			recent := persisted
			recent.ID, recent.HeadCommit, recent.BaseHeadCommit = "recent", advanced, advanced
			recent.Provenance, err = changes.Capture(ctx, base, advanced)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Create(ctx, recent); err != nil {
				t.Fatal(err)
			}
			captures := &countedReviewChanges{ReviewChanges: changes}
			svc.SetReviewChanges(captures)
			for range 2 {
				captures.calls = 0
				listed, err = svc.List(ctx, pr.ID, pullrequestwork.KindReview)
				if err != nil || len(listed) != 3 || captures.calls != 1 {
					t.Fatalf("history captures=%d reviews=%+v err=%v", captures.calls, listed, err)
				}
				if listed[0].Freshness == nil || listed[0].Freshness.Outdated != test.outdated || listed[2].Freshness == nil || listed[2].Freshness.Outdated || *listed[0].Freshness.Current != *listed[2].Freshness.Current || listed[1].Freshness != nil {
					t.Fatalf("history freshness: %+v", listed)
				}
			}
			captures.ReviewChanges = localReviewChanges{changes: metadatagit.Changes{Path: t.TempDir()}}
			captures.calls = 0
			listed, err = svc.List(ctx, pr.ID, pullrequestwork.KindReview)
			if err != nil || len(listed) != 3 || captures.calls != 1 || listed[2].Freshness != nil || listed[0].Freshness != nil || listed[0].Status != pullrequestwork.StatusCompleted || listed[1].Provenance != nil || listed[1].Freshness != nil || listed[1].Summary != legacy.Summary {
				t.Fatalf("unavailable/legacy: %+v %v", listed, err)
			}
			if _, err := svc.Start(ctx, pullrequestwork.Start{PullRequestID: pr.ID, Kind: pullrequestwork.KindReview, Mode: pullrequestwork.ModeAuto}); err == nil || launches != 1 {
				t.Fatalf("capture failure launched review: %v %d", err, launches)
			}
			inactive := persisted
			inactive.ID, inactive.Status = "inactive-review", pullrequestwork.StatusRunning
			if err := store.Create(ctx, inactive); err != nil {
				t.Fatal(err)
			}
			if _, err := prs.TransitionPullRequestStatus(pr.ID, pullrequestlifecycle.StatusClosed, time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Complete(ctx, inactive.ID, head, completion); !errors.Is(err, pullrequestwork.ErrStaleHead) {
				t.Fatalf("inactive completion: %v", err)
			}

		})
	}
}

func (f reviewLaunchFunc) Reserve(context.Context, pullrequestwork.PullRequest, pullrequestwork.Work) error {
	return nil
}
func (f reviewLaunchFunc) StartReserved(ctx context.Context, pr pullrequestwork.PullRequest, w pullrequestwork.Work, prompt string) (error, error) {
	_, err := f.Start(ctx, pr, w, prompt)
	return err, nil
}
func (f reviewLaunchFunc) SettleReserved(context.Context, pullrequestwork.Work) error { return nil }
