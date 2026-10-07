package repository

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
)

var ErrInvalidCursor = errors.New("invalid commit history cursor")

type CommitPage struct {
	Commits    []Commit `json:"commits"`
	Head       string   `json:"head"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

type commitCursor struct {
	Head   string `json:"head"`
	Offset int    `json:"offset"`
}

// CommitPage pins pagination to an immutable head. Offsets traverse the full
// graph from that head, rather than losing merged branches at page boundaries.
func (s *Service) CommitPage(ctx context.Context, ref string, limit int, cursor string) (CommitPage, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	position := commitCursor{}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(raw, &position) != nil {
			return CommitPage{}, ErrInvalidCursor
		}
		_, err = hex.DecodeString(position.Head)
		if err != nil || (len(position.Head) != 40 && len(position.Head) != 64) || position.Offset < 1 || position.Offset > 1000000000 {
			return CommitPage{}, ErrInvalidCursor
		}
	} else {
		head, err := s.store.Commits(ctx, ref, 1)
		if err != nil {
			return CommitPage{}, err
		}
		if len(head) == 0 {
			return CommitPage{Commits: []Commit{}}, nil
		}
		position.Head = head[0].SHA
	}
	commits, err := s.store.CommitPage(ctx, position.Head, limit+1, position.Offset)
	if err != nil {
		return CommitPage{}, err
	}
	page := CommitPage{Head: position.Head, Commits: commits}
	if len(commits) > limit {
		page.Commits = commits[:limit]
		position.Offset += limit
		raw, _ := json.Marshal(position)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	if page.Commits == nil {
		page.Commits = []Commit{}
	}
	return page, nil
}
