package localapp_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/testfixture/pullrequestfixture"
)

func TestPullRequestCreationReachesRequestedTarget(t *testing.T) {
	for _, target := range []pullrequestlifecycle.Status{pullrequestlifecycle.StatusWIP, pullrequestlifecycle.StatusDraft, pullrequestlifecycle.StatusOpen} {
		t.Run(string(target), func(t *testing.T) {
			fixture := newCreationFixture(t)
			created := fixture.create(t, target)
			metadata := fixture.metadata(t, created.ID)
			fixture.assertBrowsable(t, created.ID)
			if target != pullrequestlifecycle.StatusOpen {
				if created.Status != target || created.Title != "Initial title" || created.Summary != "" || metadata.AgentSession != nil || metadata.PreparationTarget != "" {
					t.Fatalf("creation prepared metadata: PR=%+v metadata=%+v", created, metadata)
				}
				requests := fixture.scenario.GitHub.CreateRequests()
				if target == pullrequestlifecycle.StatusDraft && (len(requests) != 1 || !requests[0].Draft || requests[0].Title != "Initial title" || requests[0].Body != "") {
					t.Fatalf("draft creation = %+v", requests)
				}
				if target == pullrequestlifecycle.StatusWIP && len(requests) != 0 {
					t.Fatalf("WIP published: %+v", requests)
				}
			} else {
				if created.Status != pullrequestlifecycle.StatusWIP || metadata.PreparationTarget != pullrequestmetadata.PreparationTargetOpen || metadata.AgentSession == nil {
					t.Fatalf("preparation = %+v", metadata)
				}
				fixture.assertLinked(t, created.ID, fixture.scenario.State.HolonID, metadata.AgentSession.ID)
				if !slices.Contains(created.LinkedHolonIDs, metadata.AgentSession.ID) {
					t.Fatalf("creation response lacks metadata Holon: %+v", created)
				}
				fixture.assertPublicationBlocked(t, created.ID, true)
				for _, status := range []string{"draft", "open"} {
					fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"`+status+`"}`, http.StatusConflict, nil)
				}
				if after := fixture.metadata(t, created.ID); after.AgentSession.ID != metadata.AgentSession.ID {
					t.Fatal("duplicate generation")
				}
				fixture.complete(t, metadata)
				fixture.awaitTarget(t, created.ID, target)
				requests := fixture.scenario.GitHub.CreateRequests()
				if len(requests) != 1 || requests[0].Title != generatedTitle || requests[0].Body != generatedDescription || requests[0].Draft {
					t.Fatalf("publication = %+v", requests)
				}
			}
			fixture.reopen(t)
			fixture.assertPublicationBlocked(t, created.ID, false)
			if saved := fixture.pullRequest(t, created.ID); saved.Status != target {
				t.Fatalf("persisted PR = %+v", saved)
			}
			if saved := fixture.metadata(t, created.ID); saved.PreparationTarget != "" {
				t.Fatalf("completed target retained: %+v", saved)
			}
		})
	}
}

func TestPullRequestCreationReconcilesDiffBaseAfterManualRebase(t *testing.T) {
	fixture := newCreationFixture(t)
	worktreePath := fixture.scenario.State.WorktreePath

	// Keep this scenario to one feature commit so the commits endpoint can
	// distinguish it exactly from the later main-branch commit.
	originalBase := metadataGit(t, worktreePath, "rev-parse", "HEAD~2")
	metadataGit(t, worktreePath, "reset", "--soft", originalBase)
	metadataGit(t, worktreePath, "commit", "-m", "Add pull request creation scenario")

	repositoryPath, _ := fixture.addRemote(t)

	metadataWrite(t, filepath.Join(repositoryPath, "upstream.txt"), "upstream work\n")
	metadataGit(t, repositoryPath, "add", "upstream.txt")
	metadataGit(t, repositoryPath, "commit", "-m", "Advance main")
	metadataGit(t, repositoryPath, "push", "origin", "main")
	mainHead := metadataGit(t, repositoryPath, "rev-parse", "HEAD")

	metadataGit(t, worktreePath, "fetch", "origin", "main")
	metadataGit(t, worktreePath, "rebase", "origin/main")
	featureHead := metadataGit(t, worktreePath, "rev-parse", "HEAD")

	created := fixture.create(t, pullrequestlifecycle.StatusWIP)
	if created.BaseCommit != mainHead || created.DiffBaseCommit != mainHead || created.HeadCommit != featureHead {
		t.Errorf("created PR commits: base=%s diff base=%s head=%s; want base=%s diff base=%s head=%s",
			created.BaseCommit, created.DiffBaseCommit, created.HeadCommit, mainHead, mainHead, featureHead)
	}

	var commits struct {
		Data []repository.Commit `json:"data"`
	}
	fixture.request(t, "GET", "/api/v1/pull-requests/"+created.ID+"/commits", "", http.StatusOK, &commits)
	if len(commits.Data) != 1 || commits.Data[0].SHA != featureHead {
		t.Errorf("PR commits = %+v, want only feature commit %s and not main commit %s", commits, featureHead, mainHead)
	}
}

func TestPullRequestCreationRecoversMetadataFailure(t *testing.T) {
	for _, failure := range []string{"launch", "generation"} {
		t.Run(failure, func(t *testing.T) {
			fixture := newCreationFixture(t)
			if failure == "launch" {
				fixture.scenario.Agents.SetStartError(pullrequestmetadata.ErrAgentUnavailable)
			}
			created := fixture.create(t, pullrequestlifecycle.StatusOpen)
			metadata := fixture.metadata(t, created.ID)
			if metadata.PreparationTarget != pullrequestmetadata.PreparationTargetOpen {
				t.Fatalf("lost requested target after failure: %+v", metadata)
			}
			if failure == "launch" {
				if metadata.AgentSession != nil {
					t.Fatalf("unexpected session after failed launch: %+v", metadata)
				}
				fixture.scenario.Agents.SetStartError(nil)
			} else if failure == "generation" {
				if metadata.AgentSession == nil {
					t.Fatal("metadata agent did not start")
				}
				if err := fixture.scenario.Agents.SetTurnError(t.Context(), metadata.AgentSession.ID, "Metadata generation failed"); err != nil {
					t.Fatal(err)
				}
				failed := fixture.metadata(t, created.ID)
				if failed.AgentSession == nil || failed.AgentSession.Status != "failed" || failed.AgentSession.Error != "Metadata generation failed" {
					t.Fatalf("failure not exposed through API: %+v", failed)
				}
			}
			fixture.assertBrowsable(t, created.ID)
			fixture.assertPublicationBlocked(t, created.ID, true)
			if saved := fixture.pullRequest(t, created.ID); saved.Status != pullrequestlifecycle.StatusWIP || saved.Title != "Initial title" {
				t.Fatalf("PR was not retained after failure: %+v", saved)
			}
			if requests := fixture.scenario.GitHub.CreateRequests(); len(requests) != 0 {
				t.Fatalf("published despite metadata failure: %+v", requests)
			}

			var retried pullrequestmetadata.Metadata
			fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/metadata/agent", `{"action":"regenerate"}`, http.StatusAccepted, &retried)
			if retried.AgentSession == nil || retried.AgentSession.Status != "running" || retried.AgentSession.Error != "" {
				t.Fatalf("retry did not start: %+v", retried)
			}
			fixture.assertLinked(t, created.ID, fixture.scenario.State.HolonID, retried.AgentSession.ID)
			fixture.complete(t, retried)
			fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusOpen)
		})
	}
}

func TestPullRequestCreationRejectsInvalidTargetContract(t *testing.T) {
	fixture := newCreationFixture(t)
	for _, body := range []string{
		`{"title":"Repair"}`,
		`{"title":"Repair","target":"closed"}`,
		`{"title":"Repair","target":"open","status":"open"}`,
		`{"title":"Repair","target":"open","prepare":true}`,
	} {
		var problem struct {
			Code string `json:"code"`
		}
		fixture.request(t, "POST", "/api/v1/holons/"+fixture.scenario.State.HolonID+"/pull-request", body, http.StatusBadRequest, &problem)
		if problem.Code != "invalid_request" {
			t.Fatalf("invalid contract: %+v", problem)
		}
	}
	var prs []pullrequestlifecycle.PullRequest
	fixture.request(t, "GET", "/api/v1/pull-requests", "", http.StatusOK, &prs)
	if len(prs) != 0 {
		t.Fatalf("invalid requests created PRs: %+v", prs)
	}
}

func TestPullRequestCreationReportsTargetPersistenceFailure(t *testing.T) {
	fixture := newCreationFixture(t)
	// Fail only the preparation write, leaving the real catalog and SQLite
	// services available so the public API can expose the retained local PR.
	_, err := fixture.scenario.Application.Database.ExecContext(t.Context(), `create temp trigger reject_preparation before insert on pull_request_metadata begin select raise(fail, 'preparation unavailable'); end`)
	if err != nil {
		t.Fatal(err)
	}
	fixture.create(t, pullrequestlifecycle.StatusOpen)
	var prs []pullrequestlifecycle.PullRequest
	fixture.request(t, "GET", "/api/v1/pull-requests", "", http.StatusOK, &prs)
	if len(prs) != 1 || prs[0].Status != pullrequestlifecycle.StatusWIP {
		t.Fatalf("retained PRs = %+v", prs)
	}
	fixture.assertBrowsable(t, prs[0].ID)
	if metadata := fixture.metadata(t, prs[0].ID); metadata.AgentSession != nil {
		t.Fatalf("agent started before target was saved: %+v", metadata)
	}
	if requests := fixture.scenario.GitHub.CreateRequests(); len(requests) != 0 {
		t.Fatalf("published despite preparation failure: %+v", requests)
	}
}

func TestPullRequestCreationCannotPublishCompletedHolonBeforeArtifactImport(t *testing.T) {
	for _, anotherPR := range []bool{false, true} {
		name := "same PR"
		if anotherPR {
			name = "another PR with description"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newCreationFixture(t)
			created := fixture.create(t, pullrequestlifecycle.StatusOpen)
			metadata := fixture.metadata(t, created.ID)
			openingID := created.ID
			if anotherPR {
				catalog, err := prsqlite.New(t.Context(), fixture.scenario.Application.Database)
				if err != nil {
					t.Fatal(err)
				}
				other, ok := catalog.GetPullRequest(created.ID)
				if !ok {
					t.Fatal("created PR missing from catalog")
				}
				other.ID, other.Status, other.Summary = "other-pr", pullrequestlifecycle.StatusDraft, "Existing description"
				other.LinkedHolonIDs = nil
				if _, err := catalog.CreatePullRequest(other); err != nil {
					t.Fatal(err)
				}
				openingID = other.ID
			}
			started, release := fixture.scenario.Agents.PauseArtifactRead()
			defer release()
			fixture.complete(t, metadata)
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("artifact import did not start")
			}
			fixture.assertPublicationBlocked(t, created.ID, true)
			fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"draft"}`, http.StatusConflict, nil)
			request := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/pull-requests/"+openingID+"/transition", strings.NewReader(`{"status":"open"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				fixture.scenario.Application.Handler.ServeHTTP(response, request)
				close(done)
			}()
			select {
			case <-done:
				t.Fatalf("opening returned before import: %d %s", response.Code, response.Body.String())
			case <-time.After(100 * time.Millisecond):
			}
			if requests := fixture.scenario.GitHub.CreateRequests(); len(requests) != 0 {
				t.Fatalf("published before import: %+v", requests)
			}
			release()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("opening did not finish after import")
			}
			if response.Code != http.StatusOK {
				t.Fatalf("opening after import: %d %s", response.Code, response.Body.String())
			}
			fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusOpen)
			if anotherPR {
				if saved := fixture.pullRequest(t, openingID); saved.Status != pullrequestlifecycle.StatusOpen || saved.Summary != "Existing description" {
					t.Fatalf("other PR opening = %+v", saved)
				}
				if saved := fixture.metadata(t, openingID); saved.AgentSession != nil || saved.PreparationTarget != "" {
					t.Fatalf("other PR generated metadata: %+v", saved)
				}
			}
		})
	}
}

