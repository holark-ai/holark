package repositorybrowser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/workspace"
)

const MaxBlobBytes = 256 * 1024
const originBranchFetchRefspec = "+refs/heads/*:refs/heads/*"
const originPullRequestFetchRefspec = "+refs/pull/*/head:refs/pull/*/head"
const previousOriginFetchRefspec = "+refs/heads/*:refs/remotes/origin/*"

var originFetchRefspecs = []string{originBranchFetchRefspec, originPullRequestFetchRefspec}

const (
	branchRefPrefix               = "refs/heads/"
	originTrackingBranchRefPrefix = "refs/remotes/origin/"
	sessionRefPrefix              = "refs/holark/sessions/"
	rebaseBackupRefPrefix         = "refs/holark/rebase-backups/"
	rebaseBackupBranchMarker      = ".backup.rebase."
)

var (
	ErrRepositoryUnavailable = errors.New("repository unavailable")
	ErrOriginMismatch        = errors.New("repository origin mismatch")
	ErrRefNotFound           = errors.New("ref not found")
	ErrPathNotFound          = errors.New("path not found")
	ErrPathNotDirectory      = errors.New("path not directory")
	ErrPathNotFile           = errors.New("path not file")
	ErrFileTooLarge          = errors.New("file too large")
	ErrBinaryFile            = errors.New("binary file")
	ErrInvalidPath           = errors.New("invalid path")
	ErrInvalidRepository     = errors.New("invalid repository")
	ErrStaleHead             = errors.New("stale head commit")
	ErrNotFastForward        = errors.New("not fast-forwardable")
	ErrPushRejected          = errors.New("push rejected")
	ErrRebaseConflicts       = errors.New("rebase conflicts")
)

type Repository struct {
	// GitDirectory supplies the user-configured fetch and push transports.
	GitDirectory  string
	ID            string
	RepositoryURL string
	DefaultBranch string
}

type Manager struct {
	root    string
	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	timeout time.Duration
}

type Tree struct {
	RepositoryID string      `json:"repository_id"`
	Ref          string      `json:"ref"`
	Path         string      `json:"path"`
	Entries      []TreeEntry `json:"entries"`
}

type TreeEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Mode     string `json:"mode"`
	Size     int64  `json:"size,omitempty"`
	Language string `json:"language,omitempty"`
}

type Blob struct {
	RepositoryID string `json:"repository_id"`
	Ref          string `json:"ref"`
	Path         string `json:"path"`
	Language     string `json:"language"`
	Content      string `json:"content"`
	Encoding     string `json:"encoding"`
	Size         int64  `json:"size"`
	Truncated    bool   `json:"truncated"`
}

type Commit struct {
	SHA         string    `json:"sha"`
	Message     string    `json:"message"`
	AuthorName  string    `json:"author_name,omitempty"`
	AuthorEmail string    `json:"author_email,omitempty"`
	AuthoredAt  time.Time `json:"authored_at"`
}

type Ref struct {
	Name        string    `json:"name"`
	ShortName   string    `json:"short_name"`
	Kind        string    `json:"kind"`
	Target      string    `json:"target"`
	CommittedAt time.Time `json:"committed_at"`
}

type FastForwardMergeRequest struct {
	BaseBranch         string
	HeadRef            string
	ExpectedHeadCommit string
}

type SquashMergeRequest struct {
	BaseBranch         string
	HeadRef            string
	HeadCommit         string
	ExpectedHeadCommit string
	CommitTitle        string
	CommitBody         string
}

type RebaseRequest struct {
	BaseCommit         string
	BaseBranch         string
	HeadBranch         string
	ExpectedHeadCommit string
}

type RebaseBackupRequest struct {
	HeadBranch         string
	ExpectedHeadCommit string
}

type RebaseBackupResult struct {
	Branch     string
	HeadCommit string
}

type MergeResult struct {
	MergedCommit string `json:"merged_commit"`
}

type RebaseResult struct {
	HeadCommit string `json:"head_commit"`
	Rebased    bool   `json:"rebased"`
}

type RebasePreviewResult struct {
	UpToDate         bool
	Conflicts        bool
	BaseCommitsAhead int
}

