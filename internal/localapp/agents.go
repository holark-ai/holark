package localapp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"syscall"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/prompt_templates"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/sessions/branchnaming"
	"github.com/holark-ai/holark/internal/terminalenv"
	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
	"github.com/holark-ai/holark/internal/workspace"
)

type localCommitWorkReader interface {
	WorkForSession(context.Context, string) (pullrequestwork.Work, error)
}

type localContinueWorkService interface {
	localCommitWorkReader
	PublishContinueSession(context.Context, string, pullrequestwork.PublicationOptions) (pullrequestwork.Work, error)
	Fail(context.Context, string, string) error
}

type localAgentRuntime interface {
	Cancel(context.Context, string, string) error
	Submit(context.Context, string, string, string) error
}

type metadataArtifactImporter interface {
	ImportArtifact(context.Context, string) error
}

type localPullRequestWorkCompleter interface {
	Complete(context.Context, holons.Holon) (pullRequestWorkCompletionDisposition, error)
}

type agentSessionCloser interface {
	CloseAgentSession(context.Context, string, string) (holons.Holon, error)
}

type localAgentState struct {
	rebaseAgents      *rebaseAgentCoordinator
	commitCloser      agentSessionCloser
	holons            *holons.Service
	templates         prompttemplates.Reader
	work              localCommitWorkReader
	agents            localAgentRuntime
	metadata          metadataArtifactImporter
	workCompletion    localPullRequestWorkCompleter
	addressCompletion *addressCompletionCoordinator
}

