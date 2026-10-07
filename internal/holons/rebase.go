package holons

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/holark-ai/holark/internal/protocol"
)

var ErrRebaseActive = errors.New("a rebase is active or unresolved; continue in its Rebase tab")
var ErrRebaseRequired = errors.New("the publication target must be rebased into this workspace")
var ErrPublicationUnavailable = errors.New("publication is unavailable")

type RebaseAttempt struct {
	PendingClosureAgentIDs []string `json:"pending_closure_agent_ids,omitempty"`
	ID                     string   `json:"id,omitempty"`
	Progress               string   `json:"progress,omitempty"`
	SourceAgentID          string   `json:"source_agent_id,omitempty"`
	AgentID                string   `json:"agent_id"`
	TargetBranch           string   `json:"target_branch"`
	TargetCommit           string   `json:"target_commit"`
	StartHeadCommit        string   `json:"start_head_commit"`
	ResultHeadCommit       string   `json:"result_head_commit,omitempty"`
	State                  string   `json:"state"`
	FailureReason          string   `json:"failure_reason,omitempty"`
}

func (a *RebaseAttempt) Active() bool {
	return a != nil && (a.State == "running" || a.State == "waiting")
}

type PublicationTargetReader interface {
	AcceptsPublicationTarget(Holon, PublicationReadiness) bool
	ReservePublicationTarget(context.Context, Holon, PublicationReadiness) error
}

func (s *Service) SetPublicationTargets(reader PublicationTargetReader) {
	s.publicationTargets = reader
}

// SetRebaseAutoCloseEligibility supplies the application's workspace policy.
// Without a policy, verification leaves tab completion to its owning workflow.
func (s *Service) SetRebaseAutoCloseEligibility(eligible func(context.Context, Holon) (bool, error)) {
	s.rebaseAutoCloseEligible = eligible
}

type PublicationReadiness struct {
	RecoveringPublication bool           `json:"-"`
	TargetPullRequestID   string         `json:"-"`
	TargetVersion         string         `json:"-"`
	TargetBranch          string         `json:"target_branch"`
	TargetCommit          string         `json:"target_commit"`
	WorkspaceHead         string         `json:"workspace_head"`
	RebaseRequired        bool           `json:"rebase_required"`
	RebaseAvailable       bool           `json:"rebase_available"`
	PublicationAvailable  bool           `json:"publication_available"`
	Reason                string         `json:"reason,omitempty"`
	Attempt               *RebaseAttempt `json:"attempt,omitempty"`
}

type SynchronizationInspection struct {
	RebaseBranch, RebaseTarget                string
	Branch, HeadCommit                        string
	Dirty, Rebasing, Incorporated, Conflicted bool
}
type synchronizationRepository interface {
	InspectSynchronization(context.Context, string, string, []string) (SynchronizationInspection, error)
}

