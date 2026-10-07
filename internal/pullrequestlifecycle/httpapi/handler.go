// Package httpapi exposes the singleton local pull-request catalog.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	githubratelimit "github.com/holark-ai/holark/internal/codehost/github/ratelimit"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestmerge"
	"github.com/holark-ai/holark/internal/pullrequestparticipants"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/repository"
)

type Catalog interface {
	GetOperation(context.Context, string) (pullrequestlifecycle.Operation, bool, error)
	ListPullRequests(string) []pullrequestlifecycle.PullRequest
	GetPullRequest(string) (pullrequestlifecycle.PullRequest, bool)
	CreatePullRequest(pullrequestlifecycle.PullRequest) (pullrequestlifecycle.PullRequest, error)
}
type Lifecycle interface {
	SyncActive(context.Context, string) (pullrequestlifecycle.SyncResult, error)
	Sync(context.Context, string) (pullrequestlifecycle.SyncResult, error)
	SyncPullRequest(context.Context, string) error
	RequestTransition(context.Context, pullrequestlifecycle.PullRequest, pullrequestlifecycle.Status) (pullrequestlifecycle.PullRequest, error)
	RefreshGitHubReadiness(context.Context, pullrequestlifecycle.PullRequest) (pullrequestlifecycle.PullRequest, error)
}
type MergeCoordinator interface {
	Project(context.Context, pullrequestlifecycle.PullRequest) pullrequestlifecycle.PullRequest
	Merge(context.Context, string, pullrequestmerge.Request) (pullrequestmerge.Result, error)
}
type Repository interface {
	CommitRange(context.Context, string, string) ([]repository.Commit, error)
	InspectRange(context.Context, string, string, string, string) (repository.WorkspaceInspection, error)
}
type Creator interface {
	Create(context.Context, pullrequestlifecycle.CreatePullRequest) (pullrequestlifecycle.PullRequest, error)
}
type Holons interface {
	PullRequestReference(context.Context, string) (string, error)
}
type HolonLinks interface {
	List(context.Context, string) ([]pullrequestlifecycle.HolonLink, error)
}
type ContinuePublisher interface {
	PublishLatestContinue(context.Context, string, string) (pullrequestwork.Work, error)
}
type RetirementScheduler interface {
	RequestRetirement(context.Context, string)
}
type Options struct {
	RepositoryID         string
	Catalog              Catalog
	Lifecycle            Lifecycle
	PublicationReadiness pullrequestlifecycle.PublicationReadiness
	Merge                MergeCoordinator
	Repository           Repository
	Holons               Holons
	HolonLinks           HolonLinks
	Creator              Creator
	ContinuePublisher    ContinuePublisher
	RetirementScheduler  RetirementScheduler
	Participants         interface {
		Get(context.Context, string) (pullrequestparticipants.Snapshot, error)
		GetMany(context.Context, []string) (map[string]pullrequestparticipants.Snapshot, error)
	}
	Comments interface {
		UnresolvedCount(context.Context, string) (int, error)
	}
	PanelPins interface {
		Pin(context.Context, string) error
		Unpin(context.Context, string) error
		Pinned(context.Context, string) (bool, error)
		List(context.Context, string) (map[string]bool, error)
	}
	Retirement interface {
		RetireInactive(context.Context, string) error
	}
}
type handler struct{ o Options }
type RegisterFunc func(string, http.HandlerFunc)

func RegisterRoutes(register RegisterFunc, o Options) {
	h := handler{o}
	register("GET /api/v1/pull-request-operations/{requestID}", h.operation)
	register("GET /api/v1/pull-requests", h.list)
	register("POST /api/v1/pull-requests/sync", h.sync)
	register("GET /api/v1/pull-requests/{id}", h.get)
	register("POST /api/v1/pull-requests/{id}/sync", h.syncPullRequest)
	register("POST /api/v1/pull-requests/{id}/github-readiness", h.readiness)
	register("POST /api/v1/pull-requests/{id}/transition", h.transition)
	register("POST /api/v1/pull-requests/{id}/merge", h.merge)
	register("POST /api/v1/pull-requests/{id}/publish", h.publish)
	register("PUT /api/v1/pull-requests/{id}/panel-pin", h.pinToPanel)
	register("DELETE /api/v1/pull-requests/{id}/panel-pin", h.unpinFromPanel)
	register("GET /api/v1/pull-requests/{id}/commits", h.commits)
	register("GET /api/v1/pull-requests/{id}/changes", h.changes)
	register("POST /api/v1/holons/{id}/pull-request", h.create)
	register("GET /api/v1/holons/{id}/pull-request", h.holonPullRequest)
	register("GET /api/v1/pull-request-holon-links", h.links)
}

