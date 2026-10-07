package localapp

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
	metadatagit "github.com/holark-ai/holark/internal/pullrequestmetadata/gitadapter"
)

// localMetadataHolons adapts the metadata workflow to visible, repository-local
// Holons. Artifact reads are rooted, no-follow, bounded, and ownership checked.
type localMetadataHolons struct {
	changes   pullrequestmetadata.Changes
	holons    *terminalHolonService
	harnesses localHarnessPreferences
}

func (a localMetadataHolons) Start(ctx context.Context, r pullrequestmetadata.StartAgentSessionRequest) (pullrequestmetadata.AgentSession, error) {
	selectedHarness := "codex"
	selectedModel, selectedPermissions := "", ""
	if a.harnesses != nil {
		resolved, err := a.harnesses.Resolve(ctx, agentsettings.WorkflowPullRequestMetadata, "")
		if err != nil {
			return pullrequestmetadata.AgentSession{}, err
		}
		selectedHarness, selectedModel, selectedPermissions = string(resolved.HarnessType), resolved.Model, resolved.Permissions
	}
	workSessionStartCommit := r.HeadCommit
	if workSessionStartCommit == "" {
		workSessionStartCommit = r.BaseCommit
	}
	diffBaseCommit := r.BaseCommit
	if diffBaseCommit == "" {
		diffBaseCommit = workSessionStartCommit
	}
	var input *pullrequestmetadata.Provenance
	if a.changes != nil {
		var err error
		input, err = a.changes.Capture(ctx, diffBaseCommit, workSessionStartCommit)
		if err != nil {
			return pullrequestmetadata.AgentSession{}, err
		}
	}
	h, err := a.holons.Create(ctx, holons.Create{Model: selectedModel, Permissions: selectedPermissions, SelectionResolved: true, Title: "Generate PR description and title", Prompt: r.Prompt, Kind: holons.KindPRMetadata, BaseBranch: r.BaseBranch, BaseCommit: diffBaseCommit, WorkSessionStartCommit: workSessionStartCommit, PullRequestID: r.PullRequestID, AgentType: selectedHarness})
	if err != nil {
		return pullrequestmetadata.AgentSession{}, err
	}
	result := metadataSession(h)
	result.Input = input
	return result, nil
}
func (a localMetadataHolons) Resume(ctx context.Context, r pullrequestmetadata.ResumeAgentSessionRequest) (pullrequestmetadata.AgentSession, error) {
	h, err := a.holons.Get(ctx, r.SessionID)
	if err != nil || h.Kind != holons.KindPRMetadata || h.PullRequestID != r.PullRequestID {
		return pullrequestmetadata.AgentSession{}, pullrequestmetadata.ErrAgentSessionUnavailable
	}
	if len(h.AgentSessions) == 0 {
		return pullrequestmetadata.AgentSession{}, pullrequestmetadata.ErrAgentSessionUnavailable
	}
	previousAgent := h.AgentSessions[len(h.AgentSessions)-1]
	if a.harnesses != nil {
		if err = a.harnesses.Validate(ctx, protocol.HarnessType(previousAgent.AgentType), agentsettings.WorkflowPullRequestMetadata); err != nil {
			return pullrequestmetadata.AgentSession{}, err
		}
	}
	var input *pullrequestmetadata.Provenance
	if a.changes != nil {
		input, err = (metadatagit.Changes{Path: h.WorktreePath}).Capture(ctx, h.BaseCommit, "HEAD")
		if err != nil {
			return pullrequestmetadata.AgentSession{}, err
		}
	}
	if err = removeMetadataArtifact(h.WorktreePath); err != nil {
		return pullrequestmetadata.AgentSession{}, err
	}
	// Follow-ups retain the stored selection even after workflow preferences change.
	h, err = a.holons.addAgentSessionWithSelection(ctx, h.ID, previousAgent.AgentType, r.Prompt, previousAgent.Model, previousAgent.Permissions)
	if err != nil {
		return pullrequestmetadata.AgentSession{}, err
	}
	result := metadataSession(h)
	result.Input = input
	return result, nil
}
func (a localMetadataHolons) Inspect(ctx context.Context, id string) (pullrequestmetadata.AgentSession, error) {
	h, err := a.holons.Get(ctx, id)
	if err != nil || h.Kind != holons.KindPRMetadata {
		return pullrequestmetadata.AgentSession{}, pullrequestmetadata.ErrAgentSessionUnavailable
	}
	return metadataSession(h), nil
}
func (a localMetadataHolons) ConsumeArtifact(ctx context.Context, id string) (pullrequestmetadata.Artifact, error) {
	h, err := a.holons.Get(ctx, id)
	if err != nil || h.Kind != holons.KindPRMetadata || h.WorktreePath == "" {
		return pullrequestmetadata.Artifact{}, pullrequestmetadata.ErrArtifactInvalid
	}
	root, err := os.OpenRoot(h.WorktreePath)
	if err != nil {
		return pullrequestmetadata.Artifact{}, err
	}
	defer root.Close()
	path := filepath.ToSlash(pullrequestmetadata.MetadataArtifactPath)
	f, err := root.Open(path)
	if err != nil {
		return pullrequestmetadata.Artifact{}, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > pullrequestmetadata.MaxArtifactBytes {
		f.Close()
		return pullrequestmetadata.Artifact{}, pullrequestmetadata.ErrArtifactInvalid
	}
	data, err := io.ReadAll(io.LimitReader(f, pullrequestmetadata.MaxArtifactBytes+1))
	closeErr := f.Close()
	if err != nil {
		return pullrequestmetadata.Artifact{}, err
	}
	if closeErr != nil {
		return pullrequestmetadata.Artifact{}, closeErr
	}
	return pullrequestmetadata.Artifact{Path: pullrequestmetadata.MetadataArtifactPath, Data: data, OwnerPullRequestID: h.PullRequestID}, nil
}

func removeMetadataArtifact(worktree string) error {
	if worktree == "" {
		return pullrequestmetadata.ErrArtifactInvalid
	}
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return err
	}
	defer root.Close()
	err = root.Remove(filepath.ToSlash(pullrequestmetadata.MetadataArtifactPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (a localMetadataHolons) SetTurnError(ctx context.Context, id, message string) error {
	h, e := a.holons.Get(ctx, id)
	if e != nil {
		return e
	}
	if len(h.AgentSessions) == 0 {
		return pullrequestmetadata.ErrAgentSessionUnavailable
	}
	agent := h.AgentSessions[len(h.AgentSessions)-1]
	_, e = a.holons.SetAgentSessionStatus(ctx, id, agent.ID, holons.StatusFailed, "Holark metadata recovery: "+message, agent.ResumeTarget)
	return e
}
func (a localMetadataHolons) ClearTurnError(ctx context.Context, id string) error {
	h, err := a.holons.Get(ctx, id)
	if err != nil {
		return err
	}
	return removeMetadataArtifact(h.WorktreePath)
}
func (a localMetadataHolons) Retire(ctx context.Context, id string) error {
	h, e := a.holons.Get(ctx, id)
	if errors.Is(e, holons.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	for _, agent := range h.AgentSessions {
		if agent.ClosedAt == nil {
			_, _ = a.holons.CancelAgentSession(ctx, id, agent.ID)
		}
	}
	return nil
}
func metadataSession(h holons.Holon) pullrequestmetadata.AgentSession {
	v := pullrequestmetadata.AgentSession{ID: h.ID, Status: string(h.Status), Error: h.Reason}
	if len(h.AgentSessions) > 0 {
		a := h.AgentSessions[len(h.AgentSessions)-1]
		v.Status = a.Status
		v.InputState = a.InputState
		v.Error = a.Reason
	}
	return v
}

func recoverMetadataArtifacts(ctx context.Context, hs *holons.Service, service *pullrequestmetadata.Service) {
	all, err := hs.List(ctx)
	if err != nil {
		return
	}
	for _, h := range all {
		if h.Kind == holons.KindPRMetadata && h.Status == holons.StatusCompleted {
			release := hs.BeginFinalization(h.ID)
			_ = service.ImportArtifact(ctx, h.ID)
			release()
		}
	}
}
