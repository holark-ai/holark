package pullrequestcomments

import (
	"context"
	"errors"
)

// A batch commits all draft state changes and publication intents together.
// Delivery then uses the existing durable, idempotent comment publisher.
type draftStore interface {
	SaveDraftBatch(context.Context, []Comment, []*PublicationJob) error
}

func (s *Service) PublishDrafts(ctx context.Context, id, head string, ids []string) ([]Comment, error) {
	if len(ids) == 0 || len(ids) > 250 || head == "" {
		return nil, ErrInvalidComment
	}
	unlock := s.locks.lock(id)
	defer unlock()
	selected := make(map[string]bool, len(ids))
	for _, commentID := range ids {
		selected[commentID] = true
	}
	drafts := make([]Comment, 0, len(ids))
	processed := make(map[string]bool, len(ids))
	for _, commentID := range ids {
		if processed[commentID] {
			continue
		}
		processed[commentID] = true
		comment, err := s.comments.Get(ctx, commentID)
		if err != nil {
			return nil, err
		}
		if comment.PullRequestID != id {
			return nil, ErrInvalidComment
		}
		if comment.PublicationState != PublicationDraft {
			continue // A retried batch must not enqueue accepted comments again.
		}
		drafts = append(drafts, comment)
	}
	// Acceptance is durable even if the response was lost and the PR has since
	// changed. Only comments that are still drafts need freshness checks.
	if len(drafts) == 0 {
		return s.ListByPullRequest(ctx, id)
	}
	target, err := s.pullRequests.GetCommentTarget(ctx, id)
	if err != nil {
		return nil, err
	}
	if target.Status != PullRequestWIP && target.Status != PullRequestDraft && target.Status != PullRequestOpen {
		return nil, ErrReadOnly
	}
	if !target.ComparisonCurrent {
		return nil, ErrComparisonNotReady
	}
	if target.HeadCommit != head {
		return nil, ErrStaleDraft
	}
	comments := make([]Comment, 0, len(drafts))
	jobs := make([]*PublicationJob, 0, len(drafts))
	for _, comment := range drafts {
		if comment.OriginalHeadCommit != head {
			return nil, ErrStaleDraft
		}
		if comment.ParentCommentID != "" && !selected[comment.ParentCommentID] {
			parent, err := s.comments.Get(ctx, comment.ParentCommentID)
			if err != nil {
				return nil, err
			}
			if parent.PublicationState == PublicationDraft {
				return nil, ErrInvalidParent
			}
		}
		comment.PublicationState = PublicationLocal
		comment.UpdatedAt = s.now().UTC()
		var job *PublicationJob
		if s.provider != nil {
			job, err = s.provider.mutationJob(ctx, target, "create", comment)
			if err != nil {
				return nil, err
			}
			if job != nil {
				comment.PublicationState = PublicationPending
			}
		}
		comments = append(comments, comment)
		jobs = append(jobs, job)
	}
	if len(comments) > 0 {
		store, ok := s.comments.(draftStore)
		if !ok {
			return nil, errors.New("atomic draft publication is unavailable")
		}
		if err := store.SaveDraftBatch(ctx, comments, jobs); err != nil {
			return nil, err
		}
		s.notifyPublication()
	}
	return s.ListByPullRequest(ctx, id)
}
