package localapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/prompt_templates"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repositorybrowser"
)

type localWorkCatalog struct {
	sync  pullrequestlifecycle.Synchronizer
	store interface {
		GetPullRequest(string) (pullrequestlifecycle.PullRequest, bool)
	}
}

func (c localWorkCatalog) SyncPullRequest(ctx context.Context, id string) error {
	if c.sync == nil {
		return pullrequestlifecycle.ErrSynchronizationStale
	}
	return c.sync.SyncPullRequest(ctx, id)
}
func (c localWorkCatalog) PrepareWork(ctx context.Context, id string) error {
	if preparer, ok := c.sync.(pullrequestwork.WorkPreparer); ok {
		return preparer.PrepareWork(ctx, id)
	}
	return c.SyncPullRequest(ctx, id)
}
func (c localWorkCatalog) PrepareRebasePreview(ctx context.Context, id string) error {
	return c.PrepareWork(ctx, id)
}

func (c localWorkCatalog) PullRequest(id string) (pullrequestwork.PullRequest, bool) {
	p, ok := c.store.GetPullRequest(id)
	return workPullRequest(p), ok
}

func workPullRequest(p pullrequestlifecycle.PullRequest) pullrequestwork.PullRequest {
	var source struct {
		GitHub struct {
			Head string `json:"head_repository_url"`
			Base string `json:"base_repository_url"`
		} `json:"github"`
	}
	_ = json.Unmarshal(p.SyncData, &source)
	return pullrequestwork.PullRequest{
		ID: p.ID, Title: p.Title, Summary: p.Summary,
		RepositoryID: p.RepositoryID, SyncProvider: p.SyncProvider, SyncExternalID: p.SyncExternalID, Status: string(p.Status),
		LifecycleGeneration: p.LifecycleGeneration, TopologyGeneration: p.TopologyGeneration,
		HeadRepositoryURL: source.GitHub.Head, BaseRepositoryURL: source.GitHub.Base,
		BaseBranch: p.BaseBranch, BaseCommit: p.BaseCommit, DiffBaseCommit: p.DiffBaseCommit,
		HeadBranch: p.HeadBranch, HeadCommit: p.HeadCommit, Active: p.Status.Active(),
	}
}

type localWorkCommentReader interface {
	Get(context.Context, string) (pullrequestcomments.Comment, error)
	ListByPullRequest(context.Context, string) ([]pullrequestcomments.Comment, error)
}

type localWorkLauncher struct {
	holons    *terminalHolonService
	templates prompttemplates.Reader
	comments  localWorkCommentReader
	harnesses localHarnessPreferences
	works     interface {
		List(context.Context, string) ([]pullrequestwork.Work, error)
	}
}

func (l localWorkLauncher) Preflight(ctx context.Context, kind pullrequestwork.Kind) error {
	if l.harnesses == nil {
		return nil
	}
	_, err := l.harnesses.ResolveDefault(ctx, workflowForPullRequestWork(kind))
	return err
}

func (l localWorkLauncher) PreflightContinue(ctx context.Context, options pullrequestwork.ContinueOptions) error {
	if err := (holons.Create{Kind: holons.KindPullWorker, StartupMode: options.StartupMode, AgentType: options.AgentType}).ValidateStartup(); err != nil {
		return pullrequestwork.ErrInvalid
	}
	if options.StartupMode == "terminal" {
		return nil
	}
	if options.AgentType != "" && l.harnesses != nil {
		return l.harnesses.Validate(ctx, protocol.HarnessType(options.AgentType), workflowForPullRequestWork(pullrequestwork.KindWorker))
	}
	return l.Preflight(ctx, pullrequestwork.KindWorker)
}

func (l localWorkLauncher) StartContinue(ctx context.Context, p pullrequestwork.PullRequest, w pullrequestwork.Work, prompt string, options pullrequestwork.ContinueOptions) (string, error) {
	if w.Kind != pullrequestwork.KindWorker || w.Mode != pullrequestwork.ModeContinue {
		return "", pullrequestwork.ErrInvalid
	}
	return l.start(ctx, p, w, prompt, options)
}

type localWorkPublisher struct {
	sync    pullrequestlifecycle.Synchronizer
	holons  *terminalHolonService
	actions pullrequestlifecycle.PublicationCatalog
}

