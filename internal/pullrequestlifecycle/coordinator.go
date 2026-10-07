package pullrequestlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/pullrequestparticipants"
	"github.com/holark-ai/holark/internal/repositorybrowser"
)

var (
	ErrGitHubSyncUnsupported      = errors.New("pull request is not GitHub-backed")
	ErrGitHubReadinessUnsupported = errors.New("pull request is not GitHub-backed")
	ErrGitHubReadinessInactive    = errors.New("pull request is inactive")
)

type Error struct {
	Code    string
	Message string
}

func (err Error) Error() string {
	if err.Message != "" {
		return err.Message
	}
	return err.Code
}

type Registry interface {
	CompleteTransition(context.Context, string, PullRequest, PullRequest) (PullRequest, error)
	CompleteRecoveredHead(context.Context, string, string, string, string) (PullRequest, bool, error)
	ConfirmRecoveredMetadata(context.Context, string, string, string, string, time.Time) error
	ActionRegistry
	ObservationRegistry
	GetPullRequest(string) (PullRequest, bool)
	ListPullRequests(string) []PullRequest
	CreateOperationPullRequest(context.Context, string, PullRequest) (PullRequest, error)
	ListRecoverableOperations(context.Context) ([]Operation, error)
	CancelOperationBeforeStep(context.Context, string, string) (bool, error)

	AttachObservedPullRequest(string, PullRequest, ObservationToken) (PullRequest, bool, error)
	UpdatePullRequestReadinessObservation(string, int64, int64, string, json.RawMessage, time.Time) (PullRequest, error)
}

type Project struct {
	ID            string
	RepositoryURL string
	DefaultBranch string
	GitHubBacked  bool
}

type ProjectLookup interface{ Project(string) (Project, bool) }
type ProjectLookupFunc func(string) (Project, bool)

func (fn ProjectLookupFunc) Project(id string) (Project, bool) { return fn(id) }

type Repository interface {
	TopologyRepository
	Refresh(context.Context, repositorybrowser.Repository) error
	ResolveRef(context.Context, repositorybrowser.Repository, string) (string, error)
}

type Clock interface{ Now() time.Time }
type ClockFunc func() time.Time

func (fn ClockFunc) Now() time.Time { return fn() }

type PublicationReadiness interface {
	PublicationBlocked(context.Context, string) (bool, error)
}

type OpeningPreparation interface {
	RequestOpen(context.Context, string, string) (bool, error)
	ClearPreparation(context.Context, string) error
}

// Retirement cleans up activity for accepted terminal lifecycle state.
type Retirement interface {
	RetireInactive(context.Context, string) error
}

type ParticipantObserver interface {
	BeginObservation() uint64
	ApplyObservation(context.Context, string, uint64, pullrequestparticipants.RemoteParticipantSnapshot) error
}
type SyncRecorder interface {
	RecordSync(context.Context, string, string, error) error
}
type Options struct {
	// OnCreationRecovered runs after an interrupted creation completes successfully.
	OnCreationRecovered  func(context.Context, PullRequest)
	Participants         ParticipantObserver
	SyncRecorder         SyncRecorder
	Retirement           Retirement
	Refresh              *RefreshCoordinator
	OpeningPreparation   OpeningPreparation
	Projects             ProjectLookup
	Repository           Repository
	GitHubTransport      GitHubTransport
	GitHubCodec          GitHubSyncDataCodec
	Clock                Clock
	PublicationReadiness PublicationReadiness
}

type Coordinator struct {
	registry Registry
	options  Options
}

type SyncResult struct {
	PullRequests []PullRequest `json:"pull_requests"`
	Imported     int           `json:"imported"`
	Updated      int           `json:"updated"`
	SyncedAt     time.Time     `json:"synced_at"`
}

func (coordinator *Coordinator) SetPublicationReadiness(readiness PublicationReadiness) {
	coordinator.options.PublicationReadiness = readiness
}

func (coordinator *Coordinator) SetOpeningPreparation(preparation OpeningPreparation) {
	coordinator.options.OpeningPreparation = preparation
}

