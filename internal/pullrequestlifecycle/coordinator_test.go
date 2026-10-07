package pullrequestlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repositorybrowser"
)

// Collaboration failures are isolated in the explicit participant service (see
// TestServiceLogsOneFlatSyncFailureEvent); basic refresh never invokes it or Git.
func TestCoordinatorBasicSyncDoesNotFetchGitOrReadiness(t *testing.T) {
	previous := PullRequest{ID: "pr-1", RepositoryID: "project", Status: StatusOpen, SyncProvider: "github", SyncExternalID: "github:owner/repo#12", HeadCommit: "head", BaseCommit: "base", DiffBaseCommit: "diff"}
	registry := &recordingSyncRegistry{fakeRegistry: &fakeRegistry{pullRequests: []PullRequest{previous}}}
	refreshCalls := 0
	github := &githubListStub{pullRequests: []GitHubPullRequest{{Status: StatusOpen, ExternalID: previous.SyncExternalID, HeadCommit: "head", BaseCommit: "base"}}}
	coordinator := New(registry, Options{Projects: ProjectLookupFunc(func(string) (Project, bool) {
		return Project{ID: "project", RepositoryURL: "https://github.com/owner/repo", GitHubBacked: true}, true
	}), Repository: failingSyncRepository{err: errors.New("Git must not be fetched"), refreshCalls: &refreshCalls}, GitHubTransport: github})
	for range 2 {
		result, err := coordinator.Sync(t.Context(), "project")
		if err != nil || len(result.PullRequests) != 1 || result.PullRequests[0].DiffBaseCommit != "diff" {
			t.Fatalf("basic refresh=%+v err=%v", result, err)
		}
	}
	if refreshCalls != 0 {
		t.Fatalf("basic refresh fetched Git %d times", refreshCalls)
	}
	if github.readinessCalls != 0 {
		t.Fatalf("basic refresh invoked readiness %d times", github.readinessCalls)
	}
}

func TestCoordinatorTransitionsLocalPullRequestLifecycle(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, test := range []struct {
		name   string
		from   Status
		target Status
	}{
		{name: "wip to draft", from: StatusWIP, target: StatusDraft},
		{name: "wip to open", from: StatusWIP, target: StatusOpen},
		{name: "draft to open", from: StatusDraft, target: StatusOpen},
		{name: "open to draft", from: StatusOpen, target: StatusDraft},
		{name: "draft to closed", from: StatusDraft, target: StatusClosed},
		{name: "open to closed", from: StatusOpen, target: StatusClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := &fakeRegistry{}
			projects := ProjectLookupFunc(func(id string) (Project, bool) {
				return Project{ID: id, RepositoryURL: "git@example.com:owner/repo.git", DefaultBranch: "main"}, true
			})
			coordinator := New(registry, Options{Projects: projects, Clock: ClockFunc(func() time.Time { return now })})

			updated, err := coordinator.Transition(t.Context(), PullRequest{ID: "pr_1", RepositoryID: "project", Status: test.from}, test.target)
			if err != nil {
				t.Fatal(err)
			}
			if registry.transitionCalls != 1 || registry.transitionID != "pr_1" || registry.transitionStatus != test.target || !registry.transitionUpdatedAt.Equal(now) {
				t.Fatalf("transition call = %d id=%q status=%q updated=%s", registry.transitionCalls, registry.transitionID, registry.transitionStatus, registry.transitionUpdatedAt)
			}
			if updated.Status != test.target {
				t.Fatalf("updated status = %q, want %q", updated.Status, test.target)
			}
		})
	}
}

