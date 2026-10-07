package pullrequestwork_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	workhttp "github.com/holark-ai/holark/internal/pullrequestwork/httpapi"
	"github.com/holark-ai/holark/internal/pullrequestwork/sqliteadapter"
)

type queueRebaseFunc func(context.Context, pullrequestwork.PullRequest, string) (pullrequestwork.RebasePreparation, error)

func (f queueRebaseFunc) Rebase(ctx context.Context, pr pullrequestwork.PullRequest, expectedBaseCommit, operationID string) (pullrequestwork.RebasePreparation, error) {
	return f(ctx, pr, expectedBaseCommit)
}

type queueLauncher struct {
	reserved     []pullrequestwork.Work
	works        []pullrequestwork.Work
	preflightErr error
	launchErr    error
}

func (l *queueLauncher) Preflight(context.Context, pullrequestwork.Kind) error { return l.preflightErr }
func (l *queueLauncher) Start(_ context.Context, _ pullrequestwork.PullRequest, w pullrequestwork.Work, _ string) (string, error) {
	l.works = append(l.works, w)
	if w.Kind == pullrequestwork.KindRebase && l.launchErr != nil {
		return "", l.launchErr
	}
	return "session-" + w.ID, nil
}

type queueRuntime struct {
	state                pullrequestwork.Status
	cancelled, recovered []string
}

func (r *queueRuntime) Cancel(_ context.Context, id string) error {
	r.cancelled = append(r.cancelled, id)
	return nil
}
func (r *queueRuntime) Recover(_ context.Context, id string) error {
	r.recovered = append(r.recovered, id)
	return nil
}
func (r *queueRuntime) State(context.Context, string) (pullrequestwork.Status, error) {
	return r.state, nil
}

func mixedQueueStore(t *testing.T) (*sql.DB, *sqliteadapter.Store) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "queue.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	return db, store
}

func startQueueWork(t *testing.T, s *pullrequestwork.Service, input pullrequestwork.Start) pullrequestwork.Work {
	t.Helper()
	input.PullRequestID = "pr"
	work, err := s.Start(t.Context(), input)
	if err != nil || len(work) != 1 {
		t.Fatalf("start %+v: work=%+v err=%v", input, work, err)
	}
	if err := s.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	all, err := s.List(t.Context(), "pr", input.Kind)
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range all {
		if current.ID == work[0].ID {
			return current
		}
	}
	t.Fatal("admitted work disappeared")
	return pullrequestwork.Work{}
}

