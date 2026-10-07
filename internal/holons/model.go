// Package holons owns Holark's durable local work-unit model.
package holons

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/protocol"
)

func CanonicalBaseBranch(branch string) string {
	return strings.TrimPrefix(strings.TrimSpace(branch), "origin/")
}

type Kind string

const (
	KindNormal     Kind = "normal"
	KindIssue      Kind = "issue"
	KindPullReview Kind = "pr_review"
	KindPullWorker Kind = "pr_worker"
	KindPRMetadata Kind = "pr_metadata"
	KindRebase     Kind = "rebase"
)

type Status string

const (
	StatusNaming         Status = "naming"
	StatusQueued         Status = "queued"
	StatusPreparing      Status = "preparing"
	StatusRunning        Status = "running"
	StatusCancelling     Status = "cancelling"
	StatusCancelled      Status = "cancelled"
	StatusCompleted      Status = "completed"
	StatusFailed         Status = "failed"
	StatusLost           Status = "lost"
	StatusRestoring      Status = "restoring"
	StatusRecoveryFailed Status = "recovery_failed"
	StatusExpired        Status = "expired"
)

type AgentSession struct {
	Permissions            string                 `json:"permissions,omitempty"`
	Model                  string                 `json:"model,omitempty"`
	ID                     string                 `json:"id"`
	HolonID                string                 `json:"holon_id"`
	TerminalID             string                 `json:"terminal_id,omitempty"`
	AgentType              string                 `json:"agent_type"`
	Title                  string                 `json:"title"`
	Prompt                 string                 `json:"prompt"`
	Status                 string                 `json:"status"`
	Reason                 string                 `json:"reason,omitempty"`
	ResumeTarget           string                 `json:"resume_target,omitempty"`
	RolloutPath            string                 `json:"rollout_path,omitempty"`
	Activity               protocol.AgentActivity `json:"activity"`
	InputState             string                 `json:"input_state,omitempty"`
	ObservabilityStatus    string                 `json:"observability_status,omitempty"`
	ObservabilityMessage   string                 `json:"observability_message,omitempty"`
	ContextTokens          *int64                 `json:"context_tokens,omitempty"`
	CommitPrompt           *CommitPrompt          `json:"commit_prompt,omitempty"`
	CommitStartHead        string                 `json:"-"`
	CommitPromptChangeHash string                 `json:"-"`
	ExitCode               *int                   `json:"exit_code,omitempty"`
	TabOrder               int                    `json:"tab_order"`
	CreatedAt              time.Time              `json:"created_at"`
	UpdatedAt              time.Time              `json:"updated_at"`
	StartedAt              *time.Time             `json:"started_at,omitempty"`
	FinishedAt             *time.Time             `json:"finished_at,omitempty"`
	ClosedAt               *time.Time             `json:"closed_at,omitempty"`
}
type CommitPrompt struct {
	State string `json:"state"`
}

type ManualTerminal struct {
	ID         string     `json:"id"`
	HolonID    string     `json:"holon_id"`
	TerminalID string     `json:"terminal_id"`
	Title      string     `json:"title"`
	CWD        string     `json:"cwd"`
	TabOrder   int        `json:"tab_order"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ClosedAt   *time.Time `json:"closed_at,omitempty"`
}

type IDE struct {
	ID          string     `json:"id"`
	HolonID     string     `json:"holon_id"`
	Provider    string     `json:"provider"`
	State       string     `json:"state"`
	TabOrder    int        `json:"tab_order"`
	DesiredOpen bool       `json:"desired_open"`
	Reason      string     `json:"reason,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ReadyAt     *time.Time `json:"ready_at,omitempty"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
}

type Holon struct {
	ApplicationPhase string `json:"application_phase,omitempty"`
	// EndRequested distinguishes whole-Holon shutdown from aggregated agent status.
	EndRequested             bool             `json:"-"`
	LastSelectedTabID        string           `json:"last_selected_tab_id"`
	SynchronizedTargetCommit string           `json:"synchronized_target_commit,omitempty"`
	RebaseAttempt            *RebaseAttempt   `json:"rebase_attempt,omitempty"`
	ID                       string           `json:"id"`
	Title                    string           `json:"title"`
	Prompt                   string           `json:"prompt"`
	Kind                     Kind             `json:"kind"`
	Status                   Status           `json:"status"`
	BaseBranch               string           `json:"base_branch,omitempty"`
	BaseCommit               string           `json:"base_commit"`
	WorkSessionStartCommit   string           `json:"-"`
	WorktreeBranch           string           `json:"worktree_branch,omitempty"`
	WorktreePath             string           `json:"worktree_path,omitempty"`
	UpstreamBranch           string           `json:"upstream_branch,omitempty"`
	UpstreamHeadCommit       string           `json:"upstream_head_commit,omitempty"`
	ReadOnly                 bool             `json:"read_only,omitempty"`
	Reason                   string           `json:"reason,omitempty"`
	ExitCode                 *int             `json:"exit_code,omitempty"`
	IssueID                  string           `json:"issue_id,omitempty"`
	PullRequestID            string           `json:"pull_request_id,omitempty"`
	Published                bool             `json:"published,omitempty"`
	AgentSessions            []AgentSession   `json:"agent_sessions"`
	ManualTerminals          []ManualTerminal `json:"manual_terminals"`
	IDEs                     []IDE            `json:"ides"`
	CreatedAt                time.Time        `json:"created_at"`
	StartedAt                *time.Time       `json:"started_at,omitempty"`
	FinishedAt               *time.Time       `json:"finished_at,omitempty"`
	ArchivedAt               *time.Time       `json:"archived_at,omitempty"`
}

func (h Holon) AgentSession(id string) AgentSession {
	for _, agent := range h.AgentSessions {
		if agent.ID == id {
			return agent
		}
	}
	return AgentSession{}
}

