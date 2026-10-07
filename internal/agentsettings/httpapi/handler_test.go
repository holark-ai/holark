package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/protocol"
)

type storeStub struct{ value protocol.HarnessType }

func (s *storeStub) LoadPreference(ctx context.Context, workflow agentsettings.Workflow) (agentsettings.Default, error) {
	value, explicit, err := s.LoadDefault(ctx, workflow)
	return agentsettings.Default{HarnessType: value, Explicit: explicit}, err
}
func (s *storeStub) SavePreference(ctx context.Context, workflow agentsettings.Workflow, value agentsettings.Default) error {
	return s.SaveDefault(ctx, workflow, value.HarnessType)
}

func (s *storeStub) LoadDefault(context.Context, agentsettings.Workflow) (protocol.HarnessType, bool, error) {
	return s.value, s.value != "", nil
}
func (s *storeStub) SaveDefault(_ context.Context, _ agentsettings.Workflow, value protocol.HarnessType) error {
	s.value = value
	return nil
}
func (s *storeStub) DeleteDefault(context.Context, agentsettings.Workflow) error {
	s.value = ""
	return nil
}

type proberStub []protocol.HarnessCapability

func (p proberStub) Probe(context.Context) []protocol.HarnessCapability { return p }

func TestUpdateDefaultHarnessRejectsUnavailableWithProbeReason(t *testing.T) {
	store := &storeStub{}
	service := agentsettings.New(store, proberStub{{Type: protocol.HarnessClaudeCode, UnavailableReason: "claude was not found"}})
	mux := http.NewServeMux()
	RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, service)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings/default-agent-harness", strings.NewReader(`{"harness_type":"claude-code"}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "claude was not found") || store.value != "" {
		t.Fatalf("response = %d %s, saved = %q", response.Code, response.Body.String(), store.value)
	}
}

func TestUpdateDefaultHarnessSavesAvailableSelection(t *testing.T) {
	store := &storeStub{}
	service := agentsettings.New(store, proberStub{{Type: protocol.HarnessOpenCode, Available: true}})
	mux := http.NewServeMux()
	RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, service)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings/default-agent-harness", strings.NewReader(`{"harness_type":"opencode"}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || store.value != protocol.HarnessOpenCode || !strings.Contains(response.Body.String(), `"default_harness":"opencode"`) || !strings.Contains(response.Body.String(), `"default_harness_explicit":true`) {
		t.Fatalf("response = %d %s, saved = %q", response.Code, response.Body.String(), store.value)
	}
}

func TestUpdateWorkflowHarnessReturnsWorkflowPreference(t *testing.T) {
	store := &storeStub{}
	service := agentsettings.New(store, proberStub{{Type: protocol.HarnessOpenCode, Available: true, AutomatedWorkflows: true}})
	mux := http.NewServeMux()
	RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, service)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings/agent-harness-defaults/issue", strings.NewReader(`{"harness_type":"opencode"}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || store.value != protocol.HarnessOpenCode || !strings.Contains(response.Body.String(), `"harness_type":"opencode"`) || !strings.Contains(response.Body.String(), `"explicit":true`) {
		t.Fatalf("response = %d %s, saved = %q", response.Code, response.Body.String(), store.value)
	}
}
