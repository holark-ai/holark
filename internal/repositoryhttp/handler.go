package repositoryhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/holark-ai/holark/internal/repository"
)

// Service is deliberately expressed explicitly: the HTTP adapter depends on domain operations, not Git.
type Service interface {
	Descriptor() repository.Descriptor
	Refs(ctx context.Context) ([]repository.Ref, error)
	Refresh(ctx context.Context) ([]repository.Ref, error)
	Tree(ctx context.Context, ref, path string) (repository.Tree, error)
	Blob(ctx context.Context, ref, path string) (repository.Blob, error)
	Commits(ctx context.Context, ref string, limit int) ([]repository.Commit, error)
	CommitPage(context.Context, string, int, string) (repository.CommitPage, error)
	Changes(ctx context.Context, base, target string) ([]repository.Change, error)
	Dirty(ctx context.Context) (repository.DirtyState, error)
}

type branchPreparer interface {
	PrepareBranch(context.Context, string) (repository.Preparation, error)
}
type defaultRefProvider interface{ DefaultRef() string }

type Handler struct{ service Service }

func New(service Service) http.Handler {
	h := &Handler{service: service}
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/repository", h.descriptor)
	m.HandleFunc("GET /api/v1/repository/refs", h.refs)
	m.HandleFunc("POST /api/v1/repository/refs/refresh", h.refresh)
	m.HandleFunc("GET /api/v1/repository/tree", h.tree)
	m.HandleFunc("GET /api/v1/repository/search", h.search)
	m.HandleFunc("GET /api/v1/repository/blob", h.blob)
	m.HandleFunc("GET /api/v1/repository/commits", h.commits)
	m.HandleFunc("GET /api/v1/repository/changes", h.changes)
	m.HandleFunc("GET /api/v1/repository/commit-changes", h.commitChanges)
	m.HandleFunc("GET /api/v1/repository/dirty", h.dirty)
	m.HandleFunc("POST /api/v1/repository/prepare", h.prepare)
	return m
}
func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, e error) {
	status := http.StatusInternalServerError
	code := "repository_unavailable"
	switch {
	case errors.Is(e, context.DeadlineExceeded):
		status, code = http.StatusGatewayTimeout, "repository_timeout"
	case errors.Is(e, context.Canceled):
		status, code = http.StatusRequestTimeout, "request_canceled"
	case errors.Is(e, repository.ErrRefNotFound):
		status = http.StatusNotFound
		code = "ref_not_found"
	case errors.Is(e, repository.ErrPathNotFound):
		status = http.StatusNotFound
		code = "path_not_found"
	case errors.Is(e, repository.ErrPathNotDirectory):
		status = http.StatusConflict
		code = "path_not_directory"
	case errors.Is(e, repository.ErrPathNotFile):
		status = http.StatusConflict
		code = "path_not_file"
	case errors.Is(e, repository.ErrInvalidCursor):
		status, code = http.StatusBadRequest, "invalid_cursor"
	case errors.Is(e, repository.ErrInvalidSearch):
		status, code = http.StatusBadRequest, "invalid_search"
	case errors.Is(e, repository.ErrInvalidPath):
		status = http.StatusBadRequest
		code = "invalid_path"
	case errors.Is(e, repository.ErrBinaryFile):
		status = http.StatusUnsupportedMediaType
		code = "binary_file"
	case errors.Is(e, repository.ErrFileTooLarge):
		status = http.StatusRequestEntityTooLarge
		code = "file_too_large"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	write(w, map[string]string{"code": code, "message": e.Error()})
}
func (h *Handler) descriptor(w http.ResponseWriter, r *http.Request) {
	write(w, h.service.Descriptor())
}
func (h *Handler) refs(w http.ResponseWriter, r *http.Request) {
	v, e := h.service.Refs(r.Context())
	if e != nil {
		fail(w, e)
		return
	}
	write(w, map[string]any{"refs": v, "default_ref": h.defaultRef()})
}
func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	v, e := h.service.Refresh(r.Context())
	if e != nil {
		cached, ce := h.service.Refs(r.Context())
		if ce == nil {
			write(w, map[string]any{"refs": cached, "default_ref": h.defaultRef(), "refresh_error": e.Error()})
			return
		}
		fail(w, e)
		return
	}
	write(w, map[string]any{"refs": v, "default_ref": h.defaultRef()})
}
func (h *Handler) defaultRef() string {
	if provider, ok := h.service.(defaultRefProvider); ok {
		return provider.DefaultRef()
	}
	branch := repository.CanonicalProviderBranch(h.service.Descriptor().DefaultBranch)
	if branch == "" || branch == "HEAD" {
		return "HEAD"
	}
	return "refs/heads/" + branch
}
func (h *Handler) tree(w http.ResponseWriter, r *http.Request) {
	v, e := h.service.Tree(r.Context(), r.URL.Query().Get("ref"), r.URL.Query().Get("path"))
	if e != nil {
		fail(w, e)
		return
	}
	write(w, v)
}
func (h *Handler) blob(w http.ResponseWriter, r *http.Request) {
	v, e := h.service.Blob(r.Context(), r.URL.Query().Get("ref"), r.URL.Query().Get("path"))
	if e != nil {
		fail(w, e)
		return
	}
	write(w, v)
}
func (h *Handler) commits(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, e := h.service.CommitPage(r.Context(), r.URL.Query().Get("ref"), n, r.URL.Query().Get("cursor"))
	if e != nil {
		fail(w, e)
		return
	}
	write(w, v)
}
func (h *Handler) changes(w http.ResponseWriter, r *http.Request) {
	v, e := h.service.Changes(r.Context(), r.URL.Query().Get("base"), r.URL.Query().Get("target"))
	if e != nil {
		fail(w, e)
		return
	}
	write(w, map[string]any{"files": v})
}
func (h *Handler) dirty(w http.ResponseWriter, r *http.Request) {
	v, e := h.service.Dirty(r.Context())
	if e != nil {
		fail(w, e)
		return
	}
	write(w, v)
}

// prepare pins branch-aware agent creation to a freshly fetched provider commit.
func (h *Handler) prepare(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref string `json:"ref"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	preparer, ok := h.service.(branchPreparer)
	if !ok {
		fail(w, repository.ErrRepositoryUnavailable)
		return
	}
	prepare := preparer.PrepareBranch
	if shared, ok := h.service.(interface {
		PrepareBranchShared(context.Context, string) (repository.Preparation, error)
	}); ok {
		prepare = shared.PrepareBranchShared
	}
	prepared, err := prepare(r.Context(), body.Ref)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, map[string]string{"id": prepared.Commit, "commit": prepared.Commit, "ref": prepared.Branch})
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(interface {
		Search(context.Context, string, string) (repository.SearchResults, error)
	})
	if !ok {
		fail(w, repository.ErrRepositoryUnavailable)
		return
	}
	result, err := service.Search(r.Context(), r.URL.Query().Get("ref"), r.URL.Query().Get("q"))
	if err != nil {
		fail(w, err)
		return
	}
	write(w, result)
}

func (h *Handler) commitChanges(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(interface {
		CommitChanges(context.Context, string) (repository.CommitChanges, error)
	})
	if !ok {
		fail(w, repository.ErrRepositoryUnavailable)
		return
	}
	result, err := service.CommitChanges(r.Context(), r.URL.Query().Get("ref"))
	if err != nil {
		fail(w, err)
		return
	}
	write(w, result)
}
