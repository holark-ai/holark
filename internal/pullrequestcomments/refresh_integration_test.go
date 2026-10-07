//go:build integration

package pullrequestcomments_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	githubpullrequestcomments "github.com/holark-ai/holark/internal/codehost/github/pullrequestcomments"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	commentshttp "github.com/holark-ai/holark/internal/pullrequestcomments/httpapi"
	"github.com/holark-ai/holark/internal/pullrequestcomments/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

type refreshGateway struct {
	lifecycleGateway
	indicators                 pullrequestcomments.CommentIndicators
	indicatorErr, listErr      error
	checks, lists              atomic.Int32
	entered, release           chan struct{}
	checkEntered, checkRelease chan struct{}
}

func (g *refreshGateway) CommentIndicators(ctx context.Context, _ pullrequestcomments.ProviderTarget) (pullrequestcomments.CommentIndicators, error) {
	g.checks.Add(1)
	if g.checkEntered != nil {
		select {
		case g.checkEntered <- struct{}{}:
		default:
		}
		select {
		case <-g.checkRelease:
		case <-ctx.Done():
			return pullrequestcomments.CommentIndicators{}, ctx.Err()
		}
	}
	return g.indicators, g.indicatorErr
}
func (g *refreshGateway) ListComments(ctx context.Context, target pullrequestcomments.ProviderTarget) ([]pullrequestcomments.RemoteComment, error) {
	g.lists.Add(1)
	if g.entered != nil {
		select {
		case g.entered <- struct{}{}:
		default:
		}
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if g.listErr != nil {
		return nil, g.listErr
	}
	return g.lifecycleGateway.ListComments(ctx, target)
}
func refreshService(t *testing.T, gateway *refreshGateway) (*pullrequestcomments.Service, *atomic.Int64) {
	t.Helper()
	store, err := sqliteadapter.New(t.Context(), openCommentsDB(t))
	if err != nil {
		t.Fatal(err)
	}
	clock := &atomic.Int64{}
	clock.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	scheduler := pullrequestlifecycle.NewRefreshCoordinator()
	t.Cleanup(scheduler.Close)
	service := pullrequestcomments.NewService(store, providerTargets(),
		pullrequestcomments.WithProviderGateway(gateway, nil),
		pullrequestcomments.WithWorkerReferenceReader(workerReferences{}),
		pullrequestcomments.WithRefreshClock(func() time.Time { return time.Unix(0, clock.Load()).UTC() }),
		pullrequestcomments.WithRefreshScheduler(pullrequestcomments.RefreshSchedulerFunc(func(ctx context.Context, repo, pr string, work func(context.Context) error) error {
			return scheduler.Do(ctx, pullrequestlifecycle.RefreshKey{RepositoryID: repo, PullRequestID: pr, Section: "comments"}, work)
		})),
	)
	return service, clock
}
func requireRefresh(t *testing.T, service *pullrequestcomments.Service, force, refreshed bool) pullrequestcomments.RefreshResult {
	t.Helper()
	result, err := service.Refresh(t.Context(), "pr-one", force)
	if err != nil || result.Refreshed != refreshed || !result.SyncApplicable {
		t.Fatalf("refresh = %+v, %v", result, err)
	}
	return result
}
func TestCommentRefreshIndicatorsAndFallbackReconcile(t *testing.T) {
	gateway := &refreshGateway{indicators: pullrequestcomments.CommentIndicators{UpdatedAt: "initial", TotalCommentsCount: 2}}
	gateway.remote = []pullrequestcomments.RemoteComment{
		{ProviderIdentity: pullrequestcomments.ProviderIdentity{Provider: "github", ExternalID: "inline:1", Kind: "inline"}, Body: "original", Scope: pullrequestcomments.ScopeFile, Path: "main.go"},
		{ProviderIdentity: pullrequestcomments.ProviderIdentity{Provider: "github", ExternalID: "conversation:2"}, Body: "deleted later"},
	}
	service, clock := refreshService(t, gateway)
	first := requireRefresh(t, service, false, true)
	requireRefresh(t, service, false, false)
	if gateway.checks.Load() != 1 {
		t.Fatal("fresh check reached provider")
	}
	// An edit, deletion and resolution can be invisible to the hints.
	gateway.remote = gateway.remote[:1]
	gateway.remote[0].Body, gateway.remote[0].Resolved = "edited", true
	for range 7 {
		clock.Add(int64(15 * time.Second))
		result := requireRefresh(t, service, false, false)
		if !result.SyncedAt.Equal(*first.SyncedAt) {
			t.Fatal("indicator check reset full-sync deadline")
		}
	}
	if gateway.lists.Load() != 1 {
		t.Fatal("unchanged indicators fetched bodies")
	}
	clock.Add(int64(15 * time.Second))
	requireRefresh(t, service, false, true)
	comments, err := service.ListByPullRequest(t.Context(), "pr-one")
	if err != nil || len(comments) != 1 || comments[0].Body != "edited" || comments[0].Status != pullrequestcomments.Resolved {
		t.Fatalf("fallback comments: %+v, %v", comments, err)
	}
	clock.Add(int64(15 * time.Second))
	gateway.indicators.TotalCommentsCount++
	requireRefresh(t, service, false, true)
	if gateway.lists.Load() != 3 {
		t.Fatal("changed indicator did not reconcile")
	}
	// Legacy synchronization shares the baseline and successful timestamp.
	if _, err := service.Sync(t.Context(), "pr-one"); err != nil {
		t.Fatal(err)
	}
	requireRefresh(t, service, false, false)
	if gateway.lists.Load() != 4 {
		t.Fatal("legacy synchronization did not share state")
	}
}

func TestCommentRefreshFailuresPreserveDeadlineAndBackOff(t *testing.T) {
	gateway := &refreshGateway{}
	service, clock := refreshService(t, gateway)
	first := requireRefresh(t, service, false, true)
	clock.Add(int64(2 * time.Minute))
	gateway.listErr = errors.New("offline")
	for _, delay := range []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 2 * time.Minute} {
		result, err := service.Refresh(t.Context(), "pr-one", false)
		if err == nil || !result.SyncedAt.Equal(*first.SyncedAt) {
			t.Fatalf("failed sync changed timestamp: %+v %v", result, err)
		}
		calls := gateway.lists.Load()
		clock.Add(int64(delay - time.Second))
		if _, err := service.Refresh(t.Context(), "pr-one", false); err == nil || gateway.lists.Load() != calls {
			t.Fatal("backoff did not preserve failure")
		}
		clock.Add(int64(time.Second))
	}
	gateway.listErr = nil
	// Even failed indicator reads cannot prevent an already-due reconciliation.
	gateway.indicatorErr = errors.New("indicators unavailable")
	requireRefresh(t, service, false, true)
	clock.Add(int64(15 * time.Second))
	gateway.indicatorErr = nil
	requireRefresh(t, service, false, true) // No accepted pre-sync baseline.
	gateway.indicatorErr = pullrequestcomments.RetryableProviderError(errors.New("rate limited"), 5*time.Minute)
	clock.Add(int64(15 * time.Second))
	if _, err := service.Refresh(t.Context(), "pr-one", false); err == nil {
		t.Fatal("indicator failure hidden")
	}
	checks := gateway.checks.Load()
	if _, err := service.Refresh(t.Context(), "pr-one", true); err == nil || gateway.checks.Load() != checks {
		t.Fatal("manual refresh bypassed provider deadline")
	}
	clock.Add(int64(5 * time.Minute))
	gateway.indicatorErr = nil
	requireRefresh(t, service, true, true)
	clock.Add(int64(2 * time.Minute))
	gateway.indicatorErr = pullrequestcomments.RetryableProviderError(errors.New("hints rate limited"), 5*time.Minute)
	gateway.listErr = errors.New("full sync failed")
	if _, err := service.Refresh(t.Context(), "pr-one", false); err == nil {
		t.Fatal("failed due sync hidden")
	}
	checks = gateway.checks.Load()
	clock.Add(int64(2 * time.Minute))
	if _, err := service.Refresh(t.Context(), "pr-one", true); err == nil || gateway.checks.Load() != checks {
		t.Fatal("full-sync failure lost the indicator retry deadline")
	}
}

