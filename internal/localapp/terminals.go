package localapp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"sync"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/ide"
	"github.com/holark-ai/holark/internal/prompt_templates"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/sessionterminals"
	"github.com/holark-ai/holark/internal/terminals"
)

const localTerminalHost = "local"

func clearStaleTerminalBindings(ctx context.Context, service *holons.Service) error {
	all, err := service.List(ctx)
	if err != nil {
		return err
	}
	for _, h := range all {
		// A crash can leave a binding after expiry was saved but before the
		// exit callback ran. Reconcile it even when the Holon is archived.
		for _, agent := range h.AgentSessions {
			if agent.TerminalID != "" && holons.IsTerminal(holons.Status(agent.Status)) {
				if _, err = service.CompleteAgentSession(ctx, h.ID, agent.ID, agent.TerminalID, 0); err != nil {
					return err
				}
			}
		}
		if h.ArchivedAt != nil || h.ReadOnly {
			continue
		}
		// Settle cancellation first: recovering another tab recomputes the
		// aggregate status, which would otherwise expose a pending cancellation
		// halfway through this loop and reject subsequent restoration.
		for _, agent := range h.AgentSessions {
			if agent.ClosedAt != nil {
				continue
			}
			status := holons.Status(agent.Status)
			if status == holons.StatusCancelling {
				_, err = service.SetAgentSessionStatus(ctx, h.ID, agent.ID, holons.StatusCancelled, "", agent.ResumeTarget)
			}
			if err != nil {
				return err
			}
		}
		// Cancellation intent is stored per agent. The aggregate Holon status
		// can be cancelling when only one tab was ended; its peers still recover.
		for _, agent := range h.AgentSessions {
			status := holons.Status(agent.Status)
			if agent.ClosedAt == nil && status != holons.StatusCancelling && (!holons.IsTerminal(status) || status == holons.StatusRecoveryFailed) {
				if _, err = service.PrepareAgentRecovery(ctx, h.ID, agent.ID); err != nil {
					return err
				}
			}
		}
		for _, tab := range h.ManualTerminals {
			if tab.ClosedAt == nil && tab.TerminalID != "" {
				if _, err = service.SetManualTerminalBinding(ctx, h.ID, tab.ID, ""); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// localTerminalProducts maps durable Holon records to live terminal bindings.
type localTerminalProducts struct {
	rebaseAgents      *rebaseAgentCoordinator
	holons            *holons.Service
	addressCompletion *addressCompletionCoordinator
}

func (p *localTerminalProducts) HarnessTerminalBinding(holonID, agentID string) (terminals.TerminalID, string, bool) {
	h, err := p.holons.Get(context.Background(), holonID)
	if err != nil {
		return "", "", false
	}
	for _, a := range h.AgentSessions {
		if a.ID == agentID && a.ClosedAt == nil && a.TerminalID != "" {
			return terminals.TerminalID(a.TerminalID), localTerminalHost, true
		}
	}
	return "", "", false
}
func (p *localTerminalProducts) ManualTerminalBinding(holonID, recordID string) (terminals.TerminalID, string, string, bool) {
	h, err := p.holons.Get(context.Background(), holonID)
	if err != nil {
		return "", "", "", false
	}
	for _, t := range h.ManualTerminals {
		if t.ID == recordID && t.ClosedAt == nil && t.TerminalID != "" {
			return terminals.TerminalID(t.TerminalID), localTerminalHost, t.CWD, true
		}
	}
	return "", "", "", false
}
func (p *localTerminalProducts) ResolveTerminalBinding(id terminals.TerminalID) (sessionterminals.Binding, bool) {
	all, err := p.holons.List(context.Background())
	if err != nil {
		return sessionterminals.Binding{}, false
	}
	for _, h := range all {
		for _, t := range h.ManualTerminals {
			if t.ClosedAt == nil && t.TerminalID == string(id) {
				return sessionterminals.Binding{TerminalID: id, OwnerKind: terminals.OwnerManual, OwnerID: t.ID, SessionID: h.ID, RuntimeID: localTerminalHost, CWD: t.CWD}, true
			}
		}
		for _, a := range h.AgentSessions {
			if a.ClosedAt == nil && a.TerminalID == string(id) {
				return sessionterminals.Binding{TerminalID: id, OwnerKind: terminals.OwnerHarness, OwnerID: a.ID, SessionID: h.ID, RuntimeID: localTerminalHost}, true
			}
		}
	}
	return sessionterminals.Binding{}, false
}

// resolveTerminalCompletionOwner includes hidden agent history so a process
// that exits after its tab closes can still settle the durable lifecycle row.
// Ordinary attachment and inventory lookups intentionally continue to exclude
// closed agents.
func (p *localTerminalProducts) resolveTerminalCompletionOwner(id terminals.TerminalID) (sessionterminals.Binding, bool) {
	all, err := p.holons.List(context.Background())
	if err != nil {
		return sessionterminals.Binding{}, false
	}
	for _, h := range all {
		for _, t := range h.ManualTerminals {
			if t.ClosedAt == nil && t.TerminalID == string(id) {
				return sessionterminals.Binding{TerminalID: id, OwnerKind: terminals.OwnerManual, OwnerID: t.ID, SessionID: h.ID, RuntimeID: localTerminalHost, CWD: t.CWD}, true
			}
		}
		for _, a := range h.AgentSessions {
			if a.TerminalID == string(id) {
				return sessionterminals.Binding{TerminalID: id, OwnerKind: terminals.OwnerHarness, OwnerID: a.ID, SessionID: h.ID, RuntimeID: localTerminalHost}, true
			}
		}
	}
	return sessionterminals.Binding{}, false
}
func (p *localTerminalProducts) TerminalBindingsForRuntime(string) ([]sessionterminals.Binding, error) {
	all, err := p.holons.List(context.Background())
	if err != nil {
		return nil, err
	}
	var out []sessionterminals.Binding
	for _, h := range all {
		for _, t := range h.ManualTerminals {
			if t.ClosedAt == nil && t.TerminalID != "" {
				out = append(out, sessionterminals.Binding{TerminalID: terminals.TerminalID(t.TerminalID), OwnerKind: terminals.OwnerManual, OwnerID: t.ID, SessionID: h.ID, RuntimeID: localTerminalHost, CWD: t.CWD})
			}
		}
		for _, a := range h.AgentSessions {
			if a.ClosedAt == nil && a.TerminalID != "" {
				out = append(out, sessionterminals.Binding{TerminalID: terminals.TerminalID(a.TerminalID), OwnerKind: terminals.OwnerHarness, OwnerID: a.ID, SessionID: h.ID, RuntimeID: localTerminalHost, CWD: h.WorktreePath})
			}
		}
	}
	return out, nil
}
func (p *localTerminalProducts) SessionHarnessIDs(id string) ([]string, error) {
	h, e := p.holons.Get(context.Background(), id)
	if e != nil {
		return nil, e
	}
	var v []string
	for _, a := range h.AgentSessions {
		if a.ClosedAt == nil {
			v = append(v, a.ID)
		}
	}
	return v, nil
}
func (p *localTerminalProducts) SessionTerminalIDs(id string) ([]terminals.TerminalID, error) {
	h, e := p.holons.Get(context.Background(), id)
	if e != nil {
		return nil, e
	}
	var v []terminals.TerminalID
	for _, t := range h.ManualTerminals {
		if t.ClosedAt == nil && t.TerminalID != "" {
			v = append(v, terminals.TerminalID(t.TerminalID))
		}
	}
	for _, a := range h.AgentSessions {
		if a.ClosedAt == nil && a.TerminalID != "" {
			v = append(v, terminals.TerminalID(a.TerminalID))
		}
	}
	return v, nil
}
func (p *localTerminalProducts) CancelHarnessRuntime(string, string) error { return nil }
func (p *localTerminalProducts) RetireHarnessRuntime(string, string) error { return nil }
func (p *localTerminalProducts) ApplyTerminalCompletion(_ string, completion terminals.ProcessCompletion) error {
	binding, ok := p.resolveTerminalCompletionOwner(completion.TerminalID)
	if !ok {
		return nil
	}
	if binding.OwnerKind == terminals.OwnerManual {
		_, err := p.holons.CloseManualTerminal(context.Background(), binding.SessionID, binding.OwnerID)
		return err
	}
	// A pending Address result owns process-exit verification and retirement too.
	if current, err := p.holons.Get(context.Background(), binding.SessionID); err == nil && current.ApplicationPhase == "finalizing" {
		defer p.holons.BeginFinalization(binding.SessionID)()
	}
	h, err := p.holons.CompleteAgentSession(context.Background(), binding.SessionID, binding.OwnerID, string(completion.TerminalID), completion.ExitCode)
	if err == nil && h.RebaseAttempt != nil && h.RebaseAttempt.AgentID == binding.OwnerID {
		if p.rebaseAgents != nil && (h.RebaseCompletionReady(binding.OwnerID) || h.RebaseAttempt.ClosurePending(binding.OwnerID)) {
			err = p.rebaseAgents.complete(context.Background(), h.ID, binding.OwnerID, p.addressCompletion)
		}
		if p.addressCompletion != nil {
			err = errors.Join(err, p.addressCompletion.AgentStopped(context.Background(), h, binding.OwnerID))
		}
		if err == nil {
			_, err = p.holons.ArchiveFinite(context.Background(), h.ID)
		}
	}
	return err
}
func (p *localTerminalProducts) ApplyTerminalLost(_ string, id terminals.TerminalID) error {
	b, ok := p.ResolveTerminalBinding(id)
	if !ok {
		return nil
	}
	if b.OwnerKind == terminals.OwnerHarness {
		h, e := p.holons.Get(context.Background(), b.SessionID)
		if e != nil {
			return e
		}
		resumeTarget := h.AgentSession(b.OwnerID).ResumeTarget
		h, e = p.holons.SetAgentSessionStatus(context.Background(), b.SessionID, b.OwnerID, holons.StatusLost, "Agent process was lost.", resumeTarget)
		if e == nil && p.addressCompletion != nil {
			e = p.addressCompletion.AgentStopped(context.Background(), h, b.OwnerID)
		}
		return e
	}
	_, e := p.holons.SetManualTerminalBinding(context.Background(), b.SessionID, b.OwnerID, "")
	return e
}
func (p *localTerminalProducts) RemoveManualTerminal(id, record string) error {
	_, e := p.holons.CloseManualTerminal(context.Background(), id, record)
	return e
}

// terminalHolonService couples creation/closing of a durable tab to its local PTY.
type holonIDELifecycle interface {
	Close(context.Context, string, string) (ide.IDE, error)
	Suspend(context.Context, string, string) (ide.IDE, error)
	Resume(context.Context, string, string) (ide.IDE, error)
}

type holonAgentLauncher interface {
	Launch(context.Context, string, string, terminals.Dimensions, string, agentsessions.LaunchOptions) error
	Cancel(context.Context, string, string) error
	Submit(context.Context, string, string, string) error
}

type localHarnessPreferences interface {
	Resolve(context.Context, agentsettings.Workflow, protocol.HarnessType) (agentsettings.Default, error)
	ResolveDefault(context.Context, agentsettings.Workflow) (protocol.HarnessType, error)
	Validate(context.Context, protocol.HarnessType, agentsettings.Workflow) error
}

type terminalHolonService struct {
	startups *manualStartupJobs
	actions  pullrequestlifecycle.ActionRegistry
	naming   interface {
		GenerateIdentity(agentsessions.Session, agentsessions.LaunchOptions)
	}
	// Serializes agent reservation and launch with recovery and review publication.
	recoveryMu        sync.Mutex
	agentRequests     sync.Map // agent ID -> completion channel for the current launch owner
	queuedLaunchLocks sync.Map // holon ID -> mutex serializing reserved launch with End
	*holons.Service
	commitAgents      *commitAgentCoordinator
	forkAgents        *forkAgentCoordinator
	rebaseAgents      *rebaseAgentCoordinator
	addressCompletion *addressCompletionCoordinator
	workCompletion    *localPullRequestWorkCompletionCoordinator
	terminals         *sessionterminals.Coordinator
	agents            holonAgentLauncher
	ides              holonIDELifecycle
	templates         prompttemplates.Reader
	work              localCommitWorkReader
	publication       interface {
		Publish(context.Context, string, string) (holons.Holon, error)
	}
	harnesses          localHarnessPreferences
	panelPins          pullRequestPanelPinner
	pullRequestCatalog localPullRequestLinkCatalog
	repositoryID       string
	logger             *log.Logger
	actionMu           sync.Mutex
	discussed          map[[3]string]bool
}

func (s *terminalHolonService) AgentCommitPromptAction(ctx context.Context, id, aid, action string) error {
	transition := holons.CommitPromptKeep
	if action == "discuss_commit_message" {
		transition = holons.CommitPromptDiscuss
	} else if action == "skip_for_change" {
		transition = holons.CommitPromptSkip
	} else if action != "keep_discussing" {
		return holons.ErrInvalid
	}
	s.actionMu.Lock()
	defer s.actionMu.Unlock()

	inspection, err := s.InspectWorkspace(ctx, id)
	if err != nil {
		return err
	}
	if !inspection.Dirty {
		s.clearDiscussions(id, aid, "")
		_, _, err = s.TransitionAgentCommitPrompt(ctx, id, aid, holons.CommitPromptClean, "")
		return err
	}
	payload, _ := json.Marshal(struct {
		Head    string
		Changes []holons.WorkspaceChange
	}{inspection.HeadCommit, inspection.Changes})
	hash := fmt.Sprintf("%x", sha256.Sum256(payload))
	s.clearDiscussions(id, aid, hash)
	key := [3]string{id, aid, hash}
	if s.discussed[key] {
		_, _, err = s.TransitionAgentCommitPrompt(ctx, id, aid, holons.CommitPromptDiscuss, hash)
		if err == nil {
			delete(s.discussed, key)
		}
		return err
	}
	if transition == holons.CommitPromptDiscuss {
		_, accepted, prepareErr := s.TransitionAgentCommitPrompt(ctx, id, aid, holons.CommitPromptPrepareDiscuss, hash)
		if prepareErr != nil || !accepted {
			return prepareErr
		}
		if s.agents == nil {
			return holons.ErrInvalid
		}
		h, getErr := s.Get(ctx, id)
		if getErr != nil {
			return getErr
		}
		prompt := commitRequestPrompt(ctx, h, s.templates, s.work, false)
		if s.templates == nil {
			// Retain the banner action fallback when no template reader is wired.
			prompt = "Please inspect the current uncommitted changes, propose a concise commit message, and ask whether to commit, revise, or leave them uncommitted."
		}
		if s.discussed == nil {
			s.discussed = make(map[[3]string]bool)
		}
		s.discussed[key] = false
		if err = s.agents.Submit(ctx, id, aid, prompt); err != nil {
			delete(s.discussed, key)
			return err
		}
		s.discussed[key] = true
	}
	_, _, err = s.TransitionAgentCommitPrompt(ctx, id, aid, transition, hash)
	if err == nil {
		delete(s.discussed, key)
	}
	return err
}

func (s *terminalHolonService) clearDiscussions(id, aid, except string) {
	for key := range s.discussed {
		if key[0] == id && key[1] == aid && key[2] != except {
			delete(s.discussed, key)
		}
	}
}

func (s *terminalHolonService) launchAgent(ctx context.Context, h holons.Holon, agentID string, options agentsessions.LaunchOptions) (holons.Holon, error) {
	if err := s.launchAgentRuntime(ctx, h, agentID, options); err != nil {
		return holons.Holon{}, err
	}
	if s.agents == nil {
		return h, nil
	}
	return s.Get(ctx, h.ID)
}

// launchAgentRuntime keeps successful execution separate from the optional
// database refresh, whose failure must not be treated as a failed launch.
func (s *terminalHolonService) launchAgentRuntime(ctx context.Context, h holons.Holon, agentID string, options agentsessions.LaunchOptions) error {
	if s.agents != nil {
		if err := s.agents.Launch(ctx, h.ID, agentID, terminals.Dimensions{Columns: 120, Rows: 36}, "", options); err != nil {
			return err
		}
	}
	s.pinHolonPullRequests(ctx, h)
	return nil
}
func (s *terminalHolonService) PreflightAgent(ctx context.Context, kind holons.Kind) (string, error) {
	if s.harnesses == nil {
		return "codex", nil
	}
	harnessType, err := s.harnesses.ResolveDefault(ctx, workflowForKind(kind))
	if err != nil {
		return "", err
	}
	return string(harnessType), nil
}

func (s *terminalHolonService) Create(ctx context.Context, in holons.Create) (holons.Holon, error) {
	generateTitle := (in.Kind == "" || in.Kind == holons.KindNormal) && strings.TrimSpace(in.Title) == ""
	return s.createWithLaunchOptions(ctx, in, agentsessions.LaunchOptions{GenerateIdentity: true, GenerateTitle: generateTitle}, nil)
}

func (s *terminalHolonService) createWithLaunchOptions(ctx context.Context, in holons.Create, options agentsessions.LaunchOptions, prepareAgent func(holons.Holon) error) (holons.Holon, error) {
	if err := in.ValidateStartup(); err != nil {
		return holons.Holon{}, err
	}
	if in.StartupMode == "terminal" {
		h, err := s.Service.Create(ctx, in)
		if err != nil {
			return h, err
		}
		started, startupErr := s.AddManualTerminal(ctx, h.ID, "", "", "")
		if startupErr == nil {
			// The shell is running; finish bookkeeping even if the request was cancelled.
			startupErr = s.SelectTab(context.WithoutCancel(ctx), h.ID, started.ManualTerminals[0].ID)
		}
		h, err = s.Service.FinalizeTerminalStartup(context.WithoutCancel(ctx), h.ID, startupErr)
		if startupErr != nil || err != nil {
			return h, errors.Join(startupErr, err)
		}
		if options.GenerateIdentity && strings.TrimSpace(h.Prompt) != "" && s.naming != nil {
			s.naming.GenerateIdentity(agentsessions.Session{HolonID: h.ID, Title: h.Title, Prompt: h.Prompt, Worktree: h.WorktreePath}, options)
		}
		return h, nil
	}

	if err := s.PreflightSelection(ctx, &in); err != nil {
		return holons.Holon{}, err
	}
	h, err := s.Service.Create(ctx, in)
	if err != nil || len(h.AgentSessions) == 0 {
		return h, err
	}
	if prepareAgent != nil {
		if err := prepareAgent(h); err != nil {
			_, statusErr := s.Service.SetAgentSessionStatus(context.WithoutCancel(ctx), h.ID, h.AgentSessions[0].ID, holons.StatusFailed, err.Error(), "")
			return h, errors.Join(err, statusErr)
		}
	}
	return s.launchAgent(ctx, h, h.AgentSessions[0].ID, options)
}
func (s *terminalHolonService) AddAgentSession(ctx context.Context, id, agentType, prompt string) (holons.Holon, error) {
	in := holons.Create{AgentType: agentType}
	if err := s.PreflightSelection(ctx, &in); err != nil {
		return holons.Holon{}, err
	}
	return s.addAgentSessionWithSelection(ctx, id, in.AgentType, prompt, in.Model, in.Permissions)
}

func (s *terminalHolonService) addAgentSessionWithSelection(ctx context.Context, id, agentType, prompt, model string, permissions ...string) (holons.Holon, error) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	selection := holons.AgentSelection{Model: model}
	if len(permissions) > 0 {
		selection.Permissions = permissions[0]
	}
	h, _, err := s.Service.ReserveAgentSessionWithSelection(ctx, id, agentType, prompt, "", selection)
	if err != nil {
		return h, err
	}
	return s.launchAgent(ctx, h, h.AgentSessions[len(h.AgentSessions)-1].ID, agentsessions.LaunchOptions{})
}
func (s *terminalHolonService) AddAgentSessionOnce(ctx context.Context, id, agentType, prompt, requestID string) (holons.AgentSession, error) {
	agentID := holons.AgentRequestID(id, requestID)
	// Hold ownership through both reservation and launch. A queued record alone
	// cannot distinguish an interrupted request from a launch still in progress.
	for {
		if err := ctx.Err(); err != nil {
			return holons.AgentSession{}, err
		}
		done := make(chan struct{})
		owner, loaded := s.agentRequests.LoadOrStore(agentID, done)
		if !loaded {
			defer func() {
				s.agentRequests.Delete(agentID)
				close(done)
			}()
			break
		}
		select {
		case <-owner.(chan struct{}):
		case <-ctx.Done():
			return holons.AgentSession{}, ctx.Err()
		}
	}

	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()

	h, err := s.Get(ctx, id)
	if err != nil {
		return holons.AgentSession{}, err
	}
	// An accepted request stays recoverable even if capabilities subsequently change.
	if existing := h.AgentSession(agentID); existing.ID != "" {
		if existing.AgentType != agentType || existing.Prompt != strings.TrimSpace(prompt) {
			return holons.AgentSession{}, holons.ErrInvalid
		}
	} else {
		in := holons.Create{AgentType: agentType}
		if err := s.PreflightSelection(ctx, &in); err != nil {
			return holons.AgentSession{}, err
		}
		h, _, err = s.Service.ReserveAgentSessionWithSelection(ctx, id, in.AgentType, prompt, requestID, holons.AgentSelection{Model: in.Model, Permissions: in.Permissions})
		if err != nil {
			return holons.AgentSession{}, err
		}
	}
	agent := h.AgentSession(agentID)
	if agent.Status == string(holons.StatusQueued) && agent.StartedAt == nil && agent.TerminalID == "" &&
		agent.ResumeTarget == "" && agent.RolloutPath == "" && agent.ClosedAt == nil && !h.ReadOnly && h.ArchivedAt == nil {
		h, err = s.launchAgent(ctx, h, agentID, agentsessions.LaunchOptions{})
	}
	return h.AgentSession(agentID), err
}

func (s *terminalHolonService) ResumeAgentSession(ctx context.Context, id, aid, target string) (holons.Holon, error) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	if h, err := s.Get(ctx, id); err == nil {
		for _, a := range h.AgentSessions {
			if a.ID == aid && a.Status == string(holons.StatusRecoveryFailed) {
				return s.restoreAgentLocked(ctx, id, aid, true)
			}
		}
	}

	if s.harnesses != nil {
		current, err := s.Get(ctx, id)
		if err != nil {
			return holons.Holon{}, err
		}
		for _, agent := range current.AgentSessions {
			if agent.ID == aid {
				if err = s.harnesses.Validate(ctx, protocol.HarnessType(agent.AgentType), workflowForKind(current.Kind)); err != nil {
					return holons.Holon{}, err
				}
				break
			}
		}
	}
	current, err := s.Get(ctx, id)
	if err != nil {
		return current, err
	}
	if err = s.stopRetainedAgent(ctx, current, current.AgentSession(aid)); err != nil {
		return current, err
	}
	h, err := s.Service.ResumeAgentSession(ctx, id, aid, target)
	if err != nil {
		logResumePreparationError(ctx, err, id, "agent_session_id", aid)
		return h, err
	}
	h, err = s.launchAgent(ctx, h, aid, agentsessions.LaunchOptions{RequireResume: true})
	if err != nil {
		slog.ErrorContext(ctx, "Resume agent failed", "holon_id", id, "agent_session_id", aid, "error", err)
	}
	return h, err
}

func logResumePreparationError(ctx context.Context, err error, id string, attrs ...any) {
	if errors.Is(err, holons.ErrNotFound) || errors.Is(err, holons.ErrNotResumable) || errors.Is(err, holons.ErrInvalid) {
		return
	}
	slog.ErrorContext(ctx, "Prepare resume failed", append([]any{"holon_id", id, "error", err}, attrs...)...)
}

func workflowForKind(kind holons.Kind) agentsettings.Workflow {
	switch kind {
	case holons.KindIssue:
		return agentsettings.WorkflowIssue
	case holons.KindPullReview:
		return agentsettings.WorkflowPullRequestReview
	case holons.KindPullWorker:
		return agentsettings.WorkflowPullRequestFeedback
	case holons.KindPRMetadata:
		return agentsettings.WorkflowPullRequestMetadata
	case holons.KindRebase:
		return agentsettings.WorkflowPullRequestRebase
	default:
		return agentsettings.WorkflowDefault
	}
}

func (s *terminalHolonService) Reopen(ctx context.Context, id string) (holons.Holon, error) {
	return s.resumeHolon(ctx, id, true)
}

func (s *terminalHolonService) Resume(ctx context.Context, id string) (holons.Holon, error) {
	return s.resumeHolon(ctx, id, false)
}

func (s *terminalHolonService) resumeHolon(ctx context.Context, id string, reopen bool) (holons.Holon, error) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	if h, err := s.Get(ctx, id); !reopen && err == nil {
		recovering := false
		var recoveryErr error
		for _, a := range h.AgentSessions {
			if a.ClosedAt == nil && a.Status == string(holons.StatusRecoveryFailed) {
				recovering = true
				_, err = s.restoreAgentLocked(ctx, id, a.ID, true)
				recoveryErr = errors.Join(recoveryErr, err)
			}
		}
		if recovering {
			latest, err := s.Get(ctx, id)
			return latest, errors.Join(recoveryErr, err)
		}
	}

	if s.harnesses != nil && !reopen {
		current, err := s.Get(ctx, id)
		if err != nil {
			return holons.Holon{}, err
		}
		for _, agent := range current.AgentSessions {
			if agent.ClosedAt == nil {
				if err = s.harnesses.Validate(ctx, protocol.HarnessType(agent.AgentType), workflowForKind(current.Kind)); err != nil {
					return holons.Holon{}, err
				}
			}
		}
	}
	current, err := s.Get(ctx, id)
	if err != nil {
		return current, err
	}
	for _, agent := range current.AgentSessions {
		if agent.ClosedAt == nil {
			if err = s.stopRetainedAgent(ctx, current, agent); err != nil {
				return current, err
			}
		}
	}
	var h holons.Holon
	if reopen {
		h, err = s.Service.Reopen(ctx, id)
	} else {
		h, err = s.Service.Resume(ctx, id)
	}
	if err != nil {
		logResumePreparationError(ctx, err, id)
		return h, err
	}
	var resumeErr error
	for _, agent := range h.AgentSessions {
		if agent.ClosedAt == nil && holons.Status(agent.Status) == holons.StatusQueued {
			_, launchErr := s.launchAgent(ctx, h, agent.ID, agentsessions.LaunchOptions{RequireResume: true})
			if launchErr != nil {
				slog.ErrorContext(ctx, "Resume agent failed", "holon_id", id, "agent_session_id", agent.ID, "error", launchErr)
				if reopen {
					_, statusErr := s.Service.SetAgentSessionStatus(context.WithoutCancel(ctx), id, agent.ID, holons.StatusRecoveryFailed, "Could not restore conversation: "+launchErr.Error(), agent.ResumeTarget)
					resumeErr = errors.Join(resumeErr, statusErr)
					continue
				}
			}
			resumeErr = errors.Join(resumeErr, launchErr)
		}
	}
	latest, getErr := s.Get(ctx, id)
	if getErr != nil {
		slog.ErrorContext(ctx, "Read holon during resume failed", "holon_id", id, "error", getErr)
	}
	resumeErr = errors.Join(resumeErr, getErr)
	if getErr == nil && s.ides != nil {
		for _, editor := range latest.IDEs {
			if editor.DesiredOpen && editor.State == string(ide.Suspended) {
				_, editorErr := s.ides.Resume(ctx, id, editor.ID)
				if editorErr != nil {
					slog.ErrorContext(ctx, "Resume IDE failed", "holon_id", id, "ide_id", editor.ID, "error", editorErr)
				}
			}
		}
	}
	latest, getErr = s.Get(ctx, id)
	if getErr != nil {
		slog.ErrorContext(ctx, "Read holon after resume failed", "holon_id", id, "error", getErr)
	}
	return latest, errors.Join(resumeErr, getErr)
}
func (s *terminalHolonService) CancelAgentSession(ctx context.Context, id, aid string) (holons.Holon, error) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	h, err := s.Service.CancelAgentSession(ctx, id, aid)
	if err != nil {
		return h, err
	}
	if s.terminals != nil {
		err = errors.Join(s.stopAgentExecution(ctx, id, aid), s.terminals.CloseHarness(ctx, id, aid, "cancelled"))
	} else if s.agents != nil {
		err = s.agents.Cancel(ctx, id, aid)
	}
	if err == nil {
		current, getErr := s.Get(ctx, id)
		if getErr != nil {
			err = getErr
		} else {
			for _, agent := range current.AgentSessions {
				if agent.ID == aid && agent.TerminalID == "" && holons.Status(agent.Status) == holons.StatusCancelling {
					_, err = s.SetAgentSessionStatus(ctx, id, aid, holons.StatusCancelled, "", agent.ResumeTarget)
					break
				}
			}
		}
	}
	latest, getErr := s.Get(ctx, id)
	if err == nil && getErr == nil && s.addressCompletion != nil {
		err = s.addressCompletion.AgentStopped(ctx, latest, aid)
	}
	return latest, errors.Join(err, getErr)
}

