package localapp

import (
	"context"
	"errors"

	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/repositorybrowser"
)

func (r localRebaser) rebaseSources(ctx context.Context, p pullrequestwork.PullRequest, baseCommit, headCommit string) (repositorybrowser.Repository, error) {
	if p.HeadRepositoryURL == "" && p.BaseRepositoryURL == "" {
		return r.repository, nil
	}
	return r.manager.PrepareRebaseSources(ctx, r.repository, p.HeadRepositoryURL, p.BaseRepositoryURL, baseCommit, headCommit)
}
func (r localRebaser) rebaseWithOperation(ctx context.Context, p pullrequestwork.PullRequest, baseCommit, headCommit, operationID string) (pullrequestwork.RebasePreparation, error) {
	var operation pullrequestlifecycle.Operation
	id := operationID
	if id == "" {
		id = "rebase:" + p.ID + ":" + baseCommit + ":" + headCommit
	}
	previous, found, err := r.actions.GetOperation(ctx, id)
	if err != nil {
		return pullrequestwork.RebasePreparation{}, err
	}
	if found {
		if previous.Kind != "rebase" || previous.PullRequestID != p.ID {
			return pullrequestwork.RebasePreparation{}, pullrequestlifecycle.OperationRequestConflict()
		}
		if step := previous.Steps["publication"]; step.Status == "succeeded" {
			return pullrequestwork.RebasePreparation{OperationID: previous.RequestID, HeadCommit: step.HeadCommit, Rebased: step.HeadCommit != headCommit}, nil
		}
		if previous.Active() {
			return pullrequestwork.RebasePreparation{}, pullrequestlifecycle.ErrOperationInProgress
		}
		id = pullrequestlifecycle.RequestID(ctx) + ":rebase"
	}

	current, ok := r.actions.GetPullRequest(p.ID)
	if !ok || workPullRequest(current) != p || current.BaseCommit != baseCommit || current.HeadCommit != headCommit {
		return pullrequestwork.RebasePreparation{}, pullrequestwork.ErrStaleHead
	}
	var created bool
	operation, created, err = r.actions.BeginOperation(ctx, pullrequestlifecycle.Operation{RequestID: id, PullRequestID: p.ID, Kind: "rebase", ExpectedHead: headCommit, ExpectedInputs: pullrequestlifecycle.CaptureMutationInputs(current), Groups: []pullrequestlifecycle.FieldGroup{pullrequestlifecycle.TopologyGroup, pullrequestlifecycle.LifecycleGroup}})
	if err != nil {
		return pullrequestwork.RebasePreparation{}, err
	}
	if !created {
		return pullrequestwork.RebasePreparation{}, pullrequestlifecycle.ErrOperationInProgress
	}
	if operation.ExpectedHead != headCommit {
		_, _ = r.actions.CompleteOperation(context.WithoutCancel(ctx), operation.RequestID, "failed", pullrequestwork.ErrStaleHead.Error())
		return pullrequestwork.RebasePreparation{}, pullrequestwork.ErrStaleHead
	}

	finish := func(outcome string, err error) {
		message := ""
		if err != nil {
			message = err.Error()
		}
		_, _ = r.actions.CompleteOperation(context.WithoutCancel(ctx), operation.RequestID, outcome, message)
	}
	var result repositorybrowser.RebaseResult
	mutationAttempted := false
	err = withPullRequestGit(ctx, r.scheduler, r.repository.ID, func(ctx context.Context) error {
		target, err := r.rebaseSources(ctx, p, baseCommit, headCommit)
		if err != nil {
			return err
		}
		mutationAttempted = true
		result, err = r.manager.RebasePinned(ctx, target, repositorybrowser.RebaseRequest{BaseCommit: baseCommit, BaseBranch: p.BaseBranch, HeadBranch: p.HeadBranch, ExpectedHeadCommit: headCommit})
		return err
	})
	if err != nil && !mutationAttempted {
		finish("failed", err)
		return pullrequestwork.RebasePreparation{}, err
	}
	if errors.Is(err, repositorybrowser.ErrRebaseConflicts) {
		finish("failed", err)
		return pullrequestwork.RebasePreparation{HeadCommit: headCommit, Conflicts: true}, nil
	}
	if errors.Is(err, repositorybrowser.ErrStaleHead) {
		finish("failed", err)
		return pullrequestwork.RebasePreparation{}, pullrequestwork.ErrStaleHead
	}
	if err != nil {
		// The manager may have pushed before failing to update its local ref. Keep
		// the gate until recovery determines whether publication reached GitHub.
		finish("uncertain", err)
		if errors.Is(err, repositorybrowser.ErrPushRejected) {
			return pullrequestwork.RebasePreparation{}, pullrequestwork.ErrStaleHead
		}
		return pullrequestwork.RebasePreparation{}, err
	}
	if err := r.actions.RecordOperationStep(context.WithoutCancel(ctx), operation.RequestID, p.ID, "publication", pullrequestlifecycle.OperationStep{Status: "succeeded", HeadCommit: result.HeadCommit}); err != nil {
		return pullrequestwork.RebasePreparation{}, err
	}

	// Successful publication remains protected until the existing work committer
	// atomically projects this exact head and completes the operation.
	return pullrequestwork.RebasePreparation{OperationID: operation.RequestID, HeadCommit: result.HeadCommit, Rebased: result.Rebased}, nil
}