func (s localAgentState) Session(ctx context.Context, hid, aid string) (agentsessions.Session, error) {
	h, err := s.holons.Get(ctx, hid)
	if err != nil {
		return agentsessions.Session{}, err
	}
	for index, a := range h.AgentSessions {
		if a.ID == aid {
			return agentsessions.Session{Model: a.Model, Permissions: a.Permissions, Restoring: a.Status == string(holons.StatusRestoring), HolonID: hid, ID: aid, TerminalID: a.TerminalID, Worktree: h.WorktreePath, AgentType: a.AgentType, Title: h.Title, Prompt: a.Prompt, ResumeTarget: a.ResumeTarget, RolloutPath: a.RolloutPath, InputState: protocol.InputState(a.InputState), Activity: protocol.AgentActivity(a.Activity), Initial: index == 0}, nil
		}
	}
	return agentsessions.Session{}, holons.ErrNotFound
}
func (s localAgentState) ApplyGeneratedIdentity(ctx context.Context, hid, expected, title, slug string) error {
	_, err := s.holons.ApplyGeneratedIdentity(ctx, hid, expected, title, slug)
	return err
}
func (s localAgentState) Bind(ctx context.Context, hid, aid, tid string) error {
	_, e := s.holons.UpdateAgentObservation(ctx, hid, aid, tid, "", "", "", "", "", protocol.ActivityStarting, nil)
	return e
}
func (s localAgentState) Running(ctx context.Context, hid, aid string) error {
	session, err := s.Session(ctx, hid, aid)
	if err != nil {
		return err
	}
	if session.Restoring {
		return nil
	}
	_, e := s.holons.SetAgentSessionStatus(ctx, hid, aid, holons.StatusRunning, "", session.ResumeTarget)
	return e
}
func (s localAgentState) Failed(ctx context.Context, hid, aid, reason string) error {
	session, err := s.Session(ctx, hid, aid)
	if err != nil {
		return err
	}
	status := holons.StatusFailed
	h, err := s.holons.Get(ctx, hid)
	if err != nil {
		return err
	}
	for _, a := range h.AgentSessions {
		if a.ID == aid && (a.Status == string(holons.StatusRestoring) || a.Status == string(holons.StatusRecoveryFailed)) {
			status = holons.StatusRecoveryFailed
		}
	}
	updated, e := s.holons.SetAgentSessionStatus(ctx, hid, aid, status, reason, session.ResumeTarget)
	// Synchronous fork failures are settled by the launching coordinator after
	// Fork returns. Reentering it here would deadlock its completion lock.
	launchingRebase := ctx.Value(rebaseLaunchContextKey{}) == true && updated.RebaseAttempt != nil && updated.RebaseAttempt.AgentID == aid
	if e == nil && status != holons.StatusRecoveryFailed && s.addressCompletion != nil && !launchingRebase {
		e = s.addressCompletion.AgentStopped(ctx, updated, aid)
	}
	return e
}
func (s localAgentState) Observed(ctx context.Context, hid, aid string, event harness.Event) error {
	// Register ownership before the completed observation becomes visible.
	if event.InputState == protocol.InputTaskComplete {
		h, err := s.holons.Get(ctx, hid)
		if err != nil {
			return err
		}
		finite := h.Kind == holons.KindPRMetadata || h.Kind == holons.KindPullReview || h.Kind == holons.KindRebase
		if h.Kind == holons.KindPullWorker && s.work != nil {
			w, err := s.work.WorkForSession(ctx, hid)
			finite = err == nil && w.Active() && w.Mode != pullrequestwork.ModeContinue
		}
		if finite {
			defer s.holons.BeginFinalization(hid)()
		}
	}
	updated, e := s.holons.UpdateAgentObservation(ctx, hid, aid, "", event.ResumeTarget, event.RolloutPath, string(event.InputState), string(event.ObservabilityStatus), event.ObservabilityMessage, event.Activity, event.ContextTokens, event.TerminalID)
	if e != nil {
		return e
	}
	if event.TerminalID != "" {
		current := false
		for _, a := range updated.AgentSessions {
			if a.ID == aid && a.TerminalID == event.TerminalID && a.ClosedAt == nil && !holons.IsTerminal(holons.Status(a.Status)) {
				current = true
			}
		}
		if !current {
			return nil
		}
	}
	if updated.RebaseAttempt != nil && updated.RebaseAttempt.AgentID == aid {
		if updated.Status == holons.StatusCancelling || updated.Status == holons.StatusCancelled {
			return nil
		}
		if updated.AgentSession(aid).Status == string(holons.StatusCancelling) {
			return nil
		}
		if event.InputState != protocol.InputTaskComplete {
			return nil
		}
		if s.rebaseAgents != nil {
			return s.rebaseAgents.complete(ctx, hid, aid, s.addressCompletion)
		}
		return nil
	}
	pendingCompletion := false
	if s.work != nil && updated.Kind == holons.KindPullWorker {
		if w, err := s.work.WorkForSession(ctx, hid); err == nil {
			pendingCompletion = w.PendingCompletion != nil
		}
	}
	// Only eligible commit forks may finish while workflow publication is pending.
	// Their eligibility is checked below before any completion handling.
	if pendingCompletion && (event.InputState != protocol.InputTaskComplete || updated.AgentSession(aid).CommitStartHead == "") {
		return nil
	}
	if event.InputState != protocol.InputTaskComplete {
		// Codex recovery refreshes completed activity without an input state.
		// Preserve paused rebases and reviews until a new input state arrives.
		if (updated.Kind == holons.KindRebase || updated.Kind == holons.KindPullReview) && event.InputState == "" && event.Activity == protocol.ActivityCompleted && updated.AgentSession(aid).InputState == string(protocol.InputUserRequired) {
			_, e = s.holons.UpdateAgentObservation(ctx, hid, aid, "", "", "", "", "", "", protocol.ActivityNeedsInput, nil, event.TerminalID)
			return e
		}
		if event.Type == harness.EventInputStateChanged {
			_, _, e = s.holons.TransitionAgentCommitPrompt(ctx, hid, aid, holons.CommitPromptRearm, "")
			return e
		}
		return nil
	}
	h, err := s.holons.Get(ctx, hid)
	if err != nil {
		return err
	}
	if h.AgentSession(aid).CommitStartHead != "" {
		eligible, err := autoCommitEligible(ctx, h, s.work)
		if err != nil {
			return errors.Join(err, s.waitForCommitInput(ctx, h, aid))
		}
		if eligible {
			return s.completeCommitAgent(ctx, h, aid)
		}
	}
	if pendingCompletion {
		// Address owns its saved source until publication finishes. An older
		// fork's starting HEAD must not close it or trigger publication again.
		return nil
	}
	if h.Kind == holons.KindPRMetadata {
		return s.completeMetadata(ctx, h, aid)
	}
	if h.Kind == holons.KindPullReview {
		_, _, promptErr := s.holons.TransitionAgentCommitPrompt(ctx, hid, aid, holons.CommitPromptClean, "")
		return errors.Join(promptErr, s.completePullRequestWork(ctx, h, aid))
	}
	if h.Kind == holons.KindRebase {
		// A finished turn can be a request for guidance. Only the marker declares
		// that the agent is ready for commit handling and publication.
		if _, markerErr := readPullRequestWorkArtifact(h, pullrequestwork.KindRebase); markerErr != nil {
			if errors.Is(markerErr, errPullRequestWorkArtifactMissing) {
				_, err := s.holons.UpdateAgentObservation(ctx, hid, aid, "", "", "", string(protocol.InputUserRequired), "", "", protocol.ActivityNeedsInput, nil, event.TerminalID)
				return err
			}
			return s.completePullRequestWork(ctx, h, aid)
		}
	}
	inspectOptions := holons.InspectOptions{}
	switch h.Kind {
	case holons.KindPullWorker:
		inspectOptions.IgnoredPaths = []string{pullRequestWorkArtifactPath(pullrequestwork.KindWorker)}
	case holons.KindRebase:
		inspectOptions.IgnoredPaths = []string{pullRequestWorkArtifactPath(pullrequestwork.KindRebase)}
	}
	inspection, err := s.holons.InspectWorkspaceWithOptions(ctx, hid, inspectOptions)
	if err != nil {
		return err
	}
	if !inspection.Dirty {
		_, _, promptErr := s.holons.TransitionAgentCommitPrompt(ctx, hid, aid, holons.CommitPromptClean, "")
		if h.Kind == holons.KindRebase {
			return errors.Join(promptErr, s.completePullRequestWork(ctx, h, aid))
		}
		if h.Kind == holons.KindPullWorker {
			work, workErr := s.pullRequestWork(ctx, hid)
			if workErr != nil || work.Mode != pullrequestwork.ModeContinue {
				return errors.Join(promptErr, workErr, s.completePullRequestWork(ctx, h, aid))
			}
		}
		return promptErr
	}
	payload, _ := json.Marshal(struct {
		Head    string
		Changes []holons.WorkspaceChange
	}{inspection.HeadCommit, inspection.Changes})
	hash := fmt.Sprintf("%x", sha256.Sum256(payload))
	if h.Kind == holons.KindPullWorker || h.Kind == holons.KindRebase {
		work, workErr := s.pullRequestWork(ctx, hid)
		if workErr != nil {
			return s.completePullRequestWork(ctx, h, aid)
		}
		if (work.Kind == pullrequestwork.KindRebase || work.Mode == pullrequestwork.ModeAuto) && s.agents != nil {
			_, eligible, prepareErr := s.holons.TransitionAgentCommitPrompt(ctx, hid, aid, holons.CommitPromptPrepareCompletion, hash)
			if prepareErr != nil || !eligible {
				return prepareErr
			}
			prompt := s.commitFollowUp(ctx, work)
			if prompt != "" {
				if err = s.agents.Submit(ctx, hid, aid, prompt); err != nil {
					return err
				}
				_, _, err = s.holons.TransitionAgentCommitPrompt(ctx, hid, aid, holons.CommitPromptAutoComplete, hash)
				return err
			}
		}
	}
	_, _, err = s.holons.TransitionAgentCommitPrompt(ctx, hid, aid, holons.CommitPromptComplete, hash)
	return err
}

