package pullrequestfixture

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/localapp"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
)

const RepositoryURL = "https://github.com/holark-fixture/browser-scenario"

const GeneratedTitle = "Generated browser PR"
const GeneratedDescription = `This pull request adds a persistent browser scenario for inspecting the complete pull request creation flow, from a source Holon's committed changes through description generation and publication. It gives reviewers enough time to explore the page while the generation Holon works, then read and edit the resulting Markdown without leaving the overview.

## What changed

- Start from a realistic repository with a source Holon and a committed change.
- Show the **Generate PR description and title** Holon while the description is being prepared.
- Keep **Overview**, **Commits**, and **Changes** available throughout generation.
- Replace the generation tile with this description when publication finishes.
- Allow the description to be edited in place with Save and Cancel.

## Manual walkthrough

1. Create a pull request from the source Holon's changes.
2. Hover over the generation tile and check that Operations scrolls to its row.
3. Browse the commits and changes during the ten-second generation period.
4. Return to Overview and use the pen button to edit this description.
5. Save a Markdown change, then revisit the page to check that it persists.
6. Click **Rebase**. In the manual browser scenario, main and the PR change the same README line.
7. Inspect the running **Rebase PR** Holon and its conflicting README change. The simulated agent resolves it after ten seconds.
8. Return to the PR and check the completed Rebase entry, updated commits, and changes against the new base. The resolved README includes both changes.
9. Click **Rebase** again to create and resolve another conflicting base commit. Repeat as often as needed.

## Review checklist

- [ ] The tile shows the Holon's name and running state without a temporary Idle label.
- [ ] Hovering a tile scrolls Operations without moving the pull request page.
- [ ] Long opening paragraphs wrap beside the edit button.
- [ ] Cancelling an edit keeps the saved description intact.

> This scenario uses a local repository and simulated publication, so the walkthrough can be repeated without creating a remote pull request.
`

type noOpBranchPublisher struct{}

func (noOpBranchPublisher) Publish(context.Context, string, string) error { return nil }

type scenarioMetadataProvider struct{ github *GitHub }

func (provider scenarioMetadataProvider) Update(_ context.Context, snapshot pullrequestmetadata.Snapshot, title, description string) (time.Time, error) {
	if provider.github == nil {
		return time.Time{}, nil
	}
	provider.github.mu.Lock()
	defer provider.github.mu.Unlock()
	current := provider.github.pullRequest
	if current != nil && current.ExternalID == snapshot.SyncExternalID {
		current.Title = title
		current.Summary = description
		current.UpdatedAt = time.Now().UTC()
		return current.UpdatedAt, nil
	}
	return time.Time{}, nil
}

type scenarioMetadataRecord struct {
	pullRequestID string
	agentID       string
	status        string
	error         string
	timer         *time.Timer
	data          []byte
}

type metadataAgentOutcome struct {
	title, description string
	failure            string
	afterStart         func(pullrequestmetadata.StartAgentSessionRequest)
	afterCompletion    func(string)
}

type MetadataAgents struct {
	mu                  sync.Mutex
	holons              *holons.Service
	duration            time.Duration
	closed              bool
	startError          error
	records             map[string]*scenarioMetadataRecord
	outcomes            []metadataAgentOutcome
	artifactReadGate    <-chan struct{}
	artifactReadStarted chan struct{}
}

func newMetadataAgents(duration time.Duration) *MetadataAgents {
	return &MetadataAgents{duration: duration, records: map[string]*scenarioMetadataRecord{}}
}

func (agents *MetadataAgents) bind(service *holons.Service) {
	agents.mu.Lock()
	defer agents.mu.Unlock()
	agents.holons = service
}