func TestPullRequestCreationRetainsMetadataWhenPublicationFails(t *testing.T) {
	fixture := newCreationFixture(t)
	fixture.scenario.GitHub.SetCreateError(errors.New("GitHub unavailable"))
	created := fixture.create(t, pullrequestlifecycle.StatusOpen)
	fixture.complete(t, fixture.metadata(t, created.ID))
	fixture.await(t, func() bool {
		metadata := fixture.metadata(t, created.ID)
		return metadata.ApplicationError != nil && metadata.ApplicationError.Operation == "publication" && metadata.AgentSession != nil && metadata.AgentSession.Error == ""
	})
	saved := fixture.pullRequest(t, created.ID)
	if saved.Status != pullrequestlifecycle.StatusWIP || saved.Title != generatedTitle || saved.Summary != generatedDescription || saved.SyncProvider != "" {
		t.Fatalf("publication failure lost local metadata: %+v", saved)
	}
	fixture.assertBrowsable(t, created.ID)
	fixture.assertPublicationBlocked(t, created.ID, true)
	fixture.scenario.GitHub.SetCreateError(nil)
	fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/metadata/retry", "", http.StatusOK, nil)
	fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusOpen)
	if requests := fixture.scenario.GitHub.CreateRequests(); len(requests) != 2 || requests[1].Title != generatedTitle || requests[1].Body != generatedDescription {
		t.Fatalf("publication retry did not reuse saved metadata: %+v", requests)
	}
}