func TestCoordinatorRejectsMissingProviderForGitHubRepository(t *testing.T) {
	projects := ProjectLookupFunc(func(id string) (Project, bool) {
		return Project{ID: id, RepositoryURL: "git@github.com:owner/repo.git", DefaultBranch: "main", GitHubBacked: true}, true
	})

	t.Run("sync", func(t *testing.T) {
		coordinator := New(&fakeRegistry{}, Options{Projects: projects})
		if _, err := coordinator.Sync(t.Context(), "project"); !errors.Is(err, ErrGitHubClientUnsupported) {
			t.Fatalf("error = %v, want %v", err, ErrGitHubClientUnsupported)
		}
	})

	t.Run("publish WIP", func(t *testing.T) {
		registry := &fakeRegistry{}
		coordinator := New(registry, Options{Projects: projects})
		pullRequest := PullRequest{ID: "pr_1", RepositoryID: "project", Status: StatusWIP}

		if _, err := coordinator.Transition(t.Context(), pullRequest, StatusOpen); !errors.Is(err, ErrGitHubClientUnsupported) {
			t.Fatalf("error = %v, want %v", err, ErrGitHubClientUnsupported)
		}
		if registry.transitionCalls != 0 {
			t.Fatalf("local transition calls = %d, want 0", registry.transitionCalls)
		}
	})
}

