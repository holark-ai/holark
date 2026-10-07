package pullrequestlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
)

func (coordinator *Coordinator) registerTransition(ctx context.Context, current PullRequest, target Status) (Operation, bool, error) {
	for _, pending := range current.Operations {
		if pending.Kind == "transition" && pending.RequestedStatus == target {
			operation, _, err := coordinator.registry.GetOperation(ctx, pending.RequestID)
			return operation, true, err
		}
		if target == StatusClosed && pending.Kind == "transition" && pending.RequestedStatus == StatusOpen {
			cancelled, err := coordinator.registry.CancelOperationBeforeStep(context.WithoutCancel(ctx), pending.RequestID, "mutation")
			if err != nil {
				return Operation{}, false, err
			}
			if !cancelled {
				return Operation{}, false, ErrOperationInProgress
			}
		}
	}
	operation, created, err := coordinator.registry.BeginOperation(ctx, Operation{RequestID: RequestID(ctx), PullRequestID: current.ID, Kind: "transition", RequestedStatus: target, Groups: []FieldGroup{LifecycleGroup}})
	return operation, !created, err
}
func (coordinator *Coordinator) finishTransition(ctx context.Context, operation Operation, result PullRequest, err error) {
	if operation.RequestID == "" {
		return
	}
	registry := coordinator.registry
	outcome, message := "succeeded", ""
	if err != nil {
		outcome, message = "failed", err.Error()
		var uncertain interface{ Uncertain() bool }
		if errors.As(err, &uncertain) && uncertain.Uncertain() {
			outcome = "uncertain"
		}
	}
	if _, completionErr := registry.CompleteOperation(context.WithoutCancel(ctx), operation.RequestID, outcome, message); completionErr != nil {
		slog.ErrorContext(ctx, "Pull request action completion could not be persisted", "request_id", operation.RequestID, "error", completionErr)
	}
}
func (coordinator *Coordinator) beginObservation(ctx context.Context, repositoryID string) (ObservationToken, error) {
	token, err := coordinator.registry.BeginObservation(ctx, repositoryID, []FieldGroup{LifecycleGroup, TopologyGroup, MetadataGroup})
	return token, err
}
func (coordinator *Coordinator) upsertObserved(repositoryID string, incoming []PullRequest, token ObservationToken) (int, int, error) {
	return coordinator.registry.UpsertObservedPullRequests(repositoryID, incoming, token)
}

