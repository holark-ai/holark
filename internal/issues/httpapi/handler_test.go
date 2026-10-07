package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/issues"
	issueworkflow "github.com/holark-ai/holark/internal/issues/workflow"
)

type testRateLimitError struct{ error }

func (testRateLimitError) RateLimited() bool { return true }

func TestIssueRoutesUseOnlyWorkflowFacade(t *testing.T) {
	workflow := &fakeWorkflow{issue: issues.Issue{
		ID: "issue-one", RepositoryID: "project", Title: "Bug", Status: issues.IssueOpen,
		Labels: []issues.Label{}, AssigneeHolarkIDs: []string{},
		LinkedPullRequestIDs: []string{},
	}}
	mux := routes(workflow)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/issues", nil))
	if response.Code != http.StatusOK || workflow.listSlug != "project" {
		t.Fatalf("list status=%d body=%s slug=%q", response.Code, response.Body, workflow.listSlug)
	}

	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/issues", strings.NewReader(`{"title":" Bug ","body":" Details "}`))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || workflow.createdTitle != "Bug" || workflow.createdBody != "Details" {
		t.Fatalf("create status=%d body=%s workflow=%#v", response.Code, response.Body, workflow)
	}
}

func TestProjectScopedIssueAndLabelAliasesAreUnavailable(t *testing.T) {
	mux := routes(&fakeWorkflow{})
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/projects/project/issues", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/projects/project/issues/sync", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/projects/project/labels", nil),
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d", request.Method, request.URL.Path, response.Code)
		}
	}
}

func TestPatchIssueValidatesFieldsAndReturnsCanonicalProjection(t *testing.T) {
	workflow := &fakeWorkflow{issue: issues.Issue{
		ID: "issue-one", RepositoryID: "project", Title: "Old", Body: "Old body",
		Labels: []issues.Label{}, AssigneeHolarkIDs: []string{},
		LinkedPullRequestIDs: []string{},
	}}
	mux := routes(workflow)
	for _, body := range []string{`{}`, `{"unknown":true}`, `{"title":"   "}`} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPatch, "/api/v1/issues/issue-one", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %s status=%d response=%s", body, response.Code, response.Body)
		}
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/issues/issue-one", strings.NewReader(`{"title":" New title ","body":""}`))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	if workflow.updateTitle == nil || *workflow.updateTitle != "New title" ||
		workflow.updateBody == nil || *workflow.updateBody != "" {
		t.Fatalf("update title=%v body=%v", workflow.updateTitle, workflow.updateBody)
	}
}

func TestIssueAssigneeRoutesRejectInvalidPayloads(t *testing.T) {
	workflow := &fakeWorkflow{issue: issues.Issue{
		ID: "issue-one", RepositoryID: "project", Title: "Bug", Status: issues.IssueOpen,
		Labels: []issues.Label{}, AssigneeHolarkIDs: []string{}, LinkedPullRequestIDs: []string{},
	}}
	mux := routes(workflow)

	for _, test := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/v1/issues/issue-one/assignees", `{}`},
		{http.MethodPut, "/api/v1/issues/issue-one/assignees", `{"assignee_holark_ids":null}`},
		{http.MethodPut, "/api/v1/issues/issue-one/assignees", `{"assignee_holark_ids":[" "]}`},
		{http.MethodPut, "/api/v1/issues/issue-one/assignees", `{"assignee_holark_ids":[],"unknown":true}`},
		{http.MethodPut, "/api/v1/issues/issue-one/assignees", `{"assignee_holark_ids":[]} {}`},
		{http.MethodPost, "/api/v1/issues/issue-one/assignees", `{}`},
		{http.MethodPost, "/api/v1/issues/issue-one/assignees", `{"holark_id":null}`},
		{http.MethodPost, "/api/v1/issues/issue-one/assignees", `{"holark_id":" "}`},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		request.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("%s %s body=%s status=%d response=%s", test.method, test.path, test.body, response.Code, response.Body)
		}
	}
}

