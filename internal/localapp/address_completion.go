package localapp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

// The coordinator serializes completion events without holding a domain lock
// across native fork launch. Work and holon reservations remain durable.
type addressCompletionCoordinator struct {
	mu     sync.Mutex
	holons *holons.Service
	work   *pullrequestwork.Service
	rebase *rebaseAgentCoordinator
	agents localAgentRuntime
}

func (c *addressCompletionCoordinator) Begin(ctx context.Context, h holons.Holon, agentID string, completion pullrequestwork.Completion) (result pullRequestWorkCompletionDisposition, err error) {
	defer c.holons.BeginFinalization(h.ID)()
	c.mu.Lock()
	defer func() {
		c.mu.Unlock()
		err = errors.Join(err, c.rebase.closePending(ctx, h.ID))
	}()
	w, err := c.work.WorkForSession(ctx, h.ID)
	if err != nil {
		return pullRequestWorkCompletionDisposition{}, err
	}
	w, created, err := c.work.BeginAddressCompletion(ctx, w.ID, agentID, completion)
	if err != nil || !created {
		return pullRequestWorkCompletionDisposition{}, err
	}
	return c.advance(ctx, h.ID, false)
}
func (c *addressCompletionCoordinator) advance(ctx context.Context, id string, explicit bool) (pullRequestWorkCompletionDisposition, error) {
	for {
		// Event snapshots may predate another Rebase completion or reservation.
		h, err := c.holons.Get(ctx, id)
		if err != nil {
			return pullRequestWorkCompletionDisposition{}, err
		}
		w, err := c.work.WorkForSession(ctx, id)
		if err != nil {
			return pullRequestWorkCompletionDisposition{}, err
		}
		if !w.Active() || w.Status == pullrequestwork.StatusCancelling || w.PendingCompletion == nil {
			return pullRequestWorkCompletionDisposition{AlreadyHandled: true}, nil
		}
		if w.PublicationState == "published" {
			completed, err := c.work.Complete(ctx, w.ID, w.HeadCommit, w.PendingCompletion.Completion)
			if err != nil && completed.Status != pullrequestwork.StatusCompleted {
				return c.wait(ctx, w, err)
			}
			return c.finish(ctx, h, w, err)
		}
		pending := w.PendingCompletion
		if h.RebaseAttempt.Active() {
			attempt := h.RebaseAttempt
			if explicit && !h.RebaseNeedsSynchronization() && attempt.Progress != "agent_reserved" {
				return pullRequestWorkCompletionDisposition{}, holons.ErrRebaseActive
			}
			if pending.RebaseAttemptID != "" && pending.RebaseAttemptID != attempt.ID && pending.State != "launching" {
				return c.wait(ctx, w, holons.ErrRebaseActive)
			}
			if pending.State == "retry" && !explicit {
				return pullRequestWorkCompletionDisposition{AlreadyHandled: true}, nil
			}
			if explicit {
				w, err = c.work.ReserveAddressAttempt(ctx, w.ID, attempt.TargetCommit, true)
				if err != nil {
					return pullRequestWorkCompletionDisposition{}, err
				}
				explicit = false
			}
			if h.RebaseNeedsSynchronization() || attempt.Progress == "agent_reserved" {
				created, resumeErr := c.rebase.synchronize(ctx, h.ID, pending.SourceAgentID, nil, func(created holons.Holon) error {
					var bindErr error
					w, bindErr = c.work.BindAddressRebase(ctx, w.ID, created.RebaseAttempt.AgentID, created.RebaseAttempt.ID)
					return bindErr
				})
				if errors.Is(resumeErr, holons.ErrRebaseRequired) {
					continue
				}
				if resumeErr != nil {
					return c.launchFailed(ctx, w, created, resumeErr)
				}
				if created.RebaseAttempt.State == "succeeded" {
					continue
				}
			} else if pending.State == "launching" {
				if _, err = c.work.BindAddressRebase(ctx, w.ID, attempt.AgentID, attempt.ID); err != nil {
					return pullRequestWorkCompletionDisposition{}, err
				}
			}
			return pullRequestWorkCompletionDisposition{AlreadyHandled: true}, nil
		}
		// Git may have completed before the Address binding was persisted.
		if (pending.State == "launching" || pending.State == "retry") && h.RebaseAttempt != nil && h.RebaseAttempt.State == "succeeded" && h.RebaseAttempt.TargetCommit == pending.AttemptTarget && h.RebaseAttempt.StartHeadCommit == pending.Completion.ResultHeadCommit {
			w, err = c.work.BindAddressRebase(ctx, w.ID, h.RebaseAttempt.AgentID, h.RebaseAttempt.ID)
			if err != nil {
				return pullRequestWorkCompletionDisposition{}, err
			}
			pending = w.PendingCompletion
		}
		if pending.State == "retry" && !explicit && !pending.PublicationAttempted {
			return pullRequestWorkCompletionDisposition{AlreadyHandled: true}, nil
		}
		if pending.State == "rebasing" {
			attempt := h.RebaseAttempt
			if attempt == nil || (pending.RebaseAttemptID != "" && pending.RebaseAttemptID != attempt.ID) || (pending.RebaseAgentID != "" && pending.RebaseAgentID != attempt.AgentID) {
				return c.wait(ctx, w, holons.ErrRebaseActive)
			}
			if attempt.State == "failed" {
				return c.launchFailed(ctx, w, h, errors.New(attempt.FailureReason))
			}
			if attempt.State != "succeeded" {
				return c.wait(ctx, w, holons.ErrRebaseActive)
			}
			w, err = c.work.SetAddressCompletionState(ctx, w.ID, "pending", attempt.TargetCommit, attempt.ResultHeadCommit, "")
			if err != nil {
				return pullRequestWorkCompletionDisposition{}, err
			}
			pending = w.PendingCompletion
		}
		r, err := c.rebase.PreparePublication(ctx, h.ID)
		if err != nil {
			return c.wait(ctx, w, err)
		}
		if r.Reason != "" {
			return c.wait(ctx, w, errors.New(r.Reason))
		}
		if r.TargetBranch != w.HeadBranch {
			return c.wait(ctx, w, pullrequestwork.ErrStaleHead)
		}
		expectedResult := pending.Completion.ResultHeadCommit
		if r.WorkspaceHead != expectedResult {
			return c.wait(ctx, w, errors.New("The workspace changed after completion; review the pending result before retrying."))
		}
		recoveringPush := (pending.State == "ready" || pending.PublicationAttempted) && w.PublicationTargetCommit != "" && r.TargetCommit == expectedResult
		if pending.State == "retry" && !explicit && !recoveringPush {
			return pullRequestWorkCompletionDisposition{AlreadyHandled: true}, nil
		}
		if !recoveringPush {
			w, err = c.work.ReserveAddressAttempt(ctx, w.ID, r.TargetCommit, explicit)
			if errors.Is(err, pullrequestwork.ErrAddressRetriesExhausted) {
				return pullRequestWorkCompletionDisposition{}, nil
			}
			if err != nil {
				return pullRequestWorkCompletionDisposition{}, err
			}
		}
		explicit = false
		if r.RebaseRequired {
			if !r.RebaseAvailable {
				return c.wait(ctx, w, holons.ErrPublicationUnavailable)
			}
			w, err = c.work.SetAddressCompletionState(ctx, w.ID, "launching", r.TargetCommit, "", "")
			if err != nil {
				return pullRequestWorkCompletionDisposition{}, err
			}
			created, launchErr := c.rebase.synchronize(ctx, h.ID, pending.SourceAgentID, &r, func(created holons.Holon) error {
				var bindErr error
				w, bindErr = c.work.BindAddressRebase(ctx, w.ID, created.RebaseAttempt.AgentID, created.RebaseAttempt.ID)
				return bindErr
			})
			if errors.Is(launchErr, holons.ErrRebaseRequired) {
				latest, readErr := c.rebase.PreparePublication(ctx, h.ID)
				if readErr == nil && latest.Reason == "" && latest.WorkspaceHead == r.WorkspaceHead && latest.TargetCommit != r.TargetCommit {
					continue
				}
			}
			if launchErr != nil {
				return c.launchFailed(ctx, w, created, launchErr)
			}
			if created.RebaseAttempt.State == "succeeded" {
				continue
			}
			return pullRequestWorkCompletionDisposition{}, nil
		}
		if recoveringPush {
			r.TargetCommit = w.PublicationTargetCommit
		}
		p, ok := c.rebase.target(h)
		if !ok {
			return c.wait(ctx, w, pullrequestwork.ErrPullRequestNotFound)
		}
		if p.HeadBranch != r.TargetBranch || p.HeadBranch != w.HeadBranch {
			return c.wait(ctx, w, pullrequestwork.ErrStaleHead)
		}
		if !recoveringPush && (p.HeadCommit != r.TargetCommit || publicationTargetVersion(p) != r.TargetVersion) {
			return c.wait(ctx, w, pullrequestwork.ErrStaleHead)
		}
		w, err = c.work.SetAddressCompletionState(ctx, w.ID, "ready", r.TargetCommit, r.WorkspaceHead, "")
		if err != nil {
			return pullRequestWorkCompletionDisposition{}, err
		}
		completed, err := c.work.Complete(ctx, w.ID, w.HeadCommit, w.PendingCompletion.Completion)
		if err != nil && completed.Status != pullrequestwork.StatusCompleted {
			if errors.Is(err, pullrequestwork.ErrStaleHead) {
				latest, readErr := c.rebase.PreparePublication(ctx, h.ID)
				if readErr == nil && latest.Reason == "" && latest.WorkspaceHead == r.WorkspaceHead && latest.TargetCommit != r.TargetCommit {
					continue
				}
			}
			return c.wait(ctx, w, err)
		}
		return c.finish(ctx, h, w, err)
	}
}

