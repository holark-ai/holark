package repository

import "context"

// RemoteCommitStore imports an immutable commit from its owning repository.
// The caller keeps the observed commit ID as its comparison input.
type RemoteCommitStore interface {
	EnsureRemoteCommit(context.Context, string, string) error
}

func (s *Service) EnsureRemoteCommit(ctx context.Context, repositoryURL, commit string) error {
	store, ok := s.store.(RemoteCommitStore)
	if !ok {
		return ErrRepositoryUnavailable
	}
	return store.EnsureRemoteCommit(ctx, repositoryURL, commit)
}

// RemoteBranchStore reads the actual owning branch, including fork branches.
type RemoteBranchStore interface {
	RemoteBranchHead(context.Context, string, string) (string, error)
}

func (s *Service) RemoteBranchHead(ctx context.Context, repositoryURL, branch string) (string, error) {
	store, ok := s.store.(RemoteBranchStore)
	if !ok {
		return "", ErrRepositoryUnavailable
	}
	return store.RemoteBranchHead(ctx, repositoryURL, branch)
}