const generatedTitle = "Repair checkout validation"
const generatedDescription = "Validate checkout input before saving the order."

type creationFixture struct {
	root     string
	scenario *pullrequestfixture.Scenario
	server   *httptest.Server
	client   *http.Client
}

func newCreationFixture(t *testing.T) *creationFixture {
	t.Helper()
	fixture := &creationFixture{root: t.TempDir()}
	var err error
	fixture.scenario, err = pullrequestfixture.New(t.Context(), fixture.root, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Keep generation coverage on a branch with multiple new commits.
	path := fixture.scenario.State.WorktreePath
	metadataGit(t, path, "commit", "--allow-empty", "-m", "Add pull request creation scenario")
	fixture.scenario.GitHub.SetCommits(metadataGit(t, path, "rev-parse", "HEAD~2"), metadataGit(t, path, "rev-parse", "HEAD"))
	fixture.server = httptest.NewServer(fixture.scenario.Application.Handler)
	fixture.client = fixture.server.Client()
	fixture.client.Timeout = 5 * time.Second
	t.Cleanup(func() {
		fixture.server.Close()
		if err := fixture.scenario.Close(); err != nil {
			t.Error(err)
		}
	})
	return fixture
}

func (fixture *creationFixture) request(t *testing.T, method, path, body string, status int, result any) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, fixture.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := fixture.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s: status %d, want %d; %s", method, path, response.StatusCode, status, data)
	}
	if result != nil {
		if err := json.Unmarshal(data, result); err != nil {
			t.Fatalf("%s: %v; %s", path, err, data)
		}
	}
}

func (fixture *creationFixture) create(t *testing.T, target pullrequestlifecycle.Status) pullrequestlifecycle.PullRequest {
	t.Helper()
	var created pullrequestlifecycle.PullRequest
	fixture.request(t, "POST", "/api/v1/holons/"+fixture.scenario.State.HolonID+"/pull-request", `{"title":"  Initial title  ","target":"`+string(target)+`"}`, http.StatusCreated, &created)
	if created.ID == "" {
		t.Fatalf("created PR = %+v", created)
	}
	return created
}