func (p localWorkPublisher) Publish(ctx context.Context, w pullrequestwork.Work, options pullrequestwork.PublicationOptions) (pullrequestwork.Publication, error) {
	requestID := ""
	if w.Mode == pullrequestwork.ModeContinue {
		requestID = options.RequestID
		if requestID == "" {
			requestID = pullrequestlifecycle.RequestID(ctx)
		}
		previous, found, err := p.actions.GetOperation(ctx, requestID)
		if err != nil {
			return pullrequestwork.Publication{}, err
		}
		if found {
			if previous.HolonID != w.SessionID || previous.Kind != "work_publish" || previous.PullRequestID != w.PullRequestID {
				return pullrequestwork.Publication{}, pullrequestlifecycle.OperationRequestConflict()
			}
			if previous.Status == "succeeded" || previous.Steps["publication"].Status == "succeeded" {
				return pullrequestwork.Publication{ExpectedHead: previous.ExpectedHead, CheckpointOnly: true, HeadCommit: previous.Steps["publication"].HeadCommit, OperationID: requestID}, nil
			}
			if previous.Status == "failed" {
				return pullrequestwork.Publication{}, pullrequestlifecycle.StoredOperationError(previous)
			}
			return pullrequestwork.Publication{}, pullrequestlifecycle.ErrOperationInProgress
		}
	}
	pending, err := p.actions.ListUnfinishedOperations(ctx)
	if err != nil {
		return pullrequestwork.Publication{}, err
	}
	for _, operation := range pending {
		if operation.Kind == "work_publish" && operation.PullRequestID == w.PullRequestID && operation.HolonID == w.SessionID && operation.Steps["publication"].Status == "succeeded" {
			return pullrequestwork.Publication{ExpectedHead: operation.ExpectedHead, CheckpointOnly: true, HeadCommit: operation.Steps["publication"].HeadCommit, OperationID: operation.RequestID}, nil
		}
	}
	target := w.HeadCommit
	if w.PublicationTargetCommit != "" {
		target = w.PublicationTargetCommit
	}
	targetVersion := ""
	var preparedHolon holons.Holon
	var preparedReadiness holons.PublicationReadiness
	expectedTarget := ""
	recoveringPublication := false
	synchronizeContinue := w.Kind == pullrequestwork.KindWorker && w.Mode == pullrequestwork.ModeContinue && p.holons.rebaseAgents != nil
	if synchronizeContinue {
		h, getErr := p.holons.Get(ctx, w.SessionID)
		if getErr != nil {
			return pullrequestwork.Publication{}, getErr
		}
		current, ok := p.actions.GetPullRequest(w.PullRequestID)
		if !ok {
			return pullrequestwork.Publication{}, pullrequestwork.ErrPullRequestNotFound
		}
		expectedTarget = pullrequestlifecycle.ExpectedPublicationTarget(h, options.TargetCommit, current.HeadCommit)
		preparedAt := time.Now()
		r, err := p.holons.rebaseAgents.PrepareManualPublication(ctx, w.SessionID)
		slog.DebugContext(ctx, "Publication preparation completed", "operation_id", requestID, "duration", time.Since(preparedAt), "error", err)
		if err != nil {
			return pullrequestwork.Publication{}, err
		}
		preparedHolon, preparedReadiness = h, r
		target, targetVersion = r.TargetCommit, r.TargetVersion
		recoveringPublication = r.RecoveringPublication
	}
	i, e := p.holons.InspectWorkspace(ctx, w.SessionID)
	if e != nil {
		return pullrequestwork.Publication{}, e
	}
	if synchronizeContinue {
		if err := pullrequestlifecycle.ValidatePreparedPublication(preparedHolon, preparedReadiness, i, expectedTarget, options.TargetCommit); err != nil {
			if errors.Is(err, pullrequestlifecycle.ErrPublicationStale) {
				return pullrequestwork.Publication{}, pullrequestwork.ErrStaleHead
			}
			return pullrequestwork.Publication{}, err
		}
	}
	if i.HeadCommit == target && w.PendingCompletion == nil && !synchronizeContinue {
		return pullrequestwork.Publication{}, pullrequestwork.ErrNoNewCommit
	}
	if w.PendingCompletion != nil && i.HeadCommit != w.PendingCompletion.Completion.ResultHeadCommit {
		return pullrequestwork.Publication{}, pullrequestwork.ErrStaleHead
	}
	var operation pullrequestlifecycle.Operation
	if requestID == "" {
		requestID = "work_publish:" + w.ID + ":" + target + ":" + i.HeadCommit
	}
	previous, found, err := p.actions.GetOperation(ctx, requestID)
	if err != nil {
		return pullrequestwork.Publication{}, err
	}
	if found {
		if previous.PullRequestID != w.PullRequestID || previous.HolonID != w.SessionID || previous.Kind != "work_publish" {
			return pullrequestwork.Publication{}, pullrequestwork.ErrInvalid
		}
		if step, ok := previous.Steps["publication"]; ok && step.Status == "succeeded" {
			return pullrequestwork.Publication{ExpectedHead: previous.ExpectedHead, CheckpointOnly: true, HeadCommit: step.HeadCommit, OperationID: previous.RequestID}, nil
		}
		if previous.Active() {
			return pullrequestwork.Publication{}, pullrequestlifecycle.ErrOperationInProgress
		}
		requestID = pullrequestlifecycle.RequestID(ctx)
	}
	if !synchronizeContinue {
		if p.sync == nil {
			return pullrequestwork.Publication{}, pullrequestlifecycle.ErrSynchronizationStale
		}
		if err := p.sync.SyncPullRequest(ctx, w.PullRequestID); err != nil {
			return pullrequestwork.Publication{}, err
		}
	}
	current, ok := p.actions.GetPullRequest(w.PullRequestID)
	if ok && current.HeadCommit != target && current.HeadCommit == i.HeadCommit && w.PendingCompletion != nil && w.PendingCompletion.PublicationAttempted {
		if history, supported := p.actions.(interface {
			HasPublicationAttempt(context.Context, string, string, string) (bool, error)
		}); supported {
			recoveringPublication, err = history.HasPublicationAttempt(ctx, current.ID, w.SessionID, i.HeadCommit)
			if err != nil {
				return pullrequestwork.Publication{}, err
			}
			if recoveringPublication {
				target = current.HeadCommit
			}
		}
	}
	if !ok || !current.Status.Active() || current.HeadCommit != target || current.HeadBranch != w.HeadBranch || (targetVersion != "" && targetVersion != publicationTargetVersion(current)) {
		return pullrequestwork.Publication{}, pullrequestwork.ErrStaleHead
	}
	var created bool
	operation, created, err = p.actions.BeginOperation(ctx, pullrequestlifecycle.Operation{
		RequestID: requestID, PullRequestID: w.PullRequestID, HolonID: w.SessionID, Kind: "work_publish",
		ExpectedHead: target, ExpectedInputs: pullrequestlifecycle.CaptureMutationInputs(current), Groups: []pullrequestlifecycle.FieldGroup{pullrequestlifecycle.TopologyGroup, pullrequestlifecycle.LifecycleGroup},
	})
	if err != nil {
		return pullrequestwork.Publication{}, err
	}
	if !created {
		return pullrequestwork.Publication{}, pullrequestlifecycle.ErrOperationInProgress
	}
	if err := p.actions.RecordOperationStep(ctx, operation.RequestID, w.PullRequestID, "publication", pullrequestlifecycle.OperationStep{Status: "running", HeadCommit: i.HeadCommit}); err != nil {
		_, _ = p.actions.CompleteOperation(context.WithoutCancel(ctx), operation.RequestID, "failed", err.Error())
		return pullrequestwork.Publication{}, err
	}

	remote := "origin"
	if source := workPullRequest(current).HeadRepositoryURL; source != "" {
		remote = source
	}

	gitStarted := time.Now()
	published, e := p.holons.Service.Publish(ctx, w.SessionID, holons.Publish{PreservePushErrors: w.PendingCompletion != nil, Remote: remote, UpstreamBranch: w.HeadBranch, ExpectedRemoteHead: target, IgnoredPaths: []string{pullRequestWorkArtifactPath(w.Kind)}, ExpectedWorkspaceHead: i.HeadCommit, RequireSynchronized: w.PendingCompletion != nil || synchronizeContinue})
	slog.DebugContext(ctx, "Git publication completed", "operation_id", requestID, "duration", time.Since(gitStarted), "error", e)
	if e != nil {
		outcome := "failed"
		var uncertain interface{ Uncertain() bool }
		if errors.As(e, &uncertain) && uncertain.Uncertain() {
			outcome = "uncertain"
		}
		_, _ = p.actions.CompleteOperation(context.WithoutCancel(ctx), operation.RequestID, outcome, e.Error())
	} else {
		if err := p.actions.RecordOperationStep(context.WithoutCancel(ctx), operation.RequestID, w.PullRequestID, "publication", pullrequestlifecycle.OperationStep{Status: "succeeded", HeadCommit: published.UpstreamHeadCommit}); err != nil {
			return pullrequestwork.Publication{}, err
		}
	}

	if errors.Is(e, repository.ErrStaleHead) {
		return pullrequestwork.Publication{}, pullrequestwork.ErrStaleHead
	}
	if e != nil {
		return pullrequestwork.Publication{}, e
	}
	return pullrequestwork.Publication{ExpectedHead: target, CheckpointOnly: recoveringPublication, HeadCommit: published.UpstreamHeadCommit, OperationID: operation.RequestID}, nil
}

