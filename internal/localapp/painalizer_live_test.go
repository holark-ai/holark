//go:build painalizer_live

package localapp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

// Explicitly opt-in live validation. All mutations use a disposable base branch.
func TestPainalizerLiveManualPush(t *testing.T) { runPainalizerLive(t, "manual_push") }

func TestPainalizerLiveSynchronization(t *testing.T) { runPainalizerLive(t, "") }
func TestPainalizerLiveMerge(t *testing.T)           { runPainalizerLive(t, "merge_only") }
func TestPainalizerLiveRebaseCreatedDiverged(t *testing.T) {
	runPainalizerLive(t, "rebase_created_diverged")
}
func TestPainalizerLiveRebaseMerge(t *testing.T) { runPainalizerLive(t, "rebase_merge") }
func TestPainalizerLiveWorkers(t *testing.T) {
	for _, mode := range []string{"continue", "address"} {
		t.Run(mode, func(t *testing.T) { runPainalizerLive(t, mode) })
	}
}
func runPainalizerLive(t *testing.T, workerMode string) {
	if os.Getenv("HOLARK_LIVE_TEST_REPOSITORY") != "tt10612/painalizer" {
		t.Skip("requires explicit painalizer live opt-in")
	}
	root, err := os.MkdirTemp("/tmp", "painalizer-live-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Evidence and isolated application state: %s", root)
	command := func(dir, name string, args ...string) string {
		t.Helper()
		c := exec.CommandContext(t.Context(), name, args...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	repo := filepath.Join(root, "repo")
	command(root, "git", "clone", "git@github.com:tt10612/painalizer.git", repo)
	git := func(dir string, args ...string) string { t.Helper(); return command(dir, "git", args...) }
	gh := func(args ...string) string { t.Helper(); return command(repo, "gh", args...) }
	git(repo, "config", "user.name", "Holark authenticated test")
	git(repo, "config", "user.email", "holark-test@users.noreply.github.com")
	main := git(repo, "rev-parse", "origin/main")
	prefix := "holark-auth-" + time.Now().UTC().Format("20060102-150405")
	base := prefix + "-base"
	expected := map[string]string{base: main}
	git(repo, "push", "--force-with-lease=refs/heads/"+base+":", "origin", main+":refs/heads/"+base)
	var app *Application
	var remotePR int
	t.Cleanup(func() {
		if app != nil {
			if err := app.Close(); err != nil {
				t.Errorf("application close: %v", err)
			}
		}
		if remotePR != 0 {
			c := exec.Command("gh", "api", fmt.Sprintf("repos/tt10612/painalizer/pulls/%d", remotePR))
			c.Dir = repo
			out, e := c.Output()
			var p struct {
				State  string `json:"state"`
				Merged bool   `json:"merged"`
				Head   struct {
					Ref string `json:"ref"`
				} `json:"head"`
				Base struct {
					Ref string `json:"ref"`
				} `json:"base"`
			}
			if e == nil && json.Unmarshal(out, &p) == nil && p.Base.Ref == base && p.State == "open" {
				c = exec.Command("gh", "pr", "close", fmt.Sprint(remotePR), "--repo", "tt10612/painalizer")
				if out, e = c.CombinedOutput(); e != nil {
					t.Errorf("close test PR: %v %s", e, out)
				}
			}
		}
		for branch, head := range expected {
			c := exec.Command("git", "push", "--force-with-lease=refs/heads/"+branch+":"+head, "origin", ":refs/heads/"+branch)
			c.Dir = repo
			if out, e := c.CombinedOutput(); e != nil {
				t.Errorf("cleanup retained branch %s: %v %s", branch, e, out)
			}
		}
		c := exec.Command("git", "ls-remote", "origin", "refs/heads/main")
		c.Dir = repo
		out, e := c.Output()
		if e != nil || !strings.HasPrefix(string(out), main+"\t") {
			t.Errorf("main changed during run: %s %v", out, e)
		}
	})
	app, err = New(t.Context(), Options{RepositoryPath: repo, HomeDirectory: filepath.Join(root, "app"), ConfirmGitHubRepository: true})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, body any, id string, want int, result any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		if id != "" {
			r.Header.Set("X-Request-ID", id)
		}
		w := httptest.NewRecorder()
		app.Handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d %s", method, path, w.Code, want, w.Body.String())
		}
		if want == 204 && w.Body.Len() != 0 {
			t.Fatal("sync returned a snapshot")
		}
		if result != nil {
			if err := json.Unmarshal(w.Body.Bytes(), result); err != nil {
				t.Fatalf("decode %s: %v %s", path, err, w.Body.String())
			}
		}
	}
	get := func(id string) pr.PullRequest {
		t.Helper()
		var p pr.PullRequest
		request("GET", "/api/v1/pull-requests/"+id, nil, "", 200, &p)
		return p
	}
	syncPR := func(id string) pr.PullRequest {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for {
			w := httptest.NewRecorder()
			app.Handler.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/pull-requests/"+id+"/sync", strings.NewReader("{}")))
			if w.Code == 204 {
				if w.Body.Len() != 0 {
					t.Fatal("sync returned a snapshot")
				}
				return get(id)
			}
			var problem struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &problem)
			if w.Code != 409 || problem.Code != "pull_request_sync_stale" || time.Now().After(deadline) {
				t.Fatalf("sync failed to converge: %d %s", w.Code, w.Body.String())
			}
			t.Log("Observed expected freshness rejection while GitHub observations converge; retrying synchronization")
			time.Sleep(time.Second)
		}
	}
	commit := func(dir, file, text string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, file), []byte(text+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git(dir, "add", file)
		git(dir, "-c", "user.name=Holark authenticated test", "-c", "user.email=holark-test@users.noreply.github.com", "commit", "-m", text)
		return git(dir, "rev-parse", "HEAD")
	}
	remoteHead := func(branch string) string {
		t.Helper()
		return strings.Fields(git(repo, "ls-remote", "origin", "refs/heads/"+branch))[0]
	}
	h, err := app.Holons.Create(t.Context(), holons.Create{Title: prefix + " workspace", BaseBranch: base, BaseCommit: main})
	if err != nil {
		t.Fatal(err)
	}
	first := commit(h.WorktreePath, prefix+"-feature.txt", "Authenticated shared cache test")
	expected[h.WorktreeBranch] = first
	var p pr.PullRequest
	request("POST", "/api/v1/holons/"+h.ID+"/pull-request", map[string]any{"title": "[AUTH TEST] " + prefix, "summary": "Disposable authenticated synchronization validation; merges target only the test base branch.", "target": "wip"}, prefix+"-create", 201, &p)
	if p.Status != pr.StatusWIP || p.HeadCommit != first || p.SyncExternalID != "" {
		t.Fatalf("WIP creation: %+v", p)
	}
	t.Log("PASS: authenticated WIP creation publishes branch without opening a GitHub PR")
	second := commit(h.WorktreePath, prefix+"-unpublished.txt", "Unpublished workspace change")
	cached := syncPR(p.ID)
	var ready holons.PublicationReadiness
	request("GET", "/api/v1/holons/"+h.ID+"/publication-readiness", nil, "", 200, &ready)
	if cached.HeadCommit != first || ready.TargetCommit != first || ready.WorkspaceHead != second || !ready.PublicationAvailable || remoteHead(p.HeadBranch) != first {
		t.Fatalf("refresh published or mixed inputs: %+v %+v", cached, ready)
	}
	t.Log("PASS: single-PR 204 then cached GET and Holon readiness retain the published head; refresh never pushes workspace commits")
	expected[p.HeadBranch] = second
	request("POST", "/api/v1/holons/"+h.ID+"/publish", map[string]any{}, prefix+"-publish", 200, nil)
	if cached = syncPR(p.ID); cached.HeadCommit != second || cached.Status != pr.StatusWIP || cached.SyncExternalID != "" || remoteHead(p.HeadBranch) != second {
		t.Fatalf("WIP Push: %+v", cached)
	}
	request("POST", "/api/v1/holons/"+h.ID+"/publish", map[string]any{}, prefix+"-publish", 200, nil)
	t.Log("PASS: WIP Push and request replay preserve identity and publish the exact workspace head")
	transition := func(status pr.Status) {
		t.Helper()
		request("POST", "/api/v1/pull-requests/"+p.ID+"/transition", map[string]any{"status": status}, prefix+"-transition-"+string(status)+fmt.Sprint(time.Now().UnixNano()), 200, nil)
		deadline := time.Now().Add(45 * time.Second)
		for {
			cached = get(p.ID)
			if cached.Status == status && len(cached.Operations) == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("transition did not settle: %+v", cached)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}

	preparedBase := main
	if workerMode == "rebase_created_diverged" {
		git(repo, "switch", "-c", prefix+"-prepare-base", main)
		preparedBase = commit(repo, prefix+"-base.txt", "Advance disposable comparison base before PR creation")
		expected[base] = preparedBase
		git(repo, "push", "--force-with-lease=refs/heads/"+base+":"+main, "origin", preparedBase+":refs/heads/"+base)
	}
	transition(pr.StatusDraft)
	var source struct {
		GitHub struct {
			Number int `json:"number"`
		} `json:"github"`
	}
	json.Unmarshal(cached.SyncData, &source)
	remotePR = source.GitHub.Number
	if remotePR == 0 {
		t.Fatalf("draft lacks GitHub identity: %+v", cached)
	}
	t.Logf("Live test PR https://github.com/tt10612/painalizer/pull/%d", remotePR)
	if actual := gh("api", fmt.Sprintf("repos/tt10612/painalizer/pulls/%d", remotePR), "--jq", ".base.ref"); actual != base {
		t.Fatalf("test PR base mismatch: %s want %s", actual, base)
	}
	external := cached.SyncExternalID
	transition(pr.StatusWIP)
	cached = syncPR(p.ID)
	if cached.Status != pr.StatusWIP || cached.SyncExternalID != external {
		t.Fatalf("withdrawn WIP replaced: %+v", cached)
	}
	if state := gh("api", fmt.Sprintf("repos/tt10612/painalizer/pulls/%d", remotePR), "--jq", ".state"); state != "closed" {
		t.Fatalf("withdrawal remote=%s", state)
	}
	t.Log("PASS: draft withdrawal remains a WIP after authenticated refresh, with its original GitHub identity")
	transition(pr.StatusOpen)
	cached = syncPR(p.ID)
	if cached.Status != pr.StatusOpen || cached.HeadCommit != second {
		t.Fatalf("open: %+v", cached)
	}

	if workerMode == "merge_only" {
		deadline := time.Now().Add(45 * time.Second)
		for {
			cached = syncPR(p.ID)
			if cached.Mergeable != nil && *cached.Mergeable {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("merge never ready: %s", cached.MergeBlockedReason)
			}
			time.Sleep(time.Second)
		}
		request("POST", "/api/v1/pull-requests/"+p.ID+"/merge", map[string]any{"strategy": "squash"}, prefix+"-merge", 200, nil)
		cached = get(p.ID)
		if cached.Status != pr.StatusMerged || cached.MergedCommit == "" {
			t.Fatalf("merge did not record confirmed result: %s %s", cached.Status, cached.MergedCommit)
		}
		expected[base] = cached.MergedCommit
		request("POST", "/api/v1/pull-requests/"+p.ID+"/merge", map[string]any{"strategy": "squash"}, prefix+"-merge", 200, nil)
		if state := gh("api", fmt.Sprintf("repos/tt10612/painalizer/pulls/%d", remotePR), "--jq", ".merged"); state != "true" {
			t.Fatalf("merge unconfirmed: %s", state)
		}
		if remoteHead(base) != cached.MergedCommit {
			t.Fatal("merge changed the wrong base")
		}
		t.Log("PASS: authenticated merge and replay use the accepted pair and merge only into the disposable base")
		return
	}
	if workerMode == "continue" || workerMode == "address" || workerMode == "manual_push" {
		request("PUT", "/api/v1/settings/default-agent-harness", map[string]any{"harness_type": "codex"}, "", 200, nil)
		filename := prefix + "-" + workerMode + ".txt"
		prompt := "This is an authorized live Holark test in a disposable worktree. Create only the file " + filename + " containing AUTHENTICATED_WORKER_OK and a newline. Stage only that file and commit it with git -c user.name='Holark Test' -c user.email='holark-test@users.noreply.github.com' commit -m 'Authenticated worker test'. Do not push. Do not modify other source files."
		body := map[string]any{"mode": "continue", "prompt": prompt + " Finish with WORKER_DONE."}
		if workerMode == "address" {
			var comment pullrequestcomments.Comment
			request("POST", "/api/v1/pull-requests/"+p.ID+"/comments", map[string]any{"scope": "pull_request", "body": "[AUTH TEST] Add the disposable verification file " + filename + " containing AUTHENTICATED_WORKER_OK."}, "", 201, &comment)
			body = map[string]any{"mode": "auto", "comment_ids": []string{comment.ID}, "prompt": prompt + ` After the commit, write .holark/comment-reply.json containing {"reply":"Added the authenticated verification file."}. This is uncommitted control data; do not stage it. Finish after writing it.`}
		}
		var started []pullrequestwork.Work
		startupStarted := time.Now()
		request("POST", "/api/v1/pull-requests/"+p.ID+"/workers", body, prefix+"-start-"+workerMode, 201, &started)
		t.Logf("WORKER_STARTUP mode=%s duration=%s", workerMode, time.Since(startupStarted))
		if len(started) != 1 || started[0].HeadCommit != second || started[0].BaseCommit != main || started[0].SessionID == "" {
			t.Fatalf("worker did not capture cache: %+v", started)
		}
		w := started[0]
		t.Logf("Live %s worker %s started from exact cached head %s", workerMode, w.ID, second)
		deadline := time.Now().Add(180 * time.Second)
		for {
			jobs, err := app.PullRequestWork.List(t.Context(), p.ID, pullrequestwork.KindWorker)
			if err != nil {
				t.Fatal(err)
			}
			for _, job := range jobs {
				if job.ID == w.ID {
					w = job
				}
			}
			wh, err := app.Holons.Get(t.Context(), w.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := app.Holons.InspectWorkspace(t.Context(), w.SessionID)
			if (workerMode == "continue" || workerMode == "manual_push") && err == nil && !inspection.Dirty && inspection.HeadCommit != second && len(wh.AgentSessions) > 0 && wh.AgentSessions[0].InputState == "task_complete" {
				expected[p.HeadBranch] = inspection.HeadCommit
				var response struct {
					Worker pullrequestwork.Work `json:"worker"`
				}
				request("POST", "/api/v1/pull-requests/"+p.ID+"/publish", map[string]any{}, prefix+"-continue-push", 200, &response)
				published := response.Worker
				observed := syncPR(p.ID)
				if published.ID != w.ID || published.Status != pullrequestwork.StatusRunning || published.HeadCommit != inspection.HeadCommit || observed.HeadCommit != inspection.HeadCommit || remoteHead(p.HeadBranch) != inspection.HeadCommit {
					t.Fatalf("Continue publication: %+v %+v", published, observed)
				}
				t.Log("PASS: real authenticated Continue used cached inputs, published its commit, and remained running")
				if workerMode == "manual_push" {
					auditPainalizerManualPush(t, app, p, wh, prefix, root, repo, expected)
				}
				return
			}
			if workerMode == "address" && w.Status == pullrequestwork.StatusCompleted {
				expected[p.HeadBranch] = w.ResultHeadCommit
				observed := syncPR(p.ID)
				if w.ResultHeadCommit == "" || w.ResultHeadCommit == second || observed.HeadCommit != w.ResultHeadCommit || remoteHead(p.HeadBranch) != w.ResultHeadCommit {
					t.Fatalf("Address publication: %+v %+v", w, observed)
				}
				t.Log("PASS: real authenticated Address worker used cached inputs, committed and published its result, and completed")
				return
			}
			if w.Status == pullrequestwork.StatusFailed || time.Now().After(deadline) {
				t.Fatalf("live %s worker did not complete: work=%+v agents=%+v inspection=%+v err=%v", workerMode, w, wh.AgentSessions, inspection, err)
			}
			time.Sleep(time.Second)
		}
	}
	third := second
	if workerMode != "rebase_merge" && workerMode != "rebase_created_diverged" {
		// An external branch push is observed without restarting the application.
		git(repo, "fetch", "origin", p.HeadBranch)
		git(repo, "switch", "-c", prefix+"-external", "FETCH_HEAD")
		third = commit(repo, prefix+"-external.txt", "Externally published change")
		expected[p.HeadBranch] = third
		git(repo, "push", "--force-with-lease=refs/heads/"+p.HeadBranch+":"+second, "origin", third+":refs/heads/"+p.HeadBranch)
		cached = syncPR(p.ID)
		if cached.HeadCommit != third || cached.BaseCommit != main {
			t.Fatalf("external push: %+v", cached)
		}
		request("GET", "/api/v1/pull-requests/"+p.ID+"/commits", nil, "", 200, nil)
		request("GET", "/api/v1/pull-requests/"+p.ID+"/changes", nil, "", 200, nil)
		t.Log("PASS: external SSH push appears in cached topology and details without restart")

	}

	baseHead := preparedBase
	if workerMode != "rebase_created_diverged" {
		git(repo, "switch", "-c", prefix+"-advance-base", main)
		baseHead = commit(repo, prefix+"-base.txt", "Advance disposable comparison base")
		expected[base] = baseHead
		git(repo, "push", "--force-with-lease=refs/heads/"+base+":"+main, "origin", baseHead+":refs/heads/"+base)
	}

	providerDeadline := time.Now().Add(45 * time.Second)
	for {
		observed := remoteHead(base)
		if observed == baseHead {
			break
		}
		if time.Now().After(providerDeadline) {
			t.Fatalf("GitHub base observation did not reach pushed base: got %s want %s", observed, baseHead)
		}
		time.Sleep(time.Second)
	}
	var rebase pullrequestwork.RebaseReadiness
	request("POST", "/api/v1/pull-requests/"+p.ID+"/rebase-readiness", map[string]any{}, "", 200, &rebase)
	if rebase.HeadCommit != third || rebase.BaseCommit != baseHead {
		t.Fatalf("PR rebase selected inconsistent pair: %+v", rebase)
	}
	var rejected pullrequestwork.Work
	request("POST", "/api/v1/pull-requests/"+p.ID+"/rebase", map[string]any{"mechanical_only": true, "expected_base_commit": main}, prefix+"-stale-rebase", 201, &rejected)
	if rejected.Status != pullrequestwork.StatusFailed || rejected.Error != pullrequestwork.ErrRebaseTargetChanged.Error() || remoteHead(p.HeadBranch) != third {
		t.Fatalf("stale base was not rejected without publication: %+v", rejected)
	}
	var work pullrequestwork.Work
	request("POST", "/api/v1/pull-requests/"+p.ID+"/rebase", map[string]any{"mechanical_only": true, "expected_base_commit": baseHead}, prefix+"-rebase", 201, &work)
	if work.Status != pullrequestwork.StatusCompleted || work.ResultHeadCommit == "" || work.ResultHeadCommit == third {
		t.Fatalf("mechanical rebase: %+v", work)
	}
	expected[p.HeadBranch] = work.ResultHeadCommit
	completedAt := time.Now()
	// Cached detail reads must remain usable throughout asynchronous preparation.
	deadlineReady := completedAt.Add(45 * time.Second)
	for {
		cached = get(p.ID)
		request("GET", "/api/v1/pull-requests/"+p.ID+"/commits", nil, "", 200, nil)
		request("GET", "/api/v1/pull-requests/"+p.ID+"/changes", nil, "", 200, nil)
		if cached.HasCurrentComparison() && cached.HeadCommit == work.ResultHeadCommit {
			break
		}
		if time.Now().After(deadlineReady) {
			t.Fatalf("completion refresh never became readable: %+v", cached)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("Rebase completion-to-readable latency: %s", time.Since(completedAt))
	if cached.HeadCommit != work.ResultHeadCommit || cached.BaseCommit != baseHead || remoteHead(p.HeadBranch) != work.ResultHeadCommit {
		t.Fatalf("rebase output: %+v", cached)
	}
	var replay pullrequestwork.Work
	request("POST", "/api/v1/pull-requests/"+p.ID+"/rebase", map[string]any{"mechanical_only": true, "expected_base_commit": baseHead}, prefix+"-rebase", 201, &replay)
	if replay.ID != work.ID || replay.ResultHeadCommit != work.ResultHeadCommit {
		t.Fatalf("rebase replay: %+v", replay)
	}
	t.Log("PASS: PR rebase uses accepted cached base/head, rejects stale base, preserves pinned execution and request replay")
	if os.Getenv("HOLARK_LIVE_BROWSER_AUDIT") == "1" {
		mux := http.NewServeMux()
		mux.Handle("/api/", app.Handler)
		assets := filepath.Join("..", "localshell", "frontend", "dist")
		files := http.FileServer(http.Dir(assets))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				files.ServeHTTP(w, r)
				return
			}
			http.ServeFile(w, r, filepath.Join(assets, "index.html"))
		})
		server := httptest.NewServer(mux)
		defer server.Close()
		auditPainalizerBrowser(t, server.URL, p.ID, work.ResultHeadCommit, filepath.Join(root, "rebased"))
		git(repo, "fetch", "origin", p.HeadBranch)
		git(repo, "switch", "-C", prefix+"-after-rebase", "FETCH_HEAD")
		external := commit(repo, prefix+"-after-rebase.txt", "External push after successful rebase")
		expected[p.HeadBranch] = external
		git(repo, "push", "--force-with-lease=refs/heads/"+p.HeadBranch+":"+work.ResultHeadCommit, "origin", external+":refs/heads/"+p.HeadBranch)
		cached = syncPR(p.ID)
		if cached.HeadCommit != external {
			t.Fatalf("external publication not accepted: %+v", cached)
		}
		auditPainalizerBrowser(t, server.URL, p.ID, external, filepath.Join(root, "external"))
		if remoteHead(p.HeadBranch) != external {
			t.Fatal("refresh overwrote external publication")
		}
		t.Log("PASS: browser commits and diff read the rebased result and subsequent external publication")
	}

	deadline := time.Now().Add(45 * time.Second)
	for {
		cached = syncPR(p.ID)
		if cached.Mergeable != nil && *cached.Mergeable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("merge never ready: %+v", cached)
		}
		time.Sleep(time.Second)
	}
	request("POST", "/api/v1/pull-requests/"+p.ID+"/merge", map[string]any{"strategy": "squash"}, prefix+"-merge", 200, nil)
	cached = get(p.ID)
	if cached.Status != pr.StatusMerged || cached.MergedCommit == "" {
		t.Fatalf("merge: %+v", cached)
	}
	expected[base] = cached.MergedCommit
	request("POST", "/api/v1/pull-requests/"+p.ID+"/merge", map[string]any{"strategy": "squash"}, prefix+"-merge", 200, nil)
	if state := gh("api", fmt.Sprintf("repos/tt10612/painalizer/pulls/%d", remotePR), "--jq", ".merged"); state != "true" {
		t.Fatalf("merge unconfirmed: %s", state)
	}
	if remoteHead(base) != cached.MergedCommit {
		t.Fatal("merge changed the wrong base")
	}
	t.Log("PASS: authenticated merge and replay use the accepted pair and merge only into the disposable base")
}

