package pullrequestlifecycle_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/repository"
)

func TestPollingBatchesGitBranchObservation(t *testing.T) {
	f, github := publicationFixture(t)
	observed := make(chan map[string]repository.BranchObservation, 1)
	cached := make(chan string, 2)
	repo := observedCacheRepository{
		syncRepository: f.repo,
		afterObserve:   func(values map[string]repository.BranchObservation) { observed <- values },
		afterCache:     func(ref string, err error) { cached <- ref },
	}
	if _, err := publicationCoordinator(f, github, repo).SyncActive(t.Context(), "project"); err != nil {
		t.Fatal(err)
	}
	select {
	case values := <-observed:
		if len(values) != 2 || !values[f.pull.BaseRef.Ref].Exists || !values[f.pull.HeadRef.Ref].Exists {
			t.Fatalf("batched observation = %+v", values)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("branch observation did not run")
	}
	for range 2 {
		<-cached
	}
	assertCached(t, f.root, cacheRef("main"), f.base)
	assertCached(t, f.root, cacheRef("feature"), f.head)
}

func TestPollingAcceptsMissingRefsButPreservesCacheOnTransportFailure(t *testing.T) {
	t.Run("missing ref", func(t *testing.T) {
		f, github := publicationFixture(t)
		gitSync(t, f.remote, "update-ref", "-d", "refs/heads/feature")
		cached := make(chan string, 2)
		repo := observedCacheRepository{syncRepository: f.repo, afterCache: func(ref string, err error) { cached <- ref }}
		if _, err := publicationCoordinator(f, github, repo).SyncActive(t.Context(), "project"); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			<-cached
		}
		assertMissing(t, f.root, cacheRef("feature"))
		current, _ := f.store.GetPullRequest(f.pull.ID)
		if current.HasCurrentComparison() {
			t.Fatal("missing head retained current comparison")
		}
	})

	t.Run("transport failure", func(t *testing.T) {
		f, github := publicationFixture(t)
		attempted := make(chan struct{})
		repo := observedCacheRepository{
			syncRepository: f.repo,
			observe: func(context.Context, string, []string) (map[string]repository.BranchObservation, error) {
				close(attempted)
				return nil, errors.New("git transport failed")
			},
			afterCache: func(string, error) { t.Error("failed observation changed the branch cache") },
		}
		if _, err := publicationCoordinator(f, github, repo).SyncActive(t.Context(), "project"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-attempted:
		case <-time.After(5 * time.Second):
			t.Fatal("branch observation did not run")
		}
		assertCached(t, f.root, cacheRef("main"), f.base)
		assertCached(t, f.root, cacheRef("feature"), f.head)
	})
}

func TestPollingCoalescedRefreshObservesAgain(t *testing.T) {
	f, github := publicationFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	completed := make(chan struct{}, 2)
	cached := make(chan string, 4)
	var observations atomic.Int32
	repo := observedCacheRepository{
		syncRepository: f.repo,
		afterObserve: func(map[string]repository.BranchObservation) {
			if observations.Add(1) == 1 {
				close(entered)
				<-release
			}
			completed <- struct{}{}
		},
		afterCache: func(ref string, err error) { cached <- ref },
	}
	c := publicationCoordinator(f, github, repo)
	if _, err := c.SyncActive(t.Context(), "project"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first observation did not start")
	}
	for range 2 {
		if _, err := c.SyncActive(t.Context(), "project"); err != nil {
			t.Fatal(err)
		}
	}
	newer := f.advance(t)
	releaseOnce.Do(func() { close(release) })
	for range 2 {
		select {
		case <-completed:
		case <-time.After(5 * time.Second):
			t.Fatal("coalesced observation did not finish")
		}
	}
	for range 4 {
		<-cached
	}
	if observations.Load() != 2 {
		t.Fatalf("observations = %d", observations.Load())
	}
	assertCached(t, f.root, cacheRef("feature"), newer)
}

func TestFullSyncStillUsesGitBranchObservation(t *testing.T) {
	f, github := publicationFixture(t)
	var observations atomic.Int32
	repo := observedCacheRepository{syncRepository: f.repo, afterObserve: func(map[string]repository.BranchObservation) { observations.Add(1) }}
	if err := publicationCoordinator(f, github, repo).SyncPullRequest(t.Context(), f.pull.ID); err != nil {
		t.Fatal(err)
	}
	if observations.Load() == 0 {
		t.Fatal("full sync skipped Git observation")
	}
}
