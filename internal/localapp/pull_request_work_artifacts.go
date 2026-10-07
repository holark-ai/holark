package localapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

const (
	maxPullRequestWorkArtifactBytes  = 256 << 10
	maxPullRequestWorkTextCharacters = 4000
	maxPullRequestReviewComments     = 30
)

var errPullRequestWorkArtifactMissing = errors.New("pull request work artifact missing")

type localPullRequestWorkCompletionService interface {
	WorkForSession(context.Context, string) (pullrequestwork.Work, error)
	Complete(context.Context, string, string, pullrequestwork.Completion) (pullrequestwork.Work, error)
	Fail(context.Context, string, string) error
	Wait(context.Context, string, string) error
	Stale(context.Context, string, string) error
}

type pullRequestWorkCompletionDisposition struct {
	Terminal        bool
	AlreadyHandled  bool
	AgentsFinalized bool
	Reason          string
}

// localPullRequestWorkCompletionCoordinator owns the local artifact boundary
// and delegates durable business transitions to pullrequestwork.Service.
type localPullRequestWorkCompletionCoordinator struct {
	reviewMu *sync.Mutex
	holons   *holons.Service
	agents   localAgentRuntime
	work     localPullRequestWorkCompletionService
	address  *addressCompletionCoordinator
	logger   *log.Logger
}

func (c localPullRequestWorkCompletionCoordinator) Complete(ctx context.Context, h holons.Holon) (pullRequestWorkCompletionDisposition, error) {
	source := ""
	if len(h.AgentSessions) > 0 {
		source = h.AgentSessions[0].ID
	}
	return c.CompleteFromAgent(ctx, h, source)
}
func (c localPullRequestWorkCompletionCoordinator) CompleteFromAgent(ctx context.Context, h holons.Holon, agentID string) (pullRequestWorkCompletionDisposition, error) {
	if h.Kind == holons.KindPullReview && c.reviewMu != nil {
		c.reviewMu.Lock()
		defer c.reviewMu.Unlock()
	}
	return c.completeFromAgentLocked(ctx, h, agentID, false)
}

