package sqliteadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
)

type Store struct {
	db         *sql.DB
	projection TransactionalProjection
}

// TransactionalProjection keeps the catalog and relational metadata in the same commit.
type TransactionalProjection interface {
	NotifyActionCompletion(string)
	UpdateMetadataInTransaction(context.Context, *sql.Tx, string, string, string, time.Time, time.Time) error
	CompleteOperationInTransaction(context.Context, *sql.Tx, string, string, string) (pullrequestlifecycle.Operation, error)
}

func New(ctx context.Context, db *sql.DB, projection TransactionalProjection) (*Store, error) {
	if err := database.CreateSchema(ctx, db, `
create table if not exists pull_request_metadata (
  pull_request_id text primary key references pull_requests(id) on delete cascade,
  metadata_session_id text,
  preparation_target text check(preparation_target in ('wip', 'draft', 'open')),
  prepared_head_commit text,
  state text not null default '{}'
);`); err != nil {
		return nil, err
	}
	return &Store{db: db, projection: projection}, nil
}

func (store *Store) Get(ctx context.Context, id string) (pullrequestmetadata.Snapshot, error) {
	if store == nil || store.db == nil {
		return pullrequestmetadata.Snapshot{}, errors.Join(pullrequestmetadata.ErrUpdateFailed, errors.New("metadata database is unavailable"))
	}
	var snapshot pullrequestmetadata.Snapshot
	var updatedAt, state string
	err := store.db.QueryRowContext(ctx,
		"select pr.id, pr.repository_id, r.repository_url, pr.title, pr.description, pr.status, "+
			"pr.comparison_state, pr.base_branch, pr.base_commit, pr.head_branch, pr.head_commit, coalesce(pr.diff_base_commit, ''), "+
			"coalesce(pr.sync_provider, ''), coalesce(pr.sync_external_id, ''), coalesce(pm.metadata_session_id, ''), "+
			"coalesce(pm.preparation_target, ''), coalesce(pm.prepared_head_commit, ''), pr.updated_at, coalesce(pm.state, '{}') "+
			"from pull_requests pr join repositories r on r.id = pr.repository_id "+
			"left join pull_request_metadata pm on pm.pull_request_id = pr.id where pr.id = ?", id,
	).Scan(&snapshot.ID, &snapshot.RepositoryID, &snapshot.RepositoryURL, &snapshot.Title, &snapshot.Description, &snapshot.Status,
		&snapshot.ComparisonState, &snapshot.BaseBranch, &snapshot.BaseCommit, &snapshot.HeadBranch, &snapshot.HeadCommit, &snapshot.DiffBaseCommit,
		&snapshot.SyncProvider, &snapshot.SyncExternalID, &snapshot.MetadataSessionID, &snapshot.PreparationTarget, &snapshot.PreparedHeadCommit, &updatedAt, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return pullrequestmetadata.Snapshot{}, pullrequestmetadata.ErrPullRequestNotFound
	}
	if err != nil {
		return pullrequestmetadata.Snapshot{}, errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
	}
	if err := json.Unmarshal([]byte(state), &snapshot.State); err != nil {
		return snapshot, errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
	}
	snapshot.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return pullrequestmetadata.Snapshot{}, errors.Join(pullrequestmetadata.ErrUpdateFailed, fmt.Errorf("decode pull request updated_at: %w", err))
	}
	return snapshot, nil
}

func (store *Store) GetByMetadataSession(ctx context.Context, sessionID string) (pullrequestmetadata.Snapshot, error) {
	if store == nil || store.db == nil || sessionID == "" {
		return pullrequestmetadata.Snapshot{}, pullrequestmetadata.ErrPullRequestNotFound
	}
	var id string
	err := store.db.QueryRowContext(ctx, "select pull_request_id from pull_request_metadata where metadata_session_id = ?", sessionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return pullrequestmetadata.Snapshot{}, pullrequestmetadata.ErrPullRequestNotFound
	}
	if err != nil {
		return pullrequestmetadata.Snapshot{}, errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
	}
	return store.Get(ctx, id)
}