func TestDueCommentRefreshHonorsIndicatorBackoff(t *testing.T) {
	for _, mode := range []string{"initial", "manual", "periodic"} {
		for _, failure := range []struct {
			name  string
			err   error
			delay time.Duration
		}{
			{"retry-after", pullrequestcomments.RetryableProviderError(errors.New("retry later"), 5*time.Minute), 5 * time.Minute},
			{"rate-limit", pullrequestcomments.RateLimitedProviderError(errors.New("rate limited"), 0), 15 * time.Second},
			{"ordinary", errors.New("indicators unavailable"), 0},
		} {
			t.Run(mode+"/"+failure.name, func(t *testing.T) {
				gateway := &refreshGateway{}
				service, clock := refreshService(t, gateway)
				var previous pullrequestcomments.RefreshResult
				if mode != "initial" {
					previous = requireRefresh(t, service, false, true)
				}
				if mode == "periodic" {
					clock.Add(int64(2 * time.Minute))
				}
				gateway.indicatorErr = failure.err
				lists := gateway.lists.Load()
				if failure.delay == 0 {
					requireRefresh(t, service, mode == "manual", true)
					if gateway.lists.Load() != lists+1 {
						t.Fatal("ordinary indicator failure prevented due synchronization")
					}
					return
				}
				result, err := service.Refresh(t.Context(), "pr-one", mode == "manual")
				if !errors.Is(err, failure.err) || result.Refreshed || gateway.lists.Load() != lists {
					t.Fatalf("indicator backoff did not stop synchronization: %+v, %v", result, err)
				}
				if previous.SyncedAt == nil {
					if result.SyncedAt != nil {
						t.Fatal("failed initial sync recorded a successful timestamp")
					}
				} else if result.SyncedAt == nil || !result.SyncedAt.Equal(*previous.SyncedAt) {
					t.Fatal("indicator failure changed successful sync timestamp")
				}
				checks := gateway.checks.Load()
				gateway.indicatorErr = nil
				clock.Add(int64(failure.delay - time.Second))
				for _, force := range []bool{false, true} {
					if _, err := service.Refresh(t.Context(), "pr-one", force); !errors.Is(err, failure.err) || gateway.checks.Load() != checks || gateway.lists.Load() != lists {
						t.Fatal("refresh bypassed provider backoff")
					}
				}
				clock.Add(int64(time.Second))
				requireRefresh(t, service, true, true)
			})
		}
	}
}

