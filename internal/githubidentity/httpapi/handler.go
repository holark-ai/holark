// Package httpapi exposes the project GitHub-member use cases over HTTP.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	githubratelimit "github.com/holark-ai/holark/internal/codehost/github/ratelimit"
	"github.com/holark-ai/holark/internal/githubidentity"
)

type Service interface {
	ListProjectMembers(context.Context, string) ([]githubidentity.Member, error)
	SearchProjectMembers(context.Context, string, string, int) ([]githubidentity.Member, error)
	GetProjectMembers(context.Context, string, []string) (githubidentity.MemberResolution, error)
	SyncProjectMembers(context.Context, string, string) ([]githubidentity.Member, error)
}

type Project struct {
	ID            string
	RepositoryURL string
}

type Options struct {
	Service       Service
	ProjectLookup func(string) (Project, bool)
	Project       *Project
}

func RegisterRoutes(register func(string, http.HandlerFunc), options Options) {
	handler := &handler{options: options}
	register("GET /api/v1/projects/{slug}/github-members", handler.list)
	register("GET /api/v1/projects/{slug}/github-members/search", handler.search)
	register("POST /api/v1/projects/{slug}/github-members/resolve", handler.resolve)
	register("POST /api/v1/projects/{slug}/github-members/sync", handler.sync)
	if options.Project != nil {
		register("GET /api/v1/github-members", handler.list)
		register("GET /api/v1/github-members/search", handler.search)
		register("POST /api/v1/github-members/resolve", handler.resolve)
		register("POST /api/v1/github-members/sync", handler.sync)
	}
}

type handler struct{ options Options }

const (
	maxResolveRequestBytes = 64 * 1024
	maxResolveIDs          = 100
)

func (handler *handler) list(w http.ResponseWriter, r *http.Request) {
	project, ok := handler.project(w, r)
	if !ok {
		return
	}
	members, err := handler.options.Service.ListProjectMembers(r.Context(), project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "github_members_failed", "GitHub members could not be listed.")
		return
	}
	writeJSON(w, http.StatusOK, members)
}

func (handler *handler) search(w http.ResponseWriter, r *http.Request) {
	project, ok := handler.project(w, r)
	if !ok {
		return
	}
	limit := 0
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_limit", "Member search limit must be an integer.")
			return
		}
		limit = parsed
	}
	members, err := handler.options.Service.SearchProjectMembers(r.Context(), project.ID, r.URL.Query().Get("q"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "github_members_failed", "GitHub members could not be searched.")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Members []githubidentity.Member `json:"members"`
	}{Members: members})
}

func (handler *handler) resolve(w http.ResponseWriter, r *http.Request) {
	project, ok := handler.project(w, r)
	if !ok {
		return
	}
	var payload struct {
		IDs json.RawMessage `json:"ids"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxResolveRequestBytes))
	if err := decoder.Decode(&payload); err != nil {
		writeResolveRequestError(w, err)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeResolveRequestError(w, err)
		return
	}
	rawIDs := bytesTrimSpace(payload.IDs)
	if len(rawIDs) == 0 || rawIDs[0] != '[' {
		writeError(w, http.StatusBadRequest, "invalid_request", "Member IDs must be provided as a JSON array.")
		return
	}
	var ids []string
	if err := json.Unmarshal(rawIDs, &ids); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Member IDs must be provided as a JSON array.")
		return
	}
	if len(ids) > maxResolveIDs {
		writeError(w, http.StatusBadRequest, "too_many_member_ids", "At most 100 member IDs may be resolved at once.")
		return
	}
	resolution, err := handler.options.Service.GetProjectMembers(r.Context(), project.ID, ids)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "github_members_failed", "GitHub members could not be resolved.")
		return
	}
	writeJSON(w, http.StatusOK, resolution)
}

func writeResolveRequestError(w http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "The member resolution request is too large.")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_request", "Member IDs must be provided as a JSON array.")
}

func bytesTrimSpace(value []byte) []byte {
	return []byte(strings.TrimSpace(string(value)))
}

func (handler *handler) sync(w http.ResponseWriter, r *http.Request) {
	project, ok := handler.project(w, r)
	if !ok {
		return
	}
	members, err := handler.options.Service.SyncProjectMembers(r.Context(), project.ID, project.RepositoryURL)
	if err != nil {
		switch {
		case githubratelimit.Exceeded(err):
			writeError(w, http.StatusTooManyRequests, "github_rate_limit_exceeded", "GitHub API rate limit exceeded. Try again after the limit resets.")
		case errors.Is(err, githubidentity.ErrUnsupportedRepository):
			writeError(w, http.StatusConflict, "unsupported_repository", "This project is not hosted on GitHub.")
		case errors.Is(err, githubidentity.ErrGitHubUnavailable):
			writeError(w, http.StatusServiceUnavailable, "gh_unavailable", "GitHub CLI is unavailable.")
		case errors.Is(err, githubidentity.ErrGitHubFailed), errors.Is(err, githubidentity.ErrMalformedSnapshot):
			writeError(w, http.StatusBadGateway, "github_sync_failed", "GitHub members could not be synchronized.")
		default:
			writeError(w, http.StatusInternalServerError, "github_members_failed", "GitHub members could not be stored.")
		}
		return
	}
	writeJSON(w, http.StatusOK, members)
}

func (handler *handler) project(w http.ResponseWriter, r *http.Request) (Project, bool) {
	if handler.options.Project != nil {
		return *handler.options.Project, true
	}
	if handler.options.ProjectLookup == nil {
		writeError(w, http.StatusNotFound, "project_not_found", "Project not found.")
		return Project{}, false
	}
	project, ok := handler.options.ProjectLookup(r.PathValue("slug"))
	if !ok {
		writeError(w, http.StatusNotFound, "project_not_found", "Project not found.")
		return Project{}, false
	}
	return project, true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
