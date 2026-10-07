package holons

import (
	"context"
	"errors"

	"github.com/holark-ai/holark/internal/protocol"
)

var (
	ErrAgentSessionNotFound    = errors.New("agent session not found")
	ErrAgentSessionNotForkable = errors.New("agent session is not ready to fork")
)

// AddForkedAgent reserves a new managed tab without copying the source's
// terminal or conversation identity. Harness-specific readiness and launching
// belong to the application layer.
func (s *Service) AddForkedAgent(ctx context.Context, id, sourceID string) (AgentSession, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return AgentSession{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return AgentSession{}, err
	}
	if h.ReadOnly || h.ArchivedAt != nil || h.WorktreePath == "" || h.WorktreeBranch == "" || h.BaseCommit == "" {
		return AgentSession{}, ErrInvalid
	}
	for _, source := range h.AgentSessions {
		if source.ID != sourceID || source.HolonID != id || source.ClosedAt != nil {
			continue
		}
		now := s.now().UTC()
		destination := AgentSession{
			Model: source.Model, Permissions: source.Permissions,
			ID: newID("agent_"), HolonID: id, AgentType: source.AgentType,
			Title: "Fork", Status: string(StatusQueued), Activity: protocol.ActivityStarting,
			CreatedAt: now, UpdatedAt: now,
		}
		created, addErr := s.store.AddAgentSession(ctx, id, destination)
		if addErr != nil {
			return AgentSession{}, addErr
		}
		return created.AgentSession(destination.ID), nil
	}
	return AgentSession{}, ErrAgentSessionNotFound
}
