package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/holark-ai/holark/internal/gitexec"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/sessions/branchnaming"
)

type Manager struct {
	root string
	mu   sync.Mutex
}

var ErrDetachedHead = errors.New("workspace is not on a branch")
var ErrInvalidDiffRef = errors.New("invalid workspace diff ref")
var ErrInvalidDiffPath = errors.New("invalid workspace diff path")
var ErrInvalidBranchRename = errors.New("invalid workspace branch rename")
var ErrBranchRepository = errors.New("workspace branch repository unavailable")
var ErrBranchMismatch = errors.New("workspace branch changed")
var ErrBranchUnavailable = errors.New("workspace branch unavailable")
var ErrBranchRename = errors.New("workspace branch rename failed")

type InspectOptions struct {
	Contents               bool
	WorkSessionStartCommit string
	BaseRef                string
	TargetRef              string
	SummaryOnly            bool
	Path                   string
	IgnoredPaths           []string
}

type invalidDiffRefError struct {
	ref string
}

func (err invalidDiffRefError) Error() string {
	if strings.TrimSpace(err.ref) == "" {
		return "Diff reference is invalid."
	}
	return fmt.Sprintf("Diff reference %q is not available.", err.ref)
}

func (err invalidDiffRefError) Unwrap() error { return ErrInvalidDiffRef }

type Prepared struct {
	Path           string
	Branch         string
	BaseCommit     string
	HeadCommit     string
	UpstreamBranch string
}

type Published struct {
	Branch         string
	UpstreamBranch string
	HeadCommit     string
}

type Discovered struct {
	Branch         string
	ProposedBranch string
}

var ErrRebaseConflicts = errors.New("rebase conflicts")

type RebaseResult struct {
	HeadCommit string
	Rebased    bool
}

type GitSSHOptions struct {
	KeyPath        string
	KnownHostsPath string
}

func NewManager(root string) (*Manager, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, errors.New("invalid workspace root")
	}
	return &Manager{root: filepath.Clean(absolute)}, nil
}

func (manager *Manager) Contains(path string) bool {
	if manager == nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	return manager.contains(path)
}

// ContainsResolved also verifies that symlinks in an existing path do not
// redirect it outside the managed workspace root.
func (manager *Manager) ContainsResolved(path string) bool {
	if !manager.Contains(path) {
		return false
	}
	root, err := filepath.EvalSymlinks(manager.root)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, resolved)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// Prepare serializes all Git workspace mutations across every project.
func (manager *Manager) Prepare(ctx context.Context, projectSlug, sessionID, repositoryURL, defaultBranch, holarkGitRemoteURL string, gitSSHOptions ...GitSSHOptions) (Prepared, error) {
	if !validID(projectSlug) || !validID(sessionID) || repositoryURL == "" || defaultBranch == "" {
		return Prepared{}, errors.New("invalid workspace request")
	}
	if strings.HasPrefix(holarkGitRemoteURL, "-") {
		return Prepared{}, errors.New("invalid Holark Git remote URL")
	}
	gitSSH := firstGitSSHOptions(gitSSHOptions)
	manager.mu.Lock()
	defer manager.mu.Unlock()

	fetchRemoteURL := repositoryURL
	if holarkGitRemoteURL != "" {
		fetchRemoteURL = holarkGitRemoteURL
	}
	mirror := filepath.Join(manager.root, "mirrors", projectSlug+".git")
	if !manager.contains(mirror) {
		return Prepared{}, errors.New("workspace path escapes root")
	}
	if err := os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		return Prepared{}, fmt.Errorf("create mirror directory %q: %w", filepath.Dir(mirror), err)
	}
	if err := manager.updateMirror(ctx, mirror, sessionID, fetchRemoteURL); err != nil {
		return Prepared{}, err
	}
	workspaceKey, err := manager.allocateWorkspaceKey(ctx, mirror, sessionID)
	if err != nil {
		return Prepared{}, err
	}
	worktree := filepath.Join(manager.root, "sessions", workspaceKey, "repo")
	if !manager.contains(worktree) {
		return Prepared{}, errors.New("workspace path escapes root")
	}
	if _, err := os.Lstat(filepath.Dir(worktree)); err == nil {
		return Prepared{}, fmt.Errorf("session workspace already exists: %q", filepath.Dir(worktree))
	} else if !errors.Is(err, os.ErrNotExist) {
		return Prepared{}, fmt.Errorf("inspect session workspace %q: %w", filepath.Dir(worktree), err)
	}

	sourceRef := "refs/heads/" + defaultBranch
	if err := manager.git(ctx, "check-ref-format", sourceRef); err != nil {
		return Prepared{}, fmt.Errorf("invalid base branch: %w", err)
	}
	stagingRef := "refs/holark/fetch/" + sessionID
	branch := "holark/" + workspaceKey
	sessionRef := "refs/heads/" + branch
	if exists, err := manager.refExists(ctx, mirror, stagingRef); err != nil {
		return Prepared{}, fmt.Errorf("inspect staging ref: %w", err)
	} else if exists {
		return Prepared{}, errors.New("staging ref already exists")
	}
	if err := manager.git(ctx, "check-ref-format", sessionRef); err != nil {
		return Prepared{}, fmt.Errorf("invalid session branch: %w", err)
	}
	if exists, err := manager.sessionBranchReserved(ctx, mirror, branch); err != nil {
		return Prepared{}, fmt.Errorf("inspect session ref: %w", err)
	} else if exists {
		return Prepared{}, errors.New("session branch already exists")
	}

	stagingCreated := false
	branchCreated := false
	succeeded := false
	defer func() {
		if !succeeded {
			_ = manager.git(context.Background(), "--git-dir", mirror, "worktree", "remove", "--force", worktree)
			_ = os.RemoveAll(filepath.Dir(worktree))
			if branchCreated {
				_ = manager.git(context.Background(), "--git-dir", mirror, "branch", "-D", branch)
			}
			if stagingCreated {
				_ = manager.git(context.Background(), "--git-dir", mirror, "update-ref", "-d", stagingRef)
			}
		}
	}()
	fetchArgs := []string{"--git-dir", mirror}
	if command := gitSSHCommand(gitSSH); command != "" && holarkGitRemoteURL != "" {
		fetchArgs = append(fetchArgs, "-c", "core.sshCommand="+command)
	}
	fetchArgs = append(fetchArgs, "fetch", "--no-tags", "--no-write-fetch-head", "--refmap=", fetchRemoteURL, sourceRef+":"+stagingRef)
	if err := manager.git(ctx, fetchArgs...); err != nil {
		return Prepared{}, fmt.Errorf("fetch base branch: %w", err)
	}
	stagingCreated = true
	commit, err := manager.gitOutput(ctx, "--git-dir", mirror, "rev-parse", "--verify", stagingRef+"^{commit}")
	if err != nil {
		return Prepared{}, fmt.Errorf("resolve base commit: %w", err)
	}
	baseCommit := strings.TrimSpace(commit)
	if err := manager.git(ctx, "--git-dir", mirror, "update-ref", "refs/remotes/cache/"+defaultBranch, baseCommit); err != nil {
		return Prepared{}, fmt.Errorf("update cached base branch ref: %w", err)
	}
	if err := manager.git(ctx, "--git-dir", mirror, "branch", branch, baseCommit); err != nil {
		return Prepared{}, fmt.Errorf("create session branch: %w", err)
	}
	branchCreated = true
	if err := manager.git(ctx, "--git-dir", mirror, "update-ref", "-d", stagingRef); err != nil {
		return Prepared{}, fmt.Errorf("delete staging ref: %w", err)
	}
	stagingCreated = false
	if err := os.MkdirAll(filepath.Dir(worktree), 0o700); err != nil {
		return Prepared{}, fmt.Errorf("create session directory %q: %w", filepath.Dir(worktree), err)
	}
	if err := manager.git(ctx, "--git-dir", mirror, "worktree", "add", worktree, branch); err != nil {
		return Prepared{}, fmt.Errorf("create session worktree: %w", err)
	}
	if err := manager.configureWorktree(ctx, worktree, sessionID, projectSlug, repositoryURL, holarkGitRemoteURL, gitSSH); err != nil {
		return Prepared{}, err
	}
	succeeded = true
	return Prepared{Path: worktree, Branch: branch, BaseCommit: baseCommit, HeadCommit: baseCommit}, nil
}