func TestIssueAssigneeErrorsUseStableContracts(t *testing.T) {
	for _, test := range []struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		{issueworkflow.ErrIssueAssigneesReadOnly, http.StatusConflict, "issue_assignees_read_only"},
		{issueworkflow.ErrProviderIdentityRequired, http.StatusConflict, "provider_identity_required"},
		{issueworkflow.ErrProjectMemberUnresolved, http.StatusConflict, "project_member_unresolved"},
		{issueworkflow.ErrIssueSourceUnavailable, http.StatusServiceUnavailable, "gh_unavailable"},
		{issueworkflow.ErrIssueSourceFailed, http.StatusBadGateway, "github_sync_failed"},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/issues/issue-one/assignees", strings.NewReader(`{"holark_id":"member-a"}`))
		request.Header.Set("Content-Type", "application/json")
		routes(&fakeWorkflow{err: test.err}).ServeHTTP(response, request)
		if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
			t.Fatalf("error=%v status=%d body=%s", test.err, response.Code, response.Body)
		}
	}
}

func TestIssueSnapshotReturnsOnlyTaskFieldsAndRemovedSessionRouteIsUnavailable(t *testing.T) {
	workflow := &fakeWorkflow{issue: issues.Issue{
		ID: "issue-one", RepositoryID: "project", Title: "Bug", Body: "Details",
		Status: issues.IssueOpen, SyncData: json.RawMessage(`{"private":"projection metadata"}`),
		Labels: []issues.Label{{ID: "label-one", Name: "bug"}}, LinkedPullRequestIDs: []string{"pr-one"},
	}}
	mux := routes(workflow)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/issues/issue-one/snapshot", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("snapshot status=%d body=%s", response.Code, response.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"issue_id": "issue-one", "title": "Bug", "body": "Details"}
	if _, ok := body["discussion"].(map[string]any); !ok {
		t.Fatal("missing discussion context")
	}
	delete(body, "discussion")
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("snapshot body=%#v, want %#v", body, want)
	}
	if workflow.snapshotCalls != 1 {
		t.Fatalf("snapshot calls=%d", workflow.snapshotCalls)
	}

	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/issues/issue-one/sessions", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("removed session route status=%d body=%s", response.Code, response.Body)
	}
}

func TestIssueSnapshotUsesStableMissingIssueResponse(t *testing.T) {
	workflow := &fakeWorkflow{err: issues.ErrIssueNotFound}
	response := httptest.NewRecorder()
	routes(workflow).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/issues/missing/snapshot", nil))
	if response.Code != http.StatusNotFound || response.Body.String() != "{\"code\":\"issue_not_found\",\"message\":\"Issue not found.\"}\n" {
		t.Fatalf("snapshot status=%d body=%s", response.Code, response.Body)
	}
}

func TestIssueResponsesDoNotExposeLinkedSessionIDs(t *testing.T) {
	workflow := &fakeWorkflow{issue: issues.Issue{ID: "issue-one", RepositoryID: "project", Title: "Bug"}}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/issues", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/issues/issue-one", nil),
	} {
		response := httptest.NewRecorder()
		routes(workflow).ServeHTTP(response, request)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "linked_session_ids") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body)
		}
	}
}

func TestMutationProjectionPendingUsesStableAcceptedContract(t *testing.T) {
	workflow := &fakeWorkflow{err: &issueworkflow.ProjectionPendingError{
		GitHubIssueURL: "https://github.com/o/r/issues/42", GitHubIssueNumber: 42,
		Operation: issueworkflow.MutationClose, Err: errors.New("database down"),
	}}
	mux := routes(workflow)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/issues/issue-one/close", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "issue_projection_pending" || body["safe_to_retry"] != false ||
		body["reconciliation_required"] != true || body["github_issue_number"] != float64(42) {
		t.Fatalf("body=%#v", body)
	}
	if _, exposed := body["operation"]; exposed {
		t.Fatalf("operation was exposed in response: %#v", body)
	}
}

