package pullrequestwork

import (
	"context"
	"errors"
	"testing"
	"time"
)

type memoryStore struct{ values []Work }

func (s *memoryStore) Create(_ context.Context, w Work) error {
	s.values = append(s.values, w)
	return nil
}
func (s *memoryStore) CreateBatch(_ context.Context, works []Work) error {
	s.values = append(s.values, works...)
	return nil
}
func (s *memoryStore) Update(_ context.Context, w Work) error {
	for i := range s.values {
		if s.values[i].ID == w.ID {
			s.values[i] = w
			return nil
		}
	}
	return ErrNotFound
}
func (s *memoryStore) Get(_ context.Context, id string) (Work, error) {
	for _, w := range s.values {
		if w.ID == id {
			return w, nil
		}
	}
	return Work{}, ErrNotFound
}
func (s *memoryStore) List(_ context.Context, pr string) ([]Work, error) {
	var out []Work
	for _, w := range s.values {
		if pr == "" || w.PullRequestID == pr {
			out = append(out, w)
		}
	}
	return out, nil
}

type catalogStub struct{}

func (catalogStub) PullRequest(id string) (PullRequest, bool) {
	return PullRequest{ID: id, Title: "Change", BaseBranch: "main", BaseCommit: "base", HeadBranch: "feature", HeadCommit: "head", Active: true}, id == "pr-1"
}

type launcherStub struct {
	calls        int
	pulls        []PullRequest
	works        []Work
	preflightErr error
}

func (l *launcherStub) Preflight(context.Context, Kind) error {
	return l.preflightErr
}

func (l *launcherStub) Start(_ context.Context, pullRequest PullRequest, work Work, _ string) (string, error) {
	l.calls++
	l.pulls = append(l.pulls, pullRequest)
	l.works = append(l.works, work)
	return "holon-1", nil
}

type findingsStub struct{ calls int }
type rebaserStub struct {
	result RebasePreparation
	calls  int
	err    error
}

func (r *rebaserStub) Rebase(context.Context, PullRequest, string, string) (RebasePreparation, error) {
	r.calls++
	return r.result, r.err
}

func (f *findingsStub) DeliverReviewComments(context.Context, string, string, string, string, []ReviewComment, bool) error {
	f.calls++
	return nil
}
func (f *findingsStub) DeliverReply(context.Context, string, string, string, string, string, string) error {
	f.calls++
	return nil
}

type reviewChangesStub struct{}

func (reviewChangesStub) Capture(_ context.Context, base, head string) (*ReviewInput, error) {
	return &ReviewInput{DiffBaseCommit: base, HeadCommit: head, PatchFingerprint: "patch", MessagesFingerprint: "messages"}, nil
}

