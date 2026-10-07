// Package sqliteadapter owns pull request comment schema and
// persistence in the process-wide SQLite database.
package sqliteadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
)

type Store struct {
	db *sql.DB
	tx *sql.Tx
}

func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if err := database.CreateSchema(ctx, db, `
create table if not exists pull_request_comments (
  id text primary key,
  pull_request_id text not null references pull_requests(id),
  parent_comment_id text references pull_request_comments(id),
  body text not null,
  scope text not null,
  path text,
  old_path text,
  side text,
  line integer,
  diff_hunk text,
  original_head_commit text not null,
  status text not null,
  author_type text not null,
  author_github_user_id text,
  source_session_id text,
  source_review_id text,
  source_review_index integer,
  source_worker_id text,
  sync_provider text,
  sync_external_id text,
  publication_state text not null default 'local',
  provider_kind text,
  provider_review_id text,
  provider_thread_id text,
  created_at text not null,
  updated_at text not null,
  resolved_at text
);
create index if not exists pull_request_comments_pull_request_id
  on pull_request_comments(pull_request_id, created_at, id);
create index if not exists pull_request_comments_unresolved
  on pull_request_comments(pull_request_id, status);
create unique index if not exists pull_request_comments_provider_identity
  on pull_request_comments(sync_provider, sync_external_id)
  where sync_provider is not null and sync_provider <> '' and sync_external_id is not null and sync_external_id <> '';
create unique index if not exists pull_request_comments_source_worker
  on pull_request_comments(source_worker_id)
  where source_worker_id is not null and source_worker_id <> '';
create unique index if not exists pull_request_comments_source_review_finding
  on pull_request_comments(source_review_id, source_review_index)
  where source_review_id is not null and source_review_id <> '';

create table if not exists pull_request_review_import_receipts (
  source_review_id text not null,
  source_review_index integer not null,
  document text not null,
  primary key (source_review_id, source_review_index)
);

create table if not exists pull_request_comment_publications (
  id integer primary key autoincrement,
  comment_id text,
  pull_request_id text not null,
  operation text not null,
  payload text not null,
  attempts integer not null default 0,
  next_attempt_at text not null,
  permanent_error text,
  created_at text not null
);
create index if not exists pull_request_comment_publications_due
  on pull_request_comment_publications(pull_request_id, permanent_error, next_attempt_at, id);`); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

