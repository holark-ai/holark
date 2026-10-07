package sqliteadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/holark-ai/holark/internal/pullrequestwork"
)

type Store struct{ db *sql.DB }

func New(ctx context.Context, db *sql.DB) (*Store, error) {
	_, e := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS pull_request_work (id TEXT PRIMARY KEY, pull_request_id TEXT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL, document BLOB NOT NULL); CREATE INDEX IF NOT EXISTS pull_request_work_pr ON pull_request_work(pull_request_id, kind); CREATE UNIQUE INDEX IF NOT EXISTS pull_request_work_request ON pull_request_work(json_extract(document, '$.request_id')) WHERE kind = 'rebase' AND json_extract(document, '$.request_id') != '';`)
	return &Store{db: db}, e
}
func (s *Store) Create(ctx context.Context, w pullrequestwork.Work) error {
	return s.CreateBatch(ctx, []pullrequestwork.Work{w})
}

// CreateBatch preserves submitted order and commits all requests together.
func (s *Store) CreateBatch(ctx context.Context, works []pullrequestwork.Work) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, w := range works {
		w.Freshness = nil
		b, err := json.Marshal(w)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO pull_request_work(id,pull_request_id,kind,status,document) VALUES(?,?,?,?,?)`, w.ID, w.PullRequestID, w.Kind, w.Status, b); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Update(ctx context.Context, w pullrequestwork.Work) error {
	return update(ctx, s.db, w)
}

// UpdateInTransaction persists work through a transaction owned by a
// cross-domain publication adapter.
func (s *Store) UpdateInTransaction(ctx context.Context, tx *sql.Tx, w pullrequestwork.Work) error {
	return update(ctx, tx, w)
}

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func update(ctx context.Context, exec executor, w pullrequestwork.Work) error {
	w.Freshness = nil
	b, e := json.Marshal(w)
	if e != nil {
		return e
	}
	r, e := exec.ExecContext(ctx, `UPDATE pull_request_work SET status=?,document=? WHERE id=?`, w.Status, b, w.ID)
	if e == nil {
		if n, _ := r.RowsAffected(); n == 0 {
			return pullrequestwork.ErrNotFound
		}
	}
	return e
}
func (s *Store) Get(ctx context.Context, id string) (pullrequestwork.Work, error) {
	var b []byte
	e := s.db.QueryRowContext(ctx, `SELECT document FROM pull_request_work WHERE id=?`, id).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		e = pullrequestwork.ErrNotFound
	}
	var w pullrequestwork.Work
	if e == nil {
		e = json.Unmarshal(b, &w)
	}
	return w, e
}
func (s *Store) List(ctx context.Context, pr string) ([]pullrequestwork.Work, error) {
	q := `SELECT document FROM pull_request_work`
	args := []any{}
	if pr != "" {
		q += ` WHERE pull_request_id=?`
		args = append(args, pr)
	}
	q += ` ORDER BY rowid`
	rows, e := s.db.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []pullrequestwork.Work
	for rows.Next() {
		var b []byte
		var w pullrequestwork.Work
		if e = rows.Scan(&b); e == nil {
			e = json.Unmarshal(b, &w)
		}
		if e != nil {
			return nil, e
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// HasWorkerReference reports whether durable address work refers to a comment.
// Comment synchronization uses this to avoid deleting provider comments that
// are still needed by the work history.
func (s *Store) HasWorkerReference(ctx context.Context, commentID string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT document FROM pull_request_work WHERE kind=?`, pullrequestwork.KindWorker)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var document []byte
		if err := rows.Scan(&document); err != nil {
			return false, err
		}
		var work pullrequestwork.Work
		if err := json.Unmarshal(document, &work); err != nil {
			return false, err
		}
		if work.CommentID == commentID {
			return true, nil
		}
	}
	return false, rows.Err()
}
