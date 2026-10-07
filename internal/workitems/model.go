package workitems

import (
	"github.com/holark-ai/holark/internal/githubidentity"
	"github.com/holark-ai/holark/internal/issues"
)

type Query struct {
	Issues                     bool
	Labels                     []*LabelExpression
	Canonical                  string
	Title                      string
	Terms                      []string
	Number                     int
	States                     []string
	Statuses                   []string
	Draft                      *bool
	Author, Assignee, Reviewer string
	Sort                       string
}
type Row struct {
	Labels               []issues.Label `json:"labels,omitempty"`
	LinkedPullRequestIDs []string       `json:"linked_pull_request_ids,omitempty"`
	URL                  string         `json:"url,omitempty"`
	ID                   string         `json:"id"`
	Kind                 string         `json:"kind"`
	Title                string         `json:"title"`
	Number               int            `json:"number"`
	Status               string         `json:"status"`
	AuthorID             string         `json:"author_id,omitempty"`
	AssigneeIDs          []string       `json:"assignee_ids"`
	ReviewerIDs          []string       `json:"reviewer_ids"`
	UpdatedAt            string         `json:"updated_at"`
	Reasons              []string       `json:"reasons"`
}
type SyncStatus struct {
	Section     string `json:"section"`
	AttemptedAt string `json:"attempted_at"`
	SyncedAt    string `json:"synced_at"`
	Error       string `json:"error,omitempty"`
}
type Result struct {
	Rows     []Row                  `json:"rows"`
	Total    int                    `json:"total"`
	Page     int                    `json:"page"`
	PerPage  int                    `json:"per_page"`
	Query    string                 `json:"query,omitempty"`
	Identity *githubidentity.Member `json:"identity"`
	Counts   map[string]int         `json:"counts,omitempty"`
	Sync     []SyncStatus           `json:"sync"`
}
