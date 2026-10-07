package pullrequestwork_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

type dispatchCatalog struct {
	prepare func(context.Context, string) error
}

func (c dispatchCatalog) PrepareWork(ctx context.Context, id string) error {
	if c.prepare != nil {
		return c.prepare(ctx, id)
	}
	return nil
}
func (dispatchCatalog) SyncPullRequest(context.Context, string) error { return nil }
func (dispatchCatalog) PullRequest(id string) (pullrequestwork.PullRequest, bool) {
	return pullrequestwork.PullRequest{ID: id, HeadCommit: "head", Active: true}, true
}

func awaitDispatch[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher did not reach the expected boundary")
		var zero T
		return zero
	}
}
func runDispatcher(t *testing.T, service *pullrequestwork.Service) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); service.RunDispatcher(ctx) }()
	stop := func() { cancel(); awaitDispatch(t, done) }
	t.Cleanup(stop)
	return stop
}

func TestDispatcherAdmissionOutlivesRequestAndCoalescesWakes(t *testing.T) {
	for _, kind := range []pullrequestwork.Kind{pullrequestwork.KindWorker, pullrequestwork.KindRebase} {
		t.Run(string(kind), func(t *testing.T) {
			_, store := mixedQueueStore(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var preparations, launches atomic.Int32
			catalog := dispatchCatalog{prepare: func(ctx context.Context, _ string) error {
				preparations.Add(1)
				close(entered)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}}
			launched := make(chan pullrequestwork.Work, 2)
			service := newTestWorkService(store, catalog, recoveryLaunchFunc(func(ctx context.Context, _ pullrequestwork.PullRequest, w pullrequestwork.Work, _ string) (string, error) {
				if err := ctx.Err(); err != nil {
					return "", err
				}
				if pullrequestlifecycle.RequestID(ctx) != "admission" {
					t.Errorf("dispatch lost request identity: %s", pullrequestlifecycle.RequestID(ctx))
				}
				launches.Add(1)
				launched <- w
				return "session", nil
			}), nil, conflictedRebaser{})
			requestCtx, cancelRequest := context.WithCancel(t.Context())
			admitted, err := service.Start(requestCtx, pullrequestwork.Start{PullRequestID: "pr", Kind: kind, CommentIDs: []string{"comment"}, RequestID: "admission"})
			cancelRequest()
			if err != nil || len(admitted) != 1 || admitted[0].Status != pullrequestwork.StatusQueued || admitted[0].SessionID != "" || preparations.Load() != 0 {
				t.Fatalf("admission=%+v err=%v preparations=%d", admitted, err, preparations.Load())
			}
			persisted, err := store.Get(t.Context(), admitted[0].ID)
			if err != nil || persisted.Status != pullrequestwork.StatusQueued {
				t.Fatalf("persisted=%+v err=%v", persisted, err)
			}
			// Repeated public reconciliations coalesce notifications before the loop starts.
			for range 10 {
				if err := service.Reconcile(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			stop := runDispatcher(t, service)
			awaitDispatch(t, entered)
			close(release)
			if got := awaitDispatch(t, launched); got.ID != admitted[0].ID {
				t.Fatalf("launched=%+v", got)
			}
			if err := service.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			stop()
			for range 3 {
				if err := service.Dispatch(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if launches.Load() != 1 || preparations.Load() != 1 {
				t.Fatalf("duplicate dispatch: preparations=%d launches=%d", preparations.Load(), launches.Load())
			}
		})
	}
}

func TestDispatcherScansPersistedWorkAndRetriesPreparationOnTick(t *testing.T) {
	_, store := mixedQueueStore(t)
	admitted := newTestWorkService(store, dispatchCatalog{}, nil, nil)
	jobs, err := admitted.Start(t.Context(), pullrequestwork.Start{PullRequestID: "pr", Kind: pullrequestwork.KindWorker, CommentIDs: []string{"comment"}})
	if err != nil {
		t.Fatal(err)
	}
	// A new service has none of the admitting service's notifications.
	attempts, launched := make(chan int32, 4), make(chan pullrequestwork.Work, 1)
	var count atomic.Int32
	catalog := dispatchCatalog{prepare: func(context.Context, string) error {
		n := count.Add(1)
		attempts <- n
		if n == 1 {
			return errors.New("temporarily unavailable")
		}
		return nil
	}}
	launcher := recoveryLaunchFunc(func(_ context.Context, _ pullrequestwork.PullRequest, w pullrequestwork.Work, _ string) (string, error) {
		launched <- w
		return "session", nil
	})
	recovered := newTestWorkService(store, catalog, launcher, nil)
	if err := recovered.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 0 {
		t.Fatal("recovery prepared queued work")
	}
	// Another fresh service has neither admission nor recovery's wake.
	restarted := newTestWorkService(store, catalog, launcher, nil)
	stop := runDispatcher(t, restarted)
	if got := awaitDispatch(t, attempts); got != 1 {
		t.Fatalf("attempt=%d", got)
	}
	queued, err := store.Get(t.Context(), jobs[0].ID)
	if err != nil || queued.Status != pullrequestwork.StatusQueued {
		t.Fatalf("failed preparation lost queue: %+v %v", queued, err)
	}
	if got := awaitDispatch(t, attempts); got != 2 {
		t.Fatalf("retry=%d", got)
	}
	if got := awaitDispatch(t, launched); got.ID != jobs[0].ID {
		t.Fatalf("launched=%+v", got)
	}
	if err := restarted.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	stop()
	if count.Load() != 2 {
		t.Fatalf("preparations=%d", count.Load())
	}
}

func TestDispatchChecksOtherPullRequestsAfterPreparationFailure(t *testing.T) {
	_, store := mixedQueueStore(t)
	failure := errors.New("fetch unavailable")
	launcher := &queueLauncher{}
	service := newTestWorkService(store, dispatchCatalog{prepare: func(_ context.Context, id string) error {
		if id == "blocked" {
			return failure
		}
		return nil
	}}, launcher, nil)
	for _, id := range []string{"blocked", "ready"} {
		if _, err := service.Start(t.Context(), pullrequestwork.Start{PullRequestID: id, Kind: pullrequestwork.KindWorker, CommentIDs: []string{"comment"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.Dispatch(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("dispatch=%v", err)
	}
	if len(launcher.works) != 1 || launcher.works[0].PullRequestID != "ready" {
		t.Fatalf("launches=%+v", launcher.works)
	}
}

func TestDispatchIsolatesPublicationCheckpointFailure(t *testing.T) {
	ctx := t.Context()
	_, store := mixedQueueStore(t)
	// A verified publication with a missing result cannot be finalized and
	// must keep ownership of A's queue until its checkpoint is repaired.
	checkpoint := pullrequestwork.Work{
		ID: "published-a", PullRequestID: "a", Kind: pullrequestwork.KindRebase,
		Status: pullrequestwork.StatusRunning, HeadCommit: "head", PublicationState: "published",
	}
	if err := store.CreateBatch(ctx, []pullrequestwork.Work{
		checkpoint,
		{ID: "next-a", PullRequestID: "a", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, CommentID: "comment-a", Status: pullrequestwork.StatusQueued},
		{ID: "next-b", PullRequestID: "b", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, CommentID: "comment-b", Status: pullrequestwork.StatusQueued},
	}); err != nil {
		t.Fatal(err)
	}
	launcher := &queueLauncher{}
	service := newTestWorkService(store, dispatchCatalog{}, launcher, nil)
	if err := service.Dispatch(ctx); !errors.Is(err, pullrequestwork.ErrPublish) || !strings.Contains(err.Error(), "pull request a") {
		t.Fatalf("dispatch lost A's checkpoint error: %v", err)
	}
	published, err := store.Get(ctx, checkpoint.ID)
	if err != nil || published.Status != pullrequestwork.StatusRunning || published.PublicationState != "published" || published.ResultHeadCommit != "" || published.CompletedAt != nil {
		t.Fatalf("failed checkpoint changed: %+v, %v", published, err)
	}
	nextA, err := store.Get(ctx, "next-a")
	if err != nil || nextA.Status != pullrequestwork.StatusQueued || nextA.SessionID != "" {
		t.Fatalf("A advanced past its failed checkpoint: %+v, %v", nextA, err)
	}
	nextB, err := store.Get(ctx, "next-b")
	if err != nil || nextB.Status != pullrequestwork.StatusRunning || nextB.SessionID == "" {
		t.Fatalf("B did not start: %+v, %v", nextB, err)
	}
	if len(launcher.works) != 1 || launcher.works[0].ID != nextB.ID {
		t.Fatalf("launches after checkpoint failure: %+v", launcher.works)
	}

	checkpoint.ResultHeadCommit = "published-head"
	if err := store.Update(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := service.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	published, err = store.Get(ctx, checkpoint.ID)
	if err != nil || published.Status != pullrequestwork.StatusCompleted || published.ResultHeadCommit != checkpoint.ResultHeadCommit || published.CompletedAt == nil {
		t.Fatalf("repaired checkpoint did not complete: %+v, %v", published, err)
	}
	nextA, err = store.Get(ctx, nextA.ID)
	if err != nil || nextA.Status != pullrequestwork.StatusRunning || nextA.SessionID == "" {
		t.Fatalf("A did not advance after checkpoint repair: %+v, %v", nextA, err)
	}
	if len(launcher.works) != 2 || launcher.works[1].ID != nextA.ID {
		t.Fatalf("A did not launch exactly once or B launched twice: %+v", launcher.works)
	}
}

type heldDispatchScan struct {
	pullrequestwork.Store
	scans            atomic.Int32
	entered, release chan struct{}
}

func (s *heldDispatchScan) List(ctx context.Context, pr string) ([]pullrequestwork.Work, error) {
	work, err := s.Store.List(ctx, pr)
	// Hold the first pass's queue scan after it has read an empty snapshot.
	if pr == "" && s.scans.Add(1) == 1 {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return work, err
}
func TestDispatcherConsumesAdmissionWakeAfterEmptyScan(t *testing.T) {
	_, store := mixedQueueStore(t)
	scan := &heldDispatchScan{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	launched := make(chan pullrequestwork.Work, 1)
	service := newTestWorkService(scan, dispatchCatalog{}, recoveryLaunchFunc(func(_ context.Context, _ pullrequestwork.PullRequest, w pullrequestwork.Work, _ string) (string, error) {
		launched <- w
		return "session", nil
	}), nil)
	stop := runDispatcher(t, service)
	awaitDispatch(t, scan.entered)
	work, err := service.Start(t.Context(), pullrequestwork.Start{PullRequestID: "pr", Kind: pullrequestwork.KindWorker, CommentIDs: []string{"comment"}})
	if err != nil {
		t.Fatal(err)
	}
	close(scan.release)
	// Bound the wait below the periodic interval: this pass requires the wake
	// sent during admission after the first scan captured its empty snapshot.
	select {
	case got := <-launched:
		if got.ID != work[0].ID {
			t.Fatalf("launch=%+v", got)
		}
	case <-time.After(750 * time.Millisecond):
		t.Fatal("admission wake was lost")
	}
	if err := service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	stop()
}
