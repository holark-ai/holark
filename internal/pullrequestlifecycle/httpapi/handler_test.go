package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	githubpullrequests "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	pullrequestsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestmerge"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
)

type httpCatalog struct {
	Catalog
	items []pullrequestlifecycle.PullRequest
}

func (catalog httpCatalog) ListPullRequests(string) []pullrequestlifecycle.PullRequest {
	return catalog.items
}
func (catalog httpCatalog) GetPullRequest(id string) (pullrequestlifecycle.PullRequest, bool) {
	for _, item := range catalog.items {
		if item.ID == id {
			return item, true
		}
	}
	return pullrequestlifecycle.PullRequest{}, false
}
func (httpCatalog) CreatePullRequest(value pullrequestlifecycle.PullRequest) (pullrequestlifecycle.PullRequest, error) {
	return value, nil
}
func (httpCatalog) MergePullRequest(string, string, string, time.Time) (pullrequestlifecycle.PullRequest, error) {
	return pullrequestlifecycle.PullRequest{}, nil
}

type httpCreator struct {
	request pullrequestlifecycle.CreatePullRequest
}

func (creator *httpCreator) Create(_ context.Context, request pullrequestlifecycle.CreatePullRequest) (pullrequestlifecycle.PullRequest, error) {
	creator.request = request
	return pullrequestlifecycle.PullRequest{ID: "pr-new", Title: request.Title, Status: pullrequestlifecycle.StatusWIP, LinkedHolonIDs: []string{request.HolonID, "metadata-1"}}, nil
}

type httpHolons struct{ pullRequestID string }

func (holons httpHolons) PullRequestReference(context.Context, string) (string, error) {
	return holons.pullRequestID, nil
}

func pullRequestHTTP(options Options) http.Handler {
	mux := http.NewServeMux()
	RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, options)
	return mux
}

type httpPanelPins struct{ pinned map[string]bool }

func (pins *httpPanelPins) Pin(_ context.Context, id string) error {
	if pins.pinned == nil {
		pins.pinned = map[string]bool{}
	}
	pins.pinned[id] = true
	return nil
}
func (pins *httpPanelPins) Unpin(_ context.Context, id string) error {
	delete(pins.pinned, id)
	return nil
}
func (pins *httpPanelPins) Pinned(_ context.Context, id string) (bool, error) {
	return pins.pinned[id], nil
}
func (pins *httpPanelPins) List(context.Context, string) (map[string]bool, error) {
	result := make(map[string]bool, len(pins.pinned))
	for id, pinned := range pins.pinned {
		result[id] = pinned
	}
	return result, nil
}

func TestPanelPinHTTPContract(t *testing.T) {
	open := pullrequestlifecycle.PullRequest{ID: "open", RepositoryID: "repo", Status: pullrequestlifecycle.StatusOpen}
	merged := pullrequestlifecycle.PullRequest{ID: "merged", RepositoryID: "repo", Status: pullrequestlifecycle.StatusMerged}
	pins := &httpPanelPins{pinned: map[string]bool{open.ID: true, merged.ID: true}}
	handler := pullRequestHTTP(Options{RepositoryID: "repo", Catalog: httpCatalog{items: []pullrequestlifecycle.PullRequest{open, merged}}, PanelPins: pins})

	t.Run("detail and list expose durable state", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/pull-requests/open", nil))
		var detail response
		if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusOK || !detail.PanelPinned {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}

		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/pull-requests", nil))
		var list []response
		if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusOK || len(list) != 2 || !list[0].PanelPinned || !list[1].PanelPinned {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("PUT and DELETE are idempotent", func(t *testing.T) {
		for _, method := range []string{http.MethodPut, http.MethodPut, http.MethodDelete, http.MethodDelete} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, "/api/v1/pull-requests/open/panel-pin", nil))
			if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
				t.Fatalf("method=%s status=%d body=%s", method, response.Code, response.Body.String())
			}
		}
		if pins.pinned[open.ID] {
			t.Fatal("pull request remains pinned")
		}
	})

	t.Run("missing pull requests are rejected", func(t *testing.T) {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, "/api/v1/pull-requests/missing/panel-pin", nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("method=%s status=%d body=%s", method, response.Code, response.Body.String())
			}
		}
	})
}

