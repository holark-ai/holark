// Package httpapi owns pull request participant HTTP contracts.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	githubratelimit "github.com/holark-ai/holark/internal/codehost/github/ratelimit"
	"github.com/holark-ai/holark/internal/pullrequestparticipants"
)

type Service interface {
	Get(context.Context, string) (pullrequestparticipants.Snapshot, error)
	Sync(context.Context, string) (pullrequestparticipants.Snapshot, error)
	ReplaceAssignees(context.Context, string, []string) (pullrequestparticipants.Snapshot, error)
	AddAssignee(context.Context, string, string) (pullrequestparticipants.Snapshot, error)
	RemoveAssignee(context.Context, string, string) (pullrequestparticipants.Snapshot, error)
	ReplaceRequestedReviewers(context.Context, string, []string) (pullrequestparticipants.Snapshot, error)
	AddRequestedReviewer(context.Context, string, string) (pullrequestparticipants.Snapshot, error)
	RemoveRequestedReviewer(context.Context, string, string) (pullrequestparticipants.Snapshot, error)
}

type RegisterFunc func(string, http.HandlerFunc)

func RegisterRoutes(register RegisterFunc, service Service) {
	handler := &handler{service: service}
	register("GET /api/v1/pull-requests/{id}/participants", handler.get)
	register("POST /api/v1/pull-requests/{id}/participants/sync", handler.sync)
	register("PUT /api/v1/pull-requests/{id}/assignees", handler.replaceAssignees)
	register("POST /api/v1/pull-requests/{id}/assignees", handler.addAssignee)
	register("DELETE /api/v1/pull-requests/{id}/assignees/{memberID}", handler.removeAssignee)
	register("PUT /api/v1/pull-requests/{id}/requested-reviewers", handler.replaceRequestedReviewers)
	register("POST /api/v1/pull-requests/{id}/requested-reviewers", handler.addRequestedReviewer)
	register("DELETE /api/v1/pull-requests/{id}/requested-reviewers/{memberID}", handler.removeRequestedReviewer)
}

type handler struct{ service Service }

func (handler *handler) get(w http.ResponseWriter, request *http.Request) {
	snapshot, err := handler.callGet(request)
	handler.respond(w, snapshot, err)
}

func (handler *handler) callGet(request *http.Request) (pullrequestparticipants.Snapshot, error) {
	if handler.service == nil {
		return pullrequestparticipants.Snapshot{}, pullrequestparticipants.ErrReadFailed
	}
	return handler.service.Get(request.Context(), request.PathValue("id"))
}

func (handler *handler) sync(w http.ResponseWriter, request *http.Request) {
	if handler.service == nil {
		handler.respond(w, pullrequestparticipants.Snapshot{}, pullrequestparticipants.ErrUpdateFailed)
		return
	}
	snapshot, err := handler.service.Sync(request.Context(), request.PathValue("id"))
	handler.respond(w, snapshot, err)
}

func (handler *handler) replaceAssignees(w http.ResponseWriter, request *http.Request) {
	ids, ok := decodeCollection(w, request, "assignee_holark_ids")
	if !ok {
		return
	}
	if handler.service == nil {
		handler.respond(w, pullrequestparticipants.Snapshot{}, pullrequestparticipants.ErrUpdateFailed)
		return
	}
	snapshot, err := handler.service.ReplaceAssignees(request.Context(), request.PathValue("id"), ids)
	handler.respond(w, snapshot, err)
}

func (handler *handler) addAssignee(w http.ResponseWriter, request *http.Request) {
	memberID, ok := decodeMember(w, request)
	if !ok {
		return
	}
	if handler.service == nil {
		handler.respond(w, pullrequestparticipants.Snapshot{}, pullrequestparticipants.ErrUpdateFailed)
		return
	}
	snapshot, err := handler.service.AddAssignee(request.Context(), request.PathValue("id"), memberID)
	handler.respond(w, snapshot, err)
}

func (handler *handler) removeAssignee(w http.ResponseWriter, request *http.Request) {
	if handler.service == nil {
		handler.respond(w, pullrequestparticipants.Snapshot{}, pullrequestparticipants.ErrUpdateFailed)
		return
	}
	snapshot, err := handler.service.RemoveAssignee(request.Context(), request.PathValue("id"), request.PathValue("memberID"))
	handler.respond(w, snapshot, err)
}

func (handler *handler) replaceRequestedReviewers(w http.ResponseWriter, request *http.Request) {
	ids, ok := decodeCollection(w, request, "requested_reviewer_holark_ids")
	if !ok {
		return
	}
	if handler.service == nil {
		handler.respond(w, pullrequestparticipants.Snapshot{}, pullrequestparticipants.ErrUpdateFailed)
		return
	}
	snapshot, err := handler.service.ReplaceRequestedReviewers(request.Context(), request.PathValue("id"), ids)
	handler.respond(w, snapshot, err)
}

func (handler *handler) addRequestedReviewer(w http.ResponseWriter, request *http.Request) {
	memberID, ok := decodeMember(w, request)
	if !ok {
		return
	}
	if handler.service == nil {
		handler.respond(w, pullrequestparticipants.Snapshot{}, pullrequestparticipants.ErrUpdateFailed)
		return
	}
	snapshot, err := handler.service.AddRequestedReviewer(request.Context(), request.PathValue("id"), memberID)
	handler.respond(w, snapshot, err)
}

