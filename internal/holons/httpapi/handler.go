package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/repository"
)

type Service interface {
	SelectTab(context.Context, string, string) error
	List(context.Context) ([]holons.Holon, error)
	Get(context.Context, string) (holons.Holon, error)
	Create(context.Context, holons.Create) (holons.Holon, error)
	Rename(context.Context, string, string) (holons.Holon, error)
	End(context.Context, string) (holons.Holon, error)
	Reopen(context.Context, string) (holons.Holon, error)
	AddAgentSession(context.Context, string, string, string) (holons.Holon, error)
	ReorderTabs(context.Context, string, []holons.TabRef) (holons.Holon, error)
	RenameAgentSession(context.Context, string, string, string) (holons.Holon, error)
	SetAgentSessionStatus(context.Context, string, string, holons.Status, string, string) (holons.Holon, error)
	CancelAgentSession(context.Context, string, string) (holons.Holon, error)
	ResumeAgentSession(context.Context, string, string, string) (holons.Holon, error)
	CloseAgentSession(context.Context, string, string) (holons.Holon, error)
	AddManualTerminal(context.Context, string, string, string, string) (holons.Holon, error)
	UpdateManualTerminal(context.Context, string, string, string) (holons.Holon, error)
	CloseManualTerminal(context.Context, string, string) (holons.Holon, error)
	InspectWorkspace(context.Context, string) (holons.WorkspaceInspection, error)
	Publish(context.Context, string, holons.Publish) (holons.Holon, error)
	PullRequestActivity(context.Context, string) ([]holons.Holon, error)
}
type terminalRelauncher interface {
	RelaunchManualTerminal(context.Context, string, string) (holons.ManualTerminal, error)
}
type workspaceOptionsService interface {
	InspectWorkspaceWithOptions(context.Context, string, holons.InspectOptions) (holons.WorkspaceInspection, error)
}
type commitPromptActor interface {
	AgentCommitPromptAction(context.Context, string, string, string) error
}
type commitAgentCreator interface {
	CreateCommitAgent(context.Context, string, string) (holons.Holon, error)
}
type forkAgentCreator interface {
	ForkAgentSession(context.Context, string, string) (holons.AgentSession, error)
}
type Options struct {
	RepositoryID string
	RuntimeID    string
}

type Handler struct {
	service      Service
	repositoryID string
	runtimeID    string
}