func (fixture *creationFixture) assertPublicationBlocked(t *testing.T, id string, want bool) {
	t.Helper()
	var result struct {
		PublicationBlocked bool `json:"publication_blocked"`
	}
	fixture.request(t, "GET", "/api/v1/pull-requests/"+id, "", http.StatusOK, &result)
	if result.PublicationBlocked != want {
		t.Fatalf("publication blocked = %v, want %v", result.PublicationBlocked, want)
	}
}

func (fixture *creationFixture) pullRequest(t *testing.T, id string) pullrequestlifecycle.PullRequest {
	t.Helper()
	var result pullrequestlifecycle.PullRequest
	fixture.request(t, "GET", "/api/v1/pull-requests/"+id, "", http.StatusOK, &result)
	return result
}

func (fixture *creationFixture) metadata(t *testing.T, id string) pullrequestmetadata.Metadata {
	t.Helper()
	var result pullrequestmetadata.Metadata
	fixture.request(t, "GET", "/api/v1/pull-requests/"+id+"/metadata", "", http.StatusOK, &result)
	return result
}

func (fixture *creationFixture) complete(t *testing.T, metadata pullrequestmetadata.Metadata) {
	t.Helper()
	if metadata.AgentSession == nil {
		t.Fatal("metadata agent did not start")
	}
	if err := fixture.scenario.Agents.Complete(t.Context(), metadata.AgentSession.ID, generatedTitle, generatedDescription); err != nil {
		t.Fatal(err)
	}
}

func (fixture *creationFixture) assertBrowsable(t *testing.T, id string) {
	t.Helper()
	var commits struct {
		Data []repository.Commit `json:"data"`
	}
	fixture.request(t, "GET", "/api/v1/pull-requests/"+id+"/commits", "", http.StatusOK, &commits)
	if len(commits.Data) != 2 || commits.Data[0].Message != "Add pull request creation scenario" {
		t.Fatalf("PR commits = %+v", commits)
	}
	var changes struct {
		Data repository.WorkspaceInspection `json:"data"`
	}
	fixture.request(t, "GET", "/api/v1/pull-requests/"+id+"/changes", "", http.StatusOK, &changes)
	if len(changes.Data.Files) != 1 || changes.Data.Files[0].Path != "scenario.txt" || !strings.Contains(changes.Data.Files[0].Diff, "+pull request creation browser scenario") {
		t.Fatalf("PR changes = %+v", changes)
	}
}

func (fixture *creationFixture) assertLinked(t *testing.T, id string, holonIDs ...string) {
	t.Helper()
	var links []pullrequestlifecycle.HolonLink
	fixture.request(t, "GET", "/api/v1/pull-request-holon-links", "", http.StatusOK, &links)
	for _, holonID := range holonIDs {
		if !slices.Contains(links, pullrequestlifecycle.HolonLink{PullRequestID: id, HolonID: holonID}) {
			t.Fatalf("Holon %s missing from PR links: %+v", holonID, links)
		}
		var holon holons.Holon
		fixture.request(t, "GET", "/api/v1/holons/"+holonID, "", http.StatusOK, &holon)
		if holon.ID != holonID {
			t.Fatalf("Holon page returned %+v", holon)
		}
		var reference struct {
			PullRequest pullrequestlifecycle.PullRequest `json:"pull_request"`
		}
		fixture.request(t, "GET", "/api/v1/holons/"+holonID+"/pull-request", "", http.StatusOK, &reference)
		if reference.PullRequest.ID != id {
			t.Fatalf("Holon %s links to %s, want %s", holonID, reference.PullRequest.ID, id)
		}
	}
}

func (fixture *creationFixture) awaitTarget(t *testing.T, id string, target pullrequestlifecycle.Status) pullrequestlifecycle.PullRequest {
	t.Helper()
	var result pullrequestlifecycle.PullRequest
	fixture.await(t, func() bool {
		result = fixture.pullRequest(t, id)
		return result.Status == target && result.Title == generatedTitle && result.Summary == generatedDescription
	})
	metadata := fixture.metadata(t, id)
	if metadata.Title != generatedTitle || metadata.Description != generatedDescription {
		t.Fatalf("metadata disagrees with PR: %+v", metadata)
	}
	return result
}

func (fixture *creationFixture) await(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-deadline.C:
			var prs []pullrequestlifecycle.PullRequest
			fixture.request(t, "GET", "/api/v1/pull-requests", "", http.StatusOK, &prs)
			t.Fatalf("timed out waiting for workflow; PRs=%+v", prs)
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-tick.C:
		}
	}
}

func (fixture *creationFixture) reopen(t *testing.T) {
	t.Helper()
	fixture.server.Close()
	if err := fixture.scenario.Close(); err != nil {
		t.Fatal(err)
	}
	scenario, err := pullrequestfixture.Open(t.Context(), fixture.root, 0)
	if err != nil {
		t.Fatal(err)
	}
	fixture.scenario = scenario
	fixture.server = httptest.NewServer(scenario.Application.Handler)
	fixture.client = fixture.server.Client()
	fixture.client.Timeout = 5 * time.Second
}