func NewManager(root string) (*Manager, error) {
	if root == "" {
		return nil, errors.New("repository root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, errors.New("invalid repository root")
	}
	return &Manager{root: filepath.Clean(absolute), locks: make(map[string]*sync.Mutex), timeout: 30 * time.Second}, nil
}

func (manager *Manager) Ensure(ctx context.Context, repository Repository) error {
	if err := validateRepository(repository); err != nil {
		return err
	}
	lock := manager.repositoryLock(repository.ID)
	lock.Lock()
	defer lock.Unlock()
	return manager.ensureLocked(ctx, repository, true)
}

func (manager *Manager) ensureLocked(ctx context.Context, repository Repository, fetch bool) error {
	path := manager.repositoryPath(repository.ID)
	if !manager.contains(path) {
		return ErrInvalidRepository
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("%w: create repository directory", ErrRepositoryUnavailable)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		temporary := path + ".tmp"
		if !manager.contains(temporary) {
			return ErrInvalidRepository
		}
		_ = os.RemoveAll(temporary)
		if err := manager.git(ctx, "", "init", "--bare", temporary); err != nil {
			_ = os.RemoveAll(temporary)
			return fmt.Errorf("%w: initialize repository cache", ErrRepositoryUnavailable)
		}
		if err := manager.git(ctx, temporary, "remote", "add", "origin", repository.RepositoryURL); err != nil {
			_ = os.RemoveAll(temporary)
			return fmt.Errorf("%w: configure repository origin", ErrRepositoryUnavailable)
		}
		if err := manager.verifyOrigin(ctx, temporary, repository.RepositoryURL); err != nil {
			_ = os.RemoveAll(temporary)
			return err
		}
		if err := manager.configureOriginFetch(ctx, temporary); err != nil {
			_ = os.RemoveAll(temporary)
			return err
		}
		if fetch {
			if err := manager.fetchOrigin(ctx, temporary); err != nil {
				_ = os.RemoveAll(temporary)
				return err
			}
		}
		if err := os.Rename(temporary, path); err != nil {
			_ = os.RemoveAll(temporary)
			return fmt.Errorf("%w: install repository cache", ErrRepositoryUnavailable)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("%w: inspect repository cache", ErrRepositoryUnavailable)
	}
	if err := manager.verifyOrigin(ctx, path, repository.RepositoryURL); err != nil {
		return err
	}
	migrateHeads := manager.needsBranchNamespaceMigration(ctx, path)
	if err := manager.configureOriginFetch(ctx, path); err != nil {
		return err
	}
	if migrateHeads {
		return manager.copyOriginTrackingBranchesToLocalHeads(ctx, path)
	}
	return nil
}

func (manager *Manager) Refresh(ctx context.Context, repository Repository) error {
	if err := manager.Ensure(ctx, repository); err != nil {
		return err
	}
	lock := manager.repositoryLock(repository.ID)
	lock.Lock()
	defer lock.Unlock()
	return manager.fetchOrigin(ctx, manager.repositoryPath(repository.ID))
}

// RefreshRefs updates provider refs in the server mirror and returns the
// complete locally visible ref list, including Holark session refs.
func (manager *Manager) RefreshRefs(ctx context.Context, repository Repository) ([]Ref, error) {
	if err := manager.Refresh(ctx, repository); err != nil {
		return nil, err
	}
	return manager.Refs(ctx, repository)
}

// PrepareBranch updates only branch in the server mirror and returns the
// resolved commit after all objects reachable from it have been transferred.
func (manager *Manager) PrepareBranch(ctx context.Context, repository Repository, branch string) (string, error) {
	if err := validateRepository(repository); err != nil {
		return "", err
	}
	if !validBranchName(branch) || manager.git(ctx, "", "check-ref-format", branchRefPrefix+branch) != nil {
		return "", ErrInvalidRepository
	}
	lock := manager.repositoryLock(repository.ID)
	lock.Lock()
	defer lock.Unlock()
	if err := manager.ensureLocked(ctx, repository, false); err != nil {
		return "", err
	}
	repoPath := manager.repositoryPath(repository.ID)
	ref := branchRefPrefix + branch
	if err := manager.git(ctx, repoPath, "-c", "remote.origin.mirror=false", "fetch", "--no-tags", "origin", "+"+ref+":"+ref); err != nil {
		return "", fmt.Errorf("%w: fetch repository branch", ErrRepositoryUnavailable)
	}
	return manager.resolveCommit(ctx, repoPath, branch)
}

func (manager *Manager) Tree(ctx context.Context, repository Repository, ref, requestedPath string) (Tree, error) {
	if err := validateRepository(repository); err != nil {
		return Tree{}, err
	}
	ref, err := manager.providerBranch(ctx, repository, ref)
	if err != nil {
		return Tree{}, err
	}
	cleanPath, err := cleanTreePath(requestedPath)
	if err != nil {
		return Tree{}, err
	}
	repoPath := manager.repositoryPath(repository.ID)
	if !manager.repositoryAvailable(repoPath) {
		return Tree{}, ErrRepositoryUnavailable
	}
	commit, err := manager.resolveCommit(ctx, repoPath, ref)
	if err != nil {
		return Tree{}, err
	}
	if cleanPath != "" {
		objectType, err := manager.objectType(ctx, repoPath, commit, cleanPath)
		if err != nil {
			return Tree{}, err
		}
		if objectType != "tree" {
			return Tree{}, ErrPathNotDirectory
		}
	}
	treeish := commit
	if cleanPath != "" {
		treeish = commit + ":" + cleanPath
	}
	output, err := manager.gitOutput(ctx, repoPath, "ls-tree", "-z", "-l", treeish)
	if err != nil {
		return Tree{}, ErrRepositoryUnavailable
	}
	entries, err := parseTreeEntries(cleanPath, output)
	if err != nil {
		return Tree{}, ErrRepositoryUnavailable
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type == "directory"
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return Tree{RepositoryID: repository.ID, Ref: ref, Path: cleanPath, Entries: entries}, nil
}

func (manager *Manager) Blob(ctx context.Context, repository Repository, ref, requestedPath string) (Blob, error) {
	if err := validateRepository(repository); err != nil {
		return Blob{}, err
	}
	ref, err := manager.providerBranch(ctx, repository, ref)
	if err != nil {
		return Blob{}, err
	}
	cleanPath, err := cleanBlobPath(requestedPath)
	if err != nil {
		return Blob{}, err
	}
	repoPath := manager.repositoryPath(repository.ID)
	if !manager.repositoryAvailable(repoPath) {
		return Blob{}, ErrRepositoryUnavailable
	}
	commit, err := manager.resolveCommit(ctx, repoPath, ref)
	if err != nil {
		return Blob{}, err
	}
	objectType, err := manager.objectType(ctx, repoPath, commit, cleanPath)
	if err != nil {
		return Blob{}, err
	}
	if objectType != "blob" {
		return Blob{}, ErrPathNotFile
	}
	sizeOutput, err := manager.gitOutput(ctx, repoPath, "cat-file", "-s", commit+":"+cleanPath)
	if err != nil {
		return Blob{}, ErrRepositoryUnavailable
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeOutput)), 10, 64)
	if err != nil {
		return Blob{}, ErrRepositoryUnavailable
	}
	if size > MaxBlobBytes {
		return Blob{}, ErrFileTooLarge
	}
	content, err := manager.gitOutput(ctx, repoPath, "cat-file", "blob", commit+":"+cleanPath)
	if err != nil {
		return Blob{}, ErrRepositoryUnavailable
	}
	if bytes.Contains(content, []byte{0}) || !utf8.Valid(content) {
		return Blob{}, ErrBinaryFile
	}
	return Blob{
		RepositoryID: repository.ID,
		Ref:          ref,
		Path:         cleanPath,
		Language:     languageForPath(cleanPath),
		Content:      string(content),
		Encoding:     "utf-8",
		Size:         size,
		Truncated:    false,
	}, nil
}

func (manager *Manager) providerBranch(ctx context.Context, repository Repository, branch string) (string, error) {
	if branch == "" {
		branch = repository.DefaultBranch
	}
	if !validBranchName(branch) || manager.git(ctx, "", "check-ref-format", branchRefPrefix+branch) != nil {
		return "", ErrInvalidRepository
	}
	return branch, nil
}

func (manager *Manager) MergeBase(ctx context.Context, repository Repository, baseCommit, headCommit string) (string, error) {
	repoPath, baseCommit, headCommit, err := manager.reviewCommits(ctx, repository, baseCommit, headCommit)
	if err != nil {
		return "", err
	}
	output, err := manager.gitOutput(ctx, repoPath, "merge-base", baseCommit, headCommit)
	if err != nil {
		return "", ErrRefNotFound
	}
	return strings.TrimSpace(string(output)), nil
}

func (manager *Manager) Commits(ctx context.Context, repository Repository, baseCommit, headCommit string) ([]Commit, error) {
	repoPath, baseCommit, headCommit, err := manager.reviewCommits(ctx, repository, baseCommit, headCommit)
	if err != nil {
		return nil, err
	}
	output, err := manager.gitOutput(ctx, repoPath, "log", "--reverse", "--format=%H%x1f%an%x1f%ae%x1f%aI%x1f%B%x1e", baseCommit+".."+headCommit)
	if err != nil {
		return nil, ErrRepositoryUnavailable
	}
	return parseCommits(output)
}

func (manager *Manager) Changes(ctx context.Context, repository Repository, branch, baseBranch, baseCommit, headCommit string) (protocol.WorkspaceInspected, error) {
	repoPath, baseCommit, headCommit, err := manager.reviewCommits(ctx, repository, baseCommit, headCommit)
	if err != nil {
		return protocol.WorkspaceInspected{}, err
	}
	return workspace.InspectRange(ctx, repoPath, branch, baseBranch, baseCommit, headCommit)
}

func (manager *Manager) Refs(ctx context.Context, repository Repository) ([]Ref, error) {
	if err := validateRepository(repository); err != nil {
		return nil, err
	}
	repoPath := manager.repositoryPath(repository.ID)
	if !manager.repositoryAvailable(repoPath) {
		return nil, ErrRepositoryUnavailable
	}
	output, err := manager.gitOutput(ctx, repoPath, "for-each-ref", "--sort=refname", "--format=%(refname)%00%(objectname)%00%(committerdate:iso8601-strict)", "refs/heads", "refs/holark/sessions")
	if err != nil {
		return nil, ErrRepositoryUnavailable
	}
	refs, err := parseRefs(output)
	if err != nil {
		return nil, err
	}
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].Name < refs[j].Name
	})
	return refs, nil
}