func (s *terminalHolonService) CloseAgentSession(ctx context.Context, id, aid string) (holons.Holon, error) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	return s.closeAgentSession(ctx, id, aid)
}

// closeAgentSession requires recoveryMu to be held by the caller.
func (s *terminalHolonService) closeAgentSession(ctx context.Context, id, aid string) (holons.Holon, error) {
	h, err := s.Get(ctx, id)
	if err != nil {
		return holons.Holon{}, err
	}
	var target *holons.AgentSession
	for index := range h.AgentSessions {
		if h.AgentSessions[index].ID == aid {
			target = &h.AgentSessions[index]
			break
		}
	}
	if target == nil {
		return holons.Holon{}, holons.ErrAgentSessionNotFound
	}
	if target.ClosedAt != nil {
		closed, closeErr := s.Service.CloseAgentSession(ctx, id, aid)
		if closeErr == nil && s.addressCompletion != nil {
			closeErr = s.addressCompletion.AgentStopped(ctx, closed, aid)
		}
		return closed, closeErr
	}
	// Verified Rebase cleanup stops the runtime without turning its successful
	// result into a cancellation. Keep its runtime status until shutdown finishes.
	if !h.RebaseAttempt.ClosurePending(aid) && !holons.IsTerminal(holons.Status(target.Status)) && holons.Status(target.Status) != holons.StatusCancelling {
		if _, err = s.Service.CancelAgentSession(ctx, id, aid); err != nil {
			return holons.Holon{}, err
		}
	}
	if s.terminals != nil {
		err = errors.Join(s.stopAgentExecution(ctx, id, aid), s.terminals.CloseHarness(ctx, id, aid, "closed"))
	} else if s.agents != nil && (!holons.IsTerminal(holons.Status(target.Status)) || h.RebaseAttempt.ClosurePending(aid)) {
		err = s.agents.Cancel(ctx, id, aid)
	}
	if err != nil {
		latest, getErr := s.Get(ctx, id)
		return latest, errors.Join(err, getErr)
	}
	// Runtime shutdown has finished; the verified result can now become the
	// agent outcome. The pending marker holds archival until the tab closes.
	if h.RebaseAttempt.ClosurePending(aid) {
		if _, err = s.Service.SetAgentSessionStatus(ctx, id, aid, holons.StatusCompleted, "", target.ResumeTarget); err != nil {
			return holons.Holon{}, err
		}
	}
	closed, closeErr := s.Service.CloseAgentSession(ctx, id, aid)
	if closeErr == nil && s.addressCompletion != nil {
		closeErr = s.addressCompletion.AgentStopped(ctx, closed, aid)
	}
	return closed, closeErr
}

