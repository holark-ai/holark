package agentsettings

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/protocol"
)

type testStore struct {
	values map[Workflow]protocol.HarnessType
}

func (s *testStore) LoadPreference(ctx context.Context, workflow Workflow) (Default, error) {
	value, explicit, err := s.LoadDefault(ctx, workflow)
	return Default{HarnessType: value, Explicit: explicit}, err
}
func (s *testStore) SavePreference(ctx context.Context, workflow Workflow, value Default) error {
	return s.SaveDefault(ctx, workflow, value.HarnessType)
}

func (s *testStore) LoadDefault(_ context.Context, workflow Workflow) (protocol.HarnessType, bool, error) {
	value, saved := s.values[workflow]
	return value, saved, nil
}
func (s *testStore) SaveDefault(_ context.Context, workflow Workflow, value protocol.HarnessType) error {
	if s.values == nil {
		s.values = make(map[Workflow]protocol.HarnessType)
	}
	s.values[workflow] = value
	return nil
}
func (s *testStore) DeleteDefault(_ context.Context, workflow Workflow) error {
	delete(s.values, workflow)
	return nil
}

type testProber []protocol.HarnessCapability

func (p testProber) Probe(context.Context) []protocol.HarnessCapability { return p }

func capability(harnessType protocol.HarnessType, available bool) protocol.HarnessCapability {
	return protocol.HarnessCapability{Type: harnessType, Available: available, AutomatedWorkflows: true, UnavailableReason: "not installed"}
}

func TestResolveDefaultPreferenceAndFallbackOrder(t *testing.T) {
	tests := []struct {
		name         string
		store        testStore
		capabilities testProber
		want         protocol.HarnessType
		wantError    error
	}{
		{name: "saved exact choice", store: testStore{values: map[Workflow]protocol.HarnessType{WorkflowDefault: protocol.HarnessOpenCode}}, capabilities: testProber{capability(protocol.HarnessCodex, true), capability(protocol.HarnessOpenCode, true)}, want: protocol.HarnessOpenCode},
		{name: "Codex first", capabilities: testProber{capability(protocol.HarnessClaudeCode, true), capability(protocol.HarnessCodex, true)}, want: protocol.HarnessCodex},
		{name: "Claude only", capabilities: testProber{capability(protocol.HarnessCodex, false), capability(protocol.HarnessClaudeCode, true)}, want: protocol.HarnessClaudeCode},
		{name: "OpenCode only", capabilities: testProber{capability(protocol.HarnessOpenCode, true)}, want: protocol.HarnessOpenCode},
		{name: "none", capabilities: testProber{capability(protocol.HarnessCodex, false)}, wantError: ErrNoAvailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := New(&test.store, test.capabilities)
			got, err := service.ResolveDefault(t.Context(), WorkflowAutomated)
			if !errors.Is(err, test.wantError) || got != test.want {
				t.Fatalf("ResolveDefault() = %q, %v; want %q, %v", got, err, test.want, test.wantError)
			}
		})
	}
}

func TestSavedAndExplicitUnavailableChoicesNeverFallBack(t *testing.T) {
	prober := testProber{capability(protocol.HarnessCodex, true), capability(protocol.HarnessClaudeCode, false)}
	store := &testStore{values: map[Workflow]protocol.HarnessType{WorkflowDefault: protocol.HarnessClaudeCode}}
	service := New(store, prober)
	if _, err := service.ResolveDefault(t.Context(), WorkflowManual); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "Settings") {
		t.Fatalf("saved unavailable error = %v", err)
	}
	if err := service.Validate(t.Context(), protocol.HarnessClaudeCode, WorkflowManual); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("explicit unavailable error = %v", err)
	}
}

func TestTaskDefaultOverridesAndThenReturnsToGeneralDefault(t *testing.T) {
	store := &testStore{values: map[Workflow]protocol.HarnessType{WorkflowDefault: protocol.HarnessClaudeCode}}
	service := New(store, testProber{capability(protocol.HarnessClaudeCode, true), capability(protocol.HarnessOpenCode, true)})

	inherited, err := service.Current(t.Context(), WorkflowPullRequestReview)
	if err != nil || inherited.HarnessType != protocol.HarnessClaudeCode || inherited.Explicit {
		t.Fatalf("inherited default = %+v, %v", inherited, err)
	}
	if err = service.SaveDefault(t.Context(), WorkflowPullRequestReview, protocol.HarnessOpenCode); err != nil {
		t.Fatal(err)
	}
	overridden, err := service.Current(t.Context(), WorkflowPullRequestReview)
	if err != nil || overridden.HarnessType != protocol.HarnessOpenCode || !overridden.Explicit {
		t.Fatalf("overridden default = %+v, %v", overridden, err)
	}
	if err = service.DeleteDefault(t.Context(), WorkflowPullRequestReview); err != nil {
		t.Fatal(err)
	}
	reset, err := service.Current(t.Context(), WorkflowPullRequestReview)
	if err != nil || reset.HarnessType != protocol.HarnessClaudeCode || reset.Explicit {
		t.Fatalf("reset default = %+v, %v", reset, err)
	}
}

func TestAutomatedContractDoesNotDisableManualUse(t *testing.T) {
	prober := testProber{{Type: protocol.HarnessOpenCode, Available: true, AutomatedWorkflows: false}}
	service := New(&testStore{}, prober)
	if err := service.Validate(t.Context(), protocol.HarnessOpenCode, WorkflowManual); err != nil {
		t.Fatal(err)
	}
	if err := service.Validate(t.Context(), protocol.HarnessOpenCode, WorkflowAutomated); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
}
