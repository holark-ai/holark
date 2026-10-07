package pullrequestlifecycle_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

func TestRefreshCoordinatorRetainsActionFollowupAndCoalescesDuplicates(t *testing.T) {
	coordinator := pullrequestlifecycle.NewRefreshCoordinator()
	defer coordinator.Close()
	key := pullrequestlifecycle.RefreshKey{RepositoryID: "repository", PullRequestID: "pr", Section: "basic"}
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	coordinator.Trigger(t.Context(), key, func(context.Context) error { calls.Add(1); close(started); <-release; return nil })
	<-started
	// These requests represent an action's completion and another foreground
	// refresh. Both must survive an already running, obsolete observation.
	coordinator.Trigger(t.Context(), key, func(context.Context) error { t.Error("superseded pending work ran"); return nil })
	coordinator.Trigger(t.Context(), key, func(context.Context) error { calls.Add(1); close(finished); return nil })
	close(release)
	<-finished
	coordinator.Close()
	if got := calls.Load(); got != 2 {
		t.Fatalf("refresh calls = %d, want one old request and one follow-up", got)
	}
}

func TestRefreshCoordinatorCallerCancellationKeepsRegisteredWorkUntilShutdown(t *testing.T) {
	coordinator := pullrequestlifecycle.NewRefreshCoordinator()
	started, finished := make(chan struct{}), make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		finished <- coordinator.Do(ctx, pullrequestlifecycle.RefreshKey{RepositoryID: "repository", Section: "basic"}, func(workCtx context.Context) error { close(started); <-workCtx.Done(); return workCtx.Err() })
	}()
	<-started
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller result = %v", err)
	}
	coordinator.Close()
	if err := coordinator.Do(t.Context(), pullrequestlifecycle.RefreshKey{}, func(context.Context) error { t.Error("work ran after close"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed result = %v", err)
	}
}

func TestRefreshCoordinatorLimitsRepositoryRemoteWork(t *testing.T) {
	coordinator := pullrequestlifecycle.NewRefreshCoordinator()
	defer coordinator.Close()
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	finished := make(chan struct{}, 8)
	var running, maximum atomic.Int32
	work := func(context.Context) error {
		now := running.Add(1)
		for old := maximum.Load(); now > old && !maximum.CompareAndSwap(old, now); old = maximum.Load() {
		}
		started <- struct{}{}
		<-release
		running.Add(-1)
		finished <- struct{}{}
		return nil
	}
	for _, section := range []string{"basic", "checks", "comments", "diff", "commits", "participants"} {
		coordinator.Trigger(t.Context(), pullrequestlifecycle.RefreshKey{RepositoryID: "repository", Section: section}, work)
	}
	<-started
	<-started
	close(release)
	for range 6 {
		<-finished
	}
	coordinator.Close()
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum repository concurrency = %d, want 2", got)
	}
}
