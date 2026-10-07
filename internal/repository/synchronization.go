package repository

import "context"

type SynchronizationInspection struct {
	RebaseBranch, RebaseTarget                string
	Branch, HeadCommit                        string
	Dirty, Rebasing, Incorporated, Conflicted bool
}

func (s *Service) InspectSynchronization(ctx context.Context, id, target string, ignored []string) (SynchronizationInspection, error) {
	r, ok := s.store.(interface {
		InspectSynchronization(context.Context, string, string, []string) (SynchronizationInspection, error)
	})
	if !ok {
		return SynchronizationInspection{}, ErrInvalidWorkspace
	}
	return r.InspectSynchronization(ctx, id, target, ignored)
}
