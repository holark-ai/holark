// Package sqliteadapter owns participant schema and persistence.
package sqliteadapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestparticipants"
)

type Store struct{ db *sql.DB }

func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if err := database.CreateSchema(ctx, db, `
create table if not exists pull_request_authors (
 pull_request_id text primary key references pull_requests(id) on delete cascade,
 holark_id text not null
);
create table if not exists pull_request_assignees (
  pull_request_id text not null references pull_requests(id) on delete cascade,
  holark_id text not null,
  position integer not null check(position >= 0),
  primary key(pull_request_id, holark_id),
  unique(pull_request_id, position)
);
create table if not exists pull_request_requested_reviewers (
  pull_request_id text not null references pull_requests(id) on delete cascade,
  holark_id text not null,
  position integer not null check(position >= 0),
  primary key(pull_request_id, holark_id),
  unique(pull_request_id, position)
);`); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (store *Store) Get(ctx context.Context, id string) (pullrequestparticipants.Snapshot, error) {
	return get(ctx, store.db, strings.TrimSpace(id))
}

func (store *Store) GetMany(ctx context.Context, ids []string) (map[string]pullrequestparticipants.Snapshot, error) {
	result := make(map[string]pullrequestparticipants.Snapshot, len(ids))
	clean := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, errors.New("pull request ID is blank")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		clean = append(clean, id)
		result[id] = pullrequestparticipants.EmptySnapshot(id)
	}
	if len(clean) == 0 {
		return result, nil
	}
	if err := loadMany(ctx, store.db, "pull_request_assignees", clean, result, true); err != nil {
		return nil, err
	}
	if err := loadMany(ctx, store.db, "pull_request_requested_reviewers", clean, result, false); err != nil {
		return nil, err
	}
	return result, nil
}

func (store *Store) ReplaceSnapshot(ctx context.Context, snapshot pullrequestparticipants.Snapshot) (pullrequestparticipants.Snapshot, error) {
	return store.transaction(ctx, snapshot.PullRequestID, func(ctx context.Context, tx *sql.Tx, id string) error {
		if snapshot.AuthorHolarkID != "" {
			if _, err := tx.ExecContext(ctx, `insert into pull_request_authors(pull_request_id,holark_id) values(?,?) on conflict(pull_request_id) do update set holark_id=excluded.holark_id`, id, snapshot.AuthorHolarkID); err != nil {
				return err
			}
		}
		assignees, err := normalize(snapshot.AssigneeHolarkIDs)
		if err != nil {
			return err
		}
		reviewers, err := normalize(snapshot.RequestedReviewerHolarkIDs)
		if err != nil {
			return err
		}
		if err := replaceCollection(ctx, tx, "pull_request_assignees", id, assignees); err != nil {
			return err
		}
		return replaceCollection(ctx, tx, "pull_request_requested_reviewers", id, reviewers)
	})
}

func (store *Store) ReplaceAssignees(ctx context.Context, id string, ids []string) (pullrequestparticipants.Snapshot, error) {
	return store.replaceCollection(ctx, id, "pull_request_assignees", ids)
}

func (store *Store) AddAssignee(ctx context.Context, id, memberID string) (pullrequestparticipants.Snapshot, error) {
	return store.changeCollection(ctx, id, "pull_request_assignees", memberID, true)
}

func (store *Store) RemoveAssignee(ctx context.Context, id, memberID string) (pullrequestparticipants.Snapshot, error) {
	return store.changeCollection(ctx, id, "pull_request_assignees", memberID, false)
}

func (store *Store) ReplaceRequestedReviewers(ctx context.Context, id string, ids []string) (pullrequestparticipants.Snapshot, error) {
	return store.replaceCollection(ctx, id, "pull_request_requested_reviewers", ids)
}

func (store *Store) AddRequestedReviewer(ctx context.Context, id, memberID string) (pullrequestparticipants.Snapshot, error) {
	return store.changeCollection(ctx, id, "pull_request_requested_reviewers", memberID, true)
}

func (store *Store) RemoveRequestedReviewer(ctx context.Context, id, memberID string) (pullrequestparticipants.Snapshot, error) {
	return store.changeCollection(ctx, id, "pull_request_requested_reviewers", memberID, false)
}

func (store *Store) replaceCollection(ctx context.Context, id, table string, values []string) (pullrequestparticipants.Snapshot, error) {
	return store.transaction(ctx, id, func(ctx context.Context, tx *sql.Tx, id string) error {
		normalized, err := normalize(values)
		if err != nil {
			return err
		}
		return replaceCollection(ctx, tx, table, id, normalized)
	})
}

