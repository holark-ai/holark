package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/protocol"
)

func RegisterModels(register func(string, http.HandlerFunc), registry *harness.Registry, repositoryPath string) {
	register("GET /api/v1/agents/{harness}/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		kind := protocol.HarnessType(r.PathValue("harness"))
		if _, ok := registry.Driver(kind); !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Agent is not registered."})
			return
		}
		models, err := registry.Models(r.Context(), kind, repositoryPath)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Model discovery failed. Retry or enter a custom model."})
			return
		}
		_ = json.NewEncoder(w).Encode(models)
	})
}