func TestCoordinatorSyncPersistsGitHubLifecycleWhenRepositoryRefreshFails(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	mergedAt := now.Add(-time.Minute)
	previous := PullRequest{
		ID: "pr_1", RepositoryID: "project", Status: StatusOpen,
		SyncProvider: string(SyncProviderGitHub), SyncExternalID: "github:owner/repo#12",
		BaseBranch: "main", BaseCommit: "stored-base", DiffBaseCommit: "stored-diff-base",
		HeadBranch: "feature", HeadCommit: "stored-head",
	}
	registry := &recordingSyncRegistry{fakeRegistry: &fakeRegistry{pullRequests: []PullRequest{previous}}}
	coordinator := New(registry, Options{
		Projects: ProjectLookupFunc(func(id string) (Project, bool) {
			return Project{ID: id, RepositoryURL: "git@github.com:owner/repo.git", DefaultBranch: "main", GitHubBacked: true}, true
		}),
		Repository: failingSyncRepository{err: repository.ErrRepositoryUnavailable},
		GitHubTransport: &githubListStub{pullRequests: []GitHubPullRequest{{
			Title: "Merged change", BaseBranch: "main", BaseCommit: "provider-base",
			HeadBranch: "feature", HeadCommit: "provider-head", Status: StatusMerged,
			ExternalID: previous.SyncExternalID, MergedAt: &mergedAt, UpdatedAt: now,
		}}},
		Clock: ClockFunc(func() time.Time { return now }),
	})

	result, err := coordinator.Sync(t.Context(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.upserted) != 1 {
		t.Fatalf("upserted pull requests = %d, want 1", len(registry.upserted))
	}
	updated := registry.upserted[0]
	if updated.Status != StatusMerged || updated.MergedAt == nil || !updated.MergedAt.Equal(mergedAt) {
		t.Fatalf("updated lifecycle = %+v", updated)
	}
	if updated.BaseBranch != "main" || updated.BaseCommit != "provider-base" || updated.DiffBaseCommit != "" {
		t.Fatalf("updated topology = base branch %q, base %q, diff base %q; want pinned %q, %q, %q",
			updated.BaseBranch, updated.BaseCommit, updated.DiffBaseCommit, "main", "provider-base", "")
	}
	if len(result.PullRequests) != 1 || result.PullRequests[0].Status != StatusMerged {
		t.Fatalf("sync result = %+v", result)
	}
}

func TestCoordinatorSyncPullRequestPersistsMergedLifecycleWhenRepositoryRefreshFails(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	mergedAt := now.Add(-time.Minute)
	previous := PullRequest{
		ID: "pr_1", RepositoryID: "project", Status: StatusOpen,
		SyncProvider: string(SyncProviderGitHub), SyncExternalID: "github:owner/repo#12",
		BaseBranch: "main", BaseCommit: "stored-base", DiffBaseCommit: "stored-diff-base",
		HeadBranch: "feature", HeadCommit: "stored-head", LinkedHolonIDs: []string{"holon-1"},
	}
	registry := &recordingSyncRegistry{fakeRegistry: &fakeRegistry{pullRequests: []PullRequest{previous}}}
	github := &githubGetStub{pullRequest: GitHubPullRequest{
		Title: "Merged change", BaseBranch: "main", BaseCommit: "provider-base",
		HeadBranch: "feature", HeadCommit: "provider-head", Status: StatusMerged,
		ExternalID: previous.SyncExternalID, MergedAt: &mergedAt, UpdatedAt: now,
	}}
	coordinator := New(registry, Options{
		Projects: ProjectLookupFunc(func(id string) (Project, bool) {
			return Project{ID: id, RepositoryURL: "git@github.com:owner/repo.git", DefaultBranch: "main", GitHubBacked: true}, true
		}),
		Repository:      failingSyncRepository{err: repository.ErrRepositoryUnavailable},
		GitHubTransport: github,
		GitHubCodec:     githubCodecStub{},
		Clock:           ClockFunc(func() time.Time { return now }),
	})

	updated, err := syncCached(coordinator, t.Context(), previous)
	if err == nil {
		t.Fatal("required refresh failure must be reported")
	}
	if github.getCalls != 1 || github.readinessCalls != 0 {
		t.Fatalf("GitHub calls = get %d, readiness %d; want 1, 0", github.getCalls, github.readinessCalls)
	}
	if updated.ID != previous.ID || updated.Status != StatusMerged || updated.MergedAt == nil || !updated.MergedAt.Equal(mergedAt) {
		t.Fatalf("updated lifecycle = %+v", updated)
	}
	if updated.BaseBranch != previous.BaseBranch || updated.BaseCommit != previous.BaseCommit || updated.DiffBaseCommit != previous.DiffBaseCommit {
		t.Fatalf("updated topology = base branch %q, base %q, diff base %q; want preserved %q, %q, %q",
			updated.BaseBranch, updated.BaseCommit, updated.DiffBaseCommit, previous.BaseBranch, previous.BaseCommit, previous.DiffBaseCommit)
	}
	if !reflect.DeepEqual(updated.LinkedHolonIDs, previous.LinkedHolonIDs) {
		t.Fatalf("linked holons = %v, want %v", updated.LinkedHolonIDs, previous.LinkedHolonIDs)
	}
}

func TestCoordinatorRefreshGitHubReadinessFailurePreservesPullRequest(t *testing.T) {
	readiness := GitHubReadiness{
		HeadCommit: "head", ChecksState: GitHubChecksPassing,
		MergeabilityState: GitHubMergeabilityMergeable,
		DetailsURL:        "https://github.com/owner/repo/pull/12", SyncedAt: time.Date(2026, 1, 2, 2, 4, 5, 0, time.UTC),
	}
	codec := &readinessJSONCodec{}
	syncData := codec.mustStoreReadiness(t, json.RawMessage(`{"state":"OPEN"}`), readiness)
	pullRequest := PullRequest{
		ID: "pr_1", RepositoryID: "project", Status: StatusOpen,
		SyncProvider: string(SyncProviderGitHub), SyncExternalID: "github:owner/repo#12", SyncData: syncData,
	}

	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "request canceled", err: context.Canceled},
		{name: "transient provider failure", err: errors.New("temporary GitHub failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := &fakeRegistry{}
			github := &githubGetStub{readinessErr: test.err}
			coordinator := New(registry, Options{
				Projects: ProjectLookupFunc(func(id string) (Project, bool) {
					return Project{ID: id, RepositoryURL: "git@github.com:owner/repo.git", GitHubBacked: true}, true
				}),
				GitHubTransport: github,
				GitHubCodec:     codec,
			})

			updated, err := coordinator.RefreshGitHubReadiness(t.Context(), pullRequest)
			if !errors.Is(err, test.err) {
				t.Fatalf("error = %v, want %v", err, test.err)
			}
			if !reflect.DeepEqual(updated, pullRequest) {
				t.Fatalf("updated pull request = %+v, want unchanged %+v", updated, pullRequest)
			}
			if github.readinessCalls != 1 {
				t.Fatalf("readiness calls = %d, want 1", github.readinessCalls)
			}
			if registry.syncDataUpdateCalls != 0 {
				t.Fatalf("sync data update calls = %d, want 0", registry.syncDataUpdateCalls)
			}
		})
	}
}

