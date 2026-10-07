// Package storeadapter implements project-owned GitHub membership persistence.
package storeadapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/githubidentity"
)

type Store struct{ db *sql.DB }

func (store *Store) SearchProjectMembers(ctx context.Context, repositoryID, query string, limit int) ([]githubidentity.Member, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := store.db.QueryContext(ctx, `select id, login, coalesce(avatar_url, ''), coalesce(profile_url, ''), permission, is_me
from github_repository_members
where repository_id = ? and instr(lower(login), lower(?)) > 0
order by login collate nocase, id
limit ?`, strings.TrimSpace(repositoryID), strings.TrimSpace(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := make([]githubidentity.Member, 0)
	for rows.Next() {
		var member githubidentity.Member
		if err := rows.Scan(&member.ID, &member.Login, &member.AvatarURL, &member.ProfileURL, &member.Permission, &member.IsMe); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

func (store *Store) GetProjectMembers(ctx context.Context, repositoryID string, ids []string) (githubidentity.MemberResolution, error) {
	normalized := make([]string, len(ids))
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for index, id := range ids {
		normalized[index] = strings.TrimSpace(id)
		if normalized[index] == "" {
			continue
		}
		if _, ok := seen[normalized[index]]; ok {
			continue
		}
		seen[normalized[index]] = struct{}{}
		unique = append(unique, normalized[index])
	}

	membersByID := make(map[string]githubidentity.Member, len(unique))
	if len(unique) > 0 {
		placeholders := make([]string, len(unique))
		arguments := make([]any, 0, len(unique)+1)
		arguments = append(arguments, strings.TrimSpace(repositoryID))
		for index, id := range unique {
			placeholders[index] = "?"
			arguments = append(arguments, id)
		}
		rows, err := store.db.QueryContext(ctx, `select id, login, coalesce(avatar_url, ''), coalesce(profile_url, ''), permission, is_me
from github_repository_members
where repository_id = ? and id in (`+strings.Join(placeholders, ",")+`)`, arguments...)
		if err != nil {
			return githubidentity.MemberResolution{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var member githubidentity.Member
			if err := rows.Scan(&member.ID, &member.Login, &member.AvatarURL, &member.ProfileURL, &member.Permission, &member.IsMe); err != nil {
				return githubidentity.MemberResolution{}, err
			}
			membersByID[member.ID] = member
		}
		if err := rows.Err(); err != nil {
			return githubidentity.MemberResolution{}, err
		}
	}

	result := githubidentity.MemberResolution{
		Members:    make([]githubidentity.Member, 0, len(normalized)),
		MissingIDs: make([]string, 0),
	}
	for _, id := range normalized {
		if member, ok := membersByID[id]; ok {
			result.Members = append(result.Members, member)
		} else {
			result.MissingIDs = append(result.MissingIDs, id)
		}
	}
	return result, nil
}

func (store *Store) ResolveProjectMemberIDs(ctx context.Context, repositoryID string, githubNodeIDs []string) ([]string, error) {
	return resolveProjectMembers(ctx, store.db, strings.TrimSpace(repositoryID), githubNodeIDs,
		`select id from github_repository_members where repository_id = ? and github_node_id = ?`, "GitHub node ID")
}

func (store *Store) ResolveProjectMemberLogins(ctx context.Context, repositoryID string, holarkIDs []string) ([]string, error) {
	return resolveProjectMembers(ctx, store.db, strings.TrimSpace(repositoryID), holarkIDs,
		`select login from github_repository_members where repository_id = ? and id = ?`, "Holark member ID")
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func resolveProjectMembers(ctx context.Context, db rowQueryer, repositoryID string, values []string, query, kind string) ([]string, error) {
	if repositoryID == "" {
		return nil, fmt.Errorf("%w: project ID is blank", githubidentity.ErrMemberUnresolved)
	}
	result := make([]string, len(values))
	for index, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, fmt.Errorf("%w: %s at position %d is blank", githubidentity.ErrMemberUnresolved, kind, index)
		}
		if err := db.QueryRowContext(ctx, query, repositoryID, value).Scan(&result[index]); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("%w: %s %q is not a member of project %q", githubidentity.ErrMemberUnresolved, kind, value, repositoryID)
			}
			return nil, err
		}
	}
	return result, nil
}

func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if err := database.CreateSchema(ctx, db, `
create table if not exists github_repository_members (
  id text primary key,
  repository_id text not null references repositories(id) on delete cascade,
  github_node_id text not null,
  login text not null,
  avatar_url text,
  profile_url text,
  permission text not null,
  is_me integer not null default 0 check(is_me in (0, 1)),
  last_seen_at text not null,
  created_at text not null,
  updated_at text not null,
  unique(repository_id, github_node_id)
);
create unique index if not exists github_repository_members_one_me on github_repository_members(repository_id) where is_me = 1;
create index if not exists github_repository_members_order on github_repository_members(repository_id, login collate nocase, id);
create table if not exists github_repository_member_sync_state (
  repository_id text primary key references repositories(id) on delete cascade,
  synced_at text not null
);`); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (store *Store) ListProjectMembers(ctx context.Context, repositoryID string) ([]githubidentity.Member, error) {
	return listProjectMembers(ctx, store.db, repositoryID)
}

func (store *Store) ObserveProjectMember(ctx context.Context, repositoryID string, member githubidentity.StoredMember) (string, error) {
	var id string
	err := store.db.QueryRowContext(ctx, `
insert into github_repository_members(
  id, repository_id, github_node_id, login, avatar_url, profile_url, permission, is_me, last_seen_at, created_at, updated_at
) values(?, ?, ?, ?, ?, ?, '', 0, ?, ?, ?)
on conflict(repository_id, github_node_id) do update set
  login = excluded.login, avatar_url = excluded.avatar_url, profile_url = excluded.profile_url,
  last_seen_at = excluded.last_seen_at, updated_at = excluded.updated_at
returning id`, member.ID, repositoryID, member.NodeID, member.Login, nullable(member.AvatarURL), nullable(member.ProfileURL),
		encodeTime(member.LastSeenAt), encodeTime(member.CreatedAt), encodeTime(member.UpdatedAt)).Scan(&id)
	return id, err
}

func (store *Store) SyncProjectMembers(ctx context.Context, repositoryID string, incoming []githubidentity.StoredMember, syncedAt time.Time) ([]githubidentity.Member, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, member := range incoming {
		if _, err := tx.ExecContext(ctx, `
insert into github_repository_members(
  id, repository_id, github_node_id, login, avatar_url, profile_url, permission, is_me, last_seen_at, created_at, updated_at
) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(repository_id, github_node_id) do update set
  login = excluded.login, avatar_url = excluded.avatar_url, profile_url = excluded.profile_url,
  permission = excluded.permission,
  last_seen_at = excluded.last_seen_at, updated_at = excluded.updated_at`,
			member.ID, repositoryID, member.NodeID, member.Login, nullable(member.AvatarURL), nullable(member.ProfileURL), member.Permission,
			false, encodeTime(member.LastSeenAt), encodeTime(member.CreatedAt), encodeTime(member.UpdatedAt)); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into github_repository_member_sync_state(repository_id, synced_at) values(?, ?) on conflict(repository_id) do update set synced_at = excluded.synced_at`, repositoryID, encodeTime(syncedAt)); err != nil {
		return nil, err
	}
	result, err := listProjectMembers(ctx, tx, repositoryID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listProjectMembers(ctx context.Context, db queryer, repositoryID string) ([]githubidentity.Member, error) {
	rows, err := db.QueryContext(ctx, `select id, login, coalesce(avatar_url, ''), coalesce(profile_url, ''), permission, is_me from github_repository_members where repository_id = ? order by login collate nocase, id`, strings.TrimSpace(repositoryID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]githubidentity.Member, 0)
	for rows.Next() {
		var member githubidentity.Member
		if err := rows.Scan(&member.ID, &member.Login, &member.AvatarURL, &member.ProfileURL, &member.Permission, &member.IsMe); err != nil {
			return nil, err
		}
		result = append(result, member)
	}
	return result, rows.Err()
}

func encodeTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

var _ githubidentity.Store = (*Store)(nil)

func (s *Store) SetAuthenticatedMember(ctx context.Context, repo, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `update github_repository_members set is_me=0 where repository_id=? and is_me=1`, repo); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `update github_repository_members set is_me=1 where repository_id=? and id=?`, repo, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return githubidentity.ErrMemberUnresolved
	}
	return tx.Commit()
}
