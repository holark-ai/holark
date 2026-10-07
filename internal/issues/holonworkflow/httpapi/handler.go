package httpapi

import (
	"context"
	"encoding/json"
	"github.com/holark-ai/holark/internal/holons"
	"net/http"
)

type Starter interface {
	Start(context.Context, string) (holons.Holon, error)
}

func New(s Starter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, e := s.Start(r.Context(), r.PathValue("id"))
		if e != nil {
			http.Error(w, e.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(h)
	})
}