func (agents *MetadataAgents) Start(ctx context.Context, request pullrequestmetadata.StartAgentSessionRequest) (pullrequestmetadata.AgentSession, error) {
	agents.mu.Lock()
	if agents.startError != nil {
		agents.mu.Unlock()
		return pullrequestmetadata.AgentSession{}, agents.startError
	}
	if agents.closed || agents.holons == nil {
		agents.mu.Unlock()
		return pullrequestmetadata.AgentSession{}, pullrequestmetadata.ErrAgentUnavailable
	}
	baseCommit := request.HeadCommit
	if baseCommit == "" {
		baseCommit = request.BaseCommit
	}
	holon, err := agents.holons.Create(ctx, holons.Create{
		Title: "Generate PR description and title", Prompt: request.Prompt, Kind: holons.KindPRMetadata,
		BaseBranch: request.HeadBranch, BaseCommit: baseCommit, PullRequestID: request.PullRequestID, AgentType: "codex",
	})
	if err != nil {
		agents.mu.Unlock()
		return pullrequestmetadata.AgentSession{}, err
	}
	agentID := holon.AgentSessions[0].ID
	if _, err = agents.holons.SetAgentSessionStatus(ctx, holon.ID, agentID, holons.StatusRunning, "", ""); err != nil {
		agents.mu.Unlock()
		return pullrequestmetadata.AgentSession{}, err
	}
	record := &scenarioMetadataRecord{pullRequestID: request.PullRequestID, agentID: agentID, status: string(holons.StatusRunning)}
	agents.records[holon.ID] = record
	outcome := agents.nextOutcome()
	agents.schedule(holon.ID, record, outcome)
	session := scenarioMetadataSession(holon.ID, record)
	agents.mu.Unlock()
	if outcome.afterStart != nil {
		go outcome.afterStart(request)
	}
	return session, nil
}

func (agents *MetadataAgents) Resume(ctx context.Context, request pullrequestmetadata.ResumeAgentSessionRequest) (pullrequestmetadata.AgentSession, error) {
	agents.mu.Lock()
	record := agents.records[request.SessionID]
	if agents.closed || agents.holons == nil || record == nil || record.pullRequestID != request.PullRequestID {
		agents.mu.Unlock()
		return pullrequestmetadata.AgentSession{}, pullrequestmetadata.ErrAgentSessionUnavailable
	}
	holon, err := agents.holons.AddAgentSession(ctx, request.SessionID, "codex", request.Prompt)
	if err != nil {
		agents.mu.Unlock()
		return pullrequestmetadata.AgentSession{}, err
	}
	if record.timer != nil {
		record.timer.Stop()
	}
	record.agentID = holon.AgentSessions[len(holon.AgentSessions)-1].ID
	record.status = string(holons.StatusRunning)
	record.error = ""
	record.data = nil
	if _, err = agents.holons.SetAgentSessionStatus(ctx, request.SessionID, record.agentID, holons.StatusRunning, "", ""); err != nil {
		agents.mu.Unlock()
		return pullrequestmetadata.AgentSession{}, err
	}
	outcome := agents.nextOutcome()
	agents.schedule(request.SessionID, record, outcome)
	session := scenarioMetadataSession(request.SessionID, record)
	agents.mu.Unlock()
	return session, nil
}

func (agents *MetadataAgents) Inspect(_ context.Context, id string) (pullrequestmetadata.AgentSession, error) {
	agents.mu.Lock()
	defer agents.mu.Unlock()
	record := agents.records[id]
	if agents.closed || record == nil {
		return pullrequestmetadata.AgentSession{}, pullrequestmetadata.ErrAgentSessionUnavailable
	}
	return scenarioMetadataSession(id, record), nil
}

func (agents *MetadataAgents) ConsumeArtifact(ctx context.Context, id string) (pullrequestmetadata.Artifact, error) {
	agents.mu.Lock()
	gate, started := agents.artifactReadGate, agents.artifactReadStarted
	if started != nil {
		close(started)
		agents.artifactReadStarted = nil
	}
	agents.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return pullrequestmetadata.Artifact{}, ctx.Err()
		}
	}
	agents.mu.Lock()
	defer agents.mu.Unlock()
	record := agents.records[id]
	if agents.closed || record == nil || record.status != string(holons.StatusCompleted) || record.data == nil {
		return pullrequestmetadata.Artifact{}, pullrequestmetadata.ErrArtifactInvalid
	}
	artifact := pullrequestmetadata.Artifact{
		Path: pullrequestmetadata.MetadataArtifactPath, Data: record.data, OwnerPullRequestID: record.pullRequestID,
	}
	// Match the local adapter, which removes the file when it is read.
	record.data = nil
	return artifact, nil
}

