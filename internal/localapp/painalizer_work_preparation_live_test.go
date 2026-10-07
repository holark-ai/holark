//go:build painalizer_live

package localapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	githubprovider "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
)

func TestPainalizerLiveWorkPreparation(t *testing.T) {
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(logger)
	app, p, repo := liveWorkFixture(t, false)
	auditWorkPreparation(t, app, p, repo)
}

func TestPainalizerLiveWorkerStartup(t *testing.T) {
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(logger)
	for _, mode := range []string{"continue_full_sync", "continue", "address_full_sync", "address"} {
		t.Run(mode, func(t *testing.T) {
			app, p, _ := liveWorkFixture(t, strings.HasSuffix(mode, "_full_sync"))
			liveWorkRequest(t, app, "PUT", "/api/v1/settings/default-agent-harness", map[string]any{"harness_type": "codex"}, 200, nil)
			body := map[string]any{"mode": "continue", "prompt": "This is an authorized startup timing test. Do not modify, commit, or push any files. Respond READY and wait for further instructions."}
			if strings.HasPrefix(mode, "address") {
				var comment pullrequestcomments.Comment
				liveWorkRequest(t, app, "POST", "/api/v1/pull-requests/"+p.ID+"/comments", map[string]any{"scope": "pull_request", "body": "[AUTH TEST] Disposable startup timing check; no code changes requested."}, 201, &comment)
				body["mode"], body["comment_ids"] = "auto", []string{comment.ID}
			}
			var work []pullrequestwork.Work
			started := time.Now()
			liveWorkRequest(t, app, "POST", "/api/v1/pull-requests/"+p.ID+"/workers", body, 201, &work)
			t.Logf("WORKER_STARTUP mode=%s duration=%s", mode, time.Since(started))
			if len(work) != 1 || work[0].SessionID == "" || work[0].BaseCommit != p.BaseCommit || work[0].HeadCommit != p.HeadCommit {
				t.Fatalf("worker did not pin accepted commits: %+v", work)
			}
			if err := app.PullRequestWork.CancelPullRequest(t.Context(), p.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func liveWorkRequest(t *testing.T, app *Application, method, path string, body any, status int, result any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Request-ID", fmt.Sprintf("work-measurement-%d", time.Now().UnixNano()))
	w := httptest.NewRecorder()
	app.Handler.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: %d want %d %s", method, path, w.Code, status, w.Body.String())
	}
	if result != nil {
		if err = json.Unmarshal(w.Body.Bytes(), result); err != nil {
			t.Fatal(err)
		}
	}
}

// Reuse the live harness's real Git/GitHub/application setup, with a draft PR
// created directly so unrelated WIP publication and metadata agents are outside
// the startup measurements. Only disposable branches and PRs are mutated.
func liveWorkFixture(t *testing.T, fullSync bool) (*Application, pr.PullRequest, string) {
	app, p, repo, _ := liveWorkFixtureWithPublishedHead(t, fullSync)
	return app, p, repo
}

func liveWorkFixtureWithPublishedHead(t *testing.T, fullSync bool) (*Application, pr.PullRequest, string, func(string)) {
	t.Helper()
	if os.Getenv("HOLARK_LIVE_TEST_REPOSITORY") != "tt10612/painalizer" {
		t.Skip("requires explicit painalizer live opt-in")
	}
	root, err := os.MkdirTemp("/tmp", "painalizer-work-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Evidence and isolated application state: %s", root)
	repo := filepath.Join(root, "repo")
	gitPlumbingCommand(t, root, "clone", "git@github.com:tt10612/painalizer.git", repo)
	git := func(args ...string) string { t.Helper(); return gitPlumbingOutput(t, repo, args...) }
	gh := func(args ...string) string {
		t.Helper()
		c := exec.CommandContext(t.Context(), "gh", args...)
		c.Dir = repo
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("gh %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	baseCommit := git("rev-parse", "origin/main")
	prefix := fmt.Sprintf("holark-work-%d", time.Now().UnixNano())
	base, head := prefix+"-base", prefix+"-head"
	var app *Application
	var number int
	expected := map[string]string{}
	t.Cleanup(func() {
		if app != nil {
			if err := app.Close(); err != nil {
				t.Error(err)
			}
		}
		cleanup := func(name string, args ...string) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			c := exec.CommandContext(ctx, name, args...)
			c.Dir = repo
			if out, err := c.CombinedOutput(); err != nil {
				t.Errorf("cleanup %s %v: %v %s", name, args, err, out)
			}
		}
		if number != 0 {
			cleanup("gh", "pr", "close", strconv.Itoa(number), "--repo", "tt10612/painalizer")
		}
		for branch, commit := range expected {
			cleanup("git", "push", "--force-with-lease=refs/heads/"+branch+":"+commit, "origin", ":refs/heads/"+branch)
		}
	})
	git("switch", "-c", head, baseCommit)
	gitPlumbingWrite(t, repo, prefix+".txt", "Disposable startup measurement\n")
	git("add", prefix+".txt")
	git("-c", "user.name=Holark Test", "-c", "user.email=holark-test@users.noreply.github.com", "commit", "-m", "Disposable startup measurement")
	headCommit := git("rev-parse", "HEAD")
	git("push", "--force-with-lease=refs/heads/"+base+":", "--force-with-lease=refs/heads/"+head+":", "origin", baseCommit+":refs/heads/"+base, headCommit+":refs/heads/"+head)
	expected[base], expected[head] = baseCommit, headCommit
	url := gh("pr", "create", "--repo", "tt10612/painalizer", "--base", base, "--head", head, "--title", "[AUTH TEST] "+prefix, "--body", "Disposable startup timing validation.", "--draft")
	number, err = strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Live test PR %s", url)
	app, err = New(t.Context(), Options{RepositoryPath: repo, HomeDirectory: filepath.Join(root, "app"), ConfirmGitHubRepository: true, PullRequestGitHubTransport: liveWorkTransport(fullSync)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := prsqlite.New(t.Context(), app.Database)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, p := range store.ListPullRequests(app.RepositoryID) {
			if p.SyncExternalID != fmt.Sprintf("github:tt10612/painalizer#%d", number) {
				continue
			}
			liveWorkRequest(t, app, "POST", "/api/v1/pull-requests/"+p.ID+"/sync", map[string]any{}, 204, nil)
			p, _ = store.GetPullRequest(p.ID)
			if !p.HasCurrentComparison() || p.BaseCommit != baseCommit || p.HeadCommit != headCommit {
				t.Fatalf("fixture comparison=%+v", p)
			}
			return app, p, repo, func(commit string) { expected[head] = commit }
		}
		if time.Now().After(deadline) {
			t.Fatal("background import did not find measurement PR")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type syncOnlyLiveGitHub struct{ pr.GitHubTransport }

func liveWorkTransport(fullSync bool) pr.GitHubTransport {
	if fullSync {
		return syncOnlyLiveGitHub{githubprovider.New(githubapi.NewCLIClient())}
	}
	return nil
}

// Count foreground CLI invocations independently of the application's ongoing
// polling. Every override delegates to the real authenticated GitHub CLI.
type workTimingAPI struct {
	*githubapi.CLIClient
	requests atomic.Int32
	fullGets atomic.Int32
}

func (c *workTimingAPI) PullRequest(ctx context.Context, r githubapi.Repository, n int) (githubapi.PullRequest, error) {
	c.requests.Add(1)
	c.fullGets.Add(1)
	return c.CLIClient.PullRequest(ctx, r, n)
}
func (c *workTimingAPI) PullRequestReadiness(ctx context.Context, r githubapi.Repository, n int) (githubapi.PullRequestReadiness, error) {
	c.requests.Add(1)
	return c.CLIClient.PullRequestReadiness(ctx, r, n)
}
func (c *workTimingAPI) PullRequestWorkState(ctx context.Context, r githubapi.Repository, n int) (githubapi.PullRequest, error) {
	c.requests.Add(1)
	return c.CLIClient.PullRequestWorkState(ctx, r, n)
}

func auditWorkPreparation(t *testing.T, app *Application, p pr.PullRequest, repo string) {
	t.Helper()
	store, err := prsqlite.New(t.Context(), app.Database)
	if err != nil {
		t.Fatal(err)
	}
	git, err := gitadapter.Open(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	service := repository.NewService(git)
	client := &workTimingAPI{CLIClient: githubapi.NewCLIClient()}
	provider := githubprovider.New(client)
	c := pr.New(store, pr.Options{Refresh: app.prRefresh, Repository: localPullRequestRepository{Service: service, scheduler: app.prRefresh}, GitHubTransport: provider, GitHubCodec: provider, Projects: pr.ProjectLookupFunc(func(id string) (pr.Project, bool) {
		return pr.Project{ID: app.RepositoryID, RepositoryURL: "https://github.com/tt10612/painalizer.git", DefaultBranch: service.Descriptor().DefaultBranch, GitHubBacked: true}, id == app.RepositoryID
	})})
	values := map[string][]time.Duration{"sync": {}, "focused": {}}
	requests := map[string]int32{}
	fallbacks := int32(0)
	// Alternate ten pairs on this unchanged PR while normal app polling runs.
	for i := 0; i < 10; i++ {
		for _, mode := range []string{"sync", "focused"} {
			ctx := pr.WithRequestID(t.Context(), fmt.Sprintf("measurement-%s-%02d", mode, i))
			beforeRequests, beforeGets := client.requests.Load(), client.fullGets.Load()
			started := time.Now()
			if mode == "sync" {
				err = c.SyncPullRequest(ctx, p.ID)
			} else {
				err = c.PrepareWork(ctx, p.ID)
			}
			duration := time.Since(started)
			count := client.requests.Load() - beforeRequests
			t.Logf("PREPARATION_SAMPLE mode=%s run=%d duration=%s gh_invocations=%d error=%v", mode, i, duration, count, err)
			if err != nil {
				t.Fatal(err)
			}
			values[mode] = append(values[mode], duration)
			requests[mode] += count
			if mode == "focused" {
				fallbacks += client.fullGets.Load() - beforeGets
				if count != 1 {
					t.Errorf("focused preparation used %d gh invocations", count)
				}
			}
			current, ok := store.GetPullRequest(p.ID)
			if !ok || current.BaseCommit != p.BaseCommit || current.HeadCommit != p.HeadCommit {
				t.Fatal("measurement PR changed")
			}
		}
	}
	for _, mode := range []string{"sync", "focused"} {
		samples := values[mode]
		slices.Sort(samples)
		t.Logf("PREPARATION_SUMMARY mode=%s n=%d median=%s min=%s max=%s gh_invocations=%d focused_fallbacks=%d", mode, len(samples), (samples[4]+samples[5])/2, samples[0], samples[9], requests[mode], fallbacks)
	}
	if (values["focused"][4]+values["focused"][5])/2 >= (values["sync"][4]+values["sync"][5])/2 {
		t.Error("focused preparation did not reduce measured median preparation latency")
	}
}
