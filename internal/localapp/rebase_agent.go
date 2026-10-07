package localapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/prompt_templates"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

// Rebase composition resolves PR ownership and starts or forks a native conversation;
// synchronization itself belongs to holons.Service.
type rebaseLaunchContextKey struct{}

type rebaseAgentCoordinator struct {
	closer interface {
		CloseRebaseAgentSession(context.Context, string, string) (holons.Holon, error)
	}
	reservations interface {
		ReservePublicationTarget(context.Context, holons.Holon, holons.PublicationReadiness) error
	}
	holons          *holons.Service
	agents          commitAgentLauncher
	catalog         pullrequestlifecycle.PublicationCatalog
	sync            pullrequestlifecycle.Synchronizer
	repositoryID    string
	templates       prompttemplates.Reader
	harnesses       localHarnessPreferences
	pinPullRequests func(context.Context, holons.Holon)
}

func (c *rebaseAgentCoordinator) target(h holons.Holon) (pullrequestlifecycle.PullRequest, bool) {
	return pullrequestlifecycle.LinkedPublicationTarget(c.catalog, c.repositoryID, h)
}

// PublicationReadiness inspects the workspace against accepted comparison inputs.
// Polling this reader must not perform remote synchronization.
func (c *rebaseAgentCoordinator) PublicationReadiness(ctx context.Context, id string) (holons.PublicationReadiness, error) {
	return c.publicationReadiness(ctx, id, false, false)
}

// PreparePublication refreshes inputs for a new action. Existing attempts and
// confirmed publication checkpoints retain their pinned execution inputs.
func (c *rebaseAgentCoordinator) PreparePublication(ctx context.Context, id string) (holons.PublicationReadiness, error) {
	return c.publicationReadiness(ctx, id, true, false)
}

func (c *rebaseAgentCoordinator) PrepareManualPublication(ctx context.Context, id string) (holons.PublicationReadiness, error) {
	return c.publicationReadiness(ctx, id, true, true)
}

func (c *rebaseAgentCoordinator) publicationReadiness(ctx context.Context, id string, refresh, manual bool) (holons.PublicationReadiness, error) {
	h, err := c.holons.Get(ctx, id)
	if err != nil {
		return holons.PublicationReadiness{}, err
	}
	unavailable := holons.PublicationReadiness{Attempt: h.RebaseAttempt, Reason: pullrequestlifecycle.ErrPublicationUnavailable.Error()}
	if h.Kind != holons.KindNormal && h.Kind != holons.KindIssue && h.Kind != holons.KindPullWorker {
		return unavailable, nil
	}
	p, ok := c.target(h)
	if !ok {
		return unavailable, nil
	}
	for _, pending := range p.Operations {
		if pending.Kind != "work_publish" {
			continue
		}
		operation, found, err := c.catalog.GetOperation(ctx, pending.RequestID)
		if err != nil {
			return unavailable, err
		}
		step := operation.Steps["publication"]
		if found && operation.HolonID == h.ID && step.Status == "succeeded" && step.HeadCommit != "" {
			r, err := c.holons.PublicationReadiness(ctx, id, p.HeadBranch, step.HeadCommit)
			r.RecoveringPublication = err == nil && r.WorkspaceHead == step.HeadCommit
			r.PublicationAvailable = r.RecoveringPublication
			return r, err
		}
	}
	if refresh && !h.RebaseAttempt.Active() {
		if c.sync == nil {
			return unavailable, pullrequestlifecycle.ErrSynchronizationStale
		}
		prepare := c.sync.SyncPullRequest
		if manual {
			if verifier, ok := c.sync.(interface {
				PreparePublication(context.Context, string) error
			}); ok {
				prepare = verifier.PreparePublication
			}
		}
		if err := prepare(ctx, p.ID); err != nil {
			return unavailable, err
		}
		p, ok = c.catalog.GetPullRequest(p.ID)
		if !ok {
			return unavailable, pullrequestlifecycle.ErrNotFound
		}
	}
	if !p.Status.Active() {
		unavailable.Reason = pullrequestlifecycle.ErrPublicationInactive.Error()
		return unavailable, nil
	}
	if !h.RebaseAttempt.Active() && !p.HasCurrentComparison() {
		unavailable.Reason = pullrequestlifecycle.ErrComparisonUnavailable.Error()
		return unavailable, nil
	}
	r, err := c.holons.PublicationReadiness(ctx, id, p.HeadBranch, p.HeadCommit)
	r.TargetVersion = publicationTargetVersion(p)
	r.TargetPullRequestID = p.ID
	// A verified published head can precede the atomic Holon checkpoint.
	// Require durable publication intent so initial PR creation is not mistaken
	// for an interrupted Push.
	if err == nil && r.Reason == "" && r.WorkspaceHead == r.TargetCommit && h.UpstreamHeadCommit != r.WorkspaceHead {
		if history, ok := c.catalog.(interface {
			HasPublicationAttempt(context.Context, string, string, string) (bool, error)
		}); ok {
			r.RecoveringPublication, err = history.HasPublicationAttempt(ctx, p.ID, h.ID, r.WorkspaceHead)
			r.PublicationAvailable = r.RecoveringPublication
		}
	}
	return r, err
}
func publicationTargetVersion(p pullrequestlifecycle.PullRequest) string {
	return fmt.Sprintf("%#v", *pullrequestlifecycle.CaptureMutationInputs(p))
}
func (c *rebaseAgentCoordinator) AcceptsPublicationTarget(h holons.Holon, target holons.PublicationReadiness) bool {
	p, ok := c.target(h)
	return ok && p.HasCurrentComparison() && p.Status.Active() && p.HeadBranch == target.TargetBranch && p.HeadCommit == target.TargetCommit && publicationTargetVersion(p) == target.TargetVersion
}
func (c *rebaseAgentCoordinator) ReservePublicationTarget(ctx context.Context, h holons.Holon, target holons.PublicationReadiness) error {
	if c.reservations == nil {
		return pullrequestlifecycle.ErrSynchronizationStale
	}
	return c.reservations.ReservePublicationTarget(ctx, h, target)
}
func (c *rebaseAgentCoordinator) CreateRebaseAgent(ctx context.Context, id, sourceID string) (holons.Holon, error) {
	h, err := c.holons.Get(ctx, id)
	if err != nil {
		return holons.Holon{}, err
	}
	if h.RebaseAttempt.Active() {
		return c.synchronize(ctx, h.ID, sourceID, nil, nil)
	}
	r, err := c.PreparePublication(ctx, id)
	if err != nil {
		return holons.Holon{}, err
	}
	if !r.RebaseAvailable {
		return holons.Holon{}, fmt.Errorf("%w: %s", holons.ErrPublicationUnavailable, r.Reason)
	}
	return c.synchronize(ctx, h.ID, sourceID, &r, nil)
}

