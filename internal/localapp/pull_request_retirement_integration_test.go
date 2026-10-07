package localapp

import (
	"context"
	"errors"
	"github.com/holark-ai/holark/internal/testfixture/branchfixture"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/holons"
	holonssqlite "github.com/holark-ai/holark/internal/holons/sqliteadapter"
	"github.com/holark-ai/holark/internal/ide"
	idesqlite "github.com/holark-ai/holark/internal/ide/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	commentssqlite "github.com/holark-ai/holark/internal/pullrequestcomments/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prhttp "github.com/holark-ai/holark/internal/pullrequestlifecycle/httpapi"
	prsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestmerge"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	worksqlite "github.com/holark-ai/holark/internal/pullrequestwork/sqliteadapter"
	"github.com/holark-ai/holark/internal/sessionterminals"
	"github.com/holark-ai/holark/internal/terminals"
)

func TestMergedPullRequestRetirementRetriesEverySessionAndPreservesHistory(t *testing.T) {
	for _, test := range []struct {
		name    string
		syncErr error
	}{
		{name: "successful sync"},
		{name: "sync fails after saving merged status", syncErr: errors.New("participant sync failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			testMergedPullRequestRetirement(t, test.syncErr)
		})
	}
}

func testMergedPullRequestRetirement(t *testing.T, syncErr error) {
	t.Helper()
	ctx := t.Context()
	db, err := database.Open(filepath.Join(t.TempDir(), "retirement.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.ExecContext(ctx, "create table repositories(id text primary key); insert into repositories(id) values('repo')"); err != nil {
		t.Fatal(err)
	}
	prs, err := prsqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	hs, err := holonssqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := worksqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	pr, err := prs.CreatePullRequest(pullrequestlifecycle.PullRequest{ID: "pr", RepositoryID: "repo", Status: pullrequestlifecycle.StatusOpen, LinkedHolonIDs: []string{"original", "review", "review", "completed"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	terminalID, err := terminals.NewID()
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "history.txt")
	if err = os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id   string
		kind holons.Kind
	}{
		{"original", holons.KindNormal}, {"metadata", holons.KindPRMetadata},
		{"review", holons.KindPullReview}, {"worker", holons.KindPullWorker},
		{"continue", holons.KindPullWorker}, {"rebase", holons.KindRebase},
		{"completed", holons.KindNormal}, {"completed-with-ide", holons.KindNormal}, {"unrelated", holons.KindNormal},
	} {
		h := holons.Holon{ID: item.id, Kind: item.kind, Status: holons.StatusRunning, PullRequestID: pr.ID, CreatedAt: now, WorktreePath: workspace, WorktreeBranch: "holark/" + item.id,
			AgentSessions: []holons.AgentSession{{ID: "agent-" + item.id, HolonID: item.id, AgentType: "codex", Status: string(holons.StatusRunning), CreatedAt: now, UpdatedAt: now, ResumeTarget: "saved-conversation"}},
		}
		if item.id == "original" || item.id == "completed" || item.id == "unrelated" {
			h.PullRequestID = ""
		}
		if item.id == "completed" || item.id == "completed-with-ide" {
			h.Status = holons.StatusCompleted
			h.AgentSessions[0].Status = string(holons.StatusCompleted)
		}
		if item.id == "review" {
			h.AgentSessions[0].TerminalID = string(terminalID)
		}
		if err = hs.Create(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	works := []pullrequestwork.Work{
		{ID: "review", SessionID: "review", Kind: pullrequestwork.KindReview, Status: pullrequestwork.StatusRunning},
		{ID: "worker", SessionID: "worker", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAssisted, Status: pullrequestwork.StatusWaiting},
		{ID: "continue", SessionID: "continue", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeContinue, Status: pullrequestwork.StatusRunning, PublicationState: "published", ResultHeadCommit: "already-published"},
		{ID: "rebase", SessionID: "rebase", Kind: pullrequestwork.KindRebase, Status: pullrequestwork.StatusRunning},
		{ID: "queued-worker", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusQueued},
		{ID: "queued-rebase", Kind: pullrequestwork.KindRebase, Status: pullrequestwork.StatusQueued},
	}
	for _, w := range works {
		w.PullRequestID = pr.ID
		if err = ws.Create(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	service := holons.NewServiceWithRepository(hs, &workLauncherRepository{})
	gateway := &launchGateway{closeErr: map[terminals.TerminalID]error{terminalID: errors.New("shutdown unavailable")}}
	products := &localTerminalProducts{holons: service}
	terminalCoordinator, err := sessionterminals.New(gateway, products)
	if err != nil {
		t.Fatal(err)
	}
	editors, err := idesqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = editors.Create(ctx, ide.IDE{ID: "editor", HolonID: "completed-with-ide", Provider: "vscode", State: ide.Ready, DesiredOpen: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	ideCloser := &cancellationIDECloser{store: editors, closeErr: errors.New("IDE shutdown unavailable")}
	runtime := &terminalHolonService{Service: service, terminals: terminalCoordinator, ides: ideCloser}
	// No launcher: dispatching any queued item during cleanup would fail this test.
	work := pullrequestwork.New(ws, localWorkCatalog{store: prs}, nil, nil)
	runtime.work = work
	work.SetWorkerRuntime(localWorkRuntime{holons: runtime})
	retirement := &localPullRequestRetirement{catalog: prs, holons: service, links: localPullRequestHolonLinks{catalog: prs, holons: service}, work: work,
		metadata: noOpRetirement{},
	}
	scheduler := pullrequestlifecycle.NewRefreshCoordinator()
	t.Cleanup(scheduler.Close)
	lifecycle := pullrequestlifecycle.New(prs, pullrequestlifecycle.Options{Refresh: scheduler, Retirement: retirement})
	mux := http.NewServeMux()
	boundary := &mergedRetirementBoundary{pr: pr, store: prs, syncErr: syncErr}
	prhttp.RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, prhttp.Options{RepositoryID: "repo", Catalog: prs, Lifecycle: boundary, Merge: boundary, Retirement: retirement, RetirementScheduler: lifecycle})
	wantSyncStatus := http.StatusOK
	if syncErr != nil {
		wantSyncStatus = http.StatusInternalServerError
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/sync", nil))
	if response.Code != wantSyncStatus {
		t.Fatalf("sync response = %d: %s", response.Code, response.Body.String())
	}
	if stored, ok := prs.GetPullRequest(pr.ID); !ok || stored.Status != pullrequestlifecycle.StatusMerged {
		t.Fatalf("merged status was not saved: %+v", stored)
	}
	for id, wantStatus := range map[string]holons.Status{
		"original": holons.StatusCancelled, "metadata": holons.StatusCancelled,
		"worker": holons.StatusCancelled, "continue": holons.StatusCancelled,
		"rebase": holons.StatusCancelled, "completed": holons.StatusCompleted,
	} {
		h, err := service.Get(ctx, id)
		if err != nil || h.Status != wantStatus || h.ArchivedAt == nil || h.WorktreePath != workspace || h.AgentSessions[0].ResumeTarget != "saved-conversation" {
			t.Fatalf("session %s: %+v, %v", id, h, err)
		}
	}
	completed, err := service.Get(ctx, "completed")
	if err != nil || completed.AgentSessions[0].Status != string(holons.StatusCompleted) || completed.ArchivedAt == nil {
		t.Fatalf("completed agent history = %+v, %v", completed, err)
	}
	pending, err := service.Get(ctx, "completed-with-ide")
	if err != nil || pending.Status != holons.StatusCompleted || pending.ArchivedAt != nil || pending.AgentSessions[0].Status != string(holons.StatusCompleted) {
		t.Fatalf("unfinished IDE cleanup = %+v, %v", pending, err)
	}
	for _, original := range works {
		w, err := ws.Get(ctx, original.ID)
		want := pullrequestwork.StatusCancelled
		if w.ID == "review" {
			want = pullrequestwork.StatusCancelling
		}
		if err != nil || w.Status != want || (want == pullrequestwork.StatusCancelled && w.CompletedAt == nil) {
			t.Fatalf("work = %+v, %v", w, err)
		}
	}
	if len(gateway.closed) != 1 {
		t.Fatalf("duplicate shutdowns: %v", gateway.closed)
	}
	// A committed merge response remains successful even if cleanup still fails.
	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/pr/merge", strings.NewReader(`{"strategy":"squash"}`))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("merge response = %d: %s", response.Code, response.Body.String())
	}
	// Wait for the scheduled failing pass before changing the service boundary.
	if err := scheduler.Do(ctx, pullrequestlifecycle.RefreshKey{RepositoryID: "repo", Section: "retirement"}, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	delete(gateway.closeErr, terminalID)
	ideCloser.closeErr = nil
	// Repository sync retries cleanup even when the same participant sync error persists.
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/sync", nil))
	if response.Code != wantSyncStatus {
		t.Fatalf("sync response = %d: %s", response.Code, response.Body.String())
	}
	if len(gateway.closed) != 3 {
		t.Fatalf("sync did not retry shutdown: %v", gateway.closed)
	}
	w, _ := ws.Get(ctx, "review")
	if w.Status != pullrequestwork.StatusCancelling {
		t.Fatalf("settled before process exit: %+v", w)
	}
	if err := products.ApplyTerminalCompletion(localTerminalHost, terminals.ProcessCompletion{TerminalID: terminalID, ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	if err := work.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	settled, _ := ws.Get(ctx, "review")
	if settled.Status != pullrequestwork.StatusCancelled || settled.CompletedAt == nil {
		t.Fatalf("unsettled review: %+v", settled)
	}
	if err := retirement.RetireInactive(ctx, pr.ID); err != nil {
		t.Fatal(err)
	}
	repeated, _ := ws.Get(ctx, "review")
	if !repeated.CompletedAt.Equal(*settled.CompletedAt) {
		t.Fatal("repeated cleanup changed completion history")
	}
	for id, want := range map[string]holons.Status{"unrelated": holons.StatusRunning, "completed": holons.StatusCompleted, "completed-with-ide": holons.StatusCompleted} {
		h, err := service.Get(ctx, id)
		if err != nil || h.Status != want || (id != "unrelated" && h.ArchivedAt == nil) {
			t.Fatalf("%s: %+v, %v", id, h, err)
		}
	}
	repeatedHolon, err := service.Get(ctx, "completed")
	if err != nil || repeatedHolon.ArchivedAt == nil || !repeatedHolon.ArchivedAt.Equal(*completed.ArchivedAt) {
		t.Fatalf("repeated cleanup changed holon completion history: %+v, %v", repeatedHolon, err)
	}
	if contents, err := os.ReadFile(marker); err != nil || string(contents) != "keep" {
		t.Fatalf("workspace removed: %q, %v", contents, err)
	}
}

type noOpRetirement struct{}

func (noOpRetirement) RetireInactive(context.Context, string) error { return nil }

// Sync persists the merged status before a possible participant sync failure;
// exercise the HTTP cleanup hook against real local work and session services.
type mergedRetirementBoundary struct {
	prhttp.Lifecycle
	pr      pullrequestlifecycle.PullRequest
	store   *prsqlite.Store
	syncErr error
}

func (b *mergedRetirementBoundary) Sync(context.Context, string) (pullrequestlifecycle.SyncResult, error) {
	now := time.Now().UTC()
	var err error
	b.pr, err = b.store.TransitionSyncedPullRequestStatus(b.pr.ID, pullrequestlifecycle.StatusMerged, nil, now, &now, now)
	if err != nil {
		return pullrequestlifecycle.SyncResult{}, err
	}
	if b.syncErr != nil {
		return pullrequestlifecycle.SyncResult{}, b.syncErr
	}
	return pullrequestlifecycle.SyncResult{PullRequests: []pullrequestlifecycle.PullRequest{b.pr}}, nil
}
func (b mergedRetirementBoundary) Merge(context.Context, string, pullrequestmerge.Request) (pullrequestmerge.Result, error) {
	return pullrequestmerge.Result{PullRequest: b.pr}, nil
}
func (b mergedRetirementBoundary) Project(_ context.Context, pr pullrequestlifecycle.PullRequest) pullrequestlifecycle.PullRequest {
	return pr
}

func TestMergedPullRequestCleanupRetriesCompletedWorkerReply(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(filepath.Join(t.TempDir(), "reply-retry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.ExecContext(ctx, "create table repositories(id text primary key); insert into repositories(id) values('repo')"); err != nil {
		t.Fatal(err)
	}
	prs, err := prsqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = branchfixture.Create(ctx, prs, pullrequestlifecycle.PullRequest{ID: "pr", RepositoryID: "repo", Status: pullrequestlifecycle.StatusMerged, HeadCommit: "head"}); err != nil {
		t.Fatal(err)
	}
	ws, err := worksqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := commentssqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	comments := pullrequestcomments.NewService(cs, localPullRequestTargets{catalog: prs})
	parent, err := comments.Create(ctx, pullrequestcomments.CreateComment{PullRequestID: "pr", Body: "Please fix this."})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := pullrequestwork.Work{ID: "worker", PullRequestID: "pr", CommentID: parent.ID, Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusRunning, HeadCommit: "head", PublicationState: "published", ResultHeadCommit: "published-head", ReplyBody: "Fixed."}
	if err = ws.Create(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	work := pullrequestwork.New(ws, localWorkCatalog{store: prs}, nil, localReviewComments{comments: comments})
	work.SetCompletionCommitter(worksqlite.NewCompletionCommitter(ws, prs))
	if _, err = db.ExecContext(ctx, `CREATE TRIGGER reject_reply BEFORE INSERT ON pull_request_comments BEGIN SELECT RAISE(ABORT, 'reply save failed'); END`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = work.CancelPullRequest(ctx, "pr"); err == nil || !strings.Contains(err.Error(), "reply save failed") {
			t.Fatalf("reply delivery failure = %v", err)
		}
		got, err := ws.Get(ctx, checkpoint.ID)
		if err != nil || got.Status != pullrequestwork.StatusCompleted || got.ReplyBody != checkpoint.ReplyBody {
			t.Fatalf("completed checkpoint = %+v, %v", got, err)
		}
	}
	if _, err = db.ExecContext(ctx, "DROP TRIGGER reject_reply"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = work.CancelPullRequest(ctx, "pr"); err != nil {
			t.Fatal(err)
		}
		replies, err := cs.ListByPullRequest(ctx, "pr")
		if err != nil || len(replies) != 2 {
			t.Fatalf("comments after retry = %+v, %v", replies, err)
		}
		reply, err := cs.FindBySourceWorkerID(ctx, checkpoint.ID)
		if err != nil || reply.ParentCommentID != parent.ID || reply.Body != checkpoint.ReplyBody || reply.OriginalHeadCommit != checkpoint.ResultHeadCommit {
			t.Fatalf("delivered reply = %+v, %v", reply, err)
		}
	}
}

type retirementActivityFunc func(context.Context, string) ([]holons.Holon, error)

func (f retirementActivityFunc) PullRequestActivity(ctx context.Context, id string) ([]holons.Holon, error) {
	return f(ctx, id)
}

func TestRetirementSerializesCallersAndRejectsReopenedLifecycle(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(filepath.Join(t.TempDir(), "reopen.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.ExecContext(ctx, "create table repositories(id text primary key); insert into repositories(id) values('repo')"); err != nil {
		t.Fatal(err)
	}
	prs, err := prsqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := worksqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	p, err := prs.CreatePullRequest(pullrequestlifecycle.PullRequest{ID: "pr", RepositoryID: "repo", Status: pullrequestlifecycle.StatusClosed})
	if err != nil {
		t.Fatal(err)
	}
	if err = ws.Create(ctx, pullrequestwork.Work{ID: "queued", PullRequestID: p.ID, Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusQueued}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 2), make(chan struct{})
	defer close(release)
	r := &localPullRequestRetirement{catalog: prs, metadata: noOpRetirement{}, work: pullrequestwork.New(ws, localWorkCatalog{store: prs}, nil, nil), links: localPullRequestHolonLinks{catalog: prs, holons: retirementActivityFunc(func(ctx context.Context, id string) ([]holons.Holon, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, nil
	})}}
	done := make(chan error, 2)
	go func() { done <- r.RetireInactive(ctx, p.ID) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}
	go func() { done <- r.RetireInactive(ctx, p.ID) }()
	select {
	case <-entered:
		t.Fatal("HTTP/background cleanup overlapped")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := prs.TransitionSyncedPullRequestStatus(p.ID, pullrequestlifecycle.StatusOpen, nil, time.Now(), nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("cleanup did not settle")
		}
	}
	w, err := ws.Get(ctx, "queued")
	if err != nil || w.Status != pullrequestwork.StatusQueued {
		t.Fatalf("reopened work was retired: %+v, %v", w, err)
	}
}

func TestRetirementWaitsForHolonArchivalWithoutActiveWork(t *testing.T) {
	for _, kind := range []holons.Kind{holons.KindNormal, holons.KindPRMetadata} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := t.Context()
			databasePath := filepath.Join(t.TempDir(), "retirement.sqlite")
			db, err := database.Open(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			if _, err = db.ExecContext(ctx, "create table repositories(id text primary key); insert into repositories(id) values('repo')"); err != nil {
				t.Fatal(err)
			}
			prs, err := prsqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			hs, err := holonssqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			ws, err := worksqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			pr := pullrequestlifecycle.PullRequest{ID: "pr", RepositoryID: "repo", Status: pullrequestlifecycle.StatusMerged}
			now := time.Now().UTC()
			terminalID, err := terminals.NewID()
			if err != nil {
				t.Fatal(err)
			}
			h := holons.Holon{ID: "holon", Kind: kind, Status: holons.StatusRunning, CreatedAt: now,
				BaseBranch: "main", BaseCommit: "base", WorktreePath: t.TempDir(), WorktreeBranch: "holark/holon",
				AgentSessions: []holons.AgentSession{{ID: "agent", HolonID: "holon", AgentType: "codex", Status: string(holons.StatusRunning), TerminalID: string(terminalID), CreatedAt: now, UpdatedAt: now}},
			}
			if kind == holons.KindNormal {
				pr.LinkedHolonIDs = []string{h.ID}
			} else {
				h.PullRequestID = pr.ID
			}
			if _, err = prs.CreatePullRequest(pr); err != nil {
				t.Fatal(err)
			}
			if err = hs.Create(ctx, h); err != nil {
				t.Fatal(err)
			}
			archiveErr := errors.New("archival unavailable")
			repository := &retryCloseRepository{failure: archiveErr}
			service := holons.NewServiceWithRepository(hs, repository)
			products := &localTerminalProducts{holons: service}
			coordinator, err := sessionterminals.New(&launchGateway{}, products)
			if err != nil {
				t.Fatal(err)
			}
			work := pullrequestwork.New(ws, localWorkCatalog{store: prs}, nil, nil)
			work.SetWorkerRuntime(localWorkRuntime{holons: &terminalHolonService{Service: service, terminals: coordinator}})
			retirement := &localPullRequestRetirement{catalog: prs, holons: service,
				links: localPullRequestHolonLinks{catalog: prs, holons: service}, metadata: noOpRetirement{}, work: work,
			}
			if err = retirement.RetireInactive(ctx, pr.ID); err != nil {
				t.Fatal(err)
			}
			stored, _ := prs.GetPullRequest(pr.ID)
			if stored.ActivityRetired {
				t.Fatal("cleanup marked complete before process exit")
			}
			if err = products.ApplyTerminalCompletion(localTerminalHost, terminals.ProcessCompletion{TerminalID: terminalID, ExitCode: 0}); !errors.Is(err, archiveErr) {
				t.Fatalf("completion error = %v", err)
			}
			stopped, err := service.Get(ctx, h.ID)
			if err != nil || !holons.IsTerminal(stopped.Status) || stopped.ArchivedAt != nil || stopped.AgentSessions[0].TerminalID != "" {
				t.Fatalf("unarchived stopped holon = %+v, %v", stopped, err)
			}
			if err = retirement.RetireInactive(ctx, pr.ID); !errors.Is(err, archiveErr) {
				t.Fatalf("archival retry error = %v", err)
			}
			stored, _ = prs.GetPullRequest(pr.ID)
			if stored.ActivityRetired {
				t.Fatal("cleanup marked complete after archival failed")
			}
			repository.failure = nil
			if err = retirement.RetireInactive(ctx, pr.ID); err != nil {
				t.Fatal(err)
			}
			archived, err := service.Get(ctx, h.ID)
			stored, _ = prs.GetPullRequest(pr.ID)
			if err != nil || archived.ArchivedAt == nil || !stored.ActivityRetired {
				t.Fatalf("cleanup did not finish: holon=%+v pr=%+v err=%v", archived, stored, err)
			}
			if _, err = service.Reopen(ctx, h.ID); err != nil {
				t.Fatal(err)
			}
			for _, restart := range []bool{false, true} {
				if restart {
					// Reopen SQLite and rebuild every service involved in cleanup.
					if err = db.Close(); err != nil {
						t.Fatal(err)
					}
					db, err = database.Open(databasePath)
					if err != nil {
						t.Fatal(err)
					}
					prs, err = prsqlite.New(ctx, db)
					if err != nil {
						t.Fatal(err)
					}
					hs, err = holonssqlite.New(ctx, db)
					if err != nil {
						t.Fatal(err)
					}
					ws, err = worksqlite.New(ctx, db)
					if err != nil {
						t.Fatal(err)
					}
					service = holons.NewServiceWithRepository(hs, repository)
					coordinator, err = sessionterminals.New(&launchGateway{}, &localTerminalProducts{holons: service})
					if err != nil {
						t.Fatal(err)
					}
					work = pullrequestwork.New(ws, localWorkCatalog{store: prs}, nil, nil)
					work.SetWorkerRuntime(localWorkRuntime{holons: &terminalHolonService{Service: service, terminals: coordinator}})
					retirement = &localPullRequestRetirement{catalog: prs, holons: service,
						links: localPullRequestHolonLinks{catalog: prs, holons: service}, metadata: noOpRetirement{}, work: work,
					}
				}
				stored, _ = prs.GetPullRequest(pr.ID)
				if !stored.ActivityRetired {
					t.Fatalf("cleanup completion lost after restart=%v", restart)
				}
				scheduler := pullrequestlifecycle.NewRefreshCoordinator()
				t.Cleanup(scheduler.Close)
				lifecycle := pullrequestlifecycle.New(prs, pullrequestlifecycle.Options{Refresh: scheduler, Retirement: retirement})
				mux := http.NewServeMux()
				boundary := &mergedRetirementBoundary{pr: pr, store: prs}
				prhttp.RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, prhttp.Options{RepositoryID: "repo", Catalog: prs, Lifecycle: boundary, Retirement: retirement, RetirementScheduler: lifecycle})
				for range 2 {
					response := httptest.NewRecorder()
					mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/sync", nil))
					if response.Code != http.StatusOK {
						t.Fatalf("sync response = %d: %s", response.Code, response.Body.String())
					}
					// Exercise background cleanup as well as the synchronous HTTP hook.
					lifecycle.RequestRetirement(ctx, pr.ID)
					if err = scheduler.Do(ctx, pullrequestlifecycle.RefreshKey{RepositoryID: "repo", Section: "retirement"}, func(context.Context) error { return nil }); err != nil {
						t.Fatal(err)
					}
					reopened, err := service.Get(ctx, h.ID)
					if err != nil || reopened.ArchivedAt != nil || holons.IsTerminal(reopened.Status) || reopened.EndRequested {
						t.Fatalf("sync retired reopened holon after restart=%v: %+v, %v", restart, reopened, err)
					}
				}
				scheduler.Close()
			}
		})
	}
}
