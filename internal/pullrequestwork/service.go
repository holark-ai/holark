package pullrequestwork

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

type Catalog interface {
	WorkPreparer
	SyncPullRequest(context.Context, string) error
	PullRequest(string) (PullRequest, bool)
}
type WorkPreparer interface {
	PrepareWork(context.Context, string) error
}

type Launcher interface {
	Start(context.Context, PullRequest, Work, string) (string, error)
}

// QueuedLauncher separates durable identity from slow preparation and launch.
// StartReserved returns execution and persistence errors separately; uncertain
// persistence must stop dispatch without attempting another side effect.
type QueuedLauncher interface {
	Reserve(context.Context, PullRequest, Work) error
	StartReserved(context.Context, PullRequest, Work, string) (executionErr, persistenceErr error)
	SettleReserved(context.Context, Work) error
}

var ErrReservationEnded = errors.New("queued holon has ended")

type continueLauncher interface {
	PreflightContinue(context.Context, ContinueOptions) error
	StartContinue(context.Context, PullRequest, Work, string, ContinueOptions) (string, error)
}
type launcherPreflighter interface {
	Preflight(context.Context, Kind) error
}
type CommentDelivery interface {
	// Assisted review findings are delivered as drafts the user publishes individually.
	DeliverReviewComments(ctx context.Context, pr, session, review, head string, comments []ReviewComment, draft bool) error
	DeliverReply(context.Context, string, string, string, string, string, string) error
}
type Rebaser interface {
	Rebase(context.Context, PullRequest, string, string) (RebasePreparation, error)
}
type RebaseReadinessChecker interface {
	RebaseReadiness(context.Context, PullRequest) (RebaseReadiness, error)
}
type Publisher interface {
	Publish(context.Context, Work, PublicationOptions) (Publication, error)
}
type ContinuePublicationCommitter interface {
	CommitContinuePublication(context.Context, Work, string, string) error
}
type CompletionCommitter interface {
	CommitCompletion(context.Context, CompletionCommit) error
}

type Service struct {
	reviewChanges       ReviewChanges
	store               Store
	catalog             Catalog
	launcher            Launcher
	commentDelivery     CommentDelivery
	rebaser             Rebaser
	publisher           Publisher
	continueCommitter   ContinuePublicationCommitter
	completionCommitter CompletionCommitter
	comments            CommentReader
	runtime             WorkerRuntime
	mu                  sync.Mutex
	dispatchMu          sync.Mutex
	dispatchWake        chan struct{}
}

func (s *Service) SetReviewChanges(changes ReviewChanges) { s.reviewChanges = changes }

func (s *Service) SetPublisher(p Publisher) { s.publisher = p }
func (s *Service) SetContinuePublicationCommitter(committer ContinuePublicationCommitter) {
	s.continueCommitter = committer
}
func (s *Service) SetCompletionCommitter(committer CompletionCommitter) {
	s.completionCommitter = committer
}

func New(store Store, catalog Catalog, launcher Launcher, comments CommentDelivery, rebasers ...Rebaser) *Service {
	s := &Service{store: store, catalog: catalog, launcher: launcher, commentDelivery: comments, dispatchWake: make(chan struct{}, 1)}
	if len(rebasers) > 0 {
		s.rebaser = rebasers[0]
	}
	return s
}

func (s *Service) RebaseReadiness(ctx context.Context, pullRequestID string) (RebaseReadiness, error) {
	pullRequest, ok := s.catalog.PullRequest(pullRequestID)
	if !ok {
		return RebaseReadiness{}, ErrPullRequestNotFound
	}
	if !pullRequest.Active {
		return RebaseReadiness{}, ErrPullRequestInactive
	}
	checker, ok := s.rebaser.(RebaseReadinessChecker)
	if !ok {
		return RebaseReadiness{}, ErrRebaseReadinessUnavailable
	}
	return checker.RebaseReadiness(ctx, pullRequest)
}