func (manager *Manager) CommitHistory(ctx context.Context, repository Repository, ref string, limit int) ([]Commit, error) {
	if err := validateRepository(repository); err != nil {
		return nil, err
	}
	if ref == "" {
		ref = branchRefPrefix + repository.DefaultBranch
	}
	if !allowedHistoryRef(ref) {
		return nil, ErrRefNotFound
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	repoPath := manager.repositoryPath(repository.ID)
	if !manager.repositoryAvailable(repoPath) {
		return nil, ErrRepositoryUnavailable
	}
	if _, err := manager.ResolveRef(ctx, repository, ref); err != nil {
		return nil, err
	}
	output, err := manager.gitOutput(ctx, repoPath, "log", "--max-count="+strconv.Itoa(limit), "--format=%H%x1f%an%x1f%ae%x1f%aI%x1f%B%x1e", ref)
	if err != nil {
		return nil, ErrRepositoryUnavailable
	}
	return parseCommits(output)
}

func (manager *Manager) ResolveRef(ctx context.Context, repository Repository, ref string) (string, error) {
	if err := validateRepository(repository); err != nil {
		return "", err
	}
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", ErrInvalidRepository
	}
	if err := manager.git(ctx, "", "check-ref-format", ref); err != nil {
		return "", ErrInvalidRepository
	}
	repoPath := manager.repositoryPath(repository.ID)
	if !manager.repositoryAvailable(repoPath) {
		return "", ErrRepositoryUnavailable
	}
	output, err := manager.gitOutput(ctx, repoPath, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", ErrRefNotFound
	}
	return strings.TrimSpace(string(output)), nil
}

func (manager *Manager) ExportBranch(ctx context.Context, repository Repository, sourceCommit, branch string) error {
	if err := validateRepository(repository); err != nil {
		return err
	}
	if !validCommitID(sourceCommit) || strings.HasPrefix(branch, "-") {
		return ErrInvalidRepository
	}
	destination := "refs/heads/" + branch
	if err := manager.git(ctx, "", "check-ref-format", destination); err != nil {
		return ErrInvalidRepository
	}
	lock := manager.repositoryLock(repository.ID)
	lock.Lock()
	defer lock.Unlock()

	repoPath := manager.repositoryPath(repository.ID)
	if !manager.repositoryAvailable(repoPath) {
		return ErrRepositoryUnavailable
	}
	resolved, err := manager.resolveReviewCommit(ctx, repoPath, sourceCommit)
	if err != nil {
		return err
	}
	if resolved != sourceCommit {
		return ErrRefNotFound
	}
	if output, err := manager.pushBranchToOrigin(ctx, repoPath, sourceCommit, destination); err != nil {
		if exportPushRejected(output) {
			return ErrPushRejected
		}
		return fmt.Errorf("%w: export branch", ErrRepositoryUnavailable)
	}
	return nil
}

func (manager *Manager) CreateRebaseBackup(ctx context.Context, repository Repository, request RebaseBackupRequest) (RebaseBackupResult, error) {
	if err := validateRepository(repository); err != nil {
		return RebaseBackupResult{}, err
	}
	headBranch := strings.TrimSpace(request.HeadBranch)
	if !validBranchName(headBranch) || !validCommitID(request.ExpectedHeadCommit) {
		return RebaseBackupResult{}, ErrInvalidRepository
	}
	headRef := branchRefPrefix + headBranch
	if err := manager.git(ctx, "", "check-ref-format", headRef); err != nil {
		return RebaseBackupResult{}, ErrInvalidRepository
	}

	lock := manager.repositoryLock(repository.ID)
	lock.Lock()
	defer lock.Unlock()

	repoPath := manager.repositoryPath(repository.ID)
	if !manager.repositoryAvailable(repoPath) {
		return RebaseBackupResult{}, ErrRepositoryUnavailable
	}
	headCommit, err := manager.resolveCommit(ctx, repoPath, headBranch)
	if err != nil {
		return RebaseBackupResult{}, err
	}
	if headCommit != request.ExpectedHeadCommit {
		return RebaseBackupResult{}, ErrStaleHead
	}
	return manager.createRebaseBackupLocked(ctx, repoPath, headBranch, headCommit)
}

func (manager *Manager) createRebaseBackupLocked(ctx context.Context, repoPath, headBranch, headCommit string) (RebaseBackupResult, error) {
	backupBranch, err := manager.nextRebaseBackupBranch(ctx, repoPath, headBranch)
	if err != nil {
		return RebaseBackupResult{}, err
	}
	// Keep backups outside fetched branch refs so fetch --prune preserves them.
	destination := rebaseBackupRefPrefix + backupBranch
	if err := manager.git(ctx, repoPath, "update-ref", destination, headCommit, ""); err != nil {
		return RebaseBackupResult{}, fmt.Errorf("%w: create local rebase backup", ErrRepositoryUnavailable)
	}
	return RebaseBackupResult{Branch: backupBranch, HeadCommit: headCommit}, nil
}