func (c *addressCompletionCoordinator) launchFailed(ctx context.Context, w pullrequestwork.Work, h holons.Holon, err error) (pullRequestWorkCompletionDisposition, error) {
	// Only a persisted terminal agent failure releases the queue slot. Preflight
	// errors (including unavailable harnesses) retain the saved completion.
	if h.RebaseAttempt != nil {
		latest, getErr := c.holons.Get(ctx, h.ID)
		if getErr != nil {
			return pullRequestWorkCompletionDisposition{}, errors.Join(err, getErr)
		}
		if latest.RebaseAttempt.Active() && (latest.RebaseAttempt.Progress == "reserved" || latest.RebaseAttempt.Progress == "executing" || latest.RebaseAttempt.Progress == "agent_reserved") && !errors.Is(err, holons.ErrPublicationUnavailable) {
			// A checkpoint failed before launch/binding. Keep the durable
			// launching state so restart can finish this same reservation.
			return pullRequestWorkCompletionDisposition{}, err
		}
		if latest.RebaseAttempt != nil && latest.RebaseAttempt.State == "failed" && latest.RebaseAttempt.AgentID != "" && latest.RebaseAttempt.AgentID == w.PendingCompletion.RebaseAgentID {
			reason := "Holark could not complete the Rebase agent: " + err.Error()
			return pullRequestWorkCompletionDisposition{Terminal: true, Reason: reason}, c.work.Fail(ctx, w.ID, reason)
		}
	}
	return c.wait(ctx, w, err)
}
func (c *addressCompletionCoordinator) wait(ctx context.Context, w pullrequestwork.Work, err error) (pullRequestWorkCompletionDisposition, error) {
	_, stateErr := c.work.SetAddressCompletionState(ctx, w.ID, "retry", "", "", err.Error())
	return pullRequestWorkCompletionDisposition{}, stateErr
}
func (c *addressCompletionCoordinator) Rebased(ctx context.Context, h holons.Holon) (pullRequestWorkCompletionDisposition, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w, err := c.work.WorkForSession(ctx, h.ID)
	if errors.Is(err, pullrequestwork.ErrNotFound) {
		return pullRequestWorkCompletionDisposition{}, nil
	}
	if err != nil {
		return pullRequestWorkCompletionDisposition{}, err
	}
	if !w.Active() || w.Status == pullrequestwork.StatusCancelling || w.PendingCompletion == nil || w.PendingCompletion.State != "rebasing" {
		return pullRequestWorkCompletionDisposition{AlreadyHandled: true}, nil
	}
	defer c.holons.BeginFinalization(h.ID)()
	latest, err := c.holons.Get(ctx, h.ID)
	if err != nil {
		return pullRequestWorkCompletionDisposition{}, err
	}
	if h.RebaseAttempt == nil || latest.RebaseAttempt == nil || h.RebaseAttempt.ID != latest.RebaseAttempt.ID || h.RebaseAttempt.AgentID != latest.RebaseAttempt.AgentID || latest.RebaseAttempt.State != "succeeded" {
		return pullRequestWorkCompletionDisposition{AlreadyHandled: true}, nil
	}
	return c.advance(ctx, h.ID, false)
}
func (c *addressCompletionCoordinator) Retry(ctx context.Context, id string) (err error) {
	defer c.holons.BeginFinalization(id)()
	c.mu.Lock()
	defer func() {
		c.mu.Unlock()
		err = errors.Join(err, c.rebase.closePending(ctx, id))
	}()
	_, err = c.advance(ctx, id, true)
	return err
}
func (c *addressCompletionCoordinator) Recover(ctx context.Context) error {
	var releases []func()
	c.mu.Lock()
	all, err := c.holons.List(ctx)
	defer func() {
		c.mu.Unlock()
		for _, h := range all {
			if closeErr := c.rebase.closePending(ctx, h.ID); closeErr != nil {
				log.Printf("recover Rebase tab holon_id=%s: %v", h.ID, closeErr)
			}
		}
		for _, release := range releases {
			release()
		}
	}()
	if err != nil {
		return err
	}
	for _, h := range all {
		w, workErr := c.work.WorkForSession(ctx, h.ID)
		if workErr != nil && !errors.Is(workErr, pullrequestwork.ErrNotFound) {
			return workErr
		}
		pendingAddress := workErr == nil && w.IsAddress() && w.PendingCompletion != nil && w.Active() && w.Status != pullrequestwork.StatusCancelling
		if pendingAddress {
			releases = append(releases, c.holons.BeginFinalization(h.ID))
		}
		if h.RebaseAttempt.Active() {
			a := h.RebaseAttempt
			if !pendingAddress {
				if h.Status == holons.StatusCancelling || h.Status == holons.StatusCancelled {
					if err = c.holons.FailRebase(ctx, h.ID, a.AgentID, "Rebase was cancelled."); err != nil {
						return err
					}
					continue
				}
				if h.RebaseNeedsSynchronization() || a.Progress == "agent_reserved" {
					// An individual workspace's error must not block other recovery.
					_, _ = c.rebase.synchronize(ctx, h.ID, a.SourceAgentID, nil, nil)
					continue
				}
			}
			// A saved task completion may precede verification. Reconcile it
			// before runtime recovery imports that turn as baseline history.
			agent := h.AgentSession(a.AgentID)
			err = nil
			if agent.Status == string(holons.StatusFailed) || agent.Status == string(holons.StatusCancelled) {
				err = c.holons.FailRebase(ctx, h.ID, agent.ID, agent.Reason)
			} else if h.RebaseCompletionReady(agent.ID) {
				_, err = c.holons.VerifyRebase(ctx, h.ID, agent.ID)
			}
			if err != nil && !errors.Is(err, holons.ErrRebaseActive) {
				return err
			}
		}
		if pendingAddress {
			if _, err = c.advance(ctx, h.ID, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// AgentStopped settles a terminal Rebase failure even if another tab remains
// open. Cancellation of the whole Address request is settled by its queue.
func (c *addressCompletionCoordinator) AgentStopped(ctx context.Context, h holons.Holon, agentID string) error {
	// Completing work can synchronously fail the next queued worker while this
	// coordinator is locked. Unrelated agents must not reenter that lock.
	if h.RebaseAttempt == nil || h.RebaseAttempt.AgentID != agentID {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var err error
	h, err = c.holons.Get(ctx, h.ID)
	if err != nil {
		return err
	}
	if h.RebaseAttempt == nil || h.RebaseAttempt.AgentID != agentID || h.RebaseAttempt.State == "succeeded" {
		return nil
	}
	agent := h.AgentSession(agentID)
	if !holons.IsTerminal(holons.Status(agent.Status)) && agent.ClosedAt == nil {
		return nil
	}
	w, workErr := c.work.WorkForSession(ctx, h.ID)
	if workErr != nil && !errors.Is(workErr, pullrequestwork.ErrNotFound) {
		return workErr
	}
	pendingAddress := workErr == nil && w.IsAddress() && w.PendingCompletion != nil
	if pendingAddress && agent.Status == string(holons.StatusCompleted) && agent.ClosedAt == nil && (h.RebaseAttempt.State == "waiting" || agent.InputState == string(protocol.InputUserRequired)) {
		// A normal Address exit with unresolved synchronization is a
		// clarification wait. Keep its reservation and queue slot.
		return nil
	}
	reason := agent.Reason
	if reason == "" {
		reason = "The Rebase agent stopped before synchronization completed."
	}
	if err := c.holons.FailRebase(ctx, h.ID, agentID, reason); err != nil {
		return err
	}
	if !pendingAddress || w.Status == pullrequestwork.StatusCancelling {
		return nil
	}
	return c.work.Fail(ctx, w.ID, reason)
}

func (c *addressCompletionCoordinator) StartRebase(ctx context.Context, id, source string) (result holons.Holon, err error) {
	release := func() {}
	defer func() { release() }()
	c.mu.Lock()
	defer func() {
		c.mu.Unlock()
		err = errors.Join(err, c.rebase.closePending(ctx, id))
	}()
	w, err := c.work.WorkForSession(ctx, id)
	if err != nil && !errors.Is(err, pullrequestwork.ErrNotFound) {
		return holons.Holon{}, err
	}
	if err == nil && w.IsAddress() && w.PendingCompletion == nil {
		return holons.Holon{}, holons.ErrPublicationUnavailable
	}
	pending := err == nil && w.IsAddress() && w.PendingCompletion != nil
	if pending {
		release = c.holons.BeginFinalization(id)
		if _, err = c.advance(ctx, id, true); err != nil {
			return holons.Holon{}, err
		}
		latest, readErr := c.work.WorkForSession(ctx, id)
		if readErr != nil {
			return holons.Holon{}, readErr
		}
		if latest.PendingCompletion != nil && latest.PendingCompletion.State == "retry" && latest.Error != "" {
			return holons.Holon{}, fmt.Errorf("%w: %s", holons.ErrPublicationUnavailable, latest.Error)
		}
		return c.holons.Get(ctx, id)
	}
	return c.rebase.CreateRebaseAgent(ctx, id, source)
}

func (c *addressCompletionCoordinator) finish(ctx context.Context, h holons.Holon, w pullrequestwork.Work, completionErr error) (pullRequestWorkCompletionDisposition, error) {
	// Stop only the agents that owned this completion. Other tabs remain usable.
	ids := map[string]bool{w.PendingCompletion.SourceAgentID: true}
	// Address owns its Rebase agent until publication completes; its tabs do
	// not participate in user-directed Commit/Rebase automatic closure.
	if h.RebaseAttempt != nil {
		ids[h.RebaseAttempt.AgentID] = true
	}
	resultErr := errors.Join(completionErr, removePullRequestWorkArtifact(h, w.Kind))
	latest, getErr := c.holons.Get(ctx, h.ID)
	resultErr = errors.Join(resultErr, getErr)
	if getErr == nil {
		for _, a := range latest.AgentSessions {
			if !ids[a.ID] || a.ClosedAt != nil || holons.IsTerminal(holons.Status(a.Status)) {
				continue
			}
			_, statusErr := c.holons.SetAgentSessionStatus(ctx, h.ID, a.ID, holons.StatusExpired, "", a.ResumeTarget)
			resultErr = errors.Join(resultErr, statusErr)
			if c.agents != nil {
				resultErr = errors.Join(resultErr, c.agents.Cancel(ctx, h.ID, a.ID))
			}
		}
	}
	return pullRequestWorkCompletionDisposition{Terminal: true, AlreadyHandled: true}, resultErr
}