func (s *terminalHolonService) Publish(ctx context.Context, id string, in holons.Publish) (holons.Holon, error) {
	if s.rebaseAgents != nil {
		operation, found, err := s.actions.GetOperation(ctx, pullrequestlifecycle.RequestID(ctx))
		if err != nil {
			return holons.Holon{}, err
		}
		if found && operation.Status == "succeeded" && operation.HolonID == id && (operation.Kind == "publish" || operation.Kind == "work_publish") {
			return s.Get(ctx, id)
		}
	}
	if s.addressCompletion != nil && s.work != nil {
		work, err := s.work.WorkForSession(ctx, id)
		if err != nil && !errors.Is(err, pullrequestwork.ErrNotFound) {
			return holons.Holon{}, err
		}
		if err == nil && work.IsAddress() {
			if work.PendingCompletion == nil {
				return holons.Holon{}, holons.ErrPublicationUnavailable
			}
			if !work.Active() || work.Status == pullrequestwork.StatusCancelling {
				return holons.Holon{}, pullrequestwork.ErrInvalid
			}
			if err = s.addressCompletion.Retry(ctx, id); err != nil {
				return holons.Holon{}, err
			}
			return s.Get(ctx, id)
		}
	}

	if workService, ok := s.work.(localContinueWorkService); ok {
		work, err := workService.WorkForSession(ctx, id)
		if err != nil && !errors.Is(err, pullrequestwork.ErrNotFound) {
			return holons.Holon{}, err
		}
		if err == nil && work.Mode == pullrequestwork.ModeContinue {
			if _, err = workService.PublishContinueSession(ctx, id, pullrequestwork.PublicationOptions{RequestID: pullrequestlifecycle.RequestID(ctx), TargetCommit: in.TargetCommit}); err != nil {
				if errors.Is(err, pullrequestwork.ErrPullRequestInactive) {
					return holons.Holon{}, pullrequestlifecycle.ErrPublicationInactive
				}
				return holons.Holon{}, err
			}
			return s.Get(ctx, id)
		}
	}
	if s.publication != nil {
		h, err := s.Get(ctx, id)
		if err != nil {
			return holons.Holon{}, err
		}
		if h.Kind == holons.KindNormal || h.Kind == holons.KindIssue {
			return s.publication.Publish(ctx, id, in.TargetCommit)
		}
	}

	if s.rebaseAgents != nil {
		h, err := s.Get(ctx, id)
		if err != nil {
			return holons.Holon{}, err
		}
		checkpoint := h.SynchronizedTargetCommit
		if checkpoint == "" {
			checkpoint = h.UpstreamHeadCommit
		}
		if checkpoint == "" {
			if pr, ok := s.rebaseAgents.target(h); ok {
				checkpoint = pr.HeadCommit
			}
		}
		if in.TargetCommit != "" {
			checkpoint = in.TargetCommit
		}
		r, err := s.PreparePublication(ctx, id)
		if err != nil {
			return holons.Holon{}, err
		}
		if r.TargetCommit != "" && checkpoint != r.TargetCommit && r.WorkspaceHead != r.TargetCommit {
			return holons.Holon{}, pullrequestwork.ErrStaleHead
		}
		if r.Reason != "" || r.RebaseRequired {
			if r.Reason == pullrequestlifecycle.ErrPublicationInactive.Error() {
				return holons.Holon{}, pullrequestlifecycle.ErrPublicationInactive
			}
			inspection, inspectErr := s.InspectWorkspace(ctx, id)
			if inspectErr == nil && inspection.Dirty {
				return holons.Holon{}, pullrequestlifecycle.ErrWorkspaceDirty
			}
			return holons.Holon{}, fmt.Errorf("%w: %s", holons.ErrPublicationUnavailable, r.Reason)
		}
		if !r.PublicationAvailable {
			return holons.Holon{}, pullrequestlifecycle.ErrPublicationNoChanges
		}
	}
	return s.Service.Publish(ctx, id, in)
}