func (manager *Manager) Rebase(ctx context.Context, repository Repository, request RebaseRequest) (RebaseResult, error) {
	return manager.rebase(ctx, repository, request, false)
}

// RebasePinned reuses commits pinned by an authoritative repository refresh.
// Missing objects are fetched on demand, and the push lease still rejects a
// remote head that moved after the caller pinned it.
func (manager *Manager) RebasePinned(ctx context.Context, repository Repository, request RebaseRequest) (RebaseResult, error) {
	return manager.rebase(ctx, repository, request, true)
}

func (manager *Manager) rebase(ctx context.Context, repository Repository, request RebaseRequest, pinned bool) (RebaseResult, error) {
	if err := validateRepository(repository); err != nil {
		return RebaseResult{}, err
	}
	baseBranch := strings.TrimSpace(request.BaseBranch)
	if baseBranch == "" {
		baseBranch = repository.DefaultBranch
	}
	headBranch := strings.TrimSpace(request.HeadBranch)
	if !validBranchName(baseBranch) || !validBranchName(headBranch) || !validCommitID(request.ExpectedHeadCommit) {
		return RebaseResult{}, ErrInvalidRepository
	}
	headRef := branchRefPrefix + headBranch
	if err := manager.git(ctx, "", "check-ref-format", headRef); err != nil {
		return RebaseResult{}, ErrInvalidRepository
	}
	if err := manager.Ensure(ctx, repository); err != nil {
		return RebaseResult{}, err
	}

	lock := manager.repositoryLock(repository.ID)
	lock.Lock()
	defer lock.Unlock()

	repoPath := manager.repositoryPath(repository.ID)
	if !manager.repositoryAvailable(repoPath) {
		return RebaseResult{}, ErrRepositoryUnavailable
	}
	var baseCommit string
	var err error
	if pinned {
		baseCommit, err = manager.resolveReviewCommit(ctx, repoPath, strings.TrimSpace(request.BaseCommit))
		if err == nil {
			_, err = manager.resolveReviewCommit(ctx, repoPath, request.ExpectedHeadCommit)
		}
	}
	if !pinned || err != nil {
		if err = manager.fetchOrigin(ctx, repoPath); err != nil {
			return RebaseResult{}, err
		}
	}
	if baseCommit == "" && strings.TrimSpace(request.BaseCommit) != "" {
		baseCommit, err = manager.resolveReviewCommit(ctx, repoPath, strings.TrimSpace(request.BaseCommit))
	} else if baseCommit == "" {
		baseCommit, err = manager.resolveCommit(ctx, repoPath, baseBranch)
	}
	if err != nil {
		return RebaseResult{}, err
	}
	var headCommit string
	if pinned {
		headCommit, err = manager.resolveReviewCommit(ctx, repoPath, request.ExpectedHeadCommit)
	} else {
		headCommit, err = manager.resolveCommit(ctx, repoPath, headBranch)
	}
	if err != nil {
		return RebaseResult{}, err
	}
	if headCommit != request.ExpectedHeadCommit {
		return RebaseResult{}, ErrStaleHead
	}
	if _, err := manager.createRebaseBackupLocked(ctx, repoPath, headBranch, headCommit); err != nil {
		return RebaseResult{}, err
	}
	if err := manager.git(ctx, repoPath, "merge-base", "--is-ancestor", baseCommit, headCommit); err == nil {
		return RebaseResult{HeadCommit: headCommit, Rebased: false}, nil
	}

	rebasedCommit, conflicts, err := manager.rebaseDetached(ctx, repoPath, repository.ID, baseCommit, headCommit)
	if err != nil {
		return RebaseResult{}, err
	}
	if conflicts {
		return RebaseResult{}, ErrRebaseConflicts
	}
	if rebasedCommit == headCommit {
		return RebaseResult{HeadCommit: headCommit, Rebased: false}, nil
	}
	lease := "--force-with-lease=" + headRef + ":" + headCommit
	if err := manager.git(ctx, repoPath, "-c", "remote.origin.mirror=false", "push", lease, "origin", rebasedCommit+":"+headRef); err != nil {
		return RebaseResult{}, ErrPushRejected
	}
	if err := manager.git(ctx, repoPath, "update-ref", headRef, rebasedCommit); err != nil {
		return RebaseResult{}, fmt.Errorf("%w: update rebased branch", ErrRepositoryUnavailable)
	}
	return RebaseResult{HeadCommit: rebasedCommit, Rebased: true}, nil
}

// PreviewRebase checks the exact mechanical rebase without creating a backup,
// updating a branch, pushing, or retaining the detached worktree.
func (manager *Manager) PreviewRebase(ctx context.Context, repository Repository, request RebaseRequest) (RebasePreviewResult, error) {
	return manager.previewRebase(ctx, repository, request, false)
}

// PreviewRebasePinned reuses commits pinned by an authoritative repository
// refresh and only fetches when the isolated cache does not contain them.
func (manager *Manager) PreviewRebasePinned(ctx context.Context, repository Repository, request RebaseRequest) (RebasePreviewResult, error) {
	return manager.previewRebase(ctx, repository, request, true)
}