func New(s Service, values ...Options) http.Handler {
	options := Options{}
	if len(values) > 0 {
		options = values[0]
	}
	h := &Handler{service: s, repositoryID: options.RepositoryID, runtimeID: options.RuntimeID}
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/holons", h.list)
	m.HandleFunc("POST /api/v1/holons", h.create)
	m.HandleFunc("GET /api/v1/holons/{id}", h.get)
	m.HandleFunc("PATCH /api/v1/holons/{id}", h.rename)
	m.HandleFunc("PUT /api/v1/holons/{id}/base-branch", h.changeBaseBranch)
	m.HandleFunc("POST /api/v1/holons/{id}/{action}", h.action)
	m.HandleFunc("POST /api/v1/holons/{id}/commit-agent", h.commitAgent)
	m.HandleFunc("GET /api/v1/holons/{id}/publication-readiness", h.publicationReadiness)
	m.HandleFunc("POST /api/v1/holons/{id}/rebase-agent", h.rebaseAgent)
	m.HandleFunc("GET /api/v1/holons/{id}/agent-sessions", h.agents)
	m.HandleFunc("POST /api/v1/holons/{id}/agent-sessions", h.addAgent)
	m.HandleFunc("PATCH /api/v1/holons/{id}/tabs", h.tabs)
	m.HandleFunc("PUT /api/v1/holons/{id}/selected-tab", h.selectedTab)
	m.HandleFunc("PATCH /api/v1/holons/{id}/agent-sessions/{agentID}", h.agent)
	m.HandleFunc("POST /api/v1/holons/{id}/agent-sessions/{agentID}/fork", h.forkAgent)
	m.HandleFunc("POST /api/v1/holons/{id}/agent-sessions/{agentID}/{action}", h.agentAction)
	m.HandleFunc("POST /api/v1/holons/{id}/agent-sessions/{agentID}/commit-prompt", h.commitPrompt)
	m.HandleFunc("POST /api/v1/holons/{id}/terminals", h.addTerminal)
	m.HandleFunc("GET /api/v1/holons/{id}/terminals", h.terminals)
	m.HandleFunc("PATCH /api/v1/holons/{id}/terminals/{terminalID}", h.updateTerminal)
	m.HandleFunc("POST /api/v1/holons/{id}/terminals/{terminalID}/close", h.closeTerminal)
	m.HandleFunc("POST /api/v1/holons/{id}/terminals/{terminalID}/relaunch", h.relaunchTerminal)
	m.HandleFunc("GET /api/v1/holons/{id}/workspace", h.workspace)
	m.HandleFunc("POST /api/v1/holons/{id}/publish", h.publish)
	m.HandleFunc("GET /api/v1/pull-requests/{id}/holon-activity", h.activity)
	return m
}
func (h *Handler) forkAgent(w http.ResponseWriter, r *http.Request) {
	creator, ok := h.service.(forkAgentCreator)
	if !ok {
		http.NotFound(w, r)
		return
	}
	agent, err := creator.ForkAgentSession(r.Context(), r.PathValue("id"), r.PathValue("agentID"))
	if err != nil {
		fail(w, err)
		return
	}
	write(w, http.StatusCreated, agent)
}
func (h *Handler) commitAgent(w http.ResponseWriter, r *http.Request) {
	creator, ok := h.service.(commitAgentCreator)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var in struct {
		SourceAgentID string `json:"source_agent_id"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, holons.ErrInvalid)
		return
	}
	v, err := creator.CreateCommitAgent(r.Context(), r.PathValue("id"), in.SourceAgentID)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, http.StatusCreated, latestAgent(v))
}
func (h *Handler) commitPrompt(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.service.(commitPromptActor)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var in struct {
		Action string `json:"action"`
	}
	if e := decode(w, r, &in); e != nil {
		fail(w, e)
		return
	}
	if e := actor.AgentCommitPromptAction(r.Context(), r.PathValue("id"), r.PathValue("agentID"), in.Action); e != nil {
		fail(w, e)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) relaunchTerminal(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(terminalRelauncher)
	if !ok {
		http.NotFound(w, r)
		return
	}
	terminal, e := service.RelaunchManualTerminal(r.Context(), r.PathValue("id"), r.PathValue("terminalID"))
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, terminal)
}
func (h *Handler) agent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string `json:"title"`
	}
	if e := decode(w, r, &in); e != nil {
		fail(w, e)
		return
	}
	v, e := h.service.RenameAgentSession(r.Context(), r.PathValue("id"), r.PathValue("agentID"), in.Title)
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, agentFrom(v, r.PathValue("agentID")))
}
func (h *Handler) agentAction(w http.ResponseWriter, r *http.Request) {
	id, aid := r.PathValue("id"), r.PathValue("agentID")
	var v holons.Holon
	var e error
	switch r.PathValue("action") {
	case "close":
		v, e = h.service.CloseAgentSession(r.Context(), id, aid)
	case "cancel":
		v, e = h.service.CancelAgentSession(r.Context(), id, aid)
	case "resume":
		var in struct {
			ResumeTarget string `json:"resume_target"`
		}
		if r.ContentLength != 0 {
			if e = decode(w, r, &in); e != nil {
				break
			}
		}
		v, e = h.service.ResumeAgentSession(r.Context(), id, aid, in.ResumeTarget)
	default:
		http.NotFound(w, r)
		return
	}
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, agentFrom(v, aid))
}
func (h *Handler) addTerminal(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title      string `json:"title"`
		CWD        string `json:"cwd"`
		TerminalID string `json:"terminal_id"`
	}
	if e := decode(w, r, &in); e != nil {
		fail(w, e)
		return
	}
	v, e := h.service.AddManualTerminal(r.Context(), r.PathValue("id"), in.Title, in.CWD, in.TerminalID)
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 201, latestTerminal(v))
}
func (h *Handler) updateTerminal(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string `json:"title"`
	}
	if e := decode(w, r, &in); e != nil {
		fail(w, e)
		return
	}
	v, e := h.service.UpdateManualTerminal(r.Context(), r.PathValue("id"), r.PathValue("terminalID"), in.Title)
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, terminalFrom(v, r.PathValue("terminalID")))
}
func (h *Handler) closeTerminal(w http.ResponseWriter, r *http.Request) {
	v, e := h.service.CloseManualTerminal(r.Context(), r.PathValue("id"), r.PathValue("terminalID"))
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, terminalFrom(v, r.PathValue("terminalID")))
}
func (h *Handler) terminals(w http.ResponseWriter, r *http.Request) {
	v, e := h.service.Get(r.Context(), r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, map[string]any{"manual_terminals": publicManualTerminals(v.ManualTerminals)})
}
func (h *Handler) workspace(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("contents") == "true" && (strings.TrimSpace(r.URL.Query().Get("path")) == "" || r.URL.Query().Get("summary") == "true") {
		fail(w, holons.ErrInvalid)
		return
	}

	var v holons.WorkspaceInspection
	var e error
	if service, ok := h.service.(workspaceOptionsService); ok {
		v, e = service.InspectWorkspaceWithOptions(r.Context(), r.PathValue("id"), holons.InspectOptions{Contents: r.URL.Query().Get("contents") == "true", BaseRef: r.URL.Query().Get("base"), TargetRef: r.URL.Query().Get("target"), SummaryOnly: r.URL.Query().Get("summary") == "true", Path: r.URL.Query().Get("path")})
	} else {
		v, e = h.service.InspectWorkspace(r.Context(), r.PathValue("id"))
	}
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, v)
}
func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	var in holons.Publish
	if e := decode(w, r, &in); e != nil {
		fail(w, e)
		return
	}
	in.TargetCommit = strings.Trim(r.Header.Get("If-Match"), `"`)
	v, e := h.service.Publish(r.Context(), r.PathValue("id"), in)
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, h.projectHolon(r.Context(), v))
}
func (h *Handler) activity(w http.ResponseWriter, r *http.Request) {
	v, e := h.service.PullRequestActivity(r.Context(), r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, map[string]any{"holons": h.projectHolons(r.Context(), v)})
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "holon_failed", "The Holon operation failed."
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		status, code, message = http.StatusGatewayTimeout, "operation_timeout", "The operation timed out. Try again."
	case errors.Is(err, context.Canceled):
		status, code, message = http.StatusRequestTimeout, "request_canceled", "The request was canceled."
	case errors.Is(err, holons.ErrRebaseActive), errors.Is(err, holons.ErrRebaseRequired), errors.Is(err, holons.ErrPublicationUnavailable):
		status, code, message = http.StatusConflict, "publication_unavailable", err.Error()
	case errors.Is(err, pullrequestlifecycle.ErrPublicationUnavailable):
		status, code, message = http.StatusConflict, "publish_unavailable", err.Error()
	case errors.Is(err, pullrequestlifecycle.ErrPublicationInactive):
		status, code, message = http.StatusConflict, "pull_request_inactive", err.Error()
	case errors.Is(err, pullrequestlifecycle.ErrPublicationStale), errors.Is(err, pullrequestlifecycle.ErrSynchronizationStale):
		status, code, message = http.StatusConflict, "stale_head", err.Error()
	case errors.Is(err, pullrequestlifecycle.ErrPublicationNoChanges):
		status, code, message = http.StatusConflict, "no_changes", err.Error()
	case errors.Is(err, pullrequestlifecycle.ErrWorkspaceDirty):
		status, code, message = http.StatusConflict, "workspace_dirty", err.Error()
	case errors.Is(err, agentsettings.ErrUnavailable), errors.Is(err, agentsettings.ErrNoAvailable):
		status, code, message = http.StatusServiceUnavailable, "harness_unavailable", err.Error()
	case errors.Is(err, agentsettings.ErrInvalid):
		status, code, message = http.StatusBadRequest, "invalid_harness", "Choose a supported agent harness."
	case errors.Is(err, holons.ErrNotFound):
		status, code, message = http.StatusNotFound, "holon_not_found", "Holon not found."
	case errors.Is(err, holons.ErrAgentSessionNotFound):
		status, code, message = http.StatusNotFound, "agent_session_not_found", "Agent session not found."
	case errors.Is(err, holons.ErrManualTerminalNotFound):
		status, code, message = http.StatusNotFound, "manual_terminal_not_found", "Shell tab not found."
	case errors.Is(err, holons.ErrAgentSessionNotForkable):
		status, code, message = http.StatusConflict, "agent_session_not_forkable", "This agent's conversation is not ready to fork yet."
	case errors.Is(err, holons.ErrInvalid):
		status, code, message = http.StatusBadRequest, "invalid_request", "The Holon request is invalid."
	case errors.Is(err, pullrequestwork.ErrBusy):
		status, code, message = http.StatusConflict, "pull_request_work_busy", err.Error()
	case errors.Is(err, pullrequestwork.ErrNotFound):
		status, code, message = http.StatusNotFound, "pull_request_work_not_found", "Continue work not found."
	case errors.Is(err, pullrequestwork.ErrPullRequestInactive), errors.Is(err, pullrequestwork.ErrInvalid):
		status, code, message = http.StatusConflict, "continue_worker_inactive", "The Continue worker is not active."
	case errors.Is(err, pullrequestwork.ErrStaleHead):
		status, code, message = http.StatusConflict, "stale_head", "The pull request head changed."
	case errors.Is(err, pullrequestwork.ErrPublish):
		status, code, message = http.StatusConflict, "publish_failed", "The Continue workspace could not be published."
	case errors.Is(err, holons.ErrCommitSourceMissing):
		status, code, message = http.StatusConflict, "commit_source_missing", "Select an open agent tab to commit."
	case errors.Is(err, holons.ErrCommitSourceNotForkable):
		status, code, message = http.StatusConflict, "commit_source_not_forkable", "This agent's conversation is not ready to fork yet."
	case errors.Is(err, holons.ErrCommitForkInProgress):
		status, code, message = http.StatusConflict, "commit_fork_in_progress", "A commit agent is already active."
	case errors.Is(err, agentsessions.ErrUnavailable):
		status, code, message = http.StatusServiceUnavailable, "harness_unavailable", "The source agent's harness is unavailable."
	case errors.Is(err, holons.ErrNotResumable):
		status, code, message = http.StatusConflict, "holon_not_resumable", "The Holon cannot be resumed."
	case errors.Is(err, holons.ErrNotCancellable):
		status, code, message = http.StatusConflict, "holon_not_cancellable", "The Holon cannot be cancelled."
	case errors.Is(err, holons.ErrTerminalLimit):
		status, code, message = http.StatusConflict, "manual_terminal_limit", "The manual terminal limit was reached."
	case errors.Is(err, repository.ErrRefNotFound):
		status, code, message = http.StatusNotFound, "ref_not_found", "Reference not found."
	case errors.Is(err, repository.ErrInvalidPath):
		status, code, message = http.StatusBadRequest, "invalid_path", "The repository path is invalid."
	case errors.Is(err, repository.ErrPathNotFound):
		status, code, message = http.StatusNotFound, "path_not_found", "Path not found."
	case errors.Is(err, repository.ErrPathNotDirectory):
		status, code, message = http.StatusConflict, "path_not_directory", "Path is not a directory."
	case errors.Is(err, repository.ErrPathNotFile):
		status, code, message = http.StatusConflict, "path_not_file", "Path is not a file."
	case errors.Is(err, repository.ErrBinaryFile):
		status, code, message = http.StatusUnsupportedMediaType, "binary_file", "Binary files cannot be displayed."
	case errors.Is(err, repository.ErrRepositoryUnavailable), errors.Is(err, repository.ErrInvalidWorkspace):
		status, code, message = http.StatusServiceUnavailable, "repository_unavailable", "Repository unavailable."
	case errors.Is(err, repository.ErrWorkspaceDirty):
		status, code, message = http.StatusConflict, "workspace_dirty", "The workspace has uncommitted changes."
	case errors.Is(err, repository.ErrWorkspaceBranch):
		status, code, message = http.StatusConflict, "workspace_changed", "The workspace branch changed."
	case errors.Is(err, repository.ErrStaleHead):
		status, code, message = http.StatusConflict, "stale_head", "The remote head changed."
	}
	write(w, status, map[string]string{"code": code, "message": message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return holons.ErrInvalid
	}
	if e := d.Decode(&struct{}{}); !errors.Is(e, io.EOF) {
		return holons.ErrInvalid
	}
	return nil
}
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	v, e := h.service.List(r.Context())
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, map[string]any{"holons": h.projectHolons(r.Context(), v)})
}
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	v, e := h.service.Get(r.Context(), r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, h.projectHolon(r.Context(), v))
}
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var in holons.Create
	if e := decode(w, r, &in); e != nil {
		fail(w, e)
		return
	}
	var v holons.Holon
	var e error
	status := http.StatusCreated
	async := false
	for _, preference := range strings.Split(r.Header.Get("Prefer"), ",") {
		if strings.EqualFold(strings.TrimSpace(preference), "respond-async") {
			async = true
		}
	}
	if creator, ok := h.service.(interface {
		CreateAsync(context.Context, holons.Create) (holons.Holon, error)
	}); async && ok {
		v, e = creator.CreateAsync(r.Context(), in)
		status = http.StatusAccepted
	} else {
		v, e = h.service.Create(r.Context(), in)
	}
	if e != nil {
		fail(w, e)
		return
	}
	if status == http.StatusAccepted {
		w.Header().Set("Location", "/api/v1/holons/"+v.ID)
		w.Header().Set("Preference-Applied", "respond-async")
	}
	write(w, status, h.projectHolon(r.Context(), v))
}
func latestAgent(h holons.Holon) holons.AgentSession { return h.AgentSessions[len(h.AgentSessions)-1] }
func agentFrom(h holons.Holon, id string) holons.AgentSession {
	for _, v := range h.AgentSessions {
		if v.ID == id {
			return v
		}
	}
	return holons.AgentSession{}
}
func latestTerminal(h holons.Holon) holons.ManualTerminal {
	// Closed tabs retain their old order; a newly appended open tab can sort
	// before them when the visible tab order is reused.
	for i := len(h.ManualTerminals) - 1; i >= 0; i-- {
		if h.ManualTerminals[i].ClosedAt == nil {
			return h.ManualTerminals[i]
		}
	}
	return holons.ManualTerminal{}
}
func terminalFrom(h holons.Holon, id string) holons.ManualTerminal {
	for _, v := range h.ManualTerminals {
		if v.ID == id {
			return v
		}
	}
	return holons.ManualTerminal{}
}
func (h *Handler) changeBaseBranch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Branch string `json:"base_branch"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	service, ok := h.service.(interface {
		ChangeBaseBranch(context.Context, string, string) (holons.Holon, error)
	})
	if !ok {
		fail(w, holons.ErrInvalid)
		return
	}
	v, err := service.ChangeBaseBranch(r.Context(), r.PathValue("id"), in.Branch)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, http.StatusOK, h.projectHolon(r.Context(), v))
}

func (h *Handler) rename(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string `json:"title"`
	}
	if e := decode(w, r, &in); e != nil {
		fail(w, e)
		return
	}
	v, e := h.service.Rename(r.Context(), r.PathValue("id"), in.Title)
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, h.projectHolon(r.Context(), v))
}
func (h *Handler) action(w http.ResponseWriter, r *http.Request) {
	var (
		v holons.Holon
		e error
	)
	switch r.PathValue("action") {
	case "end":
		v, e = h.service.End(r.Context(), r.PathValue("id"))
	case "reopen":
		v, e = h.service.Reopen(r.Context(), r.PathValue("id"))
	default:
		http.NotFound(w, r)
		return
	}
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, h.projectHolon(r.Context(), v))
}
func (h *Handler) agents(w http.ResponseWriter, r *http.Request) {
	v, e := h.service.Get(r.Context(), r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, map[string]any{"agent_sessions": v.AgentSessions})
}
func (h *Handler) addAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AgentType string `json:"agent_type"`
		Prompt    string `json:"prompt"`
	}
	if e := decode(w, r, &in); e != nil || strings.TrimSpace(in.AgentType) == "" {
		fail(w, holons.ErrInvalid)
		return
	}
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		if len(key) > 128 {
			fail(w, holons.ErrInvalid)
			return
		}
		service, ok := h.service.(interface {
			AddAgentSessionOnce(context.Context, string, string, string, string) (holons.AgentSession, error)
		})
		if !ok {
			fail(w, holons.ErrInvalid)
			return
		}
		agent, err := service.AddAgentSessionOnce(r.Context(), r.PathValue("id"), in.AgentType, in.Prompt, key)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, http.StatusCreated, agent)
		return
	}
	v, e := h.service.AddAgentSession(r.Context(), r.PathValue("id"), in.AgentType, in.Prompt)
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 201, latestAgent(v))
}
func (h *Handler) tabs(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Tabs []holons.TabRef `json:"tabs"`
	}
	if e := decode(w, r, &in); e != nil {
		fail(w, e)
		return
	}
	v, e := h.service.ReorderTabs(r.Context(), r.PathValue("id"), in.Tabs)
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, h.projectHolon(r.Context(), v))
}

type holonWire holons.Holon

type holonResponse struct {
	holonWire
	RepositoryID           string                 `json:"repository_id,omitempty"`
	RuntimeID              string                 `json:"runtime_id,omitempty"`
	WorkSessionStartCommit string                 `json:"work_session_start_commit,omitempty"`
	ProposedUpstreamBranch string                 `json:"proposed_upstream_branch,omitempty"`
	AgentSession           *holons.AgentSession   `json:"agent_session,omitempty"`
	Activity               protocol.AgentActivity `json:"activity"`
	InputState             string                 `json:"input_state"`
}

func (h *Handler) projectHolons(ctx context.Context, values []holons.Holon) []holonResponse {
	projected := make([]holonResponse, 0, len(values))
	for _, value := range values {
		projected = append(projected, h.projectHolon(ctx, value))
	}
	return projected
}

func (h *Handler) projectHolon(ctx context.Context, value holons.Holon) holonResponse {
	if projector, ok := h.service.(interface {
		ProjectHolon(context.Context, holons.Holon) holons.Holon
	}); ok {
		value = projector.ProjectHolon(ctx, value)
	}
	if value.AgentSessions == nil {
		value.AgentSessions = []holons.AgentSession{}
	}
	value.ManualTerminals = publicManualTerminals(value.ManualTerminals)
	if value.IDEs == nil {
		value.IDEs = []holons.IDE{}
	}
	workSessionStartCommit := value.WorkSessionStartCommit
	if workSessionStartCommit == "" {
		workSessionStartCommit = value.BaseCommit
	}
	return holonResponse{
		holonWire:              holonWire(value),
		RepositoryID:           h.repositoryID,
		RuntimeID:              h.runtimeID,
		WorkSessionStartCommit: workSessionStartCommit,
		ProposedUpstreamBranch: value.WorktreeBranch,
		AgentSession:           primaryAgentSession(value.AgentSessions),
		InputState:             aggregateInputState(value.AgentSessions),
		Activity:               holons.AggregateActivity(value.AgentSessions),
	}
}

func publicManualTerminals(values []holons.ManualTerminal) []holons.ManualTerminal {
	projected := make([]holons.ManualTerminal, 0, len(values))
	for _, value := range values {
		if value.ClosedAt == nil {
			projected = append(projected, value)
		}
	}
	return projected
}

func primaryAgentSession(values []holons.AgentSession) *holons.AgentSession {
	var selected *holons.AgentSession
	for index := range values {
		candidate := &values[index]
		if candidate.ClosedAt != nil {
			continue
		}
		if selected == nil || agentStatusRank(holons.Status(candidate.Status)) > agentStatusRank(holons.Status(selected.Status)) ||
			(agentStatusRank(holons.Status(candidate.Status)) == agentStatusRank(holons.Status(selected.Status)) && candidate.UpdatedAt.After(selected.UpdatedAt)) {
			selected = candidate
		}
	}
	return selected
}

func aggregateInputState(values []holons.AgentSession) string {
	hasActiveAgent := false
	hasUserInputRequired := false
	hasRunningAgent := false
	for _, agent := range values {
		if agent.ClosedAt != nil || holons.IsTerminal(holons.Status(agent.Status)) {
			continue
		}
		hasActiveAgent = true
		switch agent.InputState {
		case "permission_required":
			return "permission_required"
		case "user_input_required":
			hasUserInputRequired = true
		case "task_complete":
		default:
			hasRunningAgent = true
		}
	}
	if hasUserInputRequired {
		return "user_input_required"
	}
	if hasRunningAgent || !hasActiveAgent {
		return "none"
	}
	return "task_complete"
}

func agentStatusRank(status holons.Status) int {
	switch status {
	case holons.StatusCancelling:
		return 90
	case holons.StatusRestoring:
		return 85
	case holons.StatusRunning:
		return 80
	case holons.StatusPreparing:
		return 70
	case holons.StatusQueued, holons.StatusNaming:
		return 60
	case holons.StatusRecoveryFailed:
		return 50
	case holons.StatusFailed, holons.StatusLost, holons.StatusExpired:
		return 30
	case holons.StatusCancelled:
		return 20
	case holons.StatusCompleted:
		return 10
	}
	return 0
}

func (h *Handler) publicationReadiness(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(interface {
		PublicationReadiness(context.Context, string) (holons.PublicationReadiness, error)
	})
	if !ok {
		http.NotFound(w, r)
		return
	}
	v, err := service.PublicationReadiness(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	write(w, http.StatusOK, v)
}
func (h *Handler) rebaseAgent(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(interface {
		CreateRebaseAgent(context.Context, string, string) (holons.Holon, error)
	})
	if !ok {
		http.NotFound(w, r)
		return
	}
	var in struct {
		SourceAgentID string `json:"source_agent_id"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, holons.ErrInvalid)
		return
	}
	v, err := service.CreateRebaseAgent(r.Context(), r.PathValue("id"), in.SourceAgentID)
	if err != nil {
		fail(w, err)
		return
	}
	if v.RebaseAttempt != nil && v.RebaseAttempt.State == "succeeded" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	for _, a := range v.AgentSessions {
		if v.RebaseAttempt != nil && a.ID == v.RebaseAttempt.AgentID {
			write(w, http.StatusCreated, a)
			return
		}
	}
	fail(w, holons.ErrNotFound)
}

func (h *Handler) selectedTab(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TabID string `json:"tab_id"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := h.service.SelectTab(r.Context(), r.PathValue("id"), in.TabID); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