func (s *terminalHolonService) End(ctx context.Context, id string) (holons.Holon, error) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	unlockLaunch := s.lockQueuedLaunch(id)
	defer unlockLaunch()
	h, err := s.Service.End(ctx, id)
	if err != nil {
		return h, err
	}
	if h.ArchivedAt != nil {
		return h, nil
	}
	var shutdownErr error
	if s.ides != nil {
		for _, editor := range h.IDEs {
			if editor.DesiredOpen {
				_, closeErr := s.ides.Close(ctx, id, editor.ID)
				shutdownErr = errors.Join(shutdownErr, closeErr)
			}
		}
	}
	if s.terminals != nil {
		for _, agent := range h.AgentSessions {
			shutdownErr = errors.Join(shutdownErr, s.stopAgentExecution(ctx, id, agent.ID))
		}
		shutdownErr = errors.Join(shutdownErr, s.terminals.CloseSession(ctx, id, "cancelled"))
	} else if s.agents != nil {
		for _, agent := range h.AgentSessions {
			if agent.ClosedAt == nil && !holons.IsTerminal(holons.Status(agent.Status)) {
				shutdownErr = errors.Join(shutdownErr, s.agents.Cancel(ctx, id, agent.ID))
			}
		}
	}
	for _, agent := range h.AgentSessions {
		if agent.ClosedAt == nil && agent.TerminalID == "" && holons.Status(agent.Status) == holons.StatusCancelling {
			_, statusErr := s.SetAgentSessionStatus(ctx, id, agent.ID, holons.StatusCancelled, "", agent.ResumeTarget)
			shutdownErr = errors.Join(shutdownErr, statusErr)
		}
	}
	for _, terminal := range h.ManualTerminals {
		if terminal.ClosedAt == nil && terminal.TerminalID == "" {
			_, closeErr := s.Service.CloseManualTerminal(ctx, id, terminal.ID)
			shutdownErr = errors.Join(shutdownErr, closeErr)
		}
	}
	finalized, finalizeErr := s.Service.FinalizeCancellation(ctx, id)
	if finalizeErr == nil {
		if workService, ok := s.work.(localContinueWorkService); ok {
			if work, workErr := workService.WorkForSession(ctx, id); workErr == nil && work.Mode == pullrequestwork.ModeContinue {
				shutdownErr = errors.Join(shutdownErr, workService.Fail(ctx, work.ID, "Continue Holon was cancelled."))
			}
		}
	}
	return finalized, errors.Join(shutdownErr, finalizeErr)
}

