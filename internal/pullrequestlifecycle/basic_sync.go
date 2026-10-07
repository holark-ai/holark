package pullrequestlifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/holark-ai/holark/internal/pullrequestparticipants"
)

type basicSyncOutcome struct {
	SyncResult
	participantErr error
}

// Sync imports historical basic observations without fetching Git or collaboration data.
func (coordinator *Coordinator) Sync(ctx context.Context, projectID string) (SyncResult, error) {
	return coordinator.syncScheduled(ctx, projectID, false)
}

// SyncActive detects active pull requests using the same observation pipeline.
func (coordinator *Coordinator) SyncActive(ctx context.Context, projectID string) (SyncResult, error) {
	return coordinator.syncScheduled(ctx, projectID, true)
}

func (coordinator *Coordinator) syncScheduled(ctx context.Context, projectID string, active bool) (SyncResult, error) {
	outcome, err := coordinator.scheduledBasic(ctx, projectID, active)
	return outcome.SyncResult, errors.Join(err, outcome.participantErr)
}

func (coordinator *Coordinator) scheduledBasic(ctx context.Context, projectID string, active bool) (basicSyncOutcome, error) {
	section := "history"
	if active {
		section = "basic"
	}
	return refreshResult(ctx, coordinator.options.Refresh, RefreshKey{RepositoryID: projectID, Section: section}, func(ctx context.Context) (basicSyncOutcome, error) {
		return coordinator.syncBasic(ctx, projectID, active)
	})
}

func (coordinator *Coordinator) RequestRefresh(ctx context.Context, projectID string) {
	coordinator.options.Refresh.enqueue(ctx, RefreshKey{RepositoryID: projectID, Section: "basic"}, func(ctx context.Context) (any, error) { return coordinator.syncBasic(ctx, projectID, true) })
}

func (coordinator *Coordinator) RunBasic(ctx context.Context, projectID string) {
	_, initialErr := coordinator.scheduledBasic(ctx, projectID, true)
	if initialErr == nil {
		initialErr = coordinator.RecoverOperations(ctx)
	}
	coordinator.options.Refresh.RunBasicAfter(ctx, initialErr, func(ctx context.Context) error {
		_, err := coordinator.scheduledBasic(ctx, projectID, true)
		if err == nil {
			err = coordinator.RecoverOperations(ctx)
		}
		return err
	})
}

