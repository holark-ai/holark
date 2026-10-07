package holons

import "context"

type workspaceBaseResolver interface {
	ResolveWorkspaceBase(context.Context, string, string) (string, error)
}

// ChangeBaseBranch changes the comparison and future PR base without rewriting
// the workspace. The session-start commit continues to describe this session.
func (s *Service) ChangeBaseBranch(ctx context.Context, id, branch string) (Holon, error) {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	branch = CanonicalBaseBranch(branch)
	if branch == "" || h.ReadOnly || h.ArchivedAt != nil || h.EndRequested || h.WorktreePath == "" || h.WorktreeBranch == "" {
		return Holon{}, ErrInvalid
	}
	if h.RebaseAttempt.Active() {
		return Holon{}, ErrRebaseActive
	}
	resolver, ok := s.repository.(workspaceBaseResolver)
	if !ok {
		return Holon{}, ErrInvalid
	}
	base, err := resolver.ResolveWorkspaceBase(ctx, id, branch)
	if err != nil {
		return Holon{}, err
	}
	if h.WorkSessionStartCommit == "" {
		h.WorkSessionStartCommit = h.BaseCommit
	}
	h.BaseBranch, h.BaseCommit = branch, base
	if err := s.store.Update(ctx, h); err != nil {
		return Holon{}, err
	}
	return h, nil
}