func TestCoordinatorSyncPullRequestPreservesSuccessfulReadinessWhenRefreshFails(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	wantReadiness := GitHubReadiness{
		HeadCommit: "stored-head", ChecksState: GitHubChecksPassing,
		MergeabilityState: GitHubMergeabilityMergeable,
		DetailsURL:        "https://github.com/owner/repo/pull/12", SyncedAt: now.Add(-time.Minute),
	}
	codec := &readinessJSONCodec{}
	previous := PullRequest{
		ID: "pr_1", RepositoryID: "project", Title: "Old title", Status: StatusOpen,
		SyncProvider: string(SyncProviderGitHub), SyncExternalID: "github:owner/repo#12",
		BaseBranch: "main", BaseCommit: "stored-base", DiffBaseCommit: "stored-diff-base",
		HeadBranch: "feature", HeadCommit: "stored-head",
		SyncData: codec.mustStoreReadiness(t, json.RawMessage(`{"state":"OPEN","draft":false}`), wantReadiness),
	}
	registry := &recordingSyncRegistry{fakeRegistry: &fakeRegistry{pullRequests: []PullRequest{previous}}}
	readinessErr := errors.New("temporary GitHub failure")
	github := &githubGetStub{
		pullRequest: GitHubPullRequest{
			Title: "Updated title", BaseBranch: "main", BaseCommit: "provider-base",
			HeadBranch: "feature", HeadCommit: "provider-head", Status: StatusDraft,
			ExternalID: previous.SyncExternalID, SyncData: json.RawMessage(`{"state":"OPEN","draft":true}`), UpdatedAt: now,
		},
		readinessErr: readinessErr,
	}
	coordinator := New(registry, Options{
		Projects: ProjectLookupFunc(func(id string) (Project, bool) {
			return Project{ID: id, RepositoryURL: "git@github.com:owner/repo.git", DefaultBranch: "main", GitHubBacked: true}, true
		}),
		GitHubTransport: github,
		GitHubCodec:     codec,
		Clock:           ClockFunc(func() time.Time { return now }),
	})

	updated, err := syncCached(coordinator, t.Context(), previous)
	if err == nil {
		t.Fatal("required refresh failure must be reported")
	}
	if github.getCalls != 1 || github.readinessCalls != 1 {
		t.Fatalf("GitHub calls = get %d, readiness %d; want 1, 1", github.getCalls, github.readinessCalls)
	}
	if registry.syncDataUpdateCalls != 0 {
		t.Fatalf("sync data update calls = %d, want 0", registry.syncDataUpdateCalls)
	}
	if len(registry.upserted) != 1 {
		t.Fatalf("upserted pull requests = %d, want 1", len(registry.upserted))
	}
	upserted := registry.upserted[0]
	if upserted.Title != "Updated title" || upserted.Status != StatusDraft || upserted.HeadCommit != "stored-head" || !upserted.UpdatedAt.Equal(now) {
		t.Fatalf("upserted lifecycle = %+v", upserted)
	}
	for name, value := range map[string]PullRequest{"upserted": upserted, "returned": updated} {
		gotReadiness, ok := codec.DecodeReadiness(value.SyncData)
		if !ok || !reflect.DeepEqual(gotReadiness, wantReadiness) {
			t.Fatalf("%s readiness = %+v, %t; want %+v, true", name, gotReadiness, ok, wantReadiness)
		}
	}
}

type recordingSyncRegistry struct {
	*fakeRegistry
	upserted []PullRequest
}

func (registry *recordingSyncRegistry) UpsertObservedPullRequests(_ string, pullRequests []PullRequest, _ ObservationToken) (int, int, error) {
	stored := append([]PullRequest(nil), pullRequests...)
	for index := range stored {
		for _, existing := range registry.pullRequests {
			if existing.SyncProvider == stored[index].SyncProvider && existing.SyncExternalID == stored[index].SyncExternalID {
				stored[index].ID = existing.ID
				stored[index].LinkedHolonIDs = append([]string(nil), existing.LinkedHolonIDs...)
				break
			}
		}
	}
	registry.upserted = append([]PullRequest(nil), stored...)
	registry.pullRequests = append([]PullRequest(nil), stored...)
	return 0, len(pullRequests), nil
}

type githubListStub struct {
	readinessCalls int
	GitHubTransport
	pullRequests []GitHubPullRequest
}

func (github *githubListStub) List(context.Context, string) ([]GitHubPullRequest, error) {
	return append([]GitHubPullRequest(nil), github.pullRequests...), nil
}

type githubGetStub struct {
	GitHubTransport
	pullRequest    GitHubPullRequest
	readinessErr   error
	getCalls       int
	readinessCalls int
}