func synchronizationIgnoredPaths(h Holon) []string {
	if h.Kind == KindPullWorker {
		return []string{".holark/comment-reply.json"}
	}
	return nil
}
func (s *Service) PublicationReadiness(ctx context.Context, id, branch, target string) (PublicationReadiness, error) {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return PublicationReadiness{}, err
	}
	h, err := s.store.Get(ctx, id)
	if h.RebaseAttempt != nil {
		snapshot := *h.RebaseAttempt
		snapshot.PendingClosureAgentIDs = slices.Clone(snapshot.PendingClosureAgentIDs)
		h.RebaseAttempt = &snapshot
	}
	unlock()
	if err != nil {
		return PublicationReadiness{}, err
	}
	r := PublicationReadiness{TargetBranch: branch, Attempt: h.RebaseAttempt}
	repo, ok := s.repository.(synchronizationRepository)
	if !ok || branch == "" || h.ReadOnly || h.ArchivedAt != nil || h.WorktreePath == "" {
		r.Reason = ErrPublicationUnavailable.Error()
		return r, nil
	}
	if h.RebaseAttempt.Active() {
		target, r.TargetBranch = h.RebaseAttempt.TargetCommit, h.RebaseAttempt.TargetBranch
	}
	if target == "" {
		return r, ErrPublicationUnavailable
	}
	r.TargetCommit = target
	i, err := repo.InspectSynchronization(ctx, id, target, synchronizationIgnoredPaths(h))
	if err != nil {
		return r, err
	}
	unlock, err = s.lock(ctx, id)
	if err != nil {
		return PublicationReadiness{}, err
	}
	defer unlock()
	latest, err := s.store.Get(ctx, id)
	if err != nil {
		return r, err
	}
	if !sameSynchronizationState(h, latest) {
		return PublicationReadiness{}, ErrPublicationUnavailable
	}
	h = latest
	r.WorkspaceHead = i.HeadCommit
	r.RebaseRequired = !i.Incorporated
	if h.RebaseAttempt.Active() {
		a := h.RebaseAttempt
		r.Reason = a.FailureReason
		if a.AgentID == "" {
			r.RebaseRequired = true
			r.RebaseAvailable = a.State == "waiting" || a.Progress == "reserved"
			if r.RebaseAvailable {
				r.Reason = ""
			} else if r.Reason == "" {
				r.Reason = "A rebase is in progress."
			}
		} else if a.Progress == "agent_reserved" || a.Progress == "conflicts" {
			r.RebaseRequired, r.RebaseAvailable, r.Reason = true, true, ""
		} else if r.Reason == "" {
			r.Reason = ErrRebaseActive.Error()
		}
		return r, nil
	}
	if i.Rebasing {
		r.Reason = "A Git rebase is unresolved in this workspace."
		return r, nil
	}
	if i.Branch != h.WorktreeBranch {
		r.Reason = "The workspace is on a different branch."
		return r, nil
	}
	if i.Dirty {
		r.Reason = "Commit workspace changes before rebasing or publishing."
		return r, nil
	}
	if i.Incorporated && h.SynchronizedTargetCommit != target {
		h.SynchronizedTargetCommit = target
		if err = s.updateSynchronization(ctx, h); err != nil {
			return r, err
		}
	}
	r.RebaseAvailable = r.RebaseRequired
	r.PublicationAvailable = i.Incorporated && i.HeadCommit != target
	return r, nil
}

// Synchronize starts from captured readiness, or resumes the stored attempt
// when target is nil. It leaves Git unchanged for the agent and verifies recovered attempts.
func (s *Service) Synchronize(ctx context.Context, id, sourceID string, target *PublicationReadiness) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return h, err
	}
	repo, ok := s.repository.(synchronizationRepository)
	if !ok {
		return h, ErrInvalid
	}
	if target == nil {
		if !h.RebaseAttempt.Active() || !h.RebaseNeedsSynchronization() {
			return h, ErrRebaseActive
		}
		// Give older queued reservations an attempt identity before attaching an agent.
		if h.RebaseAttempt.AgentID != "" && h.RebaseAttempt.ID == "" {
			h.RebaseAttempt.ID, h.RebaseAttempt.Progress = newID("sync_"), "reserved"
			if err = s.updateSynchronization(ctx, h); err != nil {
				return h, err
			}
		}
	} else {
		if h.RebaseAttempt.Active() {
			return h, ErrRebaseActive
		}
		if h.ReadOnly || h.ArchivedAt != nil || target.TargetCommit == "" {
			return h, ErrInvalid
		}
		if s.publicationTargets != nil && !s.publicationTargets.AcceptsPublicationTarget(h, *target) {
			return h, ErrRebaseRequired
		}
		i, err := repo.InspectSynchronization(ctx, id, target.TargetCommit, synchronizationIgnoredPaths(h))
		if err != nil {
			return h, err
		}
		if !i.clean(h.WorktreeBranch) || i.HeadCommit != target.WorkspaceHead || i.Incorporated {
			return h, ErrPublicationUnavailable
		}
		var pendingClosures []string
		if h.RebaseAttempt != nil {
			pendingClosures = slices.Clone(h.RebaseAttempt.PendingClosureAgentIDs)
		}
		h.RebaseAttempt = &RebaseAttempt{PendingClosureAgentIDs: pendingClosures, ID: newID("sync_"), Progress: "reserved", SourceAgentID: sourceID, TargetBranch: target.TargetBranch, TargetCommit: target.TargetCommit, StartHeadCommit: target.WorkspaceHead, State: "running"}
		if s.publicationTargets != nil {
			err = s.publicationTargets.ReservePublicationTarget(ctx, h, *target)
		} else {
			err = s.updateSynchronization(ctx, h)
		}
		if err != nil {
			return h, err
		}
	}
	return s.resumeSynchronization(ctx, h, repo)
}