// RecoverOperations reconciles durable unfinished actions without repeating
// mutations. Provider uncertainty stays protected until its effect is known.
func (coordinator *Coordinator) RecoverOperations(ctx context.Context) error {
	registry := coordinator.registry
	operations, err := registry.ListRecoverableOperations(ctx)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		current, found := registry.GetPullRequest(operation.PullRequestID)
		if !found && operation.Kind == "create" {
			step := operation.Steps["preparation"]
			var prepared *PullRequest
			if input := step.Creation; input != nil {
				prepared = &PullRequest{BaseRef: input.BaseRef, HeadRef: input.HeadRef, RepositoryID: input.RepositoryID, Title: input.Title, Summary: input.Summary, BaseBranch: input.BaseBranch, BaseCommit: input.BaseCommit, HeadBranch: input.HeadBranch, HeadCommit: step.HeadCommit, DiffBaseCommit: input.DiffBaseCommit, Status: StatusWIP, LinkedHolonIDs: []string{operation.HolonID}, SyncData: json.RawMessage(`{}`)}
			}
			if prepared != nil && prepared.RepositoryID != "" && prepared.HeadCommit != "" && prepared.HeadBranch != "" {

				creation := coordinator.registry
				// Check local identity before attempting recovery: a crash
				// can occur after creation but before recording its ID.
				for _, candidate := range creation.ListPullRequests(prepared.RepositoryID) {
					for _, holonID := range candidate.LinkedHolonIDs {
						if holonID == operation.HolonID {
							current, found = candidate, true
							break
						}
					}
					if found {
						break
					}
				}
				if !found {
					project, _, lookupErr := coordinator.githubRepository(prepared.RepositoryID)
					if lookupErr == nil {
						if verifier := coordinator.options.Repository; verifier != nil {
							actual, verifyErr := verifier.RemoteBranchHead(ctx, project.RepositoryURL, prepared.HeadBranch)
							if verifyErr == nil && actual == prepared.HeadCommit {
								current, err = creation.CreateOperationPullRequest(ctx, operation.RequestID, *prepared)
								if err != nil {
									return err
								}
								found = true
							}
						}
					}
				}
				if found {

					steps := registry
					if err = steps.RecordOperationStep(ctx, operation.RequestID, current.ID, "local_identity", OperationStep{Status: "succeeded", HeadCommit: current.HeadCommit}); err != nil {
						return err
					}

				}

			}
		}
		if !found {
			slog.WarnContext(ctx, "Unresolved pull request action has no local identity", "request_id", operation.RequestID, "holon_id", operation.HolonID)
			continue
		}
		if operation.Kind == "create" {
			_, err = registry.CompleteOperation(ctx, operation.RequestID, "succeeded", "")
			if err != nil {
				return err
			}
			if coordinator.options.OnCreationRecovered != nil {
				coordinator.options.OnCreationRecovered(ctx, current)
			}
			if current.Status == StatusWIP && operation.RequestedStatus != StatusWIP {
				if _, openingErr := coordinator.RequestTransition(WithRequestID(ctx, operation.RequestID+":opening"), current, operation.RequestedStatus); openingErr != nil {
					slog.WarnContext(ctx, "Recovered creation could not resume opening", "request_id", operation.RequestID, "error", openingErr)
				}
			}
			continue
		}
		if operation.Kind == "publish" || operation.Kind == "work_publish" || operation.Kind == "rebase" {
			intended := operation.Steps["publication"].HeadCommit
			if intended == "" {
				slog.WarnContext(ctx, "Publication reconciliation lacks intended head", "request_id", operation.RequestID)
				continue
			}
			project, _, lookupErr := coordinator.githubRepository(current.RepositoryID)
			if lookupErr != nil {
				continue
			}
			verifier := coordinator.options.Repository
			if verifier == nil {
				continue
			}
			source := DecodeGitHubObservation(current.SyncData).HeadRepositoryURL
			if source == "" && current.SyncExternalID == "" {
				source = project.RepositoryURL
			}
			if source == "" {
				continue
			}
			if operation.BranchInputs != nil && (operation.BranchInputs.Head.Identity != current.HeadRef || operation.BranchInputs.Head.Commit != operation.ExpectedHead) {
				continue
			}
			actual, verifyErr := verifier.RemoteBranchHead(ctx, source, current.HeadBranch)
			if verifyErr != nil {
				continue
			}
			if actual == intended {
				if _, _, err = registry.CompleteRecoveredHead(ctx, operation.RequestID, current.ID, operation.ExpectedHead, intended); err != nil {
					return err
				}

			} else {
				slog.WarnContext(ctx, "Publication reconciliation found a different remote head", "request_id", operation.RequestID, "observed_head", actual)
			}
			continue
		}
		if current.SyncProvider == "" && operation.Kind == "transition" && coordinator.options.GitHubTransport != nil {
			project, backed, lookupErr := coordinator.githubRepository(current.RepositoryID)
			if lookupErr != nil || !backed {
				continue
			}
			remote, listErr := coordinator.options.GitHubTransport.List(ctx, project.RepositoryURL)
			if listErr != nil {
				continue
			}
			matches := []GitHubPullRequest{}
			for _, candidate := range remote {
				if candidate.Status == operation.RequestedStatus && candidate.HeadBranch == current.HeadBranch && CanonicalBaseBranch(candidate.BaseBranch) == current.BaseBranch && candidate.HeadCommit == current.HeadCommit && localPublicationSourceMatches(candidate.SyncData, candidate.HeadRepositoryURL, project.RepositoryURL) {
					matches = append(matches, candidate)
				}
			}
			if len(matches) == 1 {
				_, attachErr := coordinator.registry.CompleteTransition(ctx, operation.RequestID, current, pullRequestFromGitHub(current.RepositoryID, matches[0], current.DiffBaseCommit, coordinator.now()))
				if attachErr != nil {
					return attachErr
				}
			} else {
				slog.WarnContext(ctx, "Unresolved creation requires a unique verified remote match", "request_id", operation.RequestID, "matches", len(matches))
			}
			continue
		}
		if current.SyncProvider != SyncProviderGitHub || coordinator.options.GitHubTransport == nil {
			continue
		}
		remote, readErr := coordinator.options.GitHubTransport.Get(ctx, coordinator.githubTarget(current))
		if readErr != nil {
			slog.WarnContext(ctx, "Pull request action reconciliation failed", "request_id", operation.RequestID, "error", readErr)
			continue
		}
		if operation.Kind == "metadata" {
			intended := operation.Steps["metadata"]
			if intended.Status != "" && remote.Title == intended.Title && remote.Summary == intended.Summary {

				metadata := coordinator.registry
				if err = metadata.ConfirmRecoveredMetadata(ctx, operation.RequestID, current.ID, remote.Title, remote.Summary, remote.UpdatedAt); err != nil {
					return err
				}

			}
			continue
		}
		withdrawn := operation.Kind == "transition" && operation.RequestedStatus == StatusWIP && remote.Status == StatusClosed
		confirmed := withdrawn || operation.Kind == "transition" && remote.Status == operation.RequestedStatus || operation.Kind == "merge" && remote.Status == StatusMerged
		if confirmed {
			result := pullRequestFromGitHub(current.RepositoryID, remote, current.DiffBaseCommit, coordinator.now())
			if withdrawn {
				result.Status, result.ClosedAt = StatusWIP, nil
			}
			_, err = coordinator.registry.CompleteTransition(ctx, operation.RequestID, current, result)
			if err != nil {
				return err
			}
		} else {
			slog.WarnContext(ctx, "Pull request action remains unresolved", "request_id", operation.RequestID, "kind", operation.Kind, "observed_status", remote.Status)
		}
	}
	return nil
}