func (coordinator *Coordinator) SetBrowsingSync(participants ParticipantObserver, recorder SyncRecorder) {
	coordinator.options.Participants = participants
	coordinator.options.SyncRecorder = recorder
}

func (coordinator *Coordinator) SetRetirement(retirement Retirement) {
	coordinator.options.Retirement = retirement
}

func New(registry Registry, options Options) *Coordinator {
	if options.Refresh == nil {
		options.Refresh = NewRefreshCoordinator()
	}
	if options.Clock == nil {
		options.Clock = ClockFunc(func() time.Time { return time.Now().UTC() })
	}
	return &Coordinator{registry: registry, options: options}
}

// RequestTransition applies user-requested preparation before changing status.
// Metadata completion calls Transition directly to avoid preparing again.
func (coordinator *Coordinator) RequestTransition(ctx context.Context, pullRequest PullRequest, targetStatus Status) (result PullRequest, resultErr error) {
	pullRequest = coordinator.refreshed(pullRequest)
	ctx = WithRequestID(ctx, RequestID(ctx))
	{
		registry := coordinator.registry
		existing, found, err := registry.GetOperation(ctx, RequestID(ctx))
		if err != nil {
			return PullRequest{}, err
		}
		if found {
			requested := Operation{Kind: "transition", PullRequestID: pullRequest.ID, RequestedStatus: targetStatus}
			if !MatchesOperationRequest(existing, requested) {
				return PullRequest{}, OperationRequestConflict()
			}
			switch existing.Status {
			case "succeeded", "uncertain":
				return pullRequest, nil
			case "failed":
				return PullRequest{}, StoredOperationError(existing)
			}
		}
	}
	if !pullRequest.Status.CanTransitionTo(targetStatus) {
		return PullRequest{}, ErrInvalidTransition
	}
	operation, shared, err := coordinator.registerTransition(ctx, pullRequest, targetStatus)
	if err != nil {
		return PullRequest{}, err
	}
	ctx = WithRequestID(ctx, operation.RequestID)
	if shared && (coordinator.options.OpeningPreparation == nil || targetStatus != StatusOpen) {
		return coordinator.refreshed(pullRequest), nil
	}
	asynchronous := false
	defer func() {
		if !asynchronous && !shared && resultErr != nil && !errors.Is(resultErr, ErrOperationInProgress) {
			coordinator.finishTransition(ctx, operation, result, resultErr)
		}
		if result.ID != "" {
			result = coordinator.refreshed(result)
		}
	}()

	if targetStatus != StatusClosed && targetStatus != StatusOpen && (pullRequest.Status == StatusWIP || pullRequest.Status == StatusDraft) && coordinator.options.PublicationReadiness != nil {
		blocked, err := coordinator.options.PublicationReadiness.PublicationBlocked(ctx, pullRequest.ID)
		if err != nil {
			return PullRequest{}, err
		}
		if blocked {
			return PullRequest{}, Error{Code: "pull_request_metadata_pending", Message: "Pull request opening is pending. Complete or retry metadata preparation."}
		}
	}
	if preparation := coordinator.options.OpeningPreparation; preparation != nil {
		if targetStatus == StatusClosed {
			if err := preparation.ClearPreparation(ctx, pullRequest.ID); err != nil {
				return PullRequest{}, err
			}
		}
		if targetStatus == StatusOpen {
			handled, err := preparation.RequestOpen(ctx, pullRequest.ID, operation.RequestID)
			pullRequest = coordinator.refreshed(pullRequest)
			if handled || err != nil {
				asynchronous = handled && err == nil && pullRequest.Status != targetStatus
				return pullRequest, err
			}
		}
	}
	return coordinator.Transition(ctx, coordinator.refreshed(pullRequest), targetStatus)
}

func (coordinator *Coordinator) refreshed(pullRequest PullRequest) PullRequest {
	if latest, exists := coordinator.registry.GetPullRequest(pullRequest.ID); exists {
		return latest
	}
	return pullRequest
}

