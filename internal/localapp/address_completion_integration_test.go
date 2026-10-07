package localapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	worksqlite "github.com/holark-ai/holark/internal/pullrequestwork/sqliteadapter"
	"github.com/holark-ai/holark/internal/terminals"
)

type addressCompletionCatalog struct{ pr pullrequestwork.PullRequest }

func (c addressCompletionCatalog) PullRequest(id string) (pullrequestwork.PullRequest, bool) {
	return c.pr, id == c.pr.ID
}

type addressCompletionFindings struct {
	pullrequestwork.CommentDelivery
}

func (addressCompletionFindings) DeliverReply(context.Context, string, string, string, string, string, string) error {
	return nil
}

type unavailableAddressPublication struct {
	pullrequestlifecycle.PublicationCatalog
}

func (unavailableAddressPublication) GetPullRequest(string) (pullrequestlifecycle.PullRequest, bool) {
	return pullrequestlifecycle.PullRequest{}, false
}

func TestAddressPendingRetryReconcilesHolonCancellation(t *testing.T) {
	var cases []struct {
		mode               pullrequestwork.Mode
		startHead, outcome string
	}
	for _, mode := range []pullrequestwork.Mode{pullrequestwork.ModeAssisted, pullrequestwork.ModeAuto} {
		for _, startHead := range []string{"", "head"} {
			for _, outcome := range []string{"normal exit", "clarification wait", "cancel"} {
				cases = append(cases, struct {
					mode               pullrequestwork.Mode
					startHead, outcome string
				}{mode, startHead, outcome})
			}
		}
	}
	for _, test := range cases {
		t.Run(string(test.mode)+"/starting HEAD="+test.startHead+"/"+test.outcome, func(t *testing.T) {
			mode, startHead, outcome := test.mode, test.startHead, test.outcome
			ctx := t.Context()
			hs, holonStore := terminalTestService(t)
			hs = holons.NewServiceWithRepository(holonStore, artifactRepository{inspection: holons.WorkspaceInspection{HeadCommit: "result", Clean: true}})
			tid, err := terminals.NewID()
			if err != nil {
				t.Fatal(err)
			}
			h := holons.Holon{
				ID: "address", Kind: holons.KindPullWorker, Status: holons.StatusRunning, PullRequestID: "pr",
				WorktreePath: t.TempDir(), CreatedAt: time.Now().UTC(),
				AgentSessions: []holons.AgentSession{{
					ID: "source", HolonID: "address", TerminalID: string(tid),
					AgentType: string(protocol.HarnessClaudeCode), Status: string(holons.StatusRunning),
					CommitStartHead: startHead, CommitPrompt: &holons.CommitPrompt{State: "commit_discussion_started"}, ResumeTarget: "saved-conversation",
				}},
			}
			if err = holonStore.Create(ctx, h); err != nil {
				t.Fatal(err)
			}
			runtime := terminalRuntime(t, hs, &launchGateway{})
			db, err := database.Open(filepath.Join(t.TempDir(), "work.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			store, err := worksqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			var launches []string
			start := reviewLaunchFunc(func(_ context.Context, _ pullrequestwork.PullRequest, w pullrequestwork.Work, _ string) (string, error) {
				launches = append(launches, w.ID)
				return "next-holon", nil
			})
			catalog := addressCompletionCatalog{pullrequestwork.PullRequest{ID: "pr", Active: true, HeadCommit: "head"}}
			work := pullrequestwork.New(store, catalog, start, nil)
			work.SetWorkerRuntime(localWorkRuntime{holons: runtime})
			runtime.work = work
			bindWorkFinalization(hs, work)
			completion := pullrequestwork.Completion{PullRequestID: "pr", HeadCommit: "head", ResultHeadCommit: "result", ReplyBody: "Fixed"}
			if err = store.CreateBatch(ctx, []pullrequestwork.Work{
				{
					ID: "retrying", PullRequestID: "pr", SessionID: h.ID, CommentID: "first", HeadCommit: "head",
					Kind: pullrequestwork.KindWorker, Mode: mode, Status: pullrequestwork.StatusRunning,
				},
				{
					ID: "queued", PullRequestID: "pr", CommentID: "second",
					Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusQueued,
				},
			}); err != nil {
				t.Fatal(err)
			}
			artifact := filepath.Join(h.WorktreePath, pullRequestWorkArtifactPath(pullrequestwork.KindWorker))
			if err = os.MkdirAll(filepath.Dir(artifact), 0o700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(artifact, []byte(`{"pull_request_id":"pr","head_commit":"head","reply":"Fixed"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			address := &addressCompletionCoordinator{holons: hs, work: work, rebase: &rebaseAgentCoordinator{holons: hs, catalog: unavailableAddressPublication{}}}
			state := localAgentState{holons: hs, work: work, commitCloser: runtime, workCompletion: localPullRequestWorkCompletionCoordinator{holons: hs, work: work, address: address}}
			// Publication cannot proceed yet. The successful completion callback
			// must retain its saved source, including forks from the old version.
			if err = state.Observed(ctx, h.ID, "source", harness.Event{InputState: protocol.InputTaskComplete}); err != nil {
				t.Fatal(err)
			}
			pending, err := store.Get(ctx, "retrying")
			if err != nil || pending.PendingCompletion == nil || pending.PendingCompletion.SourceAgentID != "source" || pending.PendingCompletion.State != "retry" {
				t.Fatalf("completion was not saved: %+v, %v", pending, err)
			}
			restarted := holons.NewService(holonStore)
			bindWorkFinalization(restarted, work)
			restored, restoreErr := restarted.Get(ctx, h.ID)
			if restoreErr != nil || restored.ApplicationPhase != "finalizing" {
				t.Fatalf("restart lost pending phase=%+v %v", restored, restoreErr)
			}
			kept, err := hs.Get(ctx, h.ID)
			if err != nil || kept.ApplicationPhase != "finalizing" || kept.AgentSession("source").ClosedAt != nil || kept.AgentSession("source").Status != string(holons.StatusRunning) || kept.AgentSession("source").ResumeTarget != "saved-conversation" {
				t.Fatalf("pending source was closed: %+v, %v", kept, err)
			}
			if outcome == "clarification wait" {
				if err = state.Observed(ctx, h.ID, "source", harness.Event{InputState: protocol.InputUserRequired}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = os.Stat(artifact); err != nil {
				t.Fatal(err)
			}
			if _, err = work.SetAddressCompletionState(ctx, "retrying", "retry", "", "", "Publication failed"); err != nil {
				t.Fatal(err)
			}
			assertPending := func() {
				t.Helper()
				if err := work.Dispatch(ctx); err != nil {
					t.Fatal(err)
				}
				current, err := hs.Get(ctx, h.ID)
				expectedPhase := "finalizing"
				if current.EndRequested {
					expectedPhase = ""
				}
				if err != nil || current.ApplicationPhase != expectedPhase {
					t.Fatalf("pending phase=%+v %v", current, err)
				}
				pending, err := store.Get(ctx, "retrying")
				if err != nil || pending.Status != pullrequestwork.StatusWaiting || pending.CompletedAt != nil || pending.Error != "Publication failed" || pending.PendingCompletion == nil || pending.PendingCompletion.State != "retry" || !reflect.DeepEqual(pending.PendingCompletion.Completion, completion) {
					t.Fatalf("saved completion lost: work=%+v err=%v", pending, err)
				}
				queued, err := store.Get(ctx, "queued")
				if err != nil || queued.Status != pullrequestwork.StatusQueued || len(launches) != 0 {
					t.Fatalf("next request started prematurely: work=%+v launches=%v err=%v", queued, launches, err)
				}
			}
			assertPending()
			if outcome == "cancel" {
				// End holon bypasses work.Cancel, so the work remains waiting.
				cancelled, err := runtime.End(ctx, h.ID)
				if err != nil || cancelled.Status != holons.StatusCancelling {
					t.Fatalf("End holon: holon=%+v err=%v", cancelled, err)
				}
				assertPending()
			}
			if outcome != "clarification wait" {
				if err = (&localTerminalProducts{holons: hs}).ApplyTerminalCompletion(localTerminalHost, terminals.ProcessCompletion{TerminalID: tid, ExitCode: 0}); err != nil {
					t.Fatal(err)
				}
			}
			if outcome != "cancel" {
				assertPending()
				return
			}
			if err = work.Dispatch(ctx); err != nil {
				t.Fatal(err)
			}
			current, readErr := hs.Get(ctx, h.ID)
			if readErr != nil || current.ApplicationPhase != "" {
				t.Fatalf("cancelled phase=%+v %v", current, readErr)
			}
			cancelled, err := store.Get(ctx, "retrying")
			if err != nil || cancelled.Status != pullrequestwork.StatusCancelled || cancelled.CompletedAt == nil || cancelled.Error != "" {
				t.Fatalf("cancelled work=%+v err=%v", cancelled, err)
			}
			next, err := store.Get(ctx, "queued")
			if err != nil || next.Status != pullrequestwork.StatusRunning || next.SessionID == "" || len(launches) != 1 || launches[0] != next.ID {
				t.Fatalf("next queued request did not start: work=%+v launches=%v err=%v", next, launches, err)
			}
		})
	}
}

func TestAddressCompletionSurvivesQueuedWorkerLaunchFailure(t *testing.T) {
	for _, failure := range []string{"command", "terminal"} {
		t.Run(failure, func(t *testing.T) {
			ctx := t.Context()
			hs, holonStore := terminalTestService(t)
			db, err := database.Open(filepath.Join(t.TempDir(), "work.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			store, err := worksqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			h := holons.Holon{
				ID: "queued-holon", Kind: holons.KindPullWorker, Status: holons.StatusQueued,
				WorktreePath: t.TempDir(), CreatedAt: time.Now().UTC(),
				AgentSessions: []holons.AgentSession{{
					ID: "queued-agent", HolonID: "queued-holon", AgentType: string(protocol.HarnessClaudeCode),
					Prompt: "Address the comment", Status: string(holons.StatusQueued),
				}},
			}
			wantReason := "terminal launch refused"
			if failure == "command" {
				h.WorktreePath = "relative-worktree"
				wantReason = "invalid Claude Code command"
			}
			if err = holonStore.Create(ctx, h); err != nil {
				t.Fatal(err)
			}
			coordinator := &addressCompletionCoordinator{holons: hs}
			native, ok := harness.RegistryWithCodex(forkCommandServer{}).Driver(protocol.HarnessClaudeCode)
			if !ok {
				t.Fatal("missing native Claude Code adapter")
			}
			// Use native command construction, replacing only availability probing
			// and the terminal boundary; no agent CLI is executed or simulated.
			driver := &forkObservedDriver{Driver: native}
			launcher := &forkLaunchRecorder{err: errors.New("terminal launch refused")}
			agents, err := agentsessions.New(
				harness.NewRegistry(map[protocol.HarnessType]harness.Driver{protocol.HarnessClaudeCode: driver}),
				&localAgentState{holons: hs, addressCompletion: coordinator}, launcher, nil, nil, t.TempDir(),
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(agents.Close)
			start := reviewLaunchFunc(func(ctx context.Context, _ pullrequestwork.PullRequest, _ pullrequestwork.Work, _ string) (string, error) {
				return h.ID, agents.Launch(ctx, h.ID, h.AgentSessions[0].ID, terminals.Dimensions{Columns: 80, Rows: 24}, "", agentsessions.LaunchOptions{})
			})
			catalog := addressCompletionCatalog{pullrequestwork.PullRequest{ID: "pr", Active: true, HeadCommit: "head"}}
			work := pullrequestwork.New(store, catalog, start, addressCompletionFindings{})
			work.SetCompletionCommitter(addressCompletionStore{store})
			bindWorkFinalization(hs, work)
			coordinator.work = work
			// Resume after the durable publication checkpoint. Completion still
			// holds the coordinator lock while releasing this work's queue slot.
			completion := pullrequestwork.Completion{PullRequestID: "pr", HeadCommit: "head", ResultHeadCommit: "result", ReplyBody: "Fixed"}
			if err = store.CreateBatch(ctx, []pullrequestwork.Work{
				{
					ID: "finishing", PullRequestID: "pr", SessionID: "h", CommentID: "first",
					Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusRunning,
					HeadCommit: "head", ResultHeadCommit: "result", PublicationState: "published",
					PendingCompletion: &pullrequestwork.PendingAddressCompletion{SourceAgentID: "source", State: "ready", Completion: completion},
				},
				{
					ID: "queued", PullRequestID: "pr", CommentID: "second",
					Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusQueued,
				},
			}); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				err := coordinator.Retry(ctx, "h")
				if err == nil {
					// Subsequent operations must be able to acquire both locks.
					err = coordinator.Retry(ctx, "h")
				}
				if err == nil {
					_, err = work.Complete(ctx, "finishing", "head", completion)
				}
				done <- err
			}()
			select {
			case err = <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Address completion deadlocked after queued worker launch failure")
			}
			completed, err := store.Get(ctx, "finishing")
			if err != nil || completed.Status != pullrequestwork.StatusCompleted {
				t.Fatalf("completed work=%+v err=%v", completed, err)
			}
			if err := work.Dispatch(ctx); err != nil {
				t.Fatal(err)
			}
			failed, err := store.Get(ctx, "queued")
			if err != nil || failed.Status != pullrequestwork.StatusFailed || !strings.Contains(failed.Error, wantReason) {
				t.Fatalf("queued work=%+v err=%v", failed, err)
			}
			outgoing, readErr := hs.Get(ctx, "h")
			if readErr != nil || outgoing.ApplicationPhase != "" {
				t.Fatalf("successor changed predecessor phase=%+v %v", outgoing, readErr)
			}
			stopped, err := hs.Get(ctx, h.ID)
			if err != nil || stopped.AgentSession("queued-agent").Status != string(holons.StatusFailed) || !strings.Contains(stopped.AgentSession("queued-agent").Reason, wantReason) {
				t.Fatalf("queued holon=%+v err=%v", stopped, err)
			}
		})
	}
}

type addressCompletionStore struct{ pullrequestwork.Store }

func (store addressCompletionStore) CommitCompletion(ctx context.Context, completion pullrequestwork.CompletionCommit) error {
	return store.Update(ctx, completion.Work)
}

func (addressCompletionCatalog) SyncPullRequest(context.Context, string) error { return nil }
func (addressCompletionCatalog) PrepareWork(context.Context, string) error     { return nil }

type heldAddressPreparation struct {
	addressCompletionCatalog
	entered, release chan struct{}
}

func (c heldAddressPreparation) PrepareWork(ctx context.Context, _ string) error {
	close(c.entered)
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type dispatchCompletionStore struct {
	addressCompletionStore
	committed chan struct{}
}

func (s dispatchCompletionStore) CommitCompletion(ctx context.Context, completion pullrequestwork.CompletionCommit) error {
	if err := s.addressCompletionStore.CommitCompletion(ctx, completion); err != nil {
		return err
	}
	close(s.committed)
	return nil
}

type retirementDuringDispatch struct {
	check func()
	localAgentRuntime
	preparing <-chan struct{}
	cancelled []string
}

func (r *retirementDuringDispatch) Cancel(ctx context.Context, _, agent string) error {
	select {
	case <-r.preparing:
	case <-ctx.Done():
		return ctx.Err()
	}
	if r.check != nil {
		r.check()
	}
	r.cancelled = append(r.cancelled, agent)
	return nil
}

func TestAddressCompletionRetiresOwnedAgentsWhileSuccessorPreparationIsBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	hs, holonStore := terminalTestService(t)
	h := holons.Holon{ID: "outgoing", Kind: holons.KindPullWorker, Status: holons.StatusRunning, WorktreePath: t.TempDir(), CreatedAt: time.Now().UTC(),
		RebaseAttempt: &holons.RebaseAttempt{AgentID: "rebase", State: "succeeded"},
		AgentSessions: []holons.AgentSession{
			{ID: "source", HolonID: "outgoing", Status: string(holons.StatusRunning)},
			{ID: "rebase", HolonID: "outgoing", Status: string(holons.StatusRunning)},
			{ID: "other", HolonID: "outgoing", Status: string(holons.StatusRunning)},
		},
	}
	if err := holonStore.Create(ctx, h); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Join(t.TempDir(), "work.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := worksqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	completion := pullrequestwork.Completion{ResultHeadCommit: "result", ReplyBody: "Fixed"}
	if err := store.CreateBatch(ctx, []pullrequestwork.Work{
		{ID: "finishing", PullRequestID: "pr", SessionID: h.ID, CommentID: "first", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusRunning, HeadCommit: "head", ResultHeadCommit: "result", PublicationState: "published", PendingCompletion: &pullrequestwork.PendingAddressCompletion{SourceAgentID: "source", State: "ready", Completion: completion}},
		{ID: "next", PullRequestID: "pr", CommentID: "second", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusQueued},
	}); err != nil {
		t.Fatal(err)
	}
	entered, release, committed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	catalog := heldAddressPreparation{addressCompletionCatalog{pullrequestwork.PullRequest{ID: "pr", HeadCommit: "head", Active: true}}, entered, release}
	work := pullrequestwork.New(store, catalog, reviewLaunchFunc(func(context.Context, pullrequestwork.PullRequest, pullrequestwork.Work, string) (string, error) {
		return "next-session", nil
	}), addressCompletionFindings{})
	work.SetCompletionCommitter(dispatchCompletionStore{addressCompletionStore{store}, committed})
	bindWorkFinalization(hs, work)
	retirement := &retirementDuringDispatch{preparing: entered, check: func() {
		h, err := hs.Get(ctx, "outgoing")
		if err != nil || h.ApplicationPhase != "finalizing" {
			t.Errorf("retirement phase=%+v %v", h, err)
		}
	}}
	coordinator := &addressCompletionCoordinator{holons: hs, work: work, agents: retirement}
	dispatched := make(chan error, 1)
	go func() {
		select {
		case <-committed:
			dispatched <- work.Dispatch(ctx)
		case <-ctx.Done():
			dispatched <- ctx.Err()
		}
	}()
	defer func() { cancel(); <-dispatched }()
	completed := make(chan error, 1)
	go func() { completed <- coordinator.Retry(ctx, h.ID) }()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("completion waited for successor preparation")
	}
	if !reflect.DeepEqual(retirement.cancelled, []string{"source", "rebase"}) {
		t.Fatalf("retired=%v", retirement.cancelled)
	}
	current, err := hs.Get(ctx, h.ID)
	if err != nil || current.ApplicationPhase != "" || current.AgentSession("source").Status != string(holons.StatusExpired) || current.AgentSession("rebase").Status != string(holons.StatusExpired) || current.AgentSession("other").Status != string(holons.StatusRunning) {
		t.Fatalf("agents=%+v error=%v", current.AgentSessions, err)
	}
	finishing, err := store.Get(ctx, "finishing")
	if err != nil || finishing.Status != pullrequestwork.StatusCompleted {
		t.Fatalf("completion=%+v error=%v", finishing, err)
	}
	next, err := store.Get(ctx, "next")
	if err != nil || next.Status != pullrequestwork.StatusQueued {
		t.Fatalf("blocked successor=%+v error=%v", next, err)
	}
	close(release)
}

// Record publication at the service boundary without running an agent CLI.
type addressCompletionPublisher struct{ published []string }

func (p *addressCompletionPublisher) Publish(_ context.Context, w pullrequestwork.Work, _ pullrequestwork.PublicationOptions) (pullrequestwork.Publication, error) {
	head := w.PendingCompletion.Completion.ResultHeadCommit
	p.published = append(p.published, head)
	return pullrequestwork.Publication{HeadCommit: head}, nil
}

func TestAddressRebaseCompletionSurvivesDispatchAfterBothAgentsExit(t *testing.T) {
	ctx := t.Context()
	hs, holonStore := terminalTestService(t)
	// Terminal completion has verified the Rebase, but has not yet advanced
	// the saved Address completion. Both agents have already exited.
	h := holons.Holon{
		ID: "address", Kind: holons.KindPullWorker, PullRequestID: "pr", Status: holons.StatusCompleted,
		WorktreePath: t.TempDir(), CreatedAt: time.Now().UTC(),
		RebaseAttempt: &holons.RebaseAttempt{ID: "attempt", AgentID: "rebase", State: "succeeded", TargetCommit: "target", ResultHeadCommit: "rebased-result"},
		AgentSessions: []holons.AgentSession{
			{ID: "source", HolonID: "address", Status: string(holons.StatusCompleted)},
			{ID: "rebase", HolonID: "address", Status: string(holons.StatusCompleted)},
		},
	}
	if err := holonStore.Create(ctx, h); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Join(t.TempDir(), "work.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := worksqlite.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	completion := pullrequestwork.Completion{PullRequestID: "pr", HeadCommit: "head", ResultHeadCommit: "original-result", ReplyBody: "Fixed"}
	if err := store.CreateBatch(ctx, []pullrequestwork.Work{
		{
			ID: "finishing", PullRequestID: "pr", SessionID: h.ID, CommentID: "first", HeadCommit: "head",
			Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusWaiting,
			PendingCompletion: &pullrequestwork.PendingAddressCompletion{SourceAgentID: "source", State: "rebasing", RebaseAttemptID: "attempt", RebaseAgentID: "rebase", Completion: completion},
		},
		{ID: "next", PullRequestID: "pr", CommentID: "second", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusQueued},
	}); err != nil {
		t.Fatal(err)
	}
	var launches []string
	work := pullrequestwork.New(store, addressCompletionCatalog{pullrequestwork.PullRequest{ID: "pr", HeadCommit: "target", Active: true}}, reviewLaunchFunc(func(_ context.Context, _ pullrequestwork.PullRequest, w pullrequestwork.Work, _ string) (string, error) {
		launches = append(launches, w.ID)
		return "next-holon", nil
	}), addressCompletionFindings{})
	work.SetWorkerRuntime(localWorkRuntime{holons: terminalRuntime(t, hs, &launchGateway{})})
	work.SetCompletionCommitter(addressCompletionStore{store})
	bindWorkFinalization(hs, work)
	current, readErr := hs.Get(ctx, h.ID)
	if readErr != nil || current.ApplicationPhase != "finalizing" {
		t.Fatalf("recovered phase=%+v %v", current, readErr)
	}
	publisher := &addressCompletionPublisher{}
	work.SetPublisher(publisher)
	if err := work.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := store.Get(ctx, "finishing")
	if err != nil || pending.Status != pullrequestwork.StatusWaiting || pending.CompletedAt != nil || pending.PendingCompletion == nil || pending.PendingCompletion.State != "rebasing" || !reflect.DeepEqual(pending.PendingCompletion.Completion, completion) {
		t.Fatalf("scan discarded saved completion: work=%+v err=%v", pending, err)
	}
	next, err := store.Get(ctx, "next")
	if err != nil || next.Status != pullrequestwork.StatusQueued || len(launches) != 0 || len(publisher.published) != 0 {
		t.Fatalf("successor started before publication: work=%+v launches=%v err=%v", next, launches, err)
	}
	// Resume the completion callback. An unavailable publication target leaves
	// the rebased result saved for retry instead of losing it to reconciliation.
	coordinator := &addressCompletionCoordinator{holons: hs, work: work, rebase: &rebaseAgentCoordinator{holons: hs, catalog: unavailableAddressPublication{}}}
	if _, err := coordinator.Rebased(ctx, h); err != nil {
		t.Fatal(err)
	}
	pending, err = store.Get(ctx, "finishing")
	if err != nil || pending.PendingCompletion.State != "retry" || pending.PendingCompletion.Completion.ResultHeadCommit != "rebased-result" || pending.PendingCompletion.Completion.ReplyBody != completion.ReplyBody {
		t.Fatalf("rebased completion was not retained: work=%+v err=%v", pending, err)
	}
	current, readErr = hs.Get(ctx, h.ID)
	if readErr != nil || current.ApplicationPhase != "finalizing" {
		t.Fatalf("retry phase=%+v %v", current, readErr)
	}
	pending, err = work.SetAddressCompletionState(ctx, pending.ID, "ready", "target", "", "")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := work.Complete(ctx, pending.ID, pending.HeadCommit, pending.PendingCompletion.Completion)
	if err != nil || completed.Status != pullrequestwork.StatusCompleted || completed.PublicationState != "published" || !reflect.DeepEqual(publisher.published, []string{"rebased-result"}) {
		t.Fatalf("publication=%+v published=%v err=%v", completed, publisher.published, err)
	}
	current, readErr = hs.Get(ctx, h.ID)
	if readErr != nil || current.ApplicationPhase != "" {
		t.Fatalf("settled historical completion phase=%+v %v", current, readErr)
	}
	if err := work.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	next, err = store.Get(ctx, "next")
	if err != nil || next.Status != pullrequestwork.StatusRunning || !reflect.DeepEqual(launches, []string{"next"}) {
		t.Fatalf("successor did not start after publication: work=%+v launches=%v err=%v", next, launches, err)
	}
}