// PauseArtifactRead lets a test inspect the API after Holon completion but
// before the application can consume and apply its output.
func (agents *MetadataAgents) PauseArtifactRead() (<-chan struct{}, func()) {
	agents.mu.Lock()
	defer agents.mu.Unlock()
	gate, started := make(chan struct{}), make(chan struct{})
	agents.artifactReadGate, agents.artifactReadStarted = gate, started
	return started, sync.OnceFunc(func() { close(gate) })
}

func (agents *MetadataAgents) SetTurnError(ctx context.Context, id, message string) error {
	agents.mu.Lock()
	defer agents.mu.Unlock()
	record := agents.records[id]
	if agents.closed || agents.holons == nil || record == nil {
		return pullrequestmetadata.ErrAgentSessionUnavailable
	}
	record.status, record.error = string(holons.StatusFailed), message
	_, err := agents.holons.SetAgentSessionStatus(ctx, id, record.agentID, holons.StatusFailed, message, "")
	return err
}

func (agents *MetadataAgents) ClearTurnError(ctx context.Context, id string) error {
	agents.mu.Lock()
	defer agents.mu.Unlock()
	record := agents.records[id]
	if agents.closed || agents.holons == nil || record == nil {
		return pullrequestmetadata.ErrAgentSessionUnavailable
	}
	// Real metadata completion expires the Holon after importing its output.
	// The fixture uses background recovery instead of an agent completion event.
	if _, err := agents.holons.UpdateAgentObservation(ctx, id, record.agentID, "", "", "", "task_complete", "", "", protocol.ActivityCompleted, nil); err != nil {
		return err
	}
	if _, err := agents.holons.SetAgentSessionStatus(ctx, id, record.agentID, holons.StatusExpired, "", ""); err != nil {
		return err
	}
	record.status, record.error = string(holons.StatusExpired), ""
	return nil
}

func (agents *MetadataAgents) Retire(context.Context, string) error { return nil }

// queue configures one-shot results for successive turns. Once exhausted,
// later turns use the standard successful artifact.
func (agents *MetadataAgents) queue(outcomes ...metadataAgentOutcome) {
	agents.mu.Lock()
	defer agents.mu.Unlock()
	agents.outcomes = append(agents.outcomes, outcomes...)
}

func (agents *MetadataAgents) nextOutcome() metadataAgentOutcome {
	if len(agents.outcomes) == 0 {
		return metadataAgentOutcome{title: GeneratedTitle, description: GeneratedDescription}
	}
	outcome := agents.outcomes[0]
	agents.outcomes = agents.outcomes[1:]
	return outcome
}

// A zero delay leaves completion entirely under the test's control. Timer
// callbacks acquire the mutex through Complete or SetTurnError rather than
// running browser orchestration while the mutex is held.
func (agents *MetadataAgents) schedule(id string, record *scenarioMetadataRecord, outcome metadataAgentOutcome) {
	if agents.duration > 0 {
		record.timer = time.AfterFunc(agents.duration, func() {
			if outcome.failure != "" {
				_ = agents.SetTurnError(context.Background(), id, outcome.failure)
			} else {
				_ = agents.Complete(context.Background(), id, outcome.title, outcome.description)
			}
			if outcome.afterCompletion != nil {
				outcome.afterCompletion(record.pullRequestID)
			}
		})
	}
}

func (agents *MetadataAgents) SetStartError(err error) {
	agents.mu.Lock()
	defer agents.mu.Unlock()
	agents.startError = err
}

// Complete supplies agent output and marks the real Holon completed. The normal
// application background loop must import it and perform any PR transition.
func (agents *MetadataAgents) Complete(ctx context.Context, id, title, description string) error {
	agents.mu.Lock()
	defer agents.mu.Unlock()
	record := agents.records[id]
	if agents.closed || agents.holons == nil || record == nil || record.status != string(holons.StatusRunning) {
		return pullrequestmetadata.ErrAgentSessionUnavailable
	}
	record.data, _ = json.Marshal(map[string]string{"title": title, "description": description})
	record.status = string(holons.StatusCompleted)
	if _, err := agents.holons.SetAgentSessionStatus(ctx, id, record.agentID, holons.StatusCompleted, "", ""); err != nil {
		record.status, record.error = string(holons.StatusFailed), err.Error()
		return err
	}
	return nil
}

