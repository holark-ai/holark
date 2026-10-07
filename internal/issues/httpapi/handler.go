package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	githubratelimit "github.com/holark-ai/holark/internal/codehost/github/ratelimit"
	"github.com/holark-ai/holark/internal/issues"
	"github.com/holark-ai/holark/internal/issues/comments"
	issueworkflow "github.com/holark-ai/holark/internal/issues/workflow"
)

type Workflow interface {
	List(context.Context, string) ([]issues.Issue, error)
	Get(context.Context, string) (issues.Issue, error)
	Snapshot(context.Context, string) (issueworkflow.Snapshot, error)
	Create(context.Context, string, string, string) (issues.Issue, error)
	Update(context.Context, string, *string, *string) (issues.Issue, error)
	Close(context.Context, string) (issues.Issue, error)
	Reopen(context.Context, string) (issues.Issue, error)
	ReplaceAssignees(context.Context, string, []string) (issues.Issue, error)
	AddAssignee(context.Context, string, string) (issues.Issue, error)
	RemoveAssignee(context.Context, string, string) (issues.Issue, error)
	Sync(context.Context, string) (issueworkflow.SyncResult, error)
}

type LabelWorkflow interface {
	ListLabels(context.Context, string) ([]issues.Label, error)
	CreateLabel(context.Context, string, issueworkflow.CreateLabelRequest) (issues.Label, error)
	AddLabels(context.Context, string, []string) (issues.Issue, error)
	RemoveLabels(context.Context, string, []string) (issues.Issue, error)
}

type RegisterFunc func(pattern string, handler http.HandlerFunc)

const maxRequestBytes = 16 * 1024 * 1024

type Options struct {
	Workflow     Workflow
	RepositoryID string
}

func RegisterRoutes(register RegisterFunc, options Options) {
	handler := &handler{options: options}
	register("GET /api/v1/issues", handler.listIssues)
	register("POST /api/v1/issues", handler.createIssue)
	register("POST /api/v1/issues/sync", handler.syncIssues)
	register("GET /api/v1/labels", handler.listLabels)
	register("POST /api/v1/labels", handler.createLabel)
	register("GET /api/v1/issues/{id}", handler.getIssue)
	register("GET /api/v1/issues/{id}/comments", handler.listComments)
	register("POST /api/v1/issues/{id}/comments/sync", handler.syncComments)
	register("POST /api/v1/issues/{id}/comments", handler.createComment)
	register("PATCH /api/v1/issue-comments/{id}", handler.updateComment)
	register("DELETE /api/v1/issue-comments/{id}", handler.deleteComment)
	register("POST /api/v1/issues/{id}/labels/add", handler.addLabels)
	register("POST /api/v1/issues/{id}/labels/remove", handler.removeLabels)
	register("POST /api/v1/issues/{id}/snapshot", handler.snapshotIssue)
	register("PATCH /api/v1/issues/{id}", handler.updateIssue)
	register("POST /api/v1/issues/{id}/close", handler.closeIssue)
	register("POST /api/v1/issues/{id}/reopen", handler.reopenIssue)
	register("PUT /api/v1/issues/{id}/assignees", handler.replaceAssignees)
	register("POST /api/v1/issues/{id}/assignees", handler.addAssignee)
	register("DELETE /api/v1/issues/{id}/assignees/{memberID}", handler.removeAssignee)
}

type handler struct{ options Options }

func (handler *handler) project(_ *http.Request) string {
	return strings.TrimSpace(handler.options.RepositoryID)
}

func (handler *handler) labelWorkflow(w http.ResponseWriter) (LabelWorkflow, bool) {
	workflow, ok := handler.options.Workflow.(LabelWorkflow)
	if !ok {
		writeError(w, http.StatusInternalServerError, "issue_failed", "Labels are unavailable.")
	}
	return workflow, ok
}

func (handler *handler) listLabels(w http.ResponseWriter, r *http.Request) {
	workflow, ok := handler.labelWorkflow(w)
	if !ok {
		return
	}
	labels, err := workflow.ListLabels(r.Context(), handler.project(r))
	if err != nil {
		writeWorkflowError(w, err, "Labels could not be listed.")
		return
	}
	if labels == nil {
		labels = []issues.Label{}
	}
	writeJSON(w, http.StatusOK, labels)
}

func (handler *handler) createLabel(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var request struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	workflow, ok := handler.labelWorkflow(w)
	if !ok {
		return
	}
	label, err := workflow.CreateLabel(r.Context(), handler.project(r), issueworkflow.CreateLabelRequest{
		Name: request.Name, Color: request.Color, Description: request.Description,
	})
	if err != nil {
		if writeProjectionPending(w, err) {
			return
		}
		writeWorkflowError(w, err, "The label could not be created.")
		return
	}
	writeJSON(w, http.StatusCreated, label)
}

