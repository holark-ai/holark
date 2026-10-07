package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/holark-ai/holark/internal/pullrequestreviews"
)

type Service interface {
	List(context.Context, string) ([]pullrequestreviews.Review, error)
	Submit(context.Context, string, pullrequestreviews.Submission) (pullrequestreviews.Review, error)
	Retry(context.Context, string, string) (pullrequestreviews.Review, error)
}

func RegisterRoutes(register func(string, http.HandlerFunc), service Service) {
	h := handler{service: service}
	register("GET /api/v1/pull-requests/{id}/manual-reviews", h.list)
	register("POST /api/v1/pull-requests/{id}/manual-reviews", h.submit)
	register("POST /api/v1/pull-requests/{id}/manual-reviews/{reviewID}/retry", h.retry)
}

type handler struct{ service Service }

func (h handler) list(w http.ResponseWriter, r *http.Request) {
	reviews, err := h.service.List(r.Context(), r.PathValue("id"))
	respond(w, http.StatusOK, reviews, err)
}

func (h handler) submit(w http.ResponseWriter, r *http.Request) {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		respond(w, http.StatusUnsupportedMediaType, map[string]string{"code": "json_required", "message": "A JSON request is required."}, nil)
		return
	}
	var input pullrequestreviews.Submission
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		respond(w, 0, nil, pullrequestreviews.ErrInvalidSubmission)
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		respond(w, 0, nil, pullrequestreviews.ErrInvalidSubmission)
		return
	}
	review, err := h.service.Submit(r.Context(), r.PathValue("id"), input)
	respond(w, http.StatusCreated, review, err)
}

func (h handler) retry(w http.ResponseWriter, r *http.Request) {
	review, err := h.service.Retry(r.Context(), r.PathValue("id"), r.PathValue("reviewID"))
	respond(w, http.StatusOK, review, err)
}

func respond(w http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		code, message := "manual_review_failed", "The review could not be saved."
		status = http.StatusInternalServerError
		switch {
		case errors.Is(err, pullrequestreviews.ErrNotFound), errors.Is(err, pullrequestreviews.ErrPullRequestNotFound):
			status, code, message = http.StatusNotFound, "not_found", err.Error()
		case errors.Is(err, pullrequestreviews.ErrInvalidSubmission):
			status, code, message = http.StatusBadRequest, "invalid_request", "The review request is invalid."
		case errors.Is(err, pullrequestreviews.ErrReadOnly):
			status, code, message = http.StatusConflict, "read_only", err.Error()
		case errors.Is(err, pullrequestreviews.ErrStaleHead):
			status, code, message = http.StatusConflict, "stale_head", err.Error()
		}
		value = map[string]string{"code": code, "message": message}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