func (agents *MetadataAgents) close() {
	agents.mu.Lock()
	defer agents.mu.Unlock()
	agents.closed = true
	for _, record := range agents.records {
		if record.timer != nil {
			record.timer.Stop()
		}
	}
}

func scenarioMetadataSession(id string, record *scenarioMetadataRecord) pullrequestmetadata.AgentSession {
	inputState := "none"
	if record.status == string(holons.StatusCompleted) || record.status == string(holons.StatusExpired) {
		inputState = "task_complete"
	}
	return pullrequestmetadata.AgentSession{ID: id, RuntimeID: "browser-scenario", Status: record.status, InputState: inputState, Error: record.error}
}

type GitHub struct {
	draft                  bool
	mu                     sync.Mutex
	baseCommit, headCommit string
	pullRequest            *pullrequestlifecycle.GitHubPullRequest
	requests               []pullrequestlifecycle.GitHubCreateRequest
	createError            error
	readyError             error
	createErrors           []error
	remotePath             string
}

func (github *GitHub) SetCreateError(err error) {
	github.mu.Lock()
	defer github.mu.Unlock()
	github.createError = err
}

func (github *GitHub) SetReadyError(err error) {
	github.mu.Lock()
	defer github.mu.Unlock()
	github.readyError = err
}

// QueueCreateError fails one creation attempt and then restores the normal
// successful behavior for the next retry.
func (github *GitHub) QueueCreateError(err error) {
	github.mu.Lock()
	defer github.mu.Unlock()
	github.createErrors = append(github.createErrors, err)
}

// CreateRequests records calls made by production code, including failed attempts.
func (github *GitHub) CreateRequests() []pullrequestlifecycle.GitHubCreateRequest {
	github.mu.Lock()
	defer github.mu.Unlock()
	return append([]pullrequestlifecycle.GitHubCreateRequest(nil), github.requests...)
}

func (github *GitHub) SetRepositoryURLs(head, base string) {
	github.mu.Lock()
	defer github.mu.Unlock()
	if github.pullRequest == nil {
		return
	}
	var data map[string]map[string]any
	_ = json.Unmarshal(github.pullRequest.SyncData, &data)
	data["github"]["head_repository_url"], data["github"]["base_repository_url"] = head, base
	github.pullRequest.SyncData, _ = json.Marshal(data)
	github.pullRequest.HeadRepositoryURL, github.pullRequest.BaseRepositoryURL = head, base
}

func (github *GitHub) SetCommits(baseCommit, headCommit string) {
	github.mu.Lock()
	defer github.mu.Unlock()
	github.baseCommit, github.headCommit = baseCommit, headCommit
}

func (github *GitHub) Create(_ context.Context, request pullrequestlifecycle.GitHubCreateRequest) (pullrequestlifecycle.GitHubPullRequest, error) {
	github.mu.Lock()
	defer github.mu.Unlock()
	github.requests = append(github.requests, request)
	if len(github.createErrors) > 0 {
		err := github.createErrors[0]
		github.createErrors = github.createErrors[1:]
		if err != nil {
			return pullrequestlifecycle.GitHubPullRequest{}, err
		}
	}
	if github.createError != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, github.createError
	}
	if github.remotePath != "" {
		base, head, err := github.remoteCommits(request.Base, request.Head)
		if err != nil {
			return pullrequestlifecycle.GitHubPullRequest{}, err
		}
		github.baseCommit, github.headCommit = base, head
	}
	status := pullrequestlifecycle.StatusOpen
	if request.Draft {
		status = pullrequestlifecycle.StatusDraft
	}
	now := time.Now().UTC()
	syncData, _ := json.Marshal(map[string]any{"github": map[string]any{
		"owner": "holark-fixture", "repo": "browser-scenario", "number": 101,
		"url": "https://example.invalid/holark-fixture/browser-scenario/pull/101", "node_id": "PR_browser_scenario_101",
		"draft": request.Draft, "state": "open", "merged": false,
		"head_repository_url": request.RepositoryURL, "base_repository_url": request.RepositoryURL,
	}})
	created := pullrequestlifecycle.GitHubPullRequest{
		HeadRepositoryURL: request.RepositoryURL, BaseRepositoryURL: request.RepositoryURL,
		Title: request.Title, Summary: request.Body, BaseBranch: request.Base, BaseCommit: github.baseCommit,
		HeadBranch: request.Head, HeadCommit: github.headCommit, Status: status,
		ExternalID: "github:holark-fixture/browser-scenario#101", SyncData: syncData, CreatedAt: now, UpdatedAt: now,
	}
	github.draft = request.Draft
	github.pullRequest = &created
	return *github.pullRequest, nil
}