func TestWorkflowSourceErrorsUseStableHTTPContracts(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "unsupported repository", err: issueworkflow.ErrRepositoryUnsupported, wantStatus: http.StatusConflict, wantBody: "{\"code\":\"unsupported_repository\",\"message\":\"Project repository is not a supported GitHub repository.\"}\n"},
		{name: "unavailable source", err: issueworkflow.ErrIssueSourceUnavailable, wantStatus: http.StatusServiceUnavailable, wantBody: "{\"code\":\"gh_unavailable\",\"message\":\"GitHub sync failed. Make sure gh is installed and authenticated, then try again.\"}\n"},
		{name: "failed source", err: issueworkflow.ErrIssueSourceFailed, wantStatus: http.StatusBadGateway, wantBody: "{\"code\":\"github_sync_failed\",\"message\":\"GitHub sync failed.\"}\n"},
		{name: "rate limited", err: fmt.Errorf("%w: %w", issueworkflow.ErrIssueSourceFailed, testRateLimitError{errors.New("limited")}), wantStatus: http.StatusTooManyRequests, wantBody: "{\"code\":\"github_rate_limit_exceeded\",\"message\":\"GitHub API rate limit exceeded. Try again after the limit resets.\"}\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/issues", strings.NewReader(`{"title":"Bug","body":"Details"}`))
			request.Header.Set("Content-Type", "application/json")
			routes(&fakeWorkflow{err: fmt.Errorf("workflow transport: %w", test.err)}).ServeHTTP(response, request)
			if response.Code != test.wantStatus || response.Body.String() != test.wantBody {
				t.Fatalf("status=%d body=%q, want status=%d body=%q", response.Code, response.Body.String(), test.wantStatus, test.wantBody)
			}
		})
	}
}

func routes(workflow Workflow) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, Options{Workflow: workflow, RepositoryID: "project"})
	return mux
}

type fakeWorkflow struct {
	issue         issues.Issue
	err           error
	listSlug      string
	createdTitle  string
	createdBody   string
	updateTitle   *string
	updateBody    *string
	snapshotCalls int
}

func (workflow *fakeWorkflow) ReplaceAssignees(context.Context, string, []string) (issues.Issue, error) {
	return workflow.issue, workflow.err
}

func (workflow *fakeWorkflow) AddAssignee(context.Context, string, string) (issues.Issue, error) {
	return workflow.issue, workflow.err
}

func (workflow *fakeWorkflow) RemoveAssignee(context.Context, string, string) (issues.Issue, error) {
	return workflow.issue, workflow.err
}

func (workflow *fakeWorkflow) List(_ context.Context, slug string) ([]issues.Issue, error) {
	workflow.listSlug = slug
	if workflow.err != nil {
		return nil, workflow.err
	}
	return []issues.Issue{workflow.issue}, nil
}

func (workflow *fakeWorkflow) Get(context.Context, string) (issues.Issue, error) {
	return workflow.issue, workflow.err
}

func (workflow *fakeWorkflow) Snapshot(context.Context, string) (issueworkflow.Snapshot, error) {
	workflow.snapshotCalls++
	if workflow.err != nil {
		return issueworkflow.Snapshot{}, workflow.err
	}
	return issueworkflow.Snapshot{
		IssueID: workflow.issue.ID, RepositoryID: workflow.issue.RepositoryID,
		Title: workflow.issue.Title, Body: workflow.issue.Body,
	}, nil
}

func (workflow *fakeWorkflow) Create(_ context.Context, _ string, title, body string) (issues.Issue, error) {
	workflow.createdTitle, workflow.createdBody = title, body
	return workflow.issue, workflow.err
}

func (workflow *fakeWorkflow) Update(_ context.Context, _ string, title, body *string) (issues.Issue, error) {
	workflow.updateTitle, workflow.updateBody = title, body
	if title != nil {
		workflow.issue.Title = *title
	}
	if body != nil {
		workflow.issue.Body = *body
	}
	return workflow.issue, workflow.err
}

func (workflow *fakeWorkflow) Close(context.Context, string) (issues.Issue, error) {
	return workflow.issue, workflow.err
}

func (workflow *fakeWorkflow) Reopen(context.Context, string) (issues.Issue, error) {
	return workflow.issue, workflow.err
}

func (workflow *fakeWorkflow) Sync(context.Context, string) (issueworkflow.SyncResult, error) {
	return issueworkflow.SyncResult{Issues: []issues.Issue{}, SyncedAt: time.Now().UTC()}, workflow.err
}