func (manager *Manager) previewRebase(ctx context.Context, repository Repository, request RebaseRequest, pinned bool) (RebasePreviewResult, error) {
	if err := validateRepository(repository); err != nil {
		return RebasePreviewResult{}, err
	}
	baseBranch := strings.TrimSpace(request.BaseBranch)
	if baseBranch == "" {
		baseBranch = repository.DefaultBranch
	}
	headBranch := strings.TrimSpace(request.HeadBranch)
	if !validBranchName(baseBranch) || !validBranchName(headBranch) || !validCommitID(request.ExpectedHeadCommit) || !validCommitID(strings.TrimSpace(request.BaseCommit)) {
		return RebasePreviewResult{}, ErrInvalidRepository
	}
	if err := manager.Ensure(ctx, repository); err != nil {
		return RebasePreviewResult{}, err
	}

	lock := manager.repositoryLock(repository.ID)
	lock.Lock()
	defer lock.Unlock()
	repoPath := manager.repositoryPath(repository.ID)
	if !manager.repositoryAvailable(repoPath) {
		return RebasePreviewResult{}, ErrRepositoryUnavailable
	}
	if pinned {
		_, baseErr := manager.resolveReviewCommit(ctx, repoPath, strings.TrimSpace(request.BaseCommit))
		_, headErr := manager.resolveReviewCommit(ctx, repoPath, request.ExpectedHeadCommit)
		if baseErr != nil || headErr != nil {
			if err := manager.fetchOrigin(ctx, repoPath); err != nil {
				return RebasePreviewResult{}, err
			}
		}
	} else if err := manager.fetchOrigin(ctx, repoPath); err != nil {
		return RebasePreviewResult{}, err
	}
	baseCommit, err := manager.resolveReviewCommit(ctx, repoPath, strings.TrimSpace(request.BaseCommit))
	if err != nil {
		return RebasePreviewResult{}, err
	}
	var headCommit string
	if pinned {
		headCommit, err = manager.resolveReviewCommit(ctx, repoPath, request.ExpectedHeadCommit)
	} else {
		headCommit, err = manager.resolveCommit(ctx, repoPath, headBranch)
	}
	if err != nil {
		return RebasePreviewResult{}, err
	}
	if headCommit != request.ExpectedHeadCommit {
		return RebasePreviewResult{}, ErrStaleHead
	}
	if err := manager.git(ctx, repoPath, "merge-base", "--is-ancestor", baseCommit, headCommit); err == nil {
		return RebasePreviewResult{UpToDate: true}, nil
	}
	countOutput, err := manager.gitOutput(ctx, repoPath, "rev-list", "--count", headCommit+".."+baseCommit)
	if err != nil {
		return RebasePreviewResult{}, err
	}
	baseCommitsAhead, err := strconv.Atoi(strings.TrimSpace(string(countOutput)))
	if err != nil {
		return RebasePreviewResult{}, fmt.Errorf("%w: count base commits", ErrRepositoryUnavailable)
	}
	_, conflicts, err := manager.rebaseDetached(ctx, repoPath, repository.ID, baseCommit, headCommit)
	if err != nil {
		return RebasePreviewResult{}, err
	}
	return RebasePreviewResult{Conflicts: conflicts, BaseCommitsAhead: baseCommitsAhead}, nil
}

func (manager *Manager) rebaseDetached(ctx context.Context, repoPath, repositoryID, baseCommit, headCommit string) (string, bool, error) {
	worktreePath := filepath.Join(manager.root, ".tmp", "rebase-"+repositoryID+"-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if !manager.contains(worktreePath) {
		return "", false, ErrInvalidRepository
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o700); err != nil {
		return "", false, fmt.Errorf("%w: create rebase worktree directory", ErrRepositoryUnavailable)
	}
	if err := manager.git(ctx, repoPath, "worktree", "add", "--detach", worktreePath, headCommit); err != nil {
		return "", false, fmt.Errorf("%w: create rebase worktree", ErrRepositoryUnavailable)
	}
	defer func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), manager.timeout)
		defer cancel()
		_ = manager.gitWorktree(cleanupContext, worktreePath, "rebase", "--abort")
		_ = manager.git(cleanupContext, repoPath, "worktree", "remove", "--force", worktreePath)
		_ = os.RemoveAll(worktreePath)
	}()

	if err := manager.gitWorktree(ctx, worktreePath, "rebase", baseCommit); err != nil {
		return headCommit, true, nil
	}
	output, err := manager.gitWorktreeOutput(ctx, worktreePath, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", false, ErrRepositoryUnavailable
	}
	rebasedCommit := strings.TrimSpace(string(output))
	if !validCommitID(rebasedCommit) {
		return "", false, ErrRepositoryUnavailable
	}
	return rebasedCommit, false, nil
}

func (manager *Manager) FastForwardMerge(ctx context.Context, repository Repository, request FastForwardMergeRequest) (MergeResult, error) {
	if err := validateRepository(repository); err != nil {
		return MergeResult{}, err
	}
	baseBranch := strings.TrimSpace(request.BaseBranch)
	if baseBranch == "" {
		baseBranch = repository.DefaultBranch
	}
	if !validBranchName(baseBranch) || !validCommitID(request.ExpectedHeadCommit) {
		return MergeResult{}, ErrInvalidRepository
	}
	baseRef := branchRefPrefix + baseBranch
	if err := manager.git(ctx, "", "check-ref-format", baseRef); err != nil {
		return MergeResult{}, ErrInvalidRepository
	}
	if request.HeadRef != "" {
		if strings.HasPrefix(request.HeadRef, "-") {
			return MergeResult{}, ErrInvalidRepository
		}
		if err := manager.git(ctx, "", "check-ref-format", request.HeadRef); err != nil {
			return MergeResult{}, ErrInvalidRepository
		}
	}
	if err := manager.Ensure(ctx, repository); err != nil {
		return MergeResult{}, err
	}

	lock := manager.repositoryLock(repository.ID)
	lock.Lock()
	defer lock.Unlock()

	repoPath := manager.repositoryPath(repository.ID)
	if !manager.repositoryAvailable(repoPath) {
		return MergeResult{}, ErrRepositoryUnavailable
	}
	if err := manager.fetchOrigin(ctx, repoPath); err != nil {
		return MergeResult{}, err
	}
	baseCommit, err := manager.resolveCommit(ctx, repoPath, baseBranch)
	if err != nil {
		return MergeResult{}, err
	}
	headCommit, err := manager.resolveMergeHead(ctx, repoPath, request)
	if err != nil {
		return MergeResult{}, err
	}
	if headCommit != request.ExpectedHeadCommit {
		return MergeResult{}, ErrStaleHead
	}
	if err := manager.git(ctx, repoPath, "merge-base", "--is-ancestor", baseCommit, headCommit); err != nil {
		return MergeResult{}, ErrNotFastForward
	}
	if err := manager.git(ctx, repoPath, "update-ref", baseRef, headCommit, baseCommit); err != nil {
		return MergeResult{}, ErrPushRejected
	}
	lease := "--force-with-lease=" + baseRef + ":" + baseCommit
	if err := manager.git(ctx, repoPath, "-c", "remote.origin.mirror=false", "push", lease, "origin", headCommit+":"+baseRef); err != nil {
		_ = manager.git(ctx, repoPath, "update-ref", baseRef, baseCommit, headCommit)
		return MergeResult{}, ErrPushRejected
	}
	if request.HeadRef != "" && strings.HasPrefix(request.HeadRef, sessionRefPrefix) {
		_ = manager.git(ctx, repoPath, "update-ref", "-d", request.HeadRef, headCommit)
	}
	if err := manager.fetchOrigin(ctx, repoPath); err != nil {
		return MergeResult{}, err
	}
	return MergeResult{MergedCommit: headCommit}, nil
}

