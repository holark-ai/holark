//go:build integration

package pullrequestparticipants_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/githubidentity"
	githubidentitystore "github.com/holark-ai/holark/internal/githubidentity/storeadapter"
	"github.com/holark-ai/holark/internal/pullrequestparticipants"
	participantstore "github.com/holark-ai/holark/internal/pullrequestparticipants/sqliteadapter"
)

func TestServiceLogsOneFlatSyncFailureEvent(t *testing.T) {
	_, store, provider, targets := newIntegratedService(t)
	provider.getErr = errors.New("transport failed")
	var output bytes.Buffer
	service := pullrequestparticipants.NewService(store, targets, nil, provider,
		pullrequestparticipants.WithLogger(slog.New(slog.NewJSONHandler(&output, nil))))
	_, _ = service.Sync(t.Context(), "pr-1")
	logged := output.String()
	for _, field := range []string{`"code":"github_sync_failed"`, `"repository_id":"project-1"`, `"pull_request_id":"pr-1"`, `"provider":"github"`, `"repository":"https://github.com/acme/widgets"`, `"provider_pull_request":4`, `"stage":"provider_fetch"`, `"cause":`} {
		if !strings.Contains(logged, field) {
			t.Fatalf("log missing %s: %s", field, logged)
		}
	}
}