// PrepareFromCommit serializes Git workspace mutations and creates a review worktree at an exact commit.
func (manager *Manager) PrepareFromCommit(ctx context.Context, projectSlug, sessionID, repositoryURL string, spec protocol.WorkspaceSpec, holarkGitRemoteURL string, gitSSHOptions ...GitSSHOptions) (Prepared, error) {
	if !validID(projectSlug) || !validID(sessionID) || repositoryURL == "" || spec.Mode != protocol.WorkspaceModeCommit ||
		strings.TrimSpace(spec.BaseBranch) == "" || strings.TrimSpace(spec.BaseCommit) == "" || strings.TrimSpace(spec.HeadBranch) == "" || strings.TrimSpace(spec.HeadCommit) == "" {
		return Prepared{}, errors.New("invalid workspace request")
	}
	if strings.HasPrefix(holarkGitRemoteURL, "-") {
		return Prepared{}, errors.New("invalid Holark Git remote URL")
	}
	gitSSH := firstGitSSHOptions(gitSSHOptions)
	manager.mu.Lock()
	defer manager.mu.Unlock()

	fetchRemoteURL := repositoryURL
	if holarkGitRemoteURL != "" {
		fetchRemoteURL = holarkGitRemoteURL
	}
	mirror := filepath.Join(manager.root, "mirrors", projectSlug+".git")
	worktree := filepath.Join(manager.root, "sessions", sessionID, "repo")
	if !manager.contains(mirror) || !manager.contains(worktree) {
		return Prepared{}, errors.New("workspace path escapes root")
	}
	if err := os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		return Prepared{}, fmt.Errorf("create mirror directory %q: %w", filepath.Dir(mirror), err)
	}
	if err := manager.updateMirror(ctx, mirror, sessionID, fetchRemoteURL); err != nil {
		return Prepared{}, err
	}
	if _, err := os.Lstat(filepath.Dir(worktree)); err == nil {
		return Prepared{}, fmt.Errorf("session workspace already exists: %q", filepath.Dir(worktree))
	} else if !errors.Is(err, os.ErrNotExist) {
		return Prepared{}, fmt.Errorf("inspect session workspace %q: %w", filepath.Dir(worktree), err)
	}

	headRef := "refs/heads/" + spec.HeadBranch
	baseRef := "refs/heads/" + spec.BaseBranch
	if err := manager.git(ctx, "check-ref-format", headRef); err != nil {
		return Prepared{}, fmt.Errorf("invalid head branch: %w", err)
	}
	if err := manager.git(ctx, "check-ref-format", baseRef); err != nil {
		return Prepared{}, fmt.Errorf("invalid base branch: %w", err)
	}
	if strings.TrimSpace(spec.UpstreamBranch) != "" {
		if err := manager.git(ctx, "check-ref-format", "refs/heads/"+spec.UpstreamBranch); err != nil {
			return Prepared{}, fmt.Errorf("invalid upstream branch: %w", err)
		}
	}
	prefix := strings.TrimSuffix(spec.BranchPrefix, "/")
	if prefix == "" {
		prefix = "holark/review"
	}
	branch := prefix + "/" + sessionID
	sessionRef := "refs/heads/" + branch
	if err := manager.git(ctx, "check-ref-format", sessionRef); err != nil {
		return Prepared{}, fmt.Errorf("invalid review branch: %w", err)
	}
	headStagingRef := "refs/holark/fetch/" + sessionID + "-head"
	baseStagingRef := "refs/holark/fetch/" + sessionID + "-base"
	for _, ref := range []string{headStagingRef, baseStagingRef, sessionRef} {
		exists, err := manager.refExists(ctx, mirror, ref)
		if err != nil {
			return Prepared{}, fmt.Errorf("inspect ref %s: %w", ref, err)
		}
		if exists {
			return Prepared{}, fmt.Errorf("workspace ref already exists: %s", ref)
		}
	}

	headStagingCreated := false
	baseStagingCreated := false
	branchCreated := false
	succeeded := false
	defer func() {
		if !succeeded {
			_ = manager.git(context.Background(), "--git-dir", mirror, "worktree", "remove", "--force", worktree)
			_ = os.RemoveAll(filepath.Dir(worktree))
			if branchCreated {
				_ = manager.git(context.Background(), "--git-dir", mirror, "branch", "-D", branch)
			}
			if headStagingCreated {
				_ = manager.git(context.Background(), "--git-dir", mirror, "update-ref", "-d", headStagingRef)
			}
			if baseStagingCreated {
				_ = manager.git(context.Background(), "--git-dir", mirror, "update-ref", "-d", baseStagingRef)
			}
		}
	}()
	fetchArgs := []string{"--git-dir", mirror}
	if command := gitSSHCommand(gitSSH); command != "" && holarkGitRemoteURL != "" {
		fetchArgs = append(fetchArgs, "-c", "core.sshCommand="+command)
	}
	fetchArgs = append(fetchArgs, "fetch", "--no-tags", "--no-write-fetch-head", "--refmap=", fetchRemoteURL, headRef+":"+headStagingRef, baseRef+":"+baseStagingRef)
	if err := manager.git(ctx, fetchArgs...); err != nil {
		return Prepared{}, fmt.Errorf("fetch review branches: %w", err)
	}
	headStagingCreated = true
	baseStagingCreated = true
	if _, err := manager.gitOutput(ctx, "--git-dir", mirror, "rev-parse", "--verify", spec.BaseCommit+"^{commit}"); err != nil {
		return Prepared{}, fmt.Errorf("resolve review base commit: %w", err)
	}
	currentBaseOutput, err := manager.gitOutput(ctx, "--git-dir", mirror, "rev-parse", "--verify", baseStagingRef+"^{commit}")
	if err != nil {
		return Prepared{}, fmt.Errorf("resolve fetched base branch commit: %w", err)
	}
	currentBaseCommit := strings.TrimSpace(currentBaseOutput)
	headCommitOutput, err := manager.gitOutput(ctx, "--git-dir", mirror, "rev-parse", "--verify", spec.HeadCommit+"^{commit}")
	if err != nil {
		return Prepared{}, fmt.Errorf("resolve review head commit: %w", err)
	}
	headCommit := strings.TrimSpace(headCommitOutput)
	if err := manager.git(ctx, "--git-dir", mirror, "update-ref", "refs/remotes/cache/"+spec.BaseBranch, currentBaseCommit); err != nil {
		return Prepared{}, fmt.Errorf("update cached base branch ref: %w", err)
	}
	if err := manager.git(ctx, "--git-dir", mirror, "branch", branch, headCommit); err != nil {
		return Prepared{}, fmt.Errorf("create review branch: %w", err)
	}
	branchCreated = true
	if err := manager.git(ctx, "--git-dir", mirror, "update-ref", "-d", headStagingRef); err != nil {
		return Prepared{}, fmt.Errorf("delete head staging ref: %w", err)
	}
	headStagingCreated = false
	if err := manager.git(ctx, "--git-dir", mirror, "update-ref", "-d", baseStagingRef); err != nil {
		return Prepared{}, fmt.Errorf("delete base staging ref: %w", err)
	}
	baseStagingCreated = false
	if err := os.MkdirAll(filepath.Dir(worktree), 0o700); err != nil {
		return Prepared{}, fmt.Errorf("create session directory %q: %w", filepath.Dir(worktree), err)
	}
	if err := manager.git(ctx, "--git-dir", mirror, "worktree", "add", worktree, branch); err != nil {
		return Prepared{}, fmt.Errorf("create review worktree: %w", err)
	}
	if err := manager.configureReviewWorktree(ctx, worktree, sessionID, projectSlug, repositoryURL); err != nil {
		return Prepared{}, err
	}
	succeeded = true
	return Prepared{Path: worktree, Branch: branch, BaseCommit: spec.BaseCommit, HeadCommit: headCommit, UpstreamBranch: spec.UpstreamBranch}, nil
}

