package pullrequestlifecycle

import (
	"context"
	"strings"

	"github.com/holark-ai/holark/internal/repository"
)

// TopologyRepository exposes only the Git operations needed to derive the
// current comparison boundary for a pull request.
type TopologyRepository interface {
	EnsureRemoteCommit(context.Context, string, string) error
	RemoteBranchHead(context.Context, string, string) (string, error)
	PrepareBranch(context.Context, string) (repository.Preparation, error)
	MergeBase(context.Context, string, string) (string, error)
}

type pullRequestTopologyReconciler struct {
	repository TopologyRepository
	bases      map[string]repository.Preparation
}

func newPullRequestTopologyReconciler(value TopologyRepository) *pullRequestTopologyReconciler {
	return &pullRequestTopologyReconciler{repository: value, bases: make(map[string]repository.Preparation)}
}

func (reconciler *pullRequestTopologyReconciler) Reconcile(ctx context.Context, pullRequest PullRequest) (PullRequest, error) {
	if reconciler == nil || reconciler.repository == nil {
		return PullRequest{}, repository.ErrRepositoryUnavailable
	}
	// Published observations already identify both immutable inputs. Preparing a
	// moving branch here could combine a later base with an earlier head snapshot.
	if pullRequest.SyncExternalID != "" && strings.TrimSpace(pullRequest.BaseCommit) != "" {
		observation := DecodeGitHubObservation(pullRequest.SyncData)
		if err := reconciler.repository.EnsureRemoteCommit(ctx, observation.BaseRepositoryURL, pullRequest.BaseCommit); err != nil {
			return PullRequest{}, err
		}
		if err := reconciler.repository.EnsureRemoteCommit(ctx, observation.HeadRepositoryURL, pullRequest.HeadCommit); err != nil {
			return PullRequest{}, err
		}
		actual, err := reconciler.repository.RemoteBranchHead(ctx, observation.HeadRepositoryURL, pullRequest.HeadBranch)
		if err != nil {
			return PullRequest{}, err
		}
		pullRequest.VerifiedHead = actual == pullRequest.HeadCommit
		diffBase, err := reconciler.repository.MergeBase(ctx, pullRequest.BaseCommit, pullRequest.HeadCommit)
		if err != nil {
			return PullRequest{}, err
		}
		if strings.TrimSpace(diffBase) == "" {
			return PullRequest{}, repository.ErrRepositoryUnavailable
		}
		pullRequest.BaseBranch = CanonicalBaseBranch(pullRequest.BaseBranch)
		pullRequest.DiffBaseCommit = strings.TrimSpace(diffBase)
		return pullRequest, nil
	}
	branch := CanonicalBaseBranch(pullRequest.BaseBranch)
	prepared, ok := reconciler.bases[branch]
	if !ok {
		var err error
		prepared, err = reconciler.repository.PrepareBranch(ctx, branch)
		if err != nil {
			return PullRequest{}, err
		}
		prepared.Branch = CanonicalBaseBranch(prepared.Branch)
		prepared.Commit = strings.TrimSpace(prepared.Commit)
		if prepared.Branch == "" || prepared.Commit == "" {
			return PullRequest{}, repository.ErrRepositoryUnavailable
		}
		reconciler.bases[branch] = prepared
	}
	diffBase, err := reconciler.repository.MergeBase(ctx, prepared.Commit, pullRequest.HeadCommit)
	if err != nil {
		return PullRequest{}, err
	}
	diffBase = strings.TrimSpace(diffBase)
	if diffBase == "" {
		return PullRequest{}, repository.ErrRepositoryUnavailable
	}
	pullRequest.BaseBranch = prepared.Branch
	pullRequest.BaseCommit = prepared.Commit
	pullRequest.DiffBaseCommit = diffBase
	return pullRequest, nil
}