// Start admits Address and Rebase batches durably and returns queued records.
// Their execution results are read through List/Queue after dispatch; reviews
// and Continue workers still start directly.
func (s *Service) Start(ctx context.Context, in Start) ([]Work, error) {
	ctx = workStartupContext(ctx, in.RequestID)
	started := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	slog.DebugContext(ctx, "Work startup lock acquired", "pull_request_id", in.PullRequestID, "request_id", pullrequestlifecycle.RequestID(ctx), "kind", in.Kind, "mode", in.Mode, "duration", time.Since(started))
	if in.Kind == KindWorker && in.Mode == "" {
		in.Mode = ModeAuto
	}
	all, err := s.store.List(ctx, in.PullRequestID)
	if err != nil {
		return nil, err
	}
	if in.Kind == KindRebase && in.RequestID != "" {
		for _, work := range all {
			if work.RequestID != in.RequestID {
				continue
			}
			if work.Kind != in.Kind || work.Prompt != in.Prompt || work.MechanicalOnly != in.MechanicalOnly || work.RequestedBaseCommit != in.ExpectedBaseCommit {
				return nil, ErrInvalid
			}
			return []Work{work}, nil
		}
	}
	if in.Kind == KindWorker && in.Mode == ModeContinue {
		if err := s.catalog.PrepareWork(ctx, in.PullRequestID); err != nil {
			return nil, err
		}
	}
	pr, ok := s.catalog.PullRequest(in.PullRequestID)
	if !ok {
		return nil, ErrPullRequestNotFound
	}
	if !pr.Active && in.Kind == KindRebase {
		return nil, ErrStaleHead
	}
	if !pr.Active {
		return nil, ErrPullRequestInactive
	}
	if in.Kind != KindReview && in.Kind != KindWorker && in.Kind != KindRebase {
		return nil, ErrInvalid
	}
	if in.Kind == KindWorker && in.Mode != ModeAuto && in.Mode != ModeAssisted && in.Mode != ModeContinue {
		return nil, ErrInvalid
	}
	if in.Kind == KindReview {
		if in.Mode == "" {
			in.Mode = ModeAssisted
		}
		if in.Mode != ModeAuto && in.Mode != ModeAssisted {
			return nil, ErrInvalid
		}
	}
	if in.Kind == KindRebase {
		in.Mode = ""
	}
	if in.Startup != nil {
		launcher, ok := s.launcher.(continueLauncher)
		if !ok || in.Kind != KindWorker || in.Mode != ModeContinue {
			return nil, ErrInvalid
		}
		if err := launcher.PreflightContinue(ctx, *in.Startup); err != nil {
			return nil, err
		}
	} else if preflight, ok := s.launcher.(launcherPreflighter); ok && in.Kind != KindRebase {
		if err := preflight.Preflight(ctx, in.Kind); err != nil {
			return nil, err
		}
	}
	for _, w := range all {
		// Only reviews exclude other reviews; queue requests and Continue are independent.
		if in.Kind == KindReview && w.Kind == KindReview && w.Active() {
			return nil, ErrBusy
		}
	}
	if in.MechanicalOnly && (in.Kind != KindRebase || s.rebaser == nil) {
		return nil, ErrInvalid
	}
	comments := in.CommentIDs
	if in.Kind != KindWorker || in.Mode == ModeContinue {
		comments = []string{""}
	}
	if len(comments) == 0 {
		return nil, ErrInvalid
	}
	if (Work{Kind: in.Kind, Mode: in.Mode}).InQueue() {
		if in.Kind == KindWorker {
			for _, id := range comments {
				if _, err := s.addressComment(ctx, pr.ID, id); err != nil {
					return nil, err
				}
			}
		}
		if in.RequestID == "" {
			in.RequestID = pullrequestlifecycle.RequestID(ctx)
		}
		jobs := make([]Work, 0, len(comments))
		for _, id := range comments {
			jobs = append(jobs, Work{ID: newID("prw_"), RequestID: in.RequestID, RequestedBaseCommit: in.ExpectedBaseCommit, PullRequestID: pr.ID, CommentID: id, Kind: in.Kind, Mode: in.Mode, Prompt: in.Prompt, MechanicalOnly: in.MechanicalOnly, TargetBaseCommit: in.ExpectedBaseCommit, Status: StatusQueued, CreatedAt: time.Now().UTC()})
		}
		if err := s.store.CreateBatch(ctx, jobs); err != nil {
			return nil, err
		}
		s.notifyDispatcher()
		return jobs, nil
	}
	result := make([]Work, 0, len(comments))
	now := time.Now().UTC()
	for _, commentID := range comments {
		w := Work{ID: newID("prw_"), PullRequestID: pr.ID, CommentID: commentID, Kind: in.Kind, Mode: in.Mode, Status: StatusQueued, BaseBranch: pr.BaseBranch, BaseCommit: pr.BaseCommit, HeadBranch: pr.HeadBranch, HeadCommit: pr.HeadCommit, BaseHeadCommit: pr.HeadCommit, CreatedAt: now}
		if w.Kind == KindReview {
			if s.reviewChanges == nil {
				return nil, fmt.Errorf("capture review input: %w", ErrInvalid)
			}
			base := pr.DiffBaseCommit
			if base == "" {
				base = pr.BaseCommit
			}
			input, captureErr := s.reviewChanges.Capture(ctx, base, pr.HeadCommit)
			if captureErr != nil {
				return nil, fmt.Errorf("capture review input: %w", captureErr)
			}
			if input == nil || input.DiffBaseCommit == "" || input.HeadCommit == "" || input.PatchFingerprint == "" || input.MessagesFingerprint == "" {
				return nil, fmt.Errorf("capture review input: %w", ErrInvalid)
			}
			w.Provenance = input
			w.HeadCommit, w.BaseHeadCommit = input.HeadCommit, input.HeadCommit
			pr.DiffBaseCommit, pr.HeadCommit = input.DiffBaseCommit, input.HeadCommit
		}
		if err = s.store.Create(ctx, w); err != nil {
			return nil, err
		}
		result = append(result, w)
	}
	// Launch only the first item. Complete starts remain serialized by the coordinator.
	w := result[0]
	var sessionID string
	launchStarted := time.Now()
	if in.Startup != nil {
		sessionID, err = s.launcher.(continueLauncher).StartContinue(ctx, pr, w, in.Prompt, *in.Startup)
	} else {
		sessionID, err = s.launcher.Start(ctx, pr, w, in.Prompt)
	}
	slog.DebugContext(ctx, "Work launch completed", "pull_request_id", pr.ID, "request_id", pullrequestlifecycle.RequestID(ctx), "work_id", w.ID, "mode", w.Mode, "duration", time.Since(launchStarted), "error", err)
	if err != nil {
		w.Status = StatusFailed
		w.Error = err.Error()
		w.CompletedAt = &now
		_ = s.store.Update(ctx, w)
		return nil, err
	}
	w.SessionID = sessionID
	w.Status = StatusRunning
	w.StartedAt = &now
	if err = s.store.Update(ctx, w); err != nil {
		return nil, err
	}
	result[0] = s.withReviewFreshness(ctx, w)
	return result, nil
}
func (s *Service) List(ctx context.Context, pr string, kind Kind) ([]Work, error) {
	all, e := s.store.List(ctx, pr)
	if e != nil {
		return nil, e
	}
	out := all[:0]
	for _, w := range all {
		if w.Kind == kind {
			out = append(out, w)
		}
	}
	return s.withReviewsFreshness(ctx, out), nil
}
func (s *Service) WorkForSession(ctx context.Context, sessionID string) (Work, error) {
	all, e := s.store.List(ctx, "")
	if e != nil {
		return Work{}, e
	}
	for _, w := range all {
		if w.SessionID == sessionID {
			return w, nil
		}
	}
	return Work{}, ErrNotFound
}

