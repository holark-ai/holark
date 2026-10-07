// Package storeadapter implements issue persistence against the shared SQLite
// database without adding runtime issue responsibilities to the universal store.
package storeadapter

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/issues"
)

type Store struct {
	db *sql.DB
}

func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if err := database.CreateSchema(ctx, db, `
create table if not exists issues (
  id text primary key,
  repository_id text not null references repositories(id),
  title text not null,
  body text not null,
  status text not null,
  sync_provider text,
  sync_external_id text,
  sync_data text not null default '{}',
  issuer_holark_id text not null default '',
  created_at text not null,
  updated_at text not null,
  closed_at text,
  synced_at text
);

create unique index if not exists issues_sync_identity
  on issues(repository_id, sync_provider, sync_external_id)
  where sync_provider is not null and sync_external_id is not null;

create table if not exists issue_labels (
  id text primary key,
  repository_id text not null references repositories(id),
  sync_provider text not null,
  sync_external_id text not null,
  name text not null,
  color text not null,
  description text not null
);

create unique index if not exists issue_labels_sync_identity
  on issue_labels(repository_id, sync_provider, sync_external_id);

create table if not exists issue_label_assignments (
  issue_id text not null references issues(id) on delete cascade,
  label_id text not null references issue_labels(id),
  position integer not null check(position >= 0),
  primary key(issue_id, label_id),
  unique(issue_id, position)
);

create index if not exists issue_label_assignments_label_id
  on issue_label_assignments(label_id);

create table if not exists issue_pull_requests (
  issue_id text not null references issues(id) on delete cascade,
  pull_request_id text not null references pull_requests(id),
  primary key(issue_id, pull_request_id)
);
create index if not exists issue_pull_requests_pull_request_id on issue_pull_requests(pull_request_id);

create table if not exists issue_assignees (
  issue_id text not null references issues(id) on delete cascade,
  holark_id text not null,
  position integer not null check(position >= 0),
  primary key(issue_id, holark_id),
  unique(issue_id, position)
);

create table if not exists issue_comments (
  id text primary key,
  issue_id text not null references issues(id) on delete cascade,
  body text not null,
  author_login text not null,
  author_avatar_url text not null,
  author_url text not null,
  github_id text not null,
  github_node_id text not null,
  url text not null,
  created_at text not null,
  updated_at text not null,
  can_edit integer not null,
  can_delete integer not null,
  unique(issue_id, github_id)
);

create index if not exists issue_comments_chronological
  on issue_comments(issue_id, created_at, github_id);

create table if not exists issue_comment_sync (
  issue_id text primary key references issues(id) on delete cascade,
  synced_at text not null,
  can_comment integer
);`); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (store *Store) Get(ctx context.Context, id string) (issues.Issue, error) {
	issue, err := scanIssue(store.db.QueryRowContext(ctx, issueSelect+`
where i.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return issues.Issue{}, issues.ErrIssueNotFound
	}
	if err != nil {
		return issues.Issue{}, err
	}
	result := []issues.Issue{issue}
	if err := store.hydrateLabels(ctx, result); err != nil {
		return issues.Issue{}, err
	}
	if err := store.hydrateAssignees(ctx, result); err != nil {
		return issues.Issue{}, err
	}
	return result[0], nil
}

func (store *Store) List(ctx context.Context, projectID string) ([]issues.Issue, error) {
	rows, err := store.db.QueryContext(ctx, issueSelect+`
where i.repository_id = ?
order by i.updated_at desc, i.id`, projectID)
	if err != nil {
		return nil, err
	}
	result := make([]issues.Issue, 0)
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, issue)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := store.hydrateLabels(ctx, result); err != nil {
		return nil, err
	}
	if err := store.hydrateAssignees(ctx, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (store *Store) hydrateLabels(ctx context.Context, result []issues.Issue) error {
	if len(result) == 0 {
		return nil
	}
	indexes := make(map[string]int, len(result))
	arguments := make([]any, len(result))
	for index := range result {
		result[index].Labels = []issues.Label{}
		indexes[result[index].ID] = index
		arguments[index] = result[index].ID
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(result)), ",")
	rows, err := store.db.QueryContext(ctx, `
select a.issue_id, l.id, l.name, l.color, l.description, l.sync_provider, l.sync_external_id
from issue_label_assignments a
join issue_labels l on l.id = a.label_id
where a.issue_id in (`+placeholders+`)
order by a.issue_id, a.position`, arguments...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var issueID string
		var label issues.Label
		if err := rows.Scan(&issueID, &label.ID, &label.Name, &label.Color, &label.Description, &label.SyncProvider, &label.SyncExternalID); err != nil {
			return err
		}
		index, ok := indexes[issueID]
		if !ok {
			return fmt.Errorf("label assignment references unloaded issue %q", issueID)
		}
		result[index].Labels = append(result[index].Labels, label)
	}
	return rows.Err()
}

func (store *Store) hydrateAssignees(ctx context.Context, result []issues.Issue) error {
	if len(result) == 0 {
		return nil
	}
	indexes := make(map[string]int, len(result))
	arguments := make([]any, len(result))
	for index := range result {
		result[index].AssigneeHolarkIDs = []string{}
		indexes[result[index].ID] = index
		arguments[index] = result[index].ID
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(result)), ",")
	rows, err := store.db.QueryContext(ctx, `
select issue_id, holark_id
from issue_assignees
where issue_id in (`+placeholders+`)
order by issue_id, position`, arguments...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var issueID, holarkID string
		if err := rows.Scan(&issueID, &holarkID); err != nil {
			return err
		}
		index, ok := indexes[issueID]
		if !ok {
			return fmt.Errorf("assignee references unloaded issue %q", issueID)
		}
		result[index].AssigneeHolarkIDs = append(result[index].AssigneeHolarkIDs, holarkID)
	}
	return rows.Err()
}

func (store *Store) Insert(ctx context.Context, issue issues.Issue) error {
	syncData, err := normalizedSyncData(issue.SyncData)
	if err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertIssue(ctx, tx, issue, syncData); err != nil {
		return err
	}
	if err := replaceLabelSnapshot(ctx, tx, issue); err != nil {
		return err
	}
	if err := replaceAssigneeSnapshot(ctx, tx, issue); err != nil {
		return err
	}
	return tx.Commit()
}

func insertIssue(ctx context.Context, tx *sql.Tx, issue issues.Issue, syncData string) error {
	if _, err := tx.ExecContext(ctx, `
insert into issues(
	  id, repository_id, title, body, status, sync_provider, sync_external_id, sync_data, issuer_holark_id, created_at, updated_at, closed_at, synced_at
) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		issue.ID, issue.RepositoryID, issue.Title, issue.Body, issue.Status, nullString(issue.SyncProvider), nullString(issue.SyncExternalID), syncData,
		issue.IssuerHolarkID, encodeTime(issue.CreatedAt), encodeTime(issue.UpdatedAt), nullableTime(issue.ClosedAt), nullableTime(issue.SyncedAt)); err != nil {
		return err
	}
	for _, pullRequestID := range issue.LinkedPullRequestIDs {
		if _, err := tx.ExecContext(ctx, `insert into issue_pull_requests(issue_id, pull_request_id) values(?, ?)`, issue.ID, pullRequestID); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) Update(ctx context.Context, issue issues.Issue) error {
	syncData, err := normalizedSyncData(issue.SyncData)
	if err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
update issues set
  title = ?,
  body = ?,
  status = ?,
  sync_provider = ?,
  sync_external_id = ?,
  sync_data = ?,
  issuer_holark_id = ?,
	  updated_at = ?,
  closed_at = ?,
  synced_at = ?
where id = ?`,
		issue.Title, issue.Body, issue.Status, nullString(issue.SyncProvider), nullString(issue.SyncExternalID), syncData, issue.IssuerHolarkID,
		encodeTime(issue.UpdatedAt), nullableTime(issue.ClosedAt), nullableTime(issue.SyncedAt), issue.ID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return issues.ErrIssueNotFound
	}
	if err := replaceLabelSnapshot(ctx, tx, issue); err != nil {
		return err
	}
	if err := replaceAssigneeSnapshot(ctx, tx, issue); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) UpsertSynced(ctx context.Context, issue issues.Issue) (bool, error) {
	if issue.SyncProvider == "" || issue.SyncExternalID == "" {
		return false, errors.New("sync provider and external id are required")
	}
	syncData, err := normalizedSyncData(issue.SyncData)
	if err != nil {
		return false, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
update issues set
  title = ?,
  body = ?,
  status = ?,
  sync_data = ?,
  issuer_holark_id = ?,
	  updated_at = ?,
  closed_at = ?,
  synced_at = ?
where repository_id = ? and sync_provider = ? and sync_external_id = ?`,
		issue.Title, issue.Body, issue.Status, syncData, issue.IssuerHolarkID, encodeTime(issue.UpdatedAt), nullableTime(issue.ClosedAt), nullableTime(issue.SyncedAt),
		issue.RepositoryID, issue.SyncProvider, issue.SyncExternalID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	inserted := rows == 0
	if inserted {
		issue.SyncData = json.RawMessage(syncData)
		if err := insertIssue(ctx, tx, issue, syncData); err != nil {
			return false, err
		}
	} else {
		if err := tx.QueryRowContext(ctx, `
select id from issues
where repository_id = ? and sync_provider = ? and sync_external_id = ?`,
			issue.RepositoryID, issue.SyncProvider, issue.SyncExternalID).Scan(&issue.ID); err != nil {
			return false, err
		}
	}
	if err := replaceLabelSnapshot(ctx, tx, issue); err != nil {
		return false, err
	}
	if err := replaceAssigneeSnapshot(ctx, tx, issue); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return inserted, nil
}

func replaceLabelSnapshot(ctx context.Context, tx *sql.Tx, issue issues.Issue) error {
	labels, err := upsertLabelDefinitions(ctx, tx, issue.RepositoryID, issue.Labels)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from issue_label_assignments where issue_id = ?`, issue.ID); err != nil {
		return err
	}
	for position, label := range labels {
		if _, err := tx.ExecContext(ctx, `
insert into issue_label_assignments(issue_id, label_id, position) values(?, ?, ?)`,
			issue.ID, label.ID, position); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) UpsertLabelCatalog(ctx context.Context, projectID string, labels []issues.Label) ([]issues.Label, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stored, err := upsertLabelDefinitions(ctx, tx, projectID, labels)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return stored, nil
}

func upsertLabelDefinitions(ctx context.Context, tx *sql.Tx, projectID string, labels []issues.Label) ([]issues.Label, error) {
	result := make([]issues.Label, 0, len(labels))
	seen := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		if label.SyncProvider == "" || label.SyncExternalID == "" {
			return nil, errors.New("label sync provider and external id are required")
		}
		identity := label.SyncProvider + "\x00" + label.SyncExternalID
		if _, duplicate := seen[identity]; duplicate {
			continue
		}
		seen[identity] = struct{}{}

		err := tx.QueryRowContext(ctx, `
select id from issue_labels
where repository_id = ? and sync_provider = ? and sync_external_id = ?`,
			projectID, label.SyncProvider, label.SyncExternalID).Scan(&label.ID)
		switch {
		case err == nil:
			_, err = tx.ExecContext(ctx, `update issue_labels set name = ?, color = ?, description = ? where id = ?`,
				label.Name, label.Color, label.Description, label.ID)
		case errors.Is(err, sql.ErrNoRows):
			label.ID, err = newLabelID()
			if err == nil {
				_, err = tx.ExecContext(ctx, `
insert into issue_labels(id, repository_id, sync_provider, sync_external_id, name, color, description)
values(?, ?, ?, ?, ?, ?, ?)`, label.ID, projectID, label.SyncProvider, label.SyncExternalID,
					label.Name, label.Color, label.Description)
			}
		}
		if err != nil {
			return nil, err
		}
		result = append(result, label)
	}
	return result, nil
}

func replaceAssigneeSnapshot(ctx context.Context, tx *sql.Tx, issue issues.Issue) error {
	if _, err := tx.ExecContext(ctx, `delete from issue_assignees where issue_id = ?`, issue.ID); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(issue.AssigneeHolarkIDs))
	position := 0
	for _, raw := range issue.AssigneeHolarkIDs {
		holarkID := strings.TrimSpace(raw)
		if holarkID == "" {
			return errors.New("assignee Holark ID is required")
		}
		if _, duplicate := seen[holarkID]; duplicate {
			continue
		}
		seen[holarkID] = struct{}{}
		if _, err := tx.ExecContext(ctx,
			`insert into issue_assignees(issue_id, holark_id, position) values(?, ?, ?)`,
			issue.ID, holarkID, position); err != nil {
			return err
		}
		position++
	}
	return nil
}

func newLabelID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "label-" + hex.EncodeToString(value), nil
}

func (store *Store) LinkPullRequest(ctx context.Context, issueID, pullRequestID string) error {
	_, err := store.db.ExecContext(ctx, `insert into issue_pull_requests(issue_id, pull_request_id) values(?, ?)`, issueID, pullRequestID)
	return err
}

func (store *Store) DeleteSyncedAbsent(ctx context.Context, projectID, syncProvider string, presentExternalIDs []string) (int, error) {
	arguments := []any{projectID, syncProvider}
	query := `delete from issues where repository_id = ? and sync_provider = ?`
	if len(presentExternalIDs) > 0 {
		query += ` and sync_external_id not in (` + strings.TrimSuffix(strings.Repeat("?,", len(presentExternalIDs)), ",") + `)`
		for _, externalID := range presentExternalIDs {
			arguments = append(arguments, externalID)
		}
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, query, arguments...)
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(deleted), nil
}

const issueSelect = `
select i.id, i.repository_id, i.title, i.body, i.status, coalesce(i.sync_provider, ''), coalesce(i.sync_external_id, ''), i.sync_data,
       i.issuer_holark_id, i.created_at, i.updated_at, i.closed_at, i.synced_at,
	   coalesce((select group_concat(pull_request_id, char(31)) from issue_pull_requests where issue_id = i.id), '')
from issues i`

type issueScanner interface {
	Scan(dest ...any) error
}

func scanIssue(row issueScanner) (issues.Issue, error) {
	var issue issues.Issue
	var syncData, createdAt, updatedAt string
	var closedAt, syncedAt sql.NullString
	var linkedPullRequests string
	if err := row.Scan(
		&issue.ID, &issue.RepositoryID, &issue.Title, &issue.Body, &issue.Status, &issue.SyncProvider, &issue.SyncExternalID, &syncData,
		&issue.IssuerHolarkID, &createdAt, &updatedAt, &closedAt, &syncedAt, &linkedPullRequests,
	); err != nil {
		return issues.Issue{}, err
	}
	issue.SyncData = json.RawMessage(syncData)
	var err error
	if issue.CreatedAt, err = decodeTime(createdAt); err != nil {
		return issues.Issue{}, err
	}
	if issue.UpdatedAt, err = decodeTime(updatedAt); err != nil {
		return issues.Issue{}, err
	}
	if issue.ClosedAt, err = decodeNullableTime(closedAt); err != nil {
		return issues.Issue{}, err
	}
	if issue.SyncedAt, err = decodeNullableTime(syncedAt); err != nil {
		return issues.Issue{}, err
	}
	issue.LinkedPullRequestIDs = splitLinked(linkedPullRequests)
	if len(issue.SyncData) == 0 {
		issue.SyncData = json.RawMessage("{}")
	}
	return issue, nil
}

func normalizedSyncData(value json.RawMessage) (string, error) {
	if len(value) == 0 {
		return "{}", nil
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return "", fmt.Errorf("sync data must be a JSON object: %w", err)
	}
	if _, ok := decoded.(map[string]any); !ok {
		return "", errors.New("sync data must be a JSON object")
	}
	return string(value), nil
}

func encodeTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func decodeTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func nullableTime(value *time.Time) sql.NullString {
	if value == nil || value.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: encodeTime(*value), Valid: true}
}

func decodeNullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	decoded, err := decodeTime(value.String)
	if err != nil {
		return nil, err
	}
	return &decoded, nil
}

func nullString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func splitLinked(value string) []string {
	if value == "" {
		return []string{}
	}
	return strings.Split(value, string(rune(31)))
}

var _ issues.Store = (*Store)(nil)