func (coordinator *Coordinator) syncBasic(ctx context.Context, projectID string, active bool) (result basicSyncOutcome, resultErr error) {
	section := "history"
	if active {
		section = "pull_requests"
	}
	defer func() {
		if coordinator.options.SyncRecorder != nil {
			resultErr = errors.Join(resultErr, coordinator.options.SyncRecorder.RecordSync(context.WithoutCancel(ctx), projectID, section, resultErr))
		}
	}()
	var participantToken uint64
	if coordinator.options.Participants != nil {
		participantToken = coordinator.options.Participants.BeginObservation()
	}
	defer coordinator.enqueueRetirement(ctx, projectID)
	if active {
		defer coordinator.enqueueActiveBranches(ctx, projectID)
	}
	registry := coordinator.registry
	project, backed, err := coordinator.githubRepository(projectID)
	if err != nil {
		return basicSyncOutcome{}, err
	}
	if !backed {
		return basicSyncOutcome{SyncResult: SyncResult{PullRequests: registry.ListPullRequests(projectID)}}, nil
	}

	token, err := coordinator.beginObservation(ctx, projectID)
	if err != nil {
		return basicSyncOutcome{}, err
	}
	existing := registry.ListPullRequests(projectID)
	var remote []GitHubPullRequest
	if active {
		remote, err = coordinator.options.GitHubTransport.ListActive(ctx, project.RepositoryURL)
	} else {
		remote, err = coordinator.options.GitHubTransport.List(ctx, project.RepositoryURL)
	}
	if err != nil {
		return basicSyncOutcome{}, err
	}
	syncedAt := coordinator.now()
	incoming := make([]PullRequest, 0, len(remote))
	seen := map[string]bool{}
	for _, item := range remote {
		incoming = append(incoming, pullRequestFromGitHub(projectID, item, "", syncedAt))
		seen[item.ExternalID] = true
	}
	// A successful complete listing is required before checking disappearance.
	// Only the individual response can establish a terminal transition.
	for _, current := range existing {
		if current.SyncProvider != SyncProviderGitHub || current.SyncExternalID == "" || !current.Status.Active() || seen[current.SyncExternalID] {
			continue
		}
		item, err := coordinator.options.GitHubTransport.Get(ctx, coordinator.githubTarget(current))
		if err != nil {
			return basicSyncOutcome{}, err
		}
		if item.ExternalID != current.SyncExternalID {
			return basicSyncOutcome{}, &GitHubError{Kind: GitHubSyncFailure, Err: errors.New("GitHub returned a different pull request")}
		}
		incoming = append(incoming, pullRequestFromGitHub(projectID, item, "", syncedAt))
		remote = append(remote, item)
	}
	incoming, err = coordinator.reconcileGitHubSync(existing, incoming)
	if err != nil {
		return basicSyncOutcome{}, err
	}
	for i := range incoming {
		for _, current := range existing {
			if current.SyncExternalID != incoming[i].SyncExternalID {
				continue
			}
			if sameTopologyInputs(current, incoming[i]) {
				incoming[i].DiffBaseCommit = current.DiffBaseCommit
			}
			if !current.HeadRef.Valid() && current.TopologyConfirmation != nil && current.HeadCommit != incoming[i].HeadCommit {
				coordinator.verifyObservedHead(ctx, project, &incoming[i])
			}
			break
		}
	}
	remaining, attached, err := attachMatchingWIPs(registry, existing, incoming, token, project.RepositoryURL)
	if err != nil {
		return basicSyncOutcome{}, err
	}
	imported, updated, err := coordinator.upsertObserved(projectID, remaining, token)
	updated += attached
	if err == nil && coordinator.options.Participants != nil {
		byExternal := map[string]string{}
		for _, pr := range registry.ListPullRequests(projectID) {
			byExternal[pr.SyncExternalID] = pr.ID
		}
		for _, item := range remote {
			if id := byExternal[item.ExternalID]; id != "" {
				participantErr := coordinator.options.Participants.ApplyObservation(ctx, id, participantToken, item.Participants)
				if participantErr != nil && !errors.Is(participantErr, pullrequestparticipants.ErrStaleObservation) {
					slog.ErrorContext(ctx, "Pull request participant import failed", "repository_id", projectID, "pull_request_id", id, "section", section, "error", participantErr)
					result.participantErr = errors.Join(result.participantErr, fmt.Errorf("pull request %s: %w", id, participantErr))
				}
			}
		}
		if coordinator.options.SyncRecorder != nil {
			result.participantErr = errors.Join(result.participantErr, coordinator.options.SyncRecorder.RecordSync(context.WithoutCancel(ctx), projectID, section+"_participants", result.participantErr))
		}
	}
	result.SyncResult = SyncResult{PullRequests: registry.ListPullRequests(projectID), Imported: imported, Updated: updated, SyncedAt: syncedAt}
	return result, err
}
func (coordinator *Coordinator) verifyObservedHead(ctx context.Context, project Project, current *PullRequest) {
	if coordinator.options.Repository == nil {
		return
	}
	repositoryURL := DecodeGitHubObservation(current.SyncData).HeadRepositoryURL
	if repositoryURL == "" {
		return
	}
	head, err := coordinator.options.Repository.RemoteBranchHead(ctx, repositoryURL, current.HeadBranch)
	current.VerifiedHead = err == nil && head == current.HeadCommit
}

// RequestRetirement schedules repository cleanup without waiting for it to finish.
// The scheduler owns its lifetime, independent of the requesting context.
func (coordinator *Coordinator) RequestRetirement(ctx context.Context, pullRequestID string) {
	pullRequest, ok := coordinator.registry.GetPullRequest(pullRequestID)
	if !ok {
		return
	}
	coordinator.enqueueRetirement(ctx, pullRequest.RepositoryID)
}

// Cleanup observes the latest accepted catalog even after a partially failed sync.
// It has separate scheduling so failures cannot undo observations or publication.
func (coordinator *Coordinator) enqueueRetirement(ctx context.Context, projectID string) {
	if coordinator.options.Retirement == nil {
		return
	}
	coordinator.options.Refresh.Trigger(ctx, RefreshKey{RepositoryID: projectID, Section: "retirement"}, func(ctx context.Context) error {
		var result error
		for _, pr := range coordinator.registry.ListPullRequests(projectID) {
			if !pr.Status.Active() {
				result = errors.Join(result, coordinator.options.Retirement.RetireInactive(ctx, pr.ID))
			}
		}
		return result
	})
}