// PublishContinueSession publishes an exact, active Continue worker without
// completing it. Continue workers are long-lived and may publish repeatedly.
func (s *Service) PublishContinueSession(ctx context.Context, sessionID string, options PublicationOptions) (Work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.workForSessionLocked(ctx, sessionID)
	if err != nil {
		return Work{}, err
	}
	return s.publishContinueLocked(ctx, w, options)
}

// PublishLatestContinue publishes the most recently created Continue worker
// for a pull request.
func (s *Service) PublishLatestContinue(ctx context.Context, pullRequestID, requestID string) (Work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.store.List(ctx, pullRequestID)
	if err != nil {
		return Work{}, err
	}
	var latest *Work
	for index := range all {
		candidate := &all[index]
		if requestID != "" && candidate.PublicationOperationID == requestID {
			return *candidate, nil
		}
		if candidate.Kind != KindWorker || candidate.Mode != ModeContinue {
			continue
		}
		if latest == nil || candidate.CreatedAt.After(latest.CreatedAt) || (candidate.CreatedAt.Equal(latest.CreatedAt) && candidate.ID > latest.ID) {
			latest = candidate
		}
	}
	if latest == nil {
		return Work{}, ErrNotFound
	}
	return s.publishContinueLocked(ctx, *latest, PublicationOptions{RequestID: requestID})
}

func (s *Service) workForSessionLocked(ctx context.Context, sessionID string) (Work, error) {
	all, err := s.store.List(ctx, "")
	if err != nil {
		return Work{}, err
	}
	for _, w := range all {
		if w.SessionID == sessionID {
			return w, nil
		}
	}
	return Work{}, ErrNotFound
}