func (l localWorkLauncher) Reserve(ctx context.Context, p pullrequestwork.PullRequest, w pullrequestwork.Work) error {
	kind, title := holons.KindPullWorker, "Address pull request comments"
	if w.Kind == pullrequestwork.KindRebase {
		kind, title = holons.KindRebase, "Rebase PR"
	}
	h, err := l.holons.Service.Reserve(ctx, w.SessionID, holons.Create{Kind: kind, Title: title, Prompt: w.Prompt, PullRequestID: p.ID})
	if err != nil {
		return err
	}
	if h.EndRequested || holons.IsTerminal(h.Status) || h.Status == holons.StatusCancelling {
		return pullrequestwork.ErrReservationEnded
	}
	return nil
}

func (l localWorkLauncher) StartReserved(ctx context.Context, p pullrequestwork.PullRequest, w pullrequestwork.Work, prompt string) (error, error) {
	_, err := l.start(ctx, p, w, prompt, pullrequestwork.ContinueOptions{})
	if errors.Is(err, holons.ErrSetupPersistence) {
		return nil, err
	}
	if errors.Is(err, holons.ErrReservationEnded) {
		return pullrequestwork.ErrReservationEnded, nil
	}
	return err, nil
}

func (l localWorkLauncher) SettleReserved(ctx context.Context, w pullrequestwork.Work) error {
	status := holons.StatusFailed
	if w.Status == pullrequestwork.StatusSkipped {
		status = holons.StatusCompleted
	}
	if w.Status == pullrequestwork.StatusCancelled {
		status = holons.StatusCancelled
	}
	err := l.holons.Service.SettleReserved(ctx, w.SessionID, status, w.Error)
	// An interrupted reservation write may have left only the work's durable ID.
	if errors.Is(err, holons.ErrNotFound) {
		return nil
	}
	return err
}

