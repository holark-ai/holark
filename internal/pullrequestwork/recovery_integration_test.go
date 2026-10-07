package pullrequestwork_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/pullrequestwork/sqliteadapter"
)

type recoveryCatalog struct{ pr pullrequestwork.PullRequest }

func (c recoveryCatalog) PullRequest(id string) (pullrequestwork.PullRequest, bool) {
	return c.pr, id == c.pr.ID
}

type recoveryReviewChanges struct{}

func (recoveryReviewChanges) Capture(_ context.Context, base, head string) (*pullrequestwork.ReviewInput, error) {
	return &pullrequestwork.ReviewInput{DiffBaseCommit: base, HeadCommit: head, PatchFingerprint: "patch", MessagesFingerprint: "messages"}, nil
}

type recoveryLaunchFunc func(context.Context, pullrequestwork.PullRequest, pullrequestwork.Work, string) (string, error)

func (f recoveryLaunchFunc) Start(ctx context.Context, pr pullrequestwork.PullRequest, w pullrequestwork.Work, prompt string) (string, error) {
	return f(ctx, pr, w, prompt)
}

func TestRecoveryFailsReviewInterruptedBeforeLaunchFinalization(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "work.sqlite")
	db, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store, err := sqliteadapter.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	catalog := recoveryCatalog{pr: pullrequestwork.PullRequest{ID: "pr", BaseCommit: "base", HeadCommit: "head", Active: true}}
	var queued pullrequestwork.Work
	launches := 0
	launcher := recoveryLaunchFunc(func(ctx context.Context, _ pullrequestwork.PullRequest, w pullrequestwork.Work, _ string) (string, error) {
		launches++
		queued, err = store.Get(ctx, w.ID)
		if err != nil || queued.Status != pullrequestwork.StatusQueued || queued.SessionID != "" || queued.Provenance == nil {
			t.Fatalf("review before launch finalization: %+v, %v", queued, err)
		}
		// Interrupt after persistence, before Start can record the launch result.
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		return "session", nil
	})
	service := newTestWorkService(store, catalog, launcher, nil)
	service.SetReviewChanges(recoveryReviewChanges{})
	if _, err := service.Start(ctx, pullrequestwork.Start{PullRequestID: "pr", Kind: pullrequestwork.KindReview}); err == nil || launches != 1 {
		t.Fatalf("interrupted start: launches=%d, err=%v", launches, err)
	}

	db, err = database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err = sqliteadapter.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	launcher = recoveryLaunchFunc(func(context.Context, pullrequestwork.PullRequest, pullrequestwork.Work, string) (string, error) {
		launches++
		return "new-session", nil
	})
	service = newTestWorkService(store, catalog, launcher, nil)
	service.SetReviewChanges(recoveryReviewChanges{})
	for range 2 {
		if err := service.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		recovered, err := store.Get(ctx, queued.ID)
		if err != nil || recovered.Status != pullrequestwork.StatusFailed || recovered.Error == "" || recovered.CompletedAt == nil || launches != 1 {
			t.Fatalf("recovered review: %+v, launches=%d, err=%v", recovered, launches, err)
		}
		want := queued
		want.Status, want.Error, want.CompletedAt = recovered.Status, recovered.Error, recovered.CompletedAt
		if !reflect.DeepEqual(recovered, want) {
			t.Fatalf("recovery changed recorded review input: got %+v, want %+v", recovered, want)
		}
	}
	works, err := service.Start(ctx, pullrequestwork.Start{PullRequestID: "pr", Kind: pullrequestwork.KindReview})
	if err != nil || len(works) != 1 || works[0].Status != pullrequestwork.StatusRunning || launches != 2 {
		t.Fatalf("new review after recovery: %+v, launches=%d, err=%v", works, launches, err)
	}
}

func TestRecoveryRestoresMixedFIFOAndReconcilesInterruptedRebase(t *testing.T) {
	for _, status := range []pullrequestwork.Status{pullrequestwork.StatusRunning, pullrequestwork.StatusWaiting, pullrequestwork.StatusCancelling} {
		t.Run(string(status), func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "mixed-queue.sqlite")
			db, err := database.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			store, err := sqliteadapter.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			works := []pullrequestwork.Work{
				{ID: "interrupted", PullRequestID: "pr", Kind: pullrequestwork.KindRebase, Status: status, SessionID: "old-session", HeadCommit: "head", TargetBaseCommit: "old-base"},
				{ID: "next-address", PullRequestID: "pr", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusQueued, CommentID: "c1"},
				{ID: "last-rebase", PullRequestID: "pr", Kind: pullrequestwork.KindRebase, Status: pullrequestwork.StatusQueued, MechanicalOnly: true, Prompt: "Preserve public API"},
			}
			if err := store.CreateBatch(ctx, works); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = database.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store, err = sqliteadapter.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			launcher := &queueLauncher{}
			runtime := &queueRuntime{state: pullrequestwork.StatusFailed}
			rebaseCalls := 0
			s := newTestWorkService(store, recoveryCatalog{pr: pullrequestwork.PullRequest{ID: "pr", HeadCommit: "head", Active: true}}, launcher, nil, queueRebaseFunc(func(_ context.Context, pr pullrequestwork.PullRequest, _ string) (pullrequestwork.RebasePreparation, error) {
				rebaseCalls++
				return pullrequestwork.RebasePreparation{PullRequest: pr, HeadCommit: pr.HeadCommit, TargetBaseCommit: "new-base"}, nil
			}))
			s.SetWorkerRuntime(runtime)
			if err := s.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			if err := s.Dispatch(ctx); err != nil {
				t.Fatal(err)
			}
			wantStatus := pullrequestwork.StatusFailed
			if status == pullrequestwork.StatusCancelling {
				wantStatus = pullrequestwork.StatusCancelled
			}
			interrupted, err := store.Get(ctx, "interrupted")
			if err != nil || interrupted.Status != wantStatus || len(runtime.recovered) != 1 || runtime.recovered[0] != "old-session" {
				t.Fatalf("interrupted=%+v runtime=%+v err=%v", interrupted, runtime, err)
			}
			queue, err := s.Queue(ctx, "pr")
			if err != nil || len(queue) != 2 || queue[0].ID != "next-address" || queue[0].Status != pullrequestwork.StatusRunning || queue[1].ID != "last-rebase" || queue[1].Status != pullrequestwork.StatusQueued || !queue[1].MechanicalOnly || queue[1].Prompt != "Preserve public API" || queue[1].HeadCommit != "" || rebaseCalls != 0 {
				t.Fatalf("recovered FIFO=%+v calls=%d err=%v", queue, rebaseCalls, err)
			}
			if _, err := s.Complete(ctx, "next-address", "head", pullrequestwork.Completion{}); err != nil {
				t.Fatal(err)
			}
			if err := s.Dispatch(ctx); err != nil {
				t.Fatal(err)
			}
			last, err := store.Get(ctx, "last-rebase")
			if err != nil || last.Status != pullrequestwork.StatusCompleted || last.TargetBaseCommit != "new-base" || last.ResultHeadCommit != "head" || rebaseCalls != 1 || len(launcher.works) != 1 {
				t.Fatalf("last=%+v calls=%d launches=%+v err=%v", last, rebaseCalls, launcher.works, err)
			}
		})
	}
}