func (coordinator *Coordinator) Transition(ctx context.Context, pullRequest PullRequest, targetStatus Status) (result PullRequest, resultErr error) {
	pullRequest = coordinator.refreshed(pullRequest)
	ctx = WithRequestID(ctx, RequestID(ctx))
	operation, _, err := coordinator.registry.BeginOperation(ctx, Operation{RequestID: RequestID(ctx), PullRequestID: pullRequest.ID, Kind: "transition", RequestedStatus: targetStatus, Groups: []FieldGroup{LifecycleGroup}})
	if err != nil {
		return PullRequest{}, err
	}
	if operation.Status == "succeeded" {
		return pullRequest, nil
	}
	if operation.Status == "failed" {
		return PullRequest{}, StoredOperationError(operation)
	}
	if operation.Status == "uncertain" || operation.Steps["mutation"].Status != "" {
		return PullRequest{}, ErrOperationInProgress
	}
	defer func() {
		if resultErr == nil {
			result, resultErr = coordinator.registry.CompleteTransition(context.WithoutCancel(ctx), operation.RequestID, pullRequest, result)
			if resultErr != nil {
				result, resultErr = confirmedRemoteResult(result, resultErr)
			}
		}
		if resultErr != nil && !errors.Is(resultErr, ErrOperationInProgress) {
			coordinator.finishTransition(ctx, operation, result, resultErr)
		}
	}()
	if err = coordinator.registry.RecordOperationStep(ctx, operation.RequestID, pullRequest.ID, "mutation", OperationStep{Status: "running", HeadCommit: pullRequest.HeadCommit}); err != nil {
		return PullRequest{}, err
	}

	if !pullRequest.Status.CanTransitionTo(targetStatus) {
		return PullRequest{}, ErrInvalidTransition
	}
	if pullRequest.SyncProvider != "" && pullRequest.SyncProvider != string(SyncProviderGitHub) {
		return PullRequest{}, Error{Code: "unsupported_source", Message: "Pull request source is unsupported."}
	}
	if pullRequest.Status == StatusWIP {
		if targetStatus == StatusClosed {
			return localTransition(pullRequest, targetStatus, coordinator.now()), nil
		}
		if pullRequest.SyncProvider == "" {
			return coordinator.createGitHubPullRequest(ctx, pullRequest, targetStatus)
		}
		reopened, err := coordinator.updateGitHubPullRequestState(ctx, pullRequest, "open", StatusDraft, StatusDraft)
		if err != nil || targetStatus == StatusDraft {
			return reopened, err
		}
		return coordinator.markGitHubPullRequestReady(ctx, reopened)
	}
	if pullRequest.SyncProvider == "" {
		return localTransition(pullRequest, targetStatus, coordinator.now()), nil
	}
	switch {
	case pullRequest.Status == StatusDraft && targetStatus == StatusWIP:
		return coordinator.withdrawGitHubDraft(ctx, pullRequest)
	case pullRequest.Status == StatusDraft && targetStatus == StatusOpen:
		return coordinator.markGitHubPullRequestReady(ctx, pullRequest)
	case pullRequest.Status == StatusOpen && targetStatus == StatusDraft:
		return coordinator.convertGitHubPullRequestToDraft(ctx, pullRequest)
	case targetStatus == StatusClosed:
		return coordinator.updateGitHubPullRequestState(ctx, pullRequest, "closed", StatusClosed, StatusClosed)
	default:
		return PullRequest{}, ErrInvalidTransition
	}
}

// SyncPullRequest waits for accepted observations and required Git preparation.
// Readers must subsequently obtain their inputs from GetPullRequest.
func (coordinator *Coordinator) SyncPullRequest(ctx context.Context, id string) error {
	current, ok := coordinator.registry.GetPullRequest(id)
	if !ok {
		return ErrNotFound
	}
	return coordinator.options.Refresh.Do(ctx, RefreshKey{RepositoryID: current.RepositoryID, PullRequestID: id, Section: "pull_request"}, func(ctx context.Context) error {
		current, ok := coordinator.registry.GetPullRequest(id)
		if !ok {
			return ErrNotFound
		}
		return coordinator.syncPullRequest(ctx, current)
	})
}

