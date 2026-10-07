package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	githubprovider "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	"github.com/holark-ai/holark/internal/database"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestmerge"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
)

func cachedReadCatalog(t *testing.T) *prsqlite.Store {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`create table repositories(id text primary key); insert into repositories values('repo')`); err != nil {
		t.Fatal(err)
	}
	catalog, err := prsqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

type cachedReadRepository struct {
	service           *repository.Service
	fetches, resolves int
}

func (r *cachedReadRepository) Refresh(ctx context.Context) error {
	r.fetches++
	_, err := r.service.Refresh(ctx)
	return err
}
func (r *cachedReadRepository) Resolve(ctx context.Context, ref string) (string, error) {
	r.resolves++
	return r.service.Resolve(ctx, ref)
}

func (r *cachedReadRepository) ResolveProviderBranch(ctx context.Context, branch string) (string, error) {
	return r.service.ResolveProviderBranch(ctx, branch)
}

func (r *cachedReadRepository) PrepareBranch(ctx context.Context, branch string) (repository.Preparation, error) {
	return r.service.PrepareBranch(ctx, branch)
}

func (r *cachedReadRepository) MergeBase(ctx context.Context, base, head string) (string, error) {
	return r.service.MergeBase(ctx, base, head)
}

func TestCachedListDetailAndHolonReadsDoNotFetchMissingPRHead(t *testing.T) {
	root := t.TempDir()
	runMergeGit(t, root, "init", "-b", "main")
	runMergeGit(t, root, "config", "user.name", "Test")
	runMergeGit(t, root, "config", "user.email", "test@example.com")
	runMergeGit(t, root, "commit", "--allow-empty", "-m", "base")
	base := runMergeGit(t, root, "rev-parse", "HEAD")
	git, err := gitadapter.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	repo := &cachedReadRepository{service: repository.NewService(git)}
	catalog := cachedReadCatalog(t)
	current, err := catalog.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Title: "Cached changes", Status: pr.StatusOpen, BaseBranch: "main", BaseCommit: base, HeadBranch: "feature", HeadCommit: strings.Repeat("a", 40), SyncProvider: "github", SyncExternalID: "github:owner/repo#1", LinkedHolonIDs: []string{"holon-1"}, SyncData: json.RawMessage(`{"github":{"number":1}}`)})
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	merger := pullrequestmerge.New(pullrequestmerge.Options{Store: catalog, Repository: repo})
	handler := pullRequestHTTP(Options{RepositoryID: "repo", Catalog: catalog, Merge: merger, Lifecycle: syncLifecycle{events: &events}})
	for _, path := range []string{"/api/v1/pull-requests/" + current.ID, "/api/v1/pull-requests", "/api/v1/holons/holon-1/pull-request", "/api/v1/pull-requests/" + current.ID} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Cached changes") {
			t.Fatalf("GET %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	if repo.fetches != 0 || len(events) != 0 {
		t.Fatalf("cached reads performed remote work: fetches=%d events=%v", repo.fetches, events)
	}
	// Unavailable comparisons cannot advertise readiness or trigger Git work.
	if repo.resolves != 0 {
		t.Fatalf("unavailable comparison inspected a moving/local head, got %d", repo.resolves)
	}
}

type basicHTTPTransport struct {
	pr.GitHubTransport
	activeCalls, historicalCalls, readinessCalls int
	remote                                       pr.GitHubPullRequest
}

func (p *basicHTTPTransport) ListActive(context.Context, string) ([]pr.GitHubPullRequest, error) {
	p.activeCalls++
	return []pr.GitHubPullRequest{p.remote}, nil
}
func (p *basicHTTPTransport) List(context.Context, string) ([]pr.GitHubPullRequest, error) {
	p.historicalCalls++
	return nil, errors.New("unexpected historical synchronization")
}
func (p *basicHTTPTransport) RefreshReadiness(context.Context, pr.GitHubPullRequestTarget) (pr.GitHubReadiness, error) {
	p.readinessCalls++
	return pr.GitHubReadiness{}, errors.New("unexpected readiness synchronization")
}

func TestBasicHTTPRefreshImportsWithoutReadinessOrComments(t *testing.T) {
	catalog := cachedReadCatalog(t)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	provider := &basicHTTPTransport{remote: pr.GitHubPullRequest{Title: "Imported basic PR", Status: pr.StatusOpen, ExternalID: "github:owner/repo#1", BaseBranch: "main", BaseCommit: "base", HeadBranch: "feature", HeadCommit: "head", SyncData: json.RawMessage(`{"github":{"number":1}}`), CreatedAt: now, UpdatedAt: now}}
	lifecycle := pr.New(catalog, pr.Options{Projects: pr.ProjectLookupFunc(func(string) (pr.Project, bool) {
		return pr.Project{ID: "repo", RepositoryURL: "https://github.com/owner/repo", GitHubBacked: true}, true
	}), GitHubTransport: provider, GitHubCodec: githubprovider.New(nil), Clock: pr.ClockFunc(func() time.Time { return now })})
	events := []string{}
	handler := pullRequestHTTP(Options{RepositoryID: "repo", Catalog: catalog, Lifecycle: lifecycle})
	for pass := 0; pass < 2; pass++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/sync?scope=active", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("basic sync: %d %s", response.Code, response.Body.String())
		}
		var result pr.SyncResult
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.PullRequests) != 1 || result.PullRequests[0].Title != "Imported basic PR" || result.SyncedAt != now {
			t.Fatalf("basic response: %+v", result)
		}
		if pass == 0 && result.Imported != 1 {
			t.Fatalf("missing import counter: %+v", result)
		}
	}
	if provider.activeCalls != 2 || provider.historicalCalls != 0 || provider.readinessCalls != 0 || len(events) != 0 {
		t.Fatalf("basic refresh performed expensive work: provider=%+v events=%v", provider, events)
	}
}
