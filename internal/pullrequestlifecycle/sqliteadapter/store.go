// Package sqliteadapter owns the durable local pull-request catalog.
package sqliteadapter

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	branchstore "github.com/holark-ai/holark/internal/repository/sqliteadapter"
)

var operationProcessOwner = newID()

type Store struct {
	actionCompletionMu    sync.RWMutex
	instanceID            string
	actionCompletion      func(string)
	publicationCompletion func(string)
	db                    *sql.DB
	mu                    sync.Mutex
}

func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if err := branchstore.CreatePublishedBranchSchema(ctx, db); err != nil {
		return nil, err
	}
	if err := database.CreateSchema(ctx, db, `
create table if not exists pull_request_operations (
 request_id text primary key,
 pull_request_id text,
 holon_id text,
 document text not null
);
create index if not exists pull_request_active_operations
 on pull_request_operations(pull_request_id) where json_extract(document, '$.status') in ('running','uncertain');
create table if not exists pull_request_observation_sequence (
 id integer primary key check(id=1), value integer not null
);
insert or ignore into pull_request_observation_sequence(id,value) values(1,0);
create table if not exists pull_request_catalog (
  id text primary key,
  repository_id text not null,
  provider text,
  external_id text,
  document text not null,
  unique(repository_id, provider, external_id)
);
create table if not exists pull_requests (
  id text primary key,
  repository_id text not null references repositories(id),
  title text not null,
  description text not null,
  status text not null,
  base_ref text not null default '{}',
  head_ref text not null default '{}',
  comparison_state text not null default 'unavailable',
  comparison_snapshot text,
  base_branch text not null,
  base_commit text not null,
  head_branch text not null,
  head_commit text not null,
  diff_base_commit text,
  sync_provider text,
  sync_external_id text,
  sync_data text not null default '{}',
  created_at text not null,
  updated_at text not null,
  closed_at text,
  synced_at text
);
create unique index if not exists pull_requests_sync_identity
  on pull_requests(repository_id, sync_provider, sync_external_id)
  where sync_provider is not null and sync_external_id is not null;`); err != nil {
		return nil, err
	}
	return &Store{db: db, instanceID: operationProcessOwner}, nil
}

func (s *Store) ListPullRequests(repositoryID string) []pullrequestlifecycle.PullRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`select repository_id, document from pull_request_catalog where repository_id = ?`, repositoryID)
	if err != nil {
		return []pullrequestlifecycle.PullRequest{}
	}
	defer rows.Close()
	result := []pullrequestlifecycle.PullRequest{}
	for rows.Next() {
		var repositoryID, raw string
		if rows.Scan(&repositoryID, &raw) != nil {
			continue
		}
		if pr, err := decodePullRequest(repositoryID, raw); err == nil {
			result = append(result, pr)
		}
	}
	rows.Close()
	active, err := activeOperations(context.Background(), s.db, "pull_request_id in (select id from pull_request_catalog where repository_id=?)", repositoryID)
	if err != nil {
		return nil
	}
	for i := range result {
		result[i].Operations = active[result[i].ID]
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result
}

func (s *Store) GetPullRequest(id string) (pullrequestlifecycle.PullRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get(context.Background(), id)
}

func (s *Store) get(ctx context.Context, id string) (pullrequestlifecycle.PullRequest, bool) {
	var repositoryID, raw string
	if s.db.QueryRowContext(ctx, `select repository_id, document from pull_request_catalog where id = ?`, id).Scan(&repositoryID, &raw) != nil {
		return pullrequestlifecycle.PullRequest{}, false
	}
	pr, err := decodePullRequest(repositoryID, raw)
	if err != nil {
		return pullrequestlifecycle.PullRequest{}, false
	}
	active, err := activeOperations(ctx, s.db, "pull_request_id=?", id)
	if err != nil {
		return pullrequestlifecycle.PullRequest{}, false
	}
	pr.Operations = active[id]
	return pr, true
}