// Signal when Refresh has joined the shared pass and starts waiting for it.
type refreshWaitContext struct {
	context.Context
	waiting chan struct{}
}

func (ctx refreshWaitContext) Done() <-chan struct{} {
	ctx.waiting <- struct{}{}
	return ctx.Context.Done()
}

func TestForcedCommentRefreshJoinsAutomaticCheck(t *testing.T) {
	gateway := &refreshGateway{}
	gateway.remote = []pullrequestcomments.RemoteComment{
		{ProviderIdentity: pullrequestcomments.ProviderIdentity{Provider: "github", ExternalID: "inline:1", Kind: "inline"}, Body: "original", Scope: pullrequestcomments.ScopeFile, Path: "main.go"},
	}
	service, clock := refreshService(t, gateway)
	requireRefresh(t, service, false, true)
	gateway.remote[0].Body, gateway.remote[0].Resolved = "edited", true
	clock.Add(int64(15 * time.Second))
	gateway.checkEntered, gateway.checkRelease = make(chan struct{}, 1), make(chan struct{})
	type outcome struct {
		result pullrequestcomments.RefreshResult
		err    error
	}
	results := make(chan outcome, 3)
	refresh := func(ctx context.Context, force bool) {
		result, err := service.Refresh(ctx, "pr-one", force)
		results <- outcome{result, err}
	}
	go refresh(t.Context(), false)
	<-gateway.checkEntered
	waiting := make(chan struct{}, 2)
	for range 2 {
		go refresh(refreshWaitContext{Context: t.Context(), waiting: waiting}, true)
	}
	// Both manual callers must join while the automatic check is in progress.
	for range 2 {
		<-waiting
	}
	close(gateway.checkRelease)
	for range 3 {
		got := <-results
		if got.err != nil || !got.result.Refreshed {
			t.Fatalf("shared refresh = %+v, %v", got.result, got.err)
		}
	}
	if gateway.lists.Load() != 2 {
		t.Fatalf("expected initial sync and one forced sync, got %d", gateway.lists.Load())
	}
	comments, err := service.ListByPullRequest(t.Context(), "pr-one")
	if err != nil || len(comments) != 1 || comments[0].Body != "edited" || comments[0].Status != pullrequestcomments.Resolved {
		t.Fatalf("forced refresh comments: %+v, %v", comments, err)
	}
}