func (github *githubGetStub) Get(context.Context, GitHubPullRequestTarget) (GitHubPullRequest, error) {
	github.getCalls++
	return github.pullRequest, nil
}

func (github *githubGetStub) RefreshReadiness(context.Context, GitHubPullRequestTarget) (GitHubReadiness, error) {
	github.readinessCalls++
	return GitHubReadiness{}, github.readinessErr
}

type githubCodecStub struct{ GitHubSyncDataCodec }

func (githubCodecStub) Number(GitHubPullRequestTarget) int { return 12 }

type readinessJSONCodec struct{}

func (*readinessJSONCodec) Number(GitHubPullRequestTarget) int { return 12 }

func (*readinessJSONCodec) ExternalID(target GitHubPullRequestTarget) string {
	return target.ExternalID
}

func (*readinessJSONCodec) UpdateLifecycleFields(syncData json.RawMessage, _ GitHubLifecycleFields) (json.RawMessage, error) {
	return syncData, nil
}

func (*readinessJSONCodec) DecodeReadiness(syncData json.RawMessage) (GitHubReadiness, bool) {
	var payload struct {
		Readiness *GitHubReadiness `json:"readiness"`
	}
	if err := json.Unmarshal(syncData, &payload); err != nil || payload.Readiness == nil {
		return GitHubReadiness{}, false
	}
	return *payload.Readiness, true
}

func (*readinessJSONCodec) StoreReadiness(syncData json.RawMessage, readiness GitHubReadiness) (json.RawMessage, error) {
	payload := make(map[string]json.RawMessage)
	if len(syncData) != 0 {
		if err := json.Unmarshal(syncData, &payload); err != nil {
			return nil, err
		}
	}
	encoded, err := json.Marshal(readiness)
	if err != nil {
		return nil, err
	}
	payload["readiness"] = encoded
	return json.Marshal(payload)
}

func (codec *readinessJSONCodec) DetailsURL(syncData json.RawMessage) string {
	readiness, _ := codec.DecodeReadiness(syncData)
	return readiness.DetailsURL
}