func TestRecoverySettlesCancellingPublishedContinueWorkerThroughSession(t *testing.T) {
	for _, state := range []pullrequestwork.Status{pullrequestwork.StatusCancelling, pullrequestwork.StatusCancelled} {
		t.Run(string(state), func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "continue.sqlite")
			db, err := database.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			store, err := sqliteadapter.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			published := pullrequestwork.Work{
				ID: "continue", PullRequestID: "pr", SessionID: "session", Kind: pullrequestwork.KindWorker,
				Mode: pullrequestwork.ModeContinue, Status: pullrequestwork.StatusRunning,
				HeadCommit: "published-head", BaseHeadCommit: "published-head", ResultHeadCommit: "published-head", PublicationState: "published",
			}
			if err := store.Create(ctx, published); err != nil {
				t.Fatal(err)
			}
			runtime := &queueRuntime{state: pullrequestwork.StatusCancelling}
			service := newTestWorkService(store, nil, nil, nil)
			service.SetWorkerRuntime(runtime)
			if err := service.CancelPullRequest(ctx, "pr"); err != nil {
				t.Fatal(err)
			}
			cancelling, err := store.Get(ctx, published.ID)
			if err != nil || cancelling.Status != pullrequestwork.StatusCancelling || cancelling.PublicationState != "published" || cancelling.CompletedAt != nil {
				t.Fatalf("pending cancellation: %+v, %v", cancelling, err)
			}
			// Restart after cancellation is persisted but before it settles.
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = database.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			store, err = sqliteadapter.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			runtime = &queueRuntime{state: state}
			service = newTestWorkService(store, nil, nil, nil)
			service.SetWorkerRuntime(runtime)
			if err := service.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			recovered, err := store.Get(ctx, published.ID)
			if err != nil || recovered.Status != state || !reflect.DeepEqual(runtime.recovered, []string{"session"}) {
				t.Fatalf("recovered work: %+v, sessions=%v, err=%v", recovered, runtime.recovered, err)
			}
			if state == pullrequestwork.StatusCancelling {
				if recovered.CompletedAt != nil {
					t.Fatalf("settled before session stopped: %+v", recovered)
				}
				runtime.state = pullrequestwork.StatusCancelled
				if err := service.Reconcile(ctx); err != nil {
					t.Fatal(err)
				}
			}
			settled, err := store.Get(ctx, published.ID)
			if err != nil || settled.Status != pullrequestwork.StatusCancelled || settled.CompletedAt == nil || settled.PublicationState != published.PublicationState || settled.ResultHeadCommit != published.ResultHeadCommit || settled.ReplyBody != "" {
				t.Fatalf("settled work: %+v, %v", settled, err)
			}
		})
	}
}

func newTestWorkService(store pullrequestwork.Store, catalog pullrequestwork.Catalog, launcher pullrequestwork.Launcher, comments pullrequestwork.CommentDelivery, rebasers ...pullrequestwork.Rebaser) *pullrequestwork.Service {
	service := pullrequestwork.New(store, catalog, launcher, comments, rebasers...)
	service.SetCompletionCommitter(testCompletionStore{store})
	return service
}

type testCompletionStore struct{ pullrequestwork.Store }

func (store testCompletionStore) CommitCompletion(ctx context.Context, completion pullrequestwork.CompletionCommit) error {
	return store.Update(ctx, completion.Work)
}

func (recoveryCatalog) SyncPullRequest(context.Context, string) error { return nil }
func (recoveryCatalog) PrepareWork(context.Context, string) error     { return nil }

func (f recoveryLaunchFunc) Reserve(context.Context, pullrequestwork.PullRequest, pullrequestwork.Work) error {
	return nil
}
func (f recoveryLaunchFunc) StartReserved(ctx context.Context, pr pullrequestwork.PullRequest, w pullrequestwork.Work, prompt string) (error, error) {
	_, err := f.Start(ctx, pr, w, prompt)
	return err, nil
}
func (f recoveryLaunchFunc) SettleReserved(context.Context, pullrequestwork.Work) error { return nil }