func (h handler) create(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Title     string                      `json:"title"`
		Summary   string                      `json:"summary"`
		Target    pullrequestlifecycle.Status `json:"target"`
		RequestID string                      `json:"request_id,omitempty"`
	}
	if !decode(w, r, &request) {
		return
	}
	if !request.Target.Active() {
		creationProblem(w, pullrequestlifecycle.ErrInvalidCreation)
		return
	}
	if h.o.Creator == nil {
		creationProblem(w, pullrequestlifecycle.ErrSourceNotReady)
		return
	}
	pullRequest, err := h.o.Creator.Create(actionContext(r, request.RequestID), pullrequestlifecycle.CreatePullRequest{HolonID: r.PathValue("id"), RepositoryID: h.o.RepositoryID, Title: request.Title, Summary: request.Summary, Target: request.Target})
	if err != nil {
		creationProblem(w, err)
		return
	}
	write(w, http.StatusCreated, h.projected(r.Context(), h.withHolonLinks(r.Context(), pullRequest)))
}

func (h handler) holonPullRequest(w http.ResponseWriter, r *http.Request) {
	holonID := r.PathValue("id")
	for _, pullRequest := range h.o.Catalog.ListPullRequests(h.o.RepositoryID) {
		for _, linkedID := range pullRequest.LinkedHolonIDs {
			if linkedID == holonID {
				write(w, http.StatusOK, map[string]any{"pull_request": h.projected(r.Context(), pullRequest)})
				return
			}
		}
	}
	if h.o.Holons != nil {
		if pullRequestID, err := h.o.Holons.PullRequestReference(r.Context(), holonID); err == nil && pullRequestID != "" {
			if pullRequest, ok := h.o.Catalog.GetPullRequest(pullRequestID); ok {
				write(w, http.StatusOK, map[string]any{"pull_request": h.projected(r.Context(), pullRequest)})
				return
			}
		}
	}
	problemMessage(w, http.StatusNotFound, "pull_request_not_found", "Pull request not found.")
}

func creationProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pullrequestlifecycle.ErrOperationInProgress):
		problemMessage(w, http.StatusConflict, "pull_request_action_pending", "A pull request action is already in progress.")
	case errors.Is(err, pullrequestlifecycle.ErrInvalidCreation):
		problemMessage(w, http.StatusBadRequest, "invalid_request", "The pull request request is invalid.")
	case errors.Is(err, pullrequestlifecycle.ErrPullRequestExists):
		problemMessage(w, http.StatusConflict, "pull_request_exists", "This Holon already has a pull request.")
	case errors.Is(err, pullrequestlifecycle.ErrNoChanges):
		problemMessage(w, http.StatusConflict, "no_changes", "The workspace has no changes.")
	case errors.Is(err, pullrequestlifecycle.ErrWorkspaceDirty):
		problemMessage(w, http.StatusConflict, "workspace_dirty", "The workspace has uncommitted changes.")
	case errors.Is(err, holons.ErrNotFound):
		problemMessage(w, http.StatusNotFound, "holon_not_found", "Holon not found.")
	case errors.Is(err, pullrequestlifecycle.ErrSourceNotReady):
		problemMessage(w, http.StatusConflict, "pull_request_source_not_ready", "The pull request source is not ready.")
	case errors.Is(err, repository.ErrRefNotFound), errors.Is(err, repository.ErrRepositoryUnavailable), errors.Is(err, repository.ErrInvalidRepository):
		repositoryProblem(w, err)
	default:
		var lifecycleErr pullrequestlifecycle.Error
		var githubErr *pullrequestlifecycle.GitHubError
		if errors.As(err, &lifecycleErr) || errors.Is(err, pullrequestlifecycle.ErrInvalidTransition) {
			transitionProblem(w, err)
		} else if errors.As(err, &githubErr) || errors.Is(err, pullrequestlifecycle.ErrGitHubClientUnsupported) {
			syncProblem(w, err)
		} else {
			problemMessage(w, http.StatusInternalServerError, "pull_request_failed", "The pull request could not be created.")
		}

	}
}

func (h handler) links(w http.ResponseWriter, r *http.Request) {
	if h.o.HolonLinks == nil {
		problemMessage(w, http.StatusInternalServerError, "pull_request_holon_links_read_failed", "Pull request Holon links could not be read.")
		return
	}
	links, err := h.o.HolonLinks.List(r.Context(), h.o.RepositoryID)
	if err != nil {
		problemMessage(w, http.StatusInternalServerError, "pull_request_holon_links_read_failed", "Pull request Holon links could not be read.")
		return
	}
	write(w, http.StatusOK, links)
}