// Uses the existing Playwright installation against the real isolated application.
func auditPainalizerBrowser(t *testing.T, url, id, head, artifact string) {
	t.Helper()
	const script = `
import { chromium, expect } from '@playwright/test';
import { writeFile } from 'node:fs/promises';
const [url, id, head, artifact] = process.argv.slice(1);
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const evidence = [];
  const reads = [];
  page.on('response', response => {
    if (!new RegExp('/pull-requests/' + id + '/(commits|changes)$').test(response.url())) return;
    reads.push((async () => {
      const body = await response.json();
      evidence.push({ url: response.url(), status: response.status(), body });
      expect(response.status()).toBe(200);
      expect(body.inputs.head_commit).toBe(head);
    })());
  });
  await page.goto(url + '/pulls/' + id);
  await page.getByRole('button', { name: /^Commits/ }).click();
  await expect(page.getByText('Loading commits...', { exact: true })).toHaveCount(0);
  await expect(page.getByText('Updating commits…', { exact: true })).toHaveCount(0);
  await page.screenshot({ path: artifact + '-commits.png', fullPage: true });
  await page.getByRole('button', { name: /^Changes/ }).click();
  await expect(page.getByText('Loading changes...', { exact: true })).toHaveCount(0);
  await expect(page.getByText('Updating changes…', { exact: true })).toHaveCount(0);
  await page.screenshot({ path: artifact + '-changes.png', fullPage: true });
  await Promise.all(reads);
  expect(evidence.some(read => read.url.endsWith('/commits'))).toBe(true);
  expect(evidence.some(read => read.url.endsWith('/changes'))).toBe(true);
  await writeFile(artifact + '-responses.json', JSON.stringify(evidence, null, 2));
} finally { await browser.close(); }
`
	command := exec.CommandContext(t.Context(), "node", "--input-type=module", "-e", script, url, id, head, artifact)
	command.Dir = filepath.Join("..", "..", "web")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("browser audit: %v\n%s", err, out)
	}
	t.Logf("Browser screenshots and response evidence: %s", artifact)
}
