package storeadapter

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/holark-ai/holark/internal/issues/comments"
)

const commentColumns = `id, issue_id, body, author_login, author_avatar_url, author_url, github_id, github_node_id, url, created_at, updated_at, can_edit, can_delete`

func scanComment(row interface{ Scan(...any) error }) (comments.Comment, error) {
	var c comments.Comment
	var created, updated string
	err := row.Scan(&c.ID, &c.IssueID, &c.Body, &c.Author.Login, &c.Author.AvatarURL, &c.Author.URL, &c.GitHubID, &c.GitHubNodeID, &c.URL, &created, &updated, &c.CanEdit, &c.CanDelete)
	if errors.Is(err, sql.ErrNoRows) {
		return c, comments.ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.CreatedAt, err = decodeTime(created)
	if err != nil {
		return c, err
	}
	c.UpdatedAt, err = decodeTime(updated)
	return c, err
}
func (s *Store) GetComment(ctx context.Context, id string) (comments.Comment, error) {
	return scanComment(s.db.QueryRowContext(ctx, `select `+commentColumns+` from issue_comments where id=?`, id))
}
func (s *Store) ListComments(ctx context.Context, id string) (comments.Discussion, error) {
	d := comments.Discussion{Comments: []comments.Comment{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	var at string
	var can sql.NullBool
	err = tx.QueryRowContext(ctx, `select synced_at,can_comment from issue_comment_sync where issue_id=?`, id).Scan(&at, &can)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return d, err
	}
	if err == nil {
		parsed, e := decodeTime(at)
		if e != nil {
			return d, e
		}
		d.SyncedAt = &parsed
		if can.Valid {
			d.CanComment = &can.Bool
		}
	}
	rows, err := tx.QueryContext(ctx, `select `+commentColumns+` from issue_comments where issue_id=? order by created_at, length(github_id), github_id`, id)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		c, e := scanComment(rows)
		if e != nil {
			rows.Close()
			return d, e
		}
		d.Comments = append(d.Comments, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return d, err
	}
	return d, tx.Commit()
}
func upsertComment(ctx context.Context, tx *sql.Tx, c comments.Comment) (comments.Comment, error) {
	if c.GitHubID == "" || c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		return c, errors.New("incomplete remote comment")
	}
	// Provider identity is authoritative; never replace an existing local ID.
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return c, err
	}
	c.ID = "ic_" + hex.EncodeToString(value)
	return scanComment(tx.QueryRowContext(ctx, `insert into issue_comments (`+commentColumns+`) values (?,?,?,?,?,?,?,?,?,?,?,?,?)
 on conflict(issue_id,github_id) do update set body=excluded.body, author_login=excluded.author_login,
 author_avatar_url=excluded.author_avatar_url, author_url=excluded.author_url, github_node_id=excluded.github_node_id,
 url=excluded.url, created_at=excluded.created_at, updated_at=excluded.updated_at, can_edit=excluded.can_edit, can_delete=excluded.can_delete returning `+commentColumns,
		c.ID, c.IssueID, c.Body, c.Author.Login, c.Author.AvatarURL, c.Author.URL, c.GitHubID, c.GitHubNodeID, c.URL,
		c.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z"), c.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z"), c.CanEdit, c.CanDelete))
}
func (s *Store) UpsertComment(ctx context.Context, c comments.Comment) (comments.Comment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	c, err = upsertComment(ctx, tx, c)
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}
func (s *Store) DeleteComment(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `delete from issue_comments where id=?`, id)
	return err
}
func (s *Store) ReconcileComments(ctx context.Context, id string, remote []comments.Comment, can *bool, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	remoteIDs := make([]string, 0, len(remote))
	for _, c := range remote {
		c.IssueID = id
		if _, err = upsertComment(ctx, tx, c); err != nil {
			return err
		}
		remoteIDs = append(remoteIDs, c.GitHubID)
	}
	ids, err := json.Marshal(remoteIDs)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `delete from issue_comments where issue_id=? and github_id not in (select value from json_each(?))`, id, string(ids)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `insert into issue_comment_sync(issue_id,synced_at,can_comment) values(?,?,?) on conflict(issue_id) do update set synced_at=excluded.synced_at,can_comment=excluded.can_comment`, id, encodeTime(at), can)
	if err != nil {
		return err
	}
	return tx.Commit()
}

var _ comments.Store = (*Store)(nil)