func TestMixedQueueDispatchesRebaseUsingLatestRevision(t *testing.T) {
	_, store := mixedQueueStore(t)
	catalog := &recoveryCatalog{pr: pullrequestwork.PullRequest{ID: "pr", Active: true, BaseCommit: "base", HeadCommit: "head"}}
	launcher := &queueLauncher{}
	calls := 0
	rebaser := queueRebaseFunc(func(_ context.Context, pr pullrequestwork.PullRequest, _ string) (pullrequestwork.RebasePreparation, error) {
		calls++
		if pr.HeadCommit != "advanced-head" {
			t.Fatalf("rebase used old head: %+v", pr)
		}
		return pullrequestwork.RebasePreparation{PullRequest: pr, HeadCommit: pr.HeadCommit, TargetBaseCommit: "advanced-base", TargetDiffBaseCommit: "advanced-base", Conflicts: true}, nil
	})
	s := newTestWorkService(store, catalog, launcher, nil, rebaser)
	s.SetReviewChanges(recoveryReviewChanges{})
	first := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindWorker, CommentIDs: []string{"c1"}})
	rebase := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindRebase, Prompt: "Keep the public API"})
	last := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindWorker, CommentIDs: []string{"c2"}})
	if rebase.Status != pullrequestwork.StatusQueued || rebase.SessionID != "" || rebase.HeadCommit != "" || rebase.TargetBaseCommit != "" || rebase.StartedAt != nil || calls != 0 {
		t.Fatalf("pending rebase=%+v calls=%d", rebase, calls)
	}
	queue, err := s.Queue(t.Context(), "pr")
	if err != nil || len(queue) != 3 || queue[0].ID != first.ID || queue[1].ID != rebase.ID || queue[2].ID != last.ID {
		t.Fatalf("FIFO=%+v err=%v", queue, err)
	}
	for _, input := range []pullrequestwork.Start{{Kind: pullrequestwork.KindReview}, {Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeContinue}} {
		if w := startQueueWork(t, s, input); w.Status != pullrequestwork.StatusRunning {
			t.Fatalf("independent work=%+v", w)
		}
	}
	catalog.pr.HeadCommit = "advanced-head"
	if err := s.Fail(t.Context(), first.ID, "Address stopped after branch advanced"); err != nil {
		t.Fatal(err)
	}
	if err := s.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	running, err := store.Get(t.Context(), rebase.ID)
	if err != nil || calls != 1 || running.Status != pullrequestwork.StatusRunning || running.HeadCommit != "advanced-head" || running.TargetBaseCommit != "advanced-base" || running.Prompt != "Keep the public API" {
		t.Fatalf("dispatched=%+v err=%v", running, err)
	}
	launched := launcher.works[len(launcher.works)-1]
	if launched.ID != rebase.ID || launched.HeadCommit != running.HeadCommit || launched.TargetBaseCommit != running.TargetBaseCommit {
		t.Fatalf("launch=%+v", launched)
	}
	if err := s.Wait(t.Context(), rebase.ID, "Review conflict resolution"); err != nil {
		t.Fatal(err)
	}
	pending, _ := store.Get(t.Context(), last.ID)
	if pending.Status != pullrequestwork.StatusQueued {
		t.Fatalf("waiting rebase released slot: %+v", pending)
	}
	if _, err := s.Complete(t.Context(), rebase.ID, running.HeadCommit, pullrequestwork.Completion{ResultHeadCommit: "rebased-head"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	pending, _ = store.Get(t.Context(), last.ID)
	if pending.Status != pullrequestwork.StatusRunning || launcher.works[len(launcher.works)-1].ID != last.ID {
		t.Fatalf("next Address=%+v", pending)
	}
}

func TestRebaseExecutionOutcomesAdvanceMixedQueue(t *testing.T) {
	for _, scenario := range []struct {
		name                                string
		conflicts, mechanical               bool
		prepareErr, preflightErr, launchErr error
		want                                pullrequestwork.Status
		wantError                           string
	}{
		{name: "clean without agent", preflightErr: errors.New("agent unavailable"), want: pullrequestwork.StatusCompleted},
		{name: "mechanical conflicts", conflicts: true, mechanical: true, want: pullrequestwork.StatusFailed, wantError: pullrequestwork.ErrRebaseConflicts.Error()},
		{name: "preparation failure", prepareErr: errors.New("fetch failed"), want: pullrequestwork.StatusFailed, wantError: "fetch failed"},
		{name: "agent preflight failure", conflicts: true, preflightErr: errors.New("agent unavailable"), want: pullrequestwork.StatusFailed, wantError: "agent unavailable"},
		{name: "agent launch failure", conflicts: true, launchErr: errors.New("launch failed"), want: pullrequestwork.StatusFailed, wantError: "launch failed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			_, store := mixedQueueStore(t)
			catalog := recoveryCatalog{pr: pullrequestwork.PullRequest{ID: "pr", Active: true, HeadCommit: "head"}}
			launcher := &queueLauncher{}
			s := newTestWorkService(store, catalog, launcher, nil, queueRebaseFunc(func(_ context.Context, pr pullrequestwork.PullRequest, _ string) (pullrequestwork.RebasePreparation, error) {
				return pullrequestwork.RebasePreparation{PullRequest: pr, HeadCommit: pr.HeadCommit, TargetBaseCommit: "base", Conflicts: scenario.conflicts}, scenario.prepareErr
			}))
			first := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindWorker, CommentIDs: []string{"c1"}})
			rebase := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindRebase, MechanicalOnly: scenario.mechanical})
			last := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindWorker, CommentIDs: []string{"c2"}})
			launcher.preflightErr, launcher.launchErr = scenario.preflightErr, scenario.launchErr
			if err := s.Fail(t.Context(), first.ID, "stopped"); err != nil {
				t.Fatal(err)
			}
			if err := s.Dispatch(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, err := store.Get(t.Context(), rebase.ID)
			if err != nil || got.Status != scenario.want || got.Error != scenario.wantError || got.CompletedAt == nil || got.MechanicalOnly != scenario.mechanical {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			if (!scenario.conflicts || scenario.mechanical) && got.SessionID != "" {
				t.Fatalf("mechanical rebase reserved a holon: %+v", got)
			}
			for _, reserved := range launcher.reserved {
				if reserved.ID == rebase.ID && (!scenario.conflicts || scenario.mechanical) {
					t.Fatal("reserved before an agent was needed")
				}
			}
			if !scenario.conflicts && scenario.prepareErr == nil && (got.ResultHeadCommit != "head" || got.PublicationState != "published") {
				t.Fatalf("no-op checkpoint=%+v", got)
			}
			next, _ := store.Get(t.Context(), last.ID)
			if next.Status != pullrequestwork.StatusRunning || launcher.works[len(launcher.works)-1].ID != last.ID {
				t.Fatalf("next request was not dispatched: %+v", next)
			}
		})
	}
}