func (fixture *creationFixture) addRemote(t *testing.T) (string, string) {
	t.Helper()
	repositoryPath := filepath.Join(fixture.root, "repository")
	remotePath := filepath.Join(fixture.root, "remote.git")
	metadataGit(t, repositoryPath, "init", "--bare", "-b", "main", remotePath)
	metadataGit(t, repositoryPath, "remote", "set-url", "origin", remotePath)
	metadataGit(t, repositoryPath, "push", "-u", "origin", "main")

	// Reopen after adding the remote so the real application repository uses
	// its normal fetch and publication paths.
	state := fixture.scenario.State
	fixture.reopen(t)
	fixture.scenario.State = state
	return repositoryPath, remotePath
}

func TestPullRequestCreationReusesSingleCommitMessage(t *testing.T) {
	for _, test := range []struct {
		name, body       string
		publicationFails bool
	}{
		{name: "full message", body: "Explain the change.\n\nPreserve the detailed rationale."},
		{name: "publication retry", body: "Keep this explanation when retrying.", publicationFails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := test.body
			fixture := newCreationFixture(t)
			path := fixture.scenario.State.WorktreePath
			metadataGit(t, path, "reset", "--soft", "HEAD~1")
			metadataGit(t, path, "commit", "--amend", "-m", "Reuse this commit title", "-m", body)
			fixture.scenario.GitHub.SetCommits(metadataGit(t, path, "rev-parse", "HEAD~1"), metadataGit(t, path, "rev-parse", "HEAD"))
			if test.publicationFails {
				fixture.scenario.GitHub.SetCreateError(errors.New("GitHub unavailable"))
			}
			created := fixture.create(t, pullrequestlifecycle.StatusOpen)
			if test.publicationFails {
				pending := fixture.metadata(t, created.ID)
				if pending.ApplicationError == nil || pending.ApplicationError.Operation != "publication" || pending.AgentSession != nil || pending.Description != body {
					t.Fatalf("pending publication = %+v", pending)
				}
				fixture.scenario.GitHub.SetCreateError(nil)
				fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/metadata/retry", "", http.StatusOK, nil)
			}
			saved := fixture.pullRequest(t, created.ID)
			metadata := fixture.metadata(t, created.ID)
			if !test.publicationFails && (created.Status != saved.Status || created.Title != saved.Title || created.Summary != saved.Summary) {
				t.Fatalf("synchronous creation response is stale: %+v", created)
			}
			if saved.Status != pullrequestlifecycle.StatusOpen || saved.Title != "Reuse this commit title" || saved.Summary != body {
				t.Fatalf("created PR = %+v", saved)
			}
			if metadata.AgentSession != nil || !metadata.GenerationComplete || metadata.Freshness == nil || metadata.Freshness.Outdated {
				t.Fatalf("commit metadata = %+v", metadata)
			}
			requests := fixture.scenario.GitHub.CreateRequests()
			wantRequests := 1
			if test.publicationFails {
				wantRequests = 2
			}
			if len(requests) != wantRequests || requests[len(requests)-1].Title != saved.Title || requests[len(requests)-1].Body != body {
				t.Fatalf("publication = %+v", requests)
			}
			fixture.reopen(t)
			fixture.assertPublicationBlocked(t, created.ID, false)
			if persisted := fixture.metadata(t, created.ID); persisted.Title != saved.Title || persisted.Description != body || persisted.AgentSession != nil {
				t.Fatalf("persisted metadata = %+v", persisted)
			}
		})
	}
}