// The same path handles explicit rebases, Address automation, and recovery.
// Address checkpoints the result or reserved agent before launch/publication.
func (c *rebaseAgentCoordinator) synchronize(ctx context.Context, id, sourceID string, target *holons.PublicationReadiness, reserved func(holons.Holon) error) (holons.Holon, error) {
	h, err := c.holons.Synchronize(ctx, id, sourceID, target)
	if err != nil {
		return h, err
	}
	a := h.RebaseAttempt
	if a.State == "succeeded" {
		if reserved != nil {
			return h, reserved(h)
		}
		return h, nil
	}
	if a.SourceAgentID != "" {
		sourceID = a.SourceAgentID
		// Synchronize only resumes attempts whose agent has not launched.
		// A closed or removed source must not strand that reservation.
		if target == nil {
			previous := h.AgentSession(sourceID)
			if previous.ID == "" || previous.ClosedAt != nil {
				sourceID = ""
			}
		}
	}
	var source holons.AgentSession
	var agentType string
	if existing := h.AgentSession(a.AgentID); existing.ID != "" {
		// Retry the accepted selection even if Settings changed during recovery.
		source = holons.AgentSession{ID: sourceID, Model: existing.Model, Permissions: existing.Permissions}
		agentType = existing.AgentType
	} else {
		source, agentType, err = actionAgentSource(ctx, h, sourceID, c.agents, c.harnesses, agentsettings.WorkflowPullRequestRebase)
	}
	if err != nil {
		return h, fmt.Errorf("%w: Make the configured agent CLI available, or select a fork-ready conversation, then retry Rebase: %v", holons.ErrPublicationUnavailable, err)
	}
	prompt := fmt.Sprintf("Git is already stopped at unresolved conflicts while rebasing branch %s onto publication branch %s at exact commit %s. Reconcile the existing conflicts, stage their resolutions, and run `git rebase --continue`; handle any subsequent conflicts in this same rebase. Resolve straightforward conflicts automatically; ask the user when intent is unclear. Do not abort or restart the rebase, rebase onto a moving branch, push, publish, or create a completion artifact. Run relevant checks and finish on the expected branch with a clean workspace. Preserve existing workflow control artifacts, including .holark/comment-reply.json. Other tabs share this workspace.", h.WorktreeBranch, a.TargetBranch, a.TargetCommit)
	if a.Progress != "conflicts" {
		definition, _ := prompttemplates.DefinitionByKey(prompttemplates.HolonRebaseKey)
		template := definition.DefaultValue
		if c.templates != nil {
			template = c.templates.Read(ctx, prompttemplates.HolonRebaseKey)
		}
		prompt = prompttemplates.Render(template, map[string]string{
			"source_branch": h.WorktreeBranch, "target_branch": a.TargetBranch, "target_commit": a.TargetCommit,
		})
	}
	prompt = fmt.Sprintf("Work only in the current worktree %s on workspace branch %s. The prepared source SHA is %s and the exact target SHA is %s. Publication branch %s is context, not a branch to check out. Do not fetch, query GitHub, switch branches or worktrees, or push. Report any local preparation mismatch without investigating remotely. If a rebase is already stopped at conflicts, continue that active rebase; HEAD may differ from the original source while it is in progress. Do not create .holark/rebase_complete or any other completion artifact.\n\n", h.WorktreePath, h.WorktreeBranch, a.StartHeadCommit, a.TargetCommit, a.TargetBranch) + prompt
	h, err = c.holons.AddRebaseAgentWithSelection(ctx, h.ID, source.ID, prompt, a.ID, agentType, holons.AgentSelection{Model: source.Model, Permissions: source.Permissions})
	if err != nil {
		return h, err
	}
	if reserved != nil {
		if err = reserved(h); err != nil {
			return h, err
		}
	}
	destination := h.RebaseAttempt.AgentID
	if err := c.holons.ReserveRebaseLaunch(ctx, h.ID, h.RebaseAttempt.ID, destination); err != nil {
		return h, err
	}
	if err := launchActionAgent(context.WithValue(ctx, rebaseLaunchContextKey{}, true), c.agents, h.ID, source.ID, destination); err != nil {
		if errors.Is(err, agentsessions.ErrUnavailable) {
			return h, errors.Join(fmt.Errorf("%w: The agent CLI became unavailable; retry Rebase when it is available.", holons.ErrPublicationUnavailable), c.holons.DeferRebaseLaunch(context.WithoutCancel(ctx), h.ID, h.RebaseAttempt.ID, err.Error()))
		}
		return h, errors.Join(err, c.holons.FailRebase(context.WithoutCancel(ctx), h.ID, destination, err.Error()))
	}
	if c.pinPullRequests != nil {
		c.pinPullRequests(ctx, h)
	}
	return c.holons.Get(ctx, h.ID)
}

