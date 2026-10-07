package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	githubprs "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	"github.com/holark-ai/holark/internal/githubidentity"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestparticipants"
)

type listingResponse struct {
	items []githubapi.PullRequest
	err   error
}
type listingRequest struct {
	active bool
	reply  chan listingResponse
}
type controlledListing struct {
	githubprs.APIClient
	requests chan listingRequest
}

func (c *controlledListing) list(ctx context.Context, active bool) ([]githubapi.PullRequest, error) {
	request := listingRequest{active: active, reply: make(chan listingResponse, 1)}
	select {
	case c.requests <- request:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-request.reply:
		return r.items, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (c *controlledListing) ListPullRequests(ctx context.Context, _ githubapi.Repository) ([]githubapi.PullRequest, error) {
	return c.list(ctx, false)
}
func (c *controlledListing) ListActivePullRequests(ctx context.Context, _ githubapi.Repository) ([]githubapi.PullRequest, error) {
	return c.list(ctx, true)
}
func nextListing(t *testing.T, c *controlledListing) listingRequest {
	t.Helper()
	select {
	case r := <-c.requests:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("listing did not start")
		return listingRequest{}
	}
}
func participantPR(number int, assigned bool) githubapi.PullRequest {
	now := time.Now().UTC()
	item := githubapi.PullRequest{Number: number, NodeID: fmt.Sprint("PR_", number), Title: fmt.Sprint("PR ", number), State: "open", CreatedAt: now, UpdatedAt: now, Base: githubapi.Ref{Ref: "main", SHA: "base"}, Head: githubapi.Ref{Ref: fmt.Sprint("topic-", number), SHA: "head"}, User: githubapi.User{NodeID: "U_me", Login: "Alice"}, Assignees: []githubapi.User{}, RequestedReviewers: []githubapi.User{}}
	if assigned {
		item.Assignees = []githubapi.User{{NodeID: "U_me", Login: "Alice"}}
	}
	return item
}
func overlapCoordinator(t *testing.T, f *fixture, registry pr.Registry, c *controlledListing, participantProvider ...pullrequestparticipants.Provider) (*pr.Coordinator, *atomic.Int32, *pullrequestparticipants.Service) {
	t.Helper()
	provider := githubprs.New(c)
	var configured pullrequestparticipants.Provider
	if len(participantProvider) > 0 {
		configured = participantProvider[0]
	}
	participants := pullrequestparticipants.NewService(f.participants, catalogTargets{f}, githubidentity.NewService(f.members, nil), configured)
	refreshes := &atomic.Int32{}
	participants.SetRefreshRequester(func(context.Context, string) { refreshes.Add(1) })
	scheduler := pr.NewRefreshCoordinator()
	t.Cleanup(scheduler.Close)
	return pr.New(registry, pr.Options{Refresh: scheduler, Projects: pr.ProjectLookupFunc(func(id string) (pr.Project, bool) {
		return pr.Project{ID: "repo", RepositoryURL: "https://github.com/owner/repo", GitHubBacked: true}, id == "repo"
	}), GitHubTransport: provider, GitHubCodec: provider, Participants: participants, SyncRecorder: f.work}), refreshes, participants
}
func TestOverlappingBulkParticipantImports(t *testing.T) {
	for _, activeFirst := range []bool{false, true} {
		for _, newerFinishesFirst := range []bool{false, true} {
			for _, partial := range []bool{false, true} {
				t.Run(fmt.Sprintf("activeFirst=%t/newerFinishesFirst=%t/partial=%t", activeFirst, newerFinishesFirst, partial), func(t *testing.T) {
					f := setup(t)
					client := &controlledListing{requests: make(chan listingRequest)}
					coordinator, refreshes, _ := overlapCoordinator(t, f, f.catalog, client)
					invoke := func(active bool) <-chan error {
						done := make(chan error, 1)
						go func() {
							var err error
							if active {
								_, err = coordinator.SyncActive(t.Context(), "repo")
							} else {
								_, err = coordinator.Sync(t.Context(), "repo")
							}
							done <- err
						}()
						return done
					}
					oldDone := invoke(activeFirst)
					old := nextListing(t, client)
					newDone := invoke(!activeFirst)
					newer := nextListing(t, client)
					oldItems := []githubapi.PullRequest{participantPR(42, true)}
					if partial {
						incomplete := participantPR(43, true)
						incomplete.RequestedReviewers = nil
						oldItems = append(oldItems, incomplete)
					}
					finishOld := func() {
						old.reply <- listingResponse{items: oldItems}
						err := <-oldDone
						if partial {
							if !errors.Is(err, pullrequestparticipants.ErrIncompleteObservation) || errors.Is(err, pullrequestparticipants.ErrStaleObservation) {
								t.Fatalf("lost genuine failure or retained stale error: %v", err)
							}
						} else if err != nil {
							t.Fatal(err)
						}
					}
					finishNew := func() {
						newer.reply <- listingResponse{items: []githubapi.PullRequest{participantPR(42, false)}}
						if err := <-newDone; err != nil {
							t.Fatal(err)
						}
					}
					if newerFinishesFirst {
						finishNew()
						finishOld()
					} else {
						finishOld()
						finishNew()
					}
					for _, item := range f.catalog.ListPullRequests("repo") {
						if item.SyncExternalID == "github:owner/repo#42" {
							snapshot, err := f.participants.Get(t.Context(), item.ID)
							if err != nil || len(snapshot.AssigneeHolarkIDs) != 0 {
								t.Fatalf("newest participants lost: %+v %v", snapshot, err)
							}
						}
					}
					if refreshes.Load() != 0 {
						t.Fatalf("redundant participant fetch requests: %d", refreshes.Load())
					}
					result := f.get(t, "/api/v1/pull-requests/search")
					section := "history"
					if activeFirst {
						section = "pull_requests"
					}
					found := false
					for _, state := range result.Sync {
						if state.Section == section && state.Error != "" {
							t.Fatalf("participant error contaminated catalog: %+v", state)
						}
						if state.Section == section+"_participants" {
							found = true
							if partial != strings.Contains(state.Error, "participant metadata is incomplete") {
								t.Fatalf("participant status: %+v", state)
							}
						}
					}
					if !found {
						t.Fatal("participant metadata absent")
					}
				})
			}
		}
	}
}

type recoveryRegistry struct {
	pr.Registry
	recovered chan struct{}
}

func (r recoveryRegistry) ListRecoverableOperations(ctx context.Context) ([]pr.Operation, error) {
	select {
	case r.recovered <- struct{}{}:
	default:
	}
	return r.Registry.ListRecoverableOperations(ctx)
}
func TestParticipantFailureDoesNotDelayCatalogOrSkipRecovery(t *testing.T) {
	f := setup(t)
	client := &controlledListing{requests: make(chan listingRequest)}
	registry := recoveryRegistry{Registry: f.catalog, recovered: make(chan struct{}, 4)}
	coordinator, refreshes, _ := overlapCoordinator(t, f, registry, client)
	done := make(chan error, 1)
	go func() { _, err := coordinator.SyncActive(t.Context(), "repo"); done <- err }()
	incomplete := participantPR(42, true)
	incomplete.Assignees = nil
	request := nextListing(t, client)
	if !request.active {
		t.Fatal("background history import")
	}
	request.reply <- listingResponse{items: []githubapi.PullRequest{incomplete}}
	if err := <-done; err == nil {
		t.Fatal("incomplete participants were accepted as complete")
	}
	if err := coordinator.RecoverOperations(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-registry.recovered:
	case <-time.After(time.Second):
		t.Fatal("participant failure prevented lifecycle recovery")
	}
	if refreshes.Load() != 0 {
		t.Fatal("bulk failure scheduled per-PR refresh")
	}
	result := f.get(t, "/api/v1/pull-requests/search")
	found := false
	for _, s := range result.Sync {
		if s.Section == "pull_requests" && s.Error != "" {
			t.Fatal(s)
		}
		if s.Section == "pull_requests_participants" && s.Error != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("participant failure not observable")
	}
}

type participantMutationSource struct {
	pullrequestparticipants.Provider
	err   error
	calls int
}

func (*participantMutationSource) RemoveAssignee(context.Context, pullrequestparticipants.ProviderTarget, string) error {
	return nil
}
func (s *participantMutationSource) GetSnapshot(context.Context, pullrequestparticipants.ProviderTarget) (pullrequestparticipants.RemoteParticipantSnapshot, error) {
	s.calls++
	return pullrequestparticipants.RemoteParticipantSnapshot{Complete: true}, s.err
}

func TestBulkImportOverlappingParticipantEditRetainsEditAndReconciliation(t *testing.T) {
	f := setup(t)
	client := &controlledListing{requests: make(chan listingRequest)}
	participantProvider := &participantMutationSource{}
	coordinator, refreshes, participants := overlapCoordinator(t, f, f.catalog, client, participantProvider)
	done := make(chan error, 1)
	go func() { _, err := coordinator.Sync(t.Context(), "repo"); done <- err }()
	initial := nextListing(t, client)
	initial.reply <- listingResponse{items: []githubapi.PullRequest{participantPR(42, true)}}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	id := f.catalog.ListPullRequests("repo")[0].ID
	go func() { _, err := coordinator.Sync(t.Context(), "repo"); done <- err }()
	stale := nextListing(t, client)
	if _, err := participants.RemoveAssignee(t.Context(), id, f.me); err != nil {
		t.Fatal(err)
	}
	stale.reply <- listingResponse{items: []githubapi.PullRequest{participantPR(42, true)}}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	snapshot, err := participants.Get(t.Context(), id)
	if err != nil || len(snapshot.AssigneeHolarkIDs) != 0 || refreshes.Load() != 1 {
		t.Fatalf("edit lost or reconciliation absent: %+v %v refreshes=%d", snapshot, err, refreshes.Load())
	}
	participantProvider.err = errors.New("offline")
	if _, err := participants.Reconcile(t.Context(), id); err == nil {
		t.Fatal("reconciliation failure lost")
	}
	participantProvider.err = nil
	if _, err := participants.Reconcile(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if participantProvider.calls != 2 {
		t.Fatal(participantProvider.calls)
	}
}

func TestHistoryParticipantFailuresRemainUntilExplicitHistoryRetry(t *testing.T) {
	f := setup(t)
	client := &controlledListing{requests: make(chan listingRequest)}
	coordinator, refreshes, _ := overlapCoordinator(t, f, f.catalog, client)
	history := participantPR(42, true)
	history.State = "closed"
	history.Assignees = nil
	done := make(chan error, 1)
	go func() { _, err := coordinator.Sync(t.Context(), "repo"); done <- err }()
	request := nextListing(t, client)
	request.reply <- listingResponse{items: []githubapi.PullRequest{history}}
	if err := <-done; !errors.Is(err, pullrequestparticipants.ErrIncompleteObservation) {
		t.Fatal(err)
	}
	go func() { _, err := coordinator.SyncActive(t.Context(), "repo"); done <- err }()
	request = nextListing(t, client)
	request.reply <- listingResponse{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	failed := false
	for _, state := range f.get(t, "/api/v1/pull-requests/search").Sync {
		if state.Section == "history_participants" {
			failed = state.Error != ""
		}
	}
	if !failed || refreshes.Load() != 0 {
		t.Fatal("history failure lost or triggered per-PR fan-out")
	}
	history.Assignees = []githubapi.User{}
	go func() { _, err := coordinator.Sync(t.Context(), "repo"); done <- err }()
	request = nextListing(t, client)
	request.reply <- listingResponse{items: []githubapi.PullRequest{history}}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, state := range f.get(t, "/api/v1/pull-requests/search").Sync {
		if state.Section == "history_participants" && (state.Error != "" || state.SyncedAt == "") {
			t.Fatal(state)
		}
	}
}
