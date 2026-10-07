// Package pullrequestfixture runs the real local application with controlled
// external PR boundaries, for integration tests and the manual browser scenario.
package pullrequestfixture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/localapp"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
)

type State struct {
	HolonID      string
	WorktreePath string
}

type Scenario struct {
	Application    *localapp.Application
	State          State
	Agents         *MetadataAgents
	GitHub         *GitHub
	Instructions   string
	RebaseAgents   *RebaseAgents
	repositoryPath string
}

type BrowserFlow string

const (
	BrowserFlowActionPriority         BrowserFlow = "action-priority"
	BrowserFlowHappyPath              BrowserFlow = "happy-path"
	BrowserFlowOutdatedDescription    BrowserFlow = "outdated-description"
	BrowserFlowOutdatedReview         BrowserFlow = "outdated-review"
	BrowserFlowRebaseClickConflict    BrowserFlow = "rebase-click-conflict"
	BrowserFlowRegenerationFailure    BrowserFlow = "regeneration-failure"
	BrowserFlowPublicationFailure     BrowserFlow = "publication-failure"
	BrowserFlowDescriptionSaveFailure BrowserFlow = "description-save-failure"
)

func (flow BrowserFlow) Valid() bool {
	switch flow {
	case BrowserFlowActionPriority, BrowserFlowHappyPath, BrowserFlowOutdatedDescription, BrowserFlowOutdatedReview, BrowserFlowRebaseClickConflict, BrowserFlowRegenerationFailure,
		BrowserFlowPublicationFailure, BrowserFlowDescriptionSaveFailure:
		return true
	default:
		return false
	}
}

type BrowserOptions struct {
	CompletionDelay time.Duration
	Flow            BrowserFlow
	State           State
}

// New creates a repository, SQLite database, and source Holon under root.
// The caller owns root and removes it after Close. Use a fresh root per test.
// A positive completionDelay enables the manual scenario's automatic completion;
// zero requires an explicit Agents.Complete call.
func New(ctx context.Context, root string, completionDelay time.Duration) (*Scenario, error) {
	return newScenario(ctx, root, completionDelay, false, true, false)
}

// NewBrowserScenario adds independent Holons before the source Holon. The happy
// path also adds five review commits and a conflicting main branch for the
// rebase walkthrough.
func NewBrowserScenario(ctx context.Context, root string, options BrowserOptions) (*Scenario, error) {
	if !options.Flow.Valid() {
		return nil, fmt.Errorf("unknown browser pull request flow %q", options.Flow)
	}
	initialConflictingRebase := options.Flow == BrowserFlowHappyPath || options.Flow == BrowserFlowActionPriority
	rebaseChanges := initialConflictingRebase || options.Flow == BrowserFlowRebaseClickConflict
	// Every browser flow supports publication, reviews, comment work, and rebases.
	// Individual flows only customize the starting state and injected failures.
	scenario, err := newScenario(ctx, root, options.CompletionDelay, true, true, rebaseChanges)
	if err != nil {
		return nil, err
	}
	if initialConflictingRebase {
		err = advanceConflictingBase(scenario.repositoryPath)
	} else if options.Flow == BrowserFlowRebaseClickConflict {
		err = advanceCleanBase(scenario.repositoryPath)
	}
	if err != nil {
		_ = scenario.Close()
		return nil, err
	}
	if err = configureBrowserFlow(scenario, options.Flow); err != nil {
		_ = scenario.Close()
		return nil, err
	}
	return scenario, nil
}

// OpenBrowserScenario reopens the manual scenario and restores its configured
// fake boundary behavior without reseeding repository or application data.
func OpenBrowserScenario(ctx context.Context, root string, options BrowserOptions) (*Scenario, error) {
	if !options.Flow.Valid() {
		return nil, fmt.Errorf("unknown browser pull request flow %q", options.Flow)
	}
	scenario, err := Open(ctx, root, options.CompletionDelay)
	if err != nil {
		return nil, err
	}
	scenario.State = options.State
	if err = configureBrowserFlow(scenario, options.Flow); err != nil {
		_ = scenario.Close()
		return nil, err
	}
	return scenario, nil
}