func (l localWorkLauncher) Start(ctx context.Context, p pullrequestwork.PullRequest, w pullrequestwork.Work, custom string) (string, error) {
	return l.start(ctx, p, w, custom, pullrequestwork.ContinueOptions{})
}

func (l localWorkLauncher) start(ctx context.Context, p pullrequestwork.PullRequest, w pullrequestwork.Work, custom string, startup pullrequestwork.ContinueOptions) (string, error) {
	if w.Kind == pullrequestwork.KindRebase {
		if w.HeadCommit == "" || w.TargetBaseCommit == "" {
			return "", pullrequestwork.ErrInvalid
		}
		p.HeadCommit = w.HeadCommit
	}
	if w.Kind == pullrequestwork.KindReview {
		if w.Provenance == nil {
			return "", pullrequestwork.ErrInvalid
		}
		p.DiffBaseCommit, p.HeadCommit = w.Provenance.DiffBaseCommit, w.Provenance.HeadCommit
	}
	selectedHarness := startup.AgentType
	selectedModel, selectedPermissions := "", ""
	if startup.StartupMode == "terminal" {
		selectedHarness = ""
	} else if l.harnesses != nil {
		resolved, err := l.harnesses.Resolve(ctx, workflowForPullRequestWork(w.Kind), protocol.HarnessType(selectedHarness))
		if err != nil {
			return "", err
		}
		selectedHarness, selectedModel, selectedPermissions = string(resolved.HarnessType), resolved.Model, resolved.Permissions
	} else if selectedHarness == "" {
		selectedHarness = "codex"
	}
	kind := holons.KindPullWorker
	launchOptions := agentsessions.LaunchOptions{GenerateIdentity: true}
	var title, templateKey, upstreamBranch, upstreamHeadCommit string
	var variables map[string]string
	switch {
	case w.Kind == pullrequestwork.KindWorker && w.Mode == pullrequestwork.ModeContinue:
		launchOptions.GenerateIdentity = false
		if strings.TrimSpace(startup.Title) == "" {
			previous, err := l.works.List(ctx, p.ID)
			if err != nil {
				return "", err
			}
			number := 1
			for _, work := range previous {
				if work.Kind == pullrequestwork.KindWorker && work.Mode == pullrequestwork.ModeContinue && work.SessionID != "" {
					number++
				}
			}
			title = fmt.Sprintf("Follow Up %d", number)
		}
		upstreamBranch = p.HeadBranch
		upstreamHeadCommit = p.HeadCommit
	case w.Kind == pullrequestwork.KindReview:
		kind = holons.KindPullReview
		title = "Review pull request"
		templateKey = prompttemplates.PullRequestReviewKey
		artifactPath := ".holark/review.json"
		description := strings.TrimSpace(p.Summary)
		formattedDescription := ""
		if description != "" {
			formattedDescription = "Summary:\n" + description + "\n\n"
		}
		variables = map[string]string{"pull_request_title": p.Title, "pull_request_description": description, "pull_request_summary": formattedDescription, "base_commit": p.DiffBaseCommit, "artifact_path": artifactPath}
		if variables["base_commit"] == "" {
			variables["base_commit"] = p.BaseCommit
		}
	case w.Kind == pullrequestwork.KindRebase:
		kind = holons.KindRebase
		title = "Rebase PR"
		templateKey = prompttemplates.PullRequestRebaseKey
		artifactPath := pullRequestWorkArtifactPath(w.Kind)
		variables = map[string]string{"pull_request_title": p.Title, "base_branch": p.BaseBranch, "head_branch": p.HeadBranch, "source_commit": w.HeadCommit, "target_base_commit": w.TargetBaseCommit, "artifact_path": artifactPath}
	default:
		title = "Address pull request comments"
		templateKey = prompttemplates.PullRequestWorkerKey
		artifactPath := ".holark/comment-reply.json"
		variables = map[string]string{
			"pull_request_title": p.Title,
			"mode_instruction":   pullRequestWorkModeInstruction(w.Mode),
			"artifact_path":      artifactPath,
		}
		if w.CommentID != "" {
			if l.comments == nil {
				return "", pullrequestwork.ErrInvalid
			}
			thread, err := l.commentThread(ctx, w.CommentID)
			if err != nil {
				return "", err
			}
			variables["comment_body"] = thread
		}
	}
	instruction := strings.TrimSpace(custom)
	var prepareAgent func(holons.Holon) error
	if instruction == "" && templateKey != "" {
		definition, _ := prompttemplates.DefinitionByKey(templateKey)
		template := definition.DefaultValue
		if l.templates != nil {
			template = l.templates.Read(ctx, templateKey)
		}
		instruction = prompttemplates.Render(template, variables)
		if templateKey == prompttemplates.PullRequestWorkerKey && len(instruction) > protocol.MaxPromptBytes && variables["comment_body"] != "" {
			// Spill only the discussion; keep the task and mode instructions inline.
			thread := variables["comment_body"]
			variables["comment_body"] = "Read the full comment thread in " + pullRequestCommentThreadPath + " before doing any work. Read the entire file, in chunks if needed."
			instruction = prompttemplates.Render(template, variables)
			prepareAgent = func(h holons.Holon) error {
				return writePullRequestCommentThread(h.WorktreePath, thread)
			}
		}
	}
	if w.Kind == pullrequestwork.KindReview {
		instruction += "\n\nReview the exact recorded range " + w.Provenance.DiffBaseCommit + ".." + w.Provenance.HeadCommit + ". Use these commits even if the PR or its branches advance."
	}
	if w.Kind == pullrequestwork.KindRebase {
		instruction = fmt.Sprintf("Work only in the current prepared worktree. Its prepared source SHA is %s; the exact target SHA is %s. The published PR branch %s is context, not a local branch to check out. Do not fetch, query GitHub, switch branches or worktrees, or push. Use these pinned commits even if cached refs move. If HEAD does not match the prepared source or the target commit is missing locally, stop and report the preparation mismatch; do not investigate remotely.\n\n", w.HeadCommit, w.TargetBaseCommit, p.HeadBranch) + instruction
	}
	if w.Kind == pullrequestwork.KindRebase && strings.TrimSpace(custom) != "" {
		instruction += "\n\nRebase onto exact target commit " + w.TargetBaseCommit + ". Run `git rebase " + w.TargetBaseCommit + "`; do not rebase onto a branch name. After the rebase, checks, and follow-up commits are complete, create the empty marker " + pullRequestWorkArtifactPath(w.Kind) + ". Leave it absent while asking for guidance. Do not commit the marker or push."
	}
	diffBaseCommit := p.DiffBaseCommit
	if diffBaseCommit == "" {
		diffBaseCommit = p.BaseCommit
	}
	if diffBaseCommit == "" {
		diffBaseCommit = p.HeadCommit
	}
	if strings.TrimSpace(startup.Title) != "" {
		title = startup.Title
	}
	create := holons.Create{Model: selectedModel, Permissions: selectedPermissions, SelectionResolved: true, StartupMode: startup.StartupMode, Title: title, Prompt: instruction, Kind: kind, BaseBranch: p.BaseBranch, BaseCommit: diffBaseCommit, WorkSessionStartCommit: p.HeadCommit, PullRequestID: p.ID, AgentType: selectedHarness, UpstreamBranch: upstreamBranch, UpstreamHeadCommit: upstreamHeadCommit}
	if w.Kind == pullrequestwork.KindRebase {
		create.AgentTitle = title
		create.RebaseTargetCommit = w.TargetBaseCommit
	}
	if w.InQueue() && w.SessionID != "" {
		h, err := l.holons.prepareAndStartReserved(ctx, w.SessionID, create, launchOptions, prepareAgent)
		return h.ID, err
	}
	h, err := l.holons.createWithLaunchOptions(ctx, create, launchOptions, prepareAgent)
	return h.ID, err
}