func TestRebaseCancellationKeepsSlotUntilRuntimeStops(t *testing.T) {
	_, store := mixedQueueStore(t)
	launcher := &queueLauncher{}
	s := newTestWorkService(store, recoveryCatalog{pr: pullrequestwork.PullRequest{ID: "pr", Active: true, HeadCommit: "head"}}, launcher, nil, conflictedRebaser{})
	runtime := &queueRuntime{state: pullrequestwork.StatusRunning}
	s.SetWorkerRuntime(runtime)
	first := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindWorker, CommentIDs: []string{"c1"}})
	pending := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindRebase})
	running := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindRebase})
	last := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindWorker, CommentIDs: []string{"c2"}})
	if pending.ID == running.ID {
		t.Fatal("distinct accepted rebase requests were coalesced")
	}
	cancelled, err := s.Cancel(t.Context(), "pr", pending.ID)
	if err != nil || cancelled.Status != pullrequestwork.StatusCancelled || cancelled.SessionID != "" || len(runtime.cancelled) != 0 || len(launcher.works) != 1 {
		t.Fatalf("pending cancellation=%+v err=%v", cancelled, err)
	}
	if err := s.Fail(t.Context(), first.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := s.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	cancelling, err := s.Cancel(t.Context(), "pr", running.ID)
	if err != nil || cancelling.Status != pullrequestwork.StatusCancelling || len(runtime.cancelled) != 1 || runtime.cancelled[0] != cancelling.SessionID {
		t.Fatalf("running cancellation=%+v err=%v", cancelling, err)
	}
	if got, _ := store.Get(t.Context(), last.ID); got.Status != pullrequestwork.StatusQueued {
		t.Fatalf("slot released before confirmation: %+v", got)
	}
	runtime.state = pullrequestwork.StatusCancelled
	if err := s.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get(t.Context(), running.ID); got.Status != pullrequestwork.StatusCancelled {
		t.Fatalf("cancelled=%+v", got)
	}
	if got, _ := store.Get(t.Context(), last.ID); got.Status != pullrequestwork.StatusRunning {
		t.Fatalf("next=%+v", got)
	}
}

