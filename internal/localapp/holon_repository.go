package localapp

import (
	"context"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/repository"
)

// holonRepositoryCoordinator is composition-only glue between two domain ports.
type holonRepositoryCoordinator struct{ repositories *repository.Service }

func (c holonRepositoryCoordinator) ResolveWorkspaceBase(ctx context.Context, id, branch string) (string, error) {
	// Validate explicitly: ordinary inspections may fall back to the saved base
	// when a provider branch has disappeared.
	if _, err := c.repositories.ResolveProviderBranch(ctx, branch); err != nil {
		return "", err
	}
	i, err := c.repositories.InspectWorkspaceWithOptions(ctx, id, repository.InspectOptions{BaseBranch: branch, BaseRef: "main", TargetRef: "worktree", SummaryOnly: true})
	return i.BranchBaseCommit, err
}

func (c holonRepositoryCoordinator) CreateWorkspace(ctx context.Context, id, commit string) (holons.Workspace, error) {
	w, err := c.repositories.CreateWorkspace(ctx, id, commit)
	return holons.Workspace{Path: w.Path, Branch: w.Branch, BaseCommit: w.BaseCommit}, err
}
func (c holonRepositoryCoordinator) ArchiveWorkspace(ctx context.Context, id, expectedBranch string) error {
	return c.repositories.ArchiveWorkspace(ctx, id, expectedBranch)
}
func (c holonRepositoryCoordinator) ReopenWorkspace(ctx context.Context, id, expectedBranch string) error {
	return c.repositories.ReopenWorkspace(ctx, id, expectedBranch)
}
func (c holonRepositoryCoordinator) InspectWorkspace(ctx context.Context, id string) (holons.WorkspaceInspection, error) {
	return c.InspectWorkspaceWithOptions(ctx, id, holons.InspectOptions{})
}
func (c holonRepositoryCoordinator) InspectWorkspaceWithOptions(ctx context.Context, id string, options holons.InspectOptions) (holons.WorkspaceInspection, error) {
	i, err := c.repositories.InspectWorkspaceWithOptions(ctx, id, repository.InspectOptions{BaseBranch: options.BaseBranch, BranchBaseCommit: options.BranchBaseCommit, WorkSessionStartCommit: options.WorkSessionStartCommit, BaseRef: options.BaseRef, TargetRef: options.TargetRef, Contents: options.Contents, SummaryOnly: options.SummaryOnly, Path: options.Path, IgnoredPaths: options.IgnoredPaths})
	if err != nil {
		return holons.WorkspaceInspection{}, err
	}
	files := make([]holons.WorkspaceChange, 0, len(i.Files))
	for _, file := range i.Files {
		var contents *holons.WorkspaceContents
		if file.Contents != nil {
			contents = &holons.WorkspaceContents{Original: file.Contents.Original, Modified: file.Contents.Modified}
		}
		files = append(files, holons.WorkspaceChange{Contents: contents, ContentStatus: file.ContentStatus, Path: file.Path, OldPath: file.OldPath, Status: file.Status, Additions: file.Additions, Deletions: file.Deletions, Binary: file.Binary, Diff: file.Diff, DiffTruncated: file.DiffTruncated, Patch: file.Diff, Truncated: file.DiffTruncated})
	}
	refs := make([]holons.WorkspaceRef, 0, len(i.RefOptions))
	for _, ref := range i.RefOptions {
		refs = append(refs, holons.WorkspaceRef{ID: ref.ID, Label: ref.Label, Kind: ref.Kind, Commit: ref.Commit})
	}
	commits := make([]holons.WorkspaceCommit, 0, len(i.Commits))
	for _, commit := range i.Commits {
		commits = append(commits, holons.WorkspaceCommit{SHA: commit.SHA, ParentCommit: commit.ParentCommit, Subject: commit.Subject, Author: commit.Author, AuthoredAt: commit.AuthoredAt, Body: commit.Body})
	}
	return holons.WorkspaceInspection{
		Branch: i.Branch, BaseBranch: i.BaseBranch, BaseCommit: i.BaseCommit, HeadCommit: i.HeadCommit, HasChanges: i.HasChanges, Dirty: i.Dirty, Files: files, DiffTruncated: i.DiffTruncated,
		SummaryOnly: i.SummaryOnly, SelectedBaseRef: i.SelectedBaseRef, SelectedTargetRef: i.SelectedTargetRef, SelectedBaseCommit: i.SelectedBaseCommit, SelectedTargetCommit: i.SelectedTargetCommit,
		RefOptions: refs, BranchBaseCommit: i.BranchBaseCommit, WorkSessionStartCommit: i.WorkSessionStartCommit, WorkspaceHeadCommit: i.WorkspaceHeadCommit, Commits: commits, Clean: i.Clean, Changes: files,
	}, nil
}
func (c holonRepositoryCoordinator) PublishWorkspace(ctx context.Context, id string, in holons.Publish) (holons.PublishedWorkspace, error) {
	p, err := c.repositories.PublishWorkspace(ctx, repository.PublishRequest{PreservePushErrors: in.PreservePushErrors, WorkspaceID: id, Remote: in.Remote, UpstreamBranch: in.UpstreamBranch, ExpectedRemoteHead: in.ExpectedRemoteHead, ExpectedWorkspaceHead: in.ExpectedWorkspaceHead, IgnoredPaths: in.IgnoredPaths})
	return holons.PublishedWorkspace{UpstreamBranch: p.UpstreamBranch, HeadCommit: p.HeadCommit}, err
}
func (c holonRepositoryCoordinator) RemoveWorkspace(ctx context.Context, id string) error {
	return c.repositories.RemoveWorkspace(ctx, id)
}
func (c holonRepositoryCoordinator) RenameWorkspace(ctx context.Context, id, expected, slug string) (string, error) {
	return c.repositories.RenameWorkspace(ctx, id, expected, slug)
}

func (c holonRepositoryCoordinator) InspectSynchronization(ctx context.Context, id, target string, ignored []string) (holons.SynchronizationInspection, error) {
	i, err := c.repositories.InspectSynchronization(ctx, id, target, ignored)
	return holons.SynchronizationInspection{Branch: i.Branch, HeadCommit: i.HeadCommit, Dirty: i.Dirty, Rebasing: i.Rebasing, Incorporated: i.Incorporated, Conflicted: i.Conflicted, RebaseBranch: i.RebaseBranch, RebaseTarget: i.RebaseTarget}, err
}