// RequestPullRequestRefresh schedules the same preparation as explicit synchronization.
// Reload inside the scheduled pass so completion behind an older read gets fresh inputs.
func (coordinator *Coordinator) RequestPullRequestRefresh(ctx context.Context, id string) {
	current, ok := coordinator.registry.GetPullRequest(id)
	if !ok {
		return
	}
	coordinator.options.Refresh.Trigger(ctx, RefreshKey{RepositoryID: current.RepositoryID, PullRequestID: id, Section: "pull_request"}, func(ctx context.Context) error {
		current, ok := coordinator.registry.GetPullRequest(id)
		if !ok {
			return ErrNotFound
		}
		return coordinator.syncPullRequest(ctx, current)
	})
}

func (coordinator *Coordinator) GetPullRequest(id string) (PullRequest, bool) {
	return coordinator.registry.GetPullRequest(id)
}

func (coordinator *Coordinator) syncPullRequest(ctx context.Context, current PullRequest) error {
	err := coordinator.synchronizeAccepted(ctx, current)
	if err != nil {
		slog.WarnContext(ctx, "Pull request synchronization failed", "pull_request_id", current.ID, "error", err)
	}
	return err
}

func findPullRequest(pullRequests []PullRequest, id string) (PullRequest, bool) {
	for _, pullRequest := range pullRequests {
		if pullRequest.ID == id {
			return pullRequest, true
		}
	}
	return PullRequest{}, false
}

func (coordinator *Coordinator) createGitHubPullRequest(ctx context.Context, pullRequest PullRequest, targetStatus Status) (PullRequest, error) {
	project, githubBacked, err := coordinator.githubRepository(pullRequest.RepositoryID)
	if err != nil {
		return PullRequest{}, err
	}
	if !githubBacked {
		return localTransition(pullRequest, targetStatus, coordinator.now()), nil
	}
	if coordinator.options.Repository == nil {
		return PullRequest{}, Error{Code: "repository_unavailable", Message: "Repository browsing is unavailable."}
	}
	_ = coordinator.refreshRepositoryMirror(ctx, project)
	if _, err := coordinator.options.Repository.ResolveRef(ctx, browserRepository(project), "refs/heads/"+pullRequest.HeadBranch); err != nil {
		return PullRequest{}, err
	}
	created, err := coordinator.options.GitHubTransport.Create(ctx, GitHubCreateRequest{
		RepositoryURL: project.RepositoryURL, Title: pullRequest.Title, Body: pullRequest.Summary,
		Head: pullRequest.HeadBranch, Base: pullRequest.BaseBranch, Draft: targetStatus == StatusDraft,
	})
	if err != nil {
		return PullRequest{}, err
	}
	if err := coordinator.registry.RecordOperationStep(context.WithoutCancel(ctx), RequestID(ctx), pullRequest.ID, "github_identity", OperationStep{Status: "succeeded", ExternalID: created.ExternalID, HeadCommit: created.HeadCommit}); err != nil {
		return PullRequest{}, &GitHubError{Kind: GitHubSyncFailure, Err: err, UncertainOutcome: true}
	}

	synced := pullRequestFromGitHub(pullRequest.RepositoryID, created, pullRequest.DiffBaseCommit, coordinator.now())
	synced, err = newPullRequestTopologyReconciler(coordinator.options.Repository).Reconcile(ctx, synced)
	if err != nil {
		// Creation already succeeded remotely. Retain its gate until recovery
		// attaches the verified identity; retrying Create could duplicate it.
		return PullRequest{}, &GitHubError{Kind: GitHubSyncFailure, Err: err, UncertainOutcome: true}
	}
	synced.Status = targetStatus
	return synced, nil
}

func (coordinator *Coordinator) markGitHubPullRequestReady(ctx context.Context, pullRequest PullRequest) (PullRequest, error) {
	if err := coordinator.requireGitHubPorts(); err != nil {
		return PullRequest{}, err
	}
	target := coordinator.githubTarget(pullRequest)
	if coordinator.options.GitHubCodec.ExternalID(target) == "" {
		return PullRequest{}, Error{Code: "invalid_pull_request_transition", Message: "The pull request transition is invalid."}
	}
	remote, err := coordinator.options.GitHubTransport.MarkReadyForReview(ctx, target)
	if err != nil {
		return PullRequest{}, err
	}
	if len(remote.SyncData) != 0 {
		pullRequest.SyncData = remote.SyncData
	}
	draft, state := false, "open"
	syncData, err := coordinator.options.GitHubCodec.UpdateLifecycleFields(pullRequest.SyncData, GitHubLifecycleFields{Draft: &draft, State: &state})
	if err != nil {
		return confirmedRemoteResult(PullRequest{}, err)
	}
	now := coordinator.now()
	pullRequest = localTransition(pullRequest, StatusOpen, now)
	pullRequest.SyncData, pullRequest.SyncedAt = syncData, &now
	return pullRequest, nil
}