func (s *terminalHolonService) AddManualTerminal(ctx context.Context, id, title, cwd, ignored string) (holons.Holon, error) {
	h, e := s.Get(ctx, id)
	if e != nil {
		return holons.Holon{}, e
	}
	if cwd == "" {
		cwd = h.WorktreePath
	}
	tid, e := terminals.NewID()
	if e != nil {
		return holons.Holon{}, e
	}
	h, e = s.Service.AddManualTerminal(ctx, id, title, cwd, string(tid))
	if e != nil {
		return holons.Holon{}, e
	}
	var record holons.ManualTerminal
	for _, t := range h.ManualTerminals {
		if t.TerminalID == string(tid) {
			record = t
			break
		}
	}
	_, e = s.terminals.LaunchManual(ctx, id, record.ID, terminals.Dimensions{Columns: 80, Rows: 24})
	if e != nil {
		return holons.Holon{}, e
	}
	// Request cancellation must not turn a successful shell launch into a failure.
	return s.Get(context.WithoutCancel(ctx), id)
}

func (s *terminalHolonService) RelaunchManualTerminal(ctx context.Context, id, recordID string) (holons.ManualTerminal, error) {
	h, err := s.Get(ctx, id)
	if err != nil {
		return holons.ManualTerminal{}, err
	}
	var record holons.ManualTerminal
	for _, candidate := range h.ManualTerminals {
		if candidate.ID == recordID && candidate.ClosedAt == nil {
			record = candidate
			break
		}
	}
	if record.ID == "" {
		return holons.ManualTerminal{}, holons.ErrNotFound
	}
	if record.TerminalID != "" {
		return record, nil
	}
	tid, err := terminals.NewID()
	if err != nil {
		return holons.ManualTerminal{}, err
	}
	if _, err = s.Service.SetManualTerminalBinding(ctx, id, recordID, string(tid)); err != nil {
		return holons.ManualTerminal{}, err
	}
	if _, err = s.terminals.LaunchManual(ctx, id, recordID, terminals.Dimensions{Columns: 80, Rows: 24}); err != nil {
		_, _ = s.Service.SetManualTerminalBinding(ctx, id, recordID, "")
		return holons.ManualTerminal{}, err
	}
	h, err = s.Get(ctx, id)
	if err != nil {
		return holons.ManualTerminal{}, err
	}
	for _, candidate := range h.ManualTerminals {
		if candidate.ID == recordID {
			return candidate, nil
		}
	}
	return holons.ManualTerminal{}, holons.ErrNotFound
}
func (s *terminalHolonService) CloseManualTerminal(ctx context.Context, id, record string) (holons.Holon, error) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	h, e := s.Get(ctx, id)
	if e != nil {
		return holons.Holon{}, e
	}
	for _, t := range h.ManualTerminals {
		if t.ID == record && t.ClosedAt == nil && t.TerminalID != "" {
			if err := s.terminals.CloseTerminal(ctx, terminals.TerminalID(t.TerminalID)); err != nil {
				if errors.Is(err, terminals.ErrNotFound) {
					// Completion can remove the binding between Get and Resolve.
					// Only the requested tab's durable closure confirms success.
					latest, getErr := s.Get(ctx, id)
					if getErr != nil {
						return holons.Holon{}, getErr
					}
					for _, current := range latest.ManualTerminals {
						if current.ID == record && current.ClosedAt != nil {
							return s.Service.CloseManualTerminal(ctx, id, record)
						}
					}
				}
				return holons.Holon{}, err
			}
			return s.Get(ctx, id)
		}
	}
	return s.Service.CloseManualTerminal(ctx, id, record)
}