func (github *GitHub) List(context.Context, string) ([]pullrequestlifecycle.GitHubPullRequest, error) {
	github.mu.Lock()
	defer github.mu.Unlock()
	if github.pullRequest == nil {
		return []pullrequestlifecycle.GitHubPullRequest{}, nil
	}
	if err := github.refreshCommits(); err != nil {
		return nil, err
	}
	return []pullrequestlifecycle.GitHubPullRequest{*github.pullRequest}, nil
}

func (github *GitHub) Get(context.Context, pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubPullRequest, error) {
	github.mu.Lock()
	defer github.mu.Unlock()
	if github.pullRequest == nil {
		return pullrequestlifecycle.GitHubPullRequest{}, errors.New("fixture pull request has not been published")
	}
	if err := github.refreshCommits(); err != nil {
		return pullrequestlifecycle.GitHubPullRequest{}, err
	}
	return *github.pullRequest, nil
}

func (github *GitHub) UpdateState(_ context.Context, _ pullrequestlifecycle.GitHubPullRequestTarget, state string) (pullrequestlifecycle.GitHubPullRequest, error) {
	github.mu.Lock()
	defer github.mu.Unlock()
	if github.pullRequest == nil {
		return pullrequestlifecycle.GitHubPullRequest{}, errors.New("fixture pull request has not been published")
	}
	github.pullRequest.Status = pullrequestlifecycle.StatusOpen
	github.pullRequest.ClosedAt = nil
	if github.draft {
		github.pullRequest.Status = pullrequestlifecycle.StatusDraft
	}
	if state == "closed" {
		github.pullRequest.Status = pullrequestlifecycle.StatusClosed
		now := time.Now().UTC()
		github.pullRequest.ClosedAt = &now
	}
	github.pullRequest.UpdatedAt = time.Now().UTC()
	return *github.pullRequest, nil
}

func (github *GitHub) RefreshReadiness(context.Context, pullrequestlifecycle.GitHubPullRequestTarget) (pullrequestlifecycle.GitHubReadiness, error) {
	github.mu.Lock()
	defer github.mu.Unlock()
	if err := github.refreshCommits(); err != nil {
		return pullrequestlifecycle.GitHubReadiness{}, err
	}
	mergeability := pullrequestlifecycle.GitHubMergeabilityMergeable
	if github.remotePath != "" {
		if _, err := gitOutput(github.remotePath, "merge-base", "--is-ancestor", github.baseCommit, github.headCommit); err != nil {
			mergeability = pullrequestlifecycle.GitHubMergeabilityConflicting
		}
	}
	return pullrequestlifecycle.GitHubReadiness{
		HeadCommit: github.headCommit, ChecksState: pullrequestlifecycle.GitHubChecksPassing,
		MergeabilityState: mergeability,
		DetailsURL:        "https://example.invalid/holark-fixture/browser-scenario/pull/101", SyncedAt: time.Now().UTC(),
	}, nil
}

