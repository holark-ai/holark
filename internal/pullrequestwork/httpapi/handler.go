package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/repositorybrowser"
)

type Service interface {
	Start(context.Context, pullrequestwork.Start) ([]pullrequestwork.Work, error)
	List(context.Context, string, pullrequestwork.Kind) ([]pullrequestwork.Work, error)
	Queue(context.Context, string) ([]pullrequestwork.Work, error)
	Cancel(context.Context, string, string) (pullrequestwork.Work, error)
	RebaseReadiness(context.Context, string) (pullrequestwork.RebaseReadiness, error)
}
type RegisterFunc func(string, http.HandlerFunc)

func RegisterRoutes(register RegisterFunc, service Service) {
	h := handler{s: service}
	register("GET /api/v1/pull-requests/{id}/reviews", h.listReviews)
	register("POST /api/v1/pull-requests/{id}/reviews", h.startReview)
	register("GET /api/v1/pull-requests/{id}/workers", h.listWorkers)
	register("POST /api/v1/pull-requests/{id}/workers", h.startWorkers)
	register("GET /api/v1/pull-requests/{id}/queue", h.listQueue)
	register("POST /api/v1/pull-requests/{id}/rebase", h.startRebase)
	register("GET /api/v1/pull-requests/{id}/rebases", h.listRebases)
	register("POST /api/v1/pull-requests/{id}/rebase-readiness", h.rebaseReadiness)
	register("POST /api/v1/pull-requests/{id}/queue/{workID}/cancel", h.cancel)
}

type handler struct{ s Service }

func (h handler) list(w http.ResponseWriter, r *http.Request, k pullrequestwork.Kind) {
	v, e := h.s.List(r.Context(), r.PathValue("id"), k)
	if e != nil {
		writeError(w, e)
		return
	}
	if v == nil {
		v = []pullrequestwork.Work{}
	}
	write(w, http.StatusOK, v)
}
func (h handler) listReviews(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, pullrequestwork.KindReview)
}
func (h handler) listWorkers(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, pullrequestwork.KindWorker)
}
func (h handler) listRebases(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, pullrequestwork.KindRebase)
}
func (h handler) listQueue(w http.ResponseWriter, r *http.Request) {
	reviews, e := h.s.Queue(r.Context(), r.PathValue("id"))
	if e != nil {
		writeError(w, e)
		return
	}
	if reviews == nil {
		reviews = []pullrequestwork.Work{}
	}
	write(w, http.StatusOK, reviews)
}
func (h handler) cancel(w http.ResponseWriter, r *http.Request) {
	result, err := h.s.Cancel(r.Context(), r.PathValue("id"), r.PathValue("workID"))
	if err != nil {
		writeError(w, err)
		return
	}
	write(w, http.StatusOK, result)
}
func (h handler) startReview(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Mode pullrequestwork.Mode `json:"mode"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, pullrequestwork.ErrInvalid)
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, pullrequestwork.ErrInvalid)
		return
	}
	v, e := h.s.Start(r.Context(), pullrequestwork.Start{PullRequestID: r.PathValue("id"), Kind: pullrequestwork.KindReview, Mode: input.Mode})
	if e != nil {
		writeError(w, e)
		return
	}
	write(w, http.StatusCreated, v[0])
}

func (h handler) startWorkers(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Startup    *pullrequestwork.ContinueOptions `json:"startup,omitempty"`
		CommentIDs []string                         `json:"comment_ids"`
		Mode       pullrequestwork.Mode             `json:"mode"`
		Prompt     string                           `json:"prompt"`
	}
	e := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&v)
	if e != nil {
		write(w, 400, map[string]string{"code": "invalid_request"})
		return
	}
	result, e := h.s.Start(r.Context(), pullrequestwork.Start{PullRequestID: r.PathValue("id"), Kind: pullrequestwork.KindWorker, Mode: v.Mode, CommentIDs: v.CommentIDs, Prompt: v.Prompt, Startup: v.Startup})
	if e != nil {
		writeError(w, e)
		return
	}
	write(w, http.StatusCreated, result)
}
func (h handler) startRebase(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Prompt             string `json:"prompt"`
		MechanicalOnly     bool   `json:"mechanical_only"`
		ExpectedBaseCommit string `json:"expected_base_commit"`
	}
	e := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&v)
	if e != nil {
		write(w, 400, map[string]string{"code": "invalid_request"})
		return
	}
	result, e := h.s.Start(r.Context(), pullrequestwork.Start{RequestID: r.Header.Get("X-Request-ID"), PullRequestID: r.PathValue("id"), Kind: pullrequestwork.KindRebase, Prompt: v.Prompt, MechanicalOnly: v.MechanicalOnly, ExpectedBaseCommit: v.ExpectedBaseCommit})
	if e != nil {
		writeError(w, e)
		return
	}
	write(w, http.StatusCreated, result[0])
}
func (h handler) rebaseReadiness(w http.ResponseWriter, r *http.Request) {
	readiness, err := h.s.RebaseReadiness(r.Context(), r.PathValue("id"))
	if err != nil {
		status, code := http.StatusInternalServerError, "rebase_readiness_failed"
		switch {
		case errors.Is(err, pullrequestwork.ErrPullRequestNotFound):
			status, code = http.StatusNotFound, "pull_request_not_found"
		case errors.Is(err, pullrequestwork.ErrPullRequestInactive):
			status, code = http.StatusConflict, "pull_request_inactive"
		case errors.Is(err, pullrequestwork.ErrStaleHead), errors.Is(err, repositorybrowser.ErrStaleHead):
			status, code = http.StatusConflict, "stale_head"
		case errors.Is(err, pullrequestwork.ErrRebaseReadinessUnavailable), errors.Is(err, repositorybrowser.ErrRepositoryUnavailable), errors.Is(err, repositorybrowser.ErrInvalidRepository), errors.Is(err, repositorybrowser.ErrRefNotFound):
			status, code = http.StatusServiceUnavailable, "repository_unavailable"
		}
		write(w, status, map[string]string{"code": code, "message": err.Error()})
		return
	}
	write(w, http.StatusOK, readiness)
}
func writeError(w http.ResponseWriter, e error) {
	status := 500
	code := "pull_request_work_failed"
	if errors.Is(e, pullrequestwork.ErrNotFound) {
		status = 404
		code = "pull_request_work_not_found"
	} else if errors.Is(e, pullrequestwork.ErrPullRequestNotFound) {
		status = 404
		code = "pull_request_not_found"
	} else if errors.Is(e, pullrequestwork.ErrBusy) || errors.Is(e, pullrequestwork.ErrPullRequestInactive) {
		status = 409
		code = "pull_request_work_busy"
	} else if errors.Is(e, pullrequestwork.ErrInvalid) {
		status = 400
		code = "invalid_request"
	} else if errors.Is(e, pullrequestwork.ErrStaleHead) {
		status = 409
		code = "stale_head"
	} else if errors.Is(e, pullrequestwork.ErrRebaseTargetChanged) {
		status = 409
		code = "rebase_target_changed"
	} else if errors.Is(e, pullrequestwork.ErrRebaseConflicts) {
		status = 409
		code = "rebase_conflicts"
	}
	write(w, status, map[string]string{"code": code, "message": e.Error()})
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
