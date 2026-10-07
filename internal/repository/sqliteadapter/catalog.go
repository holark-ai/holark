// Package sqliteadapter persists the singleton repository identity needed by related domains.
package sqliteadapter

import (
	"context"
	"database/sql"
	"github.com/holark-ai/holark/internal/repository"
	"time"
)

func Bind(ctx context.Context, db *sql.DB, d repository.Descriptor) error {
	_, e := db.ExecContext(ctx, `create table if not exists repositories(id text primary key,name text not null,repository_url text not null,default_branch text not null,created_at text not null,updated_at text not null)`)
	if e != nil {
		return e
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, e = db.ExecContext(ctx, `insert into repositories(id,name,repository_url,default_branch,created_at,updated_at) values(?,?,?,?,?,?) on conflict(id) do update set name=excluded.name,repository_url=excluded.repository_url,default_branch=excluded.default_branch,updated_at=excluded.updated_at`, d.ID, d.ID, d.RepositoryURL, d.DefaultBranch, now, now)
	return e
}
