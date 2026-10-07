package pullrequestlifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	githubprovider "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle/httpapi"
	"github.com/holark-ai/holark/internal/pullrequestmerge"
	"github.com/holark-ai/holark/internal/repository"
)

type mergePreparationGitHub struct {
	*workGitHub
	readinessHook func(context.Context, pr.GitHubReadiness) (pr.GitHubReadiness, error)
	squashHook    func()
	t             *testing.T
	fixture       *branchCacheFixture
	calls         int
}

func (g *mergePreparationGitHub) RefreshReadiness(ctx context.Context, target pr.GitHubPullRequestTarget) (pr.GitHubReadiness, error) {
	r, err := g.publicationGitHub.RefreshReadiness(ctx, target)
	if err == nil && g.readinessHook != nil {
		return g.readinessHook(ctx, r)
	}
	return r, err
}
func (g *mergePreparationGitHub) Readiness(target pullrequestmerge.GitHubTarget) (pullrequestmerge.GitHubReadiness, bool) {
	return githubprovider.New(nil).Readiness(target)
}
func (g *mergePreparationGitHub) Squash(ctx context.Context, request pullrequestmerge.GitHubMergeRequest) (pullrequestmerge.GitHubMergeResult, error) {
	g.calls++
	if g.squashHook != nil {
		g.squashHook()
	}
	// Model GitHub's expected-head guard against the real remote, not the catalog.
	if gitSync(g.t, g.fixture.remote, "rev-parse", "refs/heads/feature") != request.ExpectedHeadSHA {
		return pullrequestmerge.GitHubMergeResult{}, &pullrequestmerge.GitHubError{Kind: pullrequestmerge.GitHubHeadChanged}
	}
	tree := gitSync(g.t, g.fixture.remote, "rev-parse", request.ExpectedHeadSHA+"^{tree}")
	commit := gitSync(g.t, g.fixture.remote, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit-tree", tree, "-p", g.fixture.base, "-m", request.Title)
	gitSync(g.t, g.fixture.remote, "update-ref", "refs/heads/main", commit, g.fixture.base)
	return pullrequestmerge.GitHubMergeResult{MergedCommit: commit}, nil
}

type mergePreparationComments int

func (c mergePreparationComments) UnresolvedCount(context.Context, string) (int, error) {
	return int(c), nil
}

type syncOnlyPreparation struct{ pr.Synchronizer }

func TestMergePreparationHTTP(t *testing.T) {
	for _, scenario := range []string{"merge and replay", "legacy sync", "blocked", "unknown", "readiness failure", "comments", "bypass comments", "head moves at merge"} {
		t.Run(scenario, func(t *testing.T) {
			f, transport := publicationFixture(t)
			g := &mergePreparationGitHub{workGitHub: &workGitHub{publicationGitHub: transport}, fixture: f, t: t}
			var comparisons, writes, observations atomic.Int32
			repo := observedCacheRepository{syncRepository: f.repo, afterMergeBase: func() { comparisons.Add(1) }, afterCache: func(string, error) { writes.Add(1) }, afterObserve: func(map[string]repository.BranchObservation) { observations.Add(1) }}
			c := publicationCoordinator(f, g, repo)
			var syncer pr.Synchronizer = c
			if scenario == "legacy sync" {
				syncer = syncOnlyPreparation{c}
			}
			wantStatus, wantCalls, reason := 200, 1, ""
			comments := mergePreparationComments(0)
			switch scenario {
			case "blocked", "unknown":
				wantStatus, wantCalls = 409, 0
				reason = "github_merge_blocked"
				if scenario == "unknown" {
					reason = "github_mergeability_unknown"
				}
				g.readinessHook = func(_ context.Context, r pr.GitHubReadiness) (pr.GitHubReadiness, error) {
					r.MergeabilityState = pr.GitHubMergeabilityBlocked
					if scenario == "unknown" {
						r.MergeabilityState = pr.GitHubMergeabilityUnknown
					}
					return r, nil
				}
			case "readiness failure":
				wantStatus, wantCalls = 500, 0
				g.readinessHook = func(context.Context, pr.GitHubReadiness) (pr.GitHubReadiness, error) {
					return pr.GitHubReadiness{}, errors.New("readiness transport failed")
				}
			case "comments":
				comments, wantStatus, wantCalls = 1, 409, 0
			case "bypass comments":
				comments = 1
			case "head moves at merge":
				wantStatus = 409
				g.squashHook = func() { f.advance(t) }
			}
			merger := pullrequestmerge.New(pullrequestmerge.Options{Sync: syncer, Store: f.store, Repository: f.repo.Service, GitHub: g, Comments: comments})
			mux := http.NewServeMux()
			httpapi.RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, httpapi.Options{RepositoryID: "project", Catalog: f.store, Lifecycle: c, Merge: merger})
			request := func() *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				r := httptest.NewRequest("POST", "/api/v1/pull-requests/"+f.pull.ID+"/merge", strings.NewReader(fmt.Sprintf(`{"strategy":"squash","request_id":"merge-test","bypass_unresolved_comments":%t}`, scenario == "bypass comments")))
				r.Header.Set("Content-Type", "application/json")
				mux.ServeHTTP(w, r)
				return w
			}
			w := request()
			if w.Code != wantStatus || !strings.Contains(w.Body.String(), reason) || g.calls != wantCalls {
				t.Fatalf("HTTP=%d body=%s merge calls=%d", w.Code, w.Body.String(), g.calls)
			}
			if scenario != "legacy sync" && (g.requests.Load() != 1 || g.gets.Load() != 0 || comparisons.Load() != 0 || writes.Load() != 0 || observations.Load() != 0) {
				t.Fatalf("unexpected preparation: focused=%d full=%d comparison=%d writes=%d observations=%d", g.requests.Load(), g.gets.Load(), comparisons.Load(), writes.Load(), observations.Load())
			}
			if g.readiness.Load() != 1 {
				t.Fatalf("readiness=%d", g.readiness.Load())
			}
			p, _ := f.store.GetPullRequest(f.pull.ID)
			if wantStatus == 200 {
				if p.Status != pr.StatusMerged || p.MergedCommit == "" || gitSync(t, f.remote, "rev-parse", "refs/heads/main") != p.MergedCommit {
					t.Fatalf("merge not recorded: %+v", p)
				}
				if replay := request(); replay.Code != 200 || g.calls != 1 || g.readiness.Load() != 1 {
					t.Fatalf("replay=%d %s calls=%d", replay.Code, replay.Body.String(), g.calls)
				}
			} else if p.Status == pr.StatusMerged {
				t.Fatal("blocked request recorded a merge")
			}
		})
	}
}