func TestRebaseFailurePersistenceStopsDispatchUntilRecovery(t *testing.T) {
	db, store := mixedQueueStore(t)
	launcher := &queueLauncher{}
	s := newTestWorkService(store, recoveryCatalog{pr: pullrequestwork.PullRequest{ID: "pr", Active: true}}, launcher, nil, queueRebaseFunc(func(context.Context, pullrequestwork.PullRequest, string) (pullrequestwork.RebasePreparation, error) {
		return pullrequestwork.RebasePreparation{}, errors.New("fetch failed")
	}))
	first := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindWorker, CommentIDs: []string{"c1"}})
	rebase := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindRebase})
	last := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindWorker, CommentIDs: []string{"c2"}})
	if _, err := db.Exec(`CREATE TRIGGER reject_rebase_failure BEFORE UPDATE ON pull_request_work WHEN NEW.kind='rebase' AND NEW.status='failed' BEGIN SELECT RAISE(ABORT, 'write failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(t.Context(), first.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := s.Dispatch(t.Context()); err == nil {
		t.Fatal("expected persistence failure")
	}
	if got, _ := store.Get(t.Context(), rebase.ID); got.Status != pullrequestwork.StatusRunning {
		t.Fatalf("lost queue slot: %+v", got)
	}
	if got, _ := store.Get(t.Context(), last.ID); got.Status != pullrequestwork.StatusQueued {
		t.Fatalf("advanced after persistence failure: %+v", got)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_rebase_failure`); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get(t.Context(), last.ID); got.Status != pullrequestwork.StatusRunning {
		t.Fatalf("recovery did not advance: %+v", got)
	}
}

func TestMixedQueueHTTPAdmissionHistoryAndCancellation(t *testing.T) {
	_, store := mixedQueueStore(t)
	s := newTestWorkService(store, recoveryCatalog{pr: pullrequestwork.PullRequest{ID: "pr", Active: true}}, &queueLauncher{}, nil, conflictedRebaser{})
	first := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindWorker, CommentIDs: []string{"c1"}})
	mux := http.NewServeMux()
	workhttp.RegisterRoutes(func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, h) }, s)
	request := func(method, path, body string, out any) int {
		t.Helper()
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(method, "/api/v1/pull-requests/pr/"+path, strings.NewReader(body)))
		if err := json.Unmarshal(response.Body.Bytes(), out); err != nil {
			t.Fatalf("body=%s err=%v", response.Body.String(), err)
		}
		return response.Code
	}
	var rebase pullrequestwork.Work
	if code := request("POST", "rebase", `{"prompt":"Keep API","mechanical_only":true}`, &rebase); code != http.StatusCreated || rebase.Status != pullrequestwork.StatusQueued || !rebase.MechanicalOnly || rebase.Prompt != "Keep API" || rebase.SessionID != "" {
		t.Fatalf("code=%d rebase=%+v", code, rebase)
	}
	var queue []pullrequestwork.Work
	if code := request("GET", "queue", "", &queue); code != http.StatusOK || len(queue) != 2 || queue[0].ID != first.ID || queue[1].ID != rebase.ID {
		t.Fatalf("code=%d queue=%+v", code, queue)
	}
	if code := request("POST", "queue/"+rebase.ID+"/cancel", "", &rebase); code != http.StatusOK || rebase.Status != pullrequestwork.StatusCancelled {
		t.Fatalf("code=%d cancelled=%+v", code, rebase)
	}
	var history []pullrequestwork.Work
	if code := request("GET", "rebases", "", &history); code != http.StatusOK || len(history) != 1 || history[0].ID != rebase.ID || history[0].Status != pullrequestwork.StatusCancelled {
		t.Fatalf("code=%d history=%+v", code, history)
	}
	if code := request("GET", "queue", "", &queue); code != http.StatusOK || len(queue) != 1 || queue[0].ID != first.ID {
		t.Fatalf("code=%d queue=%+v", code, queue)
	}
}