func newScenario(ctx context.Context, root string, completionDelay time.Duration, backgroundHolons, withRemote, conflictingRebase bool) (*Scenario, error) {
	repositoryPath := filepath.Join(root, "repository")
	if err := os.MkdirAll(repositoryPath, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(repositoryPath, "README.md"), []byte("Pull request scenario\n"), 0o600); err != nil {
		return nil, err
	}
	if conflictingRebase {
		if err := seedHappyPathBase(repositoryPath); err != nil {
			return nil, err
		}
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"add", "."},
		{"-c", "user.name=Holark Scenario", "-c", "user.email=scenario@invalid", "commit", "-m", "Initial commit"},
	} {
		if _, err := gitOutput(repositoryPath, args...); err != nil {
			return nil, err
		}
	}
	if withRemote {
		if err := prepareRebaseRemote(root, repositoryPath); err != nil {
			return nil, err
		}
	}
	scenario, err := Open(ctx, root, completionDelay)
	if err != nil {
		return nil, err
	}
	if backgroundHolons {
		if err = seedBackgroundHolons(ctx, scenario.Application, repositoryPath); err != nil {
			_ = scenario.Close()
			return nil, err
		}
	}
	scenario.State, err = seed(ctx, scenario.Application, repositoryPath, scenario.GitHub, conflictingRebase, backgroundHolons)
	if err != nil {
		_ = scenario.Close()
		return nil, err
	}
	return scenario, nil
}

// Open reopens an existing local repository and database without seeding them.
// External fake state is fresh; running agent sessions are not restored.
func Open(ctx context.Context, root string, completionDelay time.Duration) (*Scenario, error) {
	repositoryPath := filepath.Join(root, "repository")
	scenario := &Scenario{Agents: newMetadataAgents(completionDelay), GitHub: &GitHub{}, repositoryPath: repositoryPath}
	options := localapp.Options{
		RepositoryPath: repositoryPath, HomeDirectory: filepath.Join(root, "app"),
		PullRequestBranchPublisher:  noOpBranchPublisher{},
		PullRequestGitHubTransport:  scenario.GitHub,
		PullRequestMergeProvider:    scenario.GitHub,
		PullRequestMetadataAgents:   scenario.Agents,
		PullRequestMetadataProvider: scenarioMetadataProvider{github: scenario.GitHub},
		PullRequestRepositoryURL:    RepositoryURL,
	}
	remotePath := filepath.Join(root, "remote.git")
	if _, err := os.Stat(remotePath); err == nil {
		noGitHubBinding := ""
		options.GitHubRepositoryURL = &noGitHubBinding
		options.PullRequestBranchPublisher = nil
		scenario.GitHub.remotePath = remotePath
		scenario.RebaseAgents = newRebaseAgents(completionDelay)
		options.PullRequestWorkLauncher = scenario.RebaseAgents
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// Keep canonical provider identities while all Git transport stays inside
	// the fixture's real repository or bare remote.
	gitSource := repositoryPath
	if scenario.GitHub.remotePath != "" {
		gitSource = scenario.GitHub.remotePath
	}
	_, _ = gitOutputContext(ctx, repositoryPath, "config", "--local", "--unset-all", "url."+repositoryPath+".insteadOf", RepositoryURL)
	if _, err := gitOutputContext(ctx, repositoryPath, "config", "--local", "url."+gitSource+".insteadOf", RepositoryURL); err != nil {
		return nil, err
	}
	application, err := localapp.New(ctx, options)
	if err != nil {
		scenario.Agents.close()
		if scenario.RebaseAgents != nil {
			scenario.RebaseAgents.close()
		}
		return nil, err
	}
	scenario.Application = application
	scenario.Agents.bind(application.Holons)
	if scenario.RebaseAgents != nil {
		scenario.RebaseAgents.bind(application)
	}
	return scenario, nil
}

func (scenario *Scenario) Close() error {
	scenario.Agents.close()
	if scenario.RebaseAgents != nil {
		scenario.RebaseAgents.close()
	}
	return scenario.Application.Close()
}

func configureBrowserFlow(scenario *Scenario, flow BrowserFlow) error {
	description := browserDescription(flow)
	scenario.Instructions = browserInstructions(flow)
	success := func() metadataAgentOutcome {
		return metadataAgentOutcome{title: GeneratedTitle, description: description}
	}
	switch flow {
	case BrowserFlowActionPriority:
		scenario.Agents.queue(success())
		scenario.Instructions = "Action priority: create an Open PR, resolve the rebase, then run review (three comments), then resolve all. Each simulated agent takes five seconds. Resolution commits new changes, so Run review returns. Rebase can be repeated; review before rebase to inspect muted actions."
	case BrowserFlowHappyPath:
		scenario.Application.Handler = scenario.reviewHandler(scenario.Application.Handler)
		scenario.Agents.queue(success())
	case BrowserFlowOutdatedDescription:
		initial := success()
		initial.afterStart = func(request pullrequestmetadata.StartAgentSessionRequest) {
			if err := scenario.advancePullRequestAfterInput(request); err != nil {
				log.Printf("Prepare outdated-description flow: %v", err)
			}
		}
		scenario.Agents.queue(initial, success())
	case BrowserFlowOutdatedReview:
		scenario.Application.Handler = scenario.outdatedReviewHandler(scenario.Application.Handler)
		scenario.Agents.queue(success())
	case BrowserFlowRebaseClickConflict:
		scenario.Application.Handler = scenario.rebaseClickConflictHandler(scenario.Application.Handler)
		scenario.Agents.queue(success())
	case BrowserFlowRegenerationFailure:
		scenario.Agents.queue(success(), metadataAgentOutcome{failure: "Metadata regeneration failed once for this scenario."}, success())
	case BrowserFlowPublicationFailure:
		scenario.Agents.queue(success())
		scenario.GitHub.QueueCreateError(errors.New("simulated one-shot GitHub creation failure"))
	case BrowserFlowDescriptionSaveFailure:
		if _, err := scenario.Application.Database.ExecContext(context.Background(), `create temp trigger reject_metadata before update of description on pull_requests begin select raise(fail, 'simulated description save failure'); end`); err != nil {
			return fmt.Errorf("install description save failure: %w", err)
		}
		outcome := success()
		outcome.afterCompletion = func(pullRequestID string) {
			if err := scenario.removeSaveFailureAfterExposure(pullRequestID); err != nil {
				log.Printf("Prepare description-save-failure retry: %v", err)
			}
		}
		scenario.Agents.queue(outcome)
	default:
		return fmt.Errorf("unknown browser pull request flow %q", flow)
	}
	return nil
}

func (scenario *Scenario) advancePullRequestAfterInput(request pullrequestmetadata.StartAgentSessionRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := waitForMetadataAttempt(ctx, scenario.Application, request.PullRequestID); err != nil {
		return err
	}
	path := scenario.State.WorktreePath
	if path == "" {
		return errors.New("source worktree is unavailable")
	}
	if err := os.WriteFile(filepath.Join(path, "scenario.txt"), []byte("pull request creation browser scenario\nsubstantive change made after generation started\n"), 0o600); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"add", "scenario.txt"},
		{"-c", "user.name=Holark Browser Scenario", "-c", "user.email=browser-scenario@invalid", "commit", "-m", "Change behavior after description generation starts"},
	} {
		if _, err := gitOutput(path, args...); err != nil {
			return err
		}
	}
	headCommit, err := gitOutput(path, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if scenario.GitHub.remotePath != "" {
		if _, err := gitOutput(path, "push", "origin", "HEAD"); err != nil {
			return err
		}
	}
	scenario.GitHub.SetCommits(request.BaseCommit, strings.TrimSpace(headCommit))
	response := httptest.NewRecorder()
	scenario.Application.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/sync", nil))
	if response.Code != http.StatusOK {
		return fmt.Errorf("sync pull request: status %d: %s", response.Code, strings.TrimSpace(response.Body.String()))
	}
	return nil
}