// completeMetadata runs the one-shot artifact lifecycle for the local Holon:
// import the artifact, expire the visible Holon, and stop the otherwise
// interactive agent process after its first completed turn.
func (s *localAgentState) completeMetadata(ctx context.Context, h holons.Holon, agentID string) error {
	defer s.holons.BeginFinalization(h.ID)()
	if s.metadata == nil {
		return errors.New("metadata artifact importer is unavailable")
	}
	importErr := s.metadata.ImportArtifact(ctx, h.ID)
	reason := ""
	if importErr != nil {
		latest, getErr := s.holons.Get(ctx, h.ID)
		if getErr == nil {
			for _, agent := range latest.AgentSessions {
				if agent.ID == agentID {
					reason = agent.Reason
					break
				}
			}
		}
		if reason == "" && !errors.Is(importErr, pullrequestmetadata.ErrApplicationFailed) {
			reason = "Holark could not import the metadata output."
		}
	}
	resumeTarget := h.AgentSession(agentID).ResumeTarget
	_, statusErr := s.holons.SetAgentSessionStatus(ctx, h.ID, agentID, holons.StatusExpired, reason, resumeTarget)
	var cancelErr error
	if s.agents != nil {
		cancelErr = s.agents.Cancel(ctx, h.ID, agentID)
	}
	return errors.Join(importErr, statusErr, cancelErr)
}