type response struct {
	PublicationBlocked bool `json:"publication_blocked"`
	pullrequestlifecycle.PullRequest
	RepositoryID any      `json:"repository_id,omitempty"`
	PanelPinned  bool     `json:"panel_pinned"`
	Assignees    []string `json:"assignee_holark_ids"`
	Reviewers    []string `json:"requested_reviewer_holark_ids"`
}

func project(pr pullrequestlifecycle.PullRequest) response {
	if pr.LinkedHolonIDs == nil {
		pr.LinkedHolonIDs = []string{}
	}
	return response{PullRequest: pr, Assignees: []string{}, Reviewers: []string{}}
}
func (h handler) withHolonLinks(ctx context.Context, pr pullrequestlifecycle.PullRequest) pullrequestlifecycle.PullRequest {
	if h.o.HolonLinks != nil {
		if links, err := h.o.HolonLinks.List(ctx, pr.RepositoryID); err == nil {
			for _, link := range links {
				if link.PullRequestID == pr.ID && !slices.Contains(pr.LinkedHolonIDs, link.HolonID) {
					pr.LinkedHolonIDs = append(pr.LinkedHolonIDs, link.HolonID)
				}
			}
		}
	}
	return pr
}

func (h handler) projected(ctx context.Context, pr pullrequestlifecycle.PullRequest) response {
	out, _ := h.projectedWithPanelPin(ctx, pr)
	return out
}

func (h handler) projectedWithPanelPin(ctx context.Context, pr pullrequestlifecycle.PullRequest) (response, error) {
	out := h.projectedWithoutPanelPin(ctx, pr)
	if h.o.PanelPins != nil {
		pinned, err := h.o.PanelPins.Pinned(ctx, pr.ID)
		if err != nil {
			return out, err
		}
		out.PanelPinned = pinned
	}
	return out, nil
}

func (h handler) projectedWithoutPanelPin(ctx context.Context, pr pullrequestlifecycle.PullRequest) response {
	if h.o.Merge != nil {
		pr = h.o.Merge.Project(ctx, pr)
	}
	if pr.ComparisonState != "" && !pr.HasCurrentComparison() && pr.Status.Active() {
		pr.Mergeable = new(bool)
		if pr.MergeBlockedReason == "" {
			pr.MergeBlockedReason = "comparison_stale"
		}
	}
	out := project(pr)
	if (pr.Status == pullrequestlifecycle.StatusWIP || pr.Status == pullrequestlifecycle.StatusDraft) && h.o.PublicationReadiness != nil {
		blocked, err := h.o.PublicationReadiness.PublicationBlocked(ctx, pr.ID)
		out.PublicationBlocked = blocked || err != nil
	}
	if h.o.Participants != nil {
		if s, e := h.o.Participants.Get(ctx, pr.ID); e == nil {
			out.Assignees = append([]string{}, s.AssigneeHolarkIDs...)
			out.Reviewers = append([]string{}, s.RequestedReviewerHolarkIDs...)
		}
	}
	if h.o.Comments != nil {
		if n, e := h.o.Comments.UnresolvedCount(ctx, pr.ID); e == nil {
			out.PullRequest.UnresolvedCommentCount = n
		}
	}
	return out
}

func (h handler) list(w http.ResponseWriter, r *http.Request) {
	items := h.o.Catalog.ListPullRequests(h.o.RepositoryID)
	pinned := map[string]bool{}
	if h.o.PanelPins != nil {
		pins, err := h.o.PanelPins.List(r.Context(), h.o.RepositoryID)
		if err != nil {
			problemMessage(w, http.StatusInternalServerError, "pull_request_panel_pins_read_failed", "Pull request panel retention could not be read.")
			return
		}
		pinned = pins
	}
	out := make([]response, len(items))
	for i := range items {
		out[i] = h.projectedWithoutPanelPin(r.Context(), items[i])
		if pinned[items[i].ID] {
			out[i].PanelPinned = true
		}
	}
	write(w, 200, out)
}

