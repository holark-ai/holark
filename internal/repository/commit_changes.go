package repository

import "context"

// CommitChanges describes a commit relative to its first parent. Parent is empty
// for an initial commit, whose files are all additions.
type CommitChanges struct {
	Commit string   `json:"commit"`
	Parent string   `json:"parent"`
	Files  []Change `json:"files"`
}

type CommitChangesStore interface {
	CommitChanges(context.Context, string) (CommitChanges, error)
}

func (s *Service) CommitChanges(ctx context.Context, ref string) (CommitChanges, error) {
	store, ok := s.store.(CommitChangesStore)
	if !ok {
		return CommitChanges{}, ErrRepositoryUnavailable
	}
	return store.CommitChanges(ctx, ref)
}
