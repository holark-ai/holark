package localapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	githubprovider "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/holons"
	holonshttp "github.com/holark-ai/holark/internal/holons/httpapi"
	holonssqlite "github.com/holark-ai/holark/internal/holons/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prhttp "github.com/holark-ai/holark/internal/pullrequestlifecycle/httpapi"
	pullrequestssqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestmerge"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	workhttp "github.com/holark-ai/holark/internal/pullrequestwork/httpapi"
	pullrequestworksqlite "github.com/holark-ai/holark/internal/pullrequestwork/sqliteadapter"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
	"github.com/holark-ai/holark/internal/repositorybrowser"
)

type gitPlumbingLauncher struct{ works []pullrequestwork.Work }

func (l *gitPlumbingLauncher) Start(_ context.Context, _ pullrequestwork.PullRequest, w pullrequestwork.Work, _ string) (string, error) {
	l.works = append(l.works, w)
	return "", errors.New("conflict agent unavailable")
}

type gitPlumbingGitHub struct {
	readiness pullrequestmerge.GitHubReadiness
	request   pullrequestmerge.GitHubMergeRequest
}

func (stub *gitPlumbingGitHub) Readiness(pullrequestmerge.GitHubTarget) (pullrequestmerge.GitHubReadiness, bool) {
	return stub.readiness, true
}

func (stub *gitPlumbingGitHub) Squash(_ context.Context, request pullrequestmerge.GitHubMergeRequest) (pullrequestmerge.GitHubMergeResult, error) {
	stub.request = request
	return pullrequestmerge.GitHubMergeResult{MergedCommit: "github-merged-commit"}, nil
}

func TestRebasePublicationReadinessAndMergeAcrossSeparateGitCaches(t *testing.T) {
	for _, scenario := range []struct {
		name                                    string
		refreshScenario                         string
		mechanicalOnly, conflicts, queued, fork bool
	}{
		{name: "automatic"},
		{name: "fork", fork: true},
		{name: "mechanical", mechanicalOnly: true},
		{name: "refresh failure", mechanicalOnly: true, refreshScenario: "failure"},
		{name: "external push after rebase", mechanicalOnly: true, refreshScenario: "external"},
		{name: "mechanical conflicts", mechanicalOnly: true, conflicts: true},
		{name: "queued against updated branches", queued: true},
		{name: "conflicts developed while queued", queued: true, conflicts: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			testRebaseAcrossGitCaches(t, scenario.mechanicalOnly, scenario.conflicts, scenario.queued, scenario.fork, scenario.refreshScenario)
		})
	}
}

