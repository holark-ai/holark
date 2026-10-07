// Package githubbinding protects the durable GitHub identity associated with a checkout.
package githubbinding

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"strings"
)

var ErrConfirmationRequired = errors.New("GitHub repository binding confirmation required")

type Binding struct{ RepositoryID, RepositoryURL, Owner, Name string }
type Store struct{ db *sql.DB }

func New(ctx context.Context, db *sql.DB) (*Store, error) {
	s := &Store{db}
	_, e := db.ExecContext(ctx, `create table if not exists github_repository_binding(repository_id text primary key,repository_url text not null,owner text not null,name text not null)`)
	return s, e
}
func (s *Store) Bind(ctx context.Context, id, raw string, confirm bool) (Binding, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Binding{RepositoryID: id}, nil
	}
	repo, e := githubapi.ParseRepositoryURL(raw)
	if e != nil {
		return Binding{}, e
	}
	next := Binding{id, raw, repo.Owner, repo.Name}
	var current Binding
	e = s.db.QueryRowContext(ctx, `select repository_id,repository_url,owner,name from github_repository_binding where repository_id=?`, id).Scan(&current.RepositoryID, &current.RepositoryURL, &current.Owner, &current.Name)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return Binding{}, e
	}
	if e == nil && strings.EqualFold(current.Owner, next.Owner) && strings.EqualFold(current.Name, next.Name) {
		return current, nil
	}
	if e == nil && !confirm {
		return Binding{}, fmt.Errorf("%w: %s/%s -> %s/%s; rerun once with HOLARK_CONFIRM_GITHUB_REPOSITORY=1", ErrConfirmationRequired, current.Owner, current.Name, next.Owner, next.Name)
	}
	_, e = s.db.ExecContext(ctx, `insert into github_repository_binding(repository_id,repository_url,owner,name) values(?,?,?,?) on conflict(repository_id) do update set repository_url=excluded.repository_url,owner=excluded.owner,name=excluded.name`, id, raw, next.Owner, next.Name)
	return next, e
}
