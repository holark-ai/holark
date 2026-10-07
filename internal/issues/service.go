package issues

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrSyncIdentityRequired = errors.New("sync provider and external id are required")

type Service struct {
	mu    sync.Mutex
	store Store
	now   func() time.Time
	newID func() (string, error)
}

func NewService(store Store) *Service {
	return &Service{
		store: store,
		now:   func() time.Time { return time.Now().UTC() },
		newID: newIssueID,
	}
}

func (service *Service) Create(ctx context.Context, projectID, title, body string) (Issue, error) {
	service.mu.Lock()
	defer service.mu.Unlock()

	id, err := service.newID()
	if err != nil {
		return Issue{}, err
	}
	now := service.now().UTC()
	issue := Issue{
		ID:                   id,
		RepositoryID:         projectID,
		Title:                title,
		Body:                 body,
		Status:               IssueOpen,
		SyncData:             json.RawMessage(`{}`),
		AssigneeHolarkIDs:    []string{},
		LinkedPullRequestIDs: []string{},
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := service.store.Insert(ctx, issue); err != nil {
		return Issue{}, err
	}
	return publicIssue(issue), nil
}

func (service *Service) Get(ctx context.Context, id string) (Issue, error) {
	issue, err := service.store.Get(ctx, id)
	if err != nil {
		return Issue{}, err
	}
	return publicIssue(issue), nil
}

func (service *Service) List(ctx context.Context, projectID string) ([]Issue, error) {
	stored, err := service.store.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	result := make([]Issue, len(stored))
	for index := range stored {
		result[index] = publicIssue(stored[index])
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result, nil
}

func (service *Service) Close(ctx context.Context, id string) (Issue, error) {
	return service.setStatus(ctx, id, IssueClosed)
}

func (service *Service) Reopen(ctx context.Context, id string) (Issue, error) {
	return service.setStatus(ctx, id, IssueOpen)
}

func (service *Service) setStatus(ctx context.Context, id string, status IssueStatus) (Issue, error) {
	service.mu.Lock()
	defer service.mu.Unlock()

	issue, err := service.store.Get(ctx, id)
	if err != nil {
		return Issue{}, err
	}
	now := service.now().UTC()
	issue.Status = status
	issue.UpdatedAt = now
	if status == IssueClosed {
		issue.ClosedAt = &now
	} else {
		issue.ClosedAt = nil
	}
	if err := service.store.Update(ctx, issue); err != nil {
		return Issue{}, err
	}
	return publicIssue(issue), nil
}

func (service *Service) UpsertSynced(ctx context.Context, projectID string, incoming []Issue) (int, int, error) {
	service.mu.Lock()
	defer service.mu.Unlock()

	stored, err := service.store.List(ctx, projectID)
	if err != nil {
		return 0, 0, err
	}
	bySyncIdentity := make(map[string]Issue, len(stored)+len(incoming))
	for _, issue := range stored {
		if issue.SyncProvider != "" && issue.SyncExternalID != "" {
			bySyncIdentity[syncIdentity(issue)] = issue
		}
	}

	imported := 0
	updated := 0
	for _, issue := range incoming {
		if issue.SyncProvider == "" || issue.SyncExternalID == "" {
			return imported, updated, ErrSyncIdentityRequired
		}
		issue.RepositoryID = projectID
		if issue.Status == "" {
			issue.Status = IssueOpen
		}
		if issue.CreatedAt.IsZero() {
			issue.CreatedAt = service.now().UTC()
		}
		if issue.UpdatedAt.IsZero() {
			issue.UpdatedAt = issue.CreatedAt
		}
		issue = publicIssue(issue)

		key := syncIdentity(issue)
		if existing, ok := bySyncIdentity[key]; ok {
			issue.ID = existing.ID
			issue.CreatedAt = existing.CreatedAt
			issue.LinkedPullRequestIDs = normalizeStrings(existing.LinkedPullRequestIDs)
		} else {
			issue.ID, err = service.newID()
			if err != nil {
				return imported, updated, err
			}
		}

		inserted, err := service.store.UpsertSynced(ctx, issue)
		if err != nil {
			return imported, updated, err
		}
		bySyncIdentity[key] = issue
		if inserted {
			imported++
		} else {
			updated++
		}
	}
	return imported, updated, nil
}

// StoreSynced persists one canonical external issue and returns the stored projection.
func (service *Service) StoreSynced(ctx context.Context, projectID string, issue Issue) (Issue, bool, error) {
	imported, _, err := service.UpsertSynced(ctx, projectID, []Issue{issue})
	if err != nil {
		return Issue{}, false, err
	}
	listed, err := service.List(ctx, projectID)
	if err != nil {
		return Issue{}, false, err
	}
	for _, stored := range listed {
		if stored.SyncProvider == issue.SyncProvider && stored.SyncExternalID == issue.SyncExternalID {
			return stored, imported == 1, nil
		}
	}
	return Issue{}, false, ErrIssueNotFound
}

func (service *Service) UpsertLabelCatalog(ctx context.Context, projectID string, labels []Label) ([]Label, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.store.UpsertLabelCatalog(ctx, projectID, labels)
	if err != nil {
		return nil, err
	}
	return normalizeLabels(stored), nil
}

func (service *Service) DeleteSyncedAbsent(ctx context.Context, projectID, syncProvider string, presentExternalIDs []string) (int, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.store.DeleteSyncedAbsent(ctx, projectID, syncProvider, presentExternalIDs)
}

func (service *Service) LinkPullRequest(ctx context.Context, issueID, pullRequestID string) error {
	service.mu.Lock()
	defer service.mu.Unlock()

	issue, err := service.store.Get(ctx, issueID)
	if err != nil {
		return err
	}
	if contains(issue.LinkedPullRequestIDs, pullRequestID) {
		return nil
	}
	return service.store.LinkPullRequest(ctx, issueID, pullRequestID)
}

func newIssueID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "issue-" + hex.EncodeToString(value), nil
}

func syncIdentity(issue Issue) string {
	return issue.SyncProvider + "\x00" + issue.SyncExternalID
}

func normalizeStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
}

func publicIssue(issue Issue) Issue {
	issue.Labels = normalizeLabels(issue.Labels)
	issue.AssigneeHolarkIDs = uniqueStrings(issue.AssigneeHolarkIDs)
	issue.LinkedPullRequestIDs = normalizeStrings(issue.LinkedPullRequestIDs)
	issue.SyncData = append(json.RawMessage(nil), issue.SyncData...)
	if len(issue.SyncData) == 0 {
		issue.SyncData = json.RawMessage(`{}`)
	}
	if issue.ClosedAt != nil {
		closedAt := *issue.ClosedAt
		issue.ClosedAt = &closedAt
	}
	if issue.SyncedAt != nil {
		syncedAt := *issue.SyncedAt
		issue.SyncedAt = &syncedAt
	}
	return issue
}

func normalizeLabels(values []Label) []Label {
	if len(values) == 0 {
		return []Label{}
	}
	return append([]Label(nil), values...)
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if result == nil {
		return []string{}
	}
	return result
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