func (s *Service) resumeSynchronization(ctx context.Context, h Holon, repo synchronizationRepository) (Holon, error) {
	a := h.RebaseAttempt
	ignored := synchronizationIgnoredPaths(h)
	i, err := repo.InspectSynchronization(ctx, h.ID, a.TargetCommit, ignored)
	if err != nil {
		a.State, a.FailureReason = "waiting", err.Error()
		return h, errors.Join(err, s.updateSynchronization(context.WithoutCancel(ctx), h))
	}
	if i.clean(h.WorktreeBranch) && i.Incorporated {
		return s.verifyRebase(ctx, h, i, nil)
	}
	// A fresh attempt reserves the unchanged workspace for an agent. Git must
	// remain untouched so the agent can inspect both histories before rebasing.
	if (a.Progress == "reserved" || a.Progress == "agent_reserved") && i.clean(h.WorktreeBranch) && i.HeadCommit == a.StartHeadCommit {
		return h, nil
	}
	if i.conflictsFor(h) {
		a.State, a.Progress, a.FailureReason = "waiting", "conflicts", "Resolve the rebase conflicts."
		return h, s.updateSynchronization(context.WithoutCancel(ctx), h)
	}
	err = fmt.Errorf("%w: The interrupted rebase needs review before it can continue.", ErrPublicationUnavailable)
	a.State, a.Progress, a.FailureReason = "waiting", "interrupted", err.Error()
	return h, errors.Join(err, s.updateSynchronization(context.WithoutCancel(ctx), h))
}

// AddRebaseAgent attaches a conversation to an unchanged reservation or a recovered conflicted rebase.
// The target stays fixed even if the remote moves while conflicts are resolved.
func (s *Service) AddRebaseAgent(ctx context.Context, id, sourceID, prompt, attemptID, agentType string, model ...string) (Holon, error) {
	return s.AddRebaseAgentWithSelection(ctx, id, sourceID, prompt, attemptID, agentType, AgentSelection{Model: selectedModel(model)})
}