func TestCommentRefreshSharedWorkDoesNotBlockCachedHTTPReads(t *testing.T) {
	gateway := &refreshGateway{}
	service, _ := refreshService(t, gateway)
	comment, err := createAndPublish(t, service, pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "Saved comment"})
	if err != nil {
		t.Fatal(err)
	}
	gateway.lists.Store(0)
	gateway.entered, gateway.release = make(chan struct{}, 1), make(chan struct{})
	var tabs sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		tabs.Add(1)
		go func() { defer tabs.Done(); _, err := service.Refresh(t.Context(), "pr-one", false); errs <- err }()
	}
	<-gateway.entered
	mux := http.NewServeMux()
	commentshttp.RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, service, providerTargets())
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/pull-requests/pr-one/comments", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), comment.Body) {
		t.Fatalf("cache read while provider blocked: %s", response.Body.String())
	}
	// A disconnect must not cancel the shared synchronization.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.Refresh(ctx, "pr-one", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v", err)
	}
	close(gateway.release)
	tabs.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if gateway.lists.Load() != 1 {
		t.Fatalf("tabs ran %d full synchronizations", gateway.lists.Load())
	}
	gateway.listErr = errors.New("offline")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest("POST", "/api/v1/pull-requests/pr-one/comments/refresh", strings.NewReader(`{"force":true}`)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("refresh disguised failure: %d", response.Code)
	}
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/pull-requests/pr-one/comments", nil))
	if !strings.Contains(response.Body.String(), comment.Body) {
		t.Fatal("failure lost cached comment")
	}
}

type indicatorGitHubClient struct {
	githubLifecycleClient
	t                 *testing.T
	snapshot          map[string]any
	checks, bodyReads int
}

func (client *indicatorGitHubClient) GraphQL(ctx context.Context, query string, variables map[string]any, response any) error {
	if !strings.Contains(query, "totalCommentsCount") {
		return client.githubLifecycleClient.GraphQL(ctx, query, variables, response)
	}
	client.checks++
	for _, forbidden := range []string{"nodes", "body", "pageInfo", "first:", "after:"} {
		if strings.Contains(query, forbidden) {
			client.t.Errorf("indicator query requests %s", forbidden)
		}
	}
	if variables["owner"] != "owner" || variables["name"] != "repo" || variables["number"] != 7 {
		client.t.Errorf("indicator target: %+v", variables)
	}
	return decodeGitHubFixture(response, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": client.snapshot}}})
}
func (client *indicatorGitHubClient) Request(ctx context.Context, method, path string, input, response any) error {
	client.bodyReads++
	return client.githubLifecycleClient.Request(ctx, method, path, input, response)
}

func TestGitHubCommentIndicatorsDriveCachedRefresh(t *testing.T) {
	store, err := sqliteadapter.New(t.Context(), openCommentsDB(t))
	if err != nil {
		t.Fatal(err)
	}
	client := &indicatorGitHubClient{t: t, snapshot: map[string]any{
		"updatedAt": "2026-01-01T00:00:00Z", "totalCommentsCount": 1,
		"comments": map[string]int{"totalCount": 1}, "reviewThreads": map[string]int{"totalCount": 0},
	}}
	client.comments = map[int][]githubLifecycleComment{7: {lifecycleIssueComment(1, "Cached GitHub comment", 0)}}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	gateway := githubpullrequestcomments.New(client)
	newService := func() *pullrequestcomments.Service {
		return pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil), pullrequestcomments.WithRefreshClock(func() time.Time { return now }))
	}
	service := newService()
	requireRefresh(t, service, false, true)
	reads := client.bodyReads
	now = now.Add(15 * time.Second)
	requireRefresh(t, service, false, false)
	if client.bodyReads != reads {
		t.Fatal("unchanged indicators fetched comment bodies")
	}
	for key, value := range map[string]any{
		"updatedAt": "2026-01-01T01:00:00Z", "totalCommentsCount": 2,
		"comments": map[string]int{"totalCount": 2}, "reviewThreads": map[string]int{"totalCount": 1},
	} {
		client.snapshot[key] = value
		now = now.Add(15 * time.Second)
		requireRefresh(t, service, false, true)
	}
	// Process-local scheduling starts afresh, without losing persisted comments.
	service = newService()
	cached, err := service.ListByPullRequest(t.Context(), "pr-one")
	if err != nil || len(cached) != 1 || cached[0].Body != "Cached GitHub comment" {
		t.Fatalf("restart cache: %+v %v", cached, err)
	}
	requireRefresh(t, service, false, true)
}