func (s *Service) publishContinueLocked(ctx context.Context, w Work, options PublicationOptions) (result Work, resultErr error) {
	started := time.Now()
	operationID := options.RequestID
	defer func() {
		slog.DebugContext(ctx, "Manual publication completed", "operation_id", operationID, "duration", time.Since(started), "error", resultErr)
	}()
	if w.Kind != KindWorker || w.Mode != ModeContinue {
		return Work{}, ErrInvalid
	}
	pr, ok := s.catalog.PullRequest(w.PullRequestID)
	if !ok {
		return Work{}, ErrPullRequestNotFound
	}
	if !pr.Active {
		return Work{}, ErrPullRequestInactive
	}
	if w.Status != StatusRunning && w.Status != StatusWaiting {
		return Work{}, ErrInvalid
	}
	expectedHead := strings.TrimSpace(pr.HeadCommit)
	if expectedHead == "" || w.HeadBranch != pr.HeadBranch {
		return Work{}, ErrStaleHead
	}
	if s.publisher == nil || s.continueCommitter == nil {
		return Work{}, ErrInvalid
	}
	publication, err := s.publisher.Publish(ctx, w, options)
	if err != nil {
		return Work{}, fmt.Errorf("%w: %w", ErrPublish, err)
	}
	operationID = publication.OperationID
	expectedHead = strings.TrimSpace(publication.ExpectedHead)
	publishedHead := strings.TrimSpace(publication.HeadCommit)
	if publication.OperationID != "" && publication.OperationID == w.PublicationOperationID && publishedHead == w.ResultHeadCommit {
		return w, nil
	}
	w.PublicationOperationID = publication.OperationID
	if publishedHead == "" || (publishedHead == expectedHead && (!publication.CheckpointOnly || w.HeadCommit == publishedHead)) {
		return Work{}, publicationError(ErrNoNewCommit)
	}
	checkpointHead := expectedHead
	if publication.CheckpointOnly && publishedHead == expectedHead {
		// Reconcile the old worker checkpoint against the already-observed push.
		checkpointHead = w.HeadCommit
	}
	w.HeadCommit = publishedHead
	w.BaseHeadCommit = publishedHead
	w.ResultHeadCommit = publishedHead
	w.PublicationState = "published"
	checkpointStarted := time.Now()
	err = s.continueCommitter.CommitContinuePublication(ctx, w, checkpointHead, publishedHead)
	slog.DebugContext(ctx, "Publication checkpoint completed", "operation_id", publication.OperationID, "duration", time.Since(checkpointStarted), "error", err)
	if err != nil {
		return Work{}, err
	}
	return w, nil
}
func (s *Service) Complete(ctx context.Context, id, expectedHead string, c Completion) (Work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.store.Get(ctx, id)
	if err != nil {
		return Work{}, err
	}
	if w.Status == StatusCompleted {
		replyErr := s.deliverReply(ctx, &w)
		s.notifyDispatcher()
		return s.withReviewFreshness(ctx, w), replyErr
	}
	if !w.Active() || w.Status == StatusCancelling {
		return w, nil
	}
	if c.PullRequestID != "" && c.PullRequestID != w.PullRequestID {
		return Work{}, ErrInvalid
	}
	if c.HeadCommit != "" && c.HeadCommit != w.HeadCommit {
		return Work{}, ErrStaleHead
	}
	if w.HeadCommit != expectedHead {
		return Work{}, ErrStaleHead
	}
	pr, ok := s.catalog.PullRequest(w.PullRequestID)
	if !ok || !pr.Active || (w.Kind != KindReview && !completionHeadMatches(w, pr.HeadCommit)) {
		return Work{}, ErrStaleHead
	}
	if w.Kind == KindReview && len(c.Comments) > 0 && !w.ArtifactImported {
		if w.IsAssistedReview() && w.HeadCommit != pr.HeadCommit {
			return Work{}, ErrStaleHead
		}
		if s.commentDelivery == nil {
			return Work{}, ErrInvalid
		}
		if err = s.commentDelivery.DeliverReviewComments(ctx, w.PullRequestID, w.SessionID, w.ID, w.HeadCommit, c.Comments, w.IsAssistedReview()); err != nil {
			return Work{}, err
		}
		w.ArtifactImported = true
	}
	if w.Kind == KindWorker && strings.TrimSpace(c.ReplyBody) != "" && !w.ArtifactImported {
		if s.publisher != nil && w.PublicationState != "published" {
			publication, publishErr := s.publisher.Publish(ctx, w, PublicationOptions{})
			if publishErr != nil {
				return Work{}, publicationError(publishErr)
			}
			published := publication.HeadCommit
			w.PublicationOperationID = publication.OperationID
			if published == "" || (published == w.HeadCommit && w.PendingCompletion == nil) {
				return Work{}, publicationError(ErrNoNewCommit)
			}
			if c.ResultHeadCommit != "" && published != c.ResultHeadCommit {
				return Work{}, ErrStaleHead
			}
			w.ResultHeadCommit = published
			w.ReplyBody = strings.TrimSpace(c.ReplyBody)
			w.Summary = strings.TrimSpace(c.Summary)
			w.PublicationState = "published"
			if err = s.store.Update(ctx, w); err != nil {
				return Work{}, err
			}
		}
		if w.PublicationState == "published" {
			c.ResultHeadCommit = w.ResultHeadCommit
			if w.ReplyBody == "" {
				w.ReplyBody = strings.TrimSpace(c.ReplyBody)
				w.Summary = strings.TrimSpace(c.Summary)
				if err = s.store.Update(ctx, w); err != nil {
					return Work{}, err
				}
			}
			c.ReplyBody = w.ReplyBody
			c.Summary = w.Summary
		}
		if c.ResultHeadCommit == "" || (c.ResultHeadCommit == w.HeadCommit && w.PendingCompletion == nil) {
			return Work{}, ErrNoNewCommit
		}
	}
	if w.Kind == KindRebase && s.publisher != nil && w.PublicationState != "published" {
		publication, publishErr := s.publisher.Publish(ctx, w, PublicationOptions{})
		if publishErr != nil {
			return Work{}, publicationError(publishErr)
		}
		published := publication.HeadCommit
		w.PublicationOperationID = publication.OperationID
		if published == "" || published == w.HeadCommit {
			return Work{}, publicationError(ErrNoNewCommit)
		}
		if c.ResultHeadCommit != "" && published != c.ResultHeadCommit {
			return Work{}, ErrStaleHead
		}
		w.ResultHeadCommit = published
		w.ReplyBody = strings.TrimSpace(c.ReplyBody)
		w.Summary = strings.TrimSpace(c.Summary)
		w.PublicationState = "published"
		if err = s.store.Update(ctx, w); err != nil {
			return Work{}, err
		}
		w.ArtifactImported = true
	} else if w.Kind == KindRebase && w.PublicationState == "published" {
		c.ResultHeadCommit = w.ResultHeadCommit
		c.ReplyBody = w.ReplyBody
		c.Summary = w.Summary
		w.ArtifactImported = true
	}
	if w.Kind == KindRebase && (c.ResultHeadCommit == "" || c.ResultHeadCommit == w.HeadCommit) {
		return Work{}, ErrNoNewCommit
	}
	now := time.Now().UTC()
	w.Status = StatusCompleted
	w.Error = ""
	w.ResultHeadCommit = strings.TrimSpace(c.ResultHeadCommit)
	w.ReplyBody = strings.TrimSpace(c.ReplyBody)
	w.Summary = strings.TrimSpace(c.Summary)
	w.CompletedAt = &now
	if w.PublicationState == "published" && (w.Kind == KindWorker || w.Kind == KindRebase) {
		err = s.commitCompletedLocked(ctx, w, publicationSourceHead(w), w.ResultHeadCommit)
	} else {
		err = s.store.Update(ctx, w)
	}
	if err != nil {
		persisted, getErr := s.store.Get(ctx, w.ID)
		if getErr == nil {
			if persisted.Status == StatusCompleted {
				return persisted, err
			}
			return Work{}, err
		}
		return Work{}, errors.Join(err, getErr)
	}
	replyErr := s.deliverReply(ctx, &w)
	s.notifyDispatcher()
	return s.withReviewFreshness(ctx, w), replyErr
}

