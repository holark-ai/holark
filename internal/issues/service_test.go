package issues

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestServiceCreatesGetsAndListsIssues(t *testing.T) {
	createdAt := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	store := newFakeStore()
	store.issues["issue-old"] = Issue{
		ID: "issue-old", RepositoryID: "project", Title: "Older", Status: IssueOpen,
		SyncData:  json.RawMessage(`{"source":"old"}`),
		CreatedAt: createdAt.Add(-time.Hour), UpdatedAt: createdAt.Add(-time.Hour),
	}
	store.issues["issue-other"] = Issue{
		ID: "issue-other", RepositoryID: "other", Title: "Other", Status: IssueOpen,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	service := NewService(store)
	service.now = func() time.Time { return createdAt }
	service.newID = func() (string, error) { return "issue-new", nil }

	created, err := service.Create(t.Context(), "project", "New issue", "Details")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "issue-new" || created.Status != IssueOpen || !created.CreatedAt.Equal(createdAt) || !created.UpdatedAt.Equal(createdAt) {
		t.Fatalf("created issue = %+v", created)
	}
	if string(created.SyncData) != "{}" || created.LinkedPullRequestIDs == nil {
		t.Fatalf("created defaults = %+v", created)
	}

	listed, err := service.List(t.Context(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].ID != "issue-new" || listed[1].ID != "issue-old" {
		t.Fatalf("listed issues = %+v", listed)
	}
	if err := service.LinkPullRequest(t.Context(), "issue-new", "pr-one"); err != nil {
		t.Fatal(err)
	}
	linked := store.issues["issue-new"]
	if len(linked.LinkedPullRequestIDs) != 1 {
		t.Fatalf("linked issue = %+v", linked)
	}

	got, err := service.Get(t.Context(), "issue-old")
	if err != nil {
		t.Fatal(err)
	}
	got.SyncData[0] = '['
	again, err := service.Get(t.Context(), "issue-old")
	if err != nil {
		t.Fatal(err)
	}
	if string(again.SyncData) != `{"source":"old"}` {
		t.Fatalf("stored issue was mutated through public value: %+v", again)
	}
}

func TestServiceClosesAndReopensIssue(t *testing.T) {
	initial := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	store := newFakeStore()
	store.issues["issue-one"] = Issue{
		ID: "issue-one", RepositoryID: "project", Status: IssueOpen, CreatedAt: initial, UpdatedAt: initial,
	}
	service := NewService(store)
	now := initial.Add(time.Hour)
	service.now = func() time.Time { return now }

	closed, err := service.Close(t.Context(), "issue-one")
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != IssueClosed || closed.ClosedAt == nil || !closed.ClosedAt.Equal(now) || !closed.UpdatedAt.Equal(now) {
		t.Fatalf("closed issue = %+v", closed)
	}

	now = now.Add(time.Hour)
	reopened, err := service.Reopen(t.Context(), "issue-one")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status != IssueOpen || reopened.ClosedAt != nil || !reopened.UpdatedAt.Equal(now) {
		t.Fatalf("reopened issue = %+v", reopened)
	}
	if _, err := service.Close(t.Context(), "missing"); !errors.Is(err, ErrIssueNotFound) {
		t.Fatalf("close missing error = %v", err)
	}
}

func TestServiceUpsertsSyncedIssues(t *testing.T) {
	now := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	oldCreatedAt := now.Add(-24 * time.Hour)
	store := newFakeStore()
	store.issues["issue-existing"] = Issue{
		ID: "issue-existing", RepositoryID: "project", Title: "Old", Status: IssueOpen,
		SyncProvider: "github", SyncExternalID: "github:owner/repo#1", SyncData: json.RawMessage(`{}`),
		LinkedPullRequestIDs: []string{"pr-one"},
		CreatedAt:            oldCreatedAt, UpdatedAt: oldCreatedAt,
	}
	service := NewService(store)
	service.now = func() time.Time { return now }
	service.newID = func() (string, error) { return "issue-imported", nil }

	imported, updated, err := service.UpsertSynced(t.Context(), "project", []Issue{
		{Title: "Imported", SyncProvider: "github", SyncExternalID: "github:owner/repo#2"},
		{Title: "Updated", SyncProvider: "github", SyncExternalID: "github:owner/repo#1", UpdatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if imported != 1 || updated != 1 {
		t.Fatalf("counts = %d imported, %d updated", imported, updated)
	}

	inserted := store.issues["issue-imported"]
	if inserted.RepositoryID != "project" || inserted.Status != IssueOpen || !inserted.CreatedAt.Equal(now) || !inserted.UpdatedAt.Equal(now) || string(inserted.SyncData) != "{}" {
		t.Fatalf("imported issue = %+v", inserted)
	}
	existing := store.issues["issue-existing"]
	if existing.Title != "Updated" || !existing.CreatedAt.Equal(oldCreatedAt) || len(existing.LinkedPullRequestIDs) != 1 || existing.LinkedPullRequestIDs[0] != "pr-one" {
		t.Fatalf("updated issue = %+v", existing)
	}

	imported, updated, err = service.UpsertSynced(t.Context(), "project", []Issue{{Title: "Invalid"}})
	if imported != 0 || updated != 0 || !errors.Is(err, ErrSyncIdentityRequired) {
		t.Fatalf("invalid sync result = %d, %d, %v", imported, updated, err)
	}
}

func TestServicePropagatesPersistenceFailures(t *testing.T) {
	persistenceErr := errors.New("persistence failed")
	tests := []struct {
		name      string
		operation string
		call      func(*Service) error
	}{
		{name: "insert", operation: "insert", call: func(service *Service) error {
			_, err := service.Create(t.Context(), "project", "Title", "Body")
			return err
		}},
		{name: "update", operation: "update", call: func(service *Service) error {
			_, err := service.Close(t.Context(), "issue-one")
			return err
		}},
		{name: "upsert synced", operation: "upsert", call: func(service *Service) error {
			_, _, err := service.UpsertSynced(t.Context(), "project", []Issue{{SyncProvider: "github", SyncExternalID: "github:owner/repo#1"}})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeStore()
			store.issues["issue-one"] = Issue{ID: "issue-one", RepositoryID: "project", Status: IssueOpen}
			store.fail[test.operation] = persistenceErr
			service := NewService(store)
			service.newID = func() (string, error) { return "issue-new", nil }
			if err := test.call(service); !errors.Is(err, persistenceErr) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type fakeStore struct {
	issues map[string]Issue
	fail   map[string]error
}

func newFakeStore() *fakeStore {
	return &fakeStore{issues: make(map[string]Issue), fail: make(map[string]error)}
}

func (store *fakeStore) Get(_ context.Context, id string) (Issue, error) {
	if err := store.fail["get"]; err != nil {
		return Issue{}, err
	}
	issue, ok := store.issues[id]
	if !ok {
		return Issue{}, ErrIssueNotFound
	}
	return publicIssue(issue), nil
}

func (store *fakeStore) List(_ context.Context, projectID string) ([]Issue, error) {
	if err := store.fail["list"]; err != nil {
		return nil, err
	}
	result := []Issue{}
	for _, issue := range store.issues {
		if issue.RepositoryID == projectID {
			result = append(result, publicIssue(issue))
		}
	}
	return result, nil
}

func (store *fakeStore) Insert(_ context.Context, issue Issue) error {
	if err := store.fail["insert"]; err != nil {
		return err
	}
	store.issues[issue.ID] = publicIssue(issue)
	return nil
}

func (store *fakeStore) Update(_ context.Context, issue Issue) error {
	if err := store.fail["update"]; err != nil {
		return err
	}
	if _, ok := store.issues[issue.ID]; !ok {
		return ErrIssueNotFound
	}
	store.issues[issue.ID] = publicIssue(issue)
	return nil
}

func (store *fakeStore) UpsertSynced(_ context.Context, issue Issue) (bool, error) {
	if err := store.fail["upsert"]; err != nil {
		return false, err
	}
	for id, existing := range store.issues {
		if existing.RepositoryID == issue.RepositoryID && existing.SyncProvider == issue.SyncProvider && existing.SyncExternalID == issue.SyncExternalID {
			issue.ID = id
			store.issues[id] = publicIssue(issue)
			return false, nil
		}
	}
	store.issues[issue.ID] = publicIssue(issue)
	return true, nil
}
func (store *fakeStore) UpsertLabelCatalog(_ context.Context, _ string, labels []Label) ([]Label, error) {
	return append([]Label(nil), labels...), nil
}

func (store *fakeStore) DeleteSyncedAbsent(_ context.Context, projectID, syncProvider string, present []string) (int, error) {
	keep := make(map[string]struct{}, len(present))
	for _, externalID := range present {
		keep[externalID] = struct{}{}
	}
	deleted := 0
	for id, issue := range store.issues {
		if issue.RepositoryID != projectID || issue.SyncProvider != syncProvider {
			continue
		}
		if _, ok := keep[issue.SyncExternalID]; ok {
			continue
		}
		delete(store.issues, id)
		deleted++
	}
	return deleted, nil
}

func (store *fakeStore) LinkPullRequest(_ context.Context, issueID, pullRequestID string) error {
	if err := store.fail["link_pull_request"]; err != nil {
		return err
	}
	issue := store.issues[issueID]
	issue.LinkedPullRequestIDs = append(issue.LinkedPullRequestIDs, pullRequestID)
	store.issues[issueID] = issue
	return nil
}