func TestOpeningGeneratesMetadataForSingleCommitWithoutBody(t *testing.T) {
	for _, source := range []string{"create", "wip", "draft"} {
		for _, body := range []string{"", " \n\t "} {
			t.Run(source+"/"+body, func(t *testing.T) {
				fixture := newCreationFixture(t)
				path := fixture.scenario.State.WorktreePath
				metadataGit(t, path, "reset", "--soft", "HEAD~1")
				metadataGit(t, path, "commit", "--amend", "--cleanup=verbatim", "-m", "Commit without a description", "-m", body)
				fixture.scenario.GitHub.SetCommits(metadataGit(t, path, "rev-parse", "HEAD~1"), metadataGit(t, path, "rev-parse", "HEAD"))
				var pending pullrequestlifecycle.PullRequest
				if source == "create" {
					pending = fixture.create(t, pullrequestlifecycle.StatusOpen)
				} else {
					created := fixture.create(t, pullrequestlifecycle.Status(source))
					fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"open"}`, http.StatusOK, &pending)
				}
				metadata := fixture.metadata(t, pending.ID)
				if pending.Status == pullrequestlifecycle.StatusOpen || metadata.GenerationComplete || metadata.AgentSession == nil || metadata.PreparationTarget != pullrequestmetadata.PreparationTargetOpen {
					t.Fatalf("opening skipped metadata generation: PR=%+v metadata=%+v", pending, metadata)
				}
				fixture.assertPublicationBlocked(t, pending.ID, true)
				fixture.complete(t, metadata)
				fixture.awaitTarget(t, pending.ID, pullrequestlifecycle.StatusOpen)
				if saved := fixture.pullRequest(t, pending.ID); saved.Title != generatedTitle || saved.Summary != generatedDescription {
					t.Fatalf("generated metadata was not applied: %+v", saved)
				}
			})
		}
	}
}

func TestOpeningPreparesMissingDescriptionsFromWIPAndDraft(t *testing.T) {
	for _, source := range []string{"wip", "draft", "local draft", "withdrawn draft"} {
		for _, description := range []string{"", " \n\t "} {
			t.Run(source+"/"+description, func(t *testing.T) {
				fixture := newCreationFixture(t)
				target := pullrequestlifecycle.StatusWIP
				if source == "draft" || source == "withdrawn draft" {
					target = pullrequestlifecycle.StatusDraft
				}
				created := fixture.create(t, target)
				if source == "local draft" {
					catalog, err := prsqlite.New(t.Context(), fixture.scenario.Application.Database)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := catalog.TransitionPullRequestStatus(created.ID, pullrequestlifecycle.StatusDraft, time.Now()); err != nil {
						t.Fatal(err)
					}
					target = pullrequestlifecycle.StatusDraft
				}
				if source == "withdrawn draft" {
					fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"wip"}`, http.StatusOK, nil)
					target = pullrequestlifecycle.StatusWIP
				}
				// Exercise persisted whitespace, independent of PATCH normalization.
				if _, err := fixture.scenario.Application.Database.ExecContext(t.Context(), `update pull_requests set description = ? where id = ?`, description, created.ID); err != nil {
					t.Fatal(err)
				}
				var pending pullrequestlifecycle.PullRequest
				fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"open"}`, http.StatusOK, &pending)
				metadata := fixture.metadata(t, created.ID)
				if pending.Status != target || metadata.PreparationTarget != pullrequestmetadata.PreparationTargetOpen || metadata.AgentSession == nil {
					t.Fatalf("opening = %+v, %+v", pending, metadata)
				}
				fixture.assertPublicationBlocked(t, created.ID, true)
				competing := "draft"
				if target == pullrequestlifecycle.StatusDraft {
					competing = "wip"
				}
				fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"`+competing+`"}`, http.StatusConflict, nil)
				fixture.complete(t, metadata)
				fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusOpen)
				fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"draft"}`, http.StatusOK, nil)
				fixture.assertPublicationBlocked(t, created.ID, false)
				fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"open"}`, http.StatusOK, nil)
				if after := fixture.metadata(t, created.ID); after.PreparationTarget != "" || after.AgentSession.ID != metadata.AgentSession.ID {
					t.Fatalf("completed preparation revived: %+v", after)
				}
			})
		}
	}
}

func TestOpeningPreservesExistingMetadata(t *testing.T) {
	for _, source := range []string{"create", "wip", "draft"} {
		t.Run(source, func(t *testing.T) {
			fixture := newCreationFixture(t)
			var opened pullrequestlifecycle.PullRequest
			if source == "create" {
				fixture.request(t, "POST", "/api/v1/holons/"+fixture.scenario.State.HolonID+"/pull-request", `{"title":"User title","summary":"User description","target":"open"}`, http.StatusCreated, &opened)
			} else {
				created := fixture.create(t, pullrequestlifecycle.Status(source))
				fixture.request(t, "PATCH", "/api/v1/pull-requests/"+created.ID+"/metadata", `{"title":"User title","description":"User description"}`, http.StatusOK, nil)
				fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"open"}`, http.StatusOK, &opened)
			}
			if opened.Status != pullrequestlifecycle.StatusOpen || opened.Title != "User title" || opened.Summary != "User description" {
				t.Fatalf("metadata replaced: %+v", opened)
			}
			if metadata := fixture.metadata(t, opened.ID); metadata.AgentSession != nil || metadata.PreparationTarget != "" {
				t.Fatalf("generation started: %+v", metadata)
			}
		})
	}
}

func TestManualMetadataGenerationDoesNotRequestOpening(t *testing.T) {
	for _, target := range []pullrequestlifecycle.Status{pullrequestlifecycle.StatusWIP, pullrequestlifecycle.StatusDraft} {
		t.Run(string(target), func(t *testing.T) {
			fixture := newCreationFixture(t)
			created := fixture.create(t, target)
			var metadata pullrequestmetadata.Metadata
			fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/metadata/agent", `{"action":"regenerate"}`, http.StatusAccepted, &metadata)
			fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"open"}`, http.StatusConflict, nil)
			if pending := fixture.metadata(t, created.ID); pending.PreparationTarget != "" {
				t.Fatalf("manual generation recorded opening: %+v", pending)
			}
			fixture.complete(t, metadata)
			fixture.awaitTarget(t, created.ID, target)
			fixture.assertPublicationBlocked(t, created.ID, false)
		})
	}
}