func (s *Service) commitCompletedLocked(ctx context.Context, w Work, expectedHead, publishedHead string) error {
	if s.completionCommitter == nil {
		return ErrInvalid
	}
	commit := CompletionCommit{Work: w, ExpectedSourceHead: expectedHead, ResultingHead: publishedHead}
	if w.Kind == KindRebase {
		commit.Rebase = &RebaseCompletionMetadata{TargetBaseCommit: w.TargetBaseCommit, TargetDiffBaseCommit: w.TargetDiffBaseCommit}
	}
	return s.completionCommitter.CommitCompletion(ctx, commit)
}

func (s *Service) deliverReply(ctx context.Context, w *Work) error {
	if w.Kind != KindWorker || strings.TrimSpace(w.ReplyBody) == "" {
		return nil
	}
	if s.commentDelivery == nil {
		return ErrInvalid
	}
	err := s.commentDelivery.DeliverReply(ctx, w.PullRequestID, w.CommentID, w.SessionID, w.ID, w.ResultHeadCommit, w.ReplyBody)
	if err == nil {
		w.ArtifactImported = true
	}
	return err
}

func publicationError(err error) error {
	if errors.Is(err, ErrStaleHead) {
		return ErrStaleHead
	}
	return fmt.Errorf("%w: %v", ErrPublish, err)
}