func (s *Service) AddRebaseAgentWithSelection(ctx context.Context, id, sourceID, prompt, attemptID, agentType string, selection AgentSelection) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	a := h.RebaseAttempt
	if a == nil || a.ID != attemptID || !a.Active() || (a.Progress != "reserved" && a.Progress != "agent_reserved" && a.Progress != "conflicts") {
		return h, ErrRebaseActive
	}
	if h.ReadOnly || h.ArchivedAt != nil || prompt == "" {
		return h, ErrInvalid
	}
	repo, ok := s.repository.(synchronizationRepository)
	if !ok {
		return h, ErrInvalid
	}
	i, err := repo.InspectSynchronization(ctx, id, a.TargetCommit, synchronizationIgnoredPaths(h))
	if err != nil {
		return h, err
	}
	if !i.readyForRebaseAgent(h) {
		return h, ErrPublicationUnavailable
	}
	selection = h.actionSelection(sourceID, selection)
	agentType, err = h.actionAgentType(sourceID, agentType)
	if err != nil {
		return h, err
	}
	if a.AgentID != "" {
		existing := h.AgentSession(a.AgentID)
		if !existing.unlaunched() {
			return h, ErrRebaseActive
		}
		existing.AgentType, existing.Prompt = agentType, prompt
		existing.Model, existing.Permissions = selection.Model, selection.Permissions
		if h, err = s.store.UpdateAgentSession(ctx, id, existing, false); err != nil {
			return h, err
		}
		a = h.RebaseAttempt
		a.SourceAgentID, a.Progress, a.State, a.FailureReason = sourceID, "agent_reserved", "running", ""
		return h, s.updateSynchronization(ctx, h)
	}
	now := s.now().UTC()
	agent := AgentSession{Model: selection.Model, Permissions: selection.Permissions, ID: newID("agent_"), HolonID: id, AgentType: agentType, Title: "Rebase", Prompt: prompt, Status: string(StatusQueued), CreatedAt: now, UpdatedAt: now}
	a.AgentID, a.SourceAgentID, a.State, a.FailureReason, a.Progress = agent.ID, sourceID, "running", "", "agent_reserved"
	store, ok := s.store.(interface {
		ReserveRebase(context.Context, Holon, AgentSession) (Holon, error)
	})
	if !ok {
		return h, ErrInvalid
	}
	return store.ReserveRebase(ctx, h, agent)
}

// VerifyRebase is idempotent and only accepts completion from the reserved agent.
// It never aborts or resets Git, including during restart reconciliation.
func (s *Service) VerifyRebase(ctx context.Context, id, agentID string) (Holon, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Holon{}, lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	a := h.RebaseAttempt
	if a == nil || agentID == "" || a.AgentID != agentID || a.State == "failed" {
		return h, ErrNotFound
	}
	if a.State == "succeeded" {
		return h, nil
	}
	if !h.RebaseCompletionReady(agentID) {
		return h, ErrRebaseActive
	}
	repo, ok := s.repository.(synchronizationRepository)
	if !ok {
		return h, ErrInvalid
	}
	i, err := repo.InspectSynchronization(ctx, h.ID, a.TargetCommit, synchronizationIgnoredPaths(h))
	return s.verifyRebase(ctx, h, i, err)
}

func (s *Service) verifyRebase(ctx context.Context, h Holon, i SynchronizationInspection, err error) (Holon, error) {
	a := h.RebaseAttempt
	reason := ""
	if err != nil {
		reason = err.Error()
	} else if i.Rebasing {
		reason = "Resolve the rebase in this tab."
	} else if i.Branch != h.WorktreeBranch {
		reason = "Restore the expected workspace branch."
	} else if i.Dirty {
		reason = "The rebased workspace has uncommitted changes."
	} else if !i.Incorporated {
		reason = "The captured target is not an ancestor of the workspace head."
	}
	if reason != "" {
		a.State, a.FailureReason = "waiting", reason
	} else {
		closeAgent := false
		if h.RebaseCompletionReady(a.AgentID) && s.rebaseAutoCloseEligible != nil {
			closeAgent, err = s.rebaseAutoCloseEligible(ctx, h)
			if err != nil {
				return h, err
			}
		}
		a.State, a.FailureReason, a.ResultHeadCommit, a.Progress = "succeeded", "", i.HeadCommit, "verified"
		h.SynchronizedTargetCommit = a.TargetCommit
		if closeAgent && !a.ClosurePending(a.AgentID) {
			a.PendingClosureAgentIDs = append(slices.Clone(a.PendingClosureAgentIDs), a.AgentID)
		}
	}
	if err = s.updateSynchronization(ctx, h); err != nil {
		return h, err
	}
	if reason != "" {
		return h, fmt.Errorf("%w: %s", ErrRebaseActive, reason)
	}
	return h, nil
}
func (s *Service) FailRebase(ctx context.Context, id, agentID, reason string) error {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if h.RebaseAttempt == nil || h.RebaseAttempt.AgentID != agentID || h.RebaseAttempt.State == "succeeded" {
		return nil
	}
	h.RebaseAttempt.State, h.RebaseAttempt.FailureReason = "failed", reason
	return s.updateSynchronization(ctx, h)
}