func (manager *Manager) SquashMerge(ctx context.Context, repository Repository, request SquashMergeRequest) (MergeResult, error) {
	if err := validateRepository(repository); err != nil {
		return MergeResult{}, err
	}
	baseBranch := strings.TrimSpace(request.BaseBranch)
	if baseBranch == "" {
		baseBranch = repository.DefaultBranch
	}
	if !validBranchName(baseBranch) || !validCommitID(request.ExpectedHeadCommit) || strings.TrimSpace(request.CommitTitle) == "" ||
		strings.Contains(request.CommitTitle, "\x00") || strings.Contains(request.CommitBody, "\x00") {
		return MergeResult{}, ErrInvalidRepository
	}
	baseRef := branchRefPrefix + baseBranch
	if err := manager.git(ctx, "", "check-ref-format", baseRef); err != nil {
		return MergeResult{}, ErrInvalidRepository
	}
	if request.HeadRef != "" {
		if strings.HasPrefix(request.HeadRef, "-") {
			return MergeResult{}, ErrInvalidRepository
		}
		if err := manager.git(ctx, "", "check-ref-format", request.HeadRef); err != nil {
			return MergeResult{}, ErrInvalidRepository
		}
	}
	if request.HeadCommit != "" && !validCommitID(request.HeadCommit) {
		return MergeResult{}, ErrInvalidRepository
	}
	if err := manager.Ensure(ctx, repository); err != nil {
		return MergeResult{}, err
	}

	lock := manager.repositoryLock(repository.ID)
	lock.Lock()
	defer lock.Unlock()

	repoPath := manager.repositoryPath(repository.ID)
	if !manager.repositoryAvailable(repoPath) {
		return MergeResult{}, ErrRepositoryUnavailable
	}
	if err := manager.fetchOrigin(ctx, repoPath); err != nil {
		return MergeResult{}, err
	}
	baseCommit, err := manager.resolveCommit(ctx, repoPath, baseBranch)
	if err != nil {
		return MergeResult{}, err
	}
	headCommit, err := manager.resolveSquashMergeHead(ctx, repoPath, request)
	if err != nil {
		return MergeResult{}, err
	}
	if headCommit != request.ExpectedHeadCommit {
		return MergeResult{}, ErrStaleHead
	}
	if err := manager.git(ctx, repoPath, "merge-base", "--is-ancestor", baseCommit, headCommit); err != nil {
		return MergeResult{}, ErrNotFastForward
	}
	output, err := manager.gitOutput(ctx, repoPath,
		"-c", "user.name=Holark",
		"-c", "user.email=holark@localhost",
		"commit-tree", headCommit+"^{tree}", "-p", baseCommit, "-m", request.CommitTitle, "-m", request.CommitBody)
	if err != nil {
		return MergeResult{}, ErrRepositoryUnavailable
	}
	squashCommit := strings.TrimSpace(string(output))
	if !validCommitID(squashCommit) {
		return MergeResult{}, ErrRepositoryUnavailable
	}
	if err := manager.git(ctx, repoPath, "update-ref", baseRef, squashCommit, baseCommit); err != nil {
		return MergeResult{}, ErrPushRejected
	}
	lease := "--force-with-lease=" + baseRef + ":" + baseCommit
	if err := manager.git(ctx, repoPath, "-c", "remote.origin.mirror=false", "push", lease, "origin", squashCommit+":"+baseRef); err != nil {
		_ = manager.git(ctx, repoPath, "update-ref", baseRef, baseCommit, squashCommit)
		return MergeResult{}, ErrPushRejected
	}
	if request.HeadRef != "" && strings.HasPrefix(request.HeadRef, sessionRefPrefix) {
		_ = manager.git(ctx, repoPath, "update-ref", "-d", request.HeadRef, headCommit)
	}
	if err := manager.fetchOrigin(ctx, repoPath); err != nil {
		return MergeResult{}, err
	}
	return MergeResult{MergedCommit: squashCommit}, nil
}

func (manager *Manager) reviewCommits(ctx context.Context, repository Repository, baseCommit, headCommit string) (string, string, string, error) {
	if err := validateRepository(repository); err != nil {
		return "", "", "", err
	}
	repoPath := manager.repositoryPath(repository.ID)
	if !manager.repositoryAvailable(repoPath) {
		return "", "", "", ErrRepositoryUnavailable
	}
	resolvedBase, err := manager.resolveReviewCommit(ctx, repoPath, baseCommit)
	if err != nil {
		return "", "", "", err
	}
	resolvedHead, err := manager.resolveReviewCommit(ctx, repoPath, headCommit)
	if err != nil {
		return "", "", "", err
	}
	return repoPath, resolvedBase, resolvedHead, nil
}

func (manager *Manager) resolveReviewCommit(ctx context.Context, repoPath, commit string) (string, error) {
	if !validCommitID(commit) {
		return "", ErrRefNotFound
	}
	output, err := manager.gitOutput(ctx, repoPath, "rev-parse", "--verify", commit+"^{commit}")
	if err != nil {
		return "", ErrRefNotFound
	}
	return strings.TrimSpace(string(output)), nil
}

func (manager *Manager) resolveMergeHead(ctx context.Context, repoPath string, request FastForwardMergeRequest) (string, error) {
	if request.HeadRef != "" {
		output, err := manager.gitOutput(ctx, repoPath, "rev-parse", "--verify", request.HeadRef+"^{commit}")
		if err != nil {
			return "", ErrRefNotFound
		}
		return strings.TrimSpace(string(output)), nil
	}
	return manager.resolveReviewCommit(ctx, repoPath, request.ExpectedHeadCommit)
}

func (manager *Manager) resolveSquashMergeHead(ctx context.Context, repoPath string, request SquashMergeRequest) (string, error) {
	if request.HeadRef != "" {
		output, err := manager.gitOutput(ctx, repoPath, "rev-parse", "--verify", request.HeadRef+"^{commit}")
		if err != nil {
			return "", ErrRefNotFound
		}
		return strings.TrimSpace(string(output)), nil
	}
	if request.HeadCommit != "" {
		return manager.resolveReviewCommit(ctx, repoPath, request.HeadCommit)
	}
	return manager.resolveReviewCommit(ctx, repoPath, request.ExpectedHeadCommit)
}

func (manager *Manager) verifyOrigin(ctx context.Context, repoPath, repositoryURL string) error {
	origin, err := manager.gitOutput(ctx, repoPath, "config", "--get", "remote.origin.url")
	if err != nil {
		return fmt.Errorf("%w: read repository origin", ErrRepositoryUnavailable)
	}
	if strings.TrimSpace(string(origin)) != repositoryURL {
		return ErrOriginMismatch
	}
	return nil
}