func completionHeadMatches(w Work, head string) bool {
	// Shared sync may observe a prior push before its atomic work checkpoint.
	// The publisher still verifies durable intent and the exact workspace result.
	if pending := w.PendingCompletion; w.IsAddress() && pending != nil && pending.PublicationAttempted && pending.Completion.ResultHeadCommit != "" && head == pending.Completion.ResultHeadCommit {
		return true
	}
	if head == publicationSourceHead(w) {
		return true
	}
	return w.PublicationState == "published" && w.ResultHeadCommit != "" && head == w.ResultHeadCommit
}

func (s *Service) Fail(ctx context.Context, id, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, e := s.store.Get(ctx, id)
	if e != nil {
		return e
	}
	if w.Active() && w.InQueue() {
		if handled, err := s.finishCheckpointLocked(ctx, &w); handled {
			if err != nil {
				return err
			}
			s.notifyDispatcher()
			return nil
		}
	}
	if !w.Active() || w.Status == StatusCancelling {
		return nil
	}
	now := time.Now().UTC()
	w.Status = StatusFailed
	w.Error = strings.TrimSpace(reason)
	w.CompletedAt = &now
	if e = s.store.Update(ctx, w); e != nil {
		return e
	}
	s.notifyDispatcher()
	return nil
}

// Wait keeps recoverable assisted work active without advancing its queue.
func (s *Service) Wait(ctx context.Context, id, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, e := s.store.Get(ctx, id)
	if e != nil {
		return e
	}
	if w.Active() && w.InQueue() {
		if handled, err := s.finishCheckpointLocked(ctx, &w); handled {
			if err != nil {
				return err
			}
			s.notifyDispatcher()
			return nil
		}
	}
	if !w.Active() || w.Status == StatusCancelling {
		return nil
	}
	w.Status = StatusWaiting
	w.Error = strings.TrimSpace(reason)
	w.CompletedAt = nil
	return s.store.Update(ctx, w)
}

// Stale terminally retires comment work whose pull request head moved.
func (s *Service) Stale(ctx context.Context, id, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, e := s.store.Get(ctx, id)
	if e != nil {
		return e
	}
	if w.Active() && w.InQueue() {
		if handled, err := s.finishCheckpointLocked(ctx, &w); handled {
			if err != nil {
				return err
			}
			s.notifyDispatcher()
			return nil
		}
	}
	if !w.Active() || w.Status == StatusCancelling {
		return nil
	}
	now := time.Now().UTC()
	w.Status = StatusStale
	w.Error = strings.TrimSpace(reason)
	w.CompletedAt = &now
	if e = s.store.Update(ctx, w); e != nil {
		return e
	}
	s.notifyDispatcher()
	return nil
}

func (s *Service) startNextLocked(ctx context.Context, prID string) error {
	all, err := s.store.List(ctx, prID)
	if err != nil {
		return err
	}
	for _, w := range all {
		if w.InQueue() && w.Active() && (w.Status != StatusQueued || w.PublicationState != "") {
			return nil
		}
	}
	for _, w := range all {
		if !w.InQueue() || w.Status != StatusQueued {
			continue
		}
		ctx := workStartupContext(ctx, w.RequestID)
		if err := ctx.Err(); err != nil {
			return err
		}
		executionErr, persistenceErr := s.prepareQueuedLocked(ctx, prID, &w)
		// An uncertain durable transition must retain the slot.
		if persistenceErr != nil {
			return fmt.Errorf("work %s request %s persistence: %w", w.ID, pullrequestlifecycle.RequestID(ctx), persistenceErr)
		}
		if executionErr != nil {
			if ctx.Err() == nil {
				slog.ErrorContext(ctx, "Queued work execution failed", "pull_request_id", prID, "work_id", w.ID, "request_id", pullrequestlifecycle.RequestID(ctx), "error", executionErr)
			}
			now := time.Now().UTC()
			w.Status, w.Error, w.CompletedAt = StatusFailed, executionErr.Error(), &now
			if errors.Is(executionErr, ErrReservationEnded) {
				w.Status, w.Error = StatusCancelled, ""
			}
			if err := s.settleReservedLocked(ctx, w); err != nil {
				return err
			}
			if err := s.store.Update(ctx, w); err != nil {
				return fmt.Errorf("work %s request %s failure persistence: %w", w.ID, pullrequestlifecycle.RequestID(ctx), err)
			}
		}
		if w.Active() {
			return nil
		}
	}
	return nil
}

