package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
)

type Service interface {
	Create(context.Context, pullrequestcomments.CreateComment) (pullrequestcomments.Comment, error)
	PublishDrafts(context.Context, string, string, []string) ([]pullrequestcomments.Comment, error)
	ListByPullRequest(context.Context, string) ([]pullrequestcomments.Comment, error)
	Sync(context.Context, string) ([]pullrequestcomments.Comment, error)
	Refresh(context.Context, string, bool) (pullrequestcomments.RefreshResult, error)
	RefreshStatus(context.Context, string) (pullrequestcomments.RefreshStatus, error)
	UpdateBody(context.Context, string, string) (pullrequestcomments.Comment, error)
	Delete(context.Context, string) error
	Resolve(context.Context, string) (pullrequestcomments.Comment, error)
	Reopen(context.Context, string) (pullrequestcomments.Comment, error)
}

type TargetReader interface {
	GetCommentTarget(context.Context, string) (pullrequestcomments.PullRequestTarget, error)
}

type RegisterFunc func(string, http.HandlerFunc)

func RegisterRoutes(register RegisterFunc, service Service, targets TargetReader) {
	handler := &handler{service: service, targets: targets}
	register("GET /api/v1/pull-requests/{id}/comments", handler.list)
	register("POST /api/v1/pull-requests/{id}/comments/sync", handler.sync)
	register("POST /api/v1/pull-requests/{id}/comments/refresh", handler.refresh)
	register("GET /api/v1/pull-requests/{id}/comments/refresh", handler.refreshStatus)
	register("POST /api/v1/pull-requests/{id}/comments", handler.create)
	register("POST /api/v1/pull-requests/{id}/comments/publish", handler.publishDrafts)
	register("PATCH /api/v1/pull-request-comments/{id}", handler.update)
	register("DELETE /api/v1/pull-request-comments/{id}", handler.delete)
	register("POST /api/v1/pull-request-comments/{id}/resolve", handler.resolve)
	register("POST /api/v1/pull-request-comments/{id}/reopen", handler.reopen)
}

type handler struct {
	service Service
	targets TargetReader
}

func (handler *handler) list(w http.ResponseWriter, r *http.Request) {
	if _, err := handler.targets.GetCommentTarget(r.Context(), r.PathValue("id")); err != nil {
		writeCommentError(w, err)
		return
	}
	comments, err := handler.service.ListByPullRequest(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCommentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, comments)
}

func (handler *handler) sync(w http.ResponseWriter, r *http.Request) {
	if _, err := handler.targets.GetCommentTarget(r.Context(), r.PathValue("id")); err != nil {
		writeCommentError(w, err)
		return
	}
	comments, err := handler.service.Sync(r.Context(), r.PathValue("id"))
	if err != nil {
		comments, err = handler.service.ListByPullRequest(r.Context(), r.PathValue("id"))
	}
	if err != nil {
		writeCommentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, comments)
}

func (handler *handler) refreshStatus(w http.ResponseWriter, r *http.Request) {
	status, err := handler.service.RefreshStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCommentError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, status)
}

func (handler *handler) refresh(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Force bool `json:"force"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, protocol.MaxMessageBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The refresh request is invalid.")
		return
	}
	result, err := handler.service.Refresh(r.Context(), r.PathValue("id"), request.Force)
	if errors.Is(err, pullrequestcomments.ErrPullRequestNotFound) {
		writeCommentError(w, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "comment_refresh_failed", "Could not refresh comments. Saved comments are still available.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (handler *handler) create(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeCreateRequest(w, r)
	if !ok {
		return
	}
	comment, err := handler.service.Create(r.Context(), pullrequestcomments.CreateComment{
		PullRequestID: r.PathValue("id"), ParentCommentID: request.ParentCommentID,
		Scope: request.Scope, Path: request.Path, OldPath: request.OldPath,
		Side: request.Side, Line: request.Line, DiffHunk: request.DiffHunk,
		Body: request.Body, Origin: pullrequestcomments.UserOrigin(),
		Draft: request.Draft,
	})
	if err != nil {
		writeCommentError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, comment)
}

type createRequest struct {
	Draft           bool                      `json:"draft"`
	Body            string                    `json:"body"`
	ParentCommentID string                    `json:"parent_comment_id"`
	Scope           pullrequestcomments.Scope `json:"scope"`
	Path            string                    `json:"path"`
	OldPath         string                    `json:"old_path"`
	Side            string                    `json:"side"`
	Line            *int                      `json:"line"`
	DiffHunk        string                    `json:"diff_hunk"`
	fields          map[string]json.RawMessage
}

func (handler *handler) publishDrafts(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "json_required", "A JSON request is required.")
		return
	}
	var request struct {
		HeadCommit string   `json:"head_commit"`
		CommentIDs []string `json:"comment_ids"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, protocol.MaxMessageBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeCommentError(w, pullrequestcomments.ErrInvalidComment)
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeCommentError(w, pullrequestcomments.ErrInvalidComment)
		return
	}
	comments, err := handler.service.PublishDrafts(r.Context(), r.PathValue("id"), request.HeadCommit, request.CommentIDs)
	if err != nil {
		writeCommentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, comments)
}

