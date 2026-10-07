package sqliteadapter

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/holark-ai/holark/internal/prompt_templates"
)

type Store struct{ db *sql.DB }

func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	_, err := db.ExecContext(ctx, `
create table if not exists prompt_templates (
  key text primary key,
  value text not null,
  created_at text not null,
  updated_at text not null
)`)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (store *Store) List(ctx context.Context) ([]prompttemplates.Override, error) {
	rows, err := store.db.QueryContext(ctx, `select key, value from prompt_templates order by key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []prompttemplates.Override{}
	for rows.Next() {
		var value prompttemplates.Override
		if err := rows.Scan(&value.Key, &value.Value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (store *Store) Upsert(ctx context.Context, key, value string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := store.db.ExecContext(ctx, `
insert into prompt_templates(key,value,created_at,updated_at) values(?,?,?,?)
on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`, key, value, now, now)
	return err
}

func (store *Store) Delete(ctx context.Context, key string) error {
	_, err := store.db.ExecContext(ctx, `delete from prompt_templates where key = ?`, key)
	return err
}

var _ prompttemplates.Store = (*Store)(nil)
