package pullrequestfixture

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/localapp"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

const resolvedREADME = "Pull request scenario with PR creation and conflicting rebase coverage\n"

func prepareRebaseRemote(root, repositoryPath string) error {
	remote := filepath.Join(root, "remote.git")
	for _, args := range [][]string{
		{"init", "--bare", "-b", "main", remote},
		{"config", "user.name", "Holark Scenario"},
		{"config", "user.email", "scenario@invalid"},
		{"remote", "add", "origin", remote},
		{"push", "-u", "origin", "main"},
	} {
		if _, err := gitOutput(repositoryPath, args...); err != nil {
			return err
		}
	}
	return nil
}

func advanceCleanBase(repositoryPath string) error {
	if err := os.WriteFile(filepath.Join(repositoryPath, "main-only.txt"), []byte("A clean main update landed before the rebase preview.\n"), 0o600); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"add", "main-only.txt"},
		{"commit", "-m", "Advance main without conflicting with the pull request"},
		{"push", "origin", "main"},
	} {
		if _, err := gitOutput(repositoryPath, args...); err != nil {
			return err
		}
	}
	return nil
}

func advanceConflictingBase(repositoryPath string) error {
	round, err := gitOutput(repositoryPath, "rev-list", "--count", "HEAD")
	if err != nil {
		return err
	}
	content := "Pull request scenario with conflicting rebase coverage round " + strings.TrimSpace(round) + "\n"
	if err = os.WriteFile(filepath.Join(repositoryPath, "README.md"), []byte(content), 0o600); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"add", "README.md"},
		{"commit", "-m", "Add conflicting rebase coverage on main"},
		{"push", "origin", "main"},
	} {
		if _, err := gitOutput(repositoryPath, args...); err != nil {
			return err
		}
	}
	return nil
}