func (s *terminalHolonService) CreateCommitAgent(ctx context.Context, id, sourceID string) (holons.Holon, error) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	return s.commitAgents.CreateCommitAgent(ctx, id, sourceID)
}

func (s *terminalHolonService) ForkAgentSession(ctx context.Context, id, sourceID string) (holons.AgentSession, error) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	return s.forkAgents.ForkAgentSession(ctx, id, sourceID)
}

func (s *terminalHolonService) stopAgentExecution(ctx context.Context, id, aid string) error {
	if stopper, ok := s.agents.(interface {
		StopExecution(context.Context, string, string) error
	}); ok {
		return stopper.StopExecution(ctx, id, aid)
	}
	return nil
}

// stopRetainedAgent checks the old binding before resume preparation clears it.
// A terminal durable status alone does not prove that runtime shutdown succeeded.
func (s *terminalHolonService) stopRetainedAgent(ctx context.Context, h holons.Holon, agent holons.AgentSession) error {
	if agent.ID == "" {
		return nil // Domain preparation reports the missing agent.
	}
	if h.ReadOnly || h.BaseCommit == "" || h.WorktreeBranch == "" || h.WorktreePath == "" ||
		agent.ClosedAt != nil || !holons.IsTerminal(holons.Status(agent.Status)) {
		return holons.ErrNotResumable
	}
	if agent.TerminalID == "" {
		return nil
	}
	if s.terminals != nil {
		return errors.Join(s.stopAgentExecution(ctx, h.ID, agent.ID), s.terminals.CloseHarness(ctx, h.ID, agent.ID, "resume"))
	}
	if s.agents != nil {
		return s.agents.Cancel(ctx, h.ID, agent.ID)
	}
	return nil
}