func (codec *readinessJSONCodec) mustStoreReadiness(t *testing.T, syncData json.RawMessage, readiness GitHubReadiness) json.RawMessage {
	t.Helper()
	stored, err := codec.StoreReadiness(syncData, readiness)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

type failingSyncRepository struct {
	err          error
	refreshCalls *int
}

func (repository failingSyncRepository) Refresh(context.Context, repositorybrowser.Repository) error {
	if repository.refreshCalls != nil {
		*repository.refreshCalls++
	}
	return repository.err
}

func (failingSyncRepository) ResolveRef(context.Context, repositorybrowser.Repository, string) (string, error) {
	return "", repository.ErrRepositoryUnavailable
}

func (failingSyncRepository) PrepareBranch(context.Context, string) (repository.Preparation, error) {
	return repository.Preparation{}, repository.ErrRepositoryUnavailable
}

func (failingSyncRepository) MergeBase(context.Context, string, string) (string, error) {
	return "", repository.ErrRepositoryUnavailable
}

type fakeRegistry struct {
	Registry
	actionFixture
	transitionID        string
	transitionStatus    Status
	transitionUpdatedAt time.Time
	transitionCalls     int
	syncDataUpdateCalls int
	pullRequests        []PullRequest
}

func (registry *fakeRegistry) ListPullRequests(string) []PullRequest {
	return append([]PullRequest(nil), registry.pullRequests...)
}

func (registry *fakeRegistry) UpsertSyncedPullRequests(string, []PullRequest) (int, int, error) {
	return 0, 0, nil
}

func (registry *fakeRegistry) RefreshPullRequestHead(string, string, string) (PullRequest, bool, error) {
	return PullRequest{}, false, nil
}

func (registry *fakeRegistry) TransitionPullRequestStatus(id string, status Status, updatedAt time.Time) (PullRequest, error) {
	registry.transitionCalls++
	registry.transitionID = id
	registry.transitionStatus = status
	registry.transitionUpdatedAt = updatedAt
	return PullRequest{ID: id, Status: status, UpdatedAt: updatedAt}, nil
}

func (*fakeRegistry) TransitionSyncedPullRequestStatus(id string, status Status, syncData json.RawMessage, updatedAt time.Time, closedAt *time.Time, syncedAt time.Time) (PullRequest, error) {
	return PullRequest{ID: id, Status: status, SyncData: syncData, UpdatedAt: updatedAt, ClosedAt: closedAt, SyncedAt: &syncedAt}, nil
}

func (registry *fakeRegistry) UpdatePullRequestSyncData(id string, expectedStatus Status, syncData json.RawMessage, updatedAt time.Time) (PullRequest, error) {
	registry.syncDataUpdateCalls++
	return PullRequest{ID: id, Status: expectedStatus, SyncData: syncData, UpdatedAt: updatedAt}, nil
}

func (*fakeRegistry) AttachSyncedPullRequest(id string, synced PullRequest) (PullRequest, error) {
	synced.ID = id
	return synced, nil
}

func TestCreationCoordinatorPublishesBeforePersistingWIP(t *testing.T) {
	events := []string{}
	catalog := &creationCatalog{events: &events}
	publisher := &creationPublisher{events: &events}
	coordinator := NewCreationCoordinator(catalog, creationSource{value: validCreationSource()}, publisher, &creationLifecycle{}, validCreationTopology())

	created, err := coordinator.Create(t.Context(), CreatePullRequest{
		HolonID: "holon-1", RepositoryID: "repo", Title: "Repair", Target: StatusWIP,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != StatusWIP {
		t.Fatalf("status = %q, want %q", created.Status, StatusWIP)
	}
	if got, want := events, []string{"publish", "create"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestCreationCoordinatorDoesNotPersistWIPWhenPublicationFails(t *testing.T) {
	wantErr := errors.New("push failed")
	catalog := &creationCatalog{}
	coordinator := NewCreationCoordinator(catalog, creationSource{value: validCreationSource()}, &creationPublisher{err: wantErr}, &creationLifecycle{}, validCreationTopology())

	_, err := coordinator.Create(t.Context(), CreatePullRequest{
		HolonID: "holon-1", RepositoryID: "repo", Title: "Repair", Target: StatusWIP,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if catalog.created.ID != "" {
		t.Fatalf("persisted pull request = %+v", catalog.created)
	}
}

func TestCreationCoordinatorRejectsDirtyAndUnchangedSources(t *testing.T) {
	for _, test := range []struct {
		name   string
		source PullRequestSource
		want   error
	}{
		{name: "dirty", source: func() PullRequestSource { value := validCreationSource(); value.Dirty = true; return value }(), want: ErrWorkspaceDirty},
		{name: "unchanged", source: func() PullRequestSource { value := validCreationSource(); value.HasChanges = false; return value }(), want: ErrNoChanges},
	} {
		t.Run(test.name, func(t *testing.T) {
			publisher := &creationPublisher{}
			coordinator := NewCreationCoordinator(&creationCatalog{}, creationSource{value: test.source}, publisher, &creationLifecycle{}, validCreationTopology())
			_, err := coordinator.Create(t.Context(), CreatePullRequest{HolonID: "holon-1", RepositoryID: "repo", Title: "Repair", Target: StatusOpen})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			if publisher.calls != 0 {
				t.Fatalf("publication calls = %d, want 0", publisher.calls)
			}
		})
	}
}

func validCreationSource() PullRequestSource {
	return PullRequestSource{HolonID: "holon-1", BaseBranch: "main", BaseCommit: "base", HeadBranch: "holark/repair", HeadCommit: "head", HasChanges: true}
}

func validCreationTopology() *creationTopology {
	return &creationTopology{preparation: repository.Preparation{Branch: "main", Commit: "base"}, mergeBase: "base"}
}

type creationTopology struct {
	preparation repository.Preparation
	mergeBase   string
	err         error
}

func (topology *creationTopology) PrepareBranch(context.Context, string) (repository.Preparation, error) {
	return topology.preparation, topology.err
}

func (topology *creationTopology) MergeBase(context.Context, string, string) (string, error) {
	return topology.mergeBase, topology.err
}

type creationCatalog struct {
	actionFixture
	items   []PullRequest
	created PullRequest
	events  *[]string
}

func (catalog *creationCatalog) ListPullRequests(string) []PullRequest { return catalog.items }
func (catalog *creationCatalog) CreateOperationPullRequest(_ context.Context, _ string, value PullRequest) (PullRequest, error) {
	if catalog.events != nil {
		*catalog.events = append(*catalog.events, "create")
	}
	value.ID = "pr-new"
	value.Status = StatusWIP
	catalog.created = value
	return value, nil
}

type creationSource struct {
	value PullRequestSource
	err   error
}

func (source creationSource) PullRequestSource(context.Context, string) (PullRequestSource, error) {
	return source.value, source.err
}

type creationPublisher struct {
	commit string
	branch string
	calls  int
	err    error
	events *[]string
}

func (publisher *creationPublisher) Publish(_ context.Context, commit, branch string) error {
	publisher.commit, publisher.branch = commit, branch
	publisher.calls++
	if publisher.events != nil {
		*publisher.events = append(*publisher.events, "publish")
	}
	return publisher.err
}

type creationLifecycle struct{ target Status }

func (lifecycle *creationLifecycle) RequestTransition(_ context.Context, value PullRequest, target Status) (PullRequest, error) {
	lifecycle.target = target
	value.Status = target
	return value, nil
}

func (r failingSyncRepository) RemoteBranchHead(context.Context, string, string) (string, error) {
	return "", r.err
}

type actionFixture struct{ ActionRegistry }

func (*actionFixture) BeginOperation(_ context.Context, operation Operation) (Operation, bool, error) {
	operation.Status = "running"
	return operation, true, nil
}
func (*actionFixture) GetOperation(context.Context, string) (Operation, bool, error) {
	return Operation{}, false, nil
}
func (*actionFixture) CompleteOperation(context.Context, string, string, string) (Operation, error) {
	return Operation{}, nil
}
func (*actionFixture) RecordOperationStep(context.Context, string, string, string, OperationStep) error {
	return nil
}
func (registry *fakeRegistry) GetPullRequest(id string) (PullRequest, bool) {
	return findPullRequest(registry.pullRequests, id)
}
func (*fakeRegistry) BeginObservation(context.Context, string, []FieldGroup) (ObservationToken, error) {
	return ObservationToken{Sequence: 1}, nil
}
func (*fakeRegistry) UpsertObservedPullRequests(string, []PullRequest, ObservationToken) (int, int, error) {
	return 0, 0, nil
}
func (registry *fakeRegistry) CompleteTransition(_ context.Context, _ string, _, confirmed PullRequest) (PullRequest, error) {
	return registry.TransitionPullRequestStatus(confirmed.ID, confirmed.Status, confirmed.UpdatedAt)
}
func (github *githubListStub) RefreshReadiness(context.Context, GitHubPullRequestTarget) (GitHubReadiness, error) {
	github.readinessCalls++
	return GitHubReadiness{}, errors.New("readiness failed")
}

func (registry *fakeRegistry) BeginOperation(ctx context.Context, operation Operation) (Operation, bool, error) {
	return registry.actionFixture.BeginOperation(ctx, operation)
}
func (registry *fakeRegistry) GetOperation(ctx context.Context, id string) (Operation, bool, error) {
	return registry.actionFixture.GetOperation(ctx, id)
}
func (registry *fakeRegistry) CompleteOperation(ctx context.Context, id, status, message string) (Operation, error) {
	return registry.actionFixture.CompleteOperation(ctx, id, status, message)
}
func (registry *fakeRegistry) RecordOperationStep(ctx context.Context, id, prID, name string, step OperationStep) error {
	return registry.actionFixture.RecordOperationStep(ctx, id, prID, name, step)
}

func (r failingSyncRepository) EnsureRemoteCommit(context.Context, string, string) error {
	return r.err
}
func (topology *creationTopology) EnsureRemoteCommit(context.Context, string, string) error {
	return topology.err
}
func (topology *creationTopology) RemoteBranchHead(context.Context, string, string) (string, error) {
	return "", topology.err
}

func syncCached(c *Coordinator, ctx context.Context, p PullRequest) (PullRequest, error) {
	err := c.SyncPullRequest(ctx, p.ID)
	current, _ := c.GetPullRequest(p.ID)
	return current, err
}