func TestClosingClearsPendingOpening(t *testing.T) {
	fixture := newCreationFixture(t)
	created := fixture.create(t, pullrequestlifecycle.StatusOpen)
	metadata := fixture.metadata(t, created.ID)
	fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"closed"}`, http.StatusOK, nil)
	fixture.complete(t, metadata)
	fixture.reopen(t)
	if saved := fixture.pullRequest(t, created.ID); saved.Status != pullrequestlifecycle.StatusClosed {
		t.Fatalf("completion reopened PR: %+v", saved)
	}
	var target string
	if err := fixture.scenario.Application.Database.QueryRowContext(t.Context(), `select coalesce(preparation_target, '') from pull_request_metadata where pull_request_id = ?`, created.ID).Scan(&target); err != nil || target != "" {
		t.Fatalf("opening request remains: %q, %v", target, err)
	}
	if requests := fixture.scenario.GitHub.CreateRequests(); len(requests) != 0 {
		t.Fatalf("closed PR published: %+v", requests)
	}
}

func TestDraftCreationFailureRetainsWIPForRetry(t *testing.T) {
	fixture := newCreationFixture(t)
	fixture.scenario.GitHub.SetCreateError(errors.New("GitHub unavailable"))
	created := fixture.create(t, pullrequestlifecycle.StatusDraft)
	if created.Status != pullrequestlifecycle.StatusWIP {
		t.Fatalf("failed draft = %+v", created)
	}
	if metadata := fixture.metadata(t, created.ID); metadata.AgentSession != nil || metadata.PreparationTarget != "" {
		t.Fatalf("draft prepared metadata: %+v", metadata)
	}
	fixture.scenario.GitHub.SetCreateError(nil)
	fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"draft"}`, http.StatusOK, nil)
	if saved := fixture.pullRequest(t, created.ID); saved.Status != pullrequestlifecycle.StatusDraft {
		t.Fatalf("draft retry = %+v", saved)
	}
}

func TestOpeningRecoversInterruptedGenerationAfterRestart(t *testing.T) {
	fixture := newCreationFixture(t)
	created := fixture.create(t, pullrequestlifecycle.StatusOpen)
	fixture.reopen(t)
	fixture.scenario.GitHub.SetCommits(created.BaseCommit, created.HeadCommit)
	fixture.assertPublicationBlocked(t, created.ID, true)
	metadata := fixture.metadata(t, created.ID)
	if metadata.PreparationTarget != pullrequestmetadata.PreparationTargetOpen || metadata.AgentSession == nil || metadata.AgentSession.Status != "lost" {
		t.Fatalf("interrupted generation = %+v", metadata)
	}
	fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/metadata/agent", `{"action":"regenerate"}`, http.StatusAccepted, &metadata)
	fixture.complete(t, metadata)
	fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusOpen)
}

func TestOpeningRecoversWhenGitHubReadinessFails(t *testing.T) {
	for _, recovery := range []string{"retry", "close"} {
		t.Run(recovery, func(t *testing.T) {
			for _, target := range []pullrequestlifecycle.Status{pullrequestlifecycle.StatusWIP, pullrequestlifecycle.StatusDraft} {
				t.Run(string(target), func(t *testing.T) {
					fixture := newCreationFixture(t)
					created := fixture.create(t, pullrequestlifecycle.StatusDraft)
					if target == pullrequestlifecycle.StatusWIP {
						fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"wip"}`, http.StatusOK, nil)
					}
					fixture.scenario.GitHub.SetReadyError(errors.New("GitHub unavailable"))
					fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"open"}`, http.StatusOK, nil)
					metadata := fixture.metadata(t, created.ID)
					fixture.complete(t, metadata)
					fixture.await(t, func() bool { return fixture.metadata(t, created.ID).ApplicationError != nil })
					fixture.await(t, func() bool { return fixture.pullRequest(t, created.ID).Status == pullrequestlifecycle.StatusDraft })
					if saved := fixture.pullRequest(t, created.ID); saved.Status != pullrequestlifecycle.StatusDraft || saved.Title != generatedTitle || saved.Summary != generatedDescription {
						t.Fatalf("failed opening = %+v", saved)
					}
					if saved := fixture.metadata(t, created.ID); saved.ApplicationError.Operation != "publication" || saved.PreparationTarget != pullrequestmetadata.PreparationTargetOpen {
						t.Fatalf("publication retry lost: %+v", saved)
					}
					if recovery == "close" {
						fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"closed"}`, http.StatusOK, nil)
						remote, err := fixture.scenario.GitHub.Get(t.Context(), pullrequestlifecycle.GitHubPullRequestTarget{})
						if err != nil || remote.Status != pullrequestlifecycle.StatusClosed {
							t.Fatalf("GitHub PR after close = %+v, %v", remote, err)
						}
						fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/sync", "", http.StatusNoContent, nil)
						if saved := fixture.pullRequest(t, created.ID); saved.Status != pullrequestlifecycle.StatusClosed {
							t.Fatalf("sync reopened PR: %+v", saved)
						}
						return
					}
					fixture.scenario.GitHub.SetReadyError(nil)
					// A repeated Open PR request retries publication with durable output.
					fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"open"}`, http.StatusOK, nil)
					fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusOpen)
					if saved := fixture.metadata(t, created.ID); saved.AgentSession.ID != metadata.AgentSession.ID || saved.PreparationTarget != "" || saved.ApplicationError != nil {
						t.Fatalf("publication retry regenerated metadata: %+v", saved)
					}
				})
			}
		})
	}
}