func TestSerializesAddressIndependentlyOfReviewAndPreservesModes(t *testing.T) {
	s := &memoryStore{}
	l := &launcherStub{}
	service := newTestService(s, catalogStub{}, l, &findingsStub{})
	service.SetReviewChanges(reviewChangesStub{})
	created, e := startAndDispatch(t, service, Start{PullRequestID: "pr-1", Kind: KindWorker, Mode: ModeAssisted, CommentIDs: []string{"c1", "c2"}})
	if e != nil || len(created) != 2 || created[0].Mode != ModeAssisted || created[0].Status != StatusRunning || created[1].Status != StatusQueued || l.calls != 1 {
		t.Fatalf("created=%+v calls=%d err=%v", created, l.calls, e)
	}
	if _, e = startAndDispatch(t, service, Start{PullRequestID: "pr-1", Kind: KindWorker, Mode: ModeContinue}); e != nil {
		t.Fatal(e)
	}
	_, e = startAndDispatch(t, service, Start{PullRequestID: "pr-1", Kind: KindReview})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = startAndDispatch(t, service, Start{PullRequestID: "pr-1", Kind: KindReview}); !errors.Is(e, ErrBusy) {
		t.Fatalf("second review: %v", e)
	}
	if _, e = service.Complete(t.Context(), created[0].ID, "head", Completion{ReplyBody: "done", ResultHeadCommit: "new-head"}); e != nil {
		t.Fatal(e)
	}
	if err := service.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if l.calls != 4 || s.values[1].Status != StatusRunning {
		t.Fatalf("next worker not started: calls=%d work=%+v", l.calls, s.values)
	}
}
func TestReviewCommentsDeliveredExactlyOnce(t *testing.T) {
	s := &memoryStore{}
	l := &launcherStub{}
	f := &findingsStub{}
	catalog := &mutableCatalog{pr: PullRequest{ID: "pr-1", Active: true, BaseCommit: "base", HeadCommit: "head"}}
	service := newTestService(s, catalog, l, f)
	service.SetReviewChanges(reviewChangesStub{})
	created, e := startAndDispatch(t, service, Start{PullRequestID: "pr-1", Kind: KindReview, Mode: ModeAuto})
	if e != nil {
		t.Fatal(e)
	}
	catalog.pr.HeadCommit = "advanced"
	for range 2 {
		if _, e = service.Complete(t.Context(), created[0].ID, "head", Completion{Comments: []ReviewComment{{Body: "comment", Scope: "pull_request"}}}); e != nil {
			t.Fatal(e)
		}
	}
	if f.calls != 1 {
		t.Fatalf("deliveries=%d", f.calls)
	}
}
func TestRecoveryFailsInterruptedWork(t *testing.T) {
	s := &memoryStore{values: []Work{{ID: "w", PullRequestID: "pr-1", Status: StatusWaiting}}}
	service := newTestService(s, catalogStub{}, nil, nil)
	if e := service.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	if err := service.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s.values[0].Status != StatusFailed || s.values[0].Error == "" {
		t.Fatalf("work=%+v", s.values[0])
	}
}
func TestCleanRebaseCompletesWithoutLaunchingHolon(t *testing.T) {
	s := &memoryStore{}
	l := &launcherStub{}
	r := &rebaserStub{result: RebasePreparation{HeadCommit: "head", Rebased: false}}
	service := newTestService(s, catalogStub{}, l, nil, r)
	work, e := startAndDispatch(t, service, Start{PullRequestID: "pr-1", Kind: KindRebase})
	if e != nil {
		t.Fatal(e)
	}
	if len(work) != 1 || work[0].Status != StatusCompleted || work[0].ResultHeadCommit != "head" || l.calls != 0 || r.calls != 1 {
		t.Fatalf("work=%+v launch=%d rebase=%d", work, l.calls, r.calls)
	}
}

type publisherStub struct {
	head string
	err  error
}

func (p publisherStub) Publish(_ context.Context, w Work, _ PublicationOptions) (Publication, error) {
	return Publication{HeadCommit: p.head, ExpectedHead: w.HeadCommit}, p.err
}

type mutableCatalog struct{ pr PullRequest }

func (c *mutableCatalog) PullRequest(id string) (PullRequest, bool) { return c.pr, id == c.pr.ID }

type publicationCommitterStub struct {
	catalog *mutableCatalog
	store   *memoryStore
	calls   int
	err     error
}

func (c *publicationCommitterStub) CommitCompletion(ctx context.Context, completion CompletionCommit) error {
	work, expected, published := completion.Work, completion.ExpectedSourceHead, completion.ResultingHead
	c.calls++
	if c.err != nil {
		return c.err
	}
	if work.PullRequestID != c.catalog.pr.ID || (c.catalog.pr.HeadCommit != expected && c.catalog.pr.HeadCommit != published) {
		return ErrStaleHead
	}
	if err := c.store.Update(ctx, work); err != nil {
		return err
	}
	c.catalog.pr.HeadCommit = published
	return nil
}

func (c *publicationCommitterStub) CommitContinuePublication(ctx context.Context, work Work, expected, published string) error {
	return c.CommitCompletion(ctx, CompletionCommit{Work: work, ExpectedSourceHead: expected, ResultingHead: published})
}

type countingPublisher struct {
	head  string
	calls int
}

func (p *countingPublisher) Publish(_ context.Context, w Work, _ PublicationOptions) (Publication, error) {
	p.calls++
	return Publication{HeadCommit: p.head, ExpectedHead: w.HeadCommit}, nil
}

type retryFindingsStub struct{ replyCalls int }

func (*retryFindingsStub) DeliverReviewComments(context.Context, string, string, string, string, []ReviewComment, bool) error {
	return nil
}

