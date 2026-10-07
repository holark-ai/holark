package gitadapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/holark-ai/holark/internal/gittransport"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/repository"
	workspacepkg "github.com/holark-ai/holark/internal/workspace"
)

func (g *Git) RenameWorkspace(ctx context.Context, id, expected, slug string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if !workspaceID.MatchString(id) || !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,46}[a-z0-9]$`).MatchString(slug) {
		return "", repository.ErrInvalidWorkspace
	}
	if err := g.mu.lock(ctx, "RenameWorkspace"); err != nil {
		return "", err
	}
	defer g.mu.Unlock()
	state, err := g.workspace(ctx, id)
	if err != nil {
		return "", err
	}
	if state.Workspace.Branch != expected {
		return "", repository.ErrWorkspaceBranch
	}
	prefix := g.generatedBranchPrefix(ctx)
	for n := 1; n <= 9999; n++ {
		candidate := prefix + "/" + slug
		if n > 1 {
			candidate = fmt.Sprintf("%s/%s-%d", prefix, slug, n)
		}
		refs, refErr := g.run(ctx, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes", "refs/holark/browse")
		if refErr != nil {
			return "", refErr
		}
		reserved := false
		for _, ref := range strings.Split(refs, "\n") {
			if ref == "refs/heads/"+candidate || strings.HasSuffix(ref, "/"+candidate) {
				reserved = true
				break
			}
		}
		if reserved {
			continue
		}
		if _, err = g.run(ctx, "-C", state.Workspace.Path, "branch", "-m", candidate); err != nil {
			return "", err
		}
		return candidate, nil
	}
	return "", repository.ErrWorkspaceExists
}

func (g *Git) generatedBranchPrefix(ctx context.Context) string {
	name, err := g.run(ctx, "config", "--get", "user.name")
	if err != nil {
		return "holark"
	}
	prefix := normalizeBranchPrefix(name)
	if prefix == "" {
		return "holark"
	}
	return prefix
}

func normalizeBranchPrefix(name string) string {
	const maxLength = 48
	var prefix strings.Builder
	wantSeparator := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			wantSeparator = prefix.Len() > 0
			continue
		}
		if wantSeparator && prefix.Len() < maxLength {
			prefix.WriteByte('-')
		}
		wantSeparator = false
		if prefix.Len() == maxLength {
			break
		}
		prefix.WriteRune(r)
	}
	return strings.TrimRight(prefix.String(), "-")
}

var workspaceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type workspaceState struct {
	repository.Workspace
	root string
}

func (g *Git) workspaceRoot() string {
	if g.worktrees != "" {
		return g.worktrees
	}
	return filepath.Join(filepath.Dir(g.descriptor.CommonDir), "holark-worktrees")
}

func (g *Git) CreateWorkspace(ctx context.Context, id, selectedCommit string) (repository.Workspace, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if !workspaceID.MatchString(id) || strings.TrimSpace(selectedCommit) == "" {
		return repository.Workspace{}, repository.ErrInvalidWorkspace
	}
	if err := g.mu.lock(ctx, "CreateWorkspace"); err != nil {
		return repository.Workspace{}, err
	}
	defer g.mu.Unlock()
	commit, err := g.run(ctx, "rev-parse", "--verify", selectedCommit+"^{commit}")
	if err != nil {
		return repository.Workspace{}, repository.ErrRefNotFound
	}
	root := g.workspaceRoot()
	path := filepath.Join(root, id, "repo")
	if !contained(root, path) {
		return repository.Workspace{}, repository.ErrInvalidWorkspace
	}
	if _, err := os.Lstat(filepath.Dir(path)); err == nil {
		return repository.Workspace{}, repository.ErrWorkspaceExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return repository.Workspace{}, repository.ErrInvalidWorkspace
	}
	branch := "holark/" + id
	if _, err := g.run(ctx, "check-ref-format", "refs/heads/"+branch); err != nil {
		return repository.Workspace{}, repository.ErrInvalidWorkspace
	}
	if _, err := g.run(ctx, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		return repository.Workspace{}, repository.ErrWorkspaceExists
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return repository.Workspace{}, err
	}
	if _, err := g.run(ctx, "branch", branch, commit); err != nil {
		_ = os.RemoveAll(filepath.Dir(path))
		return repository.Workspace{}, fmt.Errorf("create workspace branch: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_, _ = g.run(context.Background(), "branch", "-D", branch)
			_ = os.RemoveAll(filepath.Dir(path))
		}
	}()
	if _, err := g.run(ctx, "worktree", "add", "--", path, branch); err != nil {
		return repository.Workspace{}, fmt.Errorf("create worktree: %w", err)
	}
	if err := excludeHolarkControlFiles(ctx, path); err != nil {
		_, _ = g.run(context.Background(), "worktree", "remove", "--force", path)
		return repository.Workspace{}, err
	}
	marker := filepath.Join(filepath.Dir(path), "owner")
	if err := os.WriteFile(marker, []byte(g.descriptor.ID+"\n"+commit+"\n"), 0o600); err != nil {
		_, _ = g.run(context.Background(), "worktree", "remove", "--force", path)
		return repository.Workspace{}, err
	}
	ok = true
	return repository.Workspace{ID: id, Path: path, Branch: branch, BaseCommit: commit, HeadCommit: commit}, nil
}

func archiveRef(id, leaf string) string {
	return "refs/holark/holons/" + id + "/" + leaf
}

func managedWorkspaceBranch(branch string) bool {
	prefix, leaf, found := strings.Cut(branch, "/")
	return found && prefix != "" && leaf != "" && !strings.Contains(leaf, "/")
}

func (g *Git) optionalRef(ctx context.Context, ref string) (string, error) {
	value, err := g.run(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err == nil {
		return value, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", nil
	}
	return "", err
}

// ArchiveWorkspace creates reachability roots for both committed and dirty
// work before removing the disposable checkout and its ordinary local branch.
func (g *Git) ArchiveWorkspace(ctx context.Context, id, expectedBranch string) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if !workspaceID.MatchString(id) || !managedWorkspaceBranch(expectedBranch) {
		return repository.ErrInvalidWorkspace
	}
	if _, err := g.run(ctx, "check-ref-format", "refs/heads/"+expectedBranch); err != nil {
		return repository.ErrInvalidWorkspace
	}
	if err := g.mu.lock(ctx, "ArchiveWorkspace"); err != nil {
		return err
	}
	defer g.mu.Unlock()

	path := filepath.Join(g.workspaceRoot(), id, "repo")
	_, pathErr := os.Lstat(path)
	if errors.Is(pathErr, os.ErrNotExist) {
		head, err := g.optionalRef(ctx, archiveRef(id, "head"))
		if err != nil {
			return err
		}
		if head == "" {
			return repository.ErrRefNotFound
		}
		parent := filepath.Dir(path)
		if _, parentErr := os.Lstat(parent); parentErr == nil {
			marker, markerErr := os.ReadFile(filepath.Join(parent, "owner"))
			if markerErr != nil || !strings.HasPrefix(string(marker), g.descriptor.ID+"\n") {
				return repository.ErrWorkspaceOwner
			}
		} else if !errors.Is(parentErr, os.ErrNotExist) {
			return parentErr
		}
		return g.finishWorkspaceArchive(ctx, path, expectedBranch, head)
	}
	if pathErr != nil {
		return pathErr
	}

	state, err := g.workspace(ctx, id)
	if err != nil {
		return err
	}
	if state.Branch != expectedBranch {
		return repository.ErrWorkspaceBranch
	}
	head := state.HeadCommit
	var snapshot string
	status, err := runGitAt(ctx, state.Path, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if status != "" {
		if err := rejectDirtySubmodules(ctx, state.Path); err != nil {
			return err
		}
		snapshot, err = createWorkspaceSnapshot(ctx, state.Path, state.HeadCommit)
		if err != nil {
			return err
		}
	}
	oldHead, err := g.optionalRef(ctx, archiveRef(id, "head"))
	if err != nil {
		return err
	}
	oldSnapshot, err := g.optionalRef(ctx, archiveRef(id, "snapshot"))
	if err != nil {
		return err
	}
	var updates strings.Builder
	if oldHead == "" {
		fmt.Fprintf(&updates, "create %s %s\n", archiveRef(id, "head"), head)
	} else {
		fmt.Fprintf(&updates, "update %s %s %s\n", archiveRef(id, "head"), head, oldHead)
	}
	if snapshot != "" {
		if oldSnapshot == "" {
			fmt.Fprintf(&updates, "create %s %s\n", archiveRef(id, "snapshot"), snapshot)
		} else {
			fmt.Fprintf(&updates, "update %s %s %s\n", archiveRef(id, "snapshot"), snapshot, oldSnapshot)
		}
	} else if oldSnapshot != "" {
		fmt.Fprintf(&updates, "delete %s %s\n", archiveRef(id, "snapshot"), oldSnapshot)
	}
	if _, err := g.runInput(ctx, g.descriptor.Root, nil, updates.String(), "update-ref", "--stdin"); err != nil {
		return fmt.Errorf("save workspace archive refs: %w", err)
	}
	if _, err := g.run(ctx, "worktree", "remove", "--force", "--", state.Path); err != nil {
		return err
	}
	return g.finishWorkspaceArchive(ctx, state.Path, expectedBranch, head)
}

func (g *Git) finishWorkspaceArchive(ctx context.Context, path, expectedBranch, head string) error {
	branchHead, err := g.optionalRef(ctx, "refs/heads/"+expectedBranch)
	if err != nil {
		return err
	}
	if branchHead != "" {
		if branchHead != head {
			return repository.ErrWorkspaceBranch
		}
		if _, err := g.run(ctx, "branch", "-D", "--", expectedBranch); err != nil {
			return err
		}
	}
	return os.RemoveAll(filepath.Dir(path))
}

// ReopenWorkspace reconstructs a managed checkout from its archive refs. The
// snapshot, when present, is applied to the checkout without updating its
// index, so all restored changes are ordinary unstaged worktree changes.
func (g *Git) ReopenWorkspace(ctx context.Context, id, expectedBranch string) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if !workspaceID.MatchString(id) || !managedWorkspaceBranch(expectedBranch) {
		return repository.ErrInvalidWorkspace
	}
	if _, err := g.run(ctx, "check-ref-format", "refs/heads/"+expectedBranch); err != nil {
		return repository.ErrInvalidWorkspace
	}
	if err := g.mu.lock(ctx, "ReopenWorkspace"); err != nil {
		return err
	}
	defer g.mu.Unlock()

	head, err := g.optionalRef(ctx, archiveRef(id, "head"))
	if err != nil {
		return err
	}
	if head == "" {
		return repository.ErrRefNotFound
	}
	snapshot, err := g.optionalRef(ctx, archiveRef(id, "snapshot"))
	if err != nil {
		return err
	}
	path := filepath.Join(g.workspaceRoot(), id, "repo")
	if !contained(g.workspaceRoot(), path) {
		return repository.ErrInvalidWorkspace
	}
	if _, statErr := os.Lstat(filepath.Dir(path)); statErr == nil {
		state, stateErr := g.workspace(ctx, id)
		if stateErr != nil {
			return stateErr
		}
		if state.Branch != expectedBranch || state.HeadCommit != head {
			return repository.ErrWorkspaceBranch
		}
		return nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}

	branchHead, err := g.optionalRef(ctx, "refs/heads/"+expectedBranch)
	if err != nil {
		return err
	}
	createdBranch := false
	if branchHead == "" {
		if _, err = g.run(ctx, "branch", expectedBranch, head); err != nil {
			return fmt.Errorf("recreate workspace branch: %w", err)
		}
		createdBranch = true
	} else if branchHead != head {
		return repository.ErrWorkspaceBranch
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		if createdBranch {
			_, _ = g.run(context.Background(), "branch", "-D", "--", expectedBranch)
		}
		return err
	}
	ok := false
	defer func() {
		if ok {
			return
		}
		_, _ = g.run(context.Background(), "worktree", "remove", "--force", "--", path)
		_ = os.RemoveAll(filepath.Dir(path))
		if createdBranch {
			_, _ = g.run(context.Background(), "branch", "-D", "--", expectedBranch)
		}
	}()
	if _, err = g.run(ctx, "worktree", "add", "--", path, expectedBranch); err != nil {
		return fmt.Errorf("recreate worktree: %w", err)
	}
	if snapshot != "" {
		patch, diffErr := g.run(ctx, "diff", "--binary", "--full-index", head, snapshot, "--")
		if diffErr != nil {
			return fmt.Errorf("read archived workspace snapshot: %w", diffErr)
		}
		if patch != "" {
			if _, err = runGitInput(ctx, path, nil, patch+"\n", "apply", "--whitespace=nowarn", "-"); err != nil {
				return fmt.Errorf("restore archived workspace snapshot: %w", err)
			}
		}
	}
	if err = excludeHolarkControlFiles(ctx, path); err != nil {
		return err
	}
	marker := filepath.Join(filepath.Dir(path), "owner")
	if err = os.WriteFile(marker, []byte(g.descriptor.ID+"\n"+head+"\n"), 0o600); err != nil {
		return err
	}
	ok = true
	return nil
}

func rejectDirtySubmodules(ctx context.Context, path string) error {
	command, finishGit := gitCommand(ctx, "ls-files", "--stage", "-z")

	defer finishGit()
	command.Dir = path
	var stderr bytes.Buffer
	command.Stderr = &stderr
	gitlinks, err := command.Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	for _, record := range bytes.Split(gitlinks, []byte{0}) {
		metadata, submodulePath, found := bytes.Cut(record, []byte{'\t'})
		fields := strings.Fields(string(metadata))
		if !found || len(fields) < 3 || fields[0] != "160000" {
			continue
		}
		name := string(submodulePath)
		submodule := filepath.Join(path, name)
		if info, statErr := os.Stat(submodule); statErr == nil && info.IsDir() {
			status, statusErr := runGitAt(ctx, submodule, "status", "--porcelain=v1", "--untracked-files=all")
			if statusErr != nil || status != "" {
				return fmt.Errorf("%w: dirty submodule %s cannot be archived", repository.ErrWorkspaceDirty, name)
			}
		}
	}
	return rejectUntrackedRepositories(ctx, path)
}

func rejectUntrackedRepositories(ctx context.Context, path string) error {
	command, finishGit := gitCommand(ctx, "ls-files", "--others", "--exclude-standard", "-z")

	defer finishGit()
	command.Dir = path
	var stderr bytes.Buffer
	command.Stderr = &stderr
	untracked, err := command.Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	for _, record := range bytes.Split(untracked, []byte{0}) {
		rawName := string(record)
		if !strings.HasSuffix(rawName, "/") {
			continue
		}
		name := strings.TrimSuffix(rawName, "/")
		metadata := filepath.Join(path, filepath.FromSlash(name), ".git")
		if _, statErr := os.Lstat(metadata); statErr == nil {
			return fmt.Errorf("%w: embedded repository %s cannot be archived", repository.ErrWorkspaceDirty, name)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	return nil
}

func createWorkspaceSnapshot(ctx context.Context, path, head string) (string, error) {
	index, err := os.CreateTemp("", "holark-workspace-index-*")
	if err != nil {
		return "", err
	}
	indexPath := index.Name()
	if closeErr := index.Close(); closeErr != nil {
		return "", closeErr
	}
	if err := os.Remove(indexPath); err != nil {
		return "", err
	}
	defer os.Remove(indexPath)
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err = runGitInput(ctx, path, env, "", "read-tree", head); err != nil {
		return "", err
	}
	if _, err = runGitInput(ctx, path, env, "", "add", "-A", "--", "."); err != nil {
		return "", err
	}
	tree, err := runGitInput(ctx, path, env, "", "write-tree")
	if err != nil {
		return "", err
	}
	identity := []string{
		"GIT_AUTHOR_NAME=Holark", "GIT_AUTHOR_EMAIL=holark@localhost",
		"GIT_COMMITTER_NAME=Holark", "GIT_COMMITTER_EMAIL=holark@localhost",
	}
	return runGitInput(ctx, path, identity, "Archived Holon workspace\n", "commit-tree", tree, "-p", head)
}

func (g *Git) runInput(ctx context.Context, dir string, env []string, input string, args ...string) (string, error) {
	return runGitInput(ctx, dir, env, input, args...)
}

func runGitInput(ctx context.Context, dir string, env []string, input string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	command, finishGit := gitCommand(ctx, args...)

	defer finishGit()
	command.Dir = dir
	command.Env = append(os.Environ(), env...)
	command.Stdin = strings.NewReader(input)
	var output, stderr bytes.Buffer
	command.Stdout, command.Stderr = &output, &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(output.String()), nil
}

func excludeHolarkControlFiles(ctx context.Context, worktree string) error {
	excludePath, err := runGitAt(ctx, worktree, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("resolve workspace exclude file: %w", err)
	}
	data, err := os.ReadFile(excludePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read workspace exclude file: %w", err)
	}
	const excludeLine = ".holark/"
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == excludeLine {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o700); err != nil {
		return fmt.Errorf("create workspace exclude directory: %w", err)
	}
	prefix := ""
	if len(data) != 0 && !strings.HasSuffix(string(data), "\n") {
		prefix = "\n"
	}
	file, err := os.OpenFile(excludePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open workspace exclude file: %w", err)
	}
	defer file.Close()
	if _, err := file.WriteString(prefix + excludeLine + "\n"); err != nil {
		return fmt.Errorf("append workspace exclude file: %w", err)
	}
	return nil
}

func (g *Git) workspace(ctx context.Context, id string) (workspaceState, error) {
	return g.workspaceAllowDetached(ctx, id, false)
}
func (g *Git) workspaceAllowDetached(ctx context.Context, id string, allowDetached bool) (workspaceState, error) {
	if !workspaceID.MatchString(id) {
		return workspaceState{}, repository.ErrInvalidWorkspace
	}
	root := g.workspaceRoot()
	path := filepath.Join(root, id, "repo")
	if !contained(root, path) {
		return workspaceState{}, repository.ErrInvalidWorkspace
	}
	marker, err := os.ReadFile(filepath.Join(filepath.Dir(path), "owner"))
	if err != nil || !strings.HasPrefix(string(marker), g.descriptor.ID+"\n") {
		return workspaceState{}, repository.ErrWorkspaceOwner
	}
	common, err := runGitAt(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || !samePath(common, g.descriptor.CommonDir) {
		return workspaceState{}, repository.ErrWorkspaceOwner
	}
	branch, err := runGitAt(ctx, path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if !allowDetached && err != nil {
		return workspaceState{}, repository.ErrWorkspaceBranch
	}
	head, err := runGitAt(ctx, path, "rev-parse", "HEAD^{commit}")
	if err != nil {
		return workspaceState{}, repository.ErrInvalidWorkspace
	}
	lines := strings.Split(strings.TrimSpace(string(marker)), "\n")
	if len(lines) != 2 {
		return workspaceState{}, repository.ErrWorkspaceOwner
	}
	return workspaceState{Workspace: repository.Workspace{ID: id, Path: path, Branch: branch, BaseCommit: lines[1], HeadCommit: head}, root: root}, nil
}

func (g *Git) InspectWorkspace(ctx context.Context, id string) (repository.WorkspaceInspection, error) {
	return g.InspectWorkspaceWithOptions(ctx, id, repository.InspectOptions{})
}

func (g *Git) InspectWorkspaceWithOptions(ctx context.Context, id string, options repository.InspectOptions) (repository.WorkspaceInspection, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := g.mu.lock(ctx, "InspectWorkspaceWithOptions"); err != nil {
		return repository.WorkspaceInspection{}, err
	}
	defer g.mu.Unlock()
	w, err := g.workspace(ctx, id)
	if err != nil {
		return repository.WorkspaceInspection{}, err
	}
	baseBranch := options.BaseBranch
	if baseBranch == "" {
		baseBranch = g.descriptor.DefaultBranch
	}
	branchBaseCommit := strings.TrimSpace(options.BranchBaseCommit)
	if branchBaseCommit == "" {
		branchBaseCommit = w.BaseCommit
	}
	// Provider refs are refreshed separately. Recompute the comparison base
	// from the current history so manual rebases are reflected immediately.
	baseBranchTip, err := g.ResolveProviderBranch(ctx, baseBranch)
	if err == nil {
		branchBaseCommit, err = g.MergeBase(ctx, baseBranchTip, w.HeadCommit)
		if err != nil {
			return repository.WorkspaceInspection{}, err
		}
	} else if !errors.Is(err, repository.ErrRefNotFound) {
		return repository.WorkspaceInspection{}, err
	}
	workSessionStartCommit := strings.TrimSpace(options.WorkSessionStartCommit)
	if workSessionStartCommit == "" {
		workSessionStartCommit = w.BaseCommit
	}
	inspection, err := workspacepkg.InspectWithOptions(ctx, w.Path, w.Branch, baseBranch, branchBaseCommit, workspacepkg.InspectOptions{
		WorkSessionStartCommit: workSessionStartCommit, BaseRef: options.BaseRef, TargetRef: options.TargetRef,
		Contents: options.Contents, SummaryOnly: options.SummaryOnly, Path: options.Path, IgnoredPaths: options.IgnoredPaths,
	})
	if err != nil {
		if errors.Is(err, workspacepkg.ErrInvalidDiffRef) {
			return repository.WorkspaceInspection{}, repository.ErrRefNotFound
		}
		if errors.Is(err, workspacepkg.ErrInvalidDiffPath) {
			return repository.WorkspaceInspection{}, repository.ErrInvalidPath
		}
		return repository.WorkspaceInspection{}, err
	}
	return projectInspection(inspection, w.Workspace), nil
}

func (g *Git) InspectRange(ctx context.Context, branch, baseBranch, baseCommit, headCommit string) (repository.WorkspaceInspection, error) {
	return g.InspectRangeWithOptions(ctx, branch, baseBranch, baseCommit, headCommit, repository.InspectOptions{})
}

func (g *Git) InspectRangeWithOptions(ctx context.Context, branch, baseBranch, baseCommit, headCommit string, options repository.InspectOptions) (repository.WorkspaceInspection, error) {
	base, err := g.resolve(ctx, baseCommit)
	if err != nil {
		return repository.WorkspaceInspection{}, err
	}
	head, err := g.resolve(ctx, headCommit)
	if err != nil {
		return repository.WorkspaceInspection{}, err
	}
	inspection, err := workspacepkg.InspectRangeWithOptions(ctx, g.descriptor.CommonDir, branch, baseBranch, base, head, workspacepkg.InspectOptions{Path: options.Path, SummaryOnly: options.SummaryOnly})
	if err != nil {
		if errors.Is(err, workspacepkg.ErrInvalidDiffPath) {
			return repository.WorkspaceInspection{}, repository.ErrInvalidPath
		}
		return repository.WorkspaceInspection{}, err
	}
	ancestor, err := g.MergeBase(ctx, base, head)
	if err != nil {
		return repository.WorkspaceInspection{}, err
	}
	result := projectInspection(inspection, repository.Workspace{Branch: branch, BaseCommit: base, HeadCommit: head})
	result.CommonAncestorCommit = ancestor
	commits, err := g.Commits(ctx, ancestor, 1)
	if err != nil {
		return repository.WorkspaceInspection{}, err
	}
	if len(commits) > 0 {
		result.CommonAncestor = &commits[0]
	}
	return result, nil
}

func projectInspection(in protocol.WorkspaceInspected, compact repository.Workspace) repository.WorkspaceInspection {
	files := make([]repository.WorkspaceFile, 0, len(in.Files))
	changes := make([]repository.Change, 0, len(in.Files))
	for _, file := range in.Files {
		var contents *repository.WorkspaceContents
		if file.Contents != nil {
			contents = &repository.WorkspaceContents{Original: file.Contents.Original, Modified: file.Contents.Modified}
		}
		files = append(files, repository.WorkspaceFile{Contents: contents, ContentStatus: file.ContentStatus, Path: file.Path, OldPath: file.OldPath, Status: file.Status, Additions: file.Additions, Deletions: file.Deletions, Binary: file.Binary, Diff: file.Diff, DiffTruncated: file.DiffTruncated})
		changes = append(changes, repository.Change{Path: file.Path, Status: file.Status, Patch: file.Diff, Binary: file.Binary, Truncated: file.DiffTruncated})
	}
	refs := make([]repository.WorkspaceRef, 0, len(in.RefOptions))
	for _, ref := range in.RefOptions {
		refs = append(refs, repository.WorkspaceRef{ID: ref.ID, Label: ref.Label, Kind: ref.Kind, Commit: ref.Commit})
	}
	commits := make([]repository.WorkspaceCommit, 0, len(in.Commits))
	for _, commit := range in.Commits {
		commits = append(commits, repository.WorkspaceCommit{SHA: commit.SHA, ParentCommit: commit.ParentCommit, Subject: commit.Subject, Author: commit.Author, AuthoredAt: commit.AuthoredAt, Body: commit.Body})
	}
	return repository.WorkspaceInspection{
		Branch: in.Branch, BaseBranch: in.BaseBranch, BaseCommit: in.BaseCommit, HeadCommit: in.HeadCommit,
		HasChanges: in.HasChanges, Dirty: in.Dirty, Files: files, DiffTruncated: in.DiffTruncated,
		SummaryOnly: in.SummaryOnly, SelectedBaseRef: in.SelectedBaseRef, SelectedTargetRef: in.SelectedTargetRef,
		SelectedBaseCommit: in.SelectedBaseCommit, SelectedTargetCommit: in.SelectedTargetCommit,
		RefOptions: refs, BranchBaseCommit: in.BranchBaseCommit, WorkSessionStartCommit: in.WorkSessionStartCommit,
		WorkspaceHeadCommit: in.WorkspaceHeadCommit, Commits: commits,
		Workspace: compact, Clean: !in.Dirty, Changes: changes,
	}
}

func workspaceStatusArgs(ignoredPaths []string) ([]string, error) {
	args := []string{"status", "--porcelain=v1", "--untracked-files=all", "--"}
	paths, err := normalizedWorkspacePaths(ignoredPaths)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return args, nil
	}
	args = append(args, ".")
	for _, path := range paths {
		args = append(args, ":(exclude)"+path)
	}
	return args, nil
}

func normalizedWorkspacePaths(paths []string) ([]string, error) {
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		clean := filepath.ToSlash(filepath.Clean(path))
		if clean == "." || filepath.IsAbs(path) || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, repository.ErrInvalidWorkspace
		}
		normalized = append(normalized, clean)
	}
	return normalized, nil
}

func (g *Git) PublishWorkspace(ctx context.Context, request repository.PublishRequest) (repository.PublishedWorkspace, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := g.mu.lock(ctx, "PublishWorkspace"); err != nil {
		return repository.PublishedWorkspace{}, err
	}
	defer g.mu.Unlock()
	if request.Remote == "" || request.UpstreamBranch == "" || strings.HasPrefix(request.Remote, "-") {
		return repository.PublishedWorkspace{}, repository.ErrInvalidWorkspace
	}
	w, err := g.workspace(ctx, request.WorkspaceID)
	if err != nil {
		return repository.PublishedWorkspace{}, err
	}
	if request.ExpectedWorkspaceHead != "" && w.HeadCommit != request.ExpectedWorkspaceHead {
		return repository.PublishedWorkspace{}, repository.ErrStaleHead
	}
	statusArgs, err := workspaceStatusArgs(request.IgnoredPaths)
	if err != nil {
		return repository.PublishedWorkspace{}, err
	}
	status, err := runGitAt(ctx, w.Path, statusArgs...)
	if err != nil {
		return repository.PublishedWorkspace{}, err
	}
	if status != "" {
		return repository.PublishedWorkspace{}, repository.ErrWorkspaceDirty
	}
	ignoredPaths, err := normalizedWorkspacePaths(request.IgnoredPaths)
	if err != nil {
		return repository.PublishedWorkspace{}, err
	}
	if len(ignoredPaths) != 0 {
		args := append([]string{"diff", "--name-only", w.BaseCommit, w.HeadCommit, "--"}, ignoredPaths...)
		committed, diffErr := runGitAt(ctx, w.Path, args...)
		if diffErr != nil {
			return repository.PublishedWorkspace{}, diffErr
		}
		if committed != "" {
			return repository.PublishedWorkspace{}, repository.ErrIgnoredPathCommitted
		}
	}
	if _, err := runGitAt(ctx, w.Path, "check-ref-format", "refs/heads/"+request.UpstreamBranch); err != nil {
		return repository.PublishedWorkspace{}, repository.ErrInvalidWorkspace
	}
	request.Remote, err = gittransport.Resolve(ctx, w.Path, request.Remote, true)
	if err != nil {
		return repository.PublishedWorkspace{}, err
	}
	remoteHead, err := remoteBranchHead(ctx, w.Path, request.Remote, request.UpstreamBranch)
	if err != nil {
		return repository.PublishedWorkspace{}, err
	}
	// A retry after an uncertain push result is successful only when the exact
	// workspace head is now visible. No other remote movement is accepted.
	if remoteHead == w.HeadCommit {
		return repository.PublishedWorkspace{Branch: w.Branch, UpstreamBranch: request.UpstreamBranch, HeadCommit: w.HeadCommit}, nil
	}
	if remoteHead != request.ExpectedRemoteHead {
		return repository.PublishedWorkspace{}, repository.ErrStaleHead
	}
	expected := request.ExpectedRemoteHead
	if expected == "" {
		expected = strings.Repeat("0", 40)
	}
	lease := "--force-with-lease=refs/heads/" + request.UpstreamBranch + ":" + expected
	if _, err := runGitAt(ctx, w.Path, "push", lease, "--", request.Remote, w.HeadCommit+":refs/heads/"+request.UpstreamBranch); err != nil {
		message := err.Error()
		staleLease := strings.Contains(message, "[rejected]") && strings.Contains(message, "(stale info)")
		movedDuringPush := strings.Contains(message, "[remote rejected]") && strings.Contains(message, "cannot lock ref 'refs/heads/"+request.UpstreamBranch+"': is at ") && strings.Contains(message, "but expected "+request.ExpectedRemoteHead)
		if !request.PreservePushErrors || staleLease || movedDuringPush {
			return repository.PublishedWorkspace{}, repository.ErrStaleHead
		}
		return repository.PublishedWorkspace{}, err
	}
	return repository.PublishedWorkspace{Branch: w.Branch, UpstreamBranch: request.UpstreamBranch, HeadCommit: w.HeadCommit}, nil
}

func (g *Git) BackupWorkspace(ctx context.Context, id, remote, branch string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := g.mu.lock(ctx, "BackupWorkspace"); err != nil {
		return "", err
	}
	defer g.mu.Unlock()
	if remote == "" || branch == "" || strings.HasPrefix(remote, "-") {
		return "", repository.ErrInvalidWorkspace
	}
	w, err := g.workspace(ctx, id)
	if err != nil {
		return "", err
	}
	if _, err := runGitAt(ctx, w.Path, "check-ref-format", "refs/heads/"+branch); err != nil {
		return "", repository.ErrInvalidWorkspace
	}
	if output, err := runGitAt(ctx, w.Path, "ls-remote", "--heads", "--", remote, "refs/heads/"+branch); err != nil {
		return "", err
	} else if output != "" {
		return "", repository.ErrBackupExists
	}
	zero := strings.Repeat("0", 40)
	lease := "--force-with-lease=refs/heads/" + branch + ":" + zero
	if _, err := runGitAt(ctx, w.Path, "push", lease, "--", remote, w.HeadCommit+":refs/heads/"+branch); err != nil {
		return "", repository.ErrBackupExists
	}
	return w.HeadCommit, nil
}

func (g *Git) RemoveWorkspace(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := g.mu.lock(ctx, "RemoveWorkspace"); err != nil {
		return err
	}
	defer g.mu.Unlock()
	w, err := g.workspace(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrWorkspaceOwner) {
			path := filepath.Join(g.workspaceRoot(), id, "repo")
			if _, statErr := os.Lstat(filepath.Dir(path)); errors.Is(statErr, os.ErrNotExist) {
				if _, refErr := g.run(ctx, "show-ref", "--verify", "--quiet", "refs/heads/holark/"+id); refErr != nil {
					return nil
				}
			}
		}
		return err
	}
	status, err := runGitAt(ctx, w.Path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return err
	}
	if status != "" {
		return repository.ErrWorkspaceDirty
	}
	if _, err := g.run(ctx, "worktree", "remove", "--", w.Path); err != nil {
		return err
	}
	if _, err := g.run(ctx, "branch", "-D", w.Branch); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Dir(w.Path))
}

func remoteBranchHead(ctx context.Context, dir, remote, branch string) (string, error) {
	output, err := runGitAt(ctx, dir, "ls-remote", "--heads", "--", remote, "refs/heads/"+branch)
	if err != nil || output == "" {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) != 2 {
		return "", repository.ErrInvalidWorkspace
	}
	return fields[0], nil
}

func changesAt(ctx context.Context, dir, base, head string) ([]repository.Change, error) {
	out, err := runGitAt(ctx, dir, "diff", "--name-status", base+"..."+head)
	if err != nil {
		return nil, err
	}
	var changes []repository.Change
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		path := fields[len(fields)-1]
		patch, _ := runGitAt(ctx, dir, "diff", "--no-ext-diff", base+"..."+head, "--", path)
		changes = append(changes, repository.Change{Path: path, Status: fields[0], Patch: patch})
	}
	return changes, nil
}

func runGitAt(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	command, finishGit := gitCommand(ctx, args...)

	defer finishGit()
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func contained(root, path string) bool {
	root, path = filepath.Clean(root), filepath.Clean(path)
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func samePath(left, right string) bool {
	l, le := filepath.EvalSymlinks(filepath.Clean(left))
	r, re := filepath.EvalSymlinks(filepath.Clean(right))
	return le == nil && re == nil && l == r
}