func (s *Service) updateSynchronization(ctx context.Context, h Holon) error {
	if store, ok := s.store.(interface {
		UpdateSynchronization(context.Context, Holon) error
	}); ok {
		return store.UpdateSynchronization(ctx, h)
	}
	return s.store.Update(ctx, h)
}

// ReserveRebaseLaunch prevents duplicate forks across requests and restart. A
// crash after this checkpoint is ambiguous and must not spawn a second process.
func (s *Service) ReserveRebaseLaunch(ctx context.Context, id, attemptID, agentID string) error {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	a := h.RebaseAttempt
	if a == nil || a.ID != attemptID || a.AgentID != agentID || a.Progress != "agent_reserved" || !a.Active() {
		return ErrRebaseActive
	}
	repo, ok := s.repository.(synchronizationRepository)
	if !ok {
		return ErrInvalid
	}
	i, err := repo.InspectSynchronization(ctx, h.ID, a.TargetCommit, synchronizationIgnoredPaths(h))
	if err != nil {
		return err
	}
	if !i.readyForRebaseAgent(h) {
		return ErrPublicationUnavailable
	}
	a.Progress = "agent_launching"
	return s.updateSynchronization(ctx, h)
}

// DeferRebaseLaunch retains a reservation if its CLI disappeared during probe.
// Only a destination with no native identity is safe to retry this way.
func (s *Service) DeferRebaseLaunch(ctx context.Context, id, attemptID, reason string) error {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	a := h.RebaseAttempt
	if a == nil || a.ID != attemptID || !a.Active() {
		return ErrRebaseActive
	}
	agent := h.AgentSession(a.AgentID)
	if agent.ID == "" {
		return ErrNotFound
	}
	if agent.TerminalID != "" || agent.ResumeTarget != "" || agent.RolloutPath != "" {
		return ErrRebaseActive
	}
	agent.Status, agent.Reason, agent.FinishedAt = string(StatusQueued), reason, nil
	if _, err = s.store.UpdateAgentSession(ctx, id, agent, false); err != nil {
		return err
	}
	a.State, a.Progress, a.FailureReason = "waiting", "agent_reserved", reason
	return s.updateSynchronization(ctx, h)
}

func (h Holon) RebaseNeedsSynchronization() bool {
	a := h.RebaseAttempt
	return a != nil && (a.AgentID == "" || (a.Progress != "agent_launching" && h.AgentSession(a.AgentID).unlaunched()))
}

func (a AgentSession) unlaunched() bool {
	return a.ID != "" && a.Status == string(StatusQueued) && a.TerminalID == "" && a.ResumeTarget == "" && a.RolloutPath == ""
}

func (i SynchronizationInspection) clean(branch string) bool {
	return !i.Rebasing && !i.Dirty && i.Branch == branch
}

func (i SynchronizationInspection) conflictsFor(h Holon) bool {
	return i.Rebasing && i.Conflicted && i.RebaseBranch == h.WorktreeBranch && i.RebaseTarget == h.RebaseAttempt.TargetCommit
}

// Conflicted attempts from an earlier run can still be resumed; new attempts
// must retain the exact clean head captured before the agent was reserved.
func (i SynchronizationInspection) readyForRebaseAgent(h Holon) bool {
	return i.conflictsFor(h) || (i.clean(h.WorktreeBranch) && i.HeadCommit == h.RebaseAttempt.StartHeadCommit && !i.Incorporated)
}