func (handler *handler) removeRequestedReviewer(w http.ResponseWriter, request *http.Request) {
	if handler.service == nil {
		handler.respond(w, pullrequestparticipants.Snapshot{}, pullrequestparticipants.ErrUpdateFailed)
		return
	}
	snapshot, err := handler.service.RemoveRequestedReviewer(request.Context(), request.PathValue("id"), request.PathValue("memberID"))
	handler.respond(w, snapshot, err)
}

func (handler *handler) respond(w http.ResponseWriter, snapshot pullrequestparticipants.Snapshot, err error) {
	if err != nil {
		status, response := ClassifyError(err)
		writeJSON(w, status, response)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func decodeCollection(w http.ResponseWriter, request *http.Request, field string) ([]string, bool) {
	fields, ok := decodeFields(w, request)
	if !ok || len(fields) != 1 {
		writeInvalidIfNeeded(w, ok)
		return nil, false
	}
	raw, exists := fields[field]
	if !exists || string(raw) == "null" {
		writeInvalid(w)
		return nil, false
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		writeInvalid(w)
		return nil, false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			writeInvalid(w)
			return nil, false
		}
	}
	return values, true
}

func decodeMember(w http.ResponseWriter, request *http.Request) (string, bool) {
	fields, ok := decodeFields(w, request)
	if !ok || len(fields) != 1 {
		writeInvalidIfNeeded(w, ok)
		return "", false
	}
	raw, exists := fields["holark_id"]
	if !exists || string(raw) == "null" {
		writeInvalid(w)
		return "", false
	}
	var memberID string
	if err := json.Unmarshal(raw, &memberID); err != nil || strings.TrimSpace(memberID) == "" {
		writeInvalid(w)
		return "", false
	}
	return memberID, true
}

func decodeFields(w http.ResponseWriter, request *http.Request) (map[string]json.RawMessage, bool) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || strings.ToLower(mediaType) != "application/json" {
		writeInvalid(w)
		return nil, false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeInvalid(w)
		return nil, false
	}
	return fields, true
}

func writeInvalidIfNeeded(w http.ResponseWriter, decoded bool) {
	if decoded {
		writeInvalid(w)
	}
}

func writeInvalid(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadRequest, ErrorResponse{Code: "invalid_request", Message: "The pull request participant request is invalid."})
}

type ErrorResponse struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	PullRequestID string `json:"pull_request_id,omitempty"`
	Stage         string `json:"stage,omitempty"`
}

func ClassifyError(err error) (int, ErrorResponse) {
	status := http.StatusInternalServerError
	response := ErrorResponse{Code: "pull_request_participants_update_failed", Message: "Pull request participants could not be updated."}
	switch {
	case githubratelimit.Exceeded(err):
		status, response.Code, response.Message = http.StatusTooManyRequests, "github_rate_limit_exceeded", "GitHub API rate limit exceeded. Try again after the limit resets."
	case errors.Is(err, pullrequestparticipants.ErrPullRequestNotFound):
		status, response.Code, response.Message = http.StatusNotFound, "pull_request_not_found", "Pull request not found."
	case errors.Is(err, pullrequestparticipants.ErrInvalidRequest):
		status, response.Code, response.Message = http.StatusBadRequest, "invalid_request", "The pull request participant request is invalid."
	case errors.Is(err, pullrequestparticipants.ErrPullRequestReadOnly):
		status, response.Code, response.Message = http.StatusConflict, "pull_request_participants_read_only", "Pull request participants are read-only in this status."
	case errors.Is(err, pullrequestparticipants.ErrProviderIdentityRequired):
		status, response.Code, response.Message = http.StatusConflict, "provider_identity_required", "A provider-backed pull request is required."
	case errors.Is(err, pullrequestparticipants.ErrUnsupportedProvider):
		status, response.Code, response.Message = http.StatusUnprocessableEntity, "unsupported_provider", "The pull request participant provider is unsupported."
	case errors.Is(err, pullrequestparticipants.ErrProjectMemberUnresolved):
		status, response.Code, response.Message = http.StatusConflict, "project_member_unresolved", "A GitHub participant is not a project member."
	case errors.Is(err, pullrequestparticipants.ErrProviderUnavailable):
		status, response.Code, response.Message = http.StatusServiceUnavailable, "gh_unavailable", "GitHub is unavailable. Try again."
	case errors.Is(err, pullrequestparticipants.ErrProviderFailed):
		status, response.Code, response.Message = http.StatusBadGateway, "github_sync_failed", "GitHub participant synchronization failed."
	case errors.Is(err, pullrequestparticipants.ErrReadFailed):
		status, response.Code, response.Message = http.StatusInternalServerError, "pull_request_participants_read_failed", "Pull request participants could not be read."
	case errors.Is(err, pullrequestparticipants.ErrUpdateFailed):
		status, response.Code, response.Message = http.StatusInternalServerError, "pull_request_participants_update_failed", "Pull request participants could not be updated."
	}
	var syncError *pullrequestparticipants.SyncError
	if errors.As(err, &syncError) {
		response.PullRequestID = syncError.PullRequestID
		response.Stage = string(syncError.Stage)
	}
	return status, response
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
