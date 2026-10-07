//go:build painalizer_live

package localapp

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/holons"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

func auditPainalizerManualPush(t *testing.T, app *Application, p pr.PullRequest, h holons.Holon, prefix, root, repo string, expected map[string]string) {
	t.Helper()
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(logger)
	request := func(method, path string, body any, status int, result any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Request-ID", fmt.Sprintf("%s-manual-%d", prefix, time.Now().UnixNano()))
		w := httptest.NewRecorder()
		app.Handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d want %d %s", method, path, w.Code, status, w.Body.String())
		}
		if result != nil {
			if err := json.Unmarshal(w.Body.Bytes(), result); err != nil {
				t.Fatal(err)
			}
		}
	}
	git := func(dir string, args ...string) string { t.Helper(); return gitPlumbingOutput(t, dir, args...) }
	commit := func(dir, name string) string {
		t.Helper()
		gitPlumbingWrite(t, dir, name, "Manual publication audit\n")
		git(dir, "add", name)
		git(dir, "-c", "user.name=Holark Test", "-c", "user.email=holark-test@users.noreply.github.com", "commit", "-m", name)
		return git(dir, "rev-parse", "HEAD")
	}
	git(repo, "fetch", "origin", p.HeadBranch)
	git(repo, "switch", "-c", prefix+"-manual-external", "FETCH_HEAD")
	external := commit(repo, prefix+"-external.txt")
	git(repo, "push", "--force-with-lease=refs/heads/"+p.HeadBranch+":"+expected[p.HeadBranch], "origin", external+":refs/heads/"+p.HeadBranch)
	expected[p.HeadBranch] = external
	commit(h.WorktreePath, prefix+"-local.txt")
	var problem struct {
		Code string `json:"code"`
	}
	request("POST", "/api/v1/holons/"+h.ID+"/publish", map[string]any{}, 409, &problem)
	if problem.Code != "stale_head" {
		t.Fatalf("stale Push code=%s", problem.Code)
	}
	t.Log("PASS: external push makes the existing Continue Holon's Push stale")
	request("POST", "/api/v1/holons/"+h.ID+"/rebase-agent", map[string]any{"source_agent_id": h.AgentSessions[0].ID}, 201, nil)
	deadline := time.Now().Add(4 * time.Minute)
	for {
		current, err := app.Holons.Get(t.Context(), h.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.RebaseAttempt != nil && current.RebaseAttempt.State == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Rebase did not finish: %+v", current.RebaseAttempt)
		}
		time.Sleep(time.Second)
	}
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
	for i := 0; i < 2; i++ {
		if i == 1 {
			var workers []pullrequestwork.Work
			request("POST", "/api/v1/pull-requests/"+p.ID+"/workers", map[string]any{"mode": "continue", "prompt": "This is an authorized disposable live test. Create only " + prefix + "-second.txt containing OK and a newline, stage it, and commit with git -c user.name='Holark Test' -c user.email='holark-test@users.noreply.github.com' commit -m 'Second manual push audit'. Do not push or change other files. Finish with WORKER_DONE."}, 201, &workers)
			if len(workers) != 1 {
				t.Fatalf("workers=%+v", workers)
			}
			deadline := time.Now().Add(4 * time.Minute)
			for {
				var err error
				h, err = app.Holons.Get(t.Context(), workers[0].SessionID)
				if err != nil {
					t.Fatal(err)
				}
				inspection, err := app.Holons.InspectWorkspace(t.Context(), h.ID)
				if err == nil && !inspection.Dirty && inspection.HeadCommit != workers[0].HeadCommit && len(h.AgentSessions) > 0 && h.AgentSessions[0].InputState == "task_complete" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("second Continue did not finish")
				}
				time.Sleep(time.Second)
			}
		}
		result := git(h.WorktreePath, "rev-parse", "HEAD")
		expected[p.HeadBranch] = result
		auditPainalizerBrowserPush(t, server.URL, h.ID, filepath.Join(root, fmt.Sprintf("manual-push-%d", i)))
		if actual := strings.Fields(git(repo, "ls-remote", "origin", "refs/heads/"+p.HeadBranch))[0]; actual != result {
			t.Fatalf("remote=%s want=%s", actual, result)
		}
		t.Logf("PASS: browser Push from PR-created Holon %s published %s", h.ID, result)
	}
}

func auditPainalizerBrowserPush(t *testing.T, url, id, artifact string) {
	t.Helper()
	const script = `
import { chromium, expect } from '@playwright/test';
import { writeFile } from 'node:fs/promises';
const [url,id,artifact] = process.argv.slice(1);
const browser = await chromium.launch({headless:true});
try {
  const page = await browser.newPage({viewport:{width:1440,height:1000}});
  const requests=[];
  page.on('request', request => requests.push({at:Date.now(),method:request.method(),path:new URL(request.url()).pathname}));
  await page.goto(url+'/holons/'+id);
  const push = page.getByRole('button',{name:'Push',exact:true});
  await expect(push).toBeEnabled({timeout:30000});
  await page.waitForTimeout(6500);
  let started, finished, startedAt, finishedAt;
  page.on('request', request => { if(request.url().endsWith('/holons/'+id+'/publish')) { started=performance.now(); startedAt=Date.now(); } });
  page.on('requestfinished', request => { if(request.url().endsWith('/holons/'+id+'/publish')) { finished=performance.now(); finishedAt=Date.now(); } });
  const response = page.waitForResponse(r => r.url().endsWith('/holons/'+id+'/publish'));
  await push.click();
  await expect(page.getByRole('button',{name:'Pushing...',exact:true})).toBeVisible();
  const visible=performance.now();
  const result=await response;
  expect(result.status()).toBe(200);
  await expect(page.getByRole('button',{name:'Pushing...',exact:true})).toHaveCount(0,{timeout:60000});
  const evidence={holon:id,status:result.status(),startedAt,finishedAt,requestMs:finished-started,pushingMs:performance.now()-visible,automaticFullSyncs:requests.filter(r=>r.method==='POST' && /\/pull-requests\/[^/]+\/sync$/.test(r.path)).length,requests};
  console.log(JSON.stringify(evidence));
  await writeFile(artifact+'.json',JSON.stringify(evidence,null,2));
  await page.screenshot({path:artifact+'.png',fullPage:true});
} finally { await browser.close(); }
`
	command := exec.CommandContext(t.Context(), "node", "--input-type=module", "-e", script, url, id, artifact)
	command.Dir = filepath.Join("..", "..", "web")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("browser Push audit: %v\n%s", err, out)
	}
	t.Logf("Browser Push timing: %s; evidence %s", out, artifact)
}