func (handler *handler) addLabels(w http.ResponseWriter, r *http.Request) {
	handler.changeLabels(w, r, true)
}

func (handler *handler) removeLabels(w http.ResponseWriter, r *http.Request) {
	handler.changeLabels(w, r, false)
}

func (handler *handler) changeLabels(w http.ResponseWriter, r *http.Request, add bool) {
	if !requireJSON(w, r) {
		return
	}
	var request struct {
		Labels []string `json:"labels"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	workflow, ok := handler.labelWorkflow(w)
	if !ok {
		return
	}
	var (
		issue issues.Issue
		err   error
	)
	if add {
		issue, err = workflow.AddLabels(r.Context(), r.PathValue("id"), request.Labels)
	} else {
		issue, err = workflow.RemoveLabels(r.Context(), r.PathValue("id"), request.Labels)
	}
	if err != nil {
		if writeProjectionPending(w, err) {
			return
		}
		writeWorkflowError(w, err, "The issue labels could not be changed.")
		return
	}
	handler.writeIssue(w, http.StatusOK, issue)
}

func (handler *handler) listIssues(w http.ResponseWriter, r *http.Request) {
	listed, err := handler.options.Workflow.List(r.Context(), handler.project(r))
	if err != nil {
		writeWorkflowError(w, err, "Issues could not be listed.")
		return
	}
	writeJSON(w, http.StatusOK, handler.projectIssues(listed))
}

func (handler *handler) createIssue(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var request struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	title := strings.TrimSpace(request.Title)
	body := strings.TrimSpace(request.Body)
	if title == "" || utf8.RuneCountInString(title) > 160 || utf8.RuneCountInString(body) > 4000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "The issue request is invalid.")
		return
	}
	issue, err := handler.options.Workflow.Create(r.Context(), handler.project(r), title, body)
	if err != nil {
		if writeProjectionPending(w, err) {
			return
		}
		writeWorkflowError(w, err, "The issue could not be created.")
		return
	}
	handler.writeIssue(w, http.StatusCreated, issue)
}

func (handler *handler) updateIssue(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var request struct {
		Title optionalString `json:"title"`
		Body  optionalString `json:"body"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if !request.Title.Present && !request.Body.Present {
		writeError(w, http.StatusBadRequest, "invalid_request", "The issue request is invalid.")
		return
	}
	var title, body *string
	if request.Title.Present {
		value := strings.TrimSpace(request.Title.Value)
		title = &value
		if value == "" || utf8.RuneCountInString(value) > 160 {
			writeError(w, http.StatusBadRequest, "invalid_request", "The issue request is invalid.")
			return
		}
	}
	if request.Body.Present {
		body = &request.Body.Value
		if utf8.RuneCountInString(*body) > 4000 {
			writeError(w, http.StatusBadRequest, "invalid_request", "The issue request is invalid.")
			return
		}
	}
	issue, err := handler.options.Workflow.Update(r.Context(), r.PathValue("id"), title, body)
	if err != nil {
		if writeProjectionPending(w, err) {
			return
		}
		writeWorkflowError(w, err, "The issue could not be updated.")
		return
	}
	handler.writeIssue(w, http.StatusOK, issue)
}

type optionalString struct {
	Present bool
	Value   string
}

func (value *optionalString) UnmarshalJSON(data []byte) error {
	value.Present = true
	if string(data) == "null" {
		return errors.New("string value cannot be null")
	}
	if err := json.Unmarshal(data, &value.Value); err != nil {
		return err
	}
	return nil
}

func (handler *handler) syncIssues(w http.ResponseWriter, r *http.Request) {
	result, err := handler.options.Workflow.Sync(r.Context(), handler.project(r))
	if err != nil {
		writeSyncWorkflowError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, syncIssuesResponse{
		Issues: handler.projectIssues(result.Issues), Imported: result.Imported,
		Updated: result.Updated, Exported: 0, SyncedAt: result.SyncedAt,
	})
}

func (handler *handler) getIssue(w http.ResponseWriter, r *http.Request) {
	issue, err := handler.options.Workflow.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeWorkflowError(w, err, "Issue not found.")
		return
	}
	handler.writeIssue(w, http.StatusOK, issue)
}

