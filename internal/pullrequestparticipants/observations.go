package pullrequestparticipants

import (
	"context"
	"errors"

	"github.com/holark-ai/holark/internal/githubidentity"
)

var ErrStaleObservation = errors.New("participant observation superseded")
var ErrIncompleteObservation = errors.New("participant metadata is incomplete")

type acceptedObservation struct {
	token    uint64
	mutation bool
}

type memberObserver interface {
	ObserveProjectMember(context.Context, string, githubidentity.SourceMember) (string, error)
}

func (s *Service) BeginObservation() uint64                                  { return s.sequence.Add(1) }
func (s *Service) SetRefreshRequester(refresh func(context.Context, string)) { s.refresh = refresh }

// ApplyObservation shares the mutation lock and never clears a partial snapshot.
func (s *Service) ApplyObservation(ctx context.Context, id string, token uint64, remote RemoteParticipantSnapshot) error {
	needsRefresh := false
	_, err := withPRLock(s, id, func() (Snapshot, error) {
		if accepted, ok := s.accepted.Load(id); ok && token <= accepted.(acceptedObservation).token {
			needsRefresh = accepted.(acceptedObservation).mutation
			return Snapshot{}, ErrStaleObservation
		}
		if s.identities == nil || s.store == nil {
			return Snapshot{}, ErrUpdateFailed
		}
		if !remote.Complete {
			return Snapshot{}, ErrIncompleteObservation
		}
		target, err := s.readTarget(ctx, id)
		if err != nil {
			return Snapshot{}, err
		}
		if err = s.observeMembers(ctx, target.RepositoryID, remote); err != nil {
			return Snapshot{}, err
		}
		assignees, err := s.identities.ResolveProjectMemberIDs(ctx, target.RepositoryID, remote.AssigneeGitHubNodeIDs)
		if err != nil {
			return Snapshot{}, err
		}
		reviewers, err := s.identities.ResolveProjectMemberIDs(ctx, target.RepositoryID, remote.RequestedReviewerGitHubNodeIDs)
		if err != nil {
			return Snapshot{}, err
		}
		author := ""
		if remote.Author != nil {
			ids, e := s.identities.ResolveProjectMemberIDs(ctx, target.RepositoryID, []string{remote.Author.NodeID})
			if e != nil {
				return Snapshot{}, e
			}
			author = ids[0]
		}
		snapshot, err := s.store.ReplaceSnapshot(ctx, Snapshot{PullRequestID: id, AuthorHolarkID: author, AssigneeHolarkIDs: assignees, RequestedReviewerHolarkIDs: reviewers})
		if err == nil {
			s.accepted.Store(id, acceptedObservation{token: token})
		}
		return snapshot, err
	})
	if needsRefresh && s.refresh != nil {
		s.refresh(ctx, id)
	}
	return err
}
func (s *Service) observeMembers(ctx context.Context, repo string, remote RemoteParticipantSnapshot) error {
	observer, ok := s.identities.(memberObserver)
	if !ok {
		return nil
	}
	members := append([]githubidentity.SourceMember{}, remote.Members...)
	if remote.Author != nil {
		members = append(members, *remote.Author)
	}
	for _, member := range members {
		if _, err := observer.ObserveProjectMember(ctx, repo, member); err != nil {
			return err
		}
	}
	return nil
}
