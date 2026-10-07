package pullrequestwork

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// CancelPullRequest ends all work, including reviews and Continue workers, and
// associated sessions. Persist every cancellation before stopping any runtime;
// unlike Cancel, this never dispatches another queued request.
func (s *Service) CancelPullRequest(ctx context.Context, pr string, associatedSessions ...string) error {
	s.mu.Lock()
	all, err := s.store.List(ctx, pr)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	sessions := map[string]bool{}
	for _, id := range associatedSessions {
		if id != "" {
			sessions[id] = true
		}
	}
	var result error
	for _, w := range all {
		if w.SessionID != "" {
			sessions[w.SessionID] = true
		}
		if !w.Active() {
			continue
		}
		// Published queue work must retain its durable checkpoint until settlement.
		// Continue publication does not complete its long-lived worker.
		if w.InQueue() && w.PublicationState == "published" {
			continue
		}
		if w.SessionID == "" {
			now := time.Now().UTC()
			w.Status, w.CompletedAt = StatusCancelled, &now
		} else {
			w.Status = StatusCancelling
		}
		if err := s.store.Update(ctx, w); err != nil {
			result = errors.Join(result, fmt.Errorf("work %s session %s: %w", w.ID, w.SessionID, err))
		}
	}
	s.mu.Unlock()
	// Shutdown can call back into this service, so never hold the work mutex here.
	ids := make([]string, 0, len(sessions))
	for id := range sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if s.runtime == nil {
			result = errors.Join(result, fmt.Errorf("session %s: %w", id, ErrInvalid))
		} else if err := s.runtime.Cancel(ctx, id); err != nil {
			result = errors.Join(result, fmt.Errorf("session %s: %w", id, err))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err = s.store.List(ctx, pr)
	if err != nil {
		return errors.Join(result, err)
	}
	for _, w := range all {
		if w.Status == StatusCompleted {
			if err := s.deliverReply(ctx, &w); err != nil {
				result = errors.Join(result, fmt.Errorf("work %s session %s: %w", w.ID, w.SessionID, err))
			}
			continue
		}
		if !w.Active() {
			continue
		}
		if w.SessionID != "" {
			if s.runtime == nil {
				continue
			}
			state, err := s.runtime.State(ctx, w.SessionID)
			if err != nil {
				result = errors.Join(result, fmt.Errorf("work %s session %s: %w", w.ID, w.SessionID, err))
				continue
			}
			if state != StatusCancelled && state != StatusCompleted && state != StatusFailed {
				continue
			}
		}
		if w.InQueue() {
			if handled, err := s.finishCheckpointLocked(ctx, &w); handled {
				if err != nil {
					result = errors.Join(result, fmt.Errorf("work %s session %s: %w", w.ID, w.SessionID, err))
				}
				continue
			}
		}
		if w.Status != StatusCancelling {
			continue
		}
		now := time.Now().UTC()
		w.Status, w.Error, w.CompletedAt = StatusCancelled, "", &now
		if err := s.store.Update(ctx, w); err != nil {
			result = errors.Join(result, fmt.Errorf("work %s session %s: %w", w.ID, w.SessionID, err))
		}
	}
	return result
}
