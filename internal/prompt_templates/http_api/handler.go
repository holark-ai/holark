package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/holark-ai/holark/internal/prompt_templates"
	"github.com/holark-ai/holark/internal/protocol"
)

type Service interface {
	List(context.Context) ([]prompttemplates.Template, error)
	Update(context.Context, string, string) (prompttemplates.Template, error)
}

type RegisterFunc func(string, http.HandlerFunc)

func RegisterRoutes(register RegisterFunc, service Service) {
	handler := &handler{service: service}
	register("GET /api/v1/prompt-templates", handler.list)
	register("PUT /api/v1/prompt-templates/{key}", handler.update)
}

type handler struct{ service Service }

func (handler *handler) list(w http.ResponseWriter, request *http.Request) {
	if handler.service == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_store_unavailable", "Prompt template storage is unavailable.")
		return
	}
	templates, err := handler.service.List(request.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "settings_failed", "Prompt templates could not be read.")
		return
	}
	writeJSON(w, http.StatusOK, templates)
}

func (handler *handler) update(w http.ResponseWriter, request *http.Request) {
	if handler.service == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_store_unavailable", "Prompt template storage is unavailable.")
		return
	}
	if !strings.HasPrefix(request.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "json_required", "A JSON request is required.")
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, protocol.MaxMessageBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "The prompt template request is invalid.")
		return
	}
	template, err := handler.service.Update(request.Context(), request.PathValue("key"), body.Value)
	if err != nil {
		switch {
		case errors.Is(err, prompttemplates.ErrNotFound):
			writeError(w, http.StatusNotFound, "template_not_found", "Prompt template not found.")
		case errors.Is(err, prompttemplates.ErrInvalid):
			writeError(w, http.StatusBadRequest, "invalid_template", "The prompt template is invalid.")
		default:
			writeError(w, http.StatusInternalServerError, "settings_failed", "Prompt template could not be stored.")
		}
		return
	}
	writeJSON(w, http.StatusOK, template)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