const pullRequestCommentThreadPath = ".holark/comment-thread.txt"

func writePullRequestCommentThread(worktree, thread string) error {
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Mkdir(".holark", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	file, err := root.OpenFile(pullRequestCommentThreadPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(thread)
	return errors.Join(writeErr, file.Close())
}

func (l localWorkLauncher) commentThread(ctx context.Context, id string) (string, error) {
	root, err := l.comments.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if root.ParentCommentID != "" {
		root, err = l.comments.Get(ctx, root.ParentCommentID)
		if err != nil {
			return "", err
		}
	}
	comments, err := l.comments.ListByPullRequest(ctx, root.PullRequestID)
	if err != nil {
		return "", err
	}
	var replies []pullrequestcomments.Comment
	for _, comment := range comments {
		if comment.ParentCommentID == root.ID {
			replies = append(replies, comment)
		}
	}
	sort.Slice(replies, func(i, j int) bool {
		if replies[i].CreatedAt.Equal(replies[j].CreatedAt) {
			return replies[i].ID < replies[j].ID
		}
		return replies[i].CreatedAt.Before(replies[j].CreatedAt)
	})
	var thread strings.Builder
	thread.WriteString(strings.TrimSpace(root.Body))
	thread.WriteString(reviewCommentLocation(root))
	for _, reply := range replies {
		fmt.Fprintf(&thread, "\n\nReply (%s, %s):\n%s", reply.AuthorType, reply.CreatedAt.UTC().Format(time.RFC3339Nano), strings.TrimSpace(reply.Body))
	}
	return thread.String(), nil
}

func workflowForPullRequestWork(kind pullrequestwork.Kind) agentsettings.Workflow {
	switch kind {
	case pullrequestwork.KindReview:
		return agentsettings.WorkflowPullRequestReview
	case pullrequestwork.KindRebase:
		return agentsettings.WorkflowPullRequestRebase
	default:
		return agentsettings.WorkflowPullRequestFeedback
	}
}

func pullRequestWorkModeInstruction(mode pullrequestwork.Mode) string {
	if mode == pullrequestwork.ModeAssisted {
		return "You may ask the user for clarification if needed. Do not commit changes unless the user explicitly approves a proposed commit message; if the fix is ready with uncommitted changes, finish so Holark can request commit approval."
	}
	return "Run independently without waiting for user input. Commit the fix before finishing; only ask for user input if you are truly blocked."
}

type localReviewComments struct{ comments *pullrequestcomments.Service }

type localRebaser struct {
	scheduler  *pullrequestlifecycle.RefreshCoordinator
	actions    pullrequestlifecycle.PublicationCatalog
	manager    *repositorybrowser.Manager
	repository repositorybrowser.Repository
}

func (r localRebaser) RebasePinned(ctx context.Context, p pullrequestwork.PullRequest, baseCommit, headCommit, operationID string) (pullrequestwork.RebasePreparation, error) {
	return r.rebaseWithOperation(ctx, p, baseCommit, headCommit, operationID)
}

func (r localRebaser) PreviewRebasePinned(ctx context.Context, p pullrequestwork.PullRequest, baseCommit, headCommit string) (pullrequestwork.RebasePreview, error) {
	var result repositorybrowser.RebasePreviewResult
	err := withPullRequestGit(ctx, r.scheduler, r.repository.ID, func(ctx context.Context) error {
		target, err := r.rebaseSources(ctx, p, baseCommit, headCommit)
		if err != nil {
			return err
		}
		result, err = r.manager.PreviewRebasePinned(ctx, target, repositorybrowser.RebaseRequest{BaseCommit: baseCommit, BaseBranch: p.BaseBranch, HeadBranch: p.HeadBranch, ExpectedHeadCommit: headCommit})
		return err
	})
	if errors.Is(err, repositorybrowser.ErrStaleHead) {
		return pullrequestwork.RebasePreview{}, pullrequestwork.ErrStaleHead
	}
	if err != nil {
		return pullrequestwork.RebasePreview{}, err
	}
	return pullrequestwork.RebasePreview{UpToDate: result.UpToDate, Conflicts: result.Conflicts, BaseCommitsAhead: result.BaseCommitsAhead}, nil
}

func (f localReviewComments) DeliverReviewComments(ctx context.Context, pr, session, review, head string, comments []pullrequestwork.ReviewComment, draft bool) error {
	request := reviewCommentBatch(pr, session, review, head, comments)
	request.Draft = draft
	_, e := f.comments.CreateReviewComments(ctx, request)
	if errors.Is(e, pullrequestcomments.ErrInvalidComment) {
		return pullrequestwork.ErrInvalid
	}
	return e
}

func (f localReviewComments) DeliverReply(ctx context.Context, pr, parent, session, worker, head, body string) error {
	_, e := f.comments.CompleteWorkerReply(ctx, pullrequestcomments.CreateComment{PullRequestID: pr, ParentCommentID: parent, Body: body, Origin: pullrequestcomments.WorkerOrigin(session, worker, head)})
	return e
}

func reviewCommentLocation(comment pullrequestcomments.Comment) string {
	if comment.Scope == pullrequestcomments.ScopeLine && comment.Line != nil {
		return "\n\nComment location: " + comment.Path + ":" + fmt.Sprint(*comment.Line) + " (" + comment.Side + ")\nReviewed commit: " + comment.OriginalHeadCommit
	}
	if comment.Scope == pullrequestcomments.ScopeFile {
		return "\n\nComment location: " + comment.Path + "\nReviewed commit: " + comment.OriginalHeadCommit
	}
	return ""
}

// localWorkComments translates comment ownership without coupling scheduling to
// the comments feature's persistence or HTTP implementation.
type localWorkComments struct{ comments localWorkCommentReader }

func (c localWorkComments) AddressComment(ctx context.Context, pr, id string) (bool, error) {
	comment, err := c.comments.Get(ctx, id)
	if errors.Is(err, pullrequestcomments.ErrCommentNotFound) {
		return false, pullrequestwork.ErrInvalid
	}
	if err != nil {
		return false, err
	}
	if comment.PullRequestID != pr {
		return false, pullrequestwork.ErrInvalid
	}
	return comment.Status == pullrequestcomments.Resolved, nil
}

type localWorkRuntime struct{ holons *terminalHolonService }

func (r localWorkRuntime) Cancel(ctx context.Context, id string) error {
	_, err := r.holons.End(ctx, id)
	if errors.Is(err, holons.ErrNotFound) {
		return nil
	}
	return err
}

func (r localWorkRuntime) State(ctx context.Context, id string) (pullrequestwork.Status, error) {
	h, err := r.holons.Get(ctx, id)
	if errors.Is(err, holons.ErrNotFound) {
		return pullrequestwork.StatusFailed, nil
	}
	if err != nil {
		return "", err
	}
	if h.Status == holons.StatusCancelling {
		return pullrequestwork.StatusCancelling, nil
	}
	if h.Kind == holons.KindPullReview && h.EndRequested && holons.IsTerminal(h.Status) {
		return pullrequestwork.StatusCancelled, nil
	}
	if h.Status == holons.StatusCancelled {
		return pullrequestwork.StatusCancelled, nil
	}
	// A verified Rebase still owns its queue slot until the saved Address
	// completion is published, even if both agents have already exited.
	if h.RebaseAttempt.Active() || (h.RebaseAttempt != nil && h.RebaseAttempt.State == "succeeded") {
		return pullrequestwork.StatusWaiting, nil
	}
	if holons.IsTerminal(h.Status) {
		return pullrequestwork.StatusFailed, nil
	}
	return pullrequestwork.StatusRunning, nil
}

// Recover retires orphaned agent reservations, including launches interrupted
// before a terminal was bound. Startup has already reconciled terminal bindings.
func (r localWorkRuntime) Recover(ctx context.Context, id string) error {
	h, err := r.holons.Get(ctx, id)
	if errors.Is(err, holons.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(h.AgentSessions) == 0 && !holons.IsTerminal(h.Status) {
		return r.holons.Service.SettleReserved(ctx, id, holons.StatusFailed, "Holark restarted before this work completed.")
	}
	for _, agent := range h.AgentSessions {
		if agent.ClosedAt == nil && !holons.IsTerminal(holons.Status(agent.Status)) {
			if _, err := r.holons.SetAgentSessionStatus(ctx, id, agent.ID, holons.StatusLost, "Holark restarted before pull request work completed.", agent.ResumeTarget); err != nil {
				return err
			}
		}
	}
	return nil
}

// localReviewChanges reuses metadata's Git fingerprint implementation without
// coupling the work service to metadata's domain or persistence.
type localReviewChanges struct{ changes pullrequestmetadata.Changes }

func (c localReviewChanges) Capture(ctx context.Context, base, head string) (*pullrequestwork.ReviewInput, error) {
	input, err := c.changes.Capture(ctx, base, head)
	if err != nil || input == nil {
		return nil, err
	}
	return &pullrequestwork.ReviewInput{DiffBaseCommit: input.DiffBaseCommit, HeadCommit: input.HeadCommit,
		PatchFingerprint: input.PatchFingerprint, MessagesFingerprint: input.MessagesFingerprint}, nil
}

func reviewCommentBatch(pr, session, review, head string, comments []pullrequestwork.ReviewComment) pullrequestcomments.CreateReviewComments {
	translated := make([]pullrequestcomments.ReviewComment, len(comments))
	for index, comment := range comments {
		translated[index] = pullrequestcomments.ReviewComment{Body: comment.Body, Scope: pullrequestcomments.Scope(comment.Scope), Path: comment.Path, Side: comment.Side, Line: comment.Line}
	}
	return pullrequestcomments.CreateReviewComments{PullRequestID: pr, Comments: translated, Origin: pullrequestcomments.ReviewOrigin(session, review, head)}
}
