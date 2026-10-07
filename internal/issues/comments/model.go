// Package comments owns the local projection of flat issue discussions.
package comments

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxBodyCharacters = 65536

var ErrInvalidBody = errors.New("comment body must be nonblank and at most 65,536 characters")
var ErrNotFound = errors.New("issue comment not found")

type Author struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	URL       string `json:"url"`
}
type Comment struct {
	ID           string    `json:"id"`
	IssueID      string    `json:"issue_id"`
	Body         string    `json:"body"`
	Author       Author    `json:"author"`
	GitHubID     string    `json:"github_id"`
	GitHubNodeID string    `json:"github_node_id"`
	URL          string    `json:"url"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	CanEdit      bool      `json:"can_edit"`
	CanDelete    bool      `json:"can_delete"`
}
type Discussion struct {
	Comments   []Comment  `json:"comments"`
	SyncedAt   *time.Time `json:"synced_at"`
	CanComment *bool      `json:"can_comment"`
}
type Store interface {
	ListComments(context.Context, string) (Discussion, error)
	GetComment(context.Context, string) (Comment, error)
	UpsertComment(context.Context, Comment) (Comment, error)
	DeleteComment(context.Context, string) error
	ReconcileComments(context.Context, string, []Comment, *bool, time.Time) error
}

func ValidateBody(body string) error {
	if !utf8.ValidString(body) || strings.TrimSpace(body) == "" || utf8.RuneCountInString(body) > MaxBodyCharacters {
		return ErrInvalidBody
	}
	return nil
}