func (coordinator *Coordinator) convertGitHubPullRequestToDraft(ctx context.Context, pullRequest PullRequest) (PullRequest, error) {
	if err := coordinator.requireGitHubPorts(); err != nil {
		return PullRequest{}, err
	}
	target := coordinator.githubTarget(pullRequest)
	if coordinator.options.GitHubCodec.ExternalID(target) == "" {
		return PullRequest{}, Error{Code: "invalid_pull_request_transition", Message: "The pull request transition is invalid."}
	}
	remote, err := coordinator.options.GitHubTransport.ConvertToDraft(ctx, target)
	if err != nil {
		return PullRequest{}, err
	}
	if len(remote.SyncData) != 0 {
		pullRequest.SyncData = remote.SyncData
	}
	draft, state := true, "open"
	syncData, err := coordinator.options.GitHubCodec.UpdateLifecycleFields(pullRequest.SyncData, GitHubLifecycleFields{Draft: &draft, State: &state})
	if err != nil {
		return confirmedRemoteResult(PullRequest{}, err)
	}
	now := coordinator.now()
	pullRequest = localTransition(pullRequest, StatusDraft, now)
	pullRequest.SyncData, pullRequest.SyncedAt = syncData, &now
	return pullRequest, nil
}

func (coordinator *Coordinator) withdrawGitHubDraft(ctx context.Context, pullRequest PullRequest) (PullRequest, error) {
	return coordinator.updateGitHubPullRequestState(ctx, pullRequest, "closed", StatusClosed, StatusWIP)
}

func (coordinator *Coordinator) updateGitHubPullRequestState(ctx context.Context, pullRequest PullRequest, state string, expectedRemoteStatus, targetStatus Status) (PullRequest, error) {
	if err := coordinator.requireGitHubPorts(); err != nil {
		return PullRequest{}, err
	}
	project, githubBacked, err := coordinator.githubRepository(pullRequest.RepositoryID)
	if err != nil {
		return PullRequest{}, err
	}
	if !githubBacked {
		return PullRequest{}, &GitHubError{Kind: GitHubUnsupportedRepository}
	}
	target := coordinator.githubTarget(pullRequest)
	if coordinator.options.GitHubCodec.Number(target) == 0 {
		return PullRequest{}, Error{Code: "invalid_pull_request_transition", Message: "The pull request transition is invalid."}
	}
	remote, err := coordinator.options.GitHubTransport.UpdateState(ctx, target, state)
	if err != nil {
		return PullRequest{}, err
	}
	syncedAt := coordinator.now()
	synced := pullRequestFromGitHub(pullRequest.RepositoryID, remote, pullRequest.DiffBaseCommit, syncedAt)
	if synced.Status != expectedRemoteStatus {
		return PullRequest{}, Error{Code: "invalid_pull_request_transition", Message: "GitHub returned an unexpected pull request state."}
	}
	closedAt := synced.ClosedAt
	if targetStatus != StatusClosed {
		closedAt = nil
	}
	pullRequest = localTransition(pullRequest, targetStatus, synced.UpdatedAt)
	pullRequest.SyncData, pullRequest.SyncedAt, pullRequest.ClosedAt = synced.SyncData, &syncedAt, closedAt
	_ = coordinator.refreshRepositoryMirror(ctx, project)
	return pullRequest, nil
}