func decodePullRequest(repositoryID, raw string) (pullrequestlifecycle.PullRequest, error) {
	var pr pullrequestlifecycle.PullRequest
	if err := json.Unmarshal([]byte(raw), &pr); err != nil {
		return pullrequestlifecycle.PullRequest{}, err
	}
	pr.RepositoryID = repositoryID
	pr.BaseBranch = pullrequestlifecycle.CanonicalBaseBranch(pr.BaseBranch)
	return pr, nil
}

func saveInTransaction(ctx context.Context, tx *sql.Tx, pr *pullrequestlifecycle.PullRequest) error {
	var repositoryID, document string
	err := tx.QueryRowContext(ctx, `select repository_id, document from pull_request_catalog where id=?`, pr.ID).Scan(&repositoryID, &document)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var previous pullrequestlifecycle.PullRequest
	if err == nil {
		previous, err = decodePullRequest(repositoryID, document)
		if err != nil {
			return err
		}
	}
	if err := bindComparisonReferences(ctx, tx, pr, previous); err != nil {
		return err
	}
	if pr.ComparisonState == "" {
		pr.ComparisonState = pullrequestlifecycle.ComparisonUnavailable
	}
	// Every writer, including transactions owned by publication and rebase, advances
	// the same versions. No caller can restore an older revision.
	pr.ViewRevision = previous.ViewRevision
	pr.LifecycleGeneration = max(pr.LifecycleGeneration, previous.LifecycleGeneration)
	pr.TopologyGeneration = max(pr.TopologyGeneration, previous.TopologyGeneration)
	pr.MetadataGeneration = max(pr.MetadataGeneration, previous.MetadataGeneration)
	if previous.Status != pr.Status {
		pr.LifecycleGeneration++
	}
	// Cleanup belongs to one terminal lifecycle. Repeated observations must
	// preserve its completion, while reopening the PR allows cleanup again.
	if pr.Status.Active() || previous.Status != pr.Status {
		pr.ActivityRetired = false
	} else {
		pr.ActivityRetired = pr.ActivityRetired || previous.ActivityRetired
	}
	// Retain a completed comparison across a lifecycle-only change only after
	// validating its immutable branch pair and mutation evidence in this tx.
	// In-flight preparation still has to match the new lifecycle generation.
	if pr.Status.Active() && pr.HasCurrentComparison() && pr.Comparison.Inputs.LifecycleGeneration != pr.LifecycleGeneration {
		pair, err := captureComparison(ctx, tx, *pr)
		expected := pr.Comparison.Inputs
		expected.LifecycleGeneration = pr.LifecycleGeneration
		if err == nil && pullrequestlifecycle.SameComparisonVersion(pair, expected) {
			snapshot := *pr.Comparison
			snapshot.Inputs = pair
			pr.Comparison = &snapshot
		} else {
			invalidateComparison(pr)
		}
	}
	comparison := *pr
	comparison.Operations = nil
	if !reflect.DeepEqual(previous, comparison) {
		pr.ViewRevision++
	}
	if previous.ComparisonState != pr.ComparisonState || previous.RelationshipGeneration != pr.RelationshipGeneration || previous.HeadCommit != pr.HeadCommit || previous.BaseCommit != pr.BaseCommit || previous.BaseBranch != pr.BaseBranch || previous.HeadBranch != pr.HeadBranch || previous.DiffBaseCommit != pr.DiffBaseCommit {
		pr.TopologyGeneration++
	}
	if previous.Title != pr.Title || previous.Summary != pr.Summary {
		pr.MetadataGeneration++
	}

	pr.BaseBranch = pullrequestlifecycle.CanonicalBaseBranch(pr.BaseBranch)
	if strings.TrimSpace(pr.RepositoryID) == "" {
		return errors.New("repository ID is required")
	}
	documentView := *pr
	documentView.Operations = nil
	raw, err := json.Marshal(documentView)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `insert into pull_request_catalog(id, repository_id, provider, external_id, document) values(?,?,?,?,?) on conflict(id) do update set provider=excluded.provider, external_id=excluded.external_id, document=excluded.document`, pr.ID, pr.RepositoryID, null(pr.SyncProvider), null(pr.SyncExternalID), string(raw)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `insert into pull_requests(id,repository_id,title,description,status,base_branch,base_commit,head_branch,head_commit,diff_base_commit,sync_provider,sync_external_id,sync_data,created_at,updated_at,closed_at,synced_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(id) do update set title=excluded.title,description=excluded.description,status=excluded.status,base_branch=excluded.base_branch,base_commit=excluded.base_commit,head_branch=excluded.head_branch,head_commit=excluded.head_commit,diff_base_commit=excluded.diff_base_commit,sync_provider=excluded.sync_provider,sync_external_id=excluded.sync_external_id,sync_data=excluded.sync_data,updated_at=excluded.updated_at,closed_at=excluded.closed_at,synced_at=excluded.synced_at`, pr.ID, pr.RepositoryID, pr.Title, pr.Summary, pr.Status, pr.BaseBranch, pr.BaseCommit, pr.HeadBranch, pr.HeadCommit, null(pr.DiffBaseCommit), null(pr.SyncProvider), null(pr.SyncExternalID), string(pr.SyncData), pr.CreatedAt.Format(time.RFC3339Nano), pr.UpdatedAt.Format(time.RFC3339Nano), timeValue(pr.ClosedAt), timeValue(pr.SyncedAt))
	if err != nil {
		return err
	}
	baseRef, _ := json.Marshal(pr.BaseRef)
	headRef, _ := json.Marshal(pr.HeadRef)
	snapshot, _ := json.Marshal(pr.Comparison)
	_, err = tx.ExecContext(ctx, `update pull_requests set base_ref=?,head_ref=?,comparison_state=?,comparison_snapshot=? where id=?`, string(baseRef), string(headRef), pr.ComparisonState, string(snapshot), pr.ID)
	return err
}

