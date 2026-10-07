// Package sqliteadapter persists pull-request panel retention independently of
// provider-owned pull-request lifecycle data.
package sqliteadapter

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/holark-ai/holark/internal/database"
)

type Store struct{ db *sql.DB }

func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if err := database.CreateSchema(ctx, db, `
create table if not exists pull_request_panel_pins (
  pull_request_id text primary key references pull_requests(id) on delete cascade
);`); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (store *Store) Pin(ctx context.Context, pullRequestID string) error {
	pullRequestID = strings.TrimSpace(pullRequestID)
	if pullRequestID == "" {
		return errors.New("pull request ID is blank")
	}
	_, err := store.db.ExecContext(ctx, `insert into pull_request_panel_pins(pull_request_id) values(?) on conflict(pull_request_id) do nothing`, pullRequestID)
	return err
}

func (store *Store) Unpin(ctx context.Context, pullRequestID string) error {
	pullRequestID = strings.TrimSpace(pullRequestID)
	if pullRequestID == "" {
		return errors.New("pull request ID is blank")
	}
	_, err := store.db.ExecContext(ctx, `delete from pull_request_panel_pins where pull_request_id = ?`, pullRequestID)
	return err
}

func (store *Store) Pinned(ctx context.Context, pullRequestID string) (bool, error) {
	pullRequestID = strings.TrimSpace(pullRequestID)
	if pullRequestID == "" {
		return false, errors.New("pull request ID is blank")
	}
	var pinned bool
	err := store.db.QueryRowContext(ctx, `select true from pull_request_panel_pins where pull_request_id = ?`, pullRequestID).Scan(&pinned)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return pinned, err
}

func (store *Store) List(ctx context.Context, repositoryID string) (map[string]bool, error) {
	repositoryID = strings.TrimSpace(repositoryID)
	if repositoryID == "" {
		return nil, errors.New("repository ID is blank")
	}
	rows, err := store.db.QueryContext(ctx, `select pins.pull_request_id
from pull_request_panel_pins pins
join pull_requests prs on prs.id = pins.pull_request_id
where prs.repository_id = ?`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var pullRequestID string
		if err := rows.Scan(&pullRequestID); err != nil {
			return nil, err
		}
		result[pullRequestID] = true
	}
	return result, rows.Err()
}
