package sqliteadapter

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	pullrequestsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/testfixture/branchfixture"
)

func publicationTestStores(t *testing.T) (*CompletionCommitter, *Store, *pullrequestsqlite.Store, pullrequestlifecycle.PullRequest) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "publication.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`create table repositories(id text primary key); insert into repositories(id) values('repo')`); err != nil {
		t.Fatal(err)
	}
	catalog, err := pullrequestsqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	work, err := New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	pr, err := branchfixture.Create(t.Context(), catalog, pullrequestlifecycle.PullRequest{RepositoryID: "repo", Title: "Change", Status: pullrequestlifecycle.StatusOpen, BaseBranch: "main", BaseCommit: "base", DiffBaseCommit: "diff-base", HeadBranch: "feature", HeadCommit: "old-head", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return NewCompletionCommitter(work, catalog), work, catalog, pr
}

func publishedWork(id, pullRequestID string, status pullrequestwork.Status) pullrequestwork.Work {
	now := time.Now().UTC()
	return pullrequestwork.Work{ID: id, PullRequestID: pullRequestID, Kind: pullrequestwork.KindWorker, Status: status, HeadCommit: "old-head", ResultHeadCommit: "new-head", ReplyBody: "Fixed", PublicationState: "published", CreatedAt: now, CompletedAt: &now}
}

func TestPublicationTransactionAllowsConcurrentCatalogRead(t *testing.T) {
	committer, _, catalog, pr := publicationTestStores(t)
	tx, err := committer.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	waitCount := committer.db.Stats().WaitCount
	read := make(chan pullrequestlifecycle.PullRequest, 1)
	go func() { value, _ := catalog.GetPullRequest(pr.ID); read <- value }()
	// Wait until the reader is waiting for the transaction's connection.
	deadline := time.Now().Add(2 * time.Second)
	for committer.db.Stats().WaitCount == waitCount {
		if time.Now().After(deadline) {
			t.Fatal("catalog reader did not start")
		}
		time.Sleep(time.Millisecond)
	}
	type result struct {
		matched bool
		err     error
	}
	completed := make(chan result, 1)
	go func() {
		matched, err := catalog.AdvancePublishedHeadInTransaction(t.Context(), tx, pr.ID, "old-head", "new-head")
		completed <- result{matched, err}
	}()
	select {
	case result := <-completed:
		if result.err != nil || !result.matched {
			t.Fatalf("publication=%+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publication deadlocked against the waiting catalog reader")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-read:
		if got.HeadCommit != "old-head" || got.ComparisonState != pullrequestlifecycle.ComparisonStale {
			t.Fatalf("reader saw %q", got.HeadCommit)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("catalog reader did not finish after publication")
	}
}

func TestPublicationCommitterAtomicallyCompletesWorkAndAdvancesBothCatalogs(t *testing.T) {
	committer, workStore, catalog, pr := publicationTestStores(t)
	work := publishedWork("work", pr.ID, pullrequestwork.StatusRunning)
	work.CompletedAt = nil
	if err := workStore.Create(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	work.Status = pullrequestwork.StatusCompleted
	now := time.Now().UTC()
	work.CompletedAt = &now
	if err := committer.CommitCompletion(t.Context(), pullrequestwork.CompletionCommit{Work: work, ExpectedSourceHead: "old-head", ResultingHead: "new-head"}); err != nil {
		t.Fatal(err)
	}
	stored, err := workStore.Get(t.Context(), work.ID)
	if err != nil || stored.Status != pullrequestwork.StatusCompleted {
		t.Fatalf("work=%+v err=%v", stored, err)
	}
	persisted, ok := catalog.GetPullRequest(pr.ID)
	if !ok || persisted.HeadCommit != "old-head" || persisted.ComparisonState != pullrequestlifecycle.ComparisonStale || persisted.BaseCommit != "base" || persisted.DiffBaseCommit != "diff-base" {
		t.Fatalf("catalog=%+v ok=%v", persisted, ok)
	}
	var relationalHead string
	if err = committer.db.QueryRow(`select head_commit from pull_requests where id=?`, pr.ID).Scan(&relationalHead); err != nil || relationalHead != "old-head" {
		t.Fatalf("relational head=%q err=%v", relationalHead, err)
	}
	if err = committer.CommitCompletion(t.Context(), pullrequestwork.CompletionCommit{Work: work, ExpectedSourceHead: "old-head", ResultingHead: "new-head"}); err != nil {
		t.Fatalf("idempotent commit: %v", err)
	}
}

func TestPublicationCommitterRollsBackCatalogWhenWorkUpdateFails(t *testing.T) {
	committer, workStore, catalog, pr := publicationTestStores(t)
	work := publishedWork("work", pr.ID, pullrequestwork.StatusRunning)
	work.CompletedAt = nil
	if err := workStore.Create(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	if _, err := committer.db.Exec(`create trigger reject_work_completion before update on pull_request_work begin select raise(abort, 'work update failed'); end`); err != nil {
		t.Fatal(err)
	}
	work.Status = pullrequestwork.StatusCompleted
	if err := committer.CommitCompletion(t.Context(), pullrequestwork.CompletionCommit{Work: work, ExpectedSourceHead: "old-head", ResultingHead: "new-head"}); err == nil {
		t.Fatal("publication commit succeeded")
	}
	persisted, _ := catalog.GetPullRequest(pr.ID)
	stored, err := workStore.Get(t.Context(), work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.HeadCommit != "old-head" || stored.Status != pullrequestwork.StatusRunning {
		t.Fatalf("catalog=%+v work=%+v", persisted, stored)
	}
}

func TestCompletionCommitterRollsBackWorkWhenPullRequestUpdateFails(t *testing.T) {
	committer, workStore, catalog, pr := publicationTestStores(t)
	work := publishedWork("rebase", pr.ID, pullrequestwork.StatusRunning)
	work.Kind = pullrequestwork.KindRebase
	work.CompletedAt = nil
	if err := workStore.Create(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	catalog.SetActionCompletionHook(func(string) { t.Error("failed transaction emitted completion refresh") })
	if _, err := committer.db.Exec(`create trigger reject_pr_update before update on pull_requests begin select raise(abort, 'pull request update failed'); end`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	work.Status = pullrequestwork.StatusCompleted
	work.CompletedAt = &now
	completion := pullrequestwork.CompletionCommit{Work: work, ExpectedSourceHead: "old-head", ResultingHead: "new-head", Rebase: &pullrequestwork.RebaseCompletionMetadata{TargetBaseCommit: "new-base", TargetDiffBaseCommit: "new-base"}}
	if err := committer.CommitCompletion(t.Context(), completion); err == nil {
		t.Fatal("completion commit succeeded")
	}
	persisted, _ := catalog.GetPullRequest(pr.ID)
	stored, err := workStore.Get(t.Context(), work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.HeadCommit != "old-head" || persisted.BaseCommit != "base" || persisted.DiffBaseCommit != "diff-base" || stored.Status != pullrequestwork.StatusRunning {
		t.Fatalf("catalog=%+v work=%+v", persisted, stored)
	}
}

func TestPublicationCommitterRejectsUnrelatedCatalogHead(t *testing.T) {
	committer, workStore, catalog, pr := publicationTestStores(t)
	work := publishedWork("work", pr.ID, pullrequestwork.StatusRunning)
	work.CompletedAt = nil
	if err := workStore.Create(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	if _, err := branchfixture.Accept(t.Context(), catalog, pr.ID, "base", "other-head", "diff-base"); err != nil {
		t.Fatal(err)
	}
	work.Status = pullrequestwork.StatusCompleted
	if err := committer.CommitCompletion(t.Context(), pullrequestwork.CompletionCommit{Work: work, ExpectedSourceHead: "old-head", ResultingHead: "new-head"}); !errors.Is(err, pullrequestwork.ErrStaleHead) {
		t.Fatalf("error=%v", err)
	}
	stored, err := workStore.Get(t.Context(), work.ID)
	if err != nil || stored.Status != pullrequestwork.StatusRunning {
		t.Fatalf("work=%+v err=%v", stored, err)
	}
}

func TestCompletionCommitterRetainsComparisonAndRebaseHistory(t *testing.T) {
	committer, workStore, catalog, pr := publicationTestStores(t)
	work := publishedWork("rebase", pr.ID, pullrequestwork.StatusRunning)
	work.Kind = pullrequestwork.KindRebase
	work.TargetBaseCommit = "target-base"
	work.TargetDiffBaseCommit = "target-diff-base"
	work.CompletedAt = nil
	if err := workStore.Create(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	if _, err := branchfixture.Accept(t.Context(), catalog, pr.ID, "target-base", "old-head", "diff-base"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	work.Status = pullrequestwork.StatusCompleted
	work.CompletedAt = &now
	completion := pullrequestwork.CompletionCommit{
		Work: work, ExpectedSourceHead: "old-head", ResultingHead: "new-head",
		Rebase: &pullrequestwork.RebaseCompletionMetadata{TargetBaseCommit: "target-base", TargetDiffBaseCommit: "target-diff-base"},
	}
	if err := committer.CommitCompletion(t.Context(), completion); err != nil {
		t.Fatal(err)
	}
	persisted, ok := catalog.GetPullRequest(pr.ID)
	if !ok || persisted.HeadCommit != "old-head" || persisted.BaseCommit != "target-base" || persisted.DiffBaseCommit != "diff-base" || persisted.ComparisonState != pullrequestlifecycle.ComparisonStale {
		t.Fatalf("catalog=%+v ok=%v", persisted, ok)
	}
	stored, err := workStore.Get(t.Context(), work.ID)
	if err != nil || stored.Status != pullrequestwork.StatusCompleted || stored.ResultHeadCommit != "new-head" || stored.TargetBaseCommit != "target-base" || stored.TargetDiffBaseCommit != "target-diff-base" {
		t.Fatalf("completed history=%+v err=%v", stored, err)
	}
	var head, base, diffBase string
	if err := committer.db.QueryRow(`select head_commit,base_commit,diff_base_commit from pull_requests where id=?`, pr.ID).Scan(&head, &base, &diffBase); err != nil {
		t.Fatal(err)
	}
	if head != "old-head" || base != "target-base" || diffBase != "diff-base" {
		t.Fatalf("relational head=%q base=%q diff-base=%q", head, base, diffBase)
	}
}

func TestCompletionCommitterPreservesMetadataForLegacyRebaseCheckpoint(t *testing.T) {
	committer, workStore, catalog, pr := publicationTestStores(t)
	work := publishedWork("legacy-rebase", pr.ID, pullrequestwork.StatusRunning)
	work.Kind = pullrequestwork.KindRebase
	work.CompletedAt = nil
	if err := workStore.Create(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	work.Status = pullrequestwork.StatusCompleted
	work.CompletedAt = &now
	completion := pullrequestwork.CompletionCommit{Work: work, ExpectedSourceHead: "old-head", ResultingHead: "new-head", Rebase: &pullrequestwork.RebaseCompletionMetadata{}}
	if err := committer.CommitCompletion(t.Context(), completion); err != nil {
		t.Fatal(err)
	}
	persisted, ok := catalog.GetPullRequest(pr.ID)
	if !ok || persisted.HeadCommit != "old-head" || persisted.ComparisonState != pullrequestlifecycle.ComparisonStale || persisted.BaseCommit != "base" || persisted.DiffBaseCommit != "diff-base" {
		t.Fatalf("catalog=%+v ok=%v", persisted, ok)
	}
}

func TestCompletionCommitterNoOpRebaseIsIdempotent(t *testing.T) {
	committer, workStore, catalog, pr := publicationTestStores(t)
	work := publishedWork("noop-rebase", pr.ID, pullrequestwork.StatusRunning)
	work.Kind = pullrequestwork.KindRebase
	work.ResultHeadCommit = "old-head"
	work.CompletedAt = nil
	if err := workStore.Create(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	if _, err := branchfixture.Accept(t.Context(), catalog, pr.ID, "new-base", "old-head", "diff-base"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	work.Status = pullrequestwork.StatusCompleted
	work.CompletedAt = &now
	completion := pullrequestwork.CompletionCommit{Work: work, ExpectedSourceHead: "old-head", ResultingHead: "old-head", Rebase: &pullrequestwork.RebaseCompletionMetadata{TargetBaseCommit: "new-base", TargetDiffBaseCommit: "new-base"}}
	for range 2 {
		if err := committer.CommitCompletion(t.Context(), completion); err != nil {
			t.Fatal(err)
		}
	}
	persisted, _ := catalog.GetPullRequest(pr.ID)
	if persisted.HeadCommit != "old-head" || persisted.BaseCommit != "new-base" || persisted.DiffBaseCommit != "diff-base" || persisted.ComparisonState != pullrequestlifecycle.ComparisonStale {
		t.Fatalf("catalog=%+v", persisted)
	}
}

func continuePublishedWork(id, pullRequestID string, status pullrequestwork.Status) pullrequestwork.Work {
	return pullrequestwork.Work{
		ID: id, PullRequestID: pullRequestID, Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeContinue,
		Status: status, HeadBranch: "feature", HeadCommit: "new-head", BaseHeadCommit: "new-head", ResultHeadCommit: "new-head", PublicationState: "published", CreatedAt: time.Now().UTC(),
	}
}

func TestContinuePublicationCommitterAtomicallyAdvancesWorkerAndCatalogs(t *testing.T) {
	_, workStore, catalog, pr := publicationTestStores(t)
	committer := NewContinuePublicationCommitter(workStore, catalog)
	original := continuePublishedWork("continue", pr.ID, pullrequestwork.StatusWaiting)
	original.HeadCommit, original.BaseHeadCommit, original.ResultHeadCommit, original.PublicationState = "old-head", "old-head", "", ""
	if err := workStore.Create(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	published := continuePublishedWork(original.ID, pr.ID, pullrequestwork.StatusWaiting)
	for range 2 {
		if err := committer.CommitContinuePublication(t.Context(), published, "old-head", "new-head"); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := workStore.Get(t.Context(), original.ID)
	if err != nil || stored.Status != pullrequestwork.StatusWaiting || stored.HeadCommit != "new-head" || stored.BaseHeadCommit != "new-head" || stored.ResultHeadCommit != "new-head" {
		t.Fatalf("work=%+v err=%v", stored, err)
	}
	persisted, ok := catalog.GetPullRequest(pr.ID)
	if !ok || persisted.HeadCommit != "old-head" || persisted.ComparisonState != pullrequestlifecycle.ComparisonStale {
		t.Fatalf("catalog=%+v ok=%v", persisted, ok)
	}
	var relationalHead string
	if err = committer.db.QueryRow(`select head_commit from pull_requests where id=?`, pr.ID).Scan(&relationalHead); err != nil || relationalHead != "old-head" {
		t.Fatalf("relational head=%q err=%v", relationalHead, err)
	}
}

func TestContinuePublicationCommitterRollsBackCatalogWhenWorkerUpdateFails(t *testing.T) {
	_, workStore, catalog, pr := publicationTestStores(t)
	committer := NewContinuePublicationCommitter(workStore, catalog)
	original := continuePublishedWork("continue", pr.ID, pullrequestwork.StatusRunning)
	original.HeadCommit, original.BaseHeadCommit, original.ResultHeadCommit, original.PublicationState = "old-head", "old-head", "", ""
	if err := workStore.Create(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	if _, err := committer.db.Exec(`create trigger reject_continue_update before update on pull_request_work begin select raise(abort, 'worker update failed'); end`); err != nil {
		t.Fatal(err)
	}
	published := continuePublishedWork(original.ID, pr.ID, pullrequestwork.StatusRunning)
	if err := committer.CommitContinuePublication(t.Context(), published, "old-head", "new-head"); err == nil {
		t.Fatal("publication commit succeeded")
	}
	persisted, _ := catalog.GetPullRequest(pr.ID)
	stored, err := workStore.Get(t.Context(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.HeadCommit != "old-head" || stored.HeadCommit != "old-head" || stored.ResultHeadCommit != "" {
		t.Fatalf("catalog=%+v work=%+v", persisted, stored)
	}
}

// The queue service uses the real SQLite catalog and completion transaction.
type completionQueueCatalog struct{ store *pullrequestsqlite.Store }

func (c completionQueueCatalog) PullRequest(id string) (pullrequestwork.PullRequest, bool) {
	pr, ok := c.store.GetPullRequest(id)
	return pullrequestwork.PullRequest{ID: pr.ID, HeadCommit: pr.HeadCommit, BaseCommit: pr.BaseCommit, BaseBranch: pr.BaseBranch, HeadBranch: pr.HeadBranch, Active: pr.Status.Active()}, ok
}

type checkpointRuntime struct{ cancelled int }

func (*checkpointRuntime) Recover(context.Context, string) error { return nil }
func (*checkpointRuntime) State(context.Context, string) (pullrequestwork.Status, error) {
	return pullrequestwork.StatusFailed, nil
}
func (r *checkpointRuntime) Cancel(context.Context, string) error { r.cancelled++; return nil }

type checkpointExecution struct {
	launches  []pullrequestwork.Work
	publishes int
}

func (e *checkpointExecution) Start(_ context.Context, _ pullrequestwork.PullRequest, w pullrequestwork.Work, _ string) (string, error) {
	e.launches = append(e.launches, w)
	return "next-session", nil
}
func (e *checkpointExecution) Publish(_ context.Context, w pullrequestwork.Work, _ pullrequestwork.PublicationOptions) (pullrequestwork.Publication, error) {
	e.publishes++
	return pullrequestwork.Publication{}, errors.New("checkpoint must not publish again")
}

func TestQueueFinalizesPublishedRebaseBeforeLifecycleRetirement(t *testing.T) {
	for _, action := range []string{"cancel", "fail", "reconcile", "recover"} {
		for _, result := range []string{"old-head", "new-head"} {
			t.Run(action+"/"+result, func(t *testing.T) {
				committer, store, catalog, pr := publicationTestStores(t)
				if _, err := branchfixture.Accept(t.Context(), catalog, pr.ID, "new-base", "old-head", "diff-base"); err != nil {
					t.Fatal(err)
				}
				w := pullrequestwork.Work{ID: "rebase", PullRequestID: pr.ID, Kind: pullrequestwork.KindRebase, Status: pullrequestwork.StatusRunning, HeadCommit: "old-head", ResultHeadCommit: result, TargetBaseCommit: "new-base", TargetDiffBaseCommit: "new-base", PublicationState: "published"}
				if result == "new-head" {
					w.SessionID = "old-session"
				}
				next := pullrequestwork.Work{ID: "address", PullRequestID: pr.ID, Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusQueued, CommentID: "c1"}
				if err := store.CreateBatch(t.Context(), []pullrequestwork.Work{w, next}); err != nil {
					t.Fatal(err)
				}
				execution := &checkpointExecution{}
				runtime := &checkpointRuntime{}
				s := pullrequestwork.New(store, completionQueueCatalog{catalog}, execution, nil)
				s.SetCompletionCommitter(committer)
				s.SetPublisher(execution)
				s.SetWorkerRuntime(runtime)
				settle := func() error {
					switch action {
					case "cancel":
						_, err := s.Cancel(t.Context(), pr.ID, w.ID)
						return err
					case "fail":
						return s.Fail(t.Context(), w.ID, "runtime failed")
					case "reconcile":
						return s.Reconcile(t.Context())
					default:
						return s.Recover(t.Context())
					}
				}
				if _, err := committer.db.Exec(`CREATE TRIGGER reject_completion BEFORE UPDATE ON pull_request_work WHEN NEW.status='completed' BEGIN SELECT RAISE(ABORT, 'completion failed'); END`); err != nil {
					t.Fatal(err)
				}
				if err := settle(); err == nil {
					t.Fatal("expected completion persistence failure")
				}
				if err := s.Dispatch(t.Context()); err == nil {
					t.Fatal("dispatcher ignored an unsettled publication checkpoint")
				}
				stored, err := store.Get(t.Context(), w.ID)
				if err != nil || stored.Status != pullrequestwork.StatusRunning || stored.PublicationState != "published" || len(execution.launches) != 0 {
					t.Fatalf("unfinished checkpoint=%+v launches=%+v err=%v", stored, execution.launches, err)
				}
				if _, err := committer.db.Exec(`DROP TRIGGER reject_completion`); err != nil {
					t.Fatal(err)
				}
				if err := settle(); err != nil {
					t.Fatal(err)
				}
				if err := s.Dispatch(t.Context()); err != nil {
					t.Fatal(err)
				}
				stored, err = store.Get(t.Context(), w.ID)
				persisted, _ := catalog.GetPullRequest(pr.ID)
				if err != nil || stored.Status != pullrequestwork.StatusCompleted || stored.CompletedAt == nil || persisted.HeadCommit != result || persisted.BaseCommit != "new-base" || persisted.DiffBaseCommit != "new-base" || !persisted.HasCurrentComparison() {
					t.Fatalf("checkpoint=%+v pr=%+v err=%v", stored, persisted, err)
				}
				if len(execution.launches) != 1 || execution.launches[0].ID != next.ID || execution.launches[0].HeadCommit != result || execution.publishes != 0 || runtime.cancelled != 0 {
					t.Fatalf("execution=%+v runtime=%+v", execution, runtime)
				}
			})
		}
	}
}

func (c completionQueueCatalog) SyncPullRequest(ctx context.Context, id string) error {
	inputs, err := c.store.CaptureComparison(ctx, id)
	if err != nil {
		return err
	}
	return c.store.AcceptComparison(ctx, id, inputs, inputs.Base.Commit)
}
func (c completionQueueCatalog) PrepareWork(ctx context.Context, id string) error {
	return c.SyncPullRequest(ctx, id)
}

func (e *checkpointExecution) Reserve(context.Context, pullrequestwork.PullRequest, pullrequestwork.Work) error {
	return nil
}
func (e *checkpointExecution) StartReserved(ctx context.Context, pr pullrequestwork.PullRequest, w pullrequestwork.Work, prompt string) (error, error) {
	_, err := e.Start(ctx, pr, w, prompt)
	return err, nil
}
func (e *checkpointExecution) SettleReserved(context.Context, pullrequestwork.Work) error { return nil }