// The caller holds the review agent-admission lock, including during recovery.
func (c localPullRequestWorkCompletionCoordinator) completeFromAgentLocked(ctx context.Context, h holons.Holon, agentID string, recovering bool) (disposition pullRequestWorkCompletionDisposition, resultErr error) {
	defer c.holons.BeginFinalization(h.ID)()
	w, err := c.work.WorkForSession(ctx, h.ID)
	if err != nil {
		reason := "Holark could not find the associated pull request work: " + err.Error()
		return pullRequestWorkCompletionDisposition{Terminal: true, Reason: reason}, err
	}
	defer func() {
		if w.Kind != pullrequestwork.KindReview || !disposition.Terminal || c.holons == nil {
			return
		}
		// Keep admission locked until every review tab is retired, so a new
		// agent cannot escape cleanup between import and workspace archival.
		disposition.AgentsFinalized = true
		for _, agent := range h.AgentSessions {
			resultErr = errors.Join(resultErr, finalizePullRequestWorkAgent(ctx, c.holons, c.agents, h, agent.ID, disposition.Reason))
		}
	}()
	if !w.Active() || w.Status == pullrequestwork.StatusCancelling {
		return pullRequestWorkCompletionDisposition{AlreadyHandled: true}, nil
	}
	if recovering && !w.IsAssistedReview() {
		return pullRequestWorkCompletionDisposition{}, nil
	}

	if w.IsAddress() && (w.PendingCompletion != nil || h.RebaseAttempt.Active()) {
		return pullRequestWorkCompletionDisposition{AlreadyHandled: true}, nil
	}
	if w.Kind == pullrequestwork.KindReview && c.holons != nil {
		// Review tabs share their artifacts. Refresh under the agent-admission
		// lock and keep it through import and removal so no new writer can start.
		h, err = c.holons.Get(ctx, h.ID)
		if err != nil {
			return pullRequestWorkCompletionDisposition{}, err
		}
		for _, agent := range h.AgentSessions {
			if agent.ClosedAt != nil || holons.IsTerminal(holons.Status(agent.Status)) {
				continue
			}
			// Startup clears observability, but a saved completed turn with no
			// replacement terminal cannot still be writing the shared artifact.
			if recovering && agent.Status == string(holons.StatusRestoring) && agent.TerminalID == "" && agent.InputState == string(protocol.InputTaskComplete) {
				continue
			}
			switch holons.Status(agent.Status) {
			case holons.StatusNaming, holons.StatusQueued, holons.StatusPreparing, holons.StatusRestoring, holons.StatusCancelling:
				return pullRequestWorkCompletionDisposition{}, nil
			}
			if agent.ObservabilityStatus != string(protocol.ObservabilityHealthy) ||
				(agent.Activity != protocol.ActivityIdle && agent.Activity != protocol.ActivityCompleted &&
					!(agent.Activity == protocol.ActivityNeedsInput && agent.InputState == string(protocol.InputUserRequired))) {
				return pullRequestWorkCompletionDisposition{}, nil
			}
		}
	}
	completion, err := readPullRequestWorkArtifact(h, w.Kind, w)
	// An assisted review without findings is still a conversation with the user.
	if w.IsAssistedReview() && errors.Is(err, errPullRequestWorkArtifactMissing) {
		_, obsErr := c.holons.UpdateAgentObservation(ctx, h.ID, agentID, "", "", "", string(protocol.InputUserRequired), "", "", protocol.ActivityNeedsInput, nil, h.AgentSession(agentID).TerminalID)
		return pullRequestWorkCompletionDisposition{}, obsErr
	}
	if w.Kind == pullrequestwork.KindRebase && errors.Is(err, errPullRequestWorkArtifactMissing) {
		return pullRequestWorkCompletionDisposition{}, nil
	}
	if err != nil {
		reason := "Holark could not import the work artifact: " + err.Error()
		if w.IsAssistedReview() {
			c.logInvalidArtifact(h, w, err)
			return c.waitForReviewCorrection(ctx, h, agentID, w, reason)
		}
		var removeErr error
		if errors.Is(err, pullrequestwork.ErrInvalid) {
			c.logInvalidArtifact(h, w, err)
			removeErr = removePullRequestWorkArtifact(h, w.Kind)
		}
		if w.Kind == pullrequestwork.KindWorker && w.Mode == pullrequestwork.ModeAssisted && errors.Is(err, errPullRequestWorkArtifactMissing) {
			waitErr := c.work.Wait(ctx, w.ID, reason)
			return pullRequestWorkCompletionDisposition{}, errors.Join(waitErr, removeErr)
		}
		stateErr := c.work.Fail(ctx, w.ID, reason)
		return pullRequestWorkCompletionDisposition{Terminal: true, Reason: reason}, errors.Join(err, stateErr, removeErr)
	}

	if w.Kind == pullrequestwork.KindWorker || w.Kind == pullrequestwork.KindRebase {
		inspection, inspectErr := c.holons.InspectWorkspaceWithOptions(ctx, h.ID, holons.InspectOptions{IgnoredPaths: []string{pullRequestWorkArtifactPath(w.Kind)}})
		if inspectErr != nil {
			reason := "Holark could not inspect the completed work: " + inspectErr.Error()
			stateErr := c.work.Fail(ctx, w.ID, reason)
			return pullRequestWorkCompletionDisposition{Terminal: true, Reason: reason}, errors.Join(inspectErr, stateErr)
		}
		if completion.ResultHeadCommit != "" && completion.ResultHeadCommit != inspection.HeadCommit {
			err = pullrequestwork.ErrInvalid
			reason := "Holark could not import the work artifact: result head does not match the workspace."
			stateErr := c.work.Fail(ctx, w.ID, reason)
			c.logInvalidArtifact(h, w, err)
			removeErr := removePullRequestWorkArtifact(h, w.Kind)
			return pullRequestWorkCompletionDisposition{Terminal: true, Reason: reason}, errors.Join(err, stateErr, removeErr)
		}
		completion.ResultHeadCommit = inspection.HeadCommit
	}

	if w.IsAddress() && c.address != nil {
		return c.address.Begin(ctx, h, agentID, completion)
	}
	completed, err := c.work.Complete(ctx, w.ID, w.HeadCommit, completion)
	if err == nil {
		removeErr := removePullRequestWorkArtifact(h, w.Kind)
		return pullRequestWorkCompletionDisposition{Terminal: true}, removeErr
	}
	if completed.Status == pullrequestwork.StatusCompleted {
		removeErr := removePullRequestWorkArtifact(h, w.Kind)
		reason := "Holark completed the pull request work, but a post-completion action failed: " + err.Error()
		return pullRequestWorkCompletionDisposition{Terminal: true, Reason: reason}, errors.Join(err, removeErr)
	}

	reason := "Holark could not complete the pull request work: " + err.Error()
	if w.IsAssistedReview() {
		// Imports can fail after saving some drafts. Keep the work and artifact
		// retryable; import receipts prevent duplicating drafts already saved.
		return c.waitForReviewCorrection(ctx, h, agentID, w, reason)
	}
	if errors.Is(err, pullrequestwork.ErrStaleHead) {
		removeErr := removePullRequestWorkArtifact(h, w.Kind)
		var stateErr error
		if w.Kind == pullrequestwork.KindWorker {
			stateErr = c.work.Stale(ctx, w.ID, reason)
		} else {
			stateErr = c.work.Fail(ctx, w.ID, reason)
		}
		return pullRequestWorkCompletionDisposition{Terminal: true, Reason: reason}, errors.Join(err, stateErr, removeErr)
	}
	if w.Kind == pullrequestwork.KindWorker && w.Mode == pullrequestwork.ModeAssisted && (errors.Is(err, pullrequestwork.ErrPublish) || errors.Is(err, pullrequestwork.ErrNoNewCommit)) {
		return pullRequestWorkCompletionDisposition{}, c.work.Wait(ctx, w.ID, reason)
	}
	removeErr := error(nil)
	if errors.Is(err, pullrequestwork.ErrInvalid) {
		c.logInvalidArtifact(h, w, err)
		removeErr = removePullRequestWorkArtifact(h, w.Kind)
	}
	stateErr := c.work.Fail(ctx, w.ID, reason)
	return pullRequestWorkCompletionDisposition{Terminal: true, Reason: reason}, errors.Join(err, stateErr, removeErr)
}