// Ignore unrelated edits (title, selected tab, agent activity) while rejecting
// results captured before lifecycle or synchronization state changed.
func sameSynchronizationState(a, b Holon) bool {
	return a.Status == b.Status && a.ReadOnly == b.ReadOnly && reflect.DeepEqual(a.ArchivedAt, b.ArchivedAt) &&
		a.WorktreePath == b.WorktreePath && a.WorktreeBranch == b.WorktreeBranch &&
		a.SynchronizedTargetCommit == b.SynchronizedTargetCommit && reflect.DeepEqual(a.RebaseAttempt, b.RebaseAttempt)
}

// ClosurePending is durable authorization to finish cleanup even after cancellation
// or a subsequent attempt, unless explicit resume revokes it. Historical
// successes have no such record.
func (a *RebaseAttempt) ClosurePending(agentID string) bool {
	return a != nil && slices.Contains(a.PendingClosureAgentIDs, agentID)
}

// RebaseCompletionReady excludes clarification, failure and cancellation, even
// when Git happens to be clean. The caller must still verify the captured target.
func (h Holon) RebaseCompletionReady(agentID string) bool {
	a := h.RebaseAttempt
	if a == nil || a.AgentID != agentID || agentID == "" || h.Kind == KindRebase ||
		h.Status == StatusCancelling || h.Status == StatusCancelled {
		return false
	}
	agent := h.AgentSession(agentID)
	if agent.ID == "" || agent.ClosedAt != nil || agent.InputState == string(protocol.InputUserRequired) {
		return false
	}
	switch Status(agent.Status) {
	case StatusCompleted:
		return agent.ExitCode != nil && *agent.ExitCode == 0
	case StatusRunning, StatusRestoring:
		return agent.InputState == string(protocol.InputTaskComplete)
	default:
		return false
	}
}

// PrepareRebaseClosure rechecks the current scope before runtime cleanup. Older
// versions could queue workflow-owned tabs; discard those requests without
// closing their agents or changing the verified rebase result.
func (s *Service) PrepareRebaseClosure(ctx context.Context, id string) (Holon, error) {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil || h.RebaseAttempt == nil || len(h.RebaseAttempt.PendingClosureAgentIDs) == 0 {
		return h, err
	}
	eligible := false
	if s.rebaseAutoCloseEligible != nil {
		eligible, err = s.rebaseAutoCloseEligible(ctx, h)
		if err != nil {
			return h, err
		}
	}
	if !eligible {
		h.RebaseAttempt.PendingClosureAgentIDs = nil
		err = s.updateSynchronization(ctx, h)
	}
	return h, err
}

// FinishRebaseClosure removes only this agent's completed cleanup, including
// when a newer attempt has inherited it. Runtime closure happens outside the lock.
func (s *Service) FinishRebaseClosure(ctx context.Context, id, agentID string) error {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil || !h.RebaseAttempt.ClosurePending(agentID) {
		return err
	}
	if h.AgentSession(agentID).ClosedAt == nil {
		return ErrRebaseActive
	}
	h.RebaseAttempt.PendingClosureAgentIDs = slices.DeleteFunc(slices.Clone(h.RebaseAttempt.PendingClosureAgentIDs), func(id string) bool { return id == agentID })
	return s.updateSynchronization(ctx, h)
}

// clearResumedRebaseClosures revokes auto-close only for retained conversations
// accepted for explicit resume. Closed tabs still need their archival retried.
// The caller holds the Holon lock and must persist this before preparing launch.
func (s *Service) clearResumedRebaseClosures(ctx context.Context, h Holon, agentIDs ...string) error {
	if h.RebaseAttempt == nil {
		return nil
	}
	pending := slices.DeleteFunc(slices.Clone(h.RebaseAttempt.PendingClosureAgentIDs), func(id string) bool {
		a := h.AgentSession(id)
		return a.ID != "" && a.ClosedAt == nil && (len(agentIDs) == 0 || slices.Contains(agentIDs, id))
	})
	if len(pending) == len(h.RebaseAttempt.PendingClosureAgentIDs) {
		return nil
	}
	h.RebaseAttempt.PendingClosureAgentIDs = pending
	return s.updateSynchronization(ctx, h)
}