func (h handler) pinToPanel(w http.ResponseWriter, r *http.Request) {
	pullRequest, ok := h.o.Catalog.GetPullRequest(r.PathValue("id"))
	if !ok {
		problemMessage(w, http.StatusNotFound, "pull_request_not_found", "Pull request not found.")
		return
	}
	if h.o.PanelPins == nil {
		slog.ErrorContext(r.Context(), "Pull request panel pinning is unavailable", "pull_request_id", pullRequest.ID, "operation", "pin")
		problemMessage(w, http.StatusInternalServerError, "pull_request_panel_pin_failed", "The pull request could not be kept in the Holons panel.")
		return
	}
	if err := h.o.PanelPins.Pin(r.Context(), pullRequest.ID); err != nil {
		slog.ErrorContext(r.Context(), "Pull request panel pin update failed", "pull_request_id", pullRequest.ID, "operation", "pin", "error", err)
		problemMessage(w, http.StatusInternalServerError, "pull_request_panel_pin_failed", "The pull request could not be kept in the Holons panel.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handler) unpinFromPanel(w http.ResponseWriter, r *http.Request) {
	pullRequest, ok := h.o.Catalog.GetPullRequest(r.PathValue("id"))
	if !ok {
		problemMessage(w, http.StatusNotFound, "pull_request_not_found", "Pull request not found.")
		return
	}
	if h.o.PanelPins == nil {
		slog.ErrorContext(r.Context(), "Pull request panel pinning is unavailable", "pull_request_id", pullRequest.ID, "operation", "unpin")
		problemMessage(w, http.StatusInternalServerError, "pull_request_panel_unpin_failed", "The pull request could not be removed from the Holons panel.")
		return
	}
	if err := h.o.PanelPins.Unpin(r.Context(), pullRequest.ID); err != nil {
		slog.ErrorContext(r.Context(), "Pull request panel pin update failed", "pull_request_id", pullRequest.ID, "operation", "unpin", "error", err)
		problemMessage(w, http.StatusInternalServerError, "pull_request_panel_unpin_failed", "The pull request could not be removed from the Holons panel.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h handler) get(w http.ResponseWriter, r *http.Request) {
	pr, ok := h.o.Catalog.GetPullRequest(r.PathValue("id"))
	if !ok {
		problem(w, 404, "pull_request_not_found")
		return
	}
	out, err := h.projectedWithPanelPin(r.Context(), pr)
	if err != nil {
		problemMessage(w, http.StatusInternalServerError, "pull_request_panel_pin_read_failed", "Pull request panel retention could not be read.")
		return
	}
	write(w, 200, out)
}
func (h handler) sync(w http.ResponseWriter, r *http.Request) {
	var v pullrequestlifecycle.SyncResult
	var e error
	if r.URL.Query().Get("scope") == "active" {
		v, e = h.o.Lifecycle.SyncActive(r.Context(), h.o.RepositoryID)
	} else {
		v, e = h.o.Lifecycle.Sync(r.Context(), h.o.RepositoryID)
	}
	// Sync may persist lifecycle changes before failing. Retire from the local
	// catalog before handling errors or syncing other pull requests' comments.
	if h.o.Retirement != nil {
		for _, pr := range h.o.Catalog.ListPullRequests(h.o.RepositoryID) {
			if !pr.Status.Active() {
				_ = h.o.Retirement.RetireInactive(r.Context(), pr.ID)
			}
		}
	}
	if e != nil {
		syncProblem(w, e)
		return
	}
	out := make([]response, len(v.PullRequests))
	for i := range v.PullRequests {
		out[i] = h.projected(r.Context(), v.PullRequests[i])
	}
	write(w, 200, map[string]any{"pull_requests": out, "imported": v.Imported, "updated": v.Updated, "synced_at": v.SyncedAt})
}
func (h handler) syncPullRequest(w http.ResponseWriter, r *http.Request) {
	pullRequest, ok := h.o.Catalog.GetPullRequest(r.PathValue("id"))
	if !ok {
		problemMessage(w, http.StatusNotFound, "pull_request_not_found", "Pull request not found.")
		return
	}
	err := h.o.Lifecycle.SyncPullRequest(r.Context(), pullRequest.ID)
	updated, found := h.o.Catalog.GetPullRequest(pullRequest.ID)
	if found && h.o.Retirement != nil && !updated.Status.Active() {
		_ = h.o.Retirement.RetireInactive(r.Context(), updated.ID)
	}
	if err != nil {
		if errors.Is(err, pullrequestlifecycle.ErrGitHubSyncUnsupported) {
			problemMessage(w, http.StatusConflict, "github_sync_unsupported", "Pull request is not GitHub-backed.")
			return
		}
		syncProblem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h handler) readiness(w http.ResponseWriter, r *http.Request) {
	pr, ok := h.o.Catalog.GetPullRequest(r.PathValue("id"))
	if !ok {
		problemMessage(w, http.StatusNotFound, "pull_request_not_found", "Pull request not found.")
		return
	}
	v, e := h.o.Lifecycle.RefreshGitHubReadiness(r.Context(), pr)
	if e != nil {
		readinessProblem(w, e)
		return
	}
	write(w, 200, h.projected(r.Context(), v))
}
func readinessProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pullrequestlifecycle.ErrGitHubReadinessUnsupported):
		problemMessage(w, http.StatusConflict, "github_readiness_unsupported", "Pull request is not GitHub-backed.")
	case errors.Is(err, pullrequestlifecycle.ErrGitHubReadinessInactive):
		problemMessage(w, http.StatusConflict, "pull_request_inactive", "Pull request is inactive.")
	default:
		syncProblem(w, err)
	}
}

func syncProblem(w http.ResponseWriter, err error) {
	if githubratelimit.Exceeded(err) {
		problemMessage(w, http.StatusTooManyRequests, "github_rate_limit_exceeded", "GitHub API rate limit exceeded. Try again after the limit resets.")
		return
	}
	if errors.Is(err, pullrequestlifecycle.ErrSynchronizationStale) || errors.Is(err, pullrequestlifecycle.ErrComparisonUnavailable) || errors.Is(err, repository.ErrBranchChanged) || errors.Is(err, pullrequestlifecycle.ErrOperationInProgress) {
		problemMessage(w, http.StatusConflict, "pull_request_sync_stale", err.Error())
		return
	}
	var githubErr *pullrequestlifecycle.GitHubError
	if errors.As(err, &githubErr) {
		switch githubErr.Kind {
		case pullrequestlifecycle.GitHubUnavailable:
			problemMessage(w, http.StatusServiceUnavailable, string(githubErr.Kind), "GitHub sync failed. Make sure gh is installed and authenticated, then try again.")
		case pullrequestlifecycle.GitHubUnsupportedRepository:
			problemMessage(w, http.StatusConflict, "unsupported_repository", "Repository is not a supported GitHub repository.")
		default:
			problemMessage(w, http.StatusBadGateway, "github_sync_failed", "GitHub sync failed.")
		}
		return
	}
	if errors.Is(err, pullrequestlifecycle.ErrGitHubClientUnsupported) {
		problemMessage(w, http.StatusBadGateway, "github_sync_failed", "GitHub sync failed.")
		return
	}
	if errors.Is(err, repository.ErrRefNotFound) || errors.Is(err, repository.ErrRepositoryUnavailable) || errors.Is(err, repository.ErrInvalidRepository) {
		repositoryProblem(w, err)
		return
	}
	if participantProblem(w, err) {
		return
	}
	problemMessage(w, http.StatusInternalServerError, "pull_request_sync_failed", "Pull requests could not be synced.")
}

func participantProblem(w http.ResponseWriter, err error) bool {
	var syncErr *pullrequestparticipants.SyncError
	if !errors.As(err, &syncErr) && !errors.Is(err, pullrequestparticipants.ErrPullRequestNotFound) && !errors.Is(err, pullrequestparticipants.ErrInvalidRequest) && !errors.Is(err, pullrequestparticipants.ErrPullRequestReadOnly) && !errors.Is(err, pullrequestparticipants.ErrProviderIdentityRequired) && !errors.Is(err, pullrequestparticipants.ErrUnsupportedProvider) && !errors.Is(err, pullrequestparticipants.ErrProjectMemberUnresolved) && !errors.Is(err, pullrequestparticipants.ErrProviderUnavailable) && !errors.Is(err, pullrequestparticipants.ErrProviderFailed) && !errors.Is(err, pullrequestparticipants.ErrReadFailed) && !errors.Is(err, pullrequestparticipants.ErrUpdateFailed) {
		return false
	}
	status, code, message := http.StatusInternalServerError, "pull_request_participants_update_failed", "Pull request participants could not be updated."
	switch {
	case errors.Is(err, pullrequestparticipants.ErrPullRequestNotFound):
		status, code, message = http.StatusNotFound, "pull_request_not_found", "Pull request not found."
	case errors.Is(err, pullrequestparticipants.ErrInvalidRequest):
		status, code, message = http.StatusBadRequest, "invalid_request", "The pull request participant request is invalid."
	case errors.Is(err, pullrequestparticipants.ErrPullRequestReadOnly):
		status, code, message = http.StatusConflict, "pull_request_participants_read_only", "Pull request participants are read-only in this status."
	case errors.Is(err, pullrequestparticipants.ErrProviderIdentityRequired):
		status, code, message = http.StatusConflict, "provider_identity_required", "A provider-backed pull request is required."
	case errors.Is(err, pullrequestparticipants.ErrUnsupportedProvider):
		status, code, message = http.StatusUnprocessableEntity, "unsupported_provider", "The pull request participant provider is unsupported."
	case errors.Is(err, pullrequestparticipants.ErrProjectMemberUnresolved):
		status, code, message = http.StatusConflict, "project_member_unresolved", "A GitHub participant is not a project member."
	case errors.Is(err, pullrequestparticipants.ErrProviderUnavailable):
		status, code, message = http.StatusServiceUnavailable, "gh_unavailable", "GitHub is unavailable. Try again."
	case errors.Is(err, pullrequestparticipants.ErrProviderFailed):
		status, code, message = http.StatusBadGateway, "github_sync_failed", "GitHub participant synchronization failed."
	case errors.Is(err, pullrequestparticipants.ErrReadFailed):
		code, message = "pull_request_participants_read_failed", "Pull request participants could not be read."
	}
	problemMessage(w, status, code, message)
	return true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		problem(w, 415, "json_required")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, protocol.MaxMessageBytes))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(&struct{}{}) != io.EOF {
		problemMessage(w, http.StatusBadRequest, "invalid_request", "The request is invalid.")
		return false
	}
	return true
}
func (h handler) transition(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Status    pullrequestlifecycle.Status `json:"status"`
		RequestID string                      `json:"request_id,omitempty"`
	}
	if !decode(w, r, &q) {
		return
	}
	pr, ok := h.o.Catalog.GetPullRequest(r.PathValue("id"))
	if !ok {
		problem(w, 404, "pull_request_not_found")
		return
	}
	v, e := h.o.Lifecycle.RequestTransition(actionContext(r, q.RequestID), pr, q.Status)
	if e != nil {
		transitionProblem(w, e)
		return
	}
	write(w, 200, h.projected(r.Context(), h.withHolonLinks(r.Context(), v)))
	if h.o.Retirement != nil && !v.Status.Active() {
		_ = h.o.Retirement.RetireInactive(r.Context(), v.ID)
	}
}
func transitionProblem(w http.ResponseWriter, err error) {
	var lifecycleErr pullrequestlifecycle.Error
	switch {
	case errors.Is(err, pullrequestlifecycle.ErrOperationInProgress):
		problemMessage(w, http.StatusConflict, "pull_request_action_pending", "A pull request action is already in progress.")
	case errors.Is(err, pullrequestlifecycle.ErrInvalidTransition):
		problemMessage(w, http.StatusConflict, "invalid_pull_request_transition", "The pull request transition is invalid.")
	case errors.As(err, &lifecycleErr):
		status := http.StatusConflict
		if lifecycleErr.Code == "project_not_found" {
			status = http.StatusNotFound
		} else if lifecycleErr.Code == "repository_unavailable" {
			status = http.StatusServiceUnavailable
		}
		problemMessage(w, status, lifecycleErr.Code, lifecycleErr.Message)
	case errors.Is(err, repository.ErrRefNotFound):
		problemMessage(w, http.StatusConflict, "stale_head", "The pull request head is no longer available.")
	case errors.Is(err, repository.ErrRepositoryUnavailable), errors.Is(err, repository.ErrInvalidRepository):
		repositoryProblem(w, err)
	default:
		var githubErr *pullrequestlifecycle.GitHubError
		if errors.As(err, &githubErr) || errors.Is(err, pullrequestlifecycle.ErrGitHubClientUnsupported) {
			syncProblem(w, err)
			return
		}
		problemMessage(w, http.StatusInternalServerError, "pull_request_transition_failed", "The pull request transition could not be completed.")
	}
}

