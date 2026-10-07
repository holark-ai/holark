package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/holark-ai/holark/internal/agentsettings"
)

func RegisterCommandRoutes(register func(string, http.HandlerFunc), service *agentsettings.LaunchCommands) {
	register("GET /api/v1/settings/agent-launch-commands", func(w http.ResponseWriter, r *http.Request) {
		commands, err := service.Current(r.Context())
		if err != nil {
			writeError(w, 500, "settings_failed", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"commands": commands})
	})
	register("PUT /api/v1/settings/agent-launch-commands", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		var input struct {
			Commands agentsettings.Commands `json:"commands"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			writeError(w, 400, "invalid_request", "Provide a commands map with the harnesses to update.")
			return
		}
		if err := service.Save(r.Context(), input.Commands); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, agentsettings.ErrInvalid) {
				status = http.StatusBadRequest
			}
			writeError(w, status, "settings_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
