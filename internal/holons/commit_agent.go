package holons

import (
	"context"
	"errors"
	"strings"

	"github.com/holark-ai/holark/internal/protocol"
)

var ErrCommitSourceMissing = errors.New("no open agent has response or tool activity")
var ErrCommitSourceNotForkable = errors.New("this agent's conversation is not ready to fork yet")
var ErrCommitForkInProgress = errors.New("a commit agent is already active")

// Commit agents remain active until verified and closed, including completed
// turns that still need user input. Legacy discussions end with their turn.
func (a AgentSession) ActiveCommitDiscussion() bool {
	return a.ClosedAt == nil && !IsTerminal(Status(a.Status)) &&
		(a.CommitStartHead != "" || (a.InputState != "task_complete" &&
			a.CommitPrompt != nil && a.CommitPrompt.State == "commit_discussion_started"))
}

// CommitClosePending records a verified commit whose automatic close must finish.
// It survives cancellation and runtime shutdown failures until the tab is closed.
func (a AgentSession) CommitClosePending() bool {
	return a.ClosedAt == nil && a.CommitPrompt != nil && a.CommitPrompt.State == "commit_close_pending"
}

func (h Holon) HasActiveCommitAgent() bool {
	for _, a := range h.AgentSessions {
		if a.ActiveCommitDiscussion() {
			return true
		}
	}
	return false
}

// AddCommitAgent reserves a commit tab and returns its ID, inheriting the source
// harness when one is provided. Prompt rendering and launching belong to the app.
func (s *Service) AddCommitAgent(ctx context.Context, id, sourceID, prompt, startHead, agentType string, model ...string) (Holon, string, error) {
	return s.AddCommitAgentWithSelection(ctx, id, sourceID, prompt, startHead, agentType, AgentSelection{Model: selectedModel(model)})
}

func (s *Service) AddCommitAgentWithSelection(ctx context.Context, id, sourceID, prompt, startHead, agentType string, selection AgentSelection) (Holon, string, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, "", lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, "", err
	}
	if h.ReadOnly || h.ArchivedAt != nil || h.WorktreePath == "" || h.WorktreeBranch == "" || h.BaseCommit == "" || strings.TrimSpace(prompt) == "" {
		return Holon{}, "", ErrInvalid
	}
	if h.HasActiveCommitAgent() {
		return Holon{}, "", ErrCommitForkInProgress
	}
	selection = h.actionSelection(sourceID, selection)
	agentType, err = h.actionAgentType(sourceID, agentType)
	if err != nil {
		return Holon{}, "", err
	}
	now := s.now().UTC()
	agentID := newID("agent_")
	h, err = s.store.AddAgentSession(ctx, id, AgentSession{Model: selection.Model, Permissions: selection.Permissions, ID: agentID, HolonID: id, AgentType: agentType, Title: "Commit", Prompt: prompt, Status: string(StatusQueued), Activity: protocol.ActivityStarting, CommitStartHead: startHead, CommitPrompt: &CommitPrompt{State: "commit_discussion_started"}, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return Holon{}, "", err
	}
	return h, agentID, nil
}

// Actions inherit their source's harness or use the resolved default for a new conversation.
func (h Holon) actionAgentType(sourceID, agentType string) (string, error) {
	if sourceID != "" {
		source := h.AgentSession(sourceID)
		if source.ID == "" || source.HolonID != h.ID || source.ClosedAt != nil {
			return "", ErrNotFound
		}
		return source.AgentType, nil
	}
	switch protocol.HarnessType(agentType) {
	case protocol.HarnessCodex, protocol.HarnessClaudeCode, protocol.HarnessOpenCode:
		return agentType, nil
	default:
		return "", ErrInvalid
	}
}
