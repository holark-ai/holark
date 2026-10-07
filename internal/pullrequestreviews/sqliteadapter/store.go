package sqliteadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestreviews"
)

type Store struct{ db *sql.DB }

func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if err := database.CreateSchema(ctx, db, `
create table if not exists pull_request_manual_reviews (
  id text primary key,
  pull_request_id text not null references pull_requests(id),
  created_at text not null,
  payload text not null
);
create index if not exists pull_request_manual_reviews_pr
  on pull_request_manual_reviews(pull_request_id, created_at, id);`); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Get(ctx context.Context, id string) (pullrequestreviews.Review, error) {
	var payload string
	if err := s.db.QueryRowContext(ctx, `select payload from pull_request_manual_reviews where id = ?`, id).Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return pullrequestreviews.Review{}, pullrequestreviews.ErrNotFound
		}
		return pullrequestreviews.Review{}, err
	}
	var review pullrequestreviews.Review
	err := json.Unmarshal([]byte(payload), &review)
	return review, err
}

func (s *Store) List(ctx context.Context, id string) ([]pullrequestreviews.Review, error) {
	rows, err := s.db.QueryContext(ctx, `select payload from pull_request_manual_reviews where pull_request_id = ? order by created_at, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reviews := make([]pullrequestreviews.Review, 0)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var review pullrequestreviews.Review
		if err := json.Unmarshal([]byte(payload), &review); err != nil {
			return nil, err
		}
		reviews = append(reviews, review)
	}
	return reviews, rows.Err()
}

func (s *Store) Save(ctx context.Context, review pullrequestreviews.Review) error {
	payload, err := json.Marshal(review)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `insert into pull_request_manual_reviews (id, pull_request_id, created_at, payload)
values (?, ?, ?, ?) on conflict(id) do update set payload = excluded.payload`, review.ID, review.PullRequestID, review.CreatedAt.Format("2006-01-02T15:04:05.000000000Z"), string(payload))
	return err
}