func seedBackgroundHolons(ctx context.Context, application *localapp.Application, repositoryPath string) error {
	baseCommit, err := gitOutput(repositoryPath, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	titles := []string{
		"Improve repository navigation", "Add keyboard shortcuts", "Polish the terminal toolbar",
		"Investigate slow startup", "Update project documentation", "Simplify branch selection",
		"Improve error messages", "Refine the changes view", "Organize project settings",
		"Check terminal reconnect behavior",
		"Improve pull request filtering", "Review empty states", "Polish command search",
		"Add repository shortcuts", "Check narrow screen layouts",
	}
	for _, title := range titles {
		holon, err := application.Holons.Create(ctx, holons.Create{
			Title: title, Prompt: title, Kind: holons.KindNormal,
			BaseBranch: "main", BaseCommit: strings.TrimSpace(baseCommit), AgentType: "codex",
		})
		if err != nil {
			return err
		}
		if _, err = application.Holons.SetAgentSessionStatus(ctx, holon.ID, holon.AgentSessions[0].ID, holons.StatusCompleted, "", ""); err != nil {
			return err
		}
	}
	return nil
}

func seed(ctx context.Context, application *localapp.Application, repositoryPath string, github *GitHub, conflictingRebase, browserScenario bool) (State, error) {
	baseCommit, err := gitOutput(repositoryPath, "rev-parse", "HEAD")
	if err != nil {
		return State{}, err
	}
	baseCommit = strings.TrimSpace(baseCommit)
	holon, err := application.Holons.Create(ctx, holons.Create{
		Title:  "Build the browser pull request scenario",
		Prompt: "Add a persistent browser scenario for inspecting pull request creation.",
		Kind:   holons.KindNormal, BaseBranch: "main", BaseCommit: baseCommit, AgentType: "codex",
	})
	if err != nil {
		return State{}, err
	}
	if err = os.WriteFile(filepath.Join(holon.WorktreePath, "scenario.txt"), []byte("pull request creation browser scenario\n"), 0o600); err != nil {
		return State{}, err
	}
	message := "Add pull request creation scenario"
	if conflictingRebase {
		if err = os.WriteFile(filepath.Join(holon.WorktreePath, "README.md"), []byte("Pull request scenario with PR creation\n"), 0o600); err != nil {
			return State{}, err
		}
		message += "\n\nAdd a committed scenario marker so the source Holon has changes to publish\nwhen creating a pull request. Update the README to describe PR creation.\n\nThe README change also overlaps with the update on main, giving the rebase\nwalkthrough a conflict to resolve while preserving both descriptions."
	}
	firstCommitAt := time.Now().Add(-5 * time.Hour)
	if browserScenario && !conflictingRebase {
		// Generation and recovery scenarios need multiple commits to reach the agent.
		if _, err = gitOutput(holon.WorktreePath, "-c", "user.name=Holark Browser Scenario", "-c", "user.email=browser-scenario@invalid", "commit", "--allow-empty", "-m", "Prepare pull request creation scenario"); err != nil {
			return State{}, err
		}
	}
	commitArgs := []string{"-c", "user.name=Holark Browser Scenario", "-c", "user.email=browser-scenario@invalid", "commit", "-m", message}
	if conflictingRebase {
		commitArgs = append(commitArgs, "--date", firstCommitAt.Format(time.RFC3339))
	}
	for _, args := range [][]string{
		{"add", "scenario.txt", "README.md"},
		commitArgs,
	} {
		if _, err = gitOutput(holon.WorktreePath, args...); err != nil {
			return State{}, err
		}
	}
	if conflictingRebase {
		if err = seedHappyPathCommits(holon.WorktreePath, firstCommitAt); err != nil {
			return State{}, err
		}
	}
	headCommit, err := gitOutput(holon.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return State{}, err
	}
	headCommit = strings.TrimSpace(headCommit)
	agentID := holon.AgentSessions[0].ID
	if _, err = application.Holons.SetAgentSessionStatus(ctx, holon.ID, agentID, holons.StatusRunning, "", ""); err != nil {
		return State{}, err
	}
	if _, err = application.Holons.UpdateAgentObservation(ctx, holon.ID, agentID, "", "", "", "task_complete", "", "", protocol.ActivityCompleted, nil); err != nil {
		return State{}, err
	}
	github.SetCommits(baseCommit, headCommit)
	return State{HolonID: holon.ID, WorktreePath: holon.WorktreePath}, nil
}

var (
	_ pullrequestlifecycle.BranchPublisher = noOpBranchPublisher{}
	_ pullrequestlifecycle.GitHubTransport = (*GitHub)(nil)
	_ pullrequestmetadata.AgentSessions    = (*MetadataAgents)(nil)
	_ pullrequestmetadata.Provider         = scenarioMetadataProvider{}
)