func testRebaseAcrossGitCaches(t *testing.T, mechanicalOnly, conflicts, queued, fork bool, refreshScenario string) {
	t.Helper()
	ctx := t.Context()
	fixtureRoot := t.TempDir()
	remote := filepath.Join(fixtureRoot, "remote.git")
	source := filepath.Join(fixtureRoot, "source")
	userCheckout := filepath.Join(fixtureRoot, "user-checkout")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	gitPlumbingCommand(t, fixtureRoot, "init", "--bare", remote)
	gitPlumbingCommand(t, source, "init", "-b", "main")
	gitPlumbingCommand(t, source, "config", "user.name", "Holark Test")
	gitPlumbingCommand(t, source, "config", "user.email", "holark@example.test")
	gitPlumbingWrite(t, source, "README.md", "initial\n")
	gitPlumbingCommand(t, source, "add", ".")
	gitPlumbingCommand(t, source, "commit", "-m", "initial")
	gitPlumbingCommand(t, source, "remote", "add", "origin", remote)
	gitPlumbingCommand(t, source, "push", "-u", "origin", "main")
	initial := gitPlumbingOutput(t, source, "rev-parse", "HEAD")

	gitPlumbingCommand(t, source, "switch", "-c", "feature")
	gitPlumbingWrite(t, source, "feature.txt", "feature\n")
	if conflicts && !queued {
		gitPlumbingWrite(t, source, "README.md", "feature version\n")
	}
	gitPlumbingCommand(t, source, "add", ".")
	gitPlumbingCommand(t, source, "commit", "-m", "feature")
	gitPlumbingCommand(t, source, "push", "-u", "origin", "feature")
	originalHead := gitPlumbingOutput(t, source, "rev-parse", "HEAD")
	headRemote := remote
	if fork {
		headRemote = filepath.Join(fixtureRoot, "fork.git")
		gitPlumbingCommand(t, fixtureRoot, "clone", "--bare", remote, headRemote)
		gitPlumbingWrite(t, source, "fork-only.txt", "Only published by the fork\n")
		gitPlumbingCommand(t, source, "add", ".")
		gitPlumbingCommand(t, source, "commit", "-m", "Advance only the fork head")
		gitPlumbingCommand(t, source, "push", headRemote, "feature")
		originalHead = gitPlumbingOutput(t, source, "rev-parse", "HEAD")
	}

	gitPlumbingCommand(t, source, "switch", "main")
	gitPlumbingWrite(t, source, "base.txt", "base\n")
	if conflicts && !queued {
		gitPlumbingWrite(t, source, "README.md", "base version\n")
	}
	gitPlumbingCommand(t, source, "add", ".")
	gitPlumbingCommand(t, source, "commit", "-m", "advance base")
	gitPlumbingCommand(t, source, "push", "origin", "main")
	baseHead := gitPlumbingOutput(t, source, "rev-parse", "HEAD")
	gitPlumbingCommand(t, fixtureRoot, "--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/main")
	gitPlumbingCommand(t, fixtureRoot, "clone", remote, userCheckout)

	gitRepository, err := gitadapter.OpenWithWorktrees(ctx, userCheckout, filepath.Join(fixtureRoot, "user-worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	repositories := repository.NewService(gitRepository)
	if _, err = repositories.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	descriptor := repositories.Descriptor()

	db, err := database.Open(filepath.Join(fixtureRoot, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, "create table repositories(id text primary key); insert into repositories(id) values(?)", descriptor.ID); err != nil {
		t.Fatal(err)
	}
	pullRequests, err := pullrequestssqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	workStore, err := pullrequestworksqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	sources, _ := json.Marshal(map[string]any{"github": map[string]any{"head_repository_url": headRemote, "base_repository_url": remote}})
	pullRequest, err := pullRequests.CreatePullRequest(pullrequestlifecycle.PullRequest{
		SyncData: sources, ID: "pr-integration", RepositoryID: descriptor.ID, Title: "Cross-cache rebase",
		BaseBranch: "main", BaseCommit: initial, DiffBaseCommit: initial,
		HeadBranch: "feature", HeadCommit: initial, Status: pullrequestlifecycle.StatusOpen,
		SyncProvider: string(pullrequestlifecycle.SyncProviderGitHub), SyncExternalID: "github:owner/repo#1",
	})
	if err != nil {
		t.Fatal(err)
	}

	rebaseManager, err := repositorybrowser.NewManager(filepath.Join(fixtureRoot, "rebase-cache"))
	if err != nil {
		t.Fatal(err)
	}
	scheduler := pullrequestlifecycle.NewRefreshCoordinator()
	defer scheduler.Close()
	observations := &previewGitPlumbingObservations{gitPlumbingObservations: gitPlumbingObservations{repositories: repositories, catalog: pullRequests}}
	syncer := pullrequestlifecycle.New(pullRequests, pullrequestlifecycle.Options{
		Refresh: scheduler,
		Projects: pullrequestlifecycle.ProjectLookupFunc(func(id string) (pullrequestlifecycle.Project, bool) {
			return pullrequestlifecycle.Project{ID: id, RepositoryURL: remote, DefaultBranch: "main", GitHubBacked: true}, true
		}),
		Repository: localPullRequestRepository{Service: repositories}, GitHubTransport: observations, GitHubCodec: githubprovider.New(nil),
	})
	catalog := localWorkCatalog{store: pullRequests, sync: syncer}
	directRebase := localRebaser{actions: pullRequests, manager: rebaseManager, repository: repositorybrowser.Repository{
		ID: descriptor.ID, RepositoryURL: remote, DefaultBranch: "main",
	}}
	rebaser := pullrequestwork.NewRebaseCoordinator(
		catalog,
		directRebase,
		directRebase,
	)
	launcher := &gitPlumbingLauncher{}
	workService := pullrequestwork.New(workStore, catalog, launcher, nil, rebaser)
	workService.SetCompletionCommitter(pullrequestworksqlite.NewCompletionCommitter(workStore, pullRequests))

	mux := http.NewServeMux()
	workhttp.RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, workService)
	prhttp.RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, prhttp.Options{Catalog: pullRequests, Repository: repositories})
	assertDetails := func(head, base string) {
		t.Helper()
		for _, section := range []string{"commits", "changes"} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/pull-requests/"+pullRequest.ID+"/"+section, nil))
			var detail struct {
				Inputs struct {
					Head string `json:"head_commit"`
					Base string `json:"diff_base_commit"`
				} `json:"inputs"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || w.Code != http.StatusOK || detail.Inputs.Head != head || detail.Inputs.Base != base {
				t.Fatalf("%s: status=%d body=%s err=%v", section, w.Code, w.Body.String(), err)
			}
		}
	}
	readinessResponse := httptest.NewRecorder()
	mux.ServeHTTP(readinessResponse, httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/"+pullRequest.ID+"/rebase-readiness", nil))
	var readiness pullrequestwork.RebaseReadiness
	if err := json.Unmarshal(readinessResponse.Body.Bytes(), &readiness); err != nil {
		t.Fatal(err)
	}
	persisted, ok := pullRequests.GetPullRequest(pullRequest.ID)
	if readinessResponse.Code != http.StatusOK || readiness.HeadCommit != originalHead || !ok || persisted.HeadCommit != originalHead {
		t.Fatalf("readiness status=%d body=%s persisted=%+v ok=%v", readinessResponse.Code, readinessResponse.Body.String(), persisted, ok)
	}
	// The first preview repairs stale inputs and fills an empty preview cache.
	// A second preview must reuse preparation, including for conflicts and forks.
	gets, focused := observations.gets, observations.focused
	second, err := rebaser.RebaseReadiness(ctx, workPullRequest(persisted))
	if err != nil || second != readiness || observations.gets != gets || observations.focused != focused+1 {
		t.Fatalf("focused preview=%+v err=%v full=%d focused=%d", second, err, observations.gets-gets, observations.focused-focused)
	}
	if !queued && !conflicts {
		// Holon inspection and both worker dispatch paths consume the accepted head,
		// even when the base repository's same-named branch points elsewhere.
		holonStore, err := holonssqlite.New(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		holonService := holons.NewServiceWithRepository(holonStore, holonRepositoryCoordinator{repositories: repositories})
		h, err := holonService.Create(ctx, holons.Create{Title: "Cached publication target", Kind: holons.KindNormal, BaseBranch: "main", BaseCommit: baseHead, WorkSessionStartCommit: originalHead, PullRequestID: pullRequest.ID, AgentType: "codex"})
		if err != nil {
			t.Fatal(err)
		}
		gitPlumbingWrite(t, h.WorktreePath, "unpublished.txt", "Workspace only\n")
		gitPlumbingCommand(t, h.WorktreePath, "add", ".")
		gitPlumbingCommand(t, h.WorktreePath, "-c", "user.name=Test", "-c", "user.email=test@invalid", "commit", "-m", "Unpublished workspace change")
		workspaceHead := gitPlumbingOutput(t, h.WorktreePath, "rev-parse", "HEAD")
		readers := &rebaseAgentCoordinator{reservations: pullrequestssqlite.NewHolonPublicationCommitter(pullRequests, holonStore), holons: holonService, catalog: pullRequests, sync: syncer, repositoryID: descriptor.ID}
		holonService.SetPublicationTargets(readers)
		// Readiness is a cached reader even when remote synchronization is unavailable.
		readers.sync = nil
		holonHandler := holonshttp.New(&terminalHolonService{Service: holonService, rebaseAgents: readers})
		readHolonReadiness := func() holons.PublicationReadiness {
			t.Helper()
			response := httptest.NewRecorder()
			holonHandler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/holons/"+h.ID+"/publication-readiness", nil))
			var readiness holons.PublicationReadiness
			if response.Code != http.StatusOK {
				t.Fatalf("cached readiness status=%d body=%s", response.Code, response.Body.String())
			}
			if err := json.Unmarshal(response.Body.Bytes(), &readiness); err != nil {
				t.Fatal(err)
			}
			return readiness
		}
		httpReadiness := readHolonReadiness()
		if httpReadiness.TargetCommit != originalHead || httpReadiness.WorkspaceHead != workspaceHead || !httpReadiness.PublicationAvailable {
			t.Fatalf("Holon readiness=%+v", httpReadiness)
		}
		// Internal reservation inputs are deliberately not exposed by HTTP.
		holonReadiness, err := readers.PublicationReadiness(ctx, h.ID)
		if err != nil {
			t.Fatal(err)
		}

		readers.sync = syncer

		if !readers.AcceptsPublicationTarget(h, holonReadiness) {
			t.Fatal("fresh target rejected before reservation")
		}
		captured, _ := pullRequests.GetPullRequest(pullRequest.ID)
		claim, _, err := pullRequests.BeginOperation(ctx, pullrequestlifecycle.Operation{RequestID: "readiness-gap", PullRequestID: captured.ID, Kind: "publish", ExpectedInputs: pullrequestlifecycle.CaptureMutationInputs(captured), Groups: []pullrequestlifecycle.FieldGroup{pullrequestlifecycle.TopologyGroup}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pullRequests.CompleteOperation(ctx, claim.RequestID, "failed", "no remote mutation"); err != nil {
			t.Fatal(err)
		}
		staleReadiness := readHolonReadiness()
		if staleReadiness.PublicationAvailable || staleReadiness.RebaseAvailable || staleReadiness.Reason == "" {
			t.Fatalf("stale comparison enabled workspace actions: %+v", staleReadiness)
		}
		// The final transaction must reject a target that passed the earlier check.
		if err = readers.ReservePublicationTarget(ctx, h, holonReadiness); !errors.Is(err, holons.ErrRebaseRequired) {
			t.Fatalf("transaction reserved a changed target: %v", err)
		}
		if _, err = holonService.Synchronize(ctx, h.ID, h.AgentSessions[0].ID, &holonReadiness); !errors.Is(err, holons.ErrRebaseRequired) {
			t.Fatalf("stale cached reservation accepted: %v", err)
		}
		latestHolon, _ := holonService.Get(ctx, h.ID)
		if latestHolon.RebaseAttempt != nil {
			t.Fatalf("stale reservation persisted: %+v", latestHolon.RebaseAttempt)
		}

		if fork {
			if err := syncer.SyncPullRequest(ctx, pullRequest.ID); err != nil {
				t.Fatal(err)
			}
			// Recover Address after Git succeeded but its Holon update failed. A later
			// shared observation is allowed to see that exact head before the retry.
			captured, _ = pullRequests.GetPullRequest(pullRequest.ID)
			interrupted, _, err := pullRequests.BeginOperation(ctx, pullrequestlifecycle.Operation{RequestID: "address-interrupted", PullRequestID: captured.ID, HolonID: h.ID, Kind: "work_publish", ExpectedHead: originalHead, ExpectedInputs: pullrequestlifecycle.CaptureMutationInputs(captured), Groups: []pullrequestlifecycle.FieldGroup{pullrequestlifecycle.TopologyGroup}})
			if err != nil {
				t.Fatal(err)
			}
			if err = pullRequests.RecordOperationStep(ctx, interrupted.RequestID, captured.ID, "publication", pullrequestlifecycle.OperationStep{Status: "running", HeadCommit: workspaceHead}); err != nil {
				t.Fatal(err)
			}
			gitPlumbingCommand(t, h.WorktreePath, "push", headRemote, workspaceHead+":refs/heads/feature")
			if _, err = pullRequests.CompleteOperation(ctx, interrupted.RequestID, "failed", "workspace checkpoint failed"); err != nil {
				t.Fatal(err)
			}
			if err = syncer.SyncPullRequest(ctx, captured.ID); err != nil {
				t.Fatal(err)
			}
			publisher := localWorkPublisher{holons: &terminalHolonService{Service: holonService}, actions: pullRequests, sync: syncer}
			recovered, err := publisher.Publish(ctx, pullrequestwork.Work{ID: "recover-address", PullRequestID: captured.ID, SessionID: h.ID, Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, HeadBranch: "feature", HeadCommit: originalHead, PublicationTargetCommit: originalHead, PendingCompletion: &pullrequestwork.PendingAddressCompletion{PublicationAttempted: true, State: "ready", Completion: pullrequestwork.Completion{ResultHeadCommit: workspaceHead}}}, pullrequestwork.PublicationOptions{})
			if err != nil || !recovered.CheckpointOnly || recovered.HeadCommit != workspaceHead {
				t.Fatalf("Address recovery=%+v err=%v", recovered, err)
			}
			if _, err = pullRequests.CompleteOperation(ctx, recovered.OperationID, "succeeded", ""); err != nil {
				t.Fatal(err)
			}
			// Restore this fixture's published input for the independent rebase checks.
			gitPlumbingCommand(t, h.WorktreePath, "push", "--force", headRemote, originalHead+":refs/heads/feature")
			if err = syncer.SyncPullRequest(ctx, captured.ID); err != nil {
				t.Fatal(err)
			}
		}
		recorder := &cachedReaderLauncher{}
		workers := pullrequestwork.New(workStore, catalog, recorder, nil, nil)
		for _, mode := range []pullrequestwork.Mode{pullrequestwork.ModeContinue, pullrequestwork.ModeAuto} {
			_, err = workers.Start(ctx, pullrequestwork.Start{PullRequestID: pullRequest.ID, Kind: pullrequestwork.KindWorker, Mode: mode, CommentIDs: []string{"comment"}})
			if mode == pullrequestwork.ModeContinue && !errors.Is(err, errCacheReaderLaunch) {
				t.Fatalf("Continue launch=%v", err)
			}
		}
		if err := workers.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		if len(recorder.inputs) != 2 {
			t.Fatalf("worker inputs=%+v", recorder.inputs)
		}
		for _, input := range recorder.inputs {
			if input.HeadCommit != originalHead || input.BaseCommit != baseHead || input.HeadRepositoryURL != headRemote {
				t.Fatalf("worker used different cached inputs: %+v", input)
			}
		}
		if head := gitPlumbingOutput(t, headRemote, "rev-parse", "refs/heads/feature"); head != originalHead {
			t.Fatalf("reader published workspace: %s", head)
		}
	}
	if queued {
		if err := workStore.Create(ctx, pullrequestwork.Work{ID: "blocker", PullRequestID: pullRequest.ID, Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusRunning, HeadCommit: originalHead}); err != nil {
			t.Fatal(err)
		}
	}
	entered, release, cached := make(chan struct{}), make(chan struct{}), make(chan error, 8)
	var releaseOnce sync.Once
	releaseRefresh := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRefresh()
	var holdOnce sync.Once
	completionSyncer := pullrequestlifecycle.New(pullRequests, pullrequestlifecycle.Options{
		Refresh: scheduler,
		Projects: pullrequestlifecycle.ProjectLookupFunc(func(id string) (pullrequestlifecycle.Project, bool) {
			return pullrequestlifecycle.Project{ID: id, RepositoryURL: remote, DefaultBranch: "main", GitHubBacked: true}, true
		}),
		Repository: heldCompletionRepository{localPullRequestRepository: localPullRequestRepository{Service: repositories}, beforeCache: func() {
			holdOnce.Do(func() { close(entered); <-release })
		}, afterCache: func(err error) {
			select {
			case cached <- err:
			default:
			}
		}},
		GitHubTransport: gitPlumbingObservations{repositories: repositories, catalog: pullRequests}, GitHubCodec: githubprovider.New(nil),
	})
	if !conflicts {
		pullRequests.SetActionCompletionHook(func(id string) {
			inputs, err := pullRequests.CaptureComparison(ctx, id)
			if errors.Is(err, pullrequestlifecycle.ErrOperationInProgress) {
				t.Errorf("completion refresh before ownership release: %+v %v", inputs, err)
			}
			completionSyncer.RequestPullRequestRefresh(ctx, id)
		})
	}
	body, err := json.Marshal(map[string]any{"mode": "auto", "mechanical_only": mechanicalOnly})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/"+pullRequest.ID+"/rebase", strings.NewReader(string(body))))
	if queued {
		var admitted pullrequestwork.Work
		if err := json.Unmarshal(response.Body.Bytes(), &admitted); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusCreated || admitted.Status != pullrequestwork.StatusQueued || admitted.HeadCommit != "" || admitted.TargetBaseCommit != "" || admitted.SessionID != "" {
			t.Fatalf("queued response=%+v status=%d", admitted, response.Code)
		}
		// Both refs move after admission; conflicting edits are introduced only now.
		gitPlumbingCommand(t, source, "switch", "feature")
		gitPlumbingWrite(t, source, "later.txt", "later Address change\n")
		if conflicts {
			gitPlumbingWrite(t, source, "README.md", "later feature version\n")
		}
		gitPlumbingCommand(t, source, "add", ".")
		gitPlumbingCommand(t, source, "commit", "-m", "Advance feature while rebase waits")
		gitPlumbingCommand(t, source, "push", "origin", "feature")
		originalHead = gitPlumbingOutput(t, source, "rev-parse", "HEAD")
		gitPlumbingCommand(t, source, "switch", "main")
		gitPlumbingWrite(t, source, "later-base.txt", "later base change\n")
		if conflicts {
			gitPlumbingWrite(t, source, "README.md", "later base version\n")
		}
		gitPlumbingCommand(t, source, "add", ".")
		gitPlumbingCommand(t, source, "commit", "-m", "Advance base while rebase waits")
		gitPlumbingCommand(t, source, "push", "origin", "main")
		baseHead = gitPlumbingOutput(t, source, "rev-parse", "HEAD")
		if err := workService.Fail(ctx, "blocker", "Address stopped after branch advanced"); err != nil {
			t.Fatal(err)
		}
	}
	if err := workService.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	work, err := workService.List(ctx, pullRequest.ID, pullrequestwork.KindRebase)
	if err != nil {
		t.Fatal(err)
	}
	if conflicts {
		wantError := pullrequestwork.ErrRebaseConflicts.Error()
		if !mechanicalOnly {
			wantError = "conflict agent unavailable"
		}
		if response.Code != http.StatusCreated || len(work) != 1 || work[0].Status != pullrequestwork.StatusFailed || work[0].Error != wantError || work[0].MechanicalOnly != mechanicalOnly {
			t.Fatalf("status=%d body=%s work=%+v", response.Code, response.Body.String(), work)
		}
		if !mechanicalOnly && (len(launcher.works) != 1 || launcher.works[0].HeadCommit != originalHead || launcher.works[0].TargetBaseCommit != baseHead) {
			t.Fatalf("automatic fallback=%+v", launcher.works)
		}
		if mechanicalOnly && len(launcher.works) != 0 {
			t.Fatalf("mechanical rebase launched agent: %+v", launcher.works)
		}
		persisted, ok := pullRequests.GetPullRequest(pullRequest.ID)
		remoteHead := gitPlumbingOutput(t, fixtureRoot, "--git-dir", headRemote, "rev-parse", "refs/heads/feature")
		if !ok || persisted.HeadCommit != originalHead || remoteHead != originalHead {
			t.Fatalf("conflict changed PR: %+v remote=%s", persisted, remoteHead)
		}
		return
	}
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(work) != 1 || work[0].Status != pullrequestwork.StatusCompleted || work[0].HeadCommit != originalHead || work[0].ResultHeadCommit == "" || work[0].ResultHeadCommit == originalHead {
		t.Fatalf("work=%+v", work)
	}
	rebasedHead := work[0].ResultHeadCommit
	if remoteHead := gitPlumbingOutput(t, fixtureRoot, "--git-dir", headRemote, "rev-parse", "refs/heads/feature"); remoteHead != rebasedHead {
		t.Fatalf("remote head=%q want=%q", remoteHead, rebasedHead)
	}
	persisted, ok = pullRequests.GetPullRequest(pullRequest.ID)
	if !ok || persisted.HeadCommit != originalHead || persisted.BaseCommit != baseHead || persisted.HasCurrentComparison() {
		t.Fatalf("persisted=%+v ok=%v", persisted, ok)
	}
	if _, err = repositories.Resolve(ctx, rebasedHead); !errors.Is(err, repository.ErrRefNotFound) {
		t.Fatalf("rebased commit unexpectedly visible before readiness refresh: %v", err)
	}

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("completion did not schedule targeted preparation")
	}
	assertDetails(originalHead, persisted.DiffBaseCommit)
	expectedHead := rebasedHead
	if refreshScenario == "external" {
		gitPlumbingCommand(t, source, "fetch", headRemote, "feature")
		gitPlumbingCommand(t, source, "switch", "-C", "external", "FETCH_HEAD")
		gitPlumbingWrite(t, source, "external.txt", "subsequent writer\n")
		gitPlumbingCommand(t, source, "add", ".")
		gitPlumbingCommand(t, source, "commit", "-m", "external push after rebase")
		expectedHead = gitPlumbingOutput(t, source, "rev-parse", "HEAD")
		gitPlumbingCommand(t, source, "push", headRemote, "HEAD:feature")
		completionSyncer.RequestPullRequestRefresh(ctx, pullRequest.ID)
	}
	if refreshScenario == "failure" {
		if err := os.Rename(headRemote, headRemote+".offline"); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	github := &gitPlumbingGitHub{readiness: pullrequestmerge.GitHubReadiness{
		HeadCommit: expectedHead, ChecksState: pullrequestmerge.ChecksPassing,
		MergeabilityState: pullrequestmerge.MergeabilityMergeable, SyncedAt: now,
	}}
	merger := pullrequestmerge.New(pullrequestmerge.Options{
		Sync: syncer, RepositoryURL: remote, Store: pullRequests,
		Repository: localPullRequestMergeRepository{Service: repositories}, GitHub: github,
		Clock: pullrequestmerge.ClockFunc(func() time.Time { return now }),
	})
	if projected := merger.Project(ctx, persisted); projected.MergeBlockedReason != "comparison_stale" {
		t.Fatalf("unprepared cached projection=%+v", projected)
	}
	releaseRefresh()
	if refreshScenario == "failure" {
		select {
		case err := <-cached:
			if err == nil {
				t.Fatal("expected preparation failure")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("preparation did not finish")
		}
		failed, _ := pullRequests.GetPullRequest(pullRequest.ID)
		if failed.HasCurrentComparison() || failed.HeadCommit != originalHead {
			t.Fatalf("failed refresh replaced comparison: %+v", failed)
		}
		assertDetails(originalHead, persisted.DiffBaseCommit)
		stored, err := workStore.Get(ctx, work[0].ID)
		if err != nil || stored.Status != pullrequestwork.StatusCompleted || stored.ResultHeadCommit != rebasedHead {
			t.Fatalf("publication changed: %+v %v", stored, err)
		}
		if err := os.Rename(headRemote+".offline", headRemote); err != nil {
			t.Fatal(err)
		}
	}
	// A synchronous request joins behind completion and any external-push follow-up.
	if err := syncer.SyncPullRequest(ctx, pullRequest.ID); err != nil {
		t.Fatal(err)
	}
	persisted, _ = pullRequests.GetPullRequest(pullRequest.ID)
	if !persisted.HasCurrentComparison() || persisted.HeadCommit != expectedHead || persisted.DiffBaseCommit != baseHead {
		t.Fatalf("prepared comparison=%+v", persisted)
	}
	assertDetails(expectedHead, baseHead)
	if remoteHead := gitPlumbingOutput(t, headRemote, "rev-parse", "refs/heads/feature"); remoteHead != expectedHead {
		t.Fatalf("refresh changed publication: %s", remoteHead)
	}
	projected := merger.Project(ctx, persisted)
	if projected.Mergeable == nil || !*projected.Mergeable || projected.MergeBlockedReason != "" {
		t.Fatalf("projected=%+v", projected)
	}
	if resolved, resolveErr := repositories.Resolve(ctx, rebasedHead); resolveErr != nil || resolved != rebasedHead {
		t.Fatalf("rebased commit was not recovered by readiness: resolved=%q err=%v", resolved, resolveErr)
	}
	// Repeating a successful rebase is a durable no-op, with no agent launch.
	noop, err := workService.Start(ctx, pullrequestwork.Start{PullRequestID: pullRequest.ID, Kind: pullrequestwork.KindRebase})
	if err != nil || len(noop) != 1 {
		t.Fatalf("admission=%+v error=%v", noop, err)
	}
	if err := workService.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	noop[0], err = workStore.Get(ctx, noop[0].ID)
	if err != nil || len(noop) != 1 || noop[0].Status != pullrequestwork.StatusCompleted || noop[0].HeadCommit != expectedHead || noop[0].ResultHeadCommit != expectedHead || len(launcher.works) != 0 {
		t.Fatalf("no-op=%+v launches=%+v err=%v", noop, launcher.works, err)
	}

	result, err := merger.Merge(ctx, pullRequest.ID, pullrequestmerge.Request{Strategy: "squash"})
	if err != nil {
		t.Fatal(err)
	}
	if result.PullRequest.Status != pullrequestlifecycle.StatusMerged || github.request.ExpectedHeadSHA != expectedHead {
		t.Fatalf("result=%+v expected head=%q", result, github.request.ExpectedHeadSHA)
	}
}

func gitPlumbingCommand(t *testing.T, dir string, arguments ...string) {
	t.Helper()
	_ = gitPlumbingOutput(t, dir, arguments...)
}

func gitPlumbingOutput(t *testing.T, dir string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func gitPlumbingWrite(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

type gitPlumbingObservations struct {
	pullrequestlifecycle.GitHubTransport
	repositories *repository.Service
	catalog      *pullrequestssqlite.Store
}

func (g gitPlumbingObservations) Get(ctx context.Context, target pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubPullRequest, error) {
	p := g.catalog.ListPullRequests(g.repositories.Descriptor().ID)[0]
	source := pullrequestlifecycle.DecodeGitHubObservation(p.SyncData)
	base, err := g.repositories.RemoteBranchHead(ctx, source.BaseRepositoryURL, p.BaseBranch)
	if err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, err
	}
	head, err := g.repositories.RemoteBranchHead(ctx, source.HeadRepositoryURL, p.HeadBranch)
	if err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, err
	}
	return pullrequestlifecycle.GitHubPullRequest{ExternalID: target.ExternalID, Status: p.Status, Title: p.Title, BaseBranch: p.BaseBranch, HeadBranch: p.HeadBranch, BaseCommit: base, HeadCommit: head, SyncData: p.SyncData}, nil
}
func (g gitPlumbingObservations) RefreshReadiness(ctx context.Context, target pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubReadiness, error) {
	p, err := g.Get(ctx, target)
	return pullrequestlifecycle.GitHubReadiness{HeadCommit: p.HeadCommit, ChecksState: pullrequestlifecycle.GitHubChecksPassing, MergeabilityState: pullrequestlifecycle.GitHubMergeabilityMergeable}, err
}

var errCacheReaderLaunch = errors.New("cache regression stops before agent launch")

type cachedReaderLauncher struct{ inputs []pullrequestwork.PullRequest }

func (l *cachedReaderLauncher) Start(_ context.Context, p pullrequestwork.PullRequest, _ pullrequestwork.Work, _ string) (string, error) {
	l.inputs = append(l.inputs, p)
	return "", errCacheReaderLaunch
}

type heldCompletionRepository struct {
	localPullRequestRepository
	beforeCache func()
	afterCache  func(error)
}

func (r heldCompletionRepository) CachePublishedBranch(ctx context.Context, source, ref string, observed repository.BranchObservation) error {
	r.beforeCache()
	err := r.localPullRequestRepository.CachePublishedBranch(ctx, source, ref, observed)
	r.afterCache(err)
	return err
}

// Controlled GitHub evidence over the fixture's real Git remotes.
type previewGitPlumbingObservations struct {
	gitPlumbingObservations
	gets, focused int
}

func (g *previewGitPlumbingObservations) Get(ctx context.Context, target pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubPullRequest, error) {
	g.gets++
	return g.gitPlumbingObservations.Get(ctx, target)
}
func (g *previewGitPlumbingObservations) GetWorkState(ctx context.Context, target pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubPullRequest, error) {
	g.focused++
	return g.gitPlumbingObservations.Get(ctx, target)
}

func (l *gitPlumbingLauncher) Reserve(context.Context, pullrequestwork.PullRequest, pullrequestwork.Work) error {
	return nil
}
func (l *gitPlumbingLauncher) StartReserved(ctx context.Context, pr pullrequestwork.PullRequest, w pullrequestwork.Work, prompt string) (error, error) {
	_, err := l.Start(ctx, pr, w, prompt)
	return err, nil
}
func (l *gitPlumbingLauncher) SettleReserved(context.Context, pullrequestwork.Work) error { return nil }

func (l *cachedReaderLauncher) Reserve(context.Context, pullrequestwork.PullRequest, pullrequestwork.Work) error {
	return nil
}
func (l *cachedReaderLauncher) StartReserved(ctx context.Context, pr pullrequestwork.PullRequest, w pullrequestwork.Work, prompt string) (error, error) {
	_, err := l.Start(ctx, pr, w, prompt)
	return err, nil
}
func (l *cachedReaderLauncher) SettleReserved(context.Context, pullrequestwork.Work) error {
	return nil
}
