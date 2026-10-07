package pullrequestwork

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Notifications only request a scan; persisted work owns the queue.
func (s *Service) notifyDispatcher() {
	select {
	case s.dispatchWake <- struct{}{}:
	default:
	}
}

// Dispatch reconciles active work and starts each eligible PR's queue in store order.
// A reconciliation failure blocks only that PR's queue.
// Preparation still holds the shared queue mutex. Release it between PRs so
// other queue operations can proceed; cancellation is checked before each PR.
// Slow preparation can still delay cancellation and progress on other PRs.
// Outgoing-agent retirement belongs to completion and may overlap preparation.
func (s *Service) Dispatch(ctx context.Context) error {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	all, err := s.store.List(ctx, "")
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	var result error
	for _, w := range all {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if (!w.InQueue() && w.Kind != KindReview && w.Status != StatusCancelling) || !w.Active() || seen[w.PullRequestID] {
			continue
		}
		seen[w.PullRequestID] = true
		s.mu.Lock()
		err := ctx.Err()
		if err == nil {
			err = s.reconcileLocked(ctx, w.PullRequestID)
		}
		if err == nil {
			err = s.startNextLocked(ctx, w.PullRequestID)
		}
		s.mu.Unlock()
		if err != nil {
			result = errors.Join(result, fmt.Errorf("dispatch pull request %s: %w", w.PullRequestID, err))
		}
	}
	return errors.Join(result, ctx.Err())
}

// RunDispatcher uses the application's lifetime context, independent of the
// requests that admitted work. Startup and periodic scans recover missed wakes.
func (s *Service) RunDispatcher(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		// Consume coalesced wakes before scanning, including wakes accumulated
		// during the previous pass. Errors do not enqueue their own retry.
		select {
		case <-s.dispatchWake:
		default:
		}
		if err := s.Dispatch(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "Work queue dispatch failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.dispatchWake:
		case <-ticker.C:
		}
	}
}
