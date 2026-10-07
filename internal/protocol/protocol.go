// Package protocol contains the small set of product-neutral contracts shared
// by the local domain adapters. It is not a transport protocol.
package protocol

import (
	"errors"
	"time"
)

const (
	MaxMessageBytes    = 16 * 1024 * 1024
	MaxPromptBytes     = 64 * 1024
	MaxIdentifierBytes = 128
	MinTerminalColumns = 20
	MaxTerminalColumns = 500
	MinTerminalRows    = 5
	MaxTerminalRows    = 300
)

type HarnessType string

const (
	HarnessCodex      HarnessType = "codex"
	HarnessClaudeCode HarnessType = "claude-code"
	HarnessOpenCode   HarnessType = "opencode"
)

type HarnessCapability struct {
	Type                   HarnessType `json:"type"`
	Available              bool        `json:"available"`
	AutomatedWorkflows     bool        `json:"automated_workflows"`
	Version                string      `json:"version,omitempty"`
	UnavailableReason      string      `json:"unavailable_reason,omitempty"`
	SupportStatus          string      `json:"support_status,omitempty"`
	SupportedRanges        []string    `json:"supported_ranges,omitempty"`
	LatestSupportedVersion string      `json:"latest_supported_version,omitempty"`
	Warning                string      `json:"warning,omitempty"`
}
type CommitPromptState string

const (
	CommitPromptClean              CommitPromptState = "clean"
	CommitPromptDirtyPromptVisible CommitPromptState = "dirty_prompt_visible"
	CommitPromptDiscussionStarted  CommitPromptState = "commit_discussion_started"
	CommitPromptSkippedForChange   CommitPromptState = "skipped_for_change"
	CommitPromptContinuedForNow    CommitPromptState = "continued_for_now"
)

type CommitPrompt struct {
	State CommitPromptState `json:"state"`
}
type ObservabilityStatus string

const (
	ObservabilityStarting ObservabilityStatus = "starting"
	ObservabilityHealthy  ObservabilityStatus = "healthy"
	ObservabilityDegraded ObservabilityStatus = "degraded"
)

// AgentActivity describes observed work independently of the PTY lifecycle.
type AgentActivity string

const (
	ActivityStarting   AgentActivity = "starting"
	ActivityIdle       AgentActivity = "idle"
	ActivityWorking    AgentActivity = "working"
	ActivityNeedsInput AgentActivity = "needs_input"
	ActivityCompleted  AgentActivity = "completed"
	ActivityUnknown    AgentActivity = "unknown"
	ActivityFailed     AgentActivity = "failed"
)

type InputState string

const (
	InputNone               InputState = "none"
	InputPermissionRequired InputState = "permission_required"
	InputUserRequired       InputState = "user_input_required"
	InputTaskComplete       InputState = "task_complete"
)

type HarnessSession struct {
	Permissions            string              `json:"permissions,omitempty"`
	Model                  string              `json:"model,omitempty"`
	ID                     string              `json:"id"`
	TerminalID             string              `json:"terminal_id"`
	SessionID              string              `json:"session_id,omitempty"`
	HarnessType            HarnessType         `json:"harness_type"`
	Title                  string              `json:"title,omitempty"`
	Prompt                 string              `json:"prompt,omitempty"`
	Status                 string              `json:"status,omitempty"`
	Reason                 string              `json:"reason,omitempty"`
	ExitCode               *int                `json:"exit_code,omitempty"`
	ResumeTarget           string              `json:"resume_target,omitempty"`
	RolloutPath            string              `json:"rollout_path,omitempty"`
	ObservabilityStatus    ObservabilityStatus `json:"observability_status,omitempty"`
	ObservabilityMessage   string              `json:"observability_message,omitempty"`
	TabOrder               int                 `json:"tab_order,omitempty"`
	Activity               AgentActivity       `json:"activity"`
	InputState             InputState          `json:"input_state"`
	CommitPrompt           *CommitPrompt       `json:"commit_prompt,omitempty"`
	CommitPromptChangeHash string              `json:"-"`
	CreatedAt              time.Time           `json:"created_at"`
	UpdatedAt              time.Time           `json:"updated_at"`
	StartedAt              *time.Time          `json:"started_at,omitempty"`
	FinishedAt             *time.Time          `json:"finished_at,omitempty"`
	ClosedAt               *time.Time          `json:"closed_at,omitempty"`
}
type Project struct {
	ID            string `json:"id"`
	RepositoryURL string `json:"repository_url"`
	DefaultBranch string `json:"default_branch"`
}
type WorkspaceMode string

const WorkspaceModeCommit WorkspaceMode = "commit"

type WorkspaceSpec struct {
	Mode           WorkspaceMode `json:"mode"`
	BaseBranch     string        `json:"base_branch"`
	BaseCommit     string        `json:"base_commit"`
	HeadBranch     string        `json:"head_branch"`
	HeadCommit     string        `json:"head_commit"`
	BranchPrefix   string        `json:"branch_prefix,omitempty"`
	UpstreamBranch string        `json:"upstream_branch,omitempty"`
}
type TerminalAttentionAction string

const (
	TerminalAttentionSubmit    TerminalAttentionAction = "submit"
	TerminalAttentionCancel    TerminalAttentionAction = "cancel"
	TerminalAttentionInterrupt TerminalAttentionAction = "interrupt"
)

type WorkspaceContents struct {
	Original string `json:"original"`
	Modified string `json:"modified"`
}

type WorkspaceFileDiff struct {
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
}
type WorkspaceDiffRef struct {
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
type WorkspaceInspected struct {
	RequestID              string              `json:"request_id"`
	Branch                 string              `json:"branch"`
	BaseBranch             string              `json:"base_branch"`
	BaseCommit             string              `json:"base_commit"`
	HeadCommit             string              `json:"head_commit"`
	HasChanges             bool                `json:"has_changes"`
	Dirty                  bool                `json:"dirty"`
	Files                  []WorkspaceFileDiff `json:"files"`
	DiffTruncated          bool                `json:"diff_truncated"`
	SummaryOnly            bool                `json:"summary_only,omitempty"`
	SelectedBaseRef        string              `json:"selected_base_ref,omitempty"`
	SelectedTargetRef      string              `json:"selected_target_ref,omitempty"`
	SelectedBaseCommit     string              `json:"selected_base_commit,omitempty"`
	SelectedTargetCommit   string              `json:"selected_target_commit,omitempty"`
	RefOptions             []WorkspaceDiffRef  `json:"ref_options,omitempty"`
	BranchBaseCommit       string              `json:"branch_base_commit,omitempty"`
	WorkSessionStartCommit string              `json:"work_session_start_commit,omitempty"`
	WorkspaceHeadCommit    string              `json:"workspace_head_commit,omitempty"`
	Commits                []WorkspaceCommit   `json:"commits,omitempty"`
}

func ValidateTerminalSize(columns, rows int) error {
	if columns < MinTerminalColumns || columns > MaxTerminalColumns || rows < MinTerminalRows || rows > MaxTerminalRows {
		return errors.New("terminal size is out of range")
	}
	return nil
}
func ValidIdentifier(value string) bool {
	if value == "" || len(value) > MaxIdentifierBytes {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
