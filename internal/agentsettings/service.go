// Package agentsettings owns coding-agent preferences and shared launch commands.
package agentsettings

import (
	"context"
	"errors"
	"fmt"

	"github.com/holark-ai/holark/internal/protocol"
)

type Workflow string

const (
	WorkflowDefault             Workflow = "default"
	WorkflowIssue               Workflow = "issue"
	WorkflowPullRequestReview   Workflow = "pull_request_review"
	WorkflowPullRequestFeedback Workflow = "pull_request_feedback"
	WorkflowPullRequestMetadata Workflow = "pull_request_metadata"
	WorkflowPullRequestRebase   Workflow = "pull_request_rebase"

	// These aliases keep callers outside the task-specific flows source-compatible.
	WorkflowManual    Workflow = WorkflowDefault
	WorkflowAutomated Workflow = "automated"
)

var Workflows = []Workflow{
	WorkflowDefault,
	WorkflowIssue,
	WorkflowPullRequestReview,
	WorkflowPullRequestFeedback,
	WorkflowPullRequestMetadata,
	WorkflowPullRequestRebase,
}

var (
	ErrInvalid     = errors.New("invalid agent harness setting")
	ErrUnavailable = errors.New("agent harness is unavailable")
	ErrNoAvailable = errors.New("no agent harness is available")
)

type Store interface {
	LoadPreference(context.Context, Workflow) (Default, error)
	SavePreference(context.Context, Workflow, Default) error
	DeleteDefault(context.Context, Workflow) error
}

type Prober interface {
	Probe(context.Context) []protocol.HarnessCapability
}

type Default struct {
	Permissions string               `json:"permissions,omitempty"`
	Model       string               `json:"model,omitempty"`
	HarnessType protocol.HarnessType `json:"harness_type"`
	Explicit    bool                 `json:"explicit"`
}

type Service struct {
	store  Store
	prober Prober
}

func New(store Store, prober Prober) *Service { return &Service{store: store, prober: prober} }

func (s *Service) Current(ctx context.Context, workflow Workflow) (Default, error) {
	return s.current(ctx, workflow, s.prober.Probe(ctx))
}

// CurrentWithCapabilities derives an unsaved default from the response's capability snapshot.
func (s *Service) CurrentWithCapabilities(ctx context.Context, workflow Workflow, capabilities []protocol.HarnessCapability) (Default, error) {
	return s.current(ctx, workflow, capabilities)
}

func (s *Service) current(ctx context.Context, workflow Workflow, capabilities []protocol.HarnessCapability) (Default, error) {
	if !validWorkflow(workflow) {
		return Default{}, ErrInvalid
	}
	preference, err := s.store.LoadPreference(ctx, workflow)
	if err != nil {
		return Default{}, err
	}
	if preference.Explicit {
		return preference, nil
	}
	if workflow != WorkflowDefault {
		inherited, loadErr := s.store.LoadPreference(ctx, WorkflowDefault)
		if loadErr != nil {
			return Default{}, loadErr
		}
		if inherited.Explicit {
			inherited.Explicit = false
			return inherited, nil
		}
	}
	harnessType, err := resolveUnsaved(capabilities, workflow)
	return Default{HarnessType: harnessType}, err
}

func (s *Service) DefaultsWithCapabilities(ctx context.Context, capabilities []protocol.HarnessCapability) (map[Workflow]Default, error) {
	defaults := make(map[Workflow]Default, len(Workflows))
	for _, workflow := range Workflows {
		current, err := s.CurrentWithCapabilities(ctx, workflow, capabilities)
		if err != nil && !errors.Is(err, ErrNoAvailable) {
			return nil, err
		}
		if errors.Is(err, ErrNoAvailable) {
			current.HarnessType = protocol.HarnessCodex
		}
		defaults[workflow] = current
	}
	return defaults, nil
}

func (s *Service) ResolveDefault(ctx context.Context, workflow Workflow) (protocol.HarnessType, error) {
	current, err := s.Current(ctx, workflow)
	if err != nil {
		return "", err
	}
	if err = s.Validate(ctx, current.HarnessType, workflow); err != nil {
		return "", fmt.Errorf("saved default %s cannot be used; choose an available harness in Settings: %w", current.HarnessType, err)
	}
	return current.HarnessType, nil
}

func (s *Service) Validate(ctx context.Context, selected protocol.HarnessType, workflow Workflow) error {
	if selected == "" || !validWorkflow(workflow) {
		return ErrInvalid
	}
	for _, capability := range s.prober.Probe(ctx) {
		if capability.Type != selected {
			continue
		}
		if !capability.Available {
			reason := capability.UnavailableReason
			if reason == "" {
				reason = "the executable could not be used"
			}
			return fmt.Errorf("%w: %s: %s", ErrUnavailable, selected, reason)
		}
		if automatedWorkflow(workflow) && !capability.AutomatedWorkflows {
			return fmt.Errorf("%w: %s does not support automated workflows", ErrUnavailable, selected)
		}
		return nil
	}
	return fmt.Errorf("%w: %s is not registered", ErrInvalid, selected)
}

func (s *Service) SaveDefault(ctx context.Context, workflow Workflow, selected protocol.HarnessType) error {
	return s.SavePreference(ctx, workflow, selected, "")
}

func (s *Service) SavePreference(ctx context.Context, workflow Workflow, selected protocol.HarnessType, model string, permissions ...string) error {
	permission := ""
	if len(permissions) > 0 {
		permission = permissions[0]
	}
	if !protocol.ValidPermissions(selected, permission) {
		return fmt.Errorf("%w: invalid permission preset for %s", ErrInvalid, selected)
	}
	if !protocol.ValidModel(model) {
		return fmt.Errorf("%w: invalid model identifier", ErrInvalid)
	}
	if err := s.Validate(ctx, selected, workflow); err != nil {
		return err
	}
	return s.store.SavePreference(ctx, workflow, Default{HarnessType: selected, Model: model, Permissions: permission, Explicit: true})
}

func (s *Service) DeleteDefault(ctx context.Context, workflow Workflow) error {
	if !validWorkflow(workflow) || workflow == WorkflowDefault {
		return ErrInvalid
	}
	return s.store.DeleteDefault(ctx, workflow)
}

func resolveUnsaved(capabilities []protocol.HarnessCapability, workflow Workflow) (protocol.HarnessType, error) {
	for _, wanted := range []protocol.HarnessType{protocol.HarnessCodex, protocol.HarnessClaudeCode, protocol.HarnessOpenCode} {
		for _, capability := range capabilities {
			if capability.Type == wanted && capability.Available && (!automatedWorkflow(workflow) || capability.AutomatedWorkflows) {
				return wanted, nil
			}
		}
	}
	return "", ErrNoAvailable
}

func validWorkflow(workflow Workflow) bool {
	if workflow == WorkflowAutomated {
		return true
	}
	for _, candidate := range Workflows {
		if workflow == candidate {
			return true
		}
	}
	return false
}

func automatedWorkflow(workflow Workflow) bool { return workflow != WorkflowDefault }

// Resolve snapshots the harness, model, and permissions once. Choosing the
// effective workflow harness retains its preferences; another uses Holark defaults.
func (s *Service) Resolve(ctx context.Context, workflow Workflow, selected protocol.HarnessType) (Default, error) {
	current, err := s.Current(ctx, workflow)
	if err != nil {
		return Default{}, err
	}
	if selected != "" && selected != current.HarnessType {
		current = Default{HarnessType: selected}
	}
	if err := s.Validate(ctx, current.HarnessType, workflow); err != nil {
		return Default{}, err
	}
	return current, nil
}