// PrepareNextRebase keeps the manual browser walkthrough repeatable. Once the
// current base is contained in the PR head, advance main with another README
// change so the next request exercises the conflicting rebase path again.
func (scenario *Scenario) PrepareNextRebase() error {
	if scenario == nil || scenario.GitHub == nil || scenario.repositoryPath == "" {
		return nil
	}
	scenario.GitHub.mu.Lock()
	defer scenario.GitHub.mu.Unlock()
	if scenario.GitHub.pullRequest == nil || scenario.GitHub.remotePath == "" {
		return nil
	}
	baseBranch := scenario.GitHub.pullRequest.BaseBranch
	headBranch := scenario.GitHub.pullRequest.HeadBranch
	remotePath := scenario.GitHub.remotePath

	base, head, err := scenario.GitHub.remoteCommits(baseBranch, headBranch)
	if err != nil {
		return err
	}
	mergeBase, err := gitOutput(remotePath, "merge-base", base, head)
	if err != nil {
		return err
	}
	if strings.TrimSpace(mergeBase) != base {
		return nil
	}
	localBase, err := gitOutput(scenario.repositoryPath, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(localBase) != base {
		return errors.New("fixture main branch is out of sync with its remote")
	}
	return advanceConflictingBase(scenario.repositoryPath)
}

func (github *GitHub) remoteCommits(baseBranch, headBranch string) (string, string, error) {
	base, err := gitOutput(github.remotePath, "rev-parse", "refs/heads/"+baseBranch)
	if err != nil {
		return "", "", err
	}
	head, err := gitOutput(github.remotePath, "rev-parse", "refs/heads/"+headBranch)
	return strings.TrimSpace(base), strings.TrimSpace(head), err
}

// Called with github.mu held. Provider polling must never restore the old head
// after the real Git publisher has pushed the resolved branch.
func (github *GitHub) refreshCommits() error {
	if github.remotePath == "" || github.pullRequest == nil {
		return nil
	}
	base, head, err := github.remoteCommits(github.pullRequest.BaseBranch, github.pullRequest.HeadBranch)
	if err != nil {
		return err
	}
	github.baseCommit, github.headCommit = base, head
	github.pullRequest.BaseCommit, github.pullRequest.HeadCommit = base, head
	return nil
}

type rebaseRecord struct {
	holon      holons.Holon
	work       pullrequestwork.Work
	completing bool
}

// RebaseAgents simulates external PR agents for every browser flow, including
// reviews and comment work. Preparation, conflict detection,
// publication, and durable completion use application code.
type RebaseAgents struct {
	mu          sync.Mutex
	application *localapp.Application
	delay       time.Duration
	records     map[string]*rebaseRecord
	ctx         context.Context
	cancel      context.CancelFunc
	closed      bool
	wg          sync.WaitGroup
}

func newRebaseAgents(delay time.Duration) *RebaseAgents {
	ctx, cancel := context.WithCancel(context.Background())
	return &RebaseAgents{delay: delay, records: map[string]*rebaseRecord{}, ctx: ctx, cancel: cancel}
}

func (agents *RebaseAgents) bind(application *localapp.Application) { agents.application = application }

func (agents *RebaseAgents) Start(ctx context.Context, pr pullrequestwork.PullRequest, work pullrequestwork.Work, _ string) (string, error) {
	agents.mu.Lock()
	defer agents.mu.Unlock()
	if agents.closed || agents.application == nil {
		return "", errors.New("fixture PR agent is unavailable")
	}
	title, kind := "Rebase PR", holons.KindRebase
	if work.Kind == pullrequestwork.KindReview {
		title, kind = "Review pull request", holons.KindPullReview
	} else if work.Kind == pullrequestwork.KindWorker {
		title, kind = "Address pull request comments", holons.KindPullWorker
	}
	create := holons.Create{
		Title: title, Kind: kind, AgentType: "codex", PullRequestID: pr.ID,
		Prompt:     fmt.Sprintf("%s. This simulated agent completes with a deterministic result after %s.", title, agents.delay),
		BaseBranch: pr.BaseBranch, BaseCommit: pr.DiffBaseCommit, WorkSessionStartCommit: pr.HeadCommit,
	}
	if work.Kind == pullrequestwork.KindRebase {
		create.AgentTitle = title
	}
	var h holons.Holon
	var err error
	if work.InQueue() && work.SessionID != "" {
		h, err = agents.application.Holons.PrepareReserved(ctx, work.SessionID, create)
	} else {
		h, err = agents.application.Holons.Create(ctx, create)
	}
	if err != nil {
		return "", err
	}
	// Recreate the conflict in the worker's own workspace, as a real agent does
	// after the direct rebase has aborted its temporary worktree.
	if work.Kind == pullrequestwork.KindRebase {
		_, rebaseErr := gitOutputContext(ctx, h.WorktreePath, "-c", "rerere.enabled=false", "rebase", work.TargetBaseCommit)
		unmerged, err := gitOutputContext(ctx, h.WorktreePath, "diff", "--name-only", "--diff-filter=U")
		if err != nil || rebaseErr == nil || strings.TrimSpace(unmerged) != "README.md" {
			err = errors.Join(fmt.Errorf("expected a README.md rebase conflict; unmerged files: %q", strings.TrimSpace(unmerged)), rebaseErr, err)
			_, _ = agents.application.Holons.SetAgentSessionStatus(ctx, h.ID, h.AgentSessions[0].ID, holons.StatusFailed, err.Error(), "")
			return "", err
		}
	}
	if _, err = agents.application.Holons.SetAgentSessionStatus(ctx, h.ID, h.AgentSessions[0].ID, holons.StatusRunning, "", ""); err != nil {
		return "", err
	}
	agents.records[h.ID] = &rebaseRecord{holon: h, work: work}
	if agents.delay > 0 {
		agents.wg.Add(1)
		go func() {
			defer agents.wg.Done()
			timer := time.NewTimer(agents.delay)
			defer timer.Stop()
			select {
			case <-agents.ctx.Done():
				return
			case <-timer.C:
				if err := agents.Complete(agents.ctx, h.ID); err != nil {
					log.Printf("Complete fixture PR work: %v", err)
				}
			}
		}()
	}
	return h.ID, nil
}

// Complete lets integration tests resolve the conflict without a wall-clock delay.
func (agents *RebaseAgents) Complete(ctx context.Context, sessionID string) error {
	agents.mu.Lock()
	record := agents.records[sessionID]
	if agents.closed || record == nil || record.completing {
		agents.mu.Unlock()
		return errors.New("fixture rebase agent is unavailable")
	}
	record.completing = true
	agents.wg.Add(1)
	agents.mu.Unlock()
	defer agents.wg.Done()
	h, work := record.holon, record.work
	var err error
	if work.Kind == pullrequestwork.KindRebase {
		err = agents.resolve(ctx, h, work)
	} else {
		err = agents.completeReviewWork(ctx, h, work)
	}
	status, reason := holons.StatusExpired, ""
	if err != nil {
		status, reason = holons.StatusFailed, err.Error()
		err = errors.Join(err, agents.application.PullRequestWork.Fail(ctx, work.ID, reason))
	}
	_, statusErr := agents.application.Holons.SetAgentSessionStatus(ctx, h.ID, h.AgentSessions[0].ID, status, reason, "")
	return errors.Join(err, statusErr)
}

func (agents *RebaseAgents) resolve(ctx context.Context, h holons.Holon, work pullrequestwork.Work) error {
	if err := os.WriteFile(filepath.Join(h.WorktreePath, "README.md"), []byte(resolvedREADME), 0o600); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"add", "README.md"},
		{"-c", "core.editor=true", "rebase", "--continue"},
	} {
		if _, err := gitOutputContext(ctx, h.WorktreePath, args...); err != nil {
			return err
		}
	}
	head, err := gitOutputContext(ctx, h.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	_, err = agents.application.PullRequestWork.Complete(ctx, work.ID, work.HeadCommit, pullrequestwork.Completion{
		PullRequestID: work.PullRequestID, HeadCommit: work.HeadCommit, ResultHeadCommit: strings.TrimSpace(head),
		ReplyBody: "Resolved the README conflict by preserving PR creation and conflicting rebase coverage.",
	})
	return err
}