func (h handler) merge(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Strategy  string `json:"strategy"`
		Bypass    bool   `json:"bypass_unresolved_comments"`
		RequestID string `json:"request_id,omitempty"`
	}
	if !decode(w, r, &q) {
		return
	}
	if h.o.Merge == nil {
		problem(w, http.StatusServiceUnavailable, "github_unavailable")
		return
	}
	result, err := h.o.Merge.Merge(actionContext(r, q.RequestID), r.PathValue("id"), pullrequestmerge.Request{Strategy: q.Strategy, BypassUnresolvedComments: q.Bypass})
	if err != nil {
		mergeProblem(w, err)
		return
	}
	if h.o.RetirementScheduler != nil {
		h.o.RetirementScheduler.RequestRetirement(r.Context(), result.PullRequest.ID)
	}
	write(w, http.StatusOK, map[string]any{"pull_request": h.projected(r.Context(), result.PullRequest), "merged_commit": result.MergedCommit})
}

func mergeProblem(w http.ResponseWriter, err error) {
	var githubErr *pullrequestmerge.GitHubError
	var blockedErr *pullrequestmerge.BlockedError
	switch {
	case errors.Is(err, pullrequestlifecycle.ErrSynchronizationStale):
		problemMessage(w, http.StatusConflict, "pull_request_sync_stale", err.Error())
	case errors.Is(err, pullrequestlifecycle.ErrOperationInProgress):
		problemMessage(w, http.StatusConflict, "pull_request_action_pending", "A pull request action is already in progress.")
	case errors.Is(err, pullrequestmerge.ErrInvalidRequest):
		problem(w, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, pullrequestmerge.ErrNotFound):
		problem(w, http.StatusNotFound, "pull_request_not_found")
	case errors.Is(err, pullrequestmerge.ErrPullRequestMerged):
		problem(w, http.StatusConflict, "pull_request_merged")
	case errors.Is(err, pullrequestmerge.ErrPullRequestClosed):
		problem(w, http.StatusConflict, "pull_request_closed")
	case errors.Is(err, pullrequestmerge.ErrUnresolvedComments):
		problem(w, http.StatusConflict, "unresolved_comments")
	case errors.Is(err, pullrequestmerge.ErrCommentLookupFailed):
		problem(w, http.StatusInternalServerError, "pull_request_comment_failed")
	case errors.Is(err, pullrequestmerge.ErrProviderUnsupported):
		problem(w, http.StatusConflict, "merge_provider_unsupported")
	case errors.Is(err, pullrequestmerge.ErrReconciliationFailed):
		problem(w, http.StatusInternalServerError, "pull_request_merge_reconciliation_failed")
	case errors.As(err, &githubErr):
		switch githubErr.Kind {
		case pullrequestmerge.GitHubUnavailable:
			problem(w, http.StatusServiceUnavailable, "github_unavailable")
		case pullrequestmerge.GitHubHeadChanged:
			problem(w, http.StatusConflict, "github_head_changed")
		default:
			problem(w, http.StatusConflict, "github_merge_blocked")
		}
	case errors.As(err, &blockedErr):
		problem(w, http.StatusConflict, blockedErr.Reason)
	default:
		problem(w, http.StatusInternalServerError, "pull_request_merge_failed")
	}
}