func (store *Store) changeCollection(ctx context.Context, id, table, memberID string, add bool) (pullrequestparticipants.Snapshot, error) {
	return store.transaction(ctx, id, func(ctx context.Context, tx *sql.Tx, id string) error {
		memberID = strings.TrimSpace(memberID)
		if memberID == "" {
			return errors.New("participant ID is blank")
		}
		values, err := loadCollection(ctx, tx, table, id)
		if err != nil {
			return err
		}
		next := make([]string, 0, len(values)+1)
		found := false
		for _, value := range values {
			if value == memberID {
				found = true
				if !add {
					continue
				}
			}
			next = append(next, value)
		}
		if add && !found {
			next = append(next, memberID)
		}
		if found == add {
			if add || !found {
				return nil
			}
		}
		return replaceCollection(ctx, tx, table, id, next)
	})
}

func (store *Store) transaction(ctx context.Context, rawID string, operation func(context.Context, *sql.Tx, string) error) (pullrequestparticipants.Snapshot, error) {
	id := strings.TrimSpace(rawID)
	if id == "" {
		return pullrequestparticipants.Snapshot{}, errors.New("pull request ID is blank")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return pullrequestparticipants.Snapshot{}, err
	}
	defer tx.Rollback()
	if err := operation(ctx, tx, id); err != nil {
		return pullrequestparticipants.Snapshot{}, err
	}
	snapshot, err := get(ctx, tx, id)
	if err != nil {
		return pullrequestparticipants.Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return pullrequestparticipants.Snapshot{}, err
	}
	return snapshot, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func get(ctx context.Context, query queryer, id string) (pullrequestparticipants.Snapshot, error) {
	snapshot := pullrequestparticipants.EmptySnapshot(id)
	authorRows, err := query.QueryContext(ctx, `select holark_id from pull_request_authors where pull_request_id=?`, id)
	if err != nil {
		return snapshot, err
	}
	if authorRows.Next() {
		err = authorRows.Scan(&snapshot.AuthorHolarkID)
	}
	if err == nil {
		err = authorRows.Err()
	}
	authorRows.Close()
	if err != nil {
		return snapshot, err
	}
	assignees, err := loadCollection(ctx, query, "pull_request_assignees", id)
	if err != nil {
		return pullrequestparticipants.Snapshot{}, err
	}
	reviewers, err := loadCollection(ctx, query, "pull_request_requested_reviewers", id)
	if err != nil {
		return pullrequestparticipants.Snapshot{}, err
	}
	snapshot.AssigneeHolarkIDs = assignees
	snapshot.RequestedReviewerHolarkIDs = reviewers
	return snapshot, nil
}

func loadCollection(ctx context.Context, query queryer, table, id string) ([]string, error) {
	rows, err := query.QueryContext(ctx, `select holark_id from `+table+` where pull_request_id = ? order by position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func loadMany(ctx context.Context, query queryer, table string, ids []string, result map[string]pullrequestparticipants.Snapshot, assignees bool) error {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	arguments := make([]any, len(ids))
	for index, id := range ids {
		arguments[index] = id
	}
	rows, err := query.QueryContext(ctx, `select pull_request_id, holark_id from `+table+` where pull_request_id in (`+placeholders+`) order by pull_request_id, position`, arguments...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, memberID string
		if err := rows.Scan(&id, &memberID); err != nil {
			return err
		}
		snapshot := result[id]
		if assignees {
			snapshot.AssigneeHolarkIDs = append(snapshot.AssigneeHolarkIDs, memberID)
		} else {
			snapshot.RequestedReviewerHolarkIDs = append(snapshot.RequestedReviewerHolarkIDs, memberID)
		}
		result[id] = snapshot
	}
	return rows.Err()
}

func replaceCollection(ctx context.Context, tx *sql.Tx, table, id string, values []string) error {
	if _, err := tx.ExecContext(ctx, `delete from `+table+` where pull_request_id = ?`, id); err != nil {
		return err
	}
	for position, value := range values {
		if _, err := tx.ExecContext(ctx, `insert into `+table+`(pull_request_id, holark_id, position) values(?, ?, ?)`, id, value, position); err != nil {
			return err
		}
	}
	return nil
}

func normalize(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, fmt.Errorf("participant ID is blank")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

var _ pullrequestparticipants.Store = (*Store)(nil)