// Track presence as well as values: even empty or null location fields conflict
// with general comments and replies.
func (request *createRequest) UnmarshalJSON(data []byte) error {
	type plain createRequest
	var value plain
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*request = createRequest(value)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	request.fields = make(map[string]json.RawMessage, len(fields))
	for name, value := range fields {
		request.fields[strings.ToLower(name)] = value
	}
	return nil
}

func (request createRequest) validLocation() bool {
	has := func(names ...string) bool {
		for _, name := range names {
			if _, ok := request.fields[name]; ok {
				return true
			}
		}
		return false
	}
	if request.ParentCommentID != "" {
		return !has("scope", "path", "old_path", "side", "line", "diff_hunk")
	}
	switch request.Scope {
	case "":
		return !has("scope", "path", "old_path", "side", "line", "diff_hunk")
	case pullrequestcomments.ScopePullRequest:
		return !has("path", "old_path", "side", "line", "diff_hunk")
	case pullrequestcomments.ScopeFile:
		return strings.TrimSpace(request.Path) != "" && !has("side", "line", "diff_hunk")
	case pullrequestcomments.ScopeLine:
		return strings.TrimSpace(request.Path) != "" && request.Line != nil && *request.Line > 0 && (request.Side == "LEFT" || request.Side == "RIGHT")
	default:
		return false
	}
}

func decodeCreateRequest(w http.ResponseWriter, r *http.Request) (createRequest, bool) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "json_required", "A JSON request is required.")
		return createRequest{}, false
	}
	var request createRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, protocol.MaxMessageBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The comment request is invalid.")
		return createRequest{}, false
	}
	request.Body = strings.TrimSpace(request.Body)
	request.ParentCommentID = strings.TrimSpace(request.ParentCommentID)
	if request.Body == "" || len(request.Body) > pullrequestcomments.MaximumBodyLength || !request.validLocation() {
		writeError(w, http.StatusBadRequest, "invalid_request", "The comment request is invalid.")
		return createRequest{}, false
	}
	return request, true
}

func (handler *handler) update(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	comment, err := handler.service.UpdateBody(r.Context(), r.PathValue("id"), body)
	if err != nil {
		writeCommentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, comment)
}

func (handler *handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := handler.service.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeCommentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (handler *handler) resolve(w http.ResponseWriter, r *http.Request) {
	comment, err := handler.service.Resolve(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCommentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, comment)
}

func (handler *handler) reopen(w http.ResponseWriter, r *http.Request) {
	comment, err := handler.service.Reopen(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCommentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, comment)
}

func decodeBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "json_required", "A JSON request is required.")
		return "", false
	}
	var request struct {
		Body string `json:"body"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, protocol.MaxMessageBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The comment request is invalid.")
		return "", false
	}
	body := strings.TrimSpace(request.Body)
	if body == "" || len(body) > pullrequestcomments.MaximumBodyLength {
		writeError(w, http.StatusBadRequest, "invalid_request", "The comment request is invalid.")
		return "", false
	}
	return body, true
}

func writeCommentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pullrequestcomments.ErrReadOnly):
		writeError(w, http.StatusConflict, "read_only", err.Error())
	case errors.Is(err, pullrequestcomments.ErrStaleDraft):
		writeError(w, http.StatusConflict, "stale_head", err.Error())
	case errors.Is(err, pullrequestcomments.ErrComparisonNotReady):
		writeError(w, http.StatusConflict, "comparison_not_ready", err.Error())
	case errors.Is(err, pullrequestcomments.ErrPullRequestNotFound):
		writeError(w, http.StatusNotFound, "pull_request_not_found", "Pull request not found.")
	case errors.Is(err, pullrequestcomments.ErrCommentNotFound):
		writeError(w, http.StatusNotFound, "pull_request_comment_not_found", "Pull request comment not found.")
	case errors.Is(err, pullrequestcomments.ErrInvalidComment), errors.Is(err, pullrequestcomments.ErrInvalidParent):
		writeError(w, http.StatusBadRequest, "invalid_request", "The comment request is invalid.")
	default:
		writeError(w, http.StatusInternalServerError, "pull_request_comment_failed", "The pull request comment could not be updated.")
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