func (agents *RebaseAgents) close() {
	agents.mu.Lock()
	agents.closed = true
	agents.cancel()
	agents.mu.Unlock()
	agents.wg.Wait()
}

func (agents *RebaseAgents) Reserve(ctx context.Context, pr pullrequestwork.PullRequest, work pullrequestwork.Work) error {
	agents.mu.Lock()
	defer agents.mu.Unlock()
	if agents.closed || agents.application == nil {
		return errors.New("fixture PR agent is unavailable")
	}
	title, kind := "Address pull request comments", holons.KindPullWorker
	if work.Kind == pullrequestwork.KindRebase {
		title, kind = "Rebase PR", holons.KindRebase
	}
	h, err := agents.application.Holons.Reserve(ctx, work.SessionID, holons.Create{Title: title, Kind: kind, PullRequestID: pr.ID})
	if err != nil {
		return err
	}
	if h.EndRequested || holons.IsTerminal(h.Status) || h.Status == holons.StatusCancelling {
		return pullrequestwork.ErrReservationEnded
	}
	return nil
}
func (agents *RebaseAgents) StartReserved(ctx context.Context, pr pullrequestwork.PullRequest, work pullrequestwork.Work, prompt string) (error, error) {
	_, err := agents.Start(ctx, pr, work, prompt)
	if errors.Is(err, holons.ErrSetupPersistence) {
		return nil, err
	}
	if errors.Is(err, holons.ErrReservationEnded) {
		return pullrequestwork.ErrReservationEnded, nil
	}
	return err, nil
}
func (agents *RebaseAgents) SettleReserved(ctx context.Context, work pullrequestwork.Work) error {
	status := holons.StatusFailed
	if work.Status == pullrequestwork.StatusSkipped {
		status = holons.StatusCompleted
	}
	if work.Status == pullrequestwork.StatusCancelled {
		status = holons.StatusCancelled
	}
	return agents.application.Holons.SettleReserved(ctx, work.SessionID, status, work.Error)
}