func TestCreateReturnsPullRequestDirectly(t *testing.T) {
	creator := &httpCreator{}
	handler := pullRequestHTTP(Options{RepositoryID: "repo", Creator: creator})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/holons/holon-1/pull-request", strings.NewReader(`{"title":"Repair","target":"open"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["id"] != "pr-new" || body["pull_request"] != nil {
		t.Fatalf("body=%v", body)
	}
	if creator.request.HolonID != "holon-1" || creator.request.Target != pullrequestlifecycle.StatusOpen {
		t.Fatalf("request=%+v", creator.request)
	}
	if links, ok := body["linked_session_ids"].([]any); !ok || len(links) != 2 || links[1] != "metadata-1" {
		t.Fatalf("links=%#v", body["linked_session_ids"])
	}
}

func TestHolonPullRequestResolvesDirectAndPurposeLinks(t *testing.T) {
	pullRequest := pullrequestlifecycle.PullRequest{ID: "pr-1", Status: pullrequestlifecycle.StatusOpen, LinkedHolonIDs: []string{"direct"}}
	for _, test := range []struct{ name, holonID, purposeID string }{
		{name: "direct", holonID: "direct"},
		{name: "purpose", holonID: "worker", purposeID: "pr-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := pullRequestHTTP(Options{RepositoryID: "repo", Catalog: httpCatalog{items: []pullrequestlifecycle.PullRequest{pullRequest}}, Holons: httpHolons{pullRequestID: test.purposeID}})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/holons/"+test.holonID+"/pull-request", nil))
			var body struct {
				PullRequest pullrequestlifecycle.PullRequest `json:"pull_request"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || body.PullRequest.ID != "pr-1" {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestHolonPullRequestNotFoundIsStructured(t *testing.T) {
	handler := pullRequestHTTP(Options{RepositoryID: "repo", Catalog: httpCatalog{}, Holons: httpHolons{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/holons/missing/pull-request", nil))
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"pull_request_not_found"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

type syncLifecycle struct {
	Lifecycle
	events       *[]string
	readinessErr error
}

func (lifecycle syncLifecycle) Sync(context.Context, string) (pullrequestlifecycle.SyncResult, error) {
	*lifecycle.events = append(*lifecycle.events, "history")
	return pullrequestlifecycle.SyncResult{PullRequests: []pullrequestlifecycle.PullRequest{{ID: "pr-1", Status: pullrequestlifecycle.StatusOpen, SyncProvider: "github"}}}, nil
}
func (lifecycle syncLifecycle) SyncPullRequest(context.Context, string) error { return nil }
func (lifecycle syncLifecycle) RequestTransition(_ context.Context, value pullrequestlifecycle.PullRequest, _ pullrequestlifecycle.Status) (pullrequestlifecycle.PullRequest, error) {
	return value, nil
}
func (lifecycle syncLifecycle) RefreshGitHubReadiness(_ context.Context, value pullrequestlifecycle.PullRequest) (pullrequestlifecycle.PullRequest, error) {
	*lifecycle.events = append(*lifecycle.events, "readiness")
	return value, lifecycle.readinessErr
}

func (lifecycle syncLifecycle) SyncActive(context.Context, string) (pullrequestlifecycle.SyncResult, error) {
	*lifecycle.events = append(*lifecycle.events, "active")
	return pullrequestlifecycle.SyncResult{}, nil
}

func TestManualSyncImportsHistoryWithoutRefreshingSections(t *testing.T) {
	events := []string{}
	handler := pullRequestHTTP(Options{RepositoryID: "repo", Lifecycle: syncLifecycle{events: &events}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/sync", nil))
	if response.Code != http.StatusOK || strings.Join(events, ",") != "history" {
		t.Fatalf("status=%d events=%v body=%s", response.Code, events, response.Body.String())
	}
}

func TestRepositorySyncDoesNotDependOnUnavailableReadiness(t *testing.T) {
	for _, scope := range []string{"history", "active"} {
		t.Run(scope, func(t *testing.T) {
			events := []string{}
			handler := pullRequestHTTP(Options{RepositoryID: "repo", Lifecycle: syncLifecycle{events: &events, readinessErr: errors.New("partial readiness")}})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/sync?scope="+scope, nil))
			if response.Code != http.StatusOK || strings.Join(events, ",") != scope {
				t.Fatalf("status=%d events=%v body=%s", response.Code, events, response.Body.String())
			}
		})
	}
}

type targetedSyncLifecycle struct {
	err error
	Lifecycle
	updated  pullrequestlifecycle.PullRequest
	received string
	catalog  *httpCatalog
}

func (lifecycle *targetedSyncLifecycle) SyncPullRequest(_ context.Context, id string) error {
	lifecycle.received = id
	lifecycle.catalog.items[0] = lifecycle.updated
	return lifecycle.err
}

type retirementRecorder struct{ pullRequestIDs []string }

func (retirement *retirementRecorder) RetireInactive(_ context.Context, pullRequestID string) error {
	retirement.pullRequestIDs = append(retirement.pullRequestIDs, pullRequestID)
	return nil
}

func TestTargetedSyncReadsAcceptedLifecycleAndRetiresInactiveWork(t *testing.T) {
	for _, partialFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(partialFailure), func(t *testing.T) {
			stored := pullrequestlifecycle.PullRequest{
				ID: "pr-1", RepositoryID: "repo", Status: pullrequestlifecycle.StatusOpen,
				SyncProvider: string(pullrequestlifecycle.SyncProviderGitHub), SyncExternalID: "github:owner/repo#12",
			}
			lifecycle := &targetedSyncLifecycle{updated: pullrequestlifecycle.PullRequest{
				ID: "pr-1", RepositoryID: "repo", Status: pullrequestlifecycle.StatusMerged,
				SyncProvider: string(pullrequestlifecycle.SyncProviderGitHub), SyncExternalID: stored.SyncExternalID,
			}}
			if partialFailure {
				lifecycle.err = errors.New("required Git preparation failed")
			}
			retirement := &retirementRecorder{}
			catalog := &httpCatalog{items: []pullrequestlifecycle.PullRequest{stored}}
			lifecycle.catalog = catalog
			handler := pullRequestHTTP(Options{
				RepositoryID: "repo", Catalog: catalog,
				Lifecycle: lifecycle, Retirement: retirement,
			})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/pr-1/sync", nil))

			if (!partialFailure && (response.Code != http.StatusNoContent || response.Body.Len() != 0)) || (partialFailure && response.Code != http.StatusInternalServerError) {
				t.Fatalf("sync status=%d body=%s", response.Code, response.Body.String())
			}
			response = httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/pull-requests/pr-1", nil))
			var projected pullrequestlifecycle.PullRequest
			if err := json.Unmarshal(response.Body.Bytes(), &projected); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || projected.ID != "pr-1" || projected.Status != pullrequestlifecycle.StatusMerged {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if lifecycle.received != stored.ID {
				t.Fatalf("synced pull request = %+v", lifecycle.received)
			}
			if len(retirement.pullRequestIDs) != 1 || retirement.pullRequestIDs[0] != stored.ID {
				t.Fatalf("retired pull requests = %v", retirement.pullRequestIDs)
			}

		})
	}
}

func TestTargetedSyncReportsGitHubRateLimit(t *testing.T) {
	stored := pullrequestlifecycle.PullRequest{ID: "pr-1", RepositoryID: "repo", Status: pullrequestlifecycle.StatusOpen}
	catalog := &httpCatalog{items: []pullrequestlifecycle.PullRequest{stored}}
	lifecycle := &targetedSyncLifecycle{
		err:     &pullrequestlifecycle.GitHubError{RateLimitExceeded: true, Err: errors.New("limited")},
		updated: stored,
		catalog: catalog,
	}
	handler := pullRequestHTTP(Options{RepositoryID: "repo", Catalog: catalog, Lifecycle: lifecycle})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/pr-1/sync", nil))
	const want = "{\"code\":\"github_rate_limit_exceeded\",\"message\":\"GitHub API rate limit exceeded. Try again after the limit resets.\"}\n"
	if response.Code != http.StatusTooManyRequests || response.Body.String() != want {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

type mergeAPIClient struct {
	githubpullrequests.APIClient
	store        *pullrequestsqlite.Store
	pullRequest  string
	calls        int
	repository   githubapi.Repository
	number       int
	request      githubapi.MergePullRequestRequest
	statusBefore pullrequestlifecycle.Status
}

func (*mergeAPIClient) CreatePullRequest(context.Context, githubapi.Repository, githubapi.CreatePullRequestRequest) (githubapi.PullRequest, error) {
	return githubapi.PullRequest{}, nil
}
func (*mergeAPIClient) ListPullRequests(context.Context, githubapi.Repository) ([]githubapi.PullRequest, error) {
	return nil, nil
}
func (*mergeAPIClient) PullRequest(context.Context, githubapi.Repository, int) (githubapi.PullRequest, error) {
	return githubapi.PullRequest{}, nil
}
func (*mergeAPIClient) UpdatePullRequestState(context.Context, githubapi.Repository, int, string) (githubapi.PullRequest, error) {
	return githubapi.PullRequest{}, nil
}
func (*mergeAPIClient) ConvertPullRequestToDraft(context.Context, string) (githubapi.PullRequest, error) {
	return githubapi.PullRequest{}, nil
}
func (*mergeAPIClient) MarkPullRequestReadyForReview(context.Context, string) (githubapi.PullRequest, error) {
	return githubapi.PullRequest{}, nil
}
func (*mergeAPIClient) PullRequestReadiness(context.Context, githubapi.Repository, int) (githubapi.PullRequestReadiness, error) {
	return githubapi.PullRequestReadiness{}, nil
}
func (client *mergeAPIClient) MergePullRequest(_ context.Context, repository githubapi.Repository, number int, request githubapi.MergePullRequestRequest) (githubapi.MergePullRequestResponse, error) {
	client.calls++
	client.repository, client.number, client.request = repository, number, request
	if persisted, ok := client.store.GetPullRequest(client.pullRequest); ok {
		client.statusBefore = persisted.Status
	}
	return githubapi.MergePullRequestResponse{Merged: true, SHA: "merged-sha"}, nil
}

func runMergeGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

type retirementFunc func(context.Context, string) error

func (f retirementFunc) RetireInactive(ctx context.Context, id string) error { return f(ctx, id) }

func TestMergeHTTPIntegration(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	runMergeGit(t, root, "init", "-b", "main")
	runMergeGit(t, root, "config", "user.name", "Test")
	runMergeGit(t, root, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runMergeGit(t, root, "add", "README.md")
	runMergeGit(t, root, "commit", "-m", "main")
	baseCommit := runMergeGit(t, root, "rev-parse", "HEAD")
	runMergeGit(t, root, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(root, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runMergeGit(t, root, "add", "feature.txt")
	runMergeGit(t, root, "commit", "-m", "feature")
	headCommit := runMergeGit(t, root, "rev-parse", "HEAD")
	remote := filepath.Join(t.TempDir(), "remote.git")
	runMergeGit(t, "/", "init", "--bare", remote)
	runMergeGit(t, root, "remote", "add", "origin", remote)
	runMergeGit(t, root, "push", "origin", "main", "feature")
	runMergeGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")

	gitStore, err := gitadapter.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	repositories := repository.NewService(gitStore)
	if _, err = repositories.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`create table repositories(id text primary key); insert into repositories(id) values('repo')`); err != nil {
		t.Fatal(err)
	}
	store, err := pullrequestsqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	detailsURL := "https://github.com/owner/repo/pull/12"
	syncData, err := json.Marshal(map[string]any{"github": map[string]any{
		"head_repository_url": remote, "base_repository_url": remote,
		"number": 12,
		"url":    detailsURL,
		"readiness": pullrequestlifecycle.GitHubReadiness{
			HeadCommit: headCommit, ChecksState: pullrequestlifecycle.GitHubChecksPassing,
			MergeabilityState: pullrequestlifecycle.GitHubMergeabilityMergeable, DetailsURL: detailsURL, SyncedAt: now,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	pullRequest, err := store.CreatePullRequest(pullrequestlifecycle.PullRequest{
		ID: "pr-github", RepositoryID: "repo", Title: "Ship merge parity", Summary: "Keep GitHub authoritative.",
		BaseBranch: "main", BaseCommit: baseCommit, HeadBranch: "feature", HeadCommit: headCommit, Status: pullrequestlifecycle.StatusOpen,
		SyncProvider: string(pullrequestlifecycle.SyncProviderGitHub), SyncExternalID: "github:owner/repo#12", SyncData: syncData, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.BeginBranchObservation(ctx, []repository.BranchIdentity{pullRequest.BaseRef, pullRequest.HeadRef})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptBranchObservation(ctx, read, map[string]repository.BranchObservation{pullRequest.BaseRef.Ref: {Commit: baseCommit, Exists: true}, pullRequest.HeadRef.Ref: {Commit: headCommit, Exists: true}}); err != nil {
		t.Fatal(err)
	}
	inputs, err := store.CaptureComparison(ctx, pullRequest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptComparison(ctx, pullRequest.ID, inputs, baseCommit); err != nil {
		t.Fatal(err)
	}
	pullRequest, _ = store.GetPullRequest(pullRequest.ID)
	client := &mergeAPIClient{store: store, pullRequest: pullRequest.ID}
	provider := githubpullrequests.New(client)
	coordinator := pullrequestmerge.New(pullrequestmerge.Options{
		Sync: syncLifecycle{}, RepositoryURL: "https://github.com/owner/repo", Store: store, Repository: &cachedReadRepository{service: repositories}, GitHub: provider,
		Clock: pullrequestmerge.ClockFunc(func() time.Time { return now }),
	})
	scheduler := pullrequestlifecycle.NewRefreshCoordinator()
	t.Cleanup(scheduler.Close)
	attempts := make(chan string, 1)
	release := make(chan error)
	finished := make(chan error, 1)
	cleanup := retirementFunc(func(ctx context.Context, id string) error {
		attempts <- id
		select {
		case err := <-release:
			finished <- ctx.Err()
			return err
		case <-ctx.Done():
			finished <- ctx.Err()
			return ctx.Err()
		}
	})
	lifecycle := pullrequestlifecycle.New(store, pullrequestlifecycle.Options{Refresh: scheduler, Retirement: cleanup})
	handler := pullRequestHTTP(Options{RepositoryID: "repo", Catalog: store, Merge: coordinator, Retirement: cleanup, RetirementScheduler: lifecycle})
	requestEnded := make(chan context.Context, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { requestEnded <- r.Context() }()
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { scheduler.Close(); server.Close() })
	clientHTTP := server.Client()
	clientHTTP.Timeout = 3 * time.Second
	mergeHTTP := func() []byte {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/pull-requests/"+pullRequest.ID+"/merge", strings.NewReader(`{"strategy":"squash","bypass_unresolved_comments":false,"request_id":"merge-cleanup"}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := clientHTTP.Do(request)
		if err != nil {
			t.Fatalf("merge response waited for cleanup: %v", err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("complete merge response = %d: %s, %v", response.StatusCode, body, err)
		}
		select {
		case requestCtx := <-requestEnded:
			select {
			case <-requestCtx.Done():
			case <-time.After(time.Second):
				t.Fatal("HTTP request did not end")
			}
		case <-time.After(time.Second):
			t.Fatal("merge handler waited for cleanup")
		}
		select {
		case id := <-attempts:
			if id != pullRequest.ID {
				t.Fatalf("retired %s", id)
			}
		case <-time.After(time.Second):
			t.Fatal("merge did not schedule cleanup")
		}
		return body
	}
	finishCleanup := func(cleanupErr error) {
		t.Helper()
		select {
		case release <- cleanupErr:
		case <-time.After(time.Second):
			t.Fatal("cleanup stopped before release")
		}
		if err := <-finished; err != nil {
			t.Fatalf("request completion cancelled cleanup: %v", err)
		}
		// Drain the real scheduler before the next replay or catalog assertion.
		if err := scheduler.Do(ctx, pullrequestlifecycle.RefreshKey{RepositoryID: "repo", Section: "retirement"}, func(context.Context) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}

	projectionResponse := httptest.NewRecorder()
	handler.ServeHTTP(projectionResponse, httptest.NewRequest(http.MethodGet, "/api/v1/pull-requests/"+pullRequest.ID, nil))
	if projectionResponse.Code != http.StatusOK {
		t.Fatalf("projection status=%d body=%s", projectionResponse.Code, projectionResponse.Body.String())
	}
	var projected pullrequestlifecycle.PullRequest
	if err := json.Unmarshal(projectionResponse.Body.Bytes(), &projected); err != nil {
		t.Fatal(err)
	}
	if projected.Mergeable == nil || !*projected.Mergeable || projected.MergeBlockedReason != "" || projected.MergeProvider != "github" {
		t.Fatalf("projected=%+v", projected)
	}

	mergeBody := mergeHTTP()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(mergeBody, &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 2 || body["pull_request"] == nil || string(body["merged_commit"]) != `"merged-sha"` {
		t.Fatalf("response=%s", mergeBody)
	}
	var merged pullrequestlifecycle.PullRequest
	if err := json.Unmarshal(body["pull_request"], &merged); err != nil {
		t.Fatal(err)
	}
	if merged.Status != pullrequestlifecycle.StatusMerged || merged.MergedCommit != "merged-sha" || merged.MergeStrategy != "squash" {
		t.Fatalf("merged response=%+v", merged)
	}
	if client.calls != 1 || client.statusBefore != pullrequestlifecycle.StatusOpen || client.repository.Owner != "owner" || client.repository.Name != "repo" || client.number != 12 {
		t.Fatalf("client=%+v", client)
	}
	if client.request.CommitTitle != "Ship merge parity (#12)" || client.request.CommitMessage != "Keep GitHub authoritative.\n\n"+detailsURL || client.request.MergeMethod != "squash" || client.request.ExpectedHeadSHA != headCommit {
		t.Fatalf("provider request=%+v", client.request)
	}
	persisted, ok := store.GetPullRequest(pullRequest.ID)
	if !ok || persisted.Status != pullrequestlifecycle.StatusMerged || persisted.MergedCommit != "merged-sha" || persisted.MergedAt == nil || !persisted.MergedAt.Equal(now) {
		t.Fatalf("persisted=%+v ok=%v", persisted, ok)
	}

	finishCleanup(errors.New("cleanup temporarily unavailable"))
	if persisted, ok := store.GetPullRequest(pullRequest.ID); !ok || persisted.Status != pullrequestlifecycle.StatusMerged {
		t.Fatalf("cleanup failure changed merge outcome: %+v", persisted)
	}
	// A replay must schedule another cleanup pass without merging on GitHub again.
	replayBody := mergeHTTP()
	if err := json.Unmarshal(replayBody, &body); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body["pull_request"], &merged); err != nil {
		t.Fatal(err)
	}
	if merged.Status != pullrequestlifecycle.StatusMerged || string(body["merged_commit"]) != `"merged-sha"` || client.calls != 1 {
		t.Fatalf("replayed merge = %s, provider calls = %d", replayBody, client.calls)
	}
	finishCleanup(nil)

	local, err := store.CreatePullRequest(pullrequestlifecycle.PullRequest{
		ID: "pr-local", RepositoryID: "repo", Title: "Local", BaseBranch: "main", BaseCommit: baseCommit,
		HeadBranch: "feature", HeadCommit: headCommit, Status: pullrequestlifecycle.StatusOpen, SyncData: json.RawMessage(`{}`), CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	localProjectionResponse := httptest.NewRecorder()
	handler.ServeHTTP(localProjectionResponse, httptest.NewRequest(http.MethodGet, "/api/v1/pull-requests/"+local.ID, nil))
	if localProjectionResponse.Code != http.StatusOK {
		t.Fatalf("local projection status=%d body=%s", localProjectionResponse.Code, localProjectionResponse.Body.String())
	}
	if err := json.Unmarshal(localProjectionResponse.Body.Bytes(), &projected); err != nil {
		t.Fatal(err)
	}
	if projected.Mergeable == nil || *projected.Mergeable || projected.MergeProvider != "local" || projected.MergeBlockedReason != "merge_provider_unsupported" {
		t.Fatalf("local projected=%+v", projected)
	}
	localMergeResponse := httptest.NewRecorder()
	localMergeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/"+local.ID+"/merge", strings.NewReader(`{"strategy":"squash"}`))
	localMergeRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(localMergeResponse, localMergeRequest)
	if localMergeResponse.Code != http.StatusConflict || !strings.Contains(localMergeResponse.Body.String(), `"code":"merge_provider_unsupported"`) || client.calls != 1 {
		t.Fatalf("local merge status=%d calls=%d body=%s", localMergeResponse.Code, client.calls, localMergeResponse.Body.String())
	}
}

type mergeCoordinatorStub struct {
	request pullrequestmerge.Request
	id      string
	err     error
}

func (coordinator *mergeCoordinatorStub) Project(_ context.Context, pullRequest pullrequestlifecycle.PullRequest) pullrequestlifecycle.PullRequest {
	return pullRequest
}

func (coordinator *mergeCoordinatorStub) Merge(_ context.Context, id string, request pullrequestmerge.Request) (pullrequestmerge.Result, error) {
	coordinator.id, coordinator.request = id, request
	return pullrequestmerge.Result{}, coordinator.err
}

func TestMergeHTTPMapsTypedFailures(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "invalid", err: pullrequestmerge.ErrInvalidRequest, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "merged", err: pullrequestmerge.ErrPullRequestMerged, status: http.StatusConflict, code: "pull_request_merged"},
		{name: "closed", err: pullrequestmerge.ErrPullRequestClosed, status: http.StatusConflict, code: "pull_request_closed"},
		{name: "comments", err: pullrequestmerge.ErrUnresolvedComments, status: http.StatusConflict, code: "unresolved_comments"},
		{name: "comment lookup", err: pullrequestmerge.ErrCommentLookupFailed, status: http.StatusInternalServerError, code: "pull_request_comment_failed"},
		{name: "unsupported", err: pullrequestmerge.ErrProviderUnsupported, status: http.StatusConflict, code: "merge_provider_unsupported"},
		{name: "github unavailable", err: &pullrequestmerge.GitHubError{Kind: pullrequestmerge.GitHubUnavailable}, status: http.StatusServiceUnavailable, code: "github_unavailable"},
		{name: "github blocked", err: &pullrequestmerge.GitHubError{Kind: pullrequestmerge.GitHubBlocked}, status: http.StatusConflict, code: "github_merge_blocked"},
		{name: "github head", err: &pullrequestmerge.GitHubError{Kind: pullrequestmerge.GitHubHeadChanged}, status: http.StatusConflict, code: "github_head_changed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coordinator := &mergeCoordinatorStub{err: test.err}
			handler := pullRequestHTTP(Options{Merge: coordinator})
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/pr-1/merge", strings.NewReader(`{"strategy":"squash","bypass_unresolved_comments":true}`))
			request.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(response, request)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if coordinator.id != "pr-1" || coordinator.request.Strategy != "squash" || !coordinator.request.BypassUnresolvedComments {
				t.Fatalf("id=%q request=%+v", coordinator.id, coordinator.request)
			}
		})
	}
}

type httpContinuePublisher struct {
	calls int
	work  pullrequestwork.Work
	err   error
}

func (publisher *httpContinuePublisher) PublishLatestContinue(_ context.Context, pullRequestID, requestID string) (pullrequestwork.Work, error) {
	publisher.calls++
	if publisher.work.PullRequestID != pullRequestID {
		return pullrequestwork.Work{}, pullrequestwork.ErrNotFound
	}
	return publisher.work, publisher.err
}

func TestPublishDelegatesToLatestContinueWorker(t *testing.T) {
	now := time.Now().UTC()
	pr := pullrequestlifecycle.PullRequest{
		ID: "pr-1", RepositoryID: "repo", Title: "Change", Status: pullrequestlifecycle.StatusOpen,
		BaseBranch: "main", BaseCommit: "base", HeadBranch: "feature", HeadCommit: "published", CreatedAt: now, UpdatedAt: now,
	}
	publisher := &httpContinuePublisher{work: pullrequestwork.Work{
		ID: "worker", PullRequestID: pr.ID, SessionID: "holon", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeContinue,
		Status: pullrequestwork.StatusRunning, HeadBranch: "feature", HeadCommit: "published", BaseHeadCommit: "published", ResultHeadCommit: "published", CreatedAt: now,
	}}
	handler := pullRequestHTTP(Options{RepositoryID: "repo", Catalog: httpCatalog{items: []pullrequestlifecycle.PullRequest{pr}}, ContinuePublisher: publisher})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/pr-1/publish", nil))
	if response.Code != http.StatusOK || publisher.calls != 1 {
		t.Fatalf("status=%d body=%s calls=%d", response.Code, response.Body.String(), publisher.calls)
	}
	var body struct {
		HeadCommit string                 `json:"head_commit"`
		Worker     pullrequestwork.Work   `json:"worker"`
		Pull       map[string]interface{} `json:"pull_request"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.HeadCommit != "published" || body.Worker.ID != "worker" || body.Pull["head_commit"] != "published" {
		t.Fatalf("body=%s", response.Body.String())
	}
}
