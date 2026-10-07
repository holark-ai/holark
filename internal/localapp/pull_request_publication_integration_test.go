package localapp_test

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
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	work "github.com/holark-ai/holark/internal/pullrequestwork"
	worksqlite "github.com/holark-ai/holark/internal/pullrequestwork/sqliteadapter"
)

func publicationGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = directory
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func publicationCommit(t *testing.T, fixture *creationFixture, name string) string {
	t.Helper()
	path := fixture.scenario.State.WorktreePath
	if err := os.WriteFile(filepath.Join(path, name), []byte(name+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	publicationGit(t, path, "add", name)
	publicationGit(t, path, "-c", "user.name=Publication Test", "-c", "user.email=test@invalid", "commit", "-m", name)
	return publicationGit(t, path, "rev-parse", "HEAD")
}

func publicationRemote(t *testing.T, fixture *creationFixture, pr pullrequestlifecycle.PullRequest) string {
	t.Helper()
	remote := filepath.Join(fixture.root, "remote.git")
	path := fixture.scenario.State.WorktreePath
	publicationGit(t, path, "push", "origin", pr.HeadCommit+":refs/heads/"+pr.HeadBranch)

	return remote
}

func assertPublicationHead(t *testing.T, fixture *creationFixture, pr pullrequestlifecycle.PullRequest, remote, expected string) {
	t.Helper()
	fixture.request(t, "POST", "/api/v1/pull-requests/"+pr.ID+"/sync", `{}`, http.StatusNoContent, nil)
	h, err := fixture.scenario.Application.Holons.Get(t.Context(), pr.LinkedHolonIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	var relational string
	if err := fixture.scenario.Application.Database.QueryRow(`select head_commit from pull_requests where id=?`, pr.ID).Scan(&relational); err != nil {
		t.Fatal(err)
	}
	if got := fixture.pullRequest(t, pr.ID); got.HeadCommit != expected || relational != expected || !h.Published || h.UpstreamBranch != pr.HeadBranch || h.UpstreamHeadCommit != expected {
		t.Fatalf("inconsistent publication: PR=%+v relational=%s Holon=%+v", got, relational, h)
	}
	if actual := publicationGit(t, remote, "rev-parse", "refs/heads/"+pr.HeadBranch); actual != expected {
		t.Fatalf("remote=%s want=%s", actual, expected)
	}
}

func TestHolonPublishAdvancesNormalIssueAndContinueRepeatedly(t *testing.T) {
	for _, kind := range []holons.Kind{holons.KindNormal, holons.KindIssue, holons.KindPullWorker} {
		for _, target := range []pullrequestlifecycle.Status{pullrequestlifecycle.StatusWIP, pullrequestlifecycle.StatusDraft, pullrequestlifecycle.StatusOpen} {
			t.Run(string(kind)+"/"+string(target), func(t *testing.T) {
				fixture := newCreationFixture(t)
				pr := fixture.create(t, target)
				if target == pullrequestlifecycle.StatusOpen {
					fixture.complete(t, fixture.metadata(t, pr.ID))
					fixture.awaitTarget(t, pr.ID, target)
				}
				remote := publicationRemote(t, fixture, pr)
				id := fixture.scenario.State.HolonID
				db := fixture.scenario.Application.Database
				if _, err := db.Exec(`update holons set kind=? where id=?`, kind, id); err != nil {
					t.Fatal(err)
				}
				var workers *worksqlite.Store
				if kind == holons.KindPullWorker {
					var err error
					workers, err = worksqlite.New(t.Context(), db)
					if err != nil {
						t.Fatal(err)
					}
					if err = workers.Create(t.Context(), work.Work{ID: "continue", PullRequestID: pr.ID, SessionID: id, Kind: work.KindWorker, Mode: work.ModeContinue, Status: work.StatusRunning, HeadBranch: pr.HeadBranch, HeadCommit: pr.HeadCommit, BaseHeadCommit: pr.HeadCommit, CreatedAt: time.Now().UTC()}); err != nil {
						t.Fatal(err)
					}
				}
				var head string
				for i := 1; i <= 2; i++ {
					head = publicationCommit(t, fixture, fmt.Sprintf("change-%d.txt", i))
					// The backend must choose the actual linked PR and lease, even
					// when the client supplies stale or unrelated publication data.
					fixture.request(t, "POST", "/api/v1/holons/"+id+"/publish", `{"remote":"wrong","upstream_branch":"wrong","expected_remote_head":"stale"}`, http.StatusOK, nil)
					assertPublicationHead(t, fixture, pr, remote, head)
					if workers != nil {
						w, err := workers.Get(t.Context(), "continue")
						if err != nil || w.Status != work.StatusRunning || w.HeadCommit != head || w.BaseHeadCommit != head || w.ResultHeadCommit != head {
							t.Fatalf("continue=%+v error=%v", w, err)
						}
					}
				}
				// Restore the fixture's fake GitHub boundary before reopening;
				// its local bare push target is not a GitHub repository binding.
				// Keep the real published repository available across restart.
				fixture.reopen(t)
				cached := fixture.pullRequest(t, pr.ID)
				if cached.HeadCommit != head {
					t.Fatalf("restart lost accepted comparison: %+v", cached)
				}
			})
		}
	}
}

func TestHolonPublishRejectsUnpublishableWorkAndRemoteMovement(t *testing.T) {
	for _, continueWorker := range []bool{false, true} {
		for _, failure := range []string{"no changes", "dirty", "closed", "remote moved"} {
			t.Run(fmt.Sprintf("%t/%s", continueWorker, failure), func(t *testing.T) {
				fixture := newCreationFixture(t)
				pr := fixture.create(t, pullrequestlifecycle.StatusWIP)
				remote := publicationRemote(t, fixture, pr)
				if continueWorker {
					id := fixture.scenario.State.HolonID
					db := fixture.scenario.Application.Database
					if _, err := db.Exec("update holons set kind=? where id=?", holons.KindPullWorker, id); err != nil {
						t.Fatal(err)
					}
					workers, err := worksqlite.New(t.Context(), db)
					if err != nil {
						t.Fatal(err)
					}
					if err := workers.Create(t.Context(), work.Work{ID: "continue", PullRequestID: pr.ID, SessionID: id, Kind: work.KindWorker, Mode: work.ModeContinue, Status: work.StatusRunning, HeadBranch: pr.HeadBranch, HeadCommit: pr.HeadCommit, BaseHeadCommit: pr.HeadCommit, CreatedAt: time.Now()}); err != nil {
						t.Fatal(err)
					}
				}
				code := "no_changes"
				remoteHead := pr.HeadCommit
				if failure != "no changes" {
					publicationCommit(t, fixture, "change.txt")
				}
				switch failure {
				case "dirty":
					if err := os.WriteFile(filepath.Join(fixture.scenario.State.WorktreePath, "dirty.txt"), []byte("dirty"), 0600); err != nil {
						t.Fatal(err)
					}
					code = "workspace_dirty"
				case "closed":
					fixture.request(t, "POST", "/api/v1/pull-requests/"+pr.ID+"/transition", `{"status":"closed"}`, http.StatusOK, nil)
					code = "pull_request_inactive"
				case "remote moved":
					remoteHead = pr.BaseCommit
					publicationGit(t, remote, "update-ref", "refs/heads/"+pr.HeadBranch, remoteHead)
					code = "stale_head"
				}
				var problem struct {
					Code string `json:"code"`
				}
				fixture.request(t, "POST", "/api/v1/holons/"+fixture.scenario.State.HolonID+"/publish", `{}`, http.StatusConflict, &problem)
				if problem.Code != code {
					t.Fatalf("code=%s want=%s", problem.Code, code)
				}
				if got := fixture.pullRequest(t, pr.ID); got.HeadCommit != remoteHead {
					t.Fatalf("PR changed: %+v", got)
				}
				if actual := publicationGit(t, remote, "rev-parse", "refs/heads/"+pr.HeadBranch); actual != remoteHead {
					t.Fatalf("remote changed: %s", actual)
				}
			})
		}
	}
}

func TestHolonPublishRetriesAfterDatabaseFailureWithoutLosingCheckpoint(t *testing.T) {
	for _, continueWorker := range []bool{false, true} {
		t.Run(fmt.Sprint(continueWorker), func(t *testing.T) {
			fixture := newCreationFixture(t)
			pr := fixture.create(t, pullrequestlifecycle.StatusWIP)
			remote := publicationRemote(t, fixture, pr)
			head := publicationCommit(t, fixture, "change.txt")
			db := fixture.scenario.Application.Database
			firstStatus := http.StatusInternalServerError
			if continueWorker {
				firstStatus = http.StatusConflict
				if _, err := db.Exec(`update holons set kind=? where id=?`, holons.KindPullWorker, fixture.scenario.State.HolonID); err != nil {
					t.Fatal(err)
				}
				workers, err := worksqlite.New(t.Context(), db)
				if err != nil {
					t.Fatal(err)
				}
				if err = workers.Create(t.Context(), work.Work{ID: "recover-continue", PullRequestID: pr.ID, SessionID: fixture.scenario.State.HolonID, Kind: work.KindWorker, Mode: work.ModeContinue, Status: work.StatusRunning, HeadBranch: pr.HeadBranch, HeadCommit: pr.HeadCommit, BaseHeadCommit: pr.HeadCommit, CreatedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
			}
			// Fail the final Holon write, after both PR representations were updated
			// inside the transaction, to verify the entire checkpoint rolls back.
			if _, err := db.Exec(`create trigger fail_publication before update of published on holons begin select raise(abort, 'publication unavailable'); end`); err != nil {
				t.Fatal(err)
			}
			id := fixture.scenario.State.HolonID
			fixture.request(t, "POST", "/api/v1/holons/"+id+"/publish", `{}`, firstStatus, nil)
			h, err := fixture.scenario.Application.Holons.Get(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			var relational string
			if err := db.QueryRow(`select head_commit from pull_requests where id=?`, pr.ID).Scan(&relational); err != nil {
				t.Fatal(err)
			}
			if h.Published || h.UpstreamHeadCommit != "" || fixture.pullRequest(t, pr.ID).HeadCommit != pr.HeadCommit || relational != pr.HeadCommit {
				t.Fatalf("checkpoint did not roll back: Holon=%+v relational=%s", h, relational)
			}
			if actual := publicationGit(t, remote, "rev-parse", "refs/heads/"+pr.HeadBranch); actual != head {
				t.Fatalf("push did not happen: %s", actual)
			}
			if _, err := db.Exec(`drop trigger fail_publication`); err != nil {
				t.Fatal(err)
			}
			// The original explicit target remains valid when recovering the
			// exact workspace result already published by the interrupted attempt.
			retry := httptest.NewRequest("POST", "/api/v1/holons/"+id+"/publish", strings.NewReader("{}"))
			retry.Header.Set("If-Match", pr.HeadCommit)
			response := httptest.NewRecorder()
			fixture.scenario.Application.Handler.ServeHTTP(response, retry)
			if response.Code != http.StatusOK {
				t.Fatalf("recovery: %d %s", response.Code, response.Body.String())
			}
			assertPublicationHead(t, fixture, pr, remote, head)
			if continueWorker {
				workers, err := worksqlite.New(t.Context(), db)
				if err != nil {
					t.Fatal(err)
				}
				saved, err := workers.Get(t.Context(), "recover-continue")
				if err != nil || saved.HeadCommit != head || saved.ResultHeadCommit != head || saved.Status != work.StatusRunning {
					t.Fatalf("recovered Continue=%+v err=%v", saved, err)
				}
			}

		})
	}
}

func TestPublicationReplayReturnsOriginalHolonOrWorkerWithoutPublishingNewCommits(t *testing.T) {
	for _, continueWorker := range []bool{false, true} {
		t.Run(fmt.Sprint(continueWorker), func(t *testing.T) {
			fixture := newCreationFixture(t)
			pr := fixture.create(t, pullrequestlifecycle.StatusWIP)
			remote := publicationRemote(t, fixture, pr)
			id := fixture.scenario.State.HolonID
			path := "/api/v1/holons/" + id + "/publish"
			if continueWorker {
				db := fixture.scenario.Application.Database
				if _, err := db.Exec(`update holons set kind=? where id=?`, holons.KindPullWorker, id); err != nil {
					t.Fatal(err)
				}
				store, err := worksqlite.New(t.Context(), db)
				if err != nil {
					t.Fatal(err)
				}
				if err = store.Create(t.Context(), work.Work{ID: "continue", PullRequestID: pr.ID, SessionID: id, Kind: work.KindWorker, Mode: work.ModeContinue, Status: work.StatusRunning, HeadBranch: pr.HeadBranch, HeadCommit: pr.HeadCommit, BaseHeadCommit: pr.HeadCommit, CreatedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
				path = "/api/v1/pull-requests/" + pr.ID + "/publish"
			}
			head := publicationCommit(t, fixture, "first.txt")
			for attempt := 0; attempt < 2; attempt++ {
				if attempt == 1 {
					publicationCommit(t, fixture, "unpublished.txt")
					if continueWorker {
						store, err := worksqlite.New(t.Context(), fixture.scenario.Application.Database)
						if err != nil {
							t.Fatal(err)
						}
						if err := store.Create(t.Context(), work.Work{ID: "newer-worker", PullRequestID: pr.ID, SessionID: "newer-holon", Kind: work.KindWorker, Mode: work.ModeContinue, Status: work.StatusRunning, HeadBranch: pr.HeadBranch, HeadCommit: head, BaseHeadCommit: head, CreatedAt: time.Now().UTC()}); err != nil {
							t.Fatal(err)
						}
					}
				}
				request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, fixture.server.URL+path, strings.NewReader(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("X-Request-ID", "publication-replay")
				response, err := fixture.client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				var result struct {
					ID                 string    `json:"id"`
					UpstreamHeadCommit string    `json:"upstream_head_commit"`
					HeadCommit         string    `json:"head_commit"`
					Worker             work.Work `json:"worker"`
				}
				err = json.NewDecoder(response.Body).Decode(&result)
				response.Body.Close()
				if err != nil || response.StatusCode != http.StatusOK {
					t.Fatalf("publication replay=%+v status=%d error=%v", result, response.StatusCode, err)
				}
				if continueWorker {
					if result.Worker.ID != "continue" || result.Worker.SessionID != id || result.HeadCommit != head {
						t.Fatalf("worker replay=%+v", result)
					}
				} else if result.ID != id || result.UpstreamHeadCommit != head {
					t.Fatalf("Holon replay=%+v", result)
				}
				assertPublicationHead(t, fixture, pr, remote, head)
			}
		})
	}
}

func TestContinueProviderPublicationUsesConfiguredSSH(t *testing.T) {
	fixture := newCreationFixture(t)
	pr := fixture.create(t, pullrequestlifecycle.StatusOpen)
	fixture.complete(t, fixture.metadata(t, pr.ID))
	fixture.awaitTarget(t, pr.ID, pullrequestlifecycle.StatusOpen)
	remote := publicationRemote(t, fixture, pr)
	id := fixture.scenario.State.HolonID
	path := fixture.scenario.State.WorktreePath
	sshURL := "git@github.com:owner/project.git"
	publicationGit(t, path, "remote", "set-url", "origin", sshURL)
	publicationGit(t, path, "config", "url."+remote+".insteadOf", sshURL)
	publicationGit(t, path, "config", "protocol.https.allow", "never")
	fixture.scenario.GitHub.SetRepositoryURLs("https://github.com/owner/project.git", "https://github.com/owner/project.git")
	db := fixture.scenario.Application.Database
	if _, err := db.Exec(`update pull_request_catalog set document=json_set(document, '$.sync_data.github.head_repository_url', ?) where id=?`, "https://github.com/owner/project.git", pr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update holons set kind=? where id=?`, holons.KindPullWorker, id); err != nil {
		t.Fatal(err)
	}
	store, err := worksqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Create(t.Context(), work.Work{ID: "ssh-continue", PullRequestID: pr.ID, SessionID: id, Kind: work.KindWorker, Mode: work.ModeContinue, Status: work.StatusRunning, HeadBranch: pr.HeadBranch, HeadCommit: pr.HeadCommit, BaseHeadCommit: pr.HeadCommit, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	head := publicationCommit(t, fixture, "ssh-publication.txt")
	fixture.request(t, "POST", "/api/v1/pull-requests/"+pr.ID+"/publish", `{}`, http.StatusOK, nil)
	assertPublicationHead(t, fixture, pr, remote, head)
	worker, err := store.Get(t.Context(), "ssh-continue")
	if err != nil || worker.Status != work.StatusRunning || worker.ResultHeadCommit != head {
		t.Fatalf("Continue after publication = %+v, %v", worker, err)
	}
	if got := publicationGit(t, path, "config", "--get", "remote.origin.url"); got != sshURL {
		t.Fatalf("origin changed to %s", got)
	}
}

func TestSharedSyncObservesWIPWithoutPublishingWorkspaceCommits(t *testing.T) {
	for _, withdrawn := range []bool{false, true} {
		t.Run(fmt.Sprint(withdrawn), func(t *testing.T) {
			fixture := newCreationFixture(t)
			target := pullrequestlifecycle.StatusWIP
			if withdrawn {
				target = pullrequestlifecycle.StatusDraft
			}
			pr := fixture.create(t, target)
			if withdrawn {
				fixture.request(t, "POST", "/api/v1/pull-requests/"+pr.ID+"/transition", `{"status":"wip"}`, http.StatusOK, nil)
				pr = fixture.pullRequest(t, pr.ID)
			}
			remote := publicationRemote(t, fixture, pr)
			workspaceHead := publicationCommit(t, fixture, "unpublished.txt")
			fixture.request(t, "POST", "/api/v1/pull-requests/"+pr.ID+"/sync", `{}`, http.StatusNoContent, nil)
			cached := fixture.pullRequest(t, pr.ID)
			if cached.ID != pr.ID || cached.Status != pullrequestlifecycle.StatusWIP || cached.SyncProvider != pr.SyncProvider || cached.SyncExternalID != pr.SyncExternalID || cached.HeadCommit != pr.HeadCommit || cached.BaseCommit != pr.BaseCommit {
				t.Fatalf("sync replaced WIP identity or published topology: %+v", cached)
			}
			var readiness holons.PublicationReadiness
			fixture.request(t, "GET", "/api/v1/holons/"+fixture.scenario.State.HolonID+"/publication-readiness", "", http.StatusOK, &readiness)
			if readiness.TargetCommit != cached.HeadCommit || readiness.WorkspaceHead != workspaceHead || !readiness.PublicationAvailable {
				t.Fatalf("readiness=%+v", readiness)
			}
			if got := publicationGit(t, remote, "rev-parse", "refs/heads/"+pr.HeadBranch); got != pr.HeadCommit {
				t.Fatalf("refresh published %s", got)
			}
			if got := publicationGit(t, fixture.scenario.State.WorktreePath, "rev-parse", "HEAD"); got != workspaceHead {
				t.Fatalf("refresh changed workspace %s", got)
			}
			fixture.request(t, "POST", "/api/v1/holons/"+fixture.scenario.State.HolonID+"/publish", `{}`, http.StatusOK, nil)
			fixture.request(t, "POST", "/api/v1/pull-requests/"+pr.ID+"/sync", `{}`, http.StatusNoContent, nil)
			cached = fixture.pullRequest(t, pr.ID)
			if cached.HeadCommit != workspaceHead || cached.Status != pullrequestlifecycle.StatusWIP || cached.SyncExternalID != pr.SyncExternalID {
				t.Fatalf("WIP publication=%+v", cached)
			}
			wantCreations := 0
			if withdrawn {
				wantCreations = 1
			}
			if got := len(fixture.scenario.GitHub.CreateRequests()); got != wantCreations {
				t.Fatalf("sync or Push opened a GitHub PR: %d", got)
			}
		})
	}
}