// Execution errors retire a request; persistence errors stop dispatch.
func (s *Service) prepareQueuedLocked(ctx context.Context, prID string, w *Work) (error, error) {
	pr, ok := s.catalog.PullRequest(prID)
	if !ok || !pr.Active {
		return ErrPullRequestInactive, nil
	}
	if w.IsAddress() {
		resolved, err := s.addressComment(ctx, prID, w.CommentID)
		if err != nil {
			return err, nil
		}
		if resolved {
			return nil, s.skipReservedLocked(ctx, w)
		}
		if executionErr, persistenceErr := s.reserveQueuedLocked(ctx, pr, w); executionErr != nil || persistenceErr != nil {
			return executionErr, persistenceErr
		}
	}
	if err := s.catalog.PrepareWork(ctx, prID); err != nil {
		slog.ErrorContext(ctx, "Queued work synchronization failed", "pull_request_id", prID, "work_id", w.ID, "request_id", pullrequestlifecycle.RequestID(ctx), "holon_id", w.SessionID, "error", err)
		// Retain queued status, reservation, FIFO position, and empty user-facing errors.
		return nil, fmt.Errorf("synchronization: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pr, ok = s.catalog.PullRequest(prID)
	if !ok || !pr.Active {
		return ErrPullRequestInactive, nil
	}
	if w.Kind == KindRebase {
		return s.executeRebaseLocked(ctx, pr, w)
	}
	resolved, err := s.addressComment(ctx, prID, w.CommentID)
	if err != nil {
		return err, nil
	}
	if resolved {
		return nil, s.skipReservedLocked(ctx, w)
	}
	return s.launchQueuedLocked(ctx, pr, w)
}

func (s *Service) reserveQueuedLocked(ctx context.Context, pr PullRequest, w *Work) (error, error) {
	launcher, ok := s.launcher.(QueuedLauncher)
	if !ok {
		return nil, ErrInvalid
	}
	if w.SessionID == "" {
		w.SessionID = newID("holon_")
		if err := s.store.Update(ctx, *w); err != nil {
			return nil, err
		}
	}
	err := launcher.Reserve(ctx, pr, *w)
	if errors.Is(err, ErrReservationEnded) {
		return err, nil
	}
	return nil, err
}

func (s *Service) settleReservedLocked(ctx context.Context, w Work) error {
	if w.SessionID == "" {
		return nil
	}
	launcher, ok := s.launcher.(QueuedLauncher)
	if !ok {
		return ErrInvalid
	}
	return launcher.SettleReserved(ctx, w)
}

func (s *Service) skipReservedLocked(ctx context.Context, w *Work) error {
	now := time.Now().UTC()
	w.Status, w.Summary, w.CompletedAt = StatusSkipped, "Comment already resolved.", &now
	if err := s.settleReservedLocked(ctx, *w); err != nil {
		return err
	}
	return s.store.Update(ctx, *w)
}

func (s *Service) recordExecutionLocked(ctx context.Context, pr PullRequest, w *Work) error {
	now := time.Now().UTC()
	w.BaseBranch, w.BaseCommit = pr.BaseBranch, pr.BaseCommit
	w.HeadBranch, w.HeadCommit, w.BaseHeadCommit = pr.HeadBranch, pr.HeadCommit, pr.HeadCommit
	w.Status = StatusRunning
	if w.StartedAt == nil {
		w.StartedAt = &now
	}
	return s.store.Update(ctx, *w)
}

func (s *Service) launchQueuedLocked(ctx context.Context, pr PullRequest, w *Work) (error, error) {
	if err := s.recordExecutionLocked(ctx, pr, w); err != nil {
		return nil, err
	}
	if preflight, ok := s.launcher.(launcherPreflighter); ok && w.Kind == KindRebase {
		if err := preflight.Preflight(ctx, w.Kind); err != nil {
			return err, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	launchStarted := time.Now()
	executionErr, persistenceErr := s.launcher.(QueuedLauncher).StartReserved(ctx, pr, *w, w.Prompt)
	slog.DebugContext(ctx, "Queued work launch completed", "pull_request_id", pr.ID, "request_id", pullrequestlifecycle.RequestID(ctx), "work_id", w.ID, "holon_id", w.SessionID, "kind", w.Kind, "duration", time.Since(launchStarted), "error", errors.Join(executionErr, persistenceErr))
	return executionErr, persistenceErr
}

func (s *Service) executeRebaseLocked(ctx context.Context, pr PullRequest, w *Work) (error, error) {
	if err := s.recordExecutionLocked(ctx, pr, w); err != nil {
		return nil, err
	}
	if s.rebaser == nil {
		return ErrInvalid, nil
	}
	prepared, err := s.rebaser.Rebase(ctx, pr, w.TargetBaseCommit, w.RequestID)
	if err != nil {
		return err, nil
	}
	if prepared.PullRequest.ID != "" {
		pr = prepared.PullRequest
	}
	w.BaseBranch, w.BaseCommit = pr.BaseBranch, pr.BaseCommit
	w.HeadBranch, w.HeadCommit, w.BaseHeadCommit = pr.HeadBranch, pr.HeadCommit, pr.HeadCommit
	w.TargetBaseCommit, w.TargetDiffBaseCommit = prepared.TargetBaseCommit, prepared.TargetDiffBaseCommit
	if !prepared.Conflicts {
		w.ResultHeadCommit, w.PublicationState = prepared.HeadCommit, "published"
		w.PublicationOperationID = prepared.OperationID
		if err := s.store.Update(ctx, *w); err != nil {
			return nil, err
		}
		_, err := s.finishCheckpointLocked(ctx, w)
		return nil, err
	}
	if w.MechanicalOnly {
		return ErrRebaseConflicts, nil
	}
	if executionErr, persistenceErr := s.reserveQueuedLocked(ctx, pr, w); executionErr != nil || persistenceErr != nil {
		return executionErr, persistenceErr
	}
	return s.launchQueuedLocked(ctx, pr, w)
}

// finishCheckpointLocked finalizes already-published work without publishing again.
// Even an incomplete checkpoint prevents cancellation, failure, or queue advancement.
func (s *Service) finishCheckpointLocked(ctx context.Context, w *Work) (bool, error) {
	if w.PublicationState != "published" {
		return false, nil
	}
	if w.ResultHeadCommit == "" || (w.Kind == KindWorker && strings.TrimSpace(w.ReplyBody) == "") {
		return true, ErrPublish
	}
	completed := *w
	now := time.Now().UTC()
	completed.Status, completed.Error, completed.CompletedAt = StatusCompleted, "", &now
	if err := s.commitCompletedLocked(ctx, completed, publicationSourceHead(*w), w.ResultHeadCommit); err != nil {
		return true, err
	}
	*w = completed
	return true, s.deliverReply(ctx, w)
}

func (s *Service) Recover(ctx context.Context, restoringSessions ...string) error {
	restoring := make(map[string]bool, len(restoringSessions))
	for _, id := range restoringSessions {
		restoring[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.store.List(ctx, "")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var recoveryErr error
	for _, w := range all {
		if w.Status == StatusCompleted {
			recoveryErr = errors.Join(recoveryErr, s.deliverReply(ctx, &w))
			continue
		}
		if w.Kind == KindWorker && w.Mode == ModeContinue && (w.Status == StatusRunning || w.Status == StatusWaiting) {
			continue
		}
		if !w.Active() {
			continue
		}
		if w.InQueue() {
			if handled, err := s.finishCheckpointLocked(ctx, &w); handled {
				recoveryErr = errors.Join(recoveryErr, err)
				continue
			}
		}
		if w.IsAddress() && w.PendingCompletion != nil && w.Status != StatusCancelling {
			continue
		}
		if w.IsAssistedReview() && w.SessionID != "" && (w.Status == StatusRunning || w.Status == StatusWaiting) {
			continue
		}
		if restoring[w.SessionID] && (w.Status == StatusRunning || w.Status == StatusWaiting) {
			continue
		}
		// Synchronization reservations have not begun execution and remain retryable.
		if w.InQueue() && w.Status == StatusQueued {
			continue
		}
		// PR-wide cancellation also retires reviews and long-lived Continue sessions.
		recoverSession := (w.InQueue() || w.Status == StatusCancelling) && w.SessionID != "" && s.runtime != nil
		if recoverSession {
			if err := s.runtime.Recover(ctx, w.SessionID); err != nil {
				return errors.Join(recoveryErr, err)
			}
		}
		// Reviews launch directly from queued; a restart before launch finalization
		// leaves no durable session to resume and must not block future reviews.
		interruptedReview := w.Kind == KindReview && w.Status == StatusQueued
		if interruptedReview || w.Status == StatusRunning || w.Status == StatusWaiting || w.Status == StatusCancelling {
			if recoverSession {
				state, stateErr := s.runtime.State(ctx, w.SessionID)
				if stateErr != nil {
					return errors.Join(recoveryErr, stateErr)
				}
				if state == StatusRunning || state == StatusWaiting || state == StatusCancelling {
					continue
				}
			}
			if w.Status == StatusCancelling {
				w.Status = StatusCancelled
			} else {
				w.Status = StatusFailed
			}
			w.Error = "Holark restarted before this work completed."
			w.CompletedAt = &now
			if err = s.store.Update(ctx, w); err != nil {
				return errors.Join(recoveryErr, err)
			}
		}
	}
	s.notifyDispatcher()
	return recoveryErr
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func publicationSourceHead(w Work) string {
	if w.PublicationTargetCommit != "" {
		return w.PublicationTargetCommit
	}
	return w.HeadCommit
}