func (manager *Manager) configureOriginFetch(ctx context.Context, repoPath string) error {
	_ = manager.git(ctx, repoPath, "config", "--unset-all", "remote.origin.mirror")
	_ = manager.git(ctx, repoPath, "config", "--unset-all", "remote.origin.fetch")
	for _, refspec := range originFetchRefspecs {
		if err := manager.git(ctx, repoPath, "config", "--add", "remote.origin.fetch", refspec); err != nil {
			return fmt.Errorf("%w: configure repository fetch", ErrRepositoryUnavailable)
		}
	}
	return nil
}

func (manager *Manager) needsBranchNamespaceMigration(ctx context.Context, repoPath string) bool {
	mirror, err := manager.gitOutput(ctx, repoPath, "config", "--get", "remote.origin.mirror")
	if err == nil && strings.TrimSpace(string(mirror)) == "true" {
		return true
	}
	fetch, err := manager.gitOutput(ctx, repoPath, "config", "--get-all", "remote.origin.fetch")
	if err != nil {
		return false
	}
	for _, refspec := range strings.Split(strings.TrimSpace(string(fetch)), "\n") {
		switch strings.TrimSpace(refspec) {
		case "+refs/*:refs/*", previousOriginFetchRefspec:
			return true
		}
	}
	return false
}

func (manager *Manager) fetchOrigin(ctx context.Context, repoPath string) error {
	if err := manager.git(ctx, repoPath, "fetch", "--prune", "--no-tags", "origin"); err != nil {
		return fmt.Errorf("%w: fetch repository cache", ErrRepositoryUnavailable)
	}
	return nil
}

func (manager *Manager) nextRebaseBackupBranch(ctx context.Context, repoPath, headBranch string) (string, error) {
	output, err := manager.gitOutput(ctx, repoPath, "for-each-ref", "--format=%(refname)", rebaseBackupRefPrefix)
	if err != nil {
		return "", ErrRepositoryUnavailable
	}
	prefix := rebaseBackupRefPrefix + headBranch + rebaseBackupBranchMarker
	var highest int64
	for _, line := range strings.Split(string(output), "\n") {
		ref := strings.TrimSpace(line)
		if !strings.HasPrefix(ref, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(ref, prefix)
		indexText, _, _ := strings.Cut(suffix, "/")
		index, err := strconv.ParseInt(indexText, 10, 64)
		if err != nil || index < 1 {
			continue
		}
		if index > highest {
			highest = index
		}
	}
	if highest == 1<<63-1 {
		return "", ErrInvalidRepository
	}
	backupBranch := headBranch + rebaseBackupBranchMarker + strconv.FormatInt(highest+1, 10)
	destination := rebaseBackupRefPrefix + backupBranch
	if !validBranchName(backupBranch) {
		return "", ErrInvalidRepository
	}
	if err := manager.git(ctx, "", "check-ref-format", destination); err != nil {
		return "", ErrInvalidRepository
	}
	return backupBranch, nil
}

func (manager *Manager) copyOriginTrackingBranchesToLocalHeads(ctx context.Context, repoPath string) error {
	output, err := manager.gitOutput(ctx, repoPath, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/remotes/origin")
	if err != nil {
		return fmt.Errorf("%w: inspect repository branches", ErrRepositoryUnavailable)
	}
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		fields := bytes.Split(line, []byte{0})
		if len(fields) != 2 {
			return fmt.Errorf("%w: inspect repository branches", ErrRepositoryUnavailable)
		}
		name := string(fields[0])
		shortName := strings.TrimPrefix(name, originTrackingBranchRefPrefix)
		if shortName == "" || shortName == name || shortName == "HEAD" {
			continue
		}
		if err := manager.git(ctx, repoPath, "update-ref", branchRefPrefix+shortName, string(fields[1])); err != nil {
			return fmt.Errorf("%w: migrate repository branches", ErrRepositoryUnavailable)
		}
	}
	return nil
}

func (manager *Manager) resolveCommit(ctx context.Context, repoPath, branch string) (string, error) {
	output, err := manager.gitOutput(ctx, repoPath, "rev-parse", "--verify", branchRefPrefix+branch+"^{commit}")
	if err != nil {
		return "", ErrRefNotFound
	}
	return strings.TrimSpace(string(output)), nil
}

func (manager *Manager) objectType(ctx context.Context, repoPath, commit, path string) (string, error) {
	output, err := manager.gitOutput(ctx, repoPath, "cat-file", "-t", commit+":"+path)
	if err != nil {
		return "", ErrPathNotFound
	}
	return strings.TrimSpace(string(output)), nil
}

func (manager *Manager) repositoryPath(id string) string {
	return filepath.Join(manager.root, id+".git")
}

func (manager *Manager) repositoryAvailable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (manager *Manager) repositoryLock(id string) *sync.Mutex {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	lock := manager.locks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		manager.locks[id] = lock
	}
	return lock
}

func (manager *Manager) contains(path string) bool {
	relative, err := filepath.Rel(manager.root, filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (manager *Manager) git(ctx context.Context, repoPath string, arguments ...string) error {
	_, err := manager.gitOutput(ctx, repoPath, arguments...)
	return err
}

func (manager *Manager) gitOutput(ctx context.Context, repoPath string, arguments ...string) ([]byte, error) {
	output, err := manager.gitCombinedOutput(ctx, repoPath, arguments...)
	if err != nil {
		return nil, fmt.Errorf("git command failed: %w", err)
	}
	return output, nil
}

func (manager *Manager) gitCombinedOutput(ctx context.Context, repoPath string, arguments ...string) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, manager.timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, "git", arguments...)
	if repoPath != "" {
		command.Args = append([]string{"git", "--git-dir", repoPath}, arguments...)
	}
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return command.CombinedOutput()
}

func (manager *Manager) pushBranchToOrigin(ctx context.Context, repoPath, sourceCommit, destination string, options ...string) ([]byte, error) {
	arguments := []string{"-c", "remote.origin.mirror=false", "push", "--porcelain"}
	arguments = append(arguments, options...)
	arguments = append(arguments, "origin", sourceCommit+":"+destination)
	return manager.gitCombinedOutput(ctx, repoPath, arguments...)
}

func exportPushRejected(output []byte) bool {
	text := strings.ToLower(string(output))
	return strings.Contains(text, "[rejected]") || strings.Contains(text, "[remote rejected]") || strings.Contains(text, "non-fast-forward")
}

