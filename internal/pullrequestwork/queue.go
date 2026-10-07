package pullrequestwork

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// CommentReader validates ownership and reads resolution at dispatch time.
type CommentReader interface {
	AddressComment(context.Context, string, string) (resolved bool, err error)
}

// WorkerRuntime reports confirmed lifecycle state without deleting workspaces.
type WorkerRuntime interface {
	Cancel(context.Context, string) error
	Recover(context.Context, string) error
	State(context.Context, string) (Status, error)
}

func (s *Service) SetComments(reader CommentReader)       { s.comments = reader }
func (s *Service) SetWorkerRuntime(runtime WorkerRuntime) { s.runtime = runtime }

func (s *Service) addressComment(ctx context.Context, pr, id string) (bool, error) {
	if id == "" {
		return false, ErrInvalid
	}
	if s.comments == nil {
		return false, nil
	}
	return s.comments.AddressComment(ctx, pr, id)
}

func (s *Service) Queue(ctx context.Context, pr string) ([]Work, error) {
	all, err := s.store.List(ctx, pr)
	if err != nil {
		return nil, err
	}
	out := []Work{}
	for _, w := range all {
		if w.InQueue() && w.Active() {
			out = append(out, w)
		}
	}
	return out, nil
}

// Cancel addresses a durable request even when it has no holon yet. An active
// request keeps its slot until runtime shutdown is confirmed by Reconcile.
func (s *Service) Cancel(ctx context.Context, pr, id string) (Work, error) {
	s.mu.Lock()
	w, err := s.store.Get(ctx, id)
	if err == nil && (w.PullRequestID != pr || !w.InQueue()) {
		err = ErrNotFound
	}
	if err != nil {
		s.mu.Unlock()
		return Work{}, err
	}
	if !w.Active() {
		s.mu.Unlock()
		return w, nil
	}
	if handled, checkpointErr := s.finishCheckpointLocked(ctx, &w); handled {
		if checkpointErr == nil {
			s.notifyDispatcher()
		}
		s.mu.Unlock()
		return w, checkpointErr
	}
	if w.SessionID == "" {
		now := time.Now().UTC()
		w.Status, w.CompletedAt = StatusCancelled, &now
		err = s.store.Update(ctx, w)
		if err == nil {
			s.notifyDispatcher()
		}
		s.mu.Unlock()
		return w, err
	}
	if s.runtime == nil {
		s.mu.Unlock()
		return Work{}, ErrInvalid
	}
	w.Status = StatusCancelling
	err = s.store.Update(ctx, w)
	s.mu.Unlock()
	if err != nil {
		return Work{}, err
	}
	cancelErr := s.runtime.Cancel(ctx, w.SessionID)
	reconcileErr := s.Reconcile(ctx)
	w, err = s.store.Get(ctx, id)
	return w, errors.Join(cancelErr, reconcileErr, err)
}

// Reconcile settles stopped workers and reviews after asynchronous terminal
// shutdown or failure, including Continue workers marked for PR-wide cancellation.
// Waiting interactive workers continue to own their queue slot.
func (s *Service) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reconcileLocked(ctx, ""); err != nil {
		return err
	}
	s.notifyDispatcher()
	return nil
}

// reconcileLocked settles durable work without starting or waking dispatch.
func (s *Service) reconcileLocked(ctx context.Context, prID string) error {
	all, err := s.store.List(ctx, prID)
	if err != nil {
		return err
	}
	for _, w := range all {
		if (!w.InQueue() && w.Kind != KindReview && w.Status != StatusCancelling) || !w.Active() {
			continue
		}
		if w.InQueue() {
			if handled, err := s.finishCheckpointLocked(ctx, &w); handled {
				if err != nil {
					return fmt.Errorf("pull request %s work %s request %s: %w", w.PullRequestID, w.ID, w.RequestID, err)
				}
				continue
			}
		}
		if w.Status == StatusQueued || w.SessionID == "" || s.runtime == nil {
			continue
		}
		state, err := s.runtime.State(ctx, w.SessionID)
		if err != nil {
			return fmt.Errorf("pull request %s work %s request %s: %w", w.PullRequestID, w.ID, w.RequestID, err)
		}
		if state == StatusRunning || state == StatusWaiting || state == StatusCancelling {
			continue
		}
		// Preserve saved results after ordinary exits, but still settle confirmed
		// cancellation from End holon, which does not mark the work cancelling.
		if w.PendingCompletion != nil && w.PendingCompletion.State != "rebasing" && w.Status != StatusCancelling && state != StatusCancelled {
			continue
		}
		// A recoverable conversation failure must not discard an unpublished review.
		if w.IsAssistedReview() && state != StatusCancelled && w.Status != StatusCancelling {
			continue
		}
		now := time.Now().UTC()
		if state == StatusCancelled || w.Status == StatusCancelling {
			w.Status, w.Error = StatusCancelled, ""
		} else {
			w.Status, w.Error = StatusFailed, "Work holon stopped before completing its artifact."
		}
		w.CompletedAt = &now
		if err := s.store.Update(ctx, w); err != nil {
			return fmt.Errorf("pull request %s work %s request %s: %w", w.PullRequestID, w.ID, w.RequestID, err)
		}
	}
	return nil
}