func (handler *handler) snapshotIssue(w http.ResponseWriter, r *http.Request) {
	snapshot, err := handler.options.Workflow.Snapshot(r.Context(), r.PathValue("id"))
	if err != nil {
		writeWorkflowError(w, err, "Issue not found.")
		return
	}
	if strings.TrimSpace(handler.options.RepositoryID) != "" {
		writeJSON(w, http.StatusOK, struct {
			Discussion comments.Context `json:"discussion"`
			IssueID    string           `json:"issue_id"`
			Title      string           `json:"title"`
			Body       string           `json:"body"`
		}{snapshot.Discussion, snapshot.IssueID, snapshot.Title, snapshot.Body})
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (handler *handler) closeIssue(w http.ResponseWriter, r *http.Request) {
	handler.changeState(w, r, handler.options.Workflow.Close, "closed")
}

func (handler *handler) reopenIssue(w http.ResponseWriter, r *http.Request) {
	handler.changeState(w, r, handler.options.Workflow.Reopen, "reopened")
}

func (handler *handler) replaceAssignees(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var request struct {
		AssigneeHolarkIDs *[]string `json:"assignee_holark_ids"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.AssigneeHolarkIDs == nil {
		writeInvalidIssueRequest(w)
		return
	}
	ids, ok := normalizeAssigneeIDs(*request.AssigneeHolarkIDs)
	if !ok {
		writeInvalidIssueRequest(w)
		return
	}
	issue, err := handler.options.Workflow.ReplaceAssignees(r.Context(), r.PathValue("id"), ids)
	handler.writeAssigneeMutation(w, issue, err)
}

func (handler *handler) addAssignee(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var request struct {
		HolarkID *string `json:"holark_id"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.HolarkID == nil {
		writeInvalidIssueRequest(w)
		return
	}
	memberID := strings.TrimSpace(*request.HolarkID)
	if memberID == "" {
		writeInvalidIssueRequest(w)
		return
	}
	issue, err := handler.options.Workflow.AddAssignee(r.Context(), r.PathValue("id"), memberID)
	handler.writeAssigneeMutation(w, issue, err)
}

func (handler *handler) removeAssignee(w http.ResponseWriter, r *http.Request) {
	memberID := strings.TrimSpace(r.PathValue("memberID"))
	if memberID == "" {
		writeInvalidIssueRequest(w)
		return
	}
	issue, err := handler.options.Workflow.RemoveAssignee(r.Context(), r.PathValue("id"), memberID)
	handler.writeAssigneeMutation(w, issue, err)
}

func (handler *handler) writeAssigneeMutation(w http.ResponseWriter, issue issues.Issue, err error) {
	if err != nil {
		if writeProjectionPending(w, err) {
			return
		}
		writeWorkflowError(w, err, "Issue assignees could not be updated.")
		return
	}
	handler.writeIssue(w, http.StatusOK, issue)
}

func normalizeAssigneeIDs(values []string) ([]string, bool) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, false
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, true
}

func writeInvalidIssueRequest(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "invalid_request", "The issue request is invalid.")
}

func (handler *handler) changeState(w http.ResponseWriter, r *http.Request, change func(context.Context, string) (issues.Issue, error), verb string) {
	issue, err := change(r.Context(), r.PathValue("id"))
	if err != nil {
		if writeProjectionPending(w, err) {
			return
		}
		writeWorkflowError(w, err, "The issue could not be "+verb+".")
		return
	}
	handler.writeIssue(w, http.StatusOK, issue)
}

type issueResponse struct{ issues.Issue }
type singletonIssueResponse struct {
	issues.Issue
	RepositoryID *string `json:"repository_id,omitempty"`
}

func (handler *handler) writeIssue(w http.ResponseWriter, status int, issue issues.Issue) {
	if strings.TrimSpace(handler.options.RepositoryID) != "" {
		writeJSON(w, status, singletonIssueResponse{Issue: issue})
		return
	}
	writeJSON(w, status, issueResponse{Issue: issue})
}

type syncIssuesResponse struct {
	Issues   any       `json:"issues"`
	Imported int       `json:"imported"`
	Updated  int       `json:"updated"`
	Exported int       `json:"exported"`
	SyncedAt time.Time `json:"synced_at"`
}

func (handler *handler) projectIssues(source []issues.Issue) any {
	if strings.TrimSpace(handler.options.RepositoryID) != "" {
		result := make([]singletonIssueResponse, len(source))
		for index := range source {
			result[index] = singletonIssueResponse{Issue: source[index]}
		}
		return result
	}
	result := make([]issueResponse, len(source))
	for index := range source {
		result[index] = issueResponse{Issue: source[index]}
	}
	return result
}

func writeProjectionPending(w http.ResponseWriter, err error) bool {
	var labelPending *issueworkflow.LabelProjectionPendingError
	if errors.As(err, &labelPending) {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"code": "label_projection_pending", "message": "GitHub created the label, but Holark has not stored it yet.",
			"reconciliation_required": true, "safe_to_retry": false,
		})
		return true
	}
	var pending *issueworkflow.ProjectionPendingError
	if !errors.As(err, &pending) {
		return false
	}
	response := struct {
		Code                   string `json:"code"`
		Message                string `json:"message"`
		GitHubIssueURL         string `json:"github_issue_url,omitempty"`
		GitHubIssueNumber      int    `json:"github_issue_number,omitempty"`
		ReconciliationRequired bool   `json:"reconciliation_required"`
		SafeToRetry            bool   `json:"safe_to_retry"`
	}{
		Code:           "issue_projection_pending",
		Message:        "GitHub accepted the change, but Holark has not stored it yet.",
		GitHubIssueURL: pending.GitHubIssueURL, GitHubIssueNumber: pending.GitHubIssueNumber,
		ReconciliationRequired: true, SafeToRetry: pending.SafeToRetry,
	}
	writeJSON(w, http.StatusAccepted, response)
	return true
}
func writeSyncWorkflowError(w http.ResponseWriter, err error) {
	if errors.Is(err, issueworkflow.ErrProjectNotFound) {
		writeError(w, http.StatusNotFound, "project_not_found", "Project not found.")
		return
	}
	if isIssueSourceError(err) {
		writeIssueSourceError(w, err)
		return
	}
	log.Printf("issue sync failed: %v", err)
	writeError(w, http.StatusInternalServerError, "issue_sync_failed", "Issues could not be synced.")
}

