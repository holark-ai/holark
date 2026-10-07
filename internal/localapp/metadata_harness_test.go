package localapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
)

type harnessPreferencesStub struct {
	resolved    protocol.HarnessType
	model       string
	resolveErr  error
	validated   []protocol.HarnessType
	validateErr error
}

func (s *harnessPreferencesStub) ResolveDefault(context.Context, agentsettings.Workflow) (protocol.HarnessType, error) {
	return s.resolved, s.resolveErr
}

func (s *harnessPreferencesStub) Resolve(ctx context.Context, workflow agentsettings.Workflow, selected protocol.HarnessType) (agentsettings.Default, error) {
	if selected == "" {
		return agentsettings.Default{HarnessType: s.resolved, Model: s.model}, s.resolveErr
	}
	model := ""
	if selected == s.resolved {
		model = s.model
	}
	return agentsettings.Default{HarnessType: selected, Model: model}, s.Validate(ctx, selected, workflow)
}
func (s *harnessPreferencesStub) Validate(_ context.Context, harnessType protocol.HarnessType, _ agentsettings.Workflow) error {
	s.validated = append(s.validated, harnessType)
	return s.validateErr
}

func TestNewMetadataSessionUsesResolvedHarness(t *testing.T) {
	service, _ := terminalTestService(t)
	preferences := &harnessPreferencesStub{resolved: protocol.HarnessOpenCode, model: "provider/metadata-model"}
	adapter := localMetadataHolons{holons: &terminalHolonService{Service: service}, harnesses: preferences}
	result, err := adapter.Start(t.Context(), pullrequestmetadata.StartAgentSessionRequest{PullRequestID: "pr-1", HeadBranch: "feature", HeadCommit: "head", Prompt: "Improve metadata"})
	if err != nil {
		t.Fatal(err)
	}
	holon, err := service.Get(t.Context(), result.ID)
	if err != nil || len(holon.AgentSessions) != 1 || holon.AgentSessions[0].AgentType != "opencode" || holon.AgentSessions[0].Model != "provider/metadata-model" {
		t.Fatalf("holon = %+v, %v", holon, err)
	}
}

func TestNewMetadataSessionPreflightsBeforeDurableCreation(t *testing.T) {
	service, _ := terminalTestService(t)
	before, err := service.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("saved default unavailable")
	adapter := localMetadataHolons{holons: &terminalHolonService{Service: service}, harnesses: &harnessPreferencesStub{resolveErr: want}}
	if _, err = adapter.Start(t.Context(), pullrequestmetadata.StartAgentSessionRequest{PullRequestID: "pr-1", HeadCommit: "head", Prompt: "Improve metadata"}); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	after, err := service.List(t.Context())
	if err != nil || len(after) != len(before) {
		t.Fatalf("durable sessions before=%d after=%d error=%v", len(before), len(after), err)
	}
}

func TestMetadataFollowUpCopiesAndValidatesStoredHarnessBeforeArtifactMutation(t *testing.T) {
	service, store := terminalTestService(t)
	worktree := t.TempDir()
	artifact := filepath.Join(worktree, pullrequestmetadata.MetadataArtifactPath)
	if err := os.MkdirAll(filepath.Dir(artifact), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte(`{"title":"Old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.Create(t.Context(), holons.Holon{
		ID: "metadata-harness", Title: "Metadata", Kind: holons.KindPRMetadata, Status: holons.StatusExpired,
		BaseCommit: "head", WorktreeBranch: "holark/metadata", WorktreePath: worktree, PullRequestID: "pr-1", CreatedAt: now,
		AgentSessions: []holons.AgentSession{{ID: "old", HolonID: "metadata-harness", AgentType: "opencode", Status: string(holons.StatusExpired), CreatedAt: now, UpdatedAt: now}},
	}); err != nil {
		t.Fatal(err)
	}
	preferences := &harnessPreferencesStub{resolved: protocol.HarnessCodex}
	adapter := localMetadataHolons{holons: &terminalHolonService{Service: service}, harnesses: preferences}
	result, err := adapter.Resume(t.Context(), pullrequestmetadata.ResumeAgentSessionRequest{PullRequestID: "pr-1", SessionID: "metadata-harness", Prompt: "Again"})
	if err != nil {
		t.Fatal(err)
	}
	if len(preferences.validated) != 1 || preferences.validated[0] != protocol.HarnessOpenCode {
		t.Fatalf("validated = %v", preferences.validated)
	}
	h, err := service.Get(t.Context(), result.ID)
	if err != nil || h.AgentSessions[len(h.AgentSessions)-1].AgentType != "opencode" {
		t.Fatalf("holon = %+v, %v", h, err)
	}
}

func TestMetadataFollowUpValidationFailurePreservesArtifact(t *testing.T) {
	service, store := terminalTestService(t)
	worktree := t.TempDir()
	artifact := filepath.Join(worktree, pullrequestmetadata.MetadataArtifactPath)
	if err := os.MkdirAll(filepath.Dir(artifact), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte(`{"title":"Old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.Create(t.Context(), holons.Holon{ID: "metadata-invalid", Title: "Metadata", Kind: holons.KindPRMetadata, Status: holons.StatusExpired, BaseCommit: "head", WorktreeBranch: "holark/metadata", WorktreePath: worktree, PullRequestID: "pr-1", CreatedAt: now, AgentSessions: []holons.AgentSession{{ID: "old", HolonID: "metadata-invalid", AgentType: "claude-code", Status: string(holons.StatusExpired), CreatedAt: now, UpdatedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("Claude Code unavailable")
	adapter := localMetadataHolons{holons: &terminalHolonService{Service: service}, harnesses: &harnessPreferencesStub{validateErr: want}}
	if _, err := adapter.Resume(t.Context(), pullrequestmetadata.ResumeAgentSessionRequest{PullRequestID: "pr-1", SessionID: "metadata-invalid", Prompt: "Again"}); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("artifact changed before validation: %v", err)
	}
}