func (s *terminalHolonService) prepareAndStartReserved(ctx context.Context, id string, in holons.Create, options agentsessions.LaunchOptions, prepareAgent func(holons.Holon) error) (holons.Holon, error) {
	if s.harnesses != nil {
		if err := s.harnesses.Validate(ctx, protocol.HarnessType(in.AgentType), workflowForKind(in.Kind)); err != nil {
			return holons.Holon{}, err
		}
	}
	h, err := s.Service.PrepareReserved(ctx, id, in)
	if err != nil {
		return h, err
	}
	if prepareAgent != nil {
		if err = prepareAgent(h); err != nil {
			return h, err
		}
	}
	// Serialize with End for this holon. Do not take recoveryMu while dispatch
	// holds its work lock: End for an unrelated Continue holon can call back
	// into the work service while holding recoveryMu.
	unlockLaunch := s.lockQueuedLaunch(id)
	defer unlockLaunch()
	// Recheck End intent after preparation, before any terminal can be created.
	if err = ctx.Err(); err != nil {
		return h, err
	}
	h, err = s.Get(ctx, id)
	if err != nil {
		return h, errors.Join(holons.ErrSetupPersistence, err)
	}
	if h.EndRequested || holons.IsTerminal(h.Status) || h.Status == holons.StatusCancelling {
		return h, holons.ErrReservationEnded
	}
	if s.agents != nil {
		if err = s.agents.Launch(ctx, id, h.AgentSessions[0].ID, terminals.Dimensions{Columns: 120, Rows: 36}, "", options); err != nil {
			return h, err
		}
	}
	s.pinHolonPullRequests(ctx, h)
	return h, nil
}

func (s *terminalHolonService) lockQueuedLaunch(id string) func() {
	value, _ := s.queuedLaunchLocks.LoadOrStore(id, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}