func (s *terminalHolonService) PublicationReadiness(ctx context.Context, id string) (holons.PublicationReadiness, error) {
	return s.publicationReadiness(ctx, id, false)
}

func (s *terminalHolonService) PreparePublication(ctx context.Context, id string) (holons.PublicationReadiness, error) {
	return s.publicationReadiness(ctx, id, true)
}

func (s *terminalHolonService) publicationReadiness(ctx context.Context, id string, refresh bool) (holons.PublicationReadiness, error) {
	if s.rebaseAgents == nil {
		return holons.PublicationReadiness{}, holons.ErrPublicationUnavailable
	}
	r, err := s.rebaseAgents.publicationReadiness(ctx, id, refresh, false)
	if err != nil || s.work == nil {
		return r, err
	}
	w, workErr := s.work.WorkForSession(ctx, id)
	if workErr != nil && !errors.Is(workErr, pullrequestwork.ErrNotFound) {
		return r, workErr
	}
	if workErr == nil && w.IsAddress() {
		if w.PendingCompletion == nil || !w.Active() || w.Status == pullrequestwork.StatusCancelling {
			r.PublicationAvailable, r.RebaseAvailable = false, false
			if r.Reason == "" {
				r.Reason = "Address publishes automatically after its committed result is complete."
			}
		} else if w.PendingCompletion.State == "retry" && r.Reason == "" && !r.RebaseRequired {
			// An explicit retry may only need to finalize a no-change result or an
			// already-visible push. Keep that action available without new commits.
			r.PublicationAvailable = true
		}
	}
	return r, nil
}
func (s *terminalHolonService) CreateRebaseAgent(ctx context.Context, id, source string) (holons.Holon, error) {
	if s.rebaseAgents == nil {
		return holons.Holon{}, holons.ErrPublicationUnavailable
	}
	if s.addressCompletion != nil {
		return s.addressCompletion.StartRebase(ctx, id, source)
	}
	return s.rebaseAgents.CreateRebaseAgent(ctx, id, source)
}

// complete verifies and checkpoints success before either publication or runtime
// cleanup. Cleanup errors must not suppress Address publication or change success.
func (c *rebaseAgentCoordinator) complete(ctx context.Context, id, agentID string, address *addressCompletionCoordinator) error {
	h, err := c.holons.VerifyRebase(ctx, id, agentID)
	if errors.Is(err, holons.ErrRebaseActive) || errors.Is(err, holons.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx = context.WithoutCancel(ctx)
	var publicationErr error
	if address != nil {
		_, publicationErr = address.Rebased(ctx, h)
	}
	return errors.Join(publicationErr, c.closePending(ctx, id))
}

// closePending must be called outside Address and Holon locks: runtime shutdown
// can synchronously deliver an agent-stopped callback. Each close is idempotent.
func (c *rebaseAgentCoordinator) closePending(ctx context.Context, id string) error {
	if c == nil || c.closer == nil {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	h, err := c.holons.Get(ctx, id)
	if err != nil || h.RebaseAttempt == nil {
		return err
	}
	var result error
	for _, agentID := range h.RebaseAttempt.PendingClosureAgentIDs {
		_, err = c.closer.CloseRebaseAgentSession(ctx, id, agentID)
		if err != nil {
			result = errors.Join(result, fmt.Errorf("close Rebase tab %s: %w", agentID, err))
		}
	}
	return result
}
