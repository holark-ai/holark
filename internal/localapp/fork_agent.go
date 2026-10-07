package localapp

import (
	"context"
	"strings"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminals"
)

type forkAgentHolons interface {
	Get(context.Context, string) (holons.Holon, error)
	AddForkedAgent(context.Context, string, string) (holons.AgentSession, error)
}

type forkAgentLauncher interface {
	Capabilities(context.Context) []protocol.HarnessCapability
	Fork(context.Context, string, string, string, terminals.Dimensions) error
}

type forkAgentCoordinator struct {
	holons          forkAgentHolons
	agents          forkAgentLauncher
	pinPullRequests func(context.Context, holons.Holon)
}

func (c *forkAgentCoordinator) ForkAgentSession(ctx context.Context, holonID, sourceID string) (holons.AgentSession, error) {
	h, err := c.holons.Get(ctx, holonID)
	if err != nil {
		return holons.AgentSession{}, err
	}
	source, err := forkSource(h, sourceID)
	if err != nil {
		return holons.AgentSession{}, err
	}
	available := false
	for _, capability := range c.agents.Capabilities(ctx) {
		if capability.Type == protocol.HarnessType(source.AgentType) && capability.Available {
			available = true
			break
		}
	}
	if !available {
		return holons.AgentSession{}, agentsessions.ErrUnavailable
	}
	destination, err := c.holons.AddForkedAgent(ctx, holonID, source.ID)
	if err != nil {
		return holons.AgentSession{}, err
	}
	if err = c.agents.Fork(ctx, holonID, source.ID, destination.ID, terminals.Dimensions{Columns: 120, Rows: 36}); err != nil {
		return holons.AgentSession{}, err
	}
	if c.pinPullRequests != nil {
		c.pinPullRequests(ctx, h)
	}
	updated, err := c.holons.Get(ctx, holonID)
	if err != nil {
		return holons.AgentSession{}, err
	}
	return updated.AgentSession(destination.ID), nil
}

func forkSource(h holons.Holon, sourceID string) (holons.AgentSession, error) {
	for _, source := range h.AgentSessions {
		if source.ID != sourceID || source.HolonID != h.ID || source.ClosedAt != nil {
			continue
		}
		if strings.TrimSpace(source.ResumeTarget) == "" {
			return holons.AgentSession{}, holons.ErrAgentSessionNotForkable
		}
		switch protocol.HarnessType(source.AgentType) {
		case protocol.HarnessCodex, protocol.HarnessOpenCode:
			return source, nil
		case protocol.HarnessClaudeCode:
			if strings.TrimSpace(source.RolloutPath) != "" {
				return source, nil
			}
		}
		return holons.AgentSession{}, holons.ErrAgentSessionNotForkable
	}
	return holons.AgentSession{}, holons.ErrAgentSessionNotFound
}