func writeWorkflowError(w http.ResponseWriter, err error, message string) {
	switch {
	case errors.Is(err, issueworkflow.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, "project_not_found", "Project not found.")
	case errors.Is(err, issues.ErrIssueNotFound):
		writeError(w, http.StatusNotFound, "issue_not_found", "Issue not found.")
	case errors.Is(err, issueworkflow.ErrInvalidLabelRequest):
		writeError(w, http.StatusBadRequest, "invalid_request", "The label request is invalid.")
	case errors.Is(err, issueworkflow.ErrLabelNotFound):
		writeError(w, http.StatusNotFound, "label_not_found", "Label not found.")
	case errors.Is(err, issueworkflow.ErrLabelAlreadyExists):
		writeError(w, http.StatusConflict, "label_already_exists", "A label with that name already exists.")
	case errors.Is(err, issueworkflow.ErrIssueAssigneesReadOnly):
		writeError(w, http.StatusConflict, "issue_assignees_read_only", "Issue assignees are read-only when the issue is closed.")
	case errors.Is(err, issueworkflow.ErrProviderIdentityRequired):
		writeError(w, http.StatusConflict, "provider_identity_required", "A GitHub-backed issue is required.")
	case errors.Is(err, issueworkflow.ErrProjectMemberUnresolved):
		writeError(w, http.StatusConflict, "project_member_unresolved", "An assignee is not a member of this project.")
	case isIssueSourceError(err):
		writeIssueSourceError(w, err)
	default:
		log.Printf("issue workflow failed: %v", err)
		writeError(w, http.StatusInternalServerError, "issue_failed", message)
	}
}

func requireJSON(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "json_required", "A JSON request is required.")
		return false
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "The issue request is invalid.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write JSON response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

func isIssueSourceError(err error) bool {
	return errors.Is(err, issueworkflow.ErrRepositoryUnsupported) ||
		errors.Is(err, issueworkflow.ErrIssueSourceUnavailable) ||
		errors.Is(err, issueworkflow.ErrIssueSourceFailed)
}

func writeIssueSourceError(w http.ResponseWriter, err error) {
	status, code, message := issueSourceErrorResponse(err)
	if errors.Is(err, issueworkflow.ErrIssueSourceFailed) {
		log.Printf("github sync failed: %v", err)
	}
	writeError(w, status, code, message)
}

func issueSourceErrorResponse(err error) (int, string, string) {
	switch {
	case githubratelimit.Exceeded(err):
		return http.StatusTooManyRequests, "github_rate_limit_exceeded", "GitHub API rate limit exceeded. Try again after the limit resets."
	case errors.Is(err, issueworkflow.ErrRepositoryUnsupported):
		return http.StatusConflict, "unsupported_repository", "Project repository is not a supported GitHub repository."
	case errors.Is(err, issueworkflow.ErrIssueSourceUnavailable):
		return http.StatusServiceUnavailable, "gh_unavailable", "GitHub sync failed. Make sure gh is installed and authenticated, then try again."
	default:
		return http.StatusBadGateway, "github_sync_failed", "GitHub sync failed."
	}
}