func (c localPullRequestWorkCompletionCoordinator) waitForReviewCorrection(ctx context.Context, h holons.Holon, agentID string, w pullrequestwork.Work, reason string) (pullRequestWorkCompletionDisposition, error) {
	waitErr := c.work.Wait(ctx, w.ID, reason)
	_, obsErr := c.holons.UpdateAgentObservation(ctx, h.ID, agentID, "", "", "", string(protocol.InputUserRequired), "", "", protocol.ActivityNeedsInput, nil, h.AgentSession(agentID).TerminalID)
	return pullRequestWorkCompletionDisposition{}, errors.Join(waitErr, obsErr)
}

func (c localPullRequestWorkCompletionCoordinator) logInvalidArtifact(h holons.Holon, work pullrequestwork.Work, artifactErr error) {
	if c.logger == nil || h.WorktreePath == "" {
		return
	}
	path := pullRequestWorkArtifactPath(work.Kind)
	var data []byte
	root, readErr := os.OpenRoot(h.WorktreePath)
	if readErr == nil {
		defer root.Close()
		var info fs.FileInfo
		info, readErr = root.Lstat(path)
		if readErr == nil && !info.Mode().IsRegular() {
			readErr = errors.New("artifact is not a regular file")
		}
		if readErr == nil {
			var file *os.File
			file, readErr = root.Open(path)
			if readErr == nil {
				data, readErr = io.ReadAll(io.LimitReader(file, maxPullRequestWorkArtifactBytes))
				_ = file.Close()
			}
		}
	}
	c.logger.Printf("pull request work artifact import failed work_id=%q pull_request_id=%q session_id=%q path=%q error=%q read_error=%q artifact_bytes=%d", work.ID, work.PullRequestID, work.SessionID, path, artifactErr, readErr, len(data))
}

func pullRequestWorkArtifactPath(kind pullrequestwork.Kind) string {
	switch kind {
	case pullrequestwork.KindReview:
		return ".holark/review.json"
	case pullrequestwork.KindRebase:
		return ".holark/rebase_complete"
	default:
		return ".holark/comment-reply.json"
	}
}