func (manager *Manager) RenameProviderBranch(ctx context.Context, projectSlug, worktreePath, expectedBranch, repositoryURL, slug, holarkGitRemoteURL string, gitSSHOptions ...GitSSHOptions) (string, error) {
	if !validID(projectSlug) || repositoryURL == "" || !branchnaming.ValidSlug(slug) || strings.TrimSpace(expectedBranch) == "" || !filepath.IsAbs(worktreePath) || filepath.Clean(worktreePath) != worktreePath {
		return "", ErrInvalidBranchRename
	}
	if strings.HasPrefix(holarkGitRemoteURL, "-") {
		return "", ErrInvalidBranchRename
	}
	gitSSH := firstGitSSHOptions(gitSSHOptions)
	manager.mu.Lock()
	defer manager.mu.Unlock()

	fetchRemoteURL := repositoryURL
	useGitSSH := false
	if holarkGitRemoteURL != "" {
		fetchRemoteURL = holarkGitRemoteURL
		useGitSSH = gitSSHCommand(gitSSH) != ""
	}
	mirror := filepath.Join(manager.root, "mirrors", projectSlug+".git")
	if !manager.contains(mirror) || !manager.ContainsResolved(worktreePath) || !manager.isSessionWorktreePath(worktreePath) {
		return "", ErrInvalidBranchRename
	}
	currentOutput, err := manager.gitOutput(ctx, "-C", worktreePath, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("%w: inspect current branch: %v", ErrBranchRepository, err)
	}
	current := strings.TrimSpace(currentOutput)
	if current != expectedBranch {
		return "", fmt.Errorf("%w: expected %q, found %q", ErrBranchMismatch, expectedBranch, current)
	}
	if err := os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		return "", fmt.Errorf("%w: create mirror directory %q: %v", ErrBranchRepository, filepath.Dir(mirror), err)
	}
	if err := manager.updateMirror(ctx, mirror, "branch-naming", fetchRemoteURL); err != nil {
		return "", fmt.Errorf("%w: %v", ErrBranchRepository, err)
	}
	if err := manager.refreshCacheRefs(ctx, mirror, gitSSH, useGitSSH); err != nil {
		return "", fmt.Errorf("%w: %v", ErrBranchRepository, err)
	}
	for index := 1; ; index++ {
		candidate, err := providerBranchCandidate(slug, index)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidBranchRename, err)
		}
		if err := manager.git(ctx, "check-ref-format", "refs/heads/"+candidate); err != nil {
			return "", fmt.Errorf("%w: invalid provider branch: %v", ErrInvalidBranchRename, err)
		}
		reserved, err := manager.providerBranchReserved(ctx, mirror, candidate)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrBranchRepository, err)
		}
		if !reserved {
			if err := manager.git(ctx, "-C", worktreePath, "config", "--worktree", "holark.proposedBranch", candidate); err != nil {
				return "", fmt.Errorf("%w: record proposed branch: %v", ErrBranchRename, err)
			}
			if err := manager.git(ctx, "-C", worktreePath, "branch", "-m", candidate); err != nil {
				_ = manager.git(context.Background(), "-C", worktreePath, "config", "--worktree", "--unset-all", "holark.proposedBranch")
				return "", fmt.Errorf("%w: %v", ErrBranchRename, err)
			}
			verifiedOutput, err := manager.gitOutput(ctx, "-C", worktreePath, "symbolic-ref", "--short", "HEAD")
			if err != nil || strings.TrimSpace(verifiedOutput) != candidate {
				return "", fmt.Errorf("%w: renamed branch could not be verified", ErrBranchRename)
			}
			return candidate, nil
		}
		if index == 9999 {
			return "", ErrBranchUnavailable
		}
	}
}

func (manager *Manager) Discover(ctx context.Context, worktreePath string) (Discovered, error) {
	if !filepath.IsAbs(worktreePath) || filepath.Clean(worktreePath) != worktreePath || !manager.ContainsResolved(worktreePath) {
		return Discovered{}, ErrInvalidBranchRename
	}
	branchOutput, err := manager.gitOutput(ctx, "-C", worktreePath, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return Discovered{}, fmt.Errorf("%w: inspect current branch: %v", ErrBranchRepository, err)
	}
	result := Discovered{Branch: strings.TrimSpace(branchOutput)}
	proposalOutput, err := manager.gitOutput(ctx, "-C", worktreePath, "config", "--worktree", "--get", "holark.proposedBranch")
	if err == nil {
		proposal := strings.TrimSpace(proposalOutput)
		if proposal == result.Branch && branchnaming.ValidBranch(proposal) {
			result.ProposedBranch = proposal
		}
	}
	return result, nil
}

func (manager *Manager) allocateWorkspaceKey(ctx context.Context, mirror, sessionID string) (string, error) {
	prefix, hexID := "session-", strings.TrimPrefix(sessionID, "session-")
	if prefix+hexID != sessionID || len(hexID) < 8 {
		return sessionID, nil
	}
	if _, err := hex.DecodeString(hexID); err != nil {
		return sessionID, nil
	}
	for length := 8; ; {
		key := prefix + hexID[:length]
		worktreeDir := filepath.Join(manager.root, "sessions", key)
		pathReserved := false
		if _, err := os.Lstat(worktreeDir); err == nil {
			pathReserved = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect session workspace %q: %w", worktreeDir, err)
		}
		branchReserved, err := manager.sessionBranchReserved(ctx, mirror, "holark/"+key)
		if err != nil {
			return "", fmt.Errorf("inspect session ref: %w", err)
		}
		if !pathReserved && !branchReserved {
			return key, nil
		}
		if length == len(hexID) {
			break
		}
		length += 4
		if length > len(hexID) {
			length = len(hexID)
		}
	}
	return "", errors.New("session workspace key already exists")
}

func providerBranchCandidate(slug string, index int) (string, error) {
	if index < 1 || !branchnaming.ValidSlug(slug) {
		return "", branchnaming.ErrInvalidSlug
	}
	candidateSlug := slug
	if index > 1 {
		suffix := "-" + strconv.Itoa(index)
		limit := 48 - len(suffix)
		if limit < 3 {
			return "", branchnaming.ErrInvalidBranch
		}
		if len(candidateSlug) > limit {
			candidateSlug = strings.TrimRight(candidateSlug[:limit], "-")
		}
		candidateSlug += suffix
	}
	return branchnaming.ComposeBranch(candidateSlug)
}

// Cleanup removes a prepared session worktree and its local workspace branch.
func (manager *Manager) Cleanup(ctx context.Context, projectSlug, worktreePath, branch string) error {
	if !validID(projectSlug) || worktreePath == "" || strings.TrimSpace(branch) == "" {
		return errors.New("invalid workspace cleanup request")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()

	mirror := filepath.Join(manager.root, "mirrors", projectSlug+".git")
	worktree := filepath.Clean(worktreePath)
	if !manager.contains(mirror) || !manager.contains(worktree) {
		return errors.New("workspace path escapes root")
	}
	if !manager.isSessionWorktreePath(worktree) {
		return errors.New("invalid workspace cleanup path")
	}
	sessionRef := "refs/heads/" + branch
	if err := manager.git(ctx, "check-ref-format", sessionRef); err != nil {
		return fmt.Errorf("invalid workspace branch: %w", err)
	}

	var cleanupErr error
	if err := manager.git(ctx, "--git-dir", mirror, "worktree", "remove", "--force", worktree); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove session worktree: %w", err))
	}
	if err := os.RemoveAll(filepath.Dir(worktree)); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove session directory %q: %w", filepath.Dir(worktree), err))
	}
	if exists, err := manager.refExists(ctx, mirror, sessionRef); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("inspect workspace branch: %w", err))
	} else if exists {
		if err := manager.git(ctx, "--git-dir", mirror, "branch", "-D", branch); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete workspace branch: %w", err))
		}
	}
	return cleanupErr
}

const maxDiffBytes = 80 * 1024
const maxUntrackedBytes = 24 * 1024
const maxTotalDiffBytes = 96 * 1024
const maxPullRequestFileDiffBytes = 8 * 1024 * 1024

func Inspect(ctx context.Context, worktreePath, branch, baseBranch, baseCommit string) (protocol.WorkspaceInspected, error) {
	return InspectWithOptions(ctx, worktreePath, branch, baseBranch, baseCommit, InspectOptions{})
}

func InspectWithOptions(ctx context.Context, worktreePath, branch, baseBranch, baseCommit string, options InspectOptions) (protocol.WorkspaceInspected, error) {
	if worktreePath == "" || branch == "" || baseBranch == "" || baseCommit == "" {
		return protocol.WorkspaceInspected{}, errors.New("invalid workspace inspection request")
	}
	workspaceInspector := inspector{worktree: worktreePath}
	head, err := workspaceInspector.gitOutput(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return protocol.WorkspaceInspected{}, errors.New("read workspace head")
	}
	headCommit := strings.TrimSpace(head)
	currentBranch, err := workspaceInspector.gitOutput(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return protocol.WorkspaceInspected{}, ErrDetachedHead
	}
	statusArgs := append([]string{"status", "--porcelain=v1"}, changePathspecArgs(options.IgnoredPaths)...)
	status, err := workspaceInspector.gitOutput(ctx, statusArgs...)
	if err != nil {
		return protocol.WorkspaceInspected{}, errors.New("read workspace status")
	}
	commits, err := workspaceInspector.firstParentCommits(ctx, baseCommit, headCommit)
	if err != nil {
		return protocol.WorkspaceInspected{}, errors.New("read workspace commit history")
	}
	branchBaseCommit, err := workspaceInspector.resolveCommit(ctx, baseCommit)
	if err != nil {
		return protocol.WorkspaceInspected{}, errors.New("read workspace branch base")
	}
	workSessionStartCommit := effectiveWorkSessionStartCommit(workspaceInspector, ctx, options.WorkSessionStartCommit, branchBaseCommit, commits)

	baseRef := strings.TrimSpace(options.BaseRef)
	if baseRef == "" {
		baseRef = "session-start"
	}
	targetRef := strings.TrimSpace(options.TargetRef)
	if targetRef == "" {
		targetRef = "worktree"
	}
	base, err := workspaceInspector.resolveDiffRef(ctx, baseRef, branchBaseCommit, workSessionStartCommit, headCommit)
	if err != nil {
		return protocol.WorkspaceInspected{}, err
	}
	target, err := workspaceInspector.resolveDiffRef(ctx, targetRef, branchBaseCommit, workSessionStartCommit, headCommit)
	if err != nil {
		return protocol.WorkspaceInspected{}, err
	}
	selectedPath := options.Path
	if options.Contents && (strings.TrimSpace(selectedPath) == "" || options.SummaryOnly) {
		return protocol.WorkspaceInspected{}, ErrInvalidDiffPath
	}
	if selectedPath != "" && unsafeRelative(selectedPath) {
		return protocol.WorkspaceInspected{}, ErrInvalidDiffPath
	}
	files, truncated, err := workspaceInspector.inspectResolvedRange(ctx, base, target, !options.SummaryOnly, selectedPath)
	if err != nil {
		return protocol.WorkspaceInspected{}, err
	}
	files = withoutIgnoredFiles(files, options.IgnoredPaths)
	if options.Contents {
		for index := range files {
			if err := workspaceInspector.loadContents(ctx, base, target, &files[index]); err != nil {
				return protocol.WorkspaceInspected{}, err
			}
		}
	}

	return protocol.WorkspaceInspected{
		Branch:                 strings.TrimSpace(currentBranch),
		BaseBranch:             baseBranch,
		BaseCommit:             base.Commit,
		HeadCommit:             target.Commit,
		HasChanges:             len(files) > 0,
		Dirty:                  strings.TrimSpace(status) != "",
		Files:                  files,
		DiffTruncated:          truncated,
		SummaryOnly:            options.SummaryOnly,
		SelectedBaseRef:        base.ID,
		SelectedTargetRef:      target.ID,
		SelectedBaseCommit:     base.Commit,
		SelectedTargetCommit:   target.Commit,
		RefOptions:             workspaceInspector.diffRefOptions(baseBranch, branchBaseCommit, workSessionStartCommit, headCommit),
		BranchBaseCommit:       branchBaseCommit,
		WorkSessionStartCommit: workSessionStartCommit,
		WorkspaceHeadCommit:    headCommit,
		Commits:                commits,
	}, nil
}