// AdvancePublishedHeadInTransaction verifies the durable catalog head and
// writes both pull-request representations through a caller-owned transaction.
func (s *Store) AdvancePublishedHeadInTransaction(ctx context.Context, tx *sql.Tx, id, expected, published string) (bool, error) {
	return s.CompleteHeadInTransaction(ctx, tx, id, expected, published, nil, nil)
}

// CompleteHeadInTransaction atomically updates both pull-request
// representations while retaining the previous comparison until Git preparation.
func (s *Store) CompleteHeadInTransaction(ctx context.Context, tx *sql.Tx, id, expected, published string, targetBase, targetDiffBase *string) (bool, error) {
	// The caller's transaction owns the database connection. Taking mu here
	// could deadlock against a catalog reader holding mu while waiting for that
	// connection. Transaction isolation protects these database-only updates.
	var repositoryID, raw string
	if err := tx.QueryRowContext(ctx, `select repository_id, document from pull_request_catalog where id = ?`, id).Scan(&repositoryID, &raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, pullrequestlifecycle.ErrNotFound
		}
		return false, err
	}
	pr, err := decodePullRequest(repositoryID, raw)
	if err != nil {
		return false, err
	}
	if !pr.HeadRef.Valid() || !pr.BaseRef.Valid() {
		return false, pullrequestlifecycle.ErrComparisonUnavailable
	}
	return completeSharedHead(ctx, tx, &pr, expected, published)
}