func (coordinator *Coordinator) RefreshGitHubReadiness(ctx context.Context, pullRequest PullRequest) (PullRequest, error) {
	return refreshResult(ctx, coordinator.options.Refresh, RefreshKey{RepositoryID: pullRequest.RepositoryID, PullRequestID: pullRequest.ID, Section: "readiness"}, func(ctx context.Context) (PullRequest, error) {
		return coordinator.refreshGitHubReadiness(ctx, coordinator.refreshed(pullRequest))
	})
}

func (coordinator *Coordinator) refreshGitHubReadiness(ctx context.Context, pullRequest PullRequest) (PullRequest, error) {
	if pullRequest.SyncProvider != string(SyncProviderGitHub) {
		return PullRequest{}, ErrGitHubReadinessUnsupported
	}
	if !pullRequest.Status.Active() {
		return PullRequest{}, ErrGitHubReadinessInactive
	}
	if err := coordinator.requireGitHubPorts(); err != nil {
		return PullRequest{}, err
	}
	_, githubBacked, err := coordinator.githubRepository(pullRequest.RepositoryID)
	if err != nil {
		return PullRequest{}, err
	}
	if !githubBacked {
		return PullRequest{}, ErrGitHubReadinessUnsupported
	}
	target := coordinator.githubTarget(pullRequest)
	if coordinator.options.GitHubCodec.Number(target) == 0 {
		return PullRequest{}, ErrGitHubReadinessUnsupported
	}
	token, err := coordinator.registry.BeginObservation(ctx, pullRequest.RepositoryID, nil)
	if err != nil {
		return PullRequest{}, err
	}
	readinessSequence := token.Sequence
	now := coordinator.now()
	providerReadiness, providerErr := coordinator.options.GitHubTransport.RefreshReadiness(ctx, target)
	if providerErr != nil {
		slog.WarnContext(ctx, "Pull request GitHub readiness refresh failed; last successful readiness will be retained",
			"pull_request_id", pullRequest.ID, "error", providerErr)
		return pullRequest, providerErr
	}
	readiness := GitHubReadiness{
		HeadCommit: providerReadiness.HeadCommit, ChecksState: providerReadiness.ChecksState,
		MergeabilityState: providerReadiness.MergeabilityState, DetailsURL: providerReadiness.DetailsURL, SyncedAt: now,
	}
	syncData, err := coordinator.options.GitHubCodec.StoreReadiness(pullRequest.SyncData, readiness)
	if err != nil {
		return PullRequest{}, err
	}
	accepted, err := coordinator.registry.UpdatePullRequestReadinessObservation(pullRequest.ID, pullRequest.TopologyGeneration, readinessSequence, providerReadiness.HeadCommit, syncData, now)
	if err == nil && (accepted.ReadinessObservation < readinessSequence || !sameTopologyInputs(accepted, pullRequest) || providerReadiness.HeadCommit != accepted.HeadCommit) {
		err = ErrSynchronizationStale
	}
	return accepted, err
}

func (coordinator *Coordinator) githubRepository(projectID string) (Project, bool, error) {
	if coordinator.options.Projects == nil {
		return Project{}, false, Error{Code: "project_not_found", Message: "Project not found."}
	}
	project, ok := coordinator.options.Projects.Project(projectID)
	if !ok {
		return Project{}, false, Error{Code: "project_not_found", Message: "Project not found."}
	}
	if project.GitHubBacked && coordinator.options.GitHubTransport == nil {
		return Project{}, false, ErrGitHubClientUnsupported
	}
	return project, project.GitHubBacked, nil
}

func (coordinator *Coordinator) requireGitHubPorts() error {
	if coordinator.options.GitHubTransport == nil || coordinator.options.GitHubCodec == nil {
		return ErrGitHubClientUnsupported
	}
	return nil
}

func (coordinator *Coordinator) githubTarget(pullRequest PullRequest) GitHubPullRequestTarget {
	repositoryURL := ""
	if coordinator.options.Projects != nil {
		if project, ok := coordinator.options.Projects.Project(pullRequest.RepositoryID); ok {
			repositoryURL = project.RepositoryURL
		}
	}
	return GitHubPullRequestTarget{RepositoryURL: repositoryURL, ExternalID: pullRequest.SyncExternalID, SyncData: pullRequest.SyncData}
}