type connection interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (store *Store) SaveDraftBatch(ctx context.Context, comments []pullrequestcomments.Comment, jobs []*pullrequestcomments.PublicationJob) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	scoped := &Store{db: store.db, tx: tx}
	for index, comment := range comments {
		if err := scoped.Update(ctx, comment); err != nil {
			return err
		}
		if jobs[index] != nil {
			if err := scoped.EnqueuePublication(ctx, *jobs[index]); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (store *Store) connection() connection {
	if store.tx != nil {
		return store.tx
	}
	return store.db
}

// SaveWithPublication commits one local change and its publication intent together.
func (store *Store) SaveWithPublication(ctx context.Context, comment pullrequestcomments.Comment, insert bool, job *pullrequestcomments.PublicationJob) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	scoped := &Store{db: store.db, tx: tx}
	if insert {
		err = scoped.Insert(ctx, comment)
	} else {
		err = scoped.Update(ctx, comment)
	}
	if err != nil {
		return err
	}
	if job != nil {
		if err = scoped.EnqueuePublication(ctx, *job); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (store *Store) DuePublicationPullRequests(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := store.connection().QueryContext(ctx, `select pull_request_id from pull_request_comment_publications where permanent_error is null and next_attempt_at <= ? group by pull_request_id order by min(id)`, formatTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (store *Store) UpdatePublicationPayload(ctx context.Context, id int64, payload string) error {
	_, err := store.connection().ExecContext(ctx, `update pull_request_comment_publications set payload = ? where id = ?`, payload, id)
	return err
}

func (store *Store) Insert(ctx context.Context, comment pullrequestcomments.Comment) error {
	// A receipt must commit with its comment and any publication job, and must
	// survive deletion of the visible comment.
	if comment.SourceReviewID != "" && store.tx == nil {
		return store.SaveWithPublication(ctx, comment, true, nil)
	}
	_, err := store.connection().ExecContext(ctx, `
insert into pull_request_comments(
  id, pull_request_id, parent_comment_id, body, scope, path, old_path, side, line, diff_hunk,
  original_head_commit, status, author_type, author_github_user_id, source_session_id, source_review_id, source_review_index,
  source_worker_id, sync_provider, sync_external_id, publication_state, provider_kind, provider_review_id,
  provider_thread_id, created_at, updated_at, resolved_at
) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, commentValues(comment)...)
	if err != nil || comment.SourceReviewID == "" {
		return err
	}
	document, err := json.Marshal(comment)
	if err != nil {
		return err
	}
	_, err = store.connection().ExecContext(ctx, `insert into pull_request_review_import_receipts(source_review_id, source_review_index, document) values(?, ?, ?)`, comment.SourceReviewID, comment.SourceReviewIndex, string(document))
	return err
}

func (store *Store) Get(ctx context.Context, id string) (pullrequestcomments.Comment, error) {
	comment, err := scanComment(store.connection().QueryRowContext(ctx, commentSelect+` where id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return pullrequestcomments.Comment{}, pullrequestcomments.ErrCommentNotFound
	}
	return comment, err
}

func (store *Store) Update(ctx context.Context, comment pullrequestcomments.Comment) error {
	values := commentValues(comment)
	values = append(values, comment.ID)
	result, err := store.connection().ExecContext(ctx, `
update pull_request_comments set
  id = ?, pull_request_id = ?, parent_comment_id = ?, body = ?, scope = ?, path = ?, old_path = ?, side = ?, line = ?, diff_hunk = ?,
  original_head_commit = ?, status = ?, author_type = ?, author_github_user_id = ?, source_session_id = ?, source_review_id = ?, source_review_index = ?,
  source_worker_id = ?, sync_provider = ?, sync_external_id = ?, publication_state = ?, provider_kind = ?, provider_review_id = ?,
  provider_thread_id = ?, created_at = ?, updated_at = ?, resolved_at = ?
where id = ?`, values...)
	if err != nil {
		return err
	}
	return requireChanged(result)
}

func (store *Store) Delete(ctx context.Context, id string) error {
	result, err := store.connection().ExecContext(ctx, `delete from pull_request_comments where id = ?`, id)
	if err != nil {
		return err
	}
	return requireChanged(result)
}

func (store *Store) ListByPullRequest(ctx context.Context, pullRequestID string) ([]pullrequestcomments.Comment, error) {
	rows, err := store.connection().QueryContext(ctx, commentSelect+` where pull_request_id = ? order by created_at, id`, pullRequestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]pullrequestcomments.Comment, 0)
	for rows.Next() {
		comment, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, comment)
	}
	return result, rows.Err()
}

func (store *Store) UnresolvedCount(ctx context.Context, pullRequestID string) (int, error) {
	var count int
	err := store.connection().QueryRowContext(ctx, `select count(*) from pull_request_comments where pull_request_id = ? and status = ? and publication_state <> 'draft'`, pullRequestID, string(pullrequestcomments.Unresolved)).Scan(&count)
	return count, err
}

func (store *Store) UnresolvedCounts(ctx context.Context, pullRequestIDs []string) (map[string]int, error) {
	counts := make(map[string]int, len(pullRequestIDs))
	if len(pullRequestIDs) == 0 {
		return counts, nil
	}
	arguments := make([]any, 0, len(pullRequestIDs)+1)
	arguments = append(arguments, string(pullrequestcomments.Unresolved))
	placeholders := make([]string, 0, len(pullRequestIDs))
	for _, pullRequestID := range pullRequestIDs {
		counts[pullRequestID] = 0
		arguments = append(arguments, pullRequestID)
		placeholders = append(placeholders, "?")
	}
	rows, err := store.connection().QueryContext(ctx, `
select pull_request_id, count(*)
from pull_request_comments
where status = ? and publication_state <> 'draft' and pull_request_id in (`+strings.Join(placeholders, ",")+`)
group by pull_request_id`, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pullRequestID string
		var count int
		if err := rows.Scan(&pullRequestID, &count); err != nil {
			return nil, err
		}
		counts[pullRequestID] = count
	}
	return counts, rows.Err()
}

func (store *Store) HasChildReplies(ctx context.Context, id string) (bool, error) {
	var count int
	err := store.connection().QueryRowContext(ctx, `select count(*) from pull_request_comments where parent_comment_id = ?`, id).Scan(&count)
	return count > 0, err
}

func (store *Store) FindByExternalIdentity(ctx context.Context, provider, externalID string) (pullrequestcomments.Comment, error) {
	comment, err := scanComment(store.connection().QueryRowContext(ctx, commentSelect+` where sync_provider = ? and sync_external_id = ?`, provider, externalID))
	if errors.Is(err, sql.ErrNoRows) {
		return pullrequestcomments.Comment{}, pullrequestcomments.ErrCommentNotFound
	}
	return comment, err
}

func (store *Store) FindBySourceWorkerID(ctx context.Context, workerID string) (pullrequestcomments.Comment, error) {
	comment, err := scanComment(store.connection().QueryRowContext(ctx, commentSelect+` where source_worker_id = ?`, workerID))
	if errors.Is(err, sql.ErrNoRows) {
		return pullrequestcomments.Comment{}, pullrequestcomments.ErrCommentNotFound
	}
	return comment, err
}

func (store *Store) FindReviewImportReceipt(ctx context.Context, reviewID string, index int) (pullrequestcomments.Comment, error) {
	var document string
	err := store.connection().QueryRowContext(ctx, `select document from pull_request_review_import_receipts where source_review_id = ? and source_review_index = ?`, reviewID, index).Scan(&document)
	if errors.Is(err, sql.ErrNoRows) {
		return pullrequestcomments.Comment{}, pullrequestcomments.ErrCommentNotFound
	}
	if err != nil {
		return pullrequestcomments.Comment{}, err
	}
	var comment pullrequestcomments.Comment
	err = json.Unmarshal([]byte(document), &comment)
	comment.SourceReviewIndex = index
	return comment, err
}

func (store *Store) UpsertExternal(ctx context.Context, incoming pullrequestcomments.Comment) (pullrequestcomments.Comment, error) {
	existing, err := store.FindByExternalIdentity(ctx, incoming.ProviderIdentity.Provider, incoming.ProviderIdentity.ExternalID)
	if err == nil {
		incoming.ID = existing.ID
		incoming.CreatedAt = existing.CreatedAt
		if err := store.Update(ctx, incoming); err != nil {
			return pullrequestcomments.Comment{}, err
		}
		return incoming, nil
	}
	if !errors.Is(err, pullrequestcomments.ErrCommentNotFound) {
		return pullrequestcomments.Comment{}, err
	}
	if err := store.Insert(ctx, incoming); err != nil {
		return pullrequestcomments.Comment{}, err
	}
	return incoming, nil
}

func (store *Store) EnqueuePublication(ctx context.Context, job pullrequestcomments.PublicationJob) error {
	if store.tx == nil {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := (&Store{db: store.db, tx: tx}).EnqueuePublication(ctx, job); err != nil {
			return err
		}
		return tx.Commit()
	}
	tx := store.tx
	// An edit before publication retries the existing creation with current saved text.
	// Retain its previously selected quote and uncertain-outcome recovery context.
	if job.Operation == "create" {
		result, err := tx.ExecContext(ctx, `update pull_request_comment_publications set next_attempt_at = ? where comment_id = ? and operation = 'create' and permanent_error is null`, formatTime(job.NextAttemptAt), job.CommentID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
	}
	if job.CommentID != "" {
		if _, err := tx.ExecContext(ctx, `update pull_request_comments set publication_state = ? where id = ?`, string(pullrequestcomments.PublicationPending), job.CommentID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
insert into pull_request_comment_publications(comment_id, pull_request_id, operation, payload, attempts, next_attempt_at, permanent_error, created_at)
values(?, ?, ?, ?, ?, ?, ?, ?)`, nullString(job.CommentID), job.PullRequestID, job.Operation, job.Payload, job.Attempts, formatTime(job.NextAttemptAt), nullString(job.PermanentError), formatTime(job.CreatedAt)); err != nil {
		return err
	}
	return nil
}

func (store *Store) DuePublications(ctx context.Context, pullRequestID string, now time.Time) ([]pullrequestcomments.PublicationJob, error) {
	return store.listPublications(ctx, pullRequestID, ` and permanent_error is null and next_attempt_at <= ?`, formatTime(now))
}

func (store *Store) PendingPublications(ctx context.Context, pullRequestID string) ([]pullrequestcomments.PublicationJob, error) {
	return store.listPublications(ctx, pullRequestID, "")
}

func (store *Store) listPublications(ctx context.Context, pullRequestID, suffix string, arguments ...any) ([]pullrequestcomments.PublicationJob, error) {
	queryArguments := []any{pullRequestID}
	queryArguments = append(queryArguments, arguments...)
	rows, err := store.connection().QueryContext(ctx, `
select id, coalesce(comment_id, ''), pull_request_id, operation, payload, attempts, next_attempt_at, coalesce(permanent_error, ''), created_at
from pull_request_comment_publications
where pull_request_id = ?`+suffix+`
order by id`, queryArguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]pullrequestcomments.PublicationJob, 0)
	for rows.Next() {
		var job pullrequestcomments.PublicationJob
		var nextAttemptAt, createdAt string
		if err := rows.Scan(&job.ID, &job.CommentID, &job.PullRequestID, &job.Operation, &job.Payload, &job.Attempts, &nextAttemptAt, &job.PermanentError, &createdAt); err != nil {
			return nil, err
		}
		if job.NextAttemptAt, err = parseTime(nextAttemptAt); err != nil {
			return nil, err
		}
		if job.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

func (store *Store) CompletePublication(ctx context.Context, id int64) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var commentID sql.NullString
	var operation string
	if err := tx.QueryRowContext(ctx, `select comment_id, operation from pull_request_comment_publications where id = ?`, id).Scan(&commentID, &operation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from pull_request_comment_publications where id = ?`, id); err != nil {
		return err
	}
	if operation == "update" {
		// A successful edit fulfills older failed edits, but not other operations.
		if _, err := tx.ExecContext(ctx, `delete from pull_request_comment_publications where comment_id = ? and operation = 'update' and permanent_error is not null and id < ?`, commentID, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `update pull_request_comments set publication_state = case
 when exists (select 1 from pull_request_comment_publications where comment_id = ? and permanent_error is null) then 'pending'
 when exists (select 1 from pull_request_comment_publications where comment_id = ?) then 'failed'
 when coalesce(sync_external_id, '') <> '' then 'published'
 else 'local' end where id = ?`, commentID, commentID, commentID); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) FailPublication(ctx context.Context, id int64, attempts int, nextAttemptAt time.Time, permanentError string) error {
	_, err := store.connection().ExecContext(ctx, `
update pull_request_comment_publications
set attempts = ?, next_attempt_at = ?, permanent_error = ?
where id = ?`, attempts, formatTime(nextAttemptAt), nullString(permanentError), id)
	return err
}

func (store *Store) CancelCommentPublications(ctx context.Context, commentID string) error {
	_, err := store.connection().ExecContext(ctx, `delete from pull_request_comment_publications where comment_id = ?`, commentID)
	return err
}

func (store *Store) SetProviderIdentity(ctx context.Context, id string, identity pullrequestcomments.ProviderIdentity, state pullrequestcomments.PublicationState) error {
	result, err := store.connection().ExecContext(ctx, `
update pull_request_comments set sync_provider = ?, sync_external_id = ?, provider_kind = ?, provider_review_id = ?, provider_thread_id = ?, publication_state = case
 when ? = 'published' and exists (select 1 from pull_request_comment_publications where comment_id = ?) then 'pending'
 else ? end
where id = ?`, nullString(identity.Provider), nullString(identity.ExternalID), nullString(identity.Kind), nullString(identity.ReviewID), nullString(identity.ThreadID), string(state), id, string(state), id)
	if err != nil {
		return err
	}
	return requireChanged(result)
}

const commentSelect = `select
  id, pull_request_id, coalesce(parent_comment_id, ''), body, scope, coalesce(path, ''), coalesce(old_path, ''), coalesce(side, ''), line, coalesce(diff_hunk, ''),
  original_head_commit, status, author_type, coalesce(author_github_user_id, ''), coalesce(source_session_id, ''), coalesce(source_review_id, ''),
  coalesce(source_review_index, 0), coalesce(source_worker_id, ''), coalesce(sync_provider, ''), coalesce(sync_external_id, ''), publication_state,
  coalesce(provider_kind, ''), coalesce(provider_review_id, ''), coalesce(provider_thread_id, ''), created_at, updated_at, resolved_at
from pull_request_comments`

type scanner interface{ Scan(...any) error }

func scanComment(row scanner) (pullrequestcomments.Comment, error) {
	var comment pullrequestcomments.Comment
	var line sql.NullInt64
	var createdAt, updatedAt string
	var resolvedAt sql.NullString
	err := row.Scan(
		&comment.ID, &comment.PullRequestID, &comment.ParentCommentID, &comment.Body, &comment.Scope,
		&comment.Path, &comment.OldPath, &comment.Side, &line, &comment.DiffHunk, &comment.OriginalHeadCommit,
		&comment.Status, &comment.AuthorType, &comment.AuthorGitHubUserID, &comment.SourceSessionID,
		&comment.SourceReviewID, &comment.SourceReviewIndex, &comment.SourceWorkerID, &comment.ProviderIdentity.Provider,
		&comment.ProviderIdentity.ExternalID, &comment.PublicationState, &comment.ProviderIdentity.Kind,
		&comment.ProviderIdentity.ReviewID, &comment.ProviderIdentity.ThreadID, &createdAt, &updatedAt, &resolvedAt,
	)
	if err != nil {
		return pullrequestcomments.Comment{}, err
	}
	if line.Valid {
		value := int(line.Int64)
		comment.Line = &value
	}
	if comment.CreatedAt, err = parseTime(createdAt); err != nil {
		return pullrequestcomments.Comment{}, err
	}
	if comment.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return pullrequestcomments.Comment{}, err
	}
	if resolvedAt.Valid {
		value, parseErr := parseTime(resolvedAt.String)
		if parseErr != nil {
			return pullrequestcomments.Comment{}, parseErr
		}
		comment.ResolvedAt = &value
	}
	return comment, nil
}

func commentValues(comment pullrequestcomments.Comment) []any {
	return []any{
		comment.ID, comment.PullRequestID, nullString(comment.ParentCommentID), comment.Body, string(comment.Scope),
		nullString(comment.Path), nullString(comment.OldPath), nullString(comment.Side), nullableInt(comment.Line), nullString(comment.DiffHunk),
		comment.OriginalHeadCommit, string(comment.Status), string(comment.AuthorType), nullString(comment.AuthorGitHubUserID),
		nullString(comment.SourceSessionID), nullString(comment.SourceReviewID), comment.SourceReviewIndex, nullString(comment.SourceWorkerID),
		nullString(comment.ProviderIdentity.Provider), nullString(comment.ProviderIdentity.ExternalID), string(comment.PublicationState),
		nullString(comment.ProviderIdentity.Kind), nullString(comment.ProviderIdentity.ReviewID), nullString(comment.ProviderIdentity.ThreadID),
		formatTime(comment.CreatedAt), formatTime(comment.UpdatedAt), nullableTime(comment.ResolvedAt),
	}
}

func requireChanged(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return pullrequestcomments.ErrCommentNotFound
	}
	return nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse comment time %q: %w", value, err)
	}
	return parsed.UTC(), nil
}