func (store *Store) SetMetadataSession(ctx context.Context, id, sessionID string) error {
	if store == nil || store.db == nil || id == "" || sessionID == "" {
		return pullrequestmetadata.ErrUpdateFailed
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
	}
	defer tx.Rollback()
	var linksTable int
	if err := tx.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name='pull_request_sessions'`).Scan(&linksTable); err != nil {
		return errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
	}
	if linksTable != 0 {
		if _, err := tx.ExecContext(ctx, `insert or ignore into pull_request_sessions(pull_request_id,session_id) values(?,?)`, id, sessionID); err != nil {
			return errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
insert into pull_request_metadata(pull_request_id, metadata_session_id) values(?, ?)
on conflict(pull_request_id) do update set metadata_session_id = excluded.metadata_session_id`, id, sessionID); err != nil {
		return errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
	}
	if err := tx.Commit(); err != nil {
		return errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
	}
	return nil
}

func (store *Store) ClearMetadataSession(ctx context.Context, id, expectedSessionID string) (bool, error) {
	if store == nil || store.db == nil || id == "" || expectedSessionID == "" {
		return false, pullrequestmetadata.ErrUpdateFailed
	}
	result, err := store.db.ExecContext(ctx,
		"update pull_request_metadata set metadata_session_id = null where pull_request_id = ? and metadata_session_id = ?",
		id, expectedSessionID,
	)
	if err != nil {
		return false, errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
	}
	return rows > 0, nil
}

func (store *Store) InitializePreparation(ctx context.Context, id string, target pullrequestmetadata.PreparationTarget) error {
	if store == nil || store.db == nil || id == "" || target != pullrequestmetadata.PreparationTargetOpen {
		return pullrequestmetadata.ErrInvalidRequest
	}
	_, err := store.db.ExecContext(ctx, `
insert into pull_request_metadata(pull_request_id, preparation_target) values(?, ?)
on conflict(pull_request_id) do update set preparation_target = excluded.preparation_target, prepared_head_commit = null`, id, target)
	if err != nil {
		return errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
	}
	return nil
}

func (store *Store) ClearPreparation(ctx context.Context, id string) error {
	_, err := store.db.ExecContext(ctx, `update pull_request_metadata set preparation_target = null, prepared_head_commit = null where pull_request_id = ?`, id)
	return err
}

func (store *Store) MarkPrepared(ctx context.Context, id, headCommit string) error {
	result, err := store.db.ExecContext(ctx, `update pull_request_metadata set prepared_head_commit = ?
where pull_request_id = ?`, headCommit, id)
	if err != nil {
		return errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return pullrequestmetadata.ErrUpdateFailed
	}
	return nil
}

func (store *Store) Update(ctx context.Context, id, requestID, title, description string, updatedAt time.Time, state pullrequestmetadata.State) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx,
		"update pull_requests set title = ?, description = ?, updated_at = ? "+
			"where id = ? and status in ('wip', 'draft', 'open')",
		title, description, updatedAt.Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
	}
	if rows > 0 {
		if err := store.projection.UpdateMetadataInTransaction(ctx, tx, id, title, description, updatedAt, state.ProviderUpdatedAt); err != nil {
			return errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
		}
		if _, err := store.projection.CompleteOperationInTransaction(ctx, tx, requestID, "succeeded", ""); err != nil {
			return errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
		}
		data, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into pull_request_metadata(pull_request_id, state) values(?, ?)
		 on conflict(pull_request_id) do update set state = excluded.state`, id, string(data)); err != nil {
			return errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		store.projection.NotifyActionCompletion(id)
		return nil
	}
	_ = tx.Rollback()
	snapshot, getErr := store.Get(ctx, id)
	if getErr != nil {
		return getErr
	}
	if snapshot.Status != "wip" && snapshot.Status != "draft" && snapshot.Status != "open" {
		return pullrequestmetadata.ErrPullRequestInactive
	}
	return pullrequestmetadata.ErrUpdateFailed
}

func (store *Store) SaveState(ctx context.Context, id string, state pullrequestmetadata.State) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, `insert into pull_request_metadata(pull_request_id, state) values(?, ?)
	 on conflict(pull_request_id) do update set state = excluded.state`, id, string(data))
	if err != nil {
		return errors.Join(pullrequestmetadata.ErrUpdateFailed, err)
	}
	return nil
}