func (holon Holon) MarshalJSON() ([]byte, error) {
	type wire Holon
	value := wire(holon)
	if value.AgentSessions == nil {
		value.AgentSessions = []AgentSession{}
	}
	if value.ManualTerminals == nil {
		value.ManualTerminals = []ManualTerminal{}
	}
	if value.IDEs == nil {
		value.IDEs = []IDE{}
	}
	return json.Marshal(value)
}

type Create struct {
	Permissions string `json:"-"`
	Model       string `json:"-"`
	// SelectionResolved prevents a prepared launch from rereading preferences.
	SelectionResolved bool   `json:"-"`
	StartupMode       string `json:"startup_mode,omitempty"`
	Title             string `json:"title"`
	Prompt            string `json:"prompt"`
	Kind              Kind   `json:"kind"`
	BaseBranch        string `json:"base_branch"`
	BaseCommit        string `json:"base_commit"`
	IssueID           string `json:"issue_id,omitempty"`
	PullRequestID     string `json:"pull_request_id,omitempty"`
	AgentType         string `json:"agent_type,omitempty"`
	// AgentTitle is internal creation metadata for workflows that give their
	// initial agent a specific role. Public creates keep the default "Agent".
	AgentTitle string `json:"-"`
	// Upstream fields are internal creation metadata. They deliberately cannot
	// be supplied through the public Holon-create JSON request.
	UpstreamBranch     string `json:"-"`
	UpstreamHeadCommit string `json:"-"`
	// WorkSessionStartCommit is the commit checked out for the new workspace.
	// It can differ from BaseCommit when work continues an existing branch.
	WorkSessionStartCommit string `json:"-"`
	// RebaseTargetCommit requires local preparation verification before creation.
	RebaseTargetCommit string `json:"-"`
}

type WorkspaceInspection struct {
	Branch                 string            `json:"branch"`
	BaseBranch             string            `json:"base_branch"`
	BaseCommit             string            `json:"base_commit"`
	HeadCommit             string            `json:"head_commit"`
	HasChanges             bool              `json:"has_changes"`
	Dirty                  bool              `json:"dirty"`
	Files                  []WorkspaceChange `json:"files"`
	DiffTruncated          bool              `json:"diff_truncated"`
	SummaryOnly            bool              `json:"summary_only,omitempty"`
	SelectedBaseRef        string            `json:"selected_base_ref,omitempty"`
	SelectedTargetRef      string            `json:"selected_target_ref,omitempty"`
	SelectedBaseCommit     string            `json:"selected_base_commit,omitempty"`
	SelectedTargetCommit   string            `json:"selected_target_commit,omitempty"`
	RefOptions             []WorkspaceRef    `json:"ref_options,omitempty"`
	BranchBaseCommit       string            `json:"branch_base_commit,omitempty"`
	WorkSessionStartCommit string            `json:"work_session_start_commit,omitempty"`
	WorkspaceHeadCommit    string            `json:"workspace_head_commit,omitempty"`
	Commits                []WorkspaceCommit `json:"commits,omitempty"`

	Clean   bool              `json:"-"`
	Changes []WorkspaceChange `json:"-"`
}

type WorkspaceContents struct {
	Original string `json:"original"`
	Modified string `json:"modified"`
}

type WorkspaceChange struct {
	ContentStatus string             `json:"content_status,omitempty"`
	Contents      *WorkspaceContents `json:"contents,omitempty"`
	Path          string             `json:"path"`
	OldPath       string             `json:"old_path,omitempty"`
	Status        string             `json:"status"`
	Additions     int                `json:"additions"`
	Deletions     int                `json:"deletions"`
	Binary        bool               `json:"binary"`
	Diff          string             `json:"diff,omitempty"`
	DiffTruncated bool               `json:"diff_truncated"`

	Patch     string `json:"-"`
	Truncated bool   `json:"-"`
}

func (inspection *WorkspaceInspection) UnmarshalJSON(data []byte) error {
	type wire WorkspaceInspection
	var value wire
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*inspection = WorkspaceInspection(value)
	inspection.Changes = append([]WorkspaceChange(nil), inspection.Files...)
	inspection.Clean = !inspection.Dirty
	for index := range inspection.Changes {
		inspection.Changes[index].Patch = inspection.Changes[index].Diff
		inspection.Changes[index].Truncated = inspection.Changes[index].DiffTruncated
	}
	return nil
}

type WorkspaceRef struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	Commit string `json:"commit,omitempty"`
}

type WorkspaceCommit struct {
	SHA          string `json:"sha"`
	ParentCommit string `json:"parent_commit"`
	Subject      string `json:"subject"`
	Author       string `json:"author"`
	AuthoredAt   string `json:"authored_at"`
	Body         string `json:"body"`
}

type InspectOptions struct {
	Contents               bool
	BaseBranch             string
	BranchBaseCommit       string
	WorkSessionStartCommit string
	BaseRef                string
	TargetRef              string
	SummaryOnly            bool
	Path                   string
	IgnoredPaths           []string
}

type Publish struct {
	PreservePushErrors    bool     `json:"-"`
	TargetCommit          string   `json:"-"`
	ExpectedWorkspaceHead string   `json:"-"`
	RequireSynchronized   bool     `json:"-"`
	Remote                string   `json:"remote"`
	UpstreamBranch        string   `json:"upstream_branch"`
	ExpectedRemoteHead    string   `json:"expected_remote_head"`
	IgnoredPaths          []string `json:"-"`
}

type TabRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func IsTerminal(s Status) bool {
	switch s {
	case StatusCancelled, StatusCompleted, StatusFailed, StatusLost, StatusExpired, StatusRecoveryFailed:
		return true
	}
	return false
}
