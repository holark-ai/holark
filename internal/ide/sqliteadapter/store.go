// Package sqliteadapter persists IDE lifecycle state in the repository database.
package sqliteadapter

import (
	"context"
	"database/sql"
	"errors"
	"github.com/holark-ai/holark/internal/ide"
	"time"
)

type Store struct{ db *sql.DB }

func New(ctx context.Context, db *sql.DB) (*Store, error) {
	s := &Store{db}
	_, e := db.ExecContext(ctx, `create table if not exists holon_ides (id text primary key,holon_id text not null references holons(id) on delete cascade,provider text not null,state text not null,tab_order integer not null,desired_open integer not null,reason text not null default '',created_at text not null,updated_at text not null,ready_at text,closed_at text)`)
	return s, e
}
func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func nts(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}
func parse(v sql.NullString) *time.Time {
	if !v.Valid {
		return nil
	}
	t, e := time.Parse(time.RFC3339Nano, v.String)
	if e != nil {
		return nil
	}
	return &t
}
func scan(row interface{ Scan(...any) error }) (ide.IDE, error) {
	var v ide.IDE
	var c, u string
	var ready, closed sql.NullString
	e := row.Scan(&v.ID, &v.HolonID, &v.Provider, &v.State, &v.TabOrder, &v.DesiredOpen, &v.Reason, &c, &u, &ready, &closed)
	if errors.Is(e, sql.ErrNoRows) {
		return v, ide.ErrNotFound
	}
	if e != nil {
		return v, e
	}
	v.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
	v.UpdatedAt, _ = time.Parse(time.RFC3339Nano, u)
	v.ReadyAt = parse(ready)
	v.ClosedAt = parse(closed)
	return v, nil
}

const columns = `id,holon_id,provider,state,tab_order,desired_open,reason,created_at,updated_at,ready_at,closed_at`

func (s *Store) List(ctx context.Context, h string) ([]ide.IDE, error) {
	rows, e := s.db.QueryContext(ctx, `select `+columns+` from holon_ides where holon_id=? order by tab_order,created_at`, h)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []ide.IDE
	for rows.Next() {
		v, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Get(ctx context.Context, h, id string) (ide.IDE, error) {
	return scan(s.db.QueryRowContext(ctx, `select `+columns+` from holon_ides where holon_id=? and id=?`, h, id))
}
func (s *Store) Create(ctx context.Context, v ide.IDE) (ide.IDE, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return v, e
	}
	defer tx.Rollback()
	if e = tx.QueryRowContext(ctx, `select coalesce(max(tab_order),-1)+1 from (select tab_order from holon_agent_sessions where holon_id=? and closed_at is null union all select tab_order from holon_manual_terminals where holon_id=? and closed_at is null union all select tab_order from holon_ides where holon_id=? and desired_open=1)`, v.HolonID, v.HolonID, v.HolonID).Scan(&v.TabOrder); e != nil {
		return v, e
	}
	_, e = tx.ExecContext(ctx, `insert into holon_ides(`+columns+`) values(?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.HolonID, v.Provider, v.State, v.TabOrder, v.DesiredOpen, v.Reason, ts(v.CreatedAt), ts(v.UpdatedAt), nts(v.ReadyAt), nts(v.ClosedAt))
	if e != nil {
		return v, e
	}
	if e = tx.Commit(); e != nil {
		return v, e
	}
	return v, nil
}
func (s *Store) Update(ctx context.Context, v ide.IDE) (ide.IDE, error) {
	r, e := s.db.ExecContext(ctx, `update holon_ides set state=?,desired_open=?,reason=?,updated_at=?,ready_at=?,closed_at=? where id=? and holon_id=?`, v.State, v.DesiredOpen, v.Reason, ts(v.UpdatedAt), nts(v.ReadyAt), nts(v.ClosedAt), v.ID, v.HolonID)
	if e != nil {
		return v, e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return v, ide.ErrNotFound
	}
	return v, nil
}
func (s *Store) OpenAtStartup(ctx context.Context) ([]ide.IDE, error) {
	rows, e := s.db.QueryContext(ctx, `select `+columns+` from holon_ides where desired_open=1`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []ide.IDE
	for rows.Next() {
		v, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