func (coordinator *Coordinator) refreshRepositoryMirror(ctx context.Context, project Project) error {
	if coordinator.options.Repository == nil {
		return nil
	}
	return coordinator.options.Repository.Refresh(ctx, browserRepository(project))
}

func (coordinator *Coordinator) now() time.Time { return coordinator.options.Clock.Now().UTC() }

func browserRepository(project Project) repositorybrowser.Repository {
	return repositorybrowser.Repository{ID: project.ID, RepositoryURL: project.RepositoryURL, DefaultBranch: project.DefaultBranch}
}

func pullRequestFromGitHub(projectID string, pullRequest GitHubPullRequest, diffBaseCommit string, syncedAt time.Time) PullRequest {
	return PullRequest{
		RepositoryID: projectID, Title: pullRequest.Title, Summary: pullRequest.Summary,
		BaseBranch: pullRequest.BaseBranch, BaseCommit: pullRequest.BaseCommit, HeadBranch: pullRequest.HeadBranch, HeadCommit: pullRequest.HeadCommit,
		DiffBaseCommit: diffBaseCommit, Status: pullRequest.Status, SyncProvider: string(SyncProviderGitHub),
		SyncExternalID: pullRequest.ExternalID, SyncData: pullRequest.SyncData, LinkedHolonIDs: []string{},
		CreatedAt: pullRequest.CreatedAt, UpdatedAt: pullRequest.UpdatedAt, ClosedAt: pullRequest.ClosedAt,
		MergedAt: pullRequest.MergedAt, MergedCommit: pullRequest.MergedCommit, MergeStrategy: pullRequest.MergeStrategy, SyncedAt: &syncedAt,
	}
}

func (coordinator *Coordinator) reconcileGitHubSync(existing, incoming []PullRequest) ([]PullRequest, error) {
	byExternalID := make(map[string]PullRequest)
	for _, pullRequest := range existing {
		if pullRequest.SyncProvider == string(SyncProviderGitHub) && pullRequest.SyncExternalID != "" {
			byExternalID[pullRequest.SyncExternalID] = pullRequest
		}
	}
	reconciled := append([]PullRequest(nil), incoming...)
	for index := range reconciled {
		pullRequest := &reconciled[index]
		existingPullRequest, ok := byExternalID[pullRequest.SyncExternalID]
		if !ok {
			continue
		}
		if coordinator.options.GitHubCodec != nil {
			// Readiness is stored inside provider sync data, so an empty payload
			// cannot contain a previous successful snapshot.
			if len(existingPullRequest.SyncData) != 0 {
				if readiness, ok := coordinator.options.GitHubCodec.DecodeReadiness(existingPullRequest.SyncData); ok && readiness.Error == "" {
					syncData, err := coordinator.options.GitHubCodec.StoreReadiness(pullRequest.SyncData, readiness)
					if err != nil {
						return nil, err
					}
					pullRequest.SyncData = syncData
				}
			}
		}
		if existingPullRequest.Status == StatusWIP && pullRequest.Status == StatusClosed {
			pullRequest.Status, pullRequest.ClosedAt = StatusWIP, nil
		}
	}
	return reconciled, nil
}

func (coordinator *Coordinator) reconcileGitHubTopologies(ctx context.Context, project Project, existing, incoming []PullRequest, topologyAvailable bool) []PullRequest {
	byExternalID := make(map[string]PullRequest)
	for _, pullRequest := range existing {
		if pullRequest.SyncProvider == string(SyncProviderGitHub) && pullRequest.SyncExternalID != "" {
			byExternalID[pullRequest.SyncExternalID] = pullRequest
		}
	}
	reconciler := newPullRequestTopologyReconciler(coordinator.options.Repository)
	reconciled := append([]PullRequest(nil), incoming...)
	for index := range reconciled {
		pullRequest := &reconciled[index]
		previous, tracked := byExternalID[pullRequest.SyncExternalID]
		needsTopology := pullRequest.Status.Active() || (tracked && previous.Status.Active())
		if !needsTopology || !topologyAvailable {
			if tracked {
				preservePullRequestTopology(pullRequest, previous)
			}
			continue
		}
		updated, err := reconciler.Reconcile(ctx, *pullRequest)
		if err == nil {
			*pullRequest = updated
			continue
		}
		if tracked {
			preservePullRequestTopology(pullRequest, previous)
		}
		slog.WarnContext(ctx, "Pull request topology reconciliation failed; provider lifecycle data will still be saved",
			"pull_request_external_id", pullRequest.SyncExternalID, "error", err)
	}
	return reconciled
}