func (s *localAgentState) completePullRequestWork(ctx context.Context, h holons.Holon, agentID string) error {
	defer s.holons.BeginFinalization(h.ID)()
	if s.workCompletion == nil {
		return errors.New("pull request work completion coordinator is unavailable")
	}
	var disposition pullRequestWorkCompletionDisposition
	var completionErr error
	if completer, ok := s.workCompletion.(interface {
		CompleteFromAgent(context.Context, holons.Holon, string) (pullRequestWorkCompletionDisposition, error)
	}); ok {
		disposition, completionErr = completer.CompleteFromAgent(ctx, h, agentID)
	} else {
		disposition, completionErr = s.workCompletion.Complete(ctx, h)
	}
	if disposition.AlreadyHandled || !disposition.Terminal || disposition.AgentsFinalized {
		return completionErr
	}
	return errors.Join(completionErr, finalizePullRequestWorkAgent(ctx, s.holons, s.agents, h, agentID, disposition.Reason))
}

func (s *localAgentState) pullRequestWork(ctx context.Context, holonID string) (pullrequestwork.Work, error) {
	if s.work == nil {
		return pullrequestwork.Work{}, pullrequestwork.ErrNotFound
	}
	return s.work.WorkForSession(ctx, holonID)
}

func (s *localAgentState) commitFollowUp(ctx context.Context, work pullrequestwork.Work) string {
	key := prompttemplates.PullRequestRebaseCommitKey
	if work.Kind == pullrequestwork.KindWorker {
		key = prompttemplates.PullRequestWorkerAutoCommitKey
		switch work.Mode {
		case pullrequestwork.ModeAssisted:
			key = prompttemplates.PullRequestWorkerAssistedCommitKey
		case pullrequestwork.ModeContinue:
			key = prompttemplates.CommitFollowUpKey
		}
	}
	definition, _ := prompttemplates.DefinitionByKey(key)
	template := definition.DefaultValue
	if s.templates != nil {
		template = s.templates.Read(ctx, key)
	}
	artifactPath := pullRequestWorkArtifactPath(work.Kind)
	return prompttemplates.Render(template, map[string]string{"artifact_path": artifactPath})
}

type localAgentLauncher struct {
	manager         *terminalhost.Manager
	terminalContext terminalenv.Context
}

func (l localAgentLauncher) Launch(ctx context.Context, spec terminals.LaunchSpec) (int, error) {
	spec.Environment = l.terminalContext.Environment(spec.Environment)
	return l.manager.Launch(ctx, spec)
}

type localSkillInstaller struct{ manager *workspace.Manager }
type localAgentNamer struct {
	harnesses localHarnessPreferences
	namer     *branchnaming.Dispatcher
	templates prompttemplates.Reader
}