func waitForMetadataAttempt(ctx context.Context, application *localapp.Application, pullRequestID string) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state string
		err := application.Database.QueryRowContext(ctx, `select state from pull_request_metadata where pull_request_id = ?`, pullRequestID).Scan(&state)
		if err == nil {
			var decoded struct {
				Attempt json.RawMessage `json:"attempt"`
			}
			if json.Unmarshal([]byte(state), &decoded) == nil && len(decoded.Attempt) > 0 && string(decoded.Attempt) != "null" {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for captured metadata input: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (scenario *Scenario) removeSaveFailureAfterExposure(pullRequestID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		response := httptest.NewRecorder()
		scenario.Application.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/pull-requests/"+pullRequestID+"/metadata", nil).WithContext(ctx))
		if response.Code == http.StatusOK {
			var metadata pullrequestmetadata.Metadata
			if json.Unmarshal(response.Body.Bytes(), &metadata) == nil && metadata.ApplicationError != nil && metadata.ApplicationError.Operation == "save" {
				_, err := scenario.Application.Database.ExecContext(ctx, "drop trigger reject_metadata")
				return err
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for exposed description save failure: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func gitOutput(directory string, args ...string) (string, error) {
	return gitOutputContext(context.Background(), directory, args...)
}

func gitOutputContext(ctx context.Context, directory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %w: %s", args, err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func browserDescription(flow BrowserFlow) string {
	if flow == BrowserFlowActionPriority {
		return "Manual action-priority scenario. Rebase → Run review → Resolve all automatically. Every simulated agent runs for five seconds. Reviews add three comments; resolution commits a deterministic change and resolves its comment, making another review necessary. Rebase again to repeat the cycle."
	}
	if flow == BrowserFlowHappyPath {
		return strings.Replace(GeneratedDescription,
			"- Start from a realistic repository with a source Holon and a committed change.",
			"- Review five commits spanning a structured checklist, JavaScript, CSS, and documentation, with roughly 250 additions and 100 deletions.\n- Opening the PR seeds an assignee, a requested reviewer, and five comments including replies and resolved/unresolved conversations. Refreshing or restarting preserves review edits.", 1)
	}
	walkthrough := map[BrowserFlow]string{
		BrowserFlowOutdatedDescription: `1. Create an **Open** pull request and keep Overview visible while generation runs.
2. After generation and publication finish, inspect the **Description may be outdated** warning.
3. Open both generated/current commit-range links, then return to the pull request.
4. Click **Regenerate**. The description and warning should remain visible while it runs.
5. When regeneration finishes, confirm that the warning clears and the branch did not advance again.`,
		BrowserFlowOutdatedReview: `1. Create an **Open** pull request and wait for generation and publication to finish.
2. Click **Run review**. The fixture advances the simulated remote immediately before Holark starts the review, then synchronizes the new head.
3. Confirm that the running review changes from current to **Review may be outdated** while retaining its original reviewed range.
4. Wait for the simulated review to complete and inspect the warning, summary, comments, and review Holon together.
5. Click **Run review** again to review the current range; the simulated remote advances only once, so this review remains current.`,
		BrowserFlowRebaseClickConflict: `1. Create an **Open** pull request and wait for generation and publication to finish.
2. Confirm that Review & merge shows the clean mechanical **Rebase onto 12345678**-style action naming the previewed commit.
3. Click the rebase action. The fixture holds the click, refreshes the clean preview, waits exactly one second, then releases the click and pushes a conflicting commit to main immediately before the production rebase handler refreshes its refs.
4. Confirm that the action names the exact commit while rebasing, changes to **Analyzing the latest main commit…** when main moves, creates no Rebase Holon, and becomes **Resolve automatically** only after readiness finds the conflict.
5. Inspect Operations to confirm that only the durable failed mechanical attempt was recorded, without an agent session.`,
		BrowserFlowRegenerationFailure: `1. Create an **Open** pull request and wait for initial generation and publication.
2. Click **Regenerate**; this turn fails once.
3. Confirm that the saved description remains visible with the failure message.
4. Click **Retry** and wait for the successful replacement turn.`,
		BrowserFlowPublicationFailure: `1. Create an **Open** pull request and wait for generation to finish.
2. Confirm that the generated description remains visible when simulated GitHub creation fails.
3. Click **Retry publication** and confirm that publication succeeds without another generation Holon.`,
		BrowserFlowDescriptionSaveFailure: `1. Create an **Open** pull request and wait for generation to finish.
2. Confirm that the application reports the one-shot description save failure.
3. Click **Retry saving** and confirm that the retained output is saved and published without another generation Holon.`,
	}[flow]
	return fmt.Sprintf(`This pull request is a deterministic browser fixture for inspecting generated-description recovery behavior. It uses the real application, SQLite catalog, seeded Git repository, background Holons, and simulated external boundaries.

## Manual walkthrough

%s

## Review checklist

- [ ] Generation stays visible for roughly ten seconds so Overview, Commits, Changes, and Operations can be inspected.
- [ ] Existing generated text is preserved while a later operation is running or failed.
- [ ] The labeled retry action resumes at the failed boundary without repeating completed work.

> This scenario publishes only through a simulated GitHub transport and does not create a remote pull request.
`, walkthrough)
}

func browserInstructions(flow BrowserFlow) string {
	return map[BrowserFlow]string{
		BrowserFlowHappyPath:              "Happy-path PR fixture: generation succeeds, the description is current, and publication completes normally using simulated GitHub.",
		BrowserFlowOutdatedDescription:    "Outdated-description PR fixture: generation succeeds, but the pull request changes while generation is running, so the generated description is marked as out of date.",
		BrowserFlowOutdatedReview:         "Outdated-review PR fixture: the simulated remote advances just before the first review starts, then synchronization exposes the review's pinned range as stale. Run review again to compare the current-state UI.",
		BrowserFlowRebaseClickConflict:    "Click-time rebase conflict fixture: clicking the clean mechanical action refreshes its preview, holds the click for exactly one second, then lands a conflicting main commit as production handles the released click.",
		BrowserFlowRegenerationFailure:    "Regeneration-failure PR fixture: initial generation succeeds, but the first regeneration attempt fails; retrying succeeds while preserving the existing description.",
		BrowserFlowPublicationFailure:     "Publication-failure PR fixture: generation succeeds, but the first simulated GitHub publication attempt fails; retrying publishes the saved description without regenerating it.",
		BrowserFlowDescriptionSaveFailure: "Description-save-failure PR fixture: generation succeeds, but applying the result to the local pull request fails once; retrying uses the retained result and publishes it without regenerating.",
	}[flow]
}
