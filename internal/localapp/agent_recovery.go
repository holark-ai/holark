package localapp

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/protocol"
)

func (s *terminalHolonService) restoreAgents(ctx context.Context) {
	all, err := s.List(ctx)
	if err != nil {
		log.Printf("list interrupted agents: %v", err)
		return
	}
	for _, h := range all {
		for _, a := range h.AgentSessions {
			if ctx.Err() != nil {
				return
			}
			if h.RebaseAttempt.ClosurePending(a.ID) || (a.ClosedAt == nil && (a.CommitClosePending() || a.Status == string(holons.StatusRestoring))) {
				if _, err = s.restoreAgent(ctx, h.ID, a.ID); err != nil {
					log.Printf("restore agent holon_id=%s agent_id=%s: %v", h.ID, a.ID, err)
				}
			}
		}
	}
}

func (s *terminalHolonService) restoreAgent(ctx context.Context, id, aid string) (result holons.Holon, err error) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	return s.restoreAgentLocked(ctx, id, aid, false)
}

// recoveryMu covers preparation and launch as well as pending Rebase cleanup.
func (s *terminalHolonService) restoreAgentLocked(ctx context.Context, id, aid string, explicit bool) (result holons.Holon, err error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return current, err
	}
	// Closing is already authorized by the saved HEAD verification. Retry it
	// even if shutdown left the agent cancelling or startup settled it cancelled.
	if current.AgentSession(aid).CommitClosePending() {
		return s.closeAgentSession(ctx, id, aid)
	}
	if !explicit && current.RebaseAttempt.ClosurePending(aid) {
		current, err = s.Service.PrepareRebaseClosure(ctx, id)
		if err != nil {
			return current, err
		}
		if current.RebaseAttempt.ClosurePending(aid) {
			return s.closeRebaseAgent(ctx, id, aid)
		}
	}
	if !explicit && current.RebaseAttempt.Active() && current.RebaseCompletionReady(aid) {
		verified, verifyErr := s.Service.VerifyRebase(ctx, id, aid)
		if verifyErr == nil && verified.RebaseAttempt.ClosurePending(aid) {
			return s.closeRebaseAgent(ctx, id, aid)
		}
		if verifyErr != nil && !errors.Is(verifyErr, holons.ErrRebaseActive) {
			return verified, verifyErr
		}
	}
	eligible := false
	for _, a := range current.AgentSessions {
		if a.ID == aid && a.ClosedAt == nil && ((a.Status == string(holons.StatusRestoring) && a.TerminalID == "") || a.Status == string(holons.StatusRecoveryFailed)) {
			eligible = true
		}
	}
	if !eligible {
		return current, holons.ErrNotResumable
	}
	for _, a := range current.AgentSessions {
		if a.ID == aid && a.TerminalID != "" {
			if s.terminals != nil {
				err = errors.Join(s.stopAgentExecution(ctx, id, aid), s.terminals.CloseHarness(ctx, id, aid, "restore"))
			} else if s.agents != nil {
				err = s.agents.Cancel(ctx, id, aid)
			}
			if err != nil {
				return current, err
			}
		}
	}
	prepare := s.Service.PrepareAgentRecovery
	if explicit {
		prepare = s.Service.PrepareAgentResumeRecovery
	}
	h, err := prepare(ctx, id, aid)
	if err != nil {
		return h, err
	}
	var agent holons.AgentSession
	for _, a := range h.AgentSessions {
		if a.ID == aid {
			agent = a
			break
		}
	}
	defer func() {
		if err == nil || ctx.Err() != nil {
			return
		}
		var updateErr error
		result, updateErr = s.Service.SetAgentSessionStatus(context.WithoutCancel(ctx), id, aid, holons.StatusRecoveryFailed, "Could not restore conversation: "+err.Error(), agent.ResumeTarget)
		err = errors.Join(err, updateErr)
	}()
	// Restored completed turns are history, so the harness will not emit a
	// second task_complete. Import their saved findings before relaunching.
	if h.Kind == holons.KindPullReview && agent.InputState == string(protocol.InputTaskComplete) && s.workCompletion != nil {
		disposition, completionErr := s.workCompletion.completeFromAgentLocked(ctx, h, aid, true)
		if completionErr != nil {
			return h, completionErr
		}
		if disposition.Terminal {
			if !disposition.AgentsFinalized {
				if err = finalizePullRequestWorkAgent(ctx, s.Service, s.agents, h, aid, disposition.Reason); err != nil {
					return h, err
				}
			}
			return s.Get(ctx, id)
		}
		h, err = s.Get(ctx, id)
		if err != nil {
			return h, err
		}
	}
	// Completion may have been saved just before a restart interrupted HEAD
	// verification. Reconcile it before launching: Codex imports the finished
	// turn as baseline history and will not emit another task_complete input.
	reconcileCommit := agent.CommitStartHead != "" && agent.InputState == string(protocol.InputTaskComplete)
	if reconcileCommit {
		// Recovery follows the same eligibility rules as live completion;
		// older workflow forks can also carry a saved starting HEAD.
		reconcileCommit, err = autoCommitEligible(ctx, h, s.work)
		if err != nil {
			return h, err
		}
	}
	if reconcileCommit {
		inspection, inspectErr := s.Service.InspectWorkspaceWithOptions(ctx, id, holons.InspectOptions{SummaryOnly: true})
		if inspectErr == nil && inspection.HeadCommit != "" && inspection.HeadCommit != agent.CommitStartHead {
			if _, _, err = s.Service.TransitionAgentCommitPrompt(ctx, id, aid, holons.CommitPromptClose, ""); err != nil {
				return h, err
			}
			return s.closeAgentSession(ctx, id, aid)
		}
		h, err = s.Service.UpdateAgentObservation(ctx, id, aid, "", "", "", string(protocol.InputUserRequired), "", "", protocol.ActivityNeedsInput, nil)
		if err = errors.Join(inspectErr, err); err != nil {
			return h, err
		}
	}
	if agent.ResumeTarget == "" {
		return h, errors.New("no saved conversation is available")
	}
	if h.WorktreePath == "" {
		return h, errors.New("the worktree is unavailable")
	}
	if info, statErr := os.Stat(h.WorktreePath); statErr != nil {
		return h, statErr
	} else if !info.IsDir() {
		return h, errors.New("the worktree is not a directory")
	}
	if s.harnesses != nil {
		if err = s.harnesses.Validate(ctx, protocol.HarnessType(agent.AgentType), workflowForKind(h.Kind)); err != nil {
			return h, err
		}
	}
	return s.launchAgent(ctx, h, aid, agentsessions.LaunchOptions{RequireResume: true})
}

func (s *terminalHolonService) CloseRebaseAgentSession(ctx context.Context, id, aid string) (holons.Holon, error) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	return s.closeRebaseAgent(ctx, id, aid)
}

// recoveryMu is already held. Finish the durable cleanup without relaunching or
// entering the public close method, which takes that same lock.
func (s *terminalHolonService) closeRebaseAgent(ctx context.Context, id, aid string) (holons.Holon, error) {
	ctx = context.WithoutCancel(ctx)
	h, err := s.Service.PrepareRebaseClosure(ctx, id)
	if err != nil || !h.RebaseAttempt.ClosurePending(aid) {
		return h, err
	}
	h, err = s.closeAgentSession(ctx, id, aid)
	if err != nil {
		return h, err
	}
	if err = s.Service.FinishRebaseClosure(ctx, id, aid); err != nil {
		return h, err
	}
	return s.Get(ctx, id)
}