func (n localAgentNamer) Name(ctx context.Context, s agentsessions.Session, options agentsessions.LaunchOptions) (agentsessions.Proposal, error) {
	// Terminal Holons have no agent session. Resolve their naming dependency only
	// here, in the background operation, so availability never blocks the shell.
	if s.AgentType == "" {
		if n.harnesses == nil {
			return agentsessions.Proposal{}, agentsessions.ErrUnavailable
		}
		resolved, err := n.harnesses.ResolveDefault(ctx, agentsettings.WorkflowDefault)
		if err != nil {
			return agentsessions.Proposal{}, err
		}
		s.AgentType = string(resolved)
	}

	template := ""
	if n.templates != nil {
		template = n.templates.Read(ctx, prompttemplates.BranchNamingKey)
	}
	p, err := n.namer.NameFor(ctx, protocol.HarnessType(s.AgentType), branchnaming.Input{SessionID: s.HolonID, Project: protocol.Project{ID: "local"}, Title: s.Title, Prompt: s.Prompt, GenerateTitle: options.GenerateTitle, PromptTemplate: template})
	return agentsessions.Proposal{Slug: p.Slug, Title: p.Title}, err
}

func (i localSkillInstaller) Install(ctx context.Context, worktree string, spec harness.SkillInstallSpec) error {
	sources := make([]workspace.SkillSource, 0, len(spec.Sources))
	for _, source := range spec.Sources {
		sources = append(sources, workspace.SkillSource{FS: source.FS, Root: source.Root})
	}
	return i.manager.InstallSkill(ctx, worktree, workspace.SkillInstall{TargetDir: spec.TargetDir, Sources: sources})
}
func (l localAgentLauncher) Signal(ctx context.Context, id terminals.TerminalID) error {
	err := l.manager.Signal(ctx, id, syscall.SIGTERM)
	// Shutdown is idempotent when recovery finds an already absent process.
	if errors.Is(err, terminalhost.ErrNotFound) {
		return nil
	}
	return err
}
func (l localAgentLauncher) Input(ctx context.Context, id terminals.TerminalID, data []byte) error {
	return l.manager.Input(ctx, id, data)
}

func (s localAgentState) completeCommitAgent(ctx context.Context, h holons.Holon, aid string) error {
	inspection, err := s.holons.InspectWorkspaceWithOptions(ctx, h.ID, holons.InspectOptions{SummaryOnly: true})
	if err != nil {
		return errors.Join(err, s.waitForCommitInput(ctx, h, aid))
	}
	if inspection.HeadCommit == "" || inspection.HeadCommit == h.AgentSession(aid).CommitStartHead {
		return s.waitForCommitInput(ctx, h, aid)
	}
	// Other tabs may have made new edits. A changed HEAD completes this fork
	// independently of the worktree's current dirty state.
	_, _, err = s.holons.TransitionAgentCommitPrompt(ctx, h.ID, aid, holons.CommitPromptClose, "")
	if err == nil && s.commitCloser != nil {
		// Closing stops this observation's monitor; finish persistence even
		// after that monitor context is cancelled.
		_, err = s.commitCloser.CloseAgentSession(context.WithoutCancel(ctx), h.ID, aid)
	}
	return err
}

// A completed turn is not proof that the requested commit succeeded.
func (s localAgentState) waitForCommitInput(ctx context.Context, h holons.Holon, aid string) error {
	_, err := s.holons.UpdateAgentObservation(ctx, h.ID, aid, "", "", "", string(protocol.InputUserRequired), "", "", protocol.ActivityNeedsInput, nil, h.AgentSession(aid).TerminalID)
	return err
}

// finalizePullRequestWorkAgent is shared by harness completion and review import.
// Retry pending archival and runtime cancellation when expiry was already persisted.
func finalizePullRequestWorkAgent(ctx context.Context, service *holons.Service, runtime localAgentRuntime, h holons.Holon, agentID, reason string) error {
	agent := h.AgentSession(agentID)
	var statusErr error
	if agent.ClosedAt == nil && agent.Status != string(holons.StatusExpired) {
		_, statusErr = service.SetAgentSessionStatus(ctx, h.ID, agentID, holons.StatusExpired, reason, agent.ResumeTarget)
	} else {
		_, statusErr = service.ArchiveFinite(ctx, h.ID)
	}
	var cancelErr error
	if runtime != nil {
		cancelErr = runtime.Cancel(ctx, h.ID, agentID)
	}
	return errors.Join(statusErr, cancelErr)
}
