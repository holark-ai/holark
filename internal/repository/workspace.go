package repository

import (
	"context"
	"errors"
)

var (
	ErrInvalidWorkspace     = errors.New("invalid workspace")
	ErrWorkspaceExists      = errors.New("workspace already exists")
	ErrWorkspaceDirty       = errors.New("workspace has uncommitted changes")
	ErrIgnoredPathCommitted = errors.New("ignored workspace path is committed")
	ErrWorkspaceOwner       = errors.New("workspace is not owned by holark")
	ErrWorkspaceBranch      = errors.New("workspace branch changed")
	ErrStaleHead            = errors.New("remote head changed")
	ErrBackupExists         = errors.New("backup already exists")
)

type Workspace struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Branch     string `json:"branch"`
	BaseCommit string `json:"base_commit"`
	HeadCommit string `json:"head_commit"`
}

type WorkspaceInspection struct {
	Branch                 string            `json:"branch"`
	BaseBranch             string            `json:"base_branch"`
	BaseCommit             string            `json:"base_commit"`
	CommonAncestorCommit   string            `json:"common_ancestor_commit,omitempty"`
	CommonAncestor         *Commit           `json:"common_ancestor,omitempty"`
	HeadCommit             string            `json:"head_commit"`
	HasChanges             bool              `json:"has_changes"`
	Dirty                  bool              `json:"dirty"`
	Files                  []WorkspaceFile   `json:"files"`
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

	// The compact internal view remains available to existing domain callers.
	Workspace Workspace `json:"-"`
	Clean     bool      `json:"-"`
	Changes   []Change  `json:"-"`
}

type WorkspaceContents struct {
	Original string `json:"original"`
	Modified string `json:"modified"`
}

type WorkspaceFile struct {
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
	Contents                                             bool
	BaseBranch, BranchBaseCommit, WorkSessionStartCommit string
	BaseRef, TargetRef                                   string
	SummaryOnly                                          bool
	Path                                                 string
	IgnoredPaths                                         []string
}

type PublishRequest struct {
	// PreservePushErrors distinguishes target races from other push failures for automatic retries.
	PreservePushErrors    bool
	ExpectedWorkspaceHead string
	WorkspaceID           string
	Remote                string
	UpstreamBranch        string
	ExpectedRemoteHead    string
	IgnoredPaths          []string
}

type PublishedWorkspace struct {
	Branch         string `json:"branch"`
	UpstreamBranch string `json:"upstream_branch"`
	HeadCommit     string `json:"head_commit"`
}

// WorkspaceStore is owned by the repository domain; adapters implement Git and filesystem details.
type WorkspaceStore interface {
	CreateWorkspace(context.Context, string, string) (Workspace, error)
	InspectWorkspace(context.Context, string) (WorkspaceInspection, error)
	PublishWorkspace(context.Context, PublishRequest) (PublishedWorkspace, error)
	BackupWorkspace(context.Context, string, string, string) (string, error)
	RemoveWorkspace(context.Context, string) error
	RenameWorkspace(context.Context, string, string, string) (string, error)
}

type WorkspaceArchiver interface {
	ArchiveWorkspace(context.Context, string, string) error
	ReopenWorkspace(context.Context, string, string) error
}

type WorkspaceInspector interface {
	InspectWorkspaceWithOptions(context.Context, string, InspectOptions) (WorkspaceInspection, error)
	InspectRange(context.Context, string, string, string, string) (WorkspaceInspection, error)
}

func (s *Service) RenameWorkspace(ctx context.Context, id, expected, slug string) (string, error) {
	store, ok := s.store.(WorkspaceStore)
	if !ok {
		return "", ErrInvalidWorkspace
	}
	return store.RenameWorkspace(ctx, id, expected, slug)
}

func (s *Service) CreateWorkspace(ctx context.Context, id, selectedCommit string) (Workspace, error) {
	store, ok := s.store.(WorkspaceStore)
	if !ok {
		return Workspace{}, ErrInvalidWorkspace
	}
	return store.CreateWorkspace(ctx, id, selectedCommit)
}

func (s *Service) ArchiveWorkspace(ctx context.Context, id, expectedBranch string) error {
	store, ok := s.store.(WorkspaceArchiver)
	if !ok {
		return ErrInvalidWorkspace
	}
	return store.ArchiveWorkspace(ctx, id, expectedBranch)
}

func (s *Service) ReopenWorkspace(ctx context.Context, id, expectedBranch string) error {
	store, ok := s.store.(WorkspaceArchiver)
	if !ok {
		return ErrInvalidWorkspace
	}
	return store.ReopenWorkspace(ctx, id, expectedBranch)
}

func (s *Service) InspectWorkspace(ctx context.Context, id string) (WorkspaceInspection, error) {
	store, ok := s.store.(WorkspaceStore)
	if !ok {
		return WorkspaceInspection{}, ErrInvalidWorkspace
	}
	return store.InspectWorkspace(ctx, id)
}

func (s *Service) InspectWorkspaceWithOptions(ctx context.Context, id string, options InspectOptions) (WorkspaceInspection, error) {
	store, ok := s.store.(WorkspaceInspector)
	if !ok {
		if options.Contents || options.BaseRef != "" || options.TargetRef != "" || options.SummaryOnly || options.Path != "" || len(options.IgnoredPaths) != 0 {
			return WorkspaceInspection{}, ErrInvalidWorkspace
		}
		return s.InspectWorkspace(ctx, id)
	}
	return store.InspectWorkspaceWithOptions(ctx, id, options)
}

func (s *Service) InspectRange(ctx context.Context, branch, baseBranch, baseCommit, headCommit string) (WorkspaceInspection, error) {
	store, ok := s.store.(WorkspaceInspector)
	if !ok {
		return WorkspaceInspection{}, ErrRepositoryUnavailable
	}
	return store.InspectRange(ctx, branch, baseBranch, baseCommit, headCommit)
}

func (s *Service) InspectRangeWithOptions(ctx context.Context, branch, baseBranch, baseCommit, headCommit string, options InspectOptions) (WorkspaceInspection, error) {
	store, ok := s.store.(interface {
		InspectRangeWithOptions(context.Context, string, string, string, string, InspectOptions) (WorkspaceInspection, error)
	})
	if !ok {
		return WorkspaceInspection{}, ErrRepositoryUnavailable
	}
	return store.InspectRangeWithOptions(ctx, branch, baseBranch, baseCommit, headCommit, options)
}

func (s *Service) PublishWorkspace(ctx context.Context, request PublishRequest) (PublishedWorkspace, error) {
	store, ok := s.store.(WorkspaceStore)
	if !ok {
		return PublishedWorkspace{}, ErrInvalidWorkspace
	}
	return store.PublishWorkspace(ctx, request)
}

func (s *Service) BackupWorkspace(ctx context.Context, id, remote, branch string) (string, error) {
	store, ok := s.store.(WorkspaceStore)
	if !ok {
		return "", ErrInvalidWorkspace
	}
	return store.BackupWorkspace(ctx, id, remote, branch)
}

func (s *Service) RemoveWorkspace(ctx context.Context, id string) error {
	store, ok := s.store.(WorkspaceStore)
	if !ok {
		return ErrInvalidWorkspace
	}
	return store.RemoveWorkspace(ctx, id)
}