func timeValue(v *time.Time) any {
	if v == nil {
		return nil
	}
	return v.Format(time.RFC3339Nano)
}
func null(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (s *Store) UpdatePullRequestMetadata(id, title, description string, at time.Time) error {
	_, err := s.mutate(id, func(pr *pullrequestlifecycle.PullRequest) error {
		pr.Title = title
		pr.Summary = description
		pr.UpdatedAt = at
		return nil
	})
	return err
}

func (s *Store) upsertObservedPullRequest(repositoryID string, pr pullrequestlifecycle.PullRequest, token pullrequestlifecycle.ObservationToken) (bool, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var id string
	if pr.SyncExternalID == "" && pr.ID != "" {
		err = tx.QueryRowContext(ctx, `select id from pull_request_catalog where repository_id=? and id=?`, repositoryID, pr.ID).Scan(&id)
	} else {
		err = tx.QueryRowContext(ctx, `select id from pull_request_catalog where repository_id=? and provider=? and external_id=?`, repositoryID, pr.SyncProvider, pr.SyncExternalID).Scan(&id)
	}
	created := errors.Is(err, sql.ErrNoRows)
	if err != nil && !created {
		return false, err
	}
	if created {
		id = newID()
		// Provider SHA fields do not constitute a prepared comparison.
		pr.BaseCommit, pr.HeadCommit, pr.DiffBaseCommit = "", "", ""
		pr.Comparison = nil
		pr.ComparisonState = pullrequestlifecycle.ComparisonUnavailable
	}

	var old pullrequestlifecycle.PullRequest
	if !created {
		var storedRepositoryID, raw string
		if err = tx.QueryRowContext(ctx, `select repository_id, document from pull_request_catalog where id = ?`, id).Scan(&storedRepositoryID, &raw); err != nil {
			return false, err
		}
		if old, err = getInTransaction(ctx, tx, id); err != nil {
			return false, err
		}
	}

	if !created {
		pr = acceptObservation(&old, pr, &token)
	}
	pr.ID = id
	pr.RepositoryID = repositoryID
	if !created {
		pr.Operations = old.Operations
		pr.ViewRevision = old.ViewRevision

		pr.CreatedAt = old.CreatedAt
		pr.LinkedHolonIDs = old.LinkedHolonIDs
	}
	if pr.CreatedAt.IsZero() {
		pr.CreatedAt = time.Now().UTC()
	}
	if pr.UpdatedAt.IsZero() {
		pr.UpdatedAt = pr.CreatedAt
	}
	if pr.LinkedHolonIDs == nil {
		pr.LinkedHolonIDs = []string{}
	}
	if err = saveInTransaction(ctx, tx, &pr); err != nil {
		return false, err
	}
	if !created && confirmsOpeningPreparation(old, pr) && pr.Status != old.Status {
		for _, operation := range old.Operations {
			if operation.Kind != "transition" || operation.RequestedStatus != pr.Status || operation.MutationStarted {
				continue
			}
			if _, err = s.CompleteOperationInTransaction(ctx, tx, operation.RequestID, "succeeded", ""); err != nil {
				return false, err
			}
		}
	}
	return created, tx.Commit()
}

func newID() string { var b [12]byte; _, _ = rand.Read(b[:]); return "pr_" + hex.EncodeToString(b[:]) }

func (s *Store) DeletePullRequest(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`delete from pull_requests where id = ?`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`delete from pull_request_catalog where id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreatePullRequest(current pullrequestlifecycle.PullRequest) (pullrequestlifecycle.PullRequest, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return current, err
	}
	defer tx.Rollback()
	current, err = createInTransaction(ctx, tx, current)
	if err != nil {
		return current, err
	}
	return current, tx.Commit()
}

// A newly visible PR already carries its creation intent. No concurrent action
// can enter between saving the PR identity and attaching the operation.
func (s *Store) CreateOperationPullRequest(ctx context.Context, requestID string, current pullrequestlifecycle.PullRequest) (pullrequestlifecycle.PullRequest, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return current, err
	}
	defer tx.Rollback()
	operation, found, err := operationInTransaction(ctx, tx, requestID)
	if err != nil {
		return current, err
	}
	if !found || operation.Kind != "create" || !operation.Active() {
		return current, pullrequestlifecycle.ErrInvalidTransition
	}
	if operation.PullRequestID != "" {
		return getInTransaction(ctx, tx, operation.PullRequestID)
	}
	if err := completeCreatedBranches(ctx, tx, &current, operation); err != nil {
		return current, err
	}
	current, err = createInTransaction(ctx, tx, current)
	if err != nil {
		return current, err
	}
	operation.PullRequestID = current.ID
	if operation.Steps == nil {
		operation.Steps = map[string]pullrequestlifecycle.OperationStep{}
	}
	operation.Steps["local_identity"] = pullrequestlifecycle.OperationStep{Status: "succeeded", HeadCommit: current.HeadCommit}
	operation.UpdatedAt = time.Now().UTC()
	if err = saveOperation(ctx, tx, operation); err != nil {
		return current, err
	}
	advanceGroups(&current, operation.Groups)
	if err = saveInTransaction(ctx, tx, &current); err != nil {
		return current, err
	}
	current, err = getInTransaction(ctx, tx, current.ID)
	if err != nil {
		return current, err
	}
	return current, tx.Commit()
}

func createInTransaction(ctx context.Context, tx *sql.Tx, pr pullrequestlifecycle.PullRequest) (pullrequestlifecycle.PullRequest, error) {
	// Creation receives a new identity, even when a caller uses an existing
	// snapshot as its field template. Durable ownership never follows a copy.
	pr.Operations = nil
	pr.ViewRevision = 0
	pr.LifecycleGeneration = 0
	pr.TopologyGeneration = 0
	pr.MetadataGeneration = 0
	pr.LifecycleObservation = 0
	pr.TopologyObservation = 0
	pr.MetadataObservation = 0
	pr.LifecycleConfirmation = nil
	pr.TopologyConfirmation = nil
	pr.MetadataConfirmation = nil
	if pr.ID == "" {
		pr.ID = newID()
	}
	pr.BaseBranch = pullrequestlifecycle.CanonicalBaseBranch(pr.BaseBranch)
	if pr.CreatedAt.IsZero() {
		pr.CreatedAt = time.Now().UTC()
	}
	pr.UpdatedAt = pr.CreatedAt
	if pr.LinkedHolonIDs == nil {
		pr.LinkedHolonIDs = []string{}
	}
	if err := saveInTransaction(ctx, tx, &pr); err != nil {
		return pullrequestlifecycle.PullRequest{}, err
	}
	return pr, nil
}

func (s *Store) mutate(id string, fn func(*pullrequestlifecycle.PullRequest) error) (pullrequestlifecycle.PullRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return pullrequestlifecycle.PullRequest{}, err
	}
	defer tx.Rollback()
	pr, err := getInTransaction(ctx, tx, id)
	if err != nil {
		return pr, err
	}
	if err = fn(&pr); err != nil {
		return pullrequestlifecycle.PullRequest{}, err
	}
	if err = saveInTransaction(ctx, tx, &pr); err != nil {
		return pullrequestlifecycle.PullRequest{}, err
	}
	return pr, tx.Commit()
}
func getInTransaction(ctx context.Context, tx *sql.Tx, id string) (pullrequestlifecycle.PullRequest, error) {
	var repositoryID, raw string
	if err := tx.QueryRowContext(ctx, `select repository_id,document from pull_request_catalog where id=?`, id).Scan(&repositoryID, &raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return pullrequestlifecycle.PullRequest{}, pullrequestlifecycle.ErrNotFound
		}
		return pullrequestlifecycle.PullRequest{}, err
	}
	current, err := decodePullRequest(repositoryID, raw)
	if err != nil {
		return current, err
	}
	active, err := activeOperations(ctx, tx, "pull_request_id=?", id)
	current.Operations = active[id]
	return current, err
}

func (s *Store) MarkPullRequestActivityRetired(id string, generation int64) error {
	_, err := s.mutate(id, func(pr *pullrequestlifecycle.PullRequest) error {
		if !pr.Status.Active() && pr.LifecycleGeneration == generation {
			pr.ActivityRetired = true
		}
		return nil
	})
	return err
}

func (s *Store) TransitionPullRequestStatus(id string, status pullrequestlifecycle.Status, at time.Time) (pullrequestlifecycle.PullRequest, error) {
	return s.mutate(id, func(pr *pullrequestlifecycle.PullRequest) error {
		if !pr.Status.CanTransitionTo(status) {
			return pullrequestlifecycle.ErrInvalidTransition
		}
		pr.Status = status
		pr.UpdatedAt = at
		if status == pullrequestlifecycle.StatusClosed {
			pr.ClosedAt = &at
		} else {
			pr.ClosedAt = nil
		}
		return nil
	})
}
func (s *Store) TransitionSyncedPullRequestStatus(id string, status pullrequestlifecycle.Status, data json.RawMessage, at time.Time, closed *time.Time, synced time.Time) (pullrequestlifecycle.PullRequest, error) {
	return s.mutate(id, func(pr *pullrequestlifecycle.PullRequest) error {
		pr.Status = status
		pr.SyncData = mergeSyncData(pr.SyncData, data, true)
		pr.UpdatedAt = at
		pr.ClosedAt = closed
		pr.SyncedAt = &synced
		return nil
	})
}