func (h handler) publish(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(actionContext(r, ""))
	pr, ok := h.o.Catalog.GetPullRequest(r.PathValue("id"))
	if !ok {
		problem(w, 404, "pull_request_not_found")
		return
	}
	if h.o.ContinuePublisher == nil {
		problem(w, http.StatusServiceUnavailable, "pull_request_publication_unavailable")
		return
	}
	worker, err := h.o.ContinuePublisher.PublishLatestContinue(r.Context(), pr.ID, pullrequestlifecycle.RequestID(r.Context()))
	if err != nil {
		continuePublicationProblem(w, err)
		return
	}
	updated, exists := h.o.Catalog.GetPullRequest(pr.ID)
	if !exists {
		problem(w, http.StatusNotFound, "pull_request_not_found")
		return
	}
	write(w, http.StatusOK, map[string]any{"pull_request": h.projected(r.Context(), updated), "worker": worker, "head_commit": worker.ResultHeadCommit})
}
func continuePublicationProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pullrequestwork.ErrPullRequestNotFound):
		problem(w, http.StatusNotFound, "pull_request_not_found")
	case errors.Is(err, pullrequestwork.ErrNotFound):
		problem(w, http.StatusNotFound, "continue_worker_not_found")
	case errors.Is(err, pullrequestwork.ErrPullRequestInactive):
		problemMessage(w, http.StatusConflict, "pull_request_inactive", "The pull request is inactive.")
	case errors.Is(err, pullrequestwork.ErrStaleHead):
		problemMessage(w, http.StatusConflict, "pull_request_head_changed", "The pull request head changed before publication.")
	case errors.Is(err, pullrequestwork.ErrInvalid):
		problemMessage(w, http.StatusConflict, "continue_worker_inactive", "The latest Continue worker is not active.")
	case errors.Is(err, pullrequestwork.ErrPublish):
		problemMessage(w, http.StatusConflict, "pull_request_publish_failed", "The Continue workspace could not be published.")
	default:
		problemMessage(w, http.StatusInternalServerError, "pull_request_publish_failed", "The pull request could not be published.")
	}
}

