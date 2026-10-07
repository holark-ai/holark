package issues

import (
	"encoding/json"
	"errors"
	"time"
)

type IssueStatus string

const (
	IssueOpen   IssueStatus = "open"
	IssueClosed IssueStatus = "closed"
)

type IssueSyncProvider string

const (
	IssueSyncProviderGitHub IssueSyncProvider = "github"
)

type Label struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Color          string `json:"color"`
	Description    string `json:"description"`
	SyncProvider   string `json:"-"`
	SyncExternalID string `json:"-"`
}

type Issue struct {
	ID                   string          `json:"id"`
	RepositoryID         string          `json:"repository_id"`
	Title                string          `json:"title"`
	Body                 string          `json:"body"`
	Status               IssueStatus     `json:"status"`
	SyncProvider         string          `json:"sync_provider,omitempty"`
	SyncExternalID       string          `json:"sync_external_id,omitempty"`
	SyncData             json.RawMessage `json:"sync_data"`
	Labels               []Label         `json:"labels"`
	IssuerHolarkID       string          `json:"issuer_holark_id"`
	AssigneeHolarkIDs    []string        `json:"assignee_holark_ids"`
	LinkedPullRequestIDs []string        `json:"linked_pull_request_ids"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	ClosedAt             *time.Time      `json:"closed_at,omitempty"`
	SyncedAt             *time.Time      `json:"synced_at,omitempty"`
}

var ErrIssueNotFound = errors.New("issue not found")
