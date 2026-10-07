package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
)

type Service interface {
	Get(context.Context, string) (pullrequestmetadata.Metadata, error)
	Update(context.Context, string, pullrequestmetadata.Request) (pullrequestmetadata.Metadata, error)
	StartAgent(context.Context, string, pullrequestmetadata.AgentRequest) (pullrequestmetadata.Metadata, error)
	RetryApplication(context.Context, string) (pullrequestmetadata.Metadata, error)
}

type RegisterFunc func(string, http.HandlerFunc)

func RegisterRoutes(register RegisterFunc, service Service) {
	handler := &handler{service: service}
	register("GET /api/v1/pull-requests/{id}/metadata", handler.get)
	register("PATCH /api/v1/pull-requests/{id}/metadata", handler.update)
	register("POST /api/v1/pull-requests/{id}/metadata/agent", handler.startAgent)
	register("POST /api/v1/pull-requests/{id}/metadata/retry", handler.retryApplication)
}

type handler struct {
	service Service
}

func (handler *handler) get(w http.ResponseWriter, r *http.Request) {
	if handler.service == nil {
		writeError(w, http.StatusInternalServerError, "pull_request_metadata_read_failed", "Pull request metadata could not be read.")
		return
	}
	metadata, err := handler.service.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metadata)
}

func (handler *handler) update(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "json_required", "A JSON request is required.")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, protocol.MaxMessageBytes))
	decoder.DisallowUnknownFields()
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || decoder.Decode(&struct{}{}) != io.EOF || len(fields) == 0 {
		writeInvalid(w)
		return
	}
	var request pullrequestmetadata.Request
	for name, raw := range fields {
		if string(raw) == "null" {
			writeInvalid(w)
			return
		}
		switch name {
		case "title":
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				writeInvalid(w)
				return
			}
			request.Title = &value
		case "description":
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				writeInvalid(w)
				return
			}
			request.Description = &value
		default:
			writeInvalid(w)
			return
		}
	}
	if handler.service == nil {
		writeError(w, http.StatusInternalServerError, "pull_request_metadata_update_failed", "Pull request metadata could not be updated.")
		return
	}
	metadata, err := handler.service.Update(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metadata)
}

func (handler *handler) startAgent(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "json_required", "A JSON request is required.")
		return
	}
	var body struct {
		Action      pullrequestmetadata.AgentAction `json:"action"`
		Instruction string                          `json:"instruction"`
		RuntimeID   string                          `json:"runtime_id"`
		Terminal    struct {
			Columns int `json:"columns"`
			Rows    int `json:"rows"`
		} `json:"terminal"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, protocol.MaxMessageBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		(body.Action != pullrequestmetadata.ActionImprove && body.Action != pullrequestmetadata.ActionRegenerate) ||
		(body.RuntimeID != "" && !protocol.ValidIdentifier(body.RuntimeID)) || ((body.Terminal.Columns != 0 || body.Terminal.Rows != 0) && protocol.ValidateTerminalSize(body.Terminal.Columns, body.Terminal.Rows) != nil) ||
		len([]rune(body.Instruction)) > pullrequestmetadata.MaxInstructionCharacters {
		writeInvalid(w)
		return
	}
	if handler.service == nil {
		writeError(w, http.StatusInternalServerError, "pull_request_metadata_agent_failed", "The metadata agent could not be started.")
		return
	}
	metadata, err := handler.service.StartAgent(r.Context(), r.PathValue("id"), pullrequestmetadata.AgentRequest{
		Action: body.Action, Instruction: body.Instruction, RuntimeID: body.RuntimeID,
		Terminal: pullrequestmetadata.TerminalSize{Columns: body.Terminal.Columns, Rows: body.Terminal.Rows},
	})
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, metadata)
}

func writeInvalid(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "invalid_request", "The pull request metadata request is invalid.")
}

func writeUpdateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pullrequestmetadata.ErrInvalidRequest):
		writeInvalid(w)
	case errors.Is(err, pullrequestmetadata.ErrPullRequestNotFound):
		writeError(w, http.StatusNotFound, "pull_request_not_found", "Pull request not found.")
	case errors.Is(err, pullrequestmetadata.ErrPullRequestInactive):
		writeError(w, http.StatusConflict, "pull_request_inactive", "Pull request metadata cannot be changed after it is closed.")
	case errors.Is(err, pullrequestmetadata.ErrAgentTurnActive):
		writeError(w, http.StatusConflict, "metadata_agent_busy", "A metadata agent turn is already running.")
	case errors.Is(err, pullrequestmetadata.ErrAgentSessionUnavailable):
		writeError(w, http.StatusConflict, "metadata_agent_session_unavailable", "The linked metadata session is unavailable. Regenerate to recover.")
	case errors.Is(err, pullrequestmetadata.ErrAgentUnavailable):
		writeError(w, http.StatusServiceUnavailable, "metadata_agent_unavailable", err.Error())
	case errors.Is(err, pullrequestmetadata.ErrUnsupportedProvider):
		writeError(w, http.StatusConflict, "unsupported_provider", "The pull request provider does not support metadata updates.")
	case errors.Is(err, pullrequestmetadata.ErrProviderUnavailable):
		writeError(w, http.StatusServiceUnavailable, "gh_unavailable", "GitHub is unavailable. Try again.")
	case errors.Is(err, pullrequestmetadata.ErrProviderFailed):
		writeError(w, http.StatusBadGateway, "github_sync_failed", "GitHub metadata update failed.")
	default:
		writeError(w, http.StatusInternalServerError, "pull_request_metadata_update_failed", "Pull request metadata could not be updated.")
	}
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (handler *handler) retryApplication(w http.ResponseWriter, r *http.Request) {
	metadata, err := handler.service.RetryApplication(r.Context(), r.PathValue("id"))
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metadata)
}