func removePullRequestWorkArtifact(h holons.Holon, kind pullrequestwork.Kind) error {
	if h.WorktreePath == "" {
		return nil
	}
	root, err := os.OpenRoot(h.WorktreePath)
	if err != nil {
		return err
	}
	defer root.Close()
	if kind == pullrequestwork.KindReview {
		paths, listErr := pullRequestReviewArtifactPaths(root)
		if listErr != nil {
			return listErr
		}
		var removeErr error
		for _, artifactPath := range paths {
			if err := root.Remove(artifactPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				removeErr = errors.Join(removeErr, err)
			}
		}
		return removeErr
	}
	err = root.Remove(pullRequestWorkArtifactPath(kind))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func readPullRequestWorkArtifact(h holons.Holon, kind pullrequestwork.Kind, works ...pullrequestwork.Work) (pullrequestwork.Completion, error) {
	work := pullrequestwork.Work{Kind: kind}
	if len(works) > 0 {
		work = works[0]
	}
	if h.WorktreePath == "" {
		return pullrequestwork.Completion{}, pullrequestwork.ErrInvalid
	}
	path := pullRequestWorkArtifactPath(kind)
	root, err := os.OpenRoot(h.WorktreePath)
	if err != nil {
		return pullrequestwork.Completion{}, err
	}
	defer root.Close()
	info, err := root.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return pullrequestwork.Completion{}, fmt.Errorf("%w: %w", errPullRequestWorkArtifactMissing, err)
		}
		return pullrequestwork.Completion{}, err
	}
	if !info.Mode().IsRegular() {
		return pullrequestwork.Completion{}, pullrequestwork.ErrInvalid
	}
	if kind == pullrequestwork.KindRebase {
		return pullrequestwork.Completion{PullRequestID: h.PullRequestID, HeadCommit: work.HeadCommit}, nil
	}
	if info.Size() > maxPullRequestWorkArtifactBytes {
		return pullrequestwork.Completion{}, pullrequestwork.ErrInvalid
	}
	f, err := root.Open(path)
	if err != nil {
		return pullrequestwork.Completion{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPullRequestWorkArtifactBytes+1))
	if err != nil {
		return pullrequestwork.Completion{}, err
	}
	if len(data) > maxPullRequestWorkArtifactBytes || !utf8.Valid(data) {
		return pullrequestwork.Completion{}, pullrequestwork.ErrInvalid
	}
	var artifact struct {
		PullRequestID    string   `json:"pull_request_id"`
		HeadCommit       string   `json:"head_commit"`
		ResultHeadCommit string   `json:"result_head_commit"`
		Reply            string   `json:"reply"`
		Findings         []string `json:"findings"`
		Comments         []struct {
			Body  string  `json:"body"`
			Scope *string `json:"scope"`
			Path  string  `json:"path"`
			Side  string  `json:"side"`
			Line  *int    `json:"line"`
		} `json:"comments"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&artifact); err != nil {
		return pullrequestwork.Completion{}, fmt.Errorf("%w: invalid JSON", pullrequestwork.ErrInvalid)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return pullrequestwork.Completion{}, fmt.Errorf("%w: invalid JSON", pullrequestwork.ErrInvalid)
	}
	if utf8.RuneCountInString(artifact.Reply) > maxPullRequestWorkTextCharacters {
		return pullrequestwork.Completion{}, pullrequestwork.ErrInvalid
	}
	strictReview := work.IsAssistedReview()
	// Assisted reviews must import every finding or remain available for correction.
	// Only historical automatic reviews use the truncation and count limits below.
	completion := pullrequestwork.Completion{PullRequestID: artifact.PullRequestID, HeadCommit: artifact.HeadCommit, ResultHeadCommit: artifact.ResultHeadCommit, ReplyBody: artifact.Reply}
	for index, finding := range artifact.Findings {
		body := strings.TrimSpace(finding)
		if strictReview && len(body) > pullrequestcomments.MaximumBodyLength {
			return pullrequestwork.Completion{}, fmt.Errorf("%w: findings[%d] exceeds %d UTF-8 bytes", pullrequestwork.ErrInvalid, index, pullrequestcomments.MaximumBodyLength)
		}
		if kind == pullrequestwork.KindReview && !strictReview {
			body = truncatePullRequestReviewComment(body)
			if body == "" {
				continue
			}
			if len(completion.Comments) == maxPullRequestReviewComments {
				break
			}
		} else if body == "" || utf8.RuneCountInString(body) > maxPullRequestWorkTextCharacters {
			return pullrequestwork.Completion{}, fmt.Errorf("%w: findings[%d] must contain 1 to %d characters", pullrequestwork.ErrInvalid, index, maxPullRequestWorkTextCharacters)
		}
		completion.Comments = append(completion.Comments, pullrequestwork.ReviewComment{Body: body, Scope: "pull_request"})
	}
	for index, comment := range artifact.Comments {
		if kind == pullrequestwork.KindReview && !strictReview && len(completion.Comments) == maxPullRequestReviewComments {
			break
		}
		body := strings.TrimSpace(comment.Body)
		if strictReview && len(body) > pullrequestcomments.MaximumBodyLength {
			return pullrequestwork.Completion{}, fmt.Errorf("%w: comments[%d].body exceeds %d UTF-8 bytes", pullrequestwork.ErrInvalid, index, pullrequestcomments.MaximumBodyLength)
		}
		if kind == pullrequestwork.KindReview && !strictReview {
			body = truncatePullRequestReviewComment(body)
			if body == "" {
				continue
			}
		} else if body == "" || utf8.RuneCountInString(body) > maxPullRequestWorkTextCharacters {
			return pullrequestwork.Completion{}, fmt.Errorf("%w: comments[%d].body must contain 1 to %d characters", pullrequestwork.ErrInvalid, index, maxPullRequestWorkTextCharacters)
		}
		scope := "pull_request"
		if comment.Scope != nil {
			scope = *comment.Scope
		}
		parsed := pullrequestwork.ReviewComment{Body: body, Scope: scope, Path: comment.Path, Side: comment.Side, Line: comment.Line}
		if !validArtifactReviewComment(parsed) {
			if kind == pullrequestwork.KindReview && !strictReview {
				continue
			}
			return pullrequestwork.Completion{}, fmt.Errorf("%w: comments[%d] has an invalid scope or location (path, side, line)", pullrequestwork.ErrInvalid, index)
		}
		completion.Comments = append(completion.Comments, parsed)
	}
	completion.ReplyBody = strings.TrimSpace(completion.ReplyBody)
	if completion.PullRequestID == "" {
		completion.PullRequestID = h.PullRequestID
	}
	if completion.HeadCommit == "" {
		completion.HeadCommit = work.HeadCommit
	}
	if completion.PullRequestID != h.PullRequestID || (work.HeadCommit != "" && completion.HeadCommit != work.HeadCommit) {
		return completion, pullrequestwork.ErrInvalid
	}
	if kind == pullrequestwork.KindWorker && completion.ReplyBody == "" {
		return completion, pullrequestwork.ErrInvalid
	}
	return completion, nil
}

func truncatePullRequestReviewComment(body string) string {
	if utf8.RuneCountInString(body) <= maxPullRequestWorkTextCharacters {
		return body
	}
	return strings.TrimSpace(string([]rune(body)[:maxPullRequestWorkTextCharacters]))
}

func pullRequestReviewArtifactPaths(root *os.Root) ([]string, error) {
	entries, err := fs.ReadDir(root.FS(), ".holark")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		name := entry.Name()
		part := strings.TrimSuffix(strings.TrimPrefix(name, "review.part-"), ".json")
		if name == "review.json" || (strings.HasPrefix(name, "review.part-") && strings.HasSuffix(name, ".json") && part != "") {
			paths = append(paths, path.Join(".holark", name))
		}
	}
	return paths, nil
}

func validArtifactReviewComment(comment pullrequestwork.ReviewComment) bool {
	switch comment.Scope {
	case "pull_request":
		return comment.Path == "" && comment.Side == "" && comment.Line == nil
	case "file":
		return strings.TrimSpace(comment.Path) != "" && comment.Side == "" && comment.Line == nil
	case "line":
		return strings.TrimSpace(comment.Path) != "" && (comment.Side == "LEFT" || comment.Side == "RIGHT") && comment.Line != nil && *comment.Line > 0
	default:
		return false
	}
}