func preservePullRequestTopology(pullRequest *PullRequest, previous PullRequest) {
	pullRequest.HeadBranch = previous.HeadBranch
	pullRequest.HeadCommit = previous.HeadCommit
	pullRequest.BaseBranch = previous.BaseBranch
	pullRequest.BaseCommit = previous.BaseCommit
	pullRequest.DiffBaseCommit = previous.DiffBaseCommit
}

func attachMatchingWIPs(registry Registry, existing, incoming []PullRequest, token ObservationToken, repositoryURL string) ([]PullRequest, int, error) {
	tracked := make(map[string]struct{})
	localWIPs := make([]PullRequest, 0)
	for _, pullRequest := range existing {
		if pullRequest.SyncProvider != "" && pullRequest.SyncExternalID != "" {
			tracked[pullRequest.SyncExternalID] = struct{}{}
		}
		if pullRequest.Status == StatusWIP && strings.TrimSpace(pullRequest.SyncProvider) == "" {
			localWIPs = append(localWIPs, pullRequest)
		}
	}
	assignments := make(map[int]PullRequest)
	assignedWIPs := make(map[string]struct{})
	for index, remote := range incoming {
		if remote.Status != StatusDraft && remote.Status != StatusOpen {
			continue
		}
		if _, ok := tracked[remote.SyncExternalID]; ok {
			continue
		}
		matches := make([]PullRequest, 0, 1)
		for _, local := range localWIPs {
			if local.RepositoryID == remote.RepositoryID && local.BaseBranch == remote.BaseBranch && local.HeadBranch == remote.HeadBranch && localPublicationSourceMatches(remote.SyncData, "", repositoryURL) {
				matches = append(matches, local)
			}
		}
		if len(matches) > 1 {
			return nil, 0, ambiguousAttachmentError(remote)
		}
		if len(matches) == 0 {
			continue
		}
		if _, duplicate := assignedWIPs[matches[0].ID]; duplicate {
			return nil, 0, ambiguousAttachmentError(remote)
		}
		assignedWIPs[matches[0].ID] = struct{}{}
		assignments[index] = matches[0]
	}
	remaining := make([]PullRequest, 0, len(incoming)-len(assignments))
	for index, remote := range incoming {
		local, attach := assignments[index]
		if !attach {
			remaining = append(remaining, remote)
			continue
		}
		if _, _, err := registry.AttachObservedPullRequest(local.ID, remote, token); err != nil {
			return nil, 0, err
		}
	}
	return remaining, len(assignments), nil
}

func ambiguousAttachmentError(pullRequest PullRequest) error {
	return &GitHubError{Kind: GitHubSyncFailure, Err: fmt.Errorf("ambiguous local WIP match for %s", pullRequest.SyncExternalID)}
}

func sameTopologyInputs(a, b PullRequest) bool {
	if a.HeadCommit != b.HeadCommit || a.BaseCommit != b.BaseCommit || a.HeadBranch != b.HeadBranch || a.BaseBranch != b.BaseBranch {
		return false
	}
	left, right := DecodeGitHubObservation(a.SyncData), DecodeGitHubObservation(b.SyncData)
	return left.HeadRepositoryURL == right.HeadRepositoryURL && left.BaseRepositoryURL == right.BaseRepositoryURL
}

func confirmedRemoteResult(result PullRequest, err error) (PullRequest, error) {
	if err != nil {
		return result, &GitHubError{Kind: GitHubSyncFailure, Err: err, UncertainOutcome: true}
	}
	return result, nil
}

func localTransition(current PullRequest, status Status, at time.Time) PullRequest {
	current.Status, current.UpdatedAt = status, at
	current.ClosedAt = nil
	if status == StatusClosed {
		current.ClosedAt = &at
	}
	return current
}