func (h handler) commits(w http.ResponseWriter, r *http.Request) {
	pr, ok := h.o.Catalog.GetPullRequest(r.PathValue("id"))
	if !ok {
		problem(w, 404, "pull_request_not_found")
		return
	}
	base := pr.DiffBaseCommit
	if base == "" {
		base = pr.BaseCommit
	}
	v, e := h.o.Repository.CommitRange(r.Context(), base, pr.HeadCommit)
	if e != nil {
		logDiffError(r.Context(), pr, base, "commits", e)
		repositoryProblem(w, e)
		return
	}
	write(w, 200, map[string]any{"inputs": map[string]string{"head_commit": pr.HeadCommit, "diff_base_commit": base, "comparison_state": string(pr.ComparisonState)}, "data": v})
}
func (h handler) changes(w http.ResponseWriter, r *http.Request) {
	pr, ok := h.o.Catalog.GetPullRequest(r.PathValue("id"))
	if !ok {
		problem(w, 404, "pull_request_not_found")
		return
	}
	base := pr.DiffBaseCommit
	if base == "" {
		base = pr.BaseCommit
	}
	path := r.URL.Query().Get("path")
	summary := r.URL.Query().Get("summary") == "true"
	var v repository.WorkspaceInspection
	var e error
	if path != "" || summary {
		inspector, ok := h.o.Repository.(interface {
			InspectRangeWithOptions(context.Context, string, string, string, string, repository.InspectOptions) (repository.WorkspaceInspection, error)
		})
		if !ok {
			problem(w, http.StatusServiceUnavailable, "repository_unavailable")
			return
		}
		v, e = inspector.InspectRangeWithOptions(r.Context(), pr.HeadBranch, pr.BaseBranch, base, pr.HeadCommit, repository.InspectOptions{Path: path, SummaryOnly: summary})
	} else {
		v, e = h.o.Repository.InspectRange(r.Context(), pr.HeadBranch, pr.BaseBranch, base, pr.HeadCommit)
	}
	if e != nil {
		logDiffError(r.Context(), pr, base, "changes", e)
		repositoryProblem(w, e)
		return
	}
	write(w, 200, map[string]any{"inputs": map[string]string{"head_commit": pr.HeadCommit, "diff_base_commit": base, "comparison_state": string(pr.ComparisonState)}, "data": v})
}
func logDiffError(ctx context.Context, pr pullrequestlifecycle.PullRequest, base, operation string, err error) {
	if ctx.Err() != nil {
		return
	}
	slog.WarnContext(ctx, "Pull request Git inspection failed", "pull_request_id", pr.ID,
		"operation", operation, "base_commit", base, "head_commit", pr.HeadCommit, "error", err)
}
func repositoryProblem(w http.ResponseWriter, err error) {
	status, code, message := http.StatusServiceUnavailable, "repository_unavailable", "Repository unavailable."
	switch {
	case errors.Is(err, repository.ErrRefNotFound):
		status, code, message = http.StatusNotFound, "ref_not_found", "Reference not found."
	case errors.Is(err, repository.ErrPathNotFound):
		status, code, message = http.StatusNotFound, "path_not_found", "Path not found."
	case errors.Is(err, repository.ErrPathNotDirectory):
		status, code, message = http.StatusConflict, "path_not_directory", "Path is not a directory."
	case errors.Is(err, repository.ErrPathNotFile):
		status, code, message = http.StatusConflict, "path_not_file", "Path is not a file."
	case errors.Is(err, repository.ErrInvalidPath):
		status, code, message = http.StatusBadRequest, "invalid_path", "The repository path is invalid."
	case errors.Is(err, repository.ErrBinaryFile):
		status, code, message = http.StatusUnsupportedMediaType, "binary_file", "Binary files cannot be displayed."
	case errors.Is(err, repository.ErrFileTooLarge):
		status, code, message = http.StatusRequestEntityTooLarge, "file_too_large", "File is too large to display."
	}
	problemMessage(w, status, code, message)
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, code string) {
	problemMessage(w, status, code, strings.ReplaceAll(code, "_", " "))
}
func problemMessage(w http.ResponseWriter, status int, code, message string) {
	write(w, status, map[string]string{"code": code, "message": message})
}

func actionContext(r *http.Request, id string) context.Context {
	if id == "" {
		id = r.Header.Get("X-Request-ID")
	}
	return pullrequestlifecycle.WithRequestID(r.Context(), id)
}

func (h handler) operation(w http.ResponseWriter, r *http.Request) {
	operation, found, err := h.o.Catalog.GetOperation(r.Context(), r.PathValue("requestID"))
	if err != nil {
		problem(w, http.StatusInternalServerError, "operation_read_failed")
		return
	}
	if !found {
		problem(w, http.StatusNotFound, "operation_not_found")
		return
	}
	write(w, http.StatusOK, operation)
}
