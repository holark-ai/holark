package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/protocol"
)

func RegisterRoutes(register func(string, http.HandlerFunc), service *agentsettings.Service) {
	register("PUT /api/v1/settings/default-agent-harness", updateDefault(service, agentsettings.WorkflowDefault, true))
	register("PUT /api/v1/settings/agent-harness-defaults/{workflow}", func(w http.ResponseWriter, r *http.Request) {
		updateDefault(service, agentsettings.Workflow(r.PathValue("workflow")), false)(w, r)
	})
	register("DELETE /api/v1/settings/agent-harness-defaults/{workflow}", func(w http.ResponseWriter, r *http.Request) {
		workflow := agentsettings.Workflow(r.PathValue("workflow"))
		if err := service.DeleteDefault(r.Context(), workflow); err != nil {
			status, code := http.StatusInternalServerError, "settings_failed"
			if errors.Is(err, agentsettings.ErrInvalid) {
				status, code = http.StatusBadRequest, "invalid_workflow"
			}
			writeError(w, status, code, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func updateDefault(service *agentsettings.Service, workflow agentsettings.Workflow, legacyResponse bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var input struct {
			Permissions string               `json:"permissions,omitempty"`
			HarnessType protocol.HarnessType `json:"harness_type"`
			Model       string               `json:"model,omitempty"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid_request", "Choose a supported agent harness.")
			return
		}
		if err := service.SavePreference(r.Context(), workflow, input.HarnessType, input.Model, input.Permissions); err != nil {
			status, code := http.StatusServiceUnavailable, "harness_unavailable"
			if errors.Is(err, agentsettings.ErrInvalid) {
				status, code = http.StatusBadRequest, "invalid_setting"
				if legacyResponse {
					code = "invalid_harness"
				}
			}
			writeError(w, status, code, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if legacyResponse {
			_ = json.NewEncoder(w).Encode(map[string]any{"default_harness": input.HarnessType, "default_harness_explicit": true})
			return
		}
		_ = json.NewEncoder(w).Encode(agentsettings.Default{HarnessType: input.HarnessType, Model: input.Model, Permissions: input.Permissions, Explicit: true})
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}