func (f *retryFindingsStub) DeliverReply(context.Context, string, string, string, string, string, string) error {
	f.replyCalls++
	if f.replyCalls == 1 {
		return errors.New("reply unavailable")
	}
	return nil
}

func TestPublishedWorkerAdvancesHeadBeforeStartingNextWorker(t *testing.T) {
	store := &memoryStore{}
	catalog := &mutableCatalog{pr: PullRequest{ID: "pr-1", Title: "Change", BaseBranch: "main", BaseCommit: "base", HeadBranch: "feature", HeadCommit: "head", Active: true}}
	launcher := &launcherStub{}
	service := newTestService(store, catalog, launcher, &findingsStub{})
	service.SetPublisher(publisherStub{head: "new-head"})
	committer := &publicationCommitterStub{catalog: catalog, store: store}
	service.SetCompletionCommitter(committer)

	workers, err := startAndDispatch(t, service, Start{PullRequestID: "pr-1", Kind: KindWorker, Mode: ModeAuto, CommentIDs: []string{"c1", "c2"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Complete(t.Context(), workers[0].ID, "head", Completion{HeadCommit: "head", ResultHeadCommit: "new-head", ReplyBody: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := service.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}

	if committer.calls != 1 || catalog.pr.HeadCommit != "new-head" {
		t.Fatalf("publication commits=%d pull request=%+v", committer.calls, catalog.pr)
	}
	if len(launcher.works) != 2 || launcher.pulls[1].HeadCommit != "new-head" || launcher.works[1].HeadCommit != "new-head" {
		t.Fatalf("second launch pulls=%+v works=%+v", launcher.pulls, launcher.works)
	}
	if store.values[1].Status != StatusRunning || store.values[1].HeadCommit != "new-head" || store.values[1].BaseHeadCommit != "new-head" {
		t.Fatalf("second worker=%+v", store.values[1])
	}
}

func TestPublishedWorkerCanRetryReplyAfterHeadAdvances(t *testing.T) {
	store := &memoryStore{}
	catalog := &mutableCatalog{pr: PullRequest{ID: "pr-1", Title: "Change", BaseBranch: "main", BaseCommit: "base", HeadBranch: "feature", HeadCommit: "head", Active: true}}
	findings := &retryFindingsStub{}
	publisher := &countingPublisher{head: "new-head"}
	launcher := &launcherStub{}
	service := newTestService(store, catalog, launcher, findings)
	service.SetPublisher(publisher)
	service.SetCompletionCommitter(&publicationCommitterStub{catalog: catalog, store: store})

	workers, err := startAndDispatch(t, service, Start{PullRequestID: "pr-1", Kind: KindWorker, Mode: ModeAuto, CommentIDs: []string{"c1", "c2"}})
	if err != nil {
		t.Fatal(err)
	}
	completion := Completion{HeadCommit: "head", ResultHeadCommit: "new-head", ReplyBody: "done"}
	if _, err = service.Complete(t.Context(), workers[0].ID, "head", completion); err == nil || err.Error() != "reply unavailable" {
		t.Fatalf("first completion error=%v", err)
	}
	if _, err = service.Complete(t.Context(), workers[0].ID, "head", completion); err != nil {
		t.Fatal(err)
	}
	if err := service.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if publisher.calls != 1 || findings.replyCalls != 2 || store.values[0].Status != StatusCompleted || store.values[1].Status != StatusRunning || launcher.calls != 2 {
		t.Fatalf("publisher calls=%d reply calls=%d launches=%d work=%+v", publisher.calls, findings.replyCalls, launcher.calls, store.values)
	}
}

func TestStartDefaultsWorkerModeAndAllowsReviewDuringRebase(t *testing.T) {
	workerStore := &memoryStore{}
	workerLauncher := &launcherStub{}
	workerService := newTestService(workerStore, catalogStub{}, workerLauncher, &findingsStub{})
	workers, err := startAndDispatch(t, workerService, Start{PullRequestID: "pr-1", Kind: KindWorker, CommentIDs: []string{"c1"}})
	if err != nil || workers[0].Mode != ModeAuto {
		t.Fatalf("workers=%+v err=%v", workers, err)
	}

	rebaseStore := &memoryStore{values: []Work{{ID: "review", PullRequestID: "pr-1", Kind: KindReview, Status: StatusRunning}}}
	rebaseService := newTestService(rebaseStore, catalogStub{}, &launcherStub{}, nil)
	rebases, err := startAndDispatch(t, rebaseService, Start{PullRequestID: "pr-1", Kind: KindRebase})
	if err != nil || rebases[0].Mode != "" {
		t.Fatalf("rebases=%+v err=%v", rebases, err)
	}
	rebaseStore.values[0].Status = StatusCompleted
	rebaseService.SetReviewChanges(reviewChangesStub{})
	if _, err = startAndDispatch(t, rebaseService, Start{PullRequestID: "pr-1", Kind: KindReview}); err != nil {
		t.Fatalf("review during rebase: %v", err)
	}
}

func TestWaitDoesNotAdvanceQueueAndStaleDoes(t *testing.T) {
	store := &memoryStore{values: []Work{
		{ID: "w1", PullRequestID: "pr-1", CommentID: "c1", Kind: KindWorker, Mode: ModeAssisted, Status: StatusRunning},
		{ID: "w2", PullRequestID: "pr-1", CommentID: "c2", Kind: KindWorker, Mode: ModeAuto, Status: StatusQueued},
	}}
	launcher := &launcherStub{}
	service := newTestService(store, catalogStub{}, launcher, nil)
	if err := service.Wait(t.Context(), "w1", "needs help"); err != nil {
		t.Fatal(err)
	}
	if store.values[0].Status != StatusWaiting || store.values[1].Status != StatusQueued || launcher.calls != 0 {
		t.Fatalf("after wait: work=%+v launches=%d", store.values, launcher.calls)
	}
	if err := service.Stale(t.Context(), "w1", "head moved"); err != nil {
		t.Fatal(err)
	}
	if err := service.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store.values[0].Status != StatusStale || store.values[1].Status != StatusRunning || launcher.calls != 1 {
		t.Fatalf("after stale: work=%+v launches=%d", store.values, launcher.calls)
	}
}

func TestCompleteRejectsChangedPullRequestHeadBeforePublishing(t *testing.T) {
	store := &memoryStore{values: []Work{{ID: "w", PullRequestID: "pr-1", Kind: KindWorker, Status: StatusRunning, HeadCommit: "old"}}}
	catalog := &mutableCatalog{pr: PullRequest{ID: "pr-1", Active: true, HeadCommit: "moved"}}
	service := newTestService(store, catalog, nil, &findingsStub{})
	service.SetPublisher(publisherStub{head: "new"})
	_, err := service.Complete(t.Context(), "w", "old", Completion{PullRequestID: "pr-1", HeadCommit: "old", ResultHeadCommit: "new", ReplyBody: "done"})
	if !errors.Is(err, ErrStaleHead) {
		t.Fatalf("error=%v", err)
	}
}

func TestCompleteRequiresPublisherToProduceNewCommit(t *testing.T) {
	store := &memoryStore{values: []Work{{ID: "w", PullRequestID: "pr-1", Kind: KindWorker, Status: StatusRunning, HeadCommit: "head"}}}
	service := newTestService(store, catalogStub{}, nil, &findingsStub{})
	service.SetPublisher(publisherStub{head: "head"})
	_, err := service.Complete(t.Context(), "w", "head", Completion{ReplyBody: "done"})
	if !errors.Is(err, ErrPublish) {
		t.Fatalf("error=%v", err)
	}
}

func TestPublishedCheckpointRetriesFinalizationWithoutPublishingAgain(t *testing.T) {
	store := &memoryStore{}
	catalog := &mutableCatalog{pr: PullRequest{ID: "pr-1", HeadCommit: "head", Active: true}}
	publisher := &countingPublisher{head: "new-head"}
	committer := &publicationCommitterStub{catalog: catalog, store: store, err: errors.New("database unavailable")}
	service := newTestService(store, catalog, &launcherStub{}, &findingsStub{})
	service.SetPublisher(publisher)
	service.SetCompletionCommitter(committer)
	workers, err := startAndDispatch(t, service, Start{PullRequestID: "pr-1", Kind: KindWorker, CommentIDs: []string{"c1"}})
	if err != nil {
		t.Fatal(err)
	}
	completion := Completion{HeadCommit: "head", ResultHeadCommit: "new-head", ReplyBody: "Fixed", Summary: "Changed it"}
	if _, err = service.Complete(t.Context(), workers[0].ID, "head", completion); err == nil {
		t.Fatal("completion succeeded")
	}
	checkpoint := store.values[0]
	if checkpoint.Status != StatusRunning || checkpoint.PublicationState != "published" || checkpoint.ResultHeadCommit != "new-head" || checkpoint.ReplyBody != "Fixed" || checkpoint.Summary != "Changed it" {
		t.Fatalf("checkpoint=%+v", checkpoint)
	}
	committer.err = nil
	if _, err = service.Complete(t.Context(), workers[0].ID, "head", completion); err != nil {
		t.Fatal(err)
	}
	if err := service.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if publisher.calls != 1 || store.values[0].Status != StatusCompleted || catalog.pr.HeadCommit != "new-head" {
		t.Fatalf("publishes=%d work=%+v catalog=%+v", publisher.calls, store.values[0], catalog.pr)
	}
}

func TestRecoveryFinalizesPublishedWorkerAndStartsQueueFromPublishedHead(t *testing.T) {
	store := &memoryStore{values: []Work{
		{ID: "w1", PullRequestID: "pr-1", CommentID: "c1", SessionID: "s1", Kind: KindWorker, Mode: ModeAuto, Status: StatusRunning, HeadCommit: "head", ResultHeadCommit: "new-head", ReplyBody: "Fixed", PublicationState: "published"},
		{ID: "w2", PullRequestID: "pr-1", CommentID: "c2", Kind: KindWorker, Mode: ModeAuto, Status: StatusQueued},
	}}
	catalog := &mutableCatalog{pr: PullRequest{ID: "pr-1", BaseBranch: "main", BaseCommit: "base", HeadBranch: "feature", HeadCommit: "head", Active: true}}
	launcher := &launcherStub{}
	findings := &findingsStub{}
	publisher := &countingPublisher{head: "unexpected"}
	service := newTestService(store, catalog, launcher, findings)
	service.SetPublisher(publisher)
	service.SetCompletionCommitter(&publicationCommitterStub{catalog: catalog, store: store})
	if err := service.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if publisher.calls != 0 || findings.calls != 1 || store.values[0].Status != StatusCompleted || store.values[1].Status != StatusRunning || store.values[1].HeadCommit != "new-head" || catalog.pr.HeadCommit != "new-head" {
		t.Fatalf("publishes=%d replies=%d catalog=%+v work=%+v", publisher.calls, findings.calls, catalog.pr, store.values)
	}
}

func TestRecoveryReplaysCompletedReplyAndLeavesLegacyCheckpointActive(t *testing.T) {
	store := &memoryStore{values: []Work{
		{ID: "completed", PullRequestID: "pr-1", Kind: KindWorker, Status: StatusCompleted, ResultHeadCommit: "new-head", ReplyBody: "Fixed"},
		{ID: "legacy", PullRequestID: "pr-2", Kind: KindWorker, Mode: ModeAuto, Status: StatusRunning, HeadCommit: "head", ResultHeadCommit: "new-head", PublicationState: "published"},
	}}
	findings := &findingsStub{}
	service := newTestService(store, catalogStub{}, &launcherStub{}, findings)
	if err := service.Recover(t.Context()); !errors.Is(err, ErrPublish) {
		t.Fatalf("unfinished checkpoint error=%v", err)
	}
	if findings.calls != 1 || store.values[1].Status != StatusRunning || store.values[1].CompletedAt != nil {
		t.Fatalf("replies=%d work=%+v", findings.calls, store.values)
	}
}

func TestPreparedRebaseUsesAtomicPublicationCommit(t *testing.T) {
	store := &memoryStore{}
	catalog := &mutableCatalog{pr: PullRequest{ID: "pr-1", HeadCommit: "head", Active: true}}
	launcher := &launcherStub{}
	committer := &publicationCommitterStub{catalog: catalog, store: store}
	service := newTestService(store, catalog, launcher, nil, &rebaserStub{result: RebasePreparation{HeadCommit: "new-head", Rebased: true}})
	service.SetCompletionCommitter(committer)
	work, err := startAndDispatch(t, service, Start{PullRequestID: "pr-1", Kind: KindRebase})
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 || work[0].Status != StatusCompleted || work[0].PublicationState != "published" || catalog.pr.HeadCommit != "new-head" || committer.calls != 1 || launcher.calls != 0 {
		t.Fatalf("work=%+v catalog=%+v commits=%d launches=%d", work, catalog.pr, committer.calls, launcher.calls)
	}
}

type completionCommitterCapture struct {
	store   *memoryStore
	commits []CompletionCommit
}

func (c *completionCommitterCapture) CommitCompletion(ctx context.Context, commit CompletionCommit) error {
	c.commits = append(c.commits, commit)
	return c.store.Update(ctx, commit.Work)
}

func TestPreparedRebasePersistsSnapshotAndCompletesNoOpTransactionally(t *testing.T) {
	store := &memoryStore{}
	launcher := &launcherStub{}
	reconciled := PullRequest{ID: "pr-1", Title: "Change", BaseBranch: "main", BaseCommit: "cached-base", HeadBranch: "feature", HeadCommit: "live-head", Active: true}
	rebaser := &rebaserStub{result: RebasePreparation{
		PullRequest: reconciled, HeadCommit: "live-head",
		TargetBaseCommit: "target-base", TargetDiffBaseCommit: "target-base",
	}}
	committer := &completionCommitterCapture{store: store}
	service := newTestService(store, catalogStub{}, launcher, nil, rebaser)
	service.SetCompletionCommitter(committer)

	work, err := startAndDispatch(t, service, Start{PullRequestID: "pr-1", Kind: KindRebase})
	if err != nil {
		t.Fatal(err)
	}
	if launcher.calls != 0 || len(work) != 1 || work[0].Status != StatusCompleted || work[0].HeadCommit != "live-head" || work[0].ResultHeadCommit != "live-head" || work[0].TargetBaseCommit != "target-base" || work[0].TargetDiffBaseCommit != "target-base" {
		t.Fatalf("work=%+v launches=%d", work, launcher.calls)
	}
	if len(committer.commits) != 1 || committer.commits[0].Rebase == nil || committer.commits[0].ExpectedSourceHead != "live-head" || committer.commits[0].ResultingHead != "live-head" || committer.commits[0].Rebase.TargetBaseCommit != "target-base" {
		t.Fatalf("commits=%+v", committer.commits)
	}
}

func TestConflictedRebaseLaunchesFromReconciledSnapshot(t *testing.T) {
	store := &memoryStore{}
	launcher := &launcherStub{}
	reconciled := PullRequest{ID: "pr-1", Title: "Change", BaseBranch: "main", BaseCommit: "cached-base", HeadBranch: "feature", HeadCommit: "live-head", Active: true}
	rebaser := &rebaserStub{result: RebasePreparation{
		PullRequest: reconciled, HeadCommit: "live-head", Conflicts: true,
		TargetBaseCommit: "target-base", TargetDiffBaseCommit: "target-base",
	}}
	service := newTestService(store, catalogStub{}, launcher, nil, rebaser)

	work, err := startAndDispatch(t, service, Start{PullRequestID: "pr-1", Kind: KindRebase})
	if err != nil {
		t.Fatal(err)
	}
	if launcher.calls != 1 || launcher.pulls[0].HeadCommit != "live-head" || launcher.works[0].HeadCommit != "live-head" || launcher.works[0].TargetBaseCommit != "target-base" || launcher.works[0].TargetDiffBaseCommit != "target-base" {
		t.Fatalf("pulls=%+v works=%+v", launcher.pulls, launcher.works)
	}
	if len(work) != 1 || work[0].Status != StatusRunning || work[0].Mode != "" {
		t.Fatalf("work=%+v", work)
	}
}

func TestStaleRebasePreparationRecordsFailureWithoutSession(t *testing.T) {
	store := &memoryStore{}
	launcher := &launcherStub{}
	rebaser := &rebaserStub{err: ErrStaleHead}
	service := newTestService(store, catalogStub{}, launcher, nil, rebaser)

	work, err := startAndDispatch(t, service, Start{PullRequestID: "pr-1", Kind: KindRebase})
	if err != nil || len(work) != 1 || work[0].Status != StatusFailed || work[0].Error != ErrStaleHead.Error() || work[0].CompletedAt == nil {
		t.Fatalf("work=%+v error=%v", work, err)
	}
	if len(store.values) != 1 || store.values[0].Status != StatusFailed || launcher.calls != 0 {
		t.Fatalf("work=%+v launches=%d", store.values, launcher.calls)
	}
}

func TestRecoveryCompletesDurableNoOpRebaseWithoutPublishing(t *testing.T) {
	store := &memoryStore{values: []Work{{
		ID: "rebase", PullRequestID: "pr-1", Kind: KindRebase, Status: StatusRunning,
		HeadCommit: "head", ResultHeadCommit: "head", TargetBaseCommit: "base-2", TargetDiffBaseCommit: "base-2", PublicationState: "published",
	}}}
	publisher := &countingPublisher{head: "unexpected"}
	committer := &completionCommitterCapture{store: store}
	service := newTestService(store, catalogStub{}, &launcherStub{}, nil)
	service.SetPublisher(publisher)
	service.SetCompletionCommitter(committer)

	if err := service.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if publisher.calls != 0 || store.values[0].Status != StatusCompleted || len(committer.commits) != 1 {
		t.Fatalf("publishes=%d work=%+v commits=%+v", publisher.calls, store.values[0], committer.commits)
	}
	if committer.commits[0].Rebase == nil || committer.commits[0].Rebase.TargetBaseCommit != "base-2" || committer.commits[0].ResultingHead != "head" {
		t.Fatalf("completion=%+v", committer.commits[0])
	}
}

func TestPublishContinueSessionAdvancesLongLivedWorkerAndPublishesAgain(t *testing.T) {
	now := time.Now().UTC()
	store := &memoryStore{values: []Work{{
		ID: "continue", SessionID: "holon-continue", PullRequestID: "pr-1", Kind: KindWorker, Mode: ModeContinue,
		Status: StatusRunning, HeadBranch: "feature", HeadCommit: "head", BaseHeadCommit: "head", CreatedAt: now,
	}}}
	catalog := &mutableCatalog{pr: PullRequest{ID: "pr-1", HeadBranch: "feature", HeadCommit: "head", Active: true}}
	publisher := &countingPublisher{head: "published-1"}
	committer := &publicationCommitterStub{catalog: catalog, store: store}
	service := newTestService(store, catalog, nil, nil)
	service.SetPublisher(publisher)
	service.SetContinuePublicationCommitter(committer)

	first, err := service.PublishContinueSession(t.Context(), "holon-continue", PublicationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusRunning || first.HeadCommit != "published-1" || first.BaseHeadCommit != "published-1" || first.ResultHeadCommit != "published-1" || catalog.pr.HeadCommit != "published-1" {
		t.Fatalf("first=%+v catalog=%+v", first, catalog.pr)
	}

	store.values[0].Status = StatusWaiting
	publisher.head = "published-2"
	second, err := service.PublishLatestContinue(t.Context(), "pr-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != StatusWaiting || second.HeadCommit != "published-2" || second.BaseHeadCommit != "published-2" || second.ResultHeadCommit != "published-2" || catalog.pr.HeadCommit != "published-2" || publisher.calls != 2 || committer.calls != 2 {
		t.Fatalf("second=%+v catalog=%+v publishes=%d commits=%d", second, catalog.pr, publisher.calls, committer.calls)
	}
}

func TestPublishContinueValidatesWorkerAndCatalogBeforePublishing(t *testing.T) {
	tests := []struct {
		name string
		work Work
		pr   PullRequest
		want error
	}{
		{name: "non continue", work: Work{Kind: KindWorker, Mode: ModeAuto, Status: StatusRunning}, pr: PullRequest{Active: true}, want: ErrInvalid},
		{name: "settled", work: Work{Kind: KindWorker, Mode: ModeContinue, Status: StatusCompleted}, pr: PullRequest{Active: true}, want: ErrInvalid},
		{name: "inactive", work: Work{Kind: KindWorker, Mode: ModeContinue, Status: StatusRunning}, pr: PullRequest{}, want: ErrPullRequestInactive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.work.ID, test.work.SessionID, test.work.PullRequestID = "work", "session", "pr-1"
			test.pr.ID = "pr-1"
			store := &memoryStore{values: []Work{test.work}}
			catalog := &mutableCatalog{pr: test.pr}
			publisher := &countingPublisher{head: "published"}
			service := newTestService(store, catalog, nil, nil)
			service.SetPublisher(publisher)
			service.SetContinuePublicationCommitter(&publicationCommitterStub{catalog: catalog, store: store})
			_, err := service.PublishContinueSession(t.Context(), "session", PublicationOptions{})
			if !errors.Is(err, test.want) || publisher.calls != 0 {
				t.Fatalf("error=%v want=%v publisher calls=%d", err, test.want, publisher.calls)
			}
		})
	}
}

func TestPublishContinueReportsNoChangesAndPublicationFailures(t *testing.T) {
	work := Work{ID: "work", SessionID: "session", PullRequestID: "pr-1", Kind: KindWorker, Mode: ModeContinue, Status: StatusRunning, HeadBranch: "feature", HeadCommit: "head", BaseHeadCommit: "head"}
	catalog := &mutableCatalog{pr: PullRequest{ID: "pr-1", Active: true, HeadBranch: "feature", HeadCommit: "head"}}
	for _, publisher := range []publisherStub{{head: "head"}, {err: errors.New("dirty workspace")}, {err: ErrStaleHead}} {
		store := &memoryStore{values: []Work{work}}
		service := newTestService(store, catalog, nil, nil)
		service.SetPublisher(publisher)
		service.SetContinuePublicationCommitter(&publicationCommitterStub{catalog: catalog, store: store})
		if _, err := service.PublishContinueSession(t.Context(), "session", PublicationOptions{}); !errors.Is(err, ErrPublish) && !errors.Is(err, ErrStaleHead) {
			t.Fatalf("publisher=%+v error=%v", publisher, err)
		}
	}
}

func TestRecoveryPreservesActiveContinueWorker(t *testing.T) {
	for _, status := range []Status{StatusRunning, StatusWaiting} {
		store := &memoryStore{values: []Work{{ID: "continue", PullRequestID: "pr-1", Kind: KindWorker, Mode: ModeContinue, Status: status, HeadCommit: "head"}}}
		service := newTestService(store, catalogStub{}, &launcherStub{}, nil)
		if err := service.Recover(t.Context()); err != nil {
			t.Fatal(err)
		}
		if store.values[0].Status != status || store.values[0].CompletedAt != nil {
			t.Fatalf("status=%s work=%+v", status, store.values[0])
		}
	}
}

func newTestService(store Store, catalog Catalog, launcher Launcher, comments CommentDelivery, rebasers ...Rebaser) *Service {
	service := New(store, catalog, launcher, comments, rebasers...)
	service.SetCompletionCommitter(testCompletionStore{store})
	return service
}

type testCompletionStore struct{ Store }

func (store testCompletionStore) CommitCompletion(ctx context.Context, completion CompletionCommit) error {
	return store.Update(ctx, completion.Work)
}

func (catalogStub) SyncPullRequest(context.Context, string) error { return nil }
func (catalogStub) PrepareWork(context.Context, string) error     { return nil }

func (*mutableCatalog) SyncPullRequest(context.Context, string) error { return nil }
func (*mutableCatalog) PrepareWork(context.Context, string) error     { return nil }

// Existing execution tests explicitly run the dispatcher after admission.
func startAndDispatch(t *testing.T, service *Service, input Start) ([]Work, error) {
	t.Helper()
	work, err := service.Start(t.Context(), input)
	if err != nil {
		return work, err
	}
	if err := service.Dispatch(t.Context()); err != nil {
		return work, err
	}
	for i := range work {
		work[i], err = service.store.Get(t.Context(), work[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return work, nil
}

func (l *launcherStub) Reserve(context.Context, PullRequest, Work) error { return nil }
func (l *launcherStub) StartReserved(ctx context.Context, pr PullRequest, w Work, prompt string) (error, error) {
	_, err := l.Start(ctx, pr, w, prompt)
	return err, nil
}
func (l *launcherStub) SettleReserved(context.Context, Work) error { return nil }