func InspectRange(ctx context.Context, gitDir, branch, baseBranch, baseCommit, headCommit string) (protocol.WorkspaceInspected, error) {
	return InspectRangeWithOptions(ctx, gitDir, branch, baseBranch, baseCommit, headCommit, InspectOptions{})
}

func InspectRangeWithOptions(ctx context.Context, gitDir, branch, baseBranch, baseCommit, headCommit string, options InspectOptions) (protocol.WorkspaceInspected, error) {
	if gitDir == "" || branch == "" || baseBranch == "" || baseCommit == "" || headCommit == "" {
		return protocol.WorkspaceInspected{}, errors.New("invalid workspace inspection request")
	}
	if options.Path != "" && unsafeRelative(options.Path) {
		return protocol.WorkspaceInspected{}, ErrInvalidDiffPath
	}
	inspector := inspector{gitDir: gitDir, base: baseCommit, head: headCommit}
	files, truncated, err := inspector.changedFilesWithPatchLimit(ctx, !options.SummaryOnly, options.Path, maxPullRequestFileDiffBytes)
	if err != nil {
		return protocol.WorkspaceInspected{}, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if !options.SummaryOnly && options.Path == "" && applyDiffBudget(files, maxTotalDiffBytes) {
		truncated = true
	}
	return protocol.WorkspaceInspected{
		Branch:        branch,
		BaseBranch:    baseBranch,
		BaseCommit:    baseCommit,
		HeadCommit:    headCommit,
		HasChanges:    len(files) > 0,
		Dirty:         false,
		Files:         files,
		DiffTruncated: truncated,
		SummaryOnly:   options.SummaryOnly,
	}, nil
}

type resolvedDiffRef struct {
	ID       string
	Kind     string
	Commit   string
	Worktree bool
}

func (inspector inspector) inspectResolvedRange(ctx context.Context, base, target resolvedDiffRef, includePatch bool, selectedPath string) ([]protocol.WorkspaceFileDiff, bool, error) {
	if base.Worktree && target.Worktree {
		return []protocol.WorkspaceFileDiff{}, false, nil
	}
	diffInspector := inspector
	switch {
	case base.Worktree:
		diffInspector.base = target.Commit
		diffInspector.head = ""
		diffInspector.reverse = true
	case target.Worktree:
		diffInspector.base = base.Commit
		diffInspector.head = ""
	default:
		diffInspector.base = base.Commit
		diffInspector.head = target.Commit
	}
	files, truncated, err := diffInspector.changedFilesWithPatch(ctx, includePatch, selectedPath)
	if err != nil {
		return nil, false, err
	}
	if base.Worktree || target.Worktree {
		untracked, err := inspector.untrackedFiles(ctx, includePatch, base.Worktree, selectedPath)
		if err != nil {
			return nil, false, err
		}
		files = append(files, untracked...)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if includePatch && applyDiffBudget(files, maxTotalDiffBytes) {
		truncated = true
	}
	for _, file := range files {
		if file.DiffTruncated {
			truncated = true
			break
		}
	}
	return files, truncated, nil
}

func (inspector inspector) changedFilesWithPatch(ctx context.Context, includePatch bool, selectedPath string) ([]protocol.WorkspaceFileDiff, bool, error) {
	return inspector.changedFilesWithPatchLimit(ctx, includePatch, selectedPath, maxTotalDiffBytes)
}

func (inspector inspector) changedFilesWithPatchLimit(ctx context.Context, includePatch bool, selectedPath string, selectedLimit int) ([]protocol.WorkspaceFileDiff, bool, error) {
	files, err := inspector.changedFiles(ctx)
	if err != nil {
		return nil, false, err
	}
	if selectedPath != "" {
		files = selectedFileDiffs(files, selectedPath)
	}
	if !includePatch {
		return files, false, nil
	}
	limit := maxDiffBytes
	arguments := inspector.diffArgs("--no-ext-diff", "--find-renames")
	if selectedPath != "" {
		limit = selectedLimit
		pathspecs := []string{selectedPath}
		if len(files) == 1 && files[0].OldPath != "" && files[0].OldPath != selectedPath {
			pathspecs = append(pathspecs, files[0].OldPath)
		}
		arguments = append(arguments, pathspecs...)
	}
	diff, truncated, err := inspector.gitOutputLimited(ctx, limit, arguments...)
	if err != nil {
		return nil, false, errors.New("read workspace diff")
	}
	for path, section := range splitDiff(diff) {
		if index := fileIndex(files, path); index >= 0 {
			files[index].Diff = section
		}
	}
	if truncated {
		if selectedPath != "" {
			for index := range files {
				if files[index].Diff != "" {
					files[index].DiffTruncated = true
				}
			}
		} else if path := lastDiffPath(diff); path != "" {
			if index := fileIndex(files, path); index >= 0 {
				files[index].DiffTruncated = true
			}
		}
	}
	return files, truncated, nil
}

func (inspector inspector) resolveDiffRef(ctx context.Context, ref, baseCommit, workSessionStartCommit, headCommit string) (resolvedDiffRef, error) {
	switch {
	case ref == "session-start":
		commit, err := inspector.resolveCommit(ctx, workSessionStartCommit)
		if err != nil {
			return resolvedDiffRef{}, invalidDiffRefError{ref: ref}
		}
		return resolvedDiffRef{ID: ref, Kind: "session_start", Commit: commit}, nil
	case ref == "main":
		commit, err := inspector.resolveCommit(ctx, baseCommit)
		if err != nil {
			return resolvedDiffRef{}, invalidDiffRefError{ref: ref}
		}
		return resolvedDiffRef{ID: ref, Kind: "branch", Commit: commit}, nil
	case ref == "worktree":
		return resolvedDiffRef{ID: ref, Kind: "worktree", Commit: headCommit, Worktree: true}, nil
	case strings.HasPrefix(ref, "commit:"):
		sha := strings.TrimPrefix(ref, "commit:")
		if !commitSHAPattern.MatchString(sha) {
			return resolvedDiffRef{}, invalidDiffRefError{ref: ref}
		}
		commit, err := inspector.resolveCommit(ctx, sha)
		if err != nil {
			return resolvedDiffRef{}, invalidDiffRefError{ref: ref}
		}
		return resolvedDiffRef{ID: "commit:" + commit, Kind: "commit", Commit: commit}, nil
	default:
		return resolvedDiffRef{}, invalidDiffRefError{ref: ref}
	}
}

func (inspector inspector) diffRefOptions(baseBranch, baseCommit, workSessionStartCommit, headCommit string) []protocol.WorkspaceDiffRef {
	refs := []protocol.WorkspaceDiffRef{
		{ID: "main", Label: fmt.Sprintf("%s (%s)", baseBranch, shortCommit(baseCommit)), Kind: "branch", Commit: baseCommit},
		{ID: "session-start", Label: "Session start (" + shortCommit(workSessionStartCommit) + ")", Kind: "session_start", Commit: workSessionStartCommit},
	}
	refs = append(refs, protocol.WorkspaceDiffRef{ID: "worktree", Label: "Worktree (HEAD " + shortCommit(headCommit) + ")", Kind: "worktree", Commit: headCommit})
	return refs
}

func (inspector inspector) firstParentCommits(ctx context.Context, baseCommit, headCommit string) ([]protocol.WorkspaceCommit, error) {
	output, err := inspector.gitOutput(ctx, "log", "--first-parent", "-z", "--format=%H%x00%P%x00%s%x00%an%x00%aI%x00%b", baseCommit+".."+headCommit)
	if err != nil {
		return nil, err
	}
	var commits []protocol.WorkspaceCommit
	// NUL-delimited fields preserve multiline bodies and record boundaries.
	fields := strings.Split(output, "\x00")
	for len(fields) >= 6 {
		record := fields[:6]
		fields = fields[6:]
		parents := strings.Fields(record[1])
		if len(parents) == 0 {
			continue
		}
		commits = append(commits, protocol.WorkspaceCommit{
			SHA:          record[0],
			ParentCommit: parents[0],
			Subject:      strings.TrimSpace(record[2]),
			Author:       record[3],
			AuthoredAt:   record[4],
			Body:         strings.TrimRight(record[5], "\n"),
		})
	}
	return commits, nil
}

func effectiveWorkSessionStartCommit(inspector inspector, ctx context.Context, requested, baseCommit string, commits []protocol.WorkspaceCommit) string {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return baseCommit
	}
	commit, err := inspector.resolveCommit(ctx, requested)
	if err != nil {
		return baseCommit
	}
	if commit == baseCommit {
		return baseCommit
	}
	for _, candidate := range commits {
		if candidate.SHA == commit {
			return commit
		}
	}
	return baseCommit
}

func (inspector inspector) resolveCommit(ctx context.Context, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00 \t\r\n") {
		return "", errors.New("invalid commit ref")
	}
	output, err := inspector.gitOutput(ctx, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func shortCommit(commit string) string {
	if len(commit) <= 8 {
		return commit
	}
	return commit[:8]
}

var commitSHAPattern = regexp.MustCompile(`^[0-9a-fA-F]{4,64}$`)

func Rebase(ctx context.Context, worktreePath, baseCommit string) (RebaseResult, error) {
	if worktreePath == "" || strings.TrimSpace(baseCommit) == "" || strings.HasPrefix(baseCommit, "-") {
		return RebaseResult{}, errors.New("invalid workspace rebase request")
	}
	publisher := publisher{worktree: worktreePath}
	baseCommit = strings.TrimSpace(baseCommit)
	if _, err := publisher.gitOutput(ctx, "rev-parse", "--verify", baseCommit+"^{commit}"); err != nil {
		return RebaseResult{}, errors.New("invalid workspace rebase base commit")
	}
	head, err := publisher.gitOutput(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return RebaseResult{}, errors.New("read workspace head")
	}
	headCommit := strings.TrimSpace(head)
	if _, err := publisher.gitOutput(ctx, "merge-base", "--is-ancestor", baseCommit, "HEAD"); err == nil {
		return RebaseResult{HeadCommit: headCommit, Rebased: false}, nil
	}
	if _, err := publisher.gitOutput(ctx, "rebase", baseCommit); err != nil {
		return RebaseResult{}, ErrRebaseConflicts
	}
	rebasedHead, err := publisher.gitOutput(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return RebaseResult{}, errors.New("read rebased workspace head")
	}
	return RebaseResult{HeadCommit: strings.TrimSpace(rebasedHead), Rebased: true}, nil
}

func PublishWorkspaceToOrigin(ctx context.Context, worktreePath, branch string, publishOptions ...string) (Published, error) {
	upstreamBranch := ""
	expectedRemoteHead := ""
	if len(publishOptions) > 0 {
		upstreamBranch = publishOptions[0]
	}
	if len(publishOptions) > 1 {
		expectedRemoteHead = publishOptions[1]
	}
	ignoredPaths := []string{}
	if len(publishOptions) > 2 {
		ignoredPaths = publishOptions[2:]
	}
	if worktreePath == "" || branch == "" || strings.HasPrefix(branch, "-") {
		return Published{}, errors.New("invalid workspace publish request")
	}
	if upstreamBranch == "" {
		upstreamBranch = branch
	}
	if strings.HasPrefix(upstreamBranch, "-") || strings.HasPrefix(expectedRemoteHead, "-") {
		return Published{}, errors.New("invalid workspace publish request")
	}
	publisher := publisher{worktree: worktreePath}
	if _, err := publisher.gitOutput(ctx, "check-ref-format", "refs/heads/"+branch); err != nil {
		return Published{}, errors.New("invalid workspace branch")
	}
	if _, err := publisher.gitOutput(ctx, "check-ref-format", "refs/heads/"+upstreamBranch); err != nil {
		return Published{}, errors.New("invalid workspace upstream branch")
	}
	currentBranch, err := publisher.gitOutput(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(currentBranch) != branch {
		return Published{}, errors.New("workspace is not on the session branch")
	}
	head, err := publisher.gitOutput(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return Published{}, errors.New("read workspace head")
	}
	statusArgs := append([]string{"status", "--porcelain=v1", "--untracked-files=all"}, changePathspecArgs(ignoredPaths)...)
	status, err := publisher.gitOutput(ctx, statusArgs...)
	if err != nil {
		return Published{}, errors.New("read workspace status")
	}
	if strings.TrimSpace(status) != "" {
		return Published{}, errors.New("workspace has uncommitted changes")
	}
	args := []string{"push", "origin"}
	if expectedRemoteHead != "" {
		args = append(args, "--force-with-lease=refs/heads/"+upstreamBranch+":"+expectedRemoteHead)
	}
	args = append(args, "HEAD:refs/heads/"+upstreamBranch)
	if _, err := publisher.gitOutput(ctx, args...); err != nil {
		return Published{}, fmt.Errorf("push workspace branch: %w", err)
	}
	return Published{Branch: branch, UpstreamBranch: upstreamBranch, HeadCommit: strings.TrimSpace(head)}, nil
}

func HashChanges(ctx context.Context, worktreePath string, ignoredPaths ...string) (string, bool, error) {
	if worktreePath == "" {
		return "", false, errors.New("invalid workspace hash request")
	}
	hasher := changeHasher{worktree: worktreePath}
	statusArgs := append([]string{"status", "--porcelain=v1", "--untracked-files=all", "-z"}, changePathspecArgs(ignoredPaths)...)
	status, err := hasher.gitOutput(ctx, statusArgs...)
	if err != nil {
		return "", false, errors.New("read workspace status")
	}
	if len(status) == 0 {
		return "", false, nil
	}
	hash := sha256.New()
	writeHashSection(hash, "status", []byte(status))
	diffArgs := append([]string{"diff", "--binary", "--no-ext-diff", "HEAD"}, changePathspecArgs(ignoredPaths)...)
	diff, err := hasher.gitOutput(ctx, diffArgs...)
	if err != nil {
		return "", false, errors.New("hash workspace diff")
	}
	writeHashSection(hash, "diff-head", []byte(diff))
	untrackedArgs := append([]string{"ls-files", "--others", "--exclude-standard", "-z"}, changePathspecArgs(ignoredPaths)...)
	untracked, err := hasher.gitOutput(ctx, untrackedArgs...)
	if err != nil {
		return "", false, errors.New("read untracked files")
	}
	for _, path := range strings.Split(untracked, "\x00") {
		if path == "" || unsafeRelative(path) {
			continue
		}
		if err := hashUntrackedPath(hash, worktreePath, path); err != nil {
			return "", false, err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), true, nil
}

func changePathspecArgs(ignoredPaths []string) []string {
	args := []string{"--"}
	paths := make([]string, 0, len(ignoredPaths))
	for _, path := range ignoredPaths {
		clean := filepath.ToSlash(filepath.Clean(path))
		if clean == "." || unsafeRelative(clean) {
			continue
		}
		paths = append(paths, clean)
	}
	if len(paths) == 0 {
		return args
	}
	args = append(args, ".")
	for _, path := range paths {
		args = append(args, ":(exclude)"+path)
	}
	return args
}

type inspector struct {
	worktree string
	gitDir   string
	base     string
	head     string
	reverse  bool
}

type publisher struct {
	worktree string
}

type changeHasher struct {
	worktree string
}

func (hasher changeHasher) gitOutput(ctx context.Context, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = hasher.worktree
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func writeHashSection(hash io.Writer, name string, data []byte) {
	_, _ = fmt.Fprintf(hash, "%s %d\x00", name, len(data))
	_, _ = hash.Write(data)
	_, _ = hash.Write([]byte{0})
}

func hashUntrackedPath(hash io.Writer, worktreePath, path string) error {
	fullPath := filepath.Join(worktreePath, path)
	info, err := os.Lstat(fullPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("hash untracked file")
	}
	writeHashSection(hash, "untracked-path", []byte(path))
	writeHashSection(hash, "untracked-mode", []byte(info.Mode().String()))
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(fullPath)
		if err != nil {
			return errors.New("hash untracked symlink")
		}
		writeHashSection(hash, "untracked-symlink", []byte(target))
		return nil
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	file, err := os.Open(fullPath)
	if err != nil {
		return errors.New("hash untracked file")
	}
	defer file.Close()
	contentHash := sha256.New()
	if _, err := io.Copy(contentHash, file); err != nil {
		return errors.New("hash untracked file")
	}
	writeHashSection(hash, "untracked-content", []byte(hex.EncodeToString(contentHash.Sum(nil))))
	return nil
}

func (publisher publisher) gitOutput(ctx context.Context, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = publisher.worktree
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", gitCommandError{arguments: arguments, err: err, output: string(output)}
	}
	return string(output), nil
}

func (inspector inspector) changedFiles(ctx context.Context) ([]protocol.WorkspaceFileDiff, error) {
	output, err := inspector.gitOutput(ctx, inspector.diffArgs("--name-status", "--find-renames", "-z")...)
	if err != nil {
		return nil, errors.New("read changed files")
	}
	stats, err := inspector.diffStats(ctx)
	if err != nil {
		return nil, err
	}
	var files []protocol.WorkspaceFileDiff
	records := strings.Split(output, "\x00")
	for index := 0; index+1 < len(records); {
		status, path := records[index], records[index+1]
		index += 2
		if status == "" || path == "" {
			continue
		}
		file := protocol.WorkspaceFileDiff{Status: status[:1], Path: path}
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if index >= len(records) {
				break
			}
			file.OldPath, file.Path = path, records[index]
			index++
		}
		if stat, ok := stats[file.Path]; ok {
			file.Additions = stat.additions
			file.Deletions = stat.deletions
			file.Binary = stat.binary
		}
		files = append(files, file)
	}
	return files, nil
}

type diffStat struct {
	additions int
	deletions int
	binary    bool
}

func (inspector inspector) diffStats(ctx context.Context) (map[string]diffStat, error) {
	output, err := inspector.gitOutput(ctx, inspector.diffArgs("--numstat", "--find-renames", "-z")...)
	if err != nil {
		return nil, errors.New("read diff stats")
	}
	stats := make(map[string]diffStat)
	records := strings.Split(output, "\x00")
	for index := 0; index < len(records); index++ {
		fields := strings.SplitN(records[index], "\t", 3)
		if len(fields) != 3 {
			continue
		}
		path := fields[2]
		if path == "" {
			if index+2 >= len(records) {
				break
			}
			path = records[index+2]
			index += 2
		}
		stat := diffStat{}
		if fields[0] == "-" || fields[1] == "-" {
			stat.binary = true
		} else {
			fmt.Sscanf(fields[0], "%d", &stat.additions)
			fmt.Sscanf(fields[1], "%d", &stat.deletions)
		}
		stats[path] = stat
	}
	return stats, nil
}

func (inspector inspector) untrackedFiles(ctx context.Context, includePatch, reverse bool, selectedPath string) ([]protocol.WorkspaceFileDiff, error) {
	output, err := inspector.gitOutput(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, errors.New("read untracked files")
	}
	var files []protocol.WorkspaceFileDiff
	diffLimit := maxUntrackedBytes
	if selectedPath != "" {
		diffLimit = maxTotalDiffBytes
	}
	for _, path := range strings.Split(output, "\x00") {
		if path == "" || unsafeRelative(path) || (selectedPath != "" && path != selectedPath) {
			continue
		}
		file := protocol.WorkspaceFileDiff{Path: path, Status: "A"}
		if reverse {
			file.Status = "D"
		}
		filePath := filepath.Join(inspector.worktree, path)
		info, err := os.Lstat(filePath)
		if err != nil || info.IsDir() {
			continue
		}
		previewLimit := 0
		if includePatch {
			previewLimit = diffLimit
		}
		var contents untrackedFileContents
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			contents, err = inspectUntrackedSymlink(filePath, previewLimit)
		case info.Mode().IsRegular():
			contents, err = inspectUntrackedFile(ctx, filePath, previewLimit)
		default:
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if contents.binary {
			file.Binary = true
		} else {
			if reverse {
				file.Deletions = contents.lineCount
			} else {
				file.Additions = contents.lineCount
			}
			if includePatch {
				file.Diff, file.DiffTruncated = renderUntrackedFileDiff(path, contents, reverse, diffLimit)
			} else {
				file.DiffTruncated = untrackedFileDiffSize(path, contents, reverse) > int64(diffLimit)
			}
		}
		files = append(files, file)
	}
	return files, nil
}

func (inspector inspector) gitOutput(ctx context.Context, arguments ...string) (string, error) {
	command, finishGit := gitexec.Command(ctx, arguments...)
	defer finishGit()
	if inspector.gitDir != "" {
		command.Args = append([]string{"git", "--git-dir", inspector.gitDir}, arguments...)
	} else {
		command.Dir = inspector.worktree
	}
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func (inspector inspector) diffArgs(options ...string) []string {
	arguments := append([]string{"diff"}, options...)
	if inspector.reverse {
		arguments = append(arguments, "-R")
	}
	arguments = append(arguments, inspector.base)
	if inspector.head != "" {
		arguments = append(arguments, inspector.head)
	}
	return append(arguments, "--")
}

func (inspector inspector) gitOutputLimited(ctx context.Context, limit int, arguments ...string) (string, bool, error) {
	output, err := inspector.gitOutput(ctx, arguments...)
	if err != nil {
		return "", false, err
	}
	if len(output) <= limit {
		return output, false, nil
	}
	return output[:limit], true, nil
}

func splitDiff(diff string) map[string]string {
	sections := make(map[string]string)
	for _, part := range strings.Split(diff, "diff --git ") {
		if part == "" {
			continue
		}
		section := "diff --git " + part
		line, _, _ := strings.Cut(section, "\n")
		fields := strings.Fields(line)
		if len(fields) >= 4 {
			sections[strings.TrimPrefix(fields[3], "b/")] = section
		}
	}
	return sections
}

func withoutIgnoredFiles(files []protocol.WorkspaceFileDiff, ignoredPaths []string) []protocol.WorkspaceFileDiff {
	ignored := make(map[string]struct{}, len(ignoredPaths))
	for _, path := range ignoredPaths {
		clean := filepath.ToSlash(filepath.Clean(path))
		if clean != "." && !unsafeRelative(clean) {
			ignored[clean] = struct{}{}
		}
	}
	if len(ignored) == 0 {
		return files
	}
	filtered := files[:0]
	for _, file := range files {
		_, pathIgnored := ignored[file.Path]
		_, oldPathIgnored := ignored[file.OldPath]
		if !pathIgnored && !oldPathIgnored {
			filtered = append(filtered, file)
		}
	}
	return filtered
}

func selectedFileDiffs(files []protocol.WorkspaceFileDiff, path string) []protocol.WorkspaceFileDiff {
	selected := make([]protocol.WorkspaceFileDiff, 0, 1)
	for _, file := range files {
		if file.Path == path || file.OldPath == path {
			selected = append(selected, file)
		}
	}
	return selected
}

func lastDiffPath(diff string) string {
	index := strings.LastIndex(diff, "diff --git ")
	if index < 0 {
		return ""
	}
	line, _, _ := strings.Cut(diff[index:], "\n")
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return ""
	}
	return strings.TrimPrefix(fields[3], "b/")
}

func fileIndex(files []protocol.WorkspaceFileDiff, path string) int {
	for index := range files {
		if files[index].Path == path {
			return index
		}
	}
	return -1
}

type untrackedFileContents struct {
	preview          string
	size             int64
	lineCount        int
	binary           bool
	previewTruncated bool
	gitMode          string
}

func inspectUntrackedFile(ctx context.Context, path string, previewLimit int) (untrackedFileContents, error) {
	file, err := os.Open(path)
	if err != nil {
		return untrackedFileContents{}, err
	}
	defer file.Close()

	contents := untrackedFileContents{gitMode: "100644"}
	preview := make([]byte, 0, previewLimit)
	buffer := make([]byte, 32*1024)
	sawData := false
	lastByte := byte(0)
	for {
		if err := ctx.Err(); err != nil {
			return untrackedFileContents{}, err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			chunk := buffer[:read]
			contents.size += int64(read)
			for _, value := range chunk {
				if value == 0 {
					contents.binary = true
					return contents, nil
				}
				if value == '\n' {
					contents.lineCount++
				}
			}
			sawData = true
			lastByte = chunk[len(chunk)-1]
			if remaining := previewLimit - len(preview); remaining > 0 {
				if remaining > len(chunk) {
					remaining = len(chunk)
				}
				preview = append(preview, chunk[:remaining]...)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return untrackedFileContents{}, readErr
		}
	}
	if sawData && lastByte != '\n' {
		contents.lineCount++
	}
	contents.preview = strings.ToValidUTF8(string(preview), "\uFFFD")
	contents.previewTruncated = contents.size > int64(len(preview))
	return contents, nil
}

func inspectUntrackedSymlink(path string, previewLimit int) (untrackedFileContents, error) {
	target, err := os.Readlink(path)
	if err != nil {
		return untrackedFileContents{}, err
	}
	contents := untrackedFileContents{
		size:      int64(len(target)),
		lineCount: strings.Count(target, "\n"),
		gitMode:   "120000",
	}
	if target != "" && !strings.HasSuffix(target, "\n") {
		contents.lineCount++
	}
	previewSize := len(target)
	if previewSize > previewLimit {
		previewSize = previewLimit
		contents.previewTruncated = true
	}
	contents.preview = strings.ToValidUTF8(target[:previewSize], "\uFFFD")
	return contents, nil
}

func renderUntrackedFileDiff(path string, contents untrackedFileContents, reverse bool, limit int) (string, bool) {
	header := untrackedFileDiffHeader(path, contents.lineCount, contents.gitMode, reverse)
	if len(header) > limit {
		return "", true
	}

	var builder strings.Builder
	builder.Grow(limit)
	builder.WriteString(header)
	marker := "+"
	if reverse {
		marker = "-"
	}
	truncated := contents.previewTruncated
	for _, line := range strings.SplitAfter(contents.preview, "\n") {
		if line == "" {
			continue
		}
		if builder.Len()+len(marker)+len(line) > limit {
			truncated = true
			remaining := limit - builder.Len() - len(marker)
			if remaining > 0 {
				builder.WriteString(marker)
				builder.WriteString(line[:remaining])
			}
			break
		}
		builder.WriteString(marker)
		builder.WriteString(line)
	}
	return builder.String(), truncated
}

func untrackedFileDiffHeader(path string, lineCount int, gitMode string, reverse bool) string {
	var builder strings.Builder
	builder.WriteString("diff --git a/")
	builder.WriteString(path)
	builder.WriteString(" b/")
	builder.WriteString(path)
	if reverse {
		builder.WriteString("\ndeleted file mode ")
		builder.WriteString(gitMode)
		builder.WriteString("\n--- a/")
		builder.WriteString(path)
		builder.WriteString("\n+++ /dev/null")
		builder.WriteString(fmt.Sprintf("\n@@ -1,%d +0,0 @@\n", lineCount))
	} else {
		builder.WriteString("\nnew file mode ")
		builder.WriteString(gitMode)
		builder.WriteString("\n--- /dev/null\n+++ b/")
		builder.WriteString(path)
		builder.WriteString(fmt.Sprintf("\n@@ -0,0 +1,%d @@\n", lineCount))
	}
	return builder.String()
}

func untrackedFileDiffSize(path string, contents untrackedFileContents, reverse bool) int64 {
	return int64(len(untrackedFileDiffHeader(path, contents.lineCount, contents.gitMode, reverse))) + contents.size + int64(contents.lineCount)
}

func applyDiffBudget(files []protocol.WorkspaceFileDiff, budget int) bool {
	truncated := false
	for index := range files {
		if files[index].Diff == "" {
			continue
		}
		if budget <= 0 {
			files[index].Diff = ""
			files[index].DiffTruncated = true
			truncated = true
			continue
		}
		if len(files[index].Diff) > budget {
			files[index].Diff = files[index].Diff[:budget]
			files[index].DiffTruncated = true
			truncated = true
			budget = 0
			continue
		}
		budget -= len(files[index].Diff)
	}
	return truncated
}

func unsafeRelative(path string) bool {
	clean := filepath.Clean(path)
	return clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func (manager *Manager) updateMirror(ctx context.Context, mirror, sessionID, repositoryURL string) error {
	_, err := os.Stat(mirror)
	if errors.Is(err, os.ErrNotExist) {
		temporary := mirror + ".tmp-" + sessionID
		if !manager.contains(temporary) {
			return errors.New("temporary path escapes root")
		}
		if err := os.RemoveAll(temporary); err != nil {
			return fmt.Errorf("remove temporary repository: %w", err)
		}
		if _, err := os.Lstat(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect temporary mirror path %q: %w", temporary, err)
		}
		if err := manager.git(ctx, "init", "--bare", temporary); err != nil {
			_ = os.RemoveAll(temporary)
			return fmt.Errorf("initialize repository: %w", err)
		}
		if err := manager.git(ctx, "--git-dir", temporary, "remote", "add", "cache", repositoryURL); err != nil {
			_ = os.RemoveAll(temporary)
			return fmt.Errorf("configure repository cache remote: %w", err)
		}
		if err := manager.ensureCacheRemote(ctx, temporary, repositoryURL); err != nil {
			_ = os.RemoveAll(temporary)
			return err
		}
		if err := manager.configureCacheFetch(ctx, temporary); err != nil {
			_ = os.RemoveAll(temporary)
			return err
		}
		if err := os.Rename(temporary, mirror); err != nil {
			_ = os.RemoveAll(temporary)
			return fmt.Errorf("install repository mirror %q: %w", mirror, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect repository mirror %q: %w", mirror, err)
	}
	if err := manager.ensureCacheRemote(ctx, mirror, repositoryURL); err != nil {
		return err
	}
	if err := manager.configureCacheFetch(ctx, mirror); err != nil {
		return err
	}
	return nil
}

func (manager *Manager) ensureCacheRemote(ctx context.Context, repoPath, repositoryURL string) error {
	cacheURLs, err := manager.configuredRemoteURLs(ctx, repoPath, "cache")
	if err == nil {
		if len(cacheURLs) == 1 && cacheURLs[0] == repositoryURL {
			return manager.removeRemoteIfPresent(ctx, repoPath, "origin")
		}
		if compatibleLoopbackCacheRemoteURLs(cacheURLs, repositoryURL) {
			if err := manager.git(ctx, "--git-dir", repoPath, "config", "--replace-all", "remote.cache.url", repositoryURL); err != nil {
				return fmt.Errorf("repair repository cache remote: %w", err)
			}
			return manager.removeRemoteIfPresent(ctx, repoPath, "origin")
		}
		return errors.New("repository mirror cache remote mismatch")
	}
	originURLs, originErr := manager.configuredRemoteURLs(ctx, repoPath, "origin")
	if originErr != nil {
		return fmt.Errorf("read repository cache remote: %w", err)
	}
	repairOrigin := false
	if len(originURLs) == 1 && originURLs[0] == repositoryURL {
		repairOrigin = false
	} else if compatibleLoopbackCacheRemoteURLs(originURLs, repositoryURL) {
		repairOrigin = true
	} else {
		return errors.New("repository mirror cache remote mismatch")
	}
	if err := manager.git(ctx, "--git-dir", repoPath, "remote", "rename", "origin", "cache"); err != nil {
		return fmt.Errorf("rename repository origin remote: %w", err)
	}
	if repairOrigin {
		if err := manager.git(ctx, "--git-dir", repoPath, "config", "--replace-all", "remote.cache.url", repositoryURL); err != nil {
			return fmt.Errorf("repair repository cache remote: %w", err)
		}
	}
	return nil
}

func (manager *Manager) configuredRemoteURLs(ctx context.Context, repoPath, name string) ([]string, error) {
	output, err := manager.gitOutput(ctx, "--git-dir", repoPath, "config", "--get-all", "remote."+name+".url")
	if err != nil {
		return nil, err
	}
	values := make([]string, 0)
	for _, line := range strings.Split(output, "\n") {
		value := strings.TrimSpace(line)
		if value != "" {
			values = append(values, value)
		}
	}
	return values, nil
}

func compatibleLoopbackCacheRemoteURLs(existing []string, replacement string) bool {
	if len(existing) == 0 {
		return false
	}
	replacementPath, ok := parseLoopbackCacheRemotePath(replacement)
	if !ok {
		return false
	}
	for _, value := range existing {
		existingPath, ok := parseLoopbackCacheRemotePath(value)
		if !ok || existingPath != replacementPath {
			return false
		}
	}
	return true
}

func parseLoopbackCacheRemotePath(value string) (string, bool) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "ssh" || parsed.Hostname() == "" || parsed.Port() == "" ||
		parsed.Path == "" || parsed.Path == "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	if parsed.User == nil || parsed.User.Username() != "git" {
		return "", false
	}
	if _, hasPassword := parsed.User.Password(); hasPassword {
		return "", false
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return "", false
	}
	return parsed.Path, true
}

func (manager *Manager) removeRemoteIfPresent(ctx context.Context, repoPath, name string) error {
	if err := manager.git(ctx, "--git-dir", repoPath, "remote", "remove", name); err != nil {
		if _, readErr := manager.gitOutput(ctx, "--git-dir", repoPath, "remote", "get-url", name); readErr == nil {
			return fmt.Errorf("remove repository %s remote: %w", name, err)
		}
	}
	return nil
}

func (manager *Manager) configureCacheFetch(ctx context.Context, repoPath string) error {
	if err := manager.git(ctx, "--git-dir", repoPath, "config", "--replace-all", "remote.cache.fetch", "+refs/heads/*:refs/remotes/cache/*"); err != nil {
		return fmt.Errorf("configure repository fetch refspec: %w", err)
	}
	if err := manager.git(ctx, "--git-dir", repoPath, "config", "--unset-all", "remote.cache.mirror"); err != nil {
		if _, readErr := manager.gitOutput(ctx, "--git-dir", repoPath, "config", "--get", "remote.cache.mirror"); readErr == nil {
			return fmt.Errorf("disable repository mirror mode: %w", err)
		}
	}
	return nil
}

func (manager *Manager) refreshCacheRefs(ctx context.Context, repoPath string, gitSSH GitSSHOptions, useGitSSH bool) error {
	arguments := []string{"--git-dir", repoPath}
	if useGitSSH {
		if command := gitSSHCommand(gitSSH); command != "" {
			arguments = append(arguments, "-c", "core.sshCommand="+command)
		}
	}
	arguments = append(arguments, "fetch", "--prune", "--no-tags", "cache")
	if err := manager.git(ctx, arguments...); err != nil {
		return fmt.Errorf("fetch repository cache refs: %w", err)
	}
	return nil
}

func (manager *Manager) configureReviewWorktree(ctx context.Context, worktree, sessionID, projectSlug, repositoryURL string) error {
	if err := manager.git(ctx, "-C", worktree, "config", "extensions.worktreeConfig", "true"); err != nil {
		return fmt.Errorf("enable review worktree config: %w", err)
	}
	configs := [][2]string{
		{"core.bare", "false"},
		{"core.worktree", worktree},
		{"remote.origin.url", repositoryURL},
		{"holark.sessionId", sessionID},
		{"holark.projectSlug", projectSlug},
	}
	for _, config := range configs {
		if err := manager.git(ctx, "-C", worktree, "config", "--worktree", config[0], config[1]); err != nil {
			return fmt.Errorf("configure review worktree %s: %w", config[0], err)
		}
	}
	return nil
}

func (manager *Manager) configureWorktree(ctx context.Context, worktree, sessionID, projectSlug, repositoryURL, holarkGitRemoteURL string, gitSSH GitSSHOptions) error {
	if err := manager.git(ctx, "-C", worktree, "config", "extensions.worktreeConfig", "true"); err != nil {
		return fmt.Errorf("enable worktree config: %w", err)
	}
	// Store session remotes and Holark metadata in config.worktree so the
	// shared bare cache can keep using its own fetch remote.
	configs := [][2]string{
		{"core.bare", "false"},
		{"core.worktree", worktree},
		{"remote.origin.url", repositoryURL},
		{"remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
		{"holark.sessionId", sessionID},
		{"holark.projectSlug", projectSlug},
		{"holark.autopushRef", "refs/holark/sessions/" + sessionID},
		{"holark.remote", "holark"},
		{"holark.autoPush", "true"},
	}
	if holarkGitRemoteURL != "" {
		configs = append(configs, [2]string{"remote.holark.url", holarkGitRemoteURL})
	}
	if gitSSH.KeyPath != "" {
		configs = append(configs, [2]string{"holark.sshKeyPath", gitSSH.KeyPath})
	}
	if gitSSH.KnownHostsPath != "" {
		configs = append(configs, [2]string{"holark.knownHostsPath", gitSSH.KnownHostsPath})
	}
	gitDirPath, err := manager.worktreeGitDir(ctx, worktree)
	if err != nil {
		return err
	}
	hooksPath := filepath.Join(gitDirPath, "hooks")
	configs = append(configs, [2]string{"core.hooksPath", hooksPath})
	for _, config := range configs {
		if err := manager.git(ctx, "-C", worktree, "config", "--worktree", config[0], config[1]); err != nil {
			return fmt.Errorf("configure worktree %s: %w", config[0], err)
		}
	}
	if holarkGitRemoteURL != "" {
		if err := manager.git(ctx, "-C", worktree, "config", "--worktree", "remote.holark.fetch", "+refs/heads/*:refs/remotes/holark/*"); err != nil {
			return fmt.Errorf("configure worktree holark fetch refspec: %w", err)
		}
	}
	if err := os.MkdirAll(hooksPath, 0o700); err != nil {
		return fmt.Errorf("create worktree hooks directory %q: %w", hooksPath, err)
	}
	hookPath := filepath.Join(hooksPath, "post-commit")
	if err := os.WriteFile(hookPath, []byte(postCommitHook), 0o700); err != nil {
		return fmt.Errorf("install post-commit hook %q: %w", hookPath, err)
	}
	return nil
}

const postCommitHook = `#!/bin/sh
auto_push=$(git config --bool holark.autoPush 2>/dev/null || true)
if [ "$auto_push" != "true" ]; then
	exit 0
fi

remote=$(git config holark.remote 2>/dev/null || true)
ref=$(git config holark.autopushRef 2>/dev/null || true)
key_path=$(git config holark.sshKeyPath 2>/dev/null || true)
known_hosts_path=$(git config holark.knownHostsPath 2>/dev/null || true)
if [ -z "$remote" ] || [ -z "$ref" ] || [ -z "$key_path" ] || [ -z "$known_hosts_path" ]; then
	exit 0
fi

git_dir=$(git rev-parse --git-dir 2>/dev/null) || exit 0
log="$git_dir/holark-autopush.log"
GIT_TERMINAL_PROMPT=0 git -c core.sshCommand="ssh -i \"$key_path\" -o BatchMode=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=\"$known_hosts_path\"" push "$remote" HEAD:"$ref" >>"$log" 2>&1
`

func firstGitSSHOptions(options []GitSSHOptions) GitSSHOptions {
	if len(options) == 0 {
		return GitSSHOptions{}
	}
	return options[0]
}

func gitSSHCommand(options GitSSHOptions) string {
	if options.KeyPath == "" || options.KnownHostsPath == "" {
		return ""
	}
	return fmt.Sprintf("ssh -i %q -o BatchMode=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=%q", options.KeyPath, options.KnownHostsPath)
}

func (manager *Manager) contains(path string) bool {
	relative, err := filepath.Rel(manager.root, filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (manager *Manager) isSessionWorktreePath(path string) bool {
	relative, err := filepath.Rel(manager.root, filepath.Clean(path))
	if err != nil {
		return false
	}
	parts := strings.Split(relative, string(filepath.Separator))
	return len(parts) == 3 && parts[0] == "sessions" && validID(parts[1]) && parts[2] == "repo"
}

func (manager *Manager) git(ctx context.Context, arguments ...string) error {
	_, err := manager.gitOutput(ctx, arguments...)
	return err
}

func (manager *Manager) gitOutput(ctx context.Context, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", gitCommandError{arguments: arguments, err: err, output: string(output)}
	}
	return string(output), nil
}

func (manager *Manager) refExists(ctx context.Context, repoPath, ref string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "show-ref", "--verify", "--quiet", ref)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, gitCommandError{arguments: command.Args[1:], err: err, output: string(output)}
}

func (manager *Manager) sessionBranchReserved(ctx context.Context, repoPath, branch string) (bool, error) {
	for _, ref := range []string{"refs/heads/" + branch, "refs/remotes/cache/" + branch} {
		exists, err := manager.refExists(ctx, repoPath, ref)
		if err != nil {
			return false, fmt.Errorf("inspect %s: %w", ref, err)
		}
		if exists {
			return true, nil
		}
	}
	return false, nil
}

func (manager *Manager) providerBranchReserved(ctx context.Context, repoPath, branch string) (bool, error) {
	for _, ref := range []string{"refs/heads/" + branch, "refs/remotes/cache/" + branch} {
		exists, err := manager.refExists(ctx, repoPath, ref)
		if err != nil {
			return false, fmt.Errorf("inspect %s: %w", ref, err)
		}
		if exists {
			return true, nil
		}
	}
	return false, nil
}

type gitCommandError struct {
	arguments []string
	err       error
	output    string
}

func (err gitCommandError) Error() string {
	output := sanitizeGitOutput(err.output)
	if output == "" {
		return fmt.Sprintf("git %s: %v", sanitizedGitArguments(err.arguments), err.err)
	}
	return fmt.Sprintf("git %s: %v: %s", sanitizedGitArguments(err.arguments), err.err, output)
}

func sanitizeGitOutput(output string) string {
	output = strings.ReplaceAll(output, "\x00", "")
	output = strings.TrimSpace(output)
	output = urlPattern.ReplaceAllString(output, "<url>")
	if len(output) > 2048 {
		output = output[:2048] + "...(truncated)"
	}
	return output
}

var urlPattern = regexp.MustCompile(`https?://[^\s]+`)

func sanitizedGitArguments(arguments []string) string {
	sanitized := make([]string, len(arguments))
	copy(sanitized, arguments)
	for index, argument := range sanitized {
		if strings.Contains(argument, "://") {
			sanitized[index] = "<url>"
		}
		if index >= 3 && sanitized[index-3] == "remote" && sanitized[index-2] == "add" && sanitized[index-1] == "origin" {
			sanitized[index] = "<repository-url>"
		}
	}
	return strings.Join(sanitized, " ")
}

func validID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-') {
			return false
		}
	}
	return true
}
