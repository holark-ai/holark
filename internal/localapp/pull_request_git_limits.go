package localapp

import (
	"context"

	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/repository"
)

// Hold one shared permit around each outer Git operation. Adapter methods call
// the underlying service directly so a nested fetch never reacquires the permit.
func withPullRequestGit(ctx context.Context, scheduler *pullrequestlifecycle.RefreshCoordinator, repositoryID string, work func(context.Context) error) error {
	if scheduler == nil {
		return work(ctx)
	}
	return scheduler.WithGit(ctx, repositoryID, work)
}
func (r localPullRequestRepository) PrepareBranch(ctx context.Context, branch string) (repository.Preparation, error) {
	var result repository.Preparation
	err := withPullRequestGit(ctx, r.scheduler, r.Descriptor().ID, func(ctx context.Context) error {
		var err error
		result, err = r.Service.PrepareBranch(ctx, branch)
		return err
	})
	return result, err
}
func (r localPullRequestRepository) EnsureRemoteCommit(ctx context.Context, source, commit string) error {
	return withPullRequestGit(ctx, r.scheduler, r.Descriptor().ID, func(ctx context.Context) error { return r.Service.EnsureRemoteCommit(ctx, source, commit) })
}
func (r localPullRequestMergeRepository) PrepareBranch(ctx context.Context, branch string) (repository.Preparation, error) {
	var result repository.Preparation
	err := withPullRequestGit(ctx, r.scheduler, r.Descriptor().ID, func(ctx context.Context) error {
		var err error
		result, err = r.Service.PrepareBranch(ctx, branch)
		return err
	})
	return result, err
}

func (r localPullRequestRepository) ObserveBranches(ctx context.Context, source string, refs []string) (map[string]repository.BranchObservation, error) {
	var result map[string]repository.BranchObservation
	err := withPullRequestGit(ctx, r.scheduler, r.Descriptor().ID, func(ctx context.Context) error {
		var err error
		result, err = r.Service.ObserveBranches(ctx, source, refs)
		return err
	})
	return result, err
}
func (r localPullRequestRepository) RemoteBranchHead(ctx context.Context, source, branch string) (string, error) {
	var result string
	err := withPullRequestGit(ctx, r.scheduler, r.Descriptor().ID, func(ctx context.Context) error {
		var err error
		result, err = r.Service.RemoteBranchHead(ctx, source, branch)
		return err
	})
	return result, err
}

func (r localPullRequestRepository) CachePublishedBranch(ctx context.Context, source, ref string, observed repository.BranchObservation) error {
	return withPullRequestGit(ctx, r.scheduler, r.Descriptor().ID, func(ctx context.Context) error {
		return r.Service.CachePublishedBranch(ctx, source, ref, observed)
	})
}
