package pullrequestwork

import (
	"context"
	"errors"
	"strings"
)

// PendingAddressCompletion survives history rewriting while retaining the
// immutable execution input and the agent that originally produced the reply.
type PendingAddressCompletion struct {
	Completion           Completion `json:"completion"`
	SourceAgentID        string     `json:"source_agent_id"`
	State                string     `json:"state"`
	RetryCount           int        `json:"retry_count,omitempty"`
	AttemptTarget        string     `json:"attempt_target,omitempty"`
	RebaseAttemptID      string     `json:"rebase_attempt_id,omitempty"`
	RebaseAgentID        string     `json:"rebase_agent_id,omitempty"`
	PublicationAttempted bool       `json:"publication_attempted,omitempty"`
}

func (s *Service) BeginAddressCompletion(ctx context.Context, id, source string, c Completion) (Work, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.store.Get(ctx, id)
	if err != nil {
		return w, false, err
	}
	if !w.IsAddress() || !w.Active() || w.Status == StatusCancelling {
		return w, false, ErrInvalid
	}
	if w.PendingCompletion != nil {
		return w, false, nil
	}
	if source == "" || c.HeadCommit != w.HeadCommit || c.PullRequestID != w.PullRequestID || c.ResultHeadCommit == "" || strings.TrimSpace(c.ReplyBody) == "" {
		return w, false, ErrInvalid
	}
	w.PendingCompletion = &PendingAddressCompletion{Completion: c, SourceAgentID: source, State: "pending"}
	w.ReplyBody, w.Summary = c.ReplyBody, c.Summary
	return w, true, s.store.Update(ctx, w)
}
func (s *Service) SetAddressCompletionState(ctx context.Context, id, state, target, result, reason string) (Work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.store.Get(ctx, id)
	if err != nil {
		return w, err
	}
	if !w.IsAddress() || w.PendingCompletion == nil || !w.Active() || w.Status == StatusCancelling {
		return w, ErrInvalid
	}
	w.PendingCompletion.State = state
	if state == "launching" {
		w.PendingCompletion.RebaseAttemptID, w.PendingCompletion.RebaseAgentID = "", ""
	}
	if state == "ready" {
		w.PendingCompletion.PublicationAttempted = true
	}
	if target != "" {
		w.PublicationTargetCommit = target
	}
	if result != "" {
		w.PendingCompletion.Completion.ResultHeadCommit = result
	}
	w.Error = reason
	if state == "retry" || state == "rebasing" {
		w.Status = StatusWaiting
	}
	return w, s.store.Update(ctx, w)
}

var ErrAddressRetriesExhausted = errors.New("The publication target kept changing. Three automatic retries have been used; retry Rebase or Publish to continue with the saved completion.")

// ReserveAddressAttempt persists the allowance before any Git or agent side
// effect. Reopening the same target after restart never consumes another retry.
func (s *Service) ReserveAddressAttempt(ctx context.Context, id, target string, explicit bool) (Work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.store.Get(ctx, id)
	if err != nil {
		return w, err
	}
	if !w.IsAddress() || w.PendingCompletion == nil || !w.Active() || w.Status == StatusCancelling || target == "" {
		return w, ErrInvalid
	}
	p := w.PendingCompletion
	if explicit {
		p.RetryCount = 0
	} else if p.AttemptTarget != "" && p.AttemptTarget != target {
		if p.RetryCount >= 3 {
			p.State, w.Error, w.Status = "retry", ErrAddressRetriesExhausted.Error(), StatusWaiting
			if err := s.store.Update(ctx, w); err != nil {
				return w, err
			}
			return w, ErrAddressRetriesExhausted
		}
		p.RetryCount++
	}
	if p.AttemptTarget != target {
		p.PublicationAttempted = false
	}
	p.AttemptTarget, w.PublicationTargetCommit = target, target
	p.State, w.Error = "pending", ""
	w.Status = StatusWaiting
	return w, s.store.Update(ctx, w)
}

// BindAddressRebase records the synchronization identity and optional agent
// before launch or publication. Empty attempt IDs are only used by legacy work.
func (s *Service) BindAddressRebase(ctx context.Context, id, agent, attemptID string) (Work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.store.Get(ctx, id)
	if err != nil {
		return w, err
	}
	if !w.IsAddress() || w.PendingCompletion == nil || !w.Active() || w.Status == StatusCancelling {
		return w, ErrInvalid
	}
	w.PendingCompletion.RebaseAgentID = agent
	w.PendingCompletion.RebaseAttemptID = attemptID
	w.PendingCompletion.State = "rebasing"
	return w, s.store.Update(ctx, w)
}
