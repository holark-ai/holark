package pullrequestcomments

import (
	"context"
	"sync"
)

// MemoryRepository supports store-less composition. Runtime composition uses
// sqliteadapter.Store.
type MemoryRepository struct {
	mu             sync.RWMutex
	comments       map[string]Comment
	reviewReceipts map[reviewImportKey]Comment
}

type reviewImportKey struct {
	reviewID string
	index    int
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{comments: make(map[string]Comment), reviewReceipts: make(map[reviewImportKey]Comment)}
}

func (repository *MemoryRepository) Insert(_ context.Context, comment Comment) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.comments[comment.ID]; exists {
		return ErrInvalidComment
	}
	if comment.SourceWorkerID != "" {
		for _, existing := range repository.comments {
			if existing.SourceWorkerID == comment.SourceWorkerID {
				return ErrInvalidComment
			}
		}
	}
	if comment.SourceReviewID != "" {
		key := reviewImportKey{comment.SourceReviewID, comment.SourceReviewIndex}
		if _, exists := repository.reviewReceipts[key]; exists {
			return ErrInvalidComment
		}
		repository.reviewReceipts[key] = publicComment(comment)
	}
	repository.comments[comment.ID] = publicComment(comment)
	return nil
}

func (repository *MemoryRepository) Get(_ context.Context, id string) (Comment, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	comment, ok := repository.comments[id]
	if !ok {
		return Comment{}, ErrCommentNotFound
	}
	return publicComment(comment), nil
}

func (repository *MemoryRepository) Update(_ context.Context, comment Comment) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, ok := repository.comments[comment.ID]; !ok {
		return ErrCommentNotFound
	}
	repository.comments[comment.ID] = publicComment(comment)
	return nil
}

func (repository *MemoryRepository) SaveDraftBatch(_ context.Context, comments []Comment, _ []*PublicationJob) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for _, comment := range comments {
		if _, ok := repository.comments[comment.ID]; !ok {
			return ErrCommentNotFound
		}
	}
	for _, comment := range comments {
		repository.comments[comment.ID] = publicComment(comment)
	}
	return nil
}

func (repository *MemoryRepository) Delete(_ context.Context, id string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, ok := repository.comments[id]; !ok {
		return ErrCommentNotFound
	}
	delete(repository.comments, id)
	return nil
}

func (repository *MemoryRepository) ListByPullRequest(_ context.Context, pullRequestID string) ([]Comment, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]Comment, 0)
	for _, comment := range repository.comments {
		if comment.PullRequestID == pullRequestID {
			result = append(result, publicComment(comment))
		}
	}
	sortComments(result)
	return result, nil
}

func (repository *MemoryRepository) UnresolvedCount(_ context.Context, pullRequestID string) (int, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	count := 0
	for _, comment := range repository.comments {
		if comment.PullRequestID == pullRequestID && comment.Status == Unresolved && comment.PublicationState != PublicationDraft {
			count++
		}
	}
	return count, nil
}

func (repository *MemoryRepository) UnresolvedCounts(_ context.Context, pullRequestIDs []string) (map[string]int, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	counts := make(map[string]int, len(pullRequestIDs))
	for _, pullRequestID := range pullRequestIDs {
		counts[pullRequestID] = 0
	}
	for _, comment := range repository.comments {
		if comment.Status == Unresolved && comment.PublicationState != PublicationDraft {
			if _, requested := counts[comment.PullRequestID]; requested {
				counts[comment.PullRequestID]++
			}
		}
	}
	return counts, nil
}

func (repository *MemoryRepository) HasChildReplies(_ context.Context, commentID string) (bool, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, comment := range repository.comments {
		if comment.ParentCommentID == commentID {
			return true, nil
		}
	}
	return false, nil
}

func (repository *MemoryRepository) FindByExternalIdentity(_ context.Context, provider, externalID string) (Comment, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, comment := range repository.comments {
		if comment.ProviderIdentity.Provider == provider && comment.ProviderIdentity.ExternalID == externalID {
			return publicComment(comment), nil
		}
	}
	return Comment{}, ErrCommentNotFound
}

func (repository *MemoryRepository) UpsertExternal(_ context.Context, incoming Comment) (Comment, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for id, existing := range repository.comments {
		if existing.ProviderIdentity.Provider == incoming.ProviderIdentity.Provider && existing.ProviderIdentity.ExternalID == incoming.ProviderIdentity.ExternalID {
			incoming.ID, incoming.CreatedAt = id, existing.CreatedAt
			repository.comments[id] = publicComment(incoming)
			return publicComment(incoming), nil
		}
	}
	repository.comments[incoming.ID] = publicComment(incoming)
	return publicComment(incoming), nil
}

func (repository *MemoryRepository) FindReviewImportReceipt(_ context.Context, reviewID string, index int) (Comment, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if comment, ok := repository.reviewReceipts[reviewImportKey{reviewID, index}]; ok {
		return publicComment(comment), nil
	}
	return Comment{}, ErrCommentNotFound
}

func (repository *MemoryRepository) FindBySourceWorkerID(_ context.Context, workerID string) (Comment, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, comment := range repository.comments {
		if comment.SourceWorkerID == workerID {
			return publicComment(comment), nil
		}
	}
	return Comment{}, ErrCommentNotFound
}