func (manager *Manager) gitWorktree(ctx context.Context, worktreePath string, arguments ...string) error {
	_, err := manager.gitWorktreeOutput(ctx, worktreePath, arguments...)
	return err
}

func (manager *Manager) gitWorktreeOutput(ctx context.Context, worktreePath string, arguments ...string) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, manager.timeout)
	defer cancel()
	args := append([]string{"-C", worktreePath}, arguments...)
	command := exec.CommandContext(commandContext, "git", args...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git command failed: %w", err)
	}
	return output, nil
}

func validateRepository(repository Repository) error {
	if !validRepositoryID(repository.ID) || repository.RepositoryURL == "" || repository.DefaultBranch == "" ||
		strings.HasPrefix(repository.RepositoryURL, "-") || strings.HasPrefix(repository.DefaultBranch, "-") {
		return ErrInvalidRepository
	}
	if strings.ContainsAny(repository.DefaultBranch, " ~^:?*[\\") ||
		strings.Contains(repository.DefaultBranch, "..") || strings.Contains(repository.DefaultBranch, "@{") {
		return ErrInvalidRepository
	}
	return nil
}

func validRepositoryID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-') {
			return false
		}
	}
	return true
}

func validCommitID(value string) bool {
	if len(value) < 4 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F') || (character >= '0' && character <= '9')) {
			return false
		}
	}
	return true
}

func validBranchName(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") || strings.ContainsAny(value, " ~^:?*[\\") ||
		strings.Contains(value, "..") || strings.Contains(value, "@{") {
		return false
	}
	return true
}

func cleanTreePath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return cleanPath(value, false)
}

func cleanBlobPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", ErrInvalidPath
	}
	return cleanPath(value, true)
}

func cleanPath(value string, rejectEmpty bool) (string, error) {
	value = strings.ReplaceAll(filepath.ToSlash(value), "\\", "/")
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\x00") {
		return "", ErrInvalidPath
	}
	clean := pathClean(value)
	if clean == "." {
		if rejectEmpty {
			return "", ErrInvalidPath
		}
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", ErrInvalidPath
	}
	return clean, nil
}

func pathClean(value string) string {
	parts := strings.Split(value, "/")
	stack := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "../invalid"
		}
		stack = append(stack, part)
	}
	if len(stack) == 0 {
		return "."
	}
	return strings.Join(stack, "/")
}

func parseTreeEntries(parent string, output []byte) ([]TreeEntry, error) {
	records := bytes.Split(output, []byte{0})
	entries := make([]TreeEntry, 0, len(records))
	for _, record := range records {
		if len(record) == 0 {
			continue
		}
		header, nameBytes, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return nil, errors.New("invalid ls-tree record")
		}
		fields := strings.Fields(string(header))
		if len(fields) < 4 {
			return nil, errors.New("invalid ls-tree header")
		}
		name := string(nameBytes)
		entryType := "file"
		size := int64(0)
		if fields[1] == "tree" {
			entryType = "directory"
		} else if fields[3] != "-" {
			parsed, err := strconv.ParseInt(fields[3], 10, 64)
			if err != nil {
				return nil, err
			}
			size = parsed
		}
		entryPath := name
		if parent != "" {
			entryPath = parent + "/" + name
		}
		entries = append(entries, TreeEntry{
			Name:     name,
			Path:     entryPath,
			Type:     entryType,
			Mode:     fields[0],
			Size:     size,
			Language: languageForPath(name),
		})
	}
	return entries, nil
}

func parseCommits(output []byte) ([]Commit, error) {
	records := bytes.Split(output, []byte{0x1e})
	commits := make([]Commit, 0, len(records))
	for _, record := range records {
		record = bytes.TrimSpace(record)
		if len(record) == 0 {
			continue
		}
		fields := bytes.SplitN(record, []byte{0x1f}, 5)
		if len(fields) != 5 {
			return nil, ErrRepositoryUnavailable
		}
		authoredAt, err := time.Parse(time.RFC3339, string(fields[3]))
		if err != nil {
			return nil, ErrRepositoryUnavailable
		}
		commits = append(commits, Commit{
			SHA:         string(fields[0]),
			AuthorName:  string(fields[1]),
			AuthorEmail: string(fields[2]),
			AuthoredAt:  authoredAt,
			Message:     strings.TrimRight(string(fields[4]), "\n"),
		})
	}
	return commits, nil
}

func parseRefs(output []byte) ([]Ref, error) {
	records := bytes.Split(output, []byte{'\n'})
	refs := make([]Ref, 0, len(records))
	for _, record := range records {
		record = bytes.TrimSpace(record)
		if len(record) == 0 {
			continue
		}
		fields := bytes.Split(record, []byte{0})
		if len(fields) != 3 {
			return nil, ErrRepositoryUnavailable
		}
		name := string(fields[0])
		externalName, kind, shortName, ok := classifyRef(name)
		if !ok {
			continue
		}
		committedAt, err := time.Parse(time.RFC3339, string(fields[2]))
		if err != nil {
			return nil, ErrRepositoryUnavailable
		}
		refs = append(refs, Ref{
			Name:        externalName,
			ShortName:   shortName,
			Kind:        kind,
			Target:      string(fields[1]),
			CommittedAt: committedAt,
		})
	}
	return refs, nil
}

func classifyRef(name string) (string, string, string, bool) {
	switch {
	case strings.HasPrefix(name, branchRefPrefix):
		shortName := strings.TrimPrefix(name, branchRefPrefix)
		return name, "branch", shortName, shortName != ""
	case strings.HasPrefix(name, sessionRefPrefix):
		shortName := strings.TrimPrefix(name, sessionRefPrefix)
		return name, "holark_session", shortName, shortName != "" && !strings.Contains(shortName, "/")
	default:
		return "", "", "", false
	}
}

func allowedHistoryRef(ref string) bool {
	_, _, _, ok := classifyRef(ref)
	return ok
}

func languageForPath(path string) string {
	name := strings.ToLower(filepath.Base(path))
	switch name {
	case "makefile":
		return "makefile"
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go":
		return "go"
	case ".js", ".jsx":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".json":
		return "json"
	case ".md", ".markdown":
		return "markdown"
	case ".css":
		return "css"
	case ".html":
		return "html"
	case ".yml", ".yaml":
		return "yaml"
	case ".xml", ".svg":
		return "xml"
	case ".sh":
		return "shell"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".sql":
		return "sql"
	default:
		return "plaintext"
	}
}