func TestRebaseRuntimeFailureAdvancesMixedQueue(t *testing.T) {
	_, store := mixedQueueStore(t)
	s := newTestWorkService(store, recoveryCatalog{pr: pullrequestwork.PullRequest{ID: "pr", Active: true, HeadCommit: "head"}}, &queueLauncher{}, nil, conflictedRebaser{})
	runtime := &queueRuntime{state: pullrequestwork.StatusRunning}
	s.SetWorkerRuntime(runtime)
	rebase := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindRebase})
	next := startQueueWork(t, s, pullrequestwork.Start{Kind: pullrequestwork.KindWorker, CommentIDs: []string{"c1"}})
	runtime.state = pullrequestwork.StatusFailed
	if err := s.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Get(t.Context(), rebase.ID)
	if err != nil || failed.Status != pullrequestwork.StatusFailed || failed.Error == "" || failed.CompletedAt == nil {
		t.Fatalf("runtime failure=%+v err=%v", failed, err)
	}
	if got, _ := store.Get(t.Context(), next.ID); got.Status != pullrequestwork.StatusRunning {
		t.Fatalf("next Address=%+v", got)
	}
}

func TestRebaseRequestReplayPreservesQueuedAndLaunchedWork(t *testing.T) {
	_, store := mixedQueueStore(t)
	catalog := &recoveryCatalog{pr: pullrequestwork.PullRequest{ID: "pr", Active: true, BaseCommit: "base", HeadCommit: "head"}}
	launcher := &queueLauncher{}
	service := newTestWorkService(store, catalog, launcher, nil, conflictedRebaser{})
	request := pullrequestwork.Start{RequestID: "rebase-request", PullRequestID: "pr", Kind: pullrequestwork.KindRebase, Prompt: "Preserve API", ExpectedBaseCommit: "base"}
	type result struct {
		work []pullrequestwork.Work
		err  error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for range 2 {
		go func() { <-start; work, err := service.Start(t.Context(), request); results <- result{work, err} }()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || len(first.work) != 1 || len(second.work) != 1 {
		t.Fatalf("replies: %+v / %+v", first, second)
	}
	if first.work[0].ID != second.work[0].ID || first.work[0].Status != pullrequestwork.StatusQueued || second.work[0].Status != pullrequestwork.StatusQueued || len(launcher.works) != 0 {
		t.Fatalf("duplicate rebase: %+v / %+v launches=%+v", first.work, second.work, launcher.works)
	}
	if err := service.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	first.work[0], _ = store.Get(t.Context(), first.work[0].ID)
	restarted := newTestWorkService(store, catalog, launcher, nil, conflictedRebaser{})
	replay, err := restarted.Start(t.Context(), request)
	if err != nil || len(replay) != 1 || replay[0].ID != first.work[0].ID || replay[0].SessionID != first.work[0].SessionID || len(launcher.works) != 1 {
		t.Fatalf("restart replay=%+v err=%v", replay, err)
	}
	request.ExpectedBaseCommit = "different-base"
	if _, err := restarted.Start(t.Context(), request); !errors.Is(err, pullrequestwork.ErrInvalid) {
		t.Fatalf("changed request accepted: %v", err)
	}
	request.RequestID, request.ExpectedBaseCommit = "queued-rebase", "base"
	queued := startQueueWork(t, restarted, request)
	replay, err = restarted.Start(t.Context(), request)
	if err != nil || queued.Status != pullrequestwork.StatusQueued || len(replay) != 1 || replay[0].ID != queued.ID || len(launcher.works) != 1 {
		t.Fatalf("queued replay=%+v err=%v", replay, err)
	}
}

func (l *queueLauncher) Reserve(_ context.Context, _ pullrequestwork.PullRequest, w pullrequestwork.Work) error {
	l.reserved = append(l.reserved, w)
	return nil
}
func (l *queueLauncher) StartReserved(ctx context.Context, pr pullrequestwork.PullRequest, w pullrequestwork.Work, prompt string) (error, error) {
	_, err := l.Start(ctx, pr, w, prompt)
	return err, nil
}
func (l *queueLauncher) SettleReserved(context.Context, pullrequestwork.Work) error { return nil }
