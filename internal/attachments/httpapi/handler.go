package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/holark-ai/holark/internal/attachments"
)

type Service interface {
	Upload(context.Context, string, []byte) (attachments.Asset, error)
	Resolve(context.Context, string) (attachments.Asset, error)
}

func RegisterRoutes(register func(string, http.HandlerFunc), service Service) {
	h := &handler{service: service}
	register("POST /api/v1/attachments", h.upload)
	register("POST /api/v1/attachments/resolve", h.resolve)
}

type handler struct{ service Service }

func (h *handler) upload(w http.ResponseWriter, r *http.Request) {
	// Read a bounded raw body in memory. Multipart parsing can spill files to disk.
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, attachments.MaximumImageBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, attachments.ErrImageTooLarge)
		} else {
			writeError(w, http.StatusBadRequest, errors.New("The image could not be read."))
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	asset, err := h.service.Upload(ctx, r.URL.Query().Get("name"), data)
	writeResult(w, http.StatusCreated, asset, err)
}

func (h *handler) resolve(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		URL string `json:"url"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := decoder.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, attachments.ErrInvalidURL)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, attachments.ErrInvalidURL)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	asset, err := h.service.Resolve(ctx, payload.URL)
	writeResult(w, http.StatusOK, asset, err)
}

func writeResult(w http.ResponseWriter, status int, asset attachments.Asset, err error) {
	if err != nil {
		switch {
		case errors.Is(err, attachments.ErrInvalidImage), errors.Is(err, attachments.ErrInvalidURL):
			status = http.StatusBadRequest
		case errors.Is(err, attachments.ErrImageTooLarge):
			status = http.StatusRequestEntityTooLarge
		case errors.Is(err, attachments.ErrPermissionDenied):
			status = http.StatusForbidden
		case errors.Is(err, attachments.ErrUnsupportedRepository):
			status = http.StatusUnprocessableEntity
		case errors.Is(err, attachments.ErrAuthentication):
			status = http.StatusServiceUnavailable
		default:
			status = http.StatusBadGateway
		}
		writeError(w, status, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(asset)
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": "github_attachment_failed", "message": err.Error()})
}
