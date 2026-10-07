package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/agentsettings"
	settingshttp "github.com/holark-ai/holark/internal/agentsettings/httpapi"
	"github.com/holark-ai/holark/internal/agentsettings/sqliteadapter"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/protocol"
)

type availableAgents struct{}

func (availableAgents) Probe(context.Context) []protocol.HarnessCapability {
	return []protocol.HarnessCapability{{Type: protocol.HarnessCodex, Available: true, AutomatedWorkflows: true}, {Type: protocol.HarnessClaudeCode, Available: true, AutomatedWorkflows: true}}
}

func TestModelPreferencesPersistInheritResetAndValidate(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(filepath.Join(t.TempDir(), "settings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := sqliteadapter.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	service := agentsettings.New(store, availableAgents{})
	mux := http.NewServeMux()
	settingshttp.RegisterRoutes(func(path string, handler http.HandlerFunc) { mux.HandleFunc(path, handler) }, service)
	request := func(method, workflow, body string, want int) {
		t.Helper()
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(method, "/api/v1/settings/agent-harness-defaults/"+workflow, strings.NewReader(body)))
		if response.Code != want {
			t.Fatalf("response: %d %s", response.Code, response.Body.String())
		}
	}
	check := func(workflow agentsettings.Workflow, agent protocol.HarnessType, model string, explicit bool) {
		t.Helper()
		// Reopen the store/service to ensure the assertion reads durable state.
		reopened, err := sqliteadapter.New(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		got, err := agentsettings.New(reopened, availableAgents{}).Current(ctx, workflow)
		if err != nil || got.HarnessType != agent || got.Model != model || got.Explicit != explicit {
			t.Fatalf("preference: %+v, %v", got, err)
		}
	}
	request("PUT", "default", `{"harness_type":"codex","model":"custom/provider-model"}`, 200)
	check(agentsettings.WorkflowIssue, protocol.HarnessCodex, "custom/provider-model", false)
	request("PUT", "issue", `{"harness_type":"codex","model":"another-model"}`, 200)
	check(agentsettings.WorkflowIssue, protocol.HarnessCodex, "another-model", true)
	for _, body := range []string{`{"harness_type":"codex","model":"bad\nmodel"}`, `{"harness_type":"codex","model":" "}`, `{"harness_type":"codex","model":"` + strings.Repeat("x", 257) + `"}`, `{"harness_type":"unknown","model":"custom"}`} {
		request("PUT", "issue", body, 400)
		check(agentsettings.WorkflowIssue, protocol.HarnessCodex, "another-model", true)
	}
	resolved, err := service.Resolve(ctx, agentsettings.WorkflowIssue, protocol.HarnessCodex)
	if err != nil || resolved.Model != "another-model" {
		t.Fatalf("matching explicit agent: %+v %v", resolved, err)
	}
	resolved, err = service.Resolve(ctx, agentsettings.WorkflowIssue, protocol.HarnessClaudeCode)
	if err != nil || resolved.Model != "" {
		t.Fatalf("different explicit agent: %+v %v", resolved, err)
	}
	request("PUT", "issue", `{"harness_type":"claude-code"}`, 200)
	check(agentsettings.WorkflowIssue, protocol.HarnessClaudeCode, "", true)
	request("DELETE", "issue", "", 204)
	check(agentsettings.WorkflowIssue, protocol.HarnessCodex, "custom/provider-model", false)
	var keys int
	if err := db.QueryRowContext(ctx, `select count(*) from agent_settings where key like 'default_agent_harness.issue%'`).Scan(&keys); err != nil || keys != 0 {
		t.Fatalf("reset keys: %d %v", keys, err)
	}
}