func TestServiceSynchronizesAuthoritativeOrderedSnapshotWithRealIdentityStorage(t *testing.T) {
	service, store, provider, _ := newIntegratedService(t)
	provider.snapshot = pullrequestparticipants.RemoteParticipantSnapshot{Complete: true,
		AssigneeGitHubNodeIDs:          []string{"node-b", "node-a", "node-b"},
		RequestedReviewerGitHubNodeIDs: []string{"node-c", "node-a"},
	}
	got, err := service.Sync(t.Context(), "pr-1")
	if err != nil {
		t.Fatal(err)
	}
	want := pullrequestparticipants.Snapshot{PullRequestID: "pr-1", AssigneeHolarkIDs: []string{"member-b", "member-a"}, RequestedReviewerHolarkIDs: []string{"member-c", "member-a"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot = %#v, want %#v", got, want)
	}
	provider.snapshot = pullrequestparticipants.RemoteParticipantSnapshot{Complete: true, AssigneeGitHubNodeIDs: []string{"node-a"}, RequestedReviewerGitHubNodeIDs: []string{}}
	if _, err := service.Sync(t.Context(), "pr-1"); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Get(t.Context(), "pr-1")
	if !reflect.DeepEqual(got.AssigneeHolarkIDs, []string{"member-a"}) || len(got.RequestedReviewerHolarkIDs) != 0 {
		t.Fatalf("authoritative snapshot = %#v", got)
	}
}

func TestServiceKeepsLocalCollectionsWhenProviderOrResolutionFails(t *testing.T) {
	service, store, provider, _ := newIntegratedService(t)
	before := pullrequestparticipants.Snapshot{PullRequestID: "pr-1", AssigneeHolarkIDs: []string{"member-a"}, RequestedReviewerHolarkIDs: []string{"member-b"}}
	if _, err := store.ReplaceSnapshot(t.Context(), before); err != nil {
		t.Fatal(err)
	}
	provider.replaceAssigneesErr = errors.New("provider rejected")
	if _, err := service.ReplaceAssignees(t.Context(), "pr-1", []string{"member-c"}); !errors.Is(err, pullrequestparticipants.ErrProviderFailed) {
		t.Fatalf("replace error = %v", err)
	}
	assertStoredSnapshot(t, store, before)

	provider.replaceAssigneesErr = nil
	provider.snapshot = pullrequestparticipants.RemoteParticipantSnapshot{Complete: true, AssigneeGitHubNodeIDs: []string{"unknown-node"}}
	_, err := service.Sync(t.Context(), "pr-1")
	var syncError *pullrequestparticipants.SyncError
	if !errors.As(err, &syncError) || syncError.PullRequestID != "pr-1" || syncError.Stage != pullrequestparticipants.SyncStageIdentityResolution {
		t.Fatalf("sync error = %#v", err)
	}
	assertStoredSnapshot(t, store, before)
}

func newIntegratedService(t *testing.T) (*pullrequestparticipants.Service, *participantstore.Store, *fakeProvider, *fakeTargets) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(filepath.Join(t.TempDir(), "service.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `
create table repositories (id text primary key);
create table pull_requests (id text primary key, repository_id text not null references repositories(id) on delete cascade);
insert into repositories(id) values ('project-1');
insert into pull_requests(id, repository_id) values ('pr-1', 'project-1');`); err != nil {
		t.Fatal(err)
	}
	participants, err := participantstore.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	identities, err := githubidentitystore.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	members := []githubidentity.StoredMember{
		{Member: githubidentity.Member{ID: "member-a", Login: "alice", Permission: "push"}, NodeID: "node-a", CreatedAt: now, UpdatedAt: now, LastSeenAt: now},
		{Member: githubidentity.Member{ID: "member-b", Login: "bob", Permission: "push"}, NodeID: "node-b", CreatedAt: now, UpdatedAt: now, LastSeenAt: now},
		{Member: githubidentity.Member{ID: "member-c", Login: "carol", Permission: "push"}, NodeID: "node-c", CreatedAt: now, UpdatedAt: now, LastSeenAt: now},
	}
	if _, err := identities.SyncProjectMembers(ctx, "project-1", members, now); err != nil {
		t.Fatal(err)
	}
	targets := &fakeTargets{target: pullrequestparticipants.PullRequestTarget{ID: "pr-1", RepositoryID: "project-1", Status: pullrequestparticipants.PullRequestOpen, SyncProvider: "github", RepositoryURL: "https://github.com/acme/widgets", ProviderPullRequest: 4}}
	provider := &fakeProvider{}
	return pullrequestparticipants.NewService(participants, targets, githubidentity.NewService(identities, nil), provider), participants, provider, targets
}

func assertStoredSnapshot(t *testing.T, store *participantstore.Store, want pullrequestparticipants.Snapshot) {
	t.Helper()
	got, err := store.Get(t.Context(), want.PullRequestID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stored snapshot = %#v, want %#v", got, want)
	}
}

type fakeTargets struct {
	target pullrequestparticipants.PullRequestTarget
}

func (targets *fakeTargets) GetParticipantTarget(context.Context, string) (pullrequestparticipants.PullRequestTarget, error) {
	return targets.target, nil
}

type fakeProvider struct {
	snapshot            pullrequestparticipants.RemoteParticipantSnapshot
	getErr              error
	getCalls            int
	replaceAssigneesErr error
}

func (provider *fakeProvider) GetSnapshot(context.Context, pullrequestparticipants.ProviderTarget) (pullrequestparticipants.RemoteParticipantSnapshot, error) {
	provider.getCalls++
	return provider.snapshot, provider.getErr
}
func (provider *fakeProvider) ReplaceAssignees(context.Context, pullrequestparticipants.ProviderTarget, []string) error {
	return provider.replaceAssigneesErr
}
func (provider *fakeProvider) AddAssignee(context.Context, pullrequestparticipants.ProviderTarget, string) error {
	return nil
}
func (provider *fakeProvider) RemoveAssignee(context.Context, pullrequestparticipants.ProviderTarget, string) error {
	return nil
}
func (provider *fakeProvider) ReplaceRequestedReviewers(context.Context, pullrequestparticipants.ProviderTarget, []string) error {
	return nil
}
func (provider *fakeProvider) AddRequestedReviewer(context.Context, pullrequestparticipants.ProviderTarget, string) error {
	return nil
}
func (provider *fakeProvider) RemoveRequestedReviewer(context.Context, pullrequestparticipants.ProviderTarget, string) error {
	return nil
}

func TestBulkObservationsPreserveIncompleteCacheRejectRacesAndObserveNewUsers(t *testing.T) {
	service, store, _, _ := newIntegratedService(t)
	remote := pullrequestparticipants.RemoteParticipantSnapshot{Complete: true, AssigneeGitHubNodeIDs: []string{"new-user"}, RequestedReviewerGitHubNodeIDs: []string{"new-user"}, Author: &githubidentity.SourceMember{NodeID: "author", Login: "author"}, Members: []githubidentity.SourceMember{{NodeID: "new-user", Login: "contributor"}}}
	if err := service.ApplyObservation(t.Context(), "pr-1", service.BeginObservation(), remote); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Get(t.Context(), "pr-1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.AuthorHolarkID == "" || len(snapshot.AssigneeHolarkIDs) != 1 || len(snapshot.RequestedReviewerHolarkIDs) != 1 {
		t.Fatal(snapshot)
	}
	if err = service.ApplyObservation(t.Context(), "pr-1", service.BeginObservation(), pullrequestparticipants.RemoteParticipantSnapshot{}); !errors.Is(err, pullrequestparticipants.ErrIncompleteObservation) {
		t.Fatal(err)
	}
	assertStoredSnapshot(t, store, snapshot)
	// A remote list began before a local edit completed.
	stale := service.BeginObservation()
	if _, err = service.RemoveAssignee(t.Context(), "pr-1", snapshot.AssigneeHolarkIDs[0]); err != nil {
		t.Fatal(err)
	}
	refreshes := 0
	service.SetRefreshRequester(func(context.Context, string) { refreshes++ })
	if err = service.ApplyObservation(t.Context(), "pr-1", stale, remote); !errors.Is(err, pullrequestparticipants.ErrStaleObservation) {
		t.Fatal(err)
	}
	current, err := store.Get(t.Context(), "pr-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(current.AssigneeHolarkIDs) != 0 || refreshes != 1 {
		t.Fatalf("stale list restored assignment: %+v refreshes=%d", current, refreshes)
	}
	old, newer := service.BeginObservation(), service.BeginObservation()
	if err = service.ApplyObservation(t.Context(), "pr-1", newer, pullrequestparticipants.RemoteParticipantSnapshot{Complete: true}); err != nil {
		t.Fatal(err)
	}
	if err = service.ApplyObservation(t.Context(), "pr-1", old, remote); !errors.Is(err, pullrequestparticipants.ErrStaleObservation) {
		t.Fatal(err)
	}
	current, err = store.Get(t.Context(), "pr-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(current.AssigneeHolarkIDs) != 0 || len(current.RequestedReviewerHolarkIDs) != 0 || current.AuthorHolarkID != snapshot.AuthorHolarkID || refreshes != 1 {
		t.Fatal(current)
	}
}

func TestMutationReconciliationRetriesFailuresAndSkipsSatisfiedRefreshes(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(fmt.Sprint(bulk), func(t *testing.T) {
			service, store, provider, _ := newIntegratedService(t)
			stale := service.BeginObservation()
			if _, err := service.AddAssignee(t.Context(), "pr-1", "member-a"); err != nil {
				t.Fatal(err)
			}
			refreshes := 0
			service.SetRefreshRequester(func(context.Context, string) { refreshes++ })
			remote := pullrequestparticipants.RemoteParticipantSnapshot{Complete: true, AssigneeGitHubNodeIDs: []string{"node-b"}}
			if err := service.ApplyObservation(t.Context(), "pr-1", stale, remote); !errors.Is(err, pullrequestparticipants.ErrStaleObservation) {
				t.Fatal(err)
			}
			provider.getErr = errors.New("offline")
			if _, err := service.Reconcile(t.Context(), "pr-1"); err == nil {
				t.Fatal("failed reconciliation succeeded")
			}
			snapshot, _ := store.Get(t.Context(), "pr-1")
			if !reflect.DeepEqual(snapshot.AssigneeHolarkIDs, []string{"member-a"}) {
				t.Fatal(snapshot)
			}
			provider.getErr = nil
			provider.snapshot = remote
			if bulk {
				if err := service.ApplyObservation(t.Context(), "pr-1", service.BeginObservation(), remote); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := service.Reconcile(t.Context(), "pr-1"); err != nil {
				t.Fatal(err)
			}
			calls := 2
			if bulk {
				calls = 1
			}
			if provider.getCalls != calls || refreshes != 1 {
				t.Fatalf("calls=%d refreshes=%d", provider.getCalls, refreshes)
			}
			if _, err := service.Reconcile(t.Context(), "pr-1"); err != nil {
				t.Fatal(err)
			}
			if provider.getCalls != calls {
				t.Fatal("satisfied reconciliation fetched again")
			}
			snapshot, _ = store.Get(t.Context(), "pr-1")
			if !reflect.DeepEqual(snapshot.AssigneeHolarkIDs, []string{"member-b"}) {
				t.Fatal(snapshot)
			}
		})
	}
}
