package pullrequestlifecycle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	githubprovider "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

type retirementFunc func(context.Context, string) error

func (f retirementFunc) RetireInactive(ctx context.Context, id string) error { return f(ctx, id) }

type failedAcceptedRegistry struct{ pr.Registry }

func (r failedAcceptedRegistry) UpsertObservedPullRequests(id string, incoming []pr.PullRequest, token pr.ObservationToken) (int, int, error) {
	imported, updated, err := r.Registry.UpsertObservedPullRequests(id, incoming, token)
	if err == nil {
		err = errors.New("failure after accepting lifecycle")
	}
	return imported, updated, err
}

func TestBackgroundRetirementRetriesAcceptedStateAfterFailedReconciliation(t *testing.T) {
	f, g := publicationFixture(t)
	g.pullRequests[0].Status = pr.StatusClosed
	attempts := make(chan string, 4)
	release := make(chan struct{})
	defer close(release)
	c := pr.New(failedAcceptedRegistry{f.store}, pr.Options{
		Refresh: f.scheduler, GitHubTransport: g, GitHubCodec: githubprovider.New(nil),
		Projects: pr.ProjectLookupFunc(func(id string) (pr.Project, bool) {
			return pr.Project{ID: "project", RepositoryURL: f.remote, GitHubBacked: true}, id == "project"
		}),
		Retirement: retirementFunc(func(ctx context.Context, id string) error {
			attempts <- id
			select {
			case <-release:
			case <-ctx.Done():
			}
			return errors.New("cleanup temporarily unavailable")
		}),
	})
	for range 2 {
		// Failed reconciliation still schedules cleanup, without waiting for it.
		if _, err := c.SyncActive(t.Context(), "project"); err == nil {
			t.Fatal("expected partial failure")
		}
		accepted, _ := f.store.GetPullRequest(f.pull.ID)
		if accepted.Status != pr.StatusClosed {
			t.Fatalf("accepted lifecycle lost: %+v", accepted)
		}
		select {
		case id := <-attempts:
			if id != f.pull.ID {
				t.Fatalf("retired %s", id)
			}
		case <-time.After(time.Second):
			t.Fatal("cleanup not scheduled")
		}
		// Release the failing attempt; the subsequent pass must retry it.
		release <- struct{}{}
	}
}

func TestBackgroundStartupDiscoversWithoutPageRequests(t *testing.T) {
	f, g := publicationFixture(t)
	g.pullRequests = append(g.pullRequests, pr.GitHubPullRequest{ExternalID: "github:owner/repo#2", Title: "Discovered at startup", Status: pr.StatusOpen, BaseBranch: "main", HeadBranch: "feature", BaseCommit: f.base, HeadCommit: f.head, SyncData: g.pullRequests[0].SyncData})
	c := publicationCoordinator(f, g, f.repo)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); c.RunBasic(ctx, "project") }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range f.store.ListPullRequests("project") {
			if p.Title == "Discovered at startup" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("startup discovery waited for routine cadence or a page request")
}

func TestFailedGitHubObservationDoesNotRetireActiveWork(t *testing.T) {
	f, g := publicationFixture(t)
	g.pullRequests[0].Status = pr.StatusClosed
	g.get = func(int32) error { return errors.New("GitHub unavailable") }
	c := publicationCoordinator(f, g, f.repo)
	retired := make(chan string, 1)
	c.SetRetirement(retirementFunc(func(_ context.Context, id string) error { retired <- id; return nil }))
	if _, err := c.SyncActive(t.Context(), "project"); err == nil {
		t.Fatal("expected provider failure")
	}
	// Drain the scheduled cleanup before checking that nothing was retired.
	if err := f.scheduler.Do(t.Context(), pr.RefreshKey{RepositoryID: "project", Section: "retirement"}, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-retired:
		t.Fatalf("failed observation retired %s", id)
	default:
	}
	accepted, _ := f.store.GetPullRequest(f.pull.ID)
	if !accepted.Status.Active() {
		t.Fatalf("failed observation changed lifecycle: %+v", accepted)
	}
}