func TestManualDescriptionReplacesFailedOpeningPreparation(t *testing.T) {
	for _, source := range []pullrequestlifecycle.Status{pullrequestlifecycle.StatusWIP, pullrequestlifecycle.StatusDraft} {
		for _, replacement := range []string{"Replacement description", ""} {
			t.Run(string(source)+"/"+replacement, func(t *testing.T) {
				fixture := newCreationFixture(t)
				created := fixture.create(t, source)
				fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"open"}`, http.StatusOK, nil)
				pending := fixture.metadata(t, created.ID)
				if _, err := fixture.scenario.Application.Database.ExecContext(t.Context(), `create temp trigger reject_metadata before update of description on pull_requests begin select raise(fail, 'save unavailable'); end`); err != nil {
					t.Fatal(err)
				}
				fixture.complete(t, pending)
				fixture.await(t, func() bool { return fixture.metadata(t, created.ID).ApplicationError != nil })
				if saved := fixture.metadata(t, created.ID); saved.ApplicationError.Operation != "save" {
					t.Fatalf("save failure = %+v", saved)
				}
				fixture.request(t, "PATCH", "/api/v1/pull-requests/"+created.ID+"/metadata", `{"description":"Replacement description"}`, http.StatusInternalServerError, nil)
				fixture.assertPublicationBlocked(t, created.ID, true)
				if _, err := fixture.scenario.Application.Database.ExecContext(t.Context(), `drop trigger reject_metadata`); err != nil {
					t.Fatal(err)
				}
				fixture.request(t, "PATCH", "/api/v1/pull-requests/"+created.ID+"/metadata", `{"title":"Replacement title"}`, http.StatusOK, nil)
				fixture.assertPublicationBlocked(t, created.ID, true)
				var saved pullrequestmetadata.Metadata
				fixture.request(t, "PATCH", "/api/v1/pull-requests/"+created.ID+"/metadata", `{"description":"`+replacement+`"}`, http.StatusOK, &saved)
				if saved.Description != replacement || saved.PreparationTarget != "" || saved.ApplicationError != nil {
					t.Fatalf("replacement = %+v", saved)
				}
				fixture.assertPublicationBlocked(t, created.ID, false)
				if pr := fixture.pullRequest(t, created.ID); pr.Status != source {
					t.Fatalf("manual save opened PR: %+v", pr)
				}
				fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"open"}`, http.StatusOK, nil)
				after := fixture.metadata(t, created.ID)
				if replacement == "" {
					if after.AgentSession == nil || after.AgentSession.ID == pending.AgentSession.ID || after.PreparationTarget != pullrequestmetadata.PreparationTargetOpen {
						t.Fatalf("empty replacement did not restart preparation: %+v", after)
					}
					fixture.complete(t, after)
					fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusOpen)
				} else {
					if after.AgentSession == nil || after.AgentSession.ID != pending.AgentSession.ID || after.PreparationTarget != "" || after.Description != replacement {
						t.Fatalf("replacement regenerated: %+v", after)
					}
					if pr := fixture.pullRequest(t, created.ID); pr.Status != pullrequestlifecycle.StatusOpen || pr.Title != "Replacement title" || pr.Summary != replacement {
						t.Fatalf("replacement opening = %+v", pr)
					}
				}
			})
		}
	}
}

func TestExternalReadinessClearsOpeningPreparationAfterGeneration(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		name := "completion"
		if cleanupFails {
			name = "cleanup retry"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newCreationFixture(t)
			created := fixture.create(t, pullrequestlifecycle.StatusDraft)
			fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"open"}`, http.StatusOK, nil)
			pending := fixture.metadata(t, created.ID)
			if _, err := fixture.scenario.GitHub.MarkReadyForReview(t.Context(), pullrequestlifecycle.GitHubPullRequestTarget{}); err != nil {
				t.Fatal(err)
			}
			fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/sync", "", http.StatusNoContent, nil)
			if pr := fixture.pullRequest(t, created.ID); pr.Status != pullrequestlifecycle.StatusOpen {
				t.Fatalf("external readiness = %+v", pr)
			}
			if cleanupFails {
				if _, err := fixture.scenario.Application.Database.ExecContext(t.Context(), `create temp trigger reject_cleanup before update of preparation_target on pull_request_metadata when new.preparation_target is null begin select raise(fail, 'cleanup unavailable'); end`); err != nil {
					t.Fatal(err)
				}
			}
			fixture.complete(t, pending)
			fixture.await(t, func() bool { return fixture.metadata(t, created.ID).GenerationComplete })
			if cleanupFails {
				if saved := fixture.metadata(t, created.ID); saved.ApplicationError == nil || saved.ApplicationError.Operation != "save" || saved.Description != generatedDescription {
					t.Fatalf("cleanup failure lost saved output or retry: %+v", saved)
				}
				if _, err := fixture.scenario.Application.Database.ExecContext(t.Context(), `drop trigger reject_cleanup`); err != nil {
					t.Fatal(err)
				}
				fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/metadata/retry", "", http.StatusOK, nil)
			}
			fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusOpen)
			fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"draft"}`, http.StatusOK, nil)
			fixture.assertPublicationBlocked(t, created.ID, false)
			fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"open"}`, http.StatusOK, nil)
			if saved := fixture.metadata(t, created.ID); saved.PreparationTarget != "" || saved.ApplicationError != nil || saved.AgentSession == nil || saved.AgentSession.ID != pending.AgentSession.ID || saved.Description != generatedDescription {
				t.Fatalf("external readiness revived preparation: %+v", saved)
			}
		})
	}
}
