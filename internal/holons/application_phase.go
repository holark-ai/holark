package holons

import (
	"context"
	"sync"
)

// applicationPhases tracks application-owned completion handlers independently
// of agent activity and durable outcomes. Each handler owns its own token.
type finalizationOwner byte

type applicationPhases struct {
	mu      sync.Mutex
	owners  map[string]map[*finalizationOwner]bool
	pending func(context.Context, string) bool
}

func (s *Service) BeginFinalization(id string) func() {
	if s == nil {
		return func() {}
	}
	token := new(finalizationOwner)
	s.phases.mu.Lock()
	if s.phases.owners == nil {
		s.phases.owners = make(map[string]map[*finalizationOwner]bool)
	}
	if s.phases.owners[id] == nil {
		s.phases.owners[id] = make(map[*finalizationOwner]bool)
	}
	s.phases.owners[id][token] = true
	s.phases.mu.Unlock()
	return func() {
		s.phases.mu.Lock()
		defer s.phases.mu.Unlock()
		delete(s.phases.owners[id], token)
		if len(s.phases.owners[id]) == 0 {
			delete(s.phases.owners, id)
		}
	}
}

// SetPendingFinalization supplies the existing durable workflow's read-only
// projection. It must not acquire execution locks or perform repository work.
func (s *Service) SetPendingFinalization(pending func(context.Context, string) bool) {
	s.phases.mu.Lock()
	defer s.phases.mu.Unlock()
	s.phases.pending = pending
}

func (s *Service) ProjectHolon(ctx context.Context, h Holon) Holon {
	h.ApplicationPhase = ""
	if h.EndRequested || h.Status == StatusCancelling || h.Status == StatusCancelled {
		return h
	}
	s.phases.mu.Lock()
	executing, pending := len(s.phases.owners[h.ID]) > 0, s.phases.pending
	s.phases.mu.Unlock()
	if executing || (h.Kind == KindPullWorker && pending != nil && pending(ctx, h.ID)) {
		h.ApplicationPhase = "finalizing"
	}
	return h
}