func TestMergePreparationChangesDuringReadiness(t *testing.T) {
	for _, scenario := range []string{"metadata", "lifecycle", "comparison", "head", "identical observation", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			f, transport := publicationFixture(t)
			g := &mergePreparationGitHub{workGitHub: &workGitHub{publicationGitHub: transport}, fixture: f, t: t}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			g.readinessHook = func(_ context.Context, r pr.GitHubReadiness) (pr.GitHubReadiness, error) {
				if g.readiness.Load() != 1 {
					return r, nil
				}
				p, _ := f.store.GetPullRequest(f.pull.ID)
				switch scenario {
				case "metadata":
					if err := f.store.UpdatePullRequestMetadata(p.ID, "Concurrent title", p.Summary, time.Now()); err != nil {
						t.Error(err)
					}
				case "lifecycle":
					completeWorkLifecycleAction(t, f, "during-readiness", pr.StatusOpen)
				case "comparison":
					if err := f.store.InvalidateComparison(ctx, p.ID); err != nil {
						t.Error(err)
					}
				case "head":
					next := f.advance(t)
					g.pullRequests[0].HeadCommit = next
					r.HeadCommit = next
				case "identical observation":
					token, err := f.store.BeginObservation(ctx, p.RepositoryID, []pr.FieldGroup{pr.LifecycleGroup, pr.TopologyGroup, pr.MetadataGroup})
					if err != nil {
						t.Error(err)
					}
					if _, _, err = f.store.UpsertObservedPullRequests(p.RepositoryID, []pr.PullRequest{p}, token); err != nil {
						t.Error(err)
					}
					read, err := f.store.BeginBranchObservation(ctx, []repository.BranchIdentity{p.BaseRef, p.HeadRef})
					if err != nil {
						t.Error(err)
					}
					if err = f.store.AcceptBranchObservation(ctx, read, map[string]repository.BranchObservation{p.BaseRef.Ref: {Commit: p.BaseCommit, Exists: true}, p.HeadRef.Ref: {Commit: p.HeadCommit, Exists: true}}); err != nil {
						t.Error(err)
					}
				case "cancel":
					cancel()
					return r, context.Canceled
				}
				return r, nil
			}
			err := publicationCoordinator(f, g, f.repo).PrepareMerge(ctx, f.pull.ID)
			f.scheduler.Close()
			wantSync := int32(1)
			if scenario == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel=%v", err)
				}
				wantSync = 0
			} else if err != nil {
				t.Fatal(err)
			}
			if scenario == "identical observation" {
				wantSync = 0
			}
			if g.gets.Load() != wantSync || g.requests.Load() != 1 {
				t.Fatalf("focused=%d full=%d", g.requests.Load(), g.gets.Load())
			}
		})
	}
}
