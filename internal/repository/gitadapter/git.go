package gitadapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/repository"
)

const maxBlob = 2 * 1024 * 1024

type Git struct {
	descriptor     repository.Descriptor
	worktrees      string
	attachedBranch string
	hasOrigin      bool
	mu             operationLock
	refsMu         operationLock
}

func Open(ctx context.Context, path string) (*Git, error) {
	return OpenWithWorktrees(ctx, path, "")
}

// RepositoryID returns the stable identity shared by a repository's main
// checkout and linked worktrees without performing remote discovery.
func RepositoryID(ctx context.Context, path string) (string, error) {
	_, common, err := repositoryPaths(ctx, path)
	if err != nil {
		return "", err
	}
	return repositoryID(common), nil
}

func repositoryID(commonDirectory string) string {
	sum := sha256.Sum256([]byte(commonDirectory))
	return hex.EncodeToString(sum[:16])
}

func repositoryPaths(ctx context.Context, path string) (string, string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", repository.ErrInvalidRepository
	}
	g := &Git{}
	root, err := g.runAt(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", repository.ErrInvalidRepository, err)
	}
	common, err := g.runAt(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", "", repository.ErrInvalidRepository
	}
	root, common = filepath.Clean(root), filepath.Clean(common)
	if canonical, canonicalErr := filepath.EvalSymlinks(root); canonicalErr == nil {
		root = canonical
	}
	if canonical, canonicalErr := filepath.EvalSymlinks(common); canonicalErr == nil {
		common = canonical
	}
	return root, common, nil
}

func OpenWithWorktrees(ctx context.Context, path, worktrees string) (*Git, error) {
	root, common, err := repositoryPaths(ctx, path)
	if err != nil {
		return nil, err
	}
	g := &Git{}
	bare, _ := g.runAt(ctx, root, "rev-parse", "--is-bare-repository")
	if bare == "true" {
		return nil, fmt.Errorf("%w: a working checkout is required", repository.ErrInvalidRepository)
	}
	branch, _ := g.runAt(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	g.attachedBranch = branch
	if branch == "" {
		branch = "HEAD"
	}
	repositoryID := repositoryID(common)
	remote, _ := g.runAt(ctx, root, "remote", "get-url", "origin")
	g.hasOrigin = strings.TrimSpace(remote) != ""
	if g.hasOrigin {
		if providerBranch := g.discoverOriginDefault(ctx, root); providerBranch != "" {
			branch = providerBranch
		}
	}
	g.descriptor = repository.Descriptor{Root: root, CommonDir: common, ID: repositoryID, DefaultBranch: branch, RepositoryURL: remote}
	if worktrees != "" {
		absolute, absoluteErr := filepath.Abs(worktrees)
		if absoluteErr != nil {
			return nil, repository.ErrInvalidRepository
		}
		g.worktrees = filepath.Clean(absolute)
	}
	return g, nil
}
func (g *Git) Descriptor() repository.Descriptor { return g.descriptor }
func (g *Git) DefaultRef() string {
	if g.hasOrigin {
		return "refs/holark/browse/origin/" + g.descriptor.DefaultBranch
	}
	if g.attachedBranch == "" {
		return "HEAD"
	}
	return "refs/heads/" + g.attachedBranch
}

func (g *Git) managedWorkspaceBranches(ctx context.Context) map[string]bool {
	branches := map[string]bool{}
	output, err := g.run(ctx, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return branches
	}
	root := g.workspaceRoot()
	if canonical, canonicalErr := filepath.EvalSymlinks(root); canonicalErr == nil {
		root = canonical
	}
	for _, worktree := range parseRegisteredWorktrees(output) {
		path := worktree.path
		if canonical, canonicalErr := filepath.EvalSymlinks(path); canonicalErr == nil {
			path = canonical
		}
		if path == root || !contained(root, path) || !strings.HasPrefix(worktree.branch, "refs/heads/") {
			continue
		}
		branches[strings.TrimPrefix(worktree.branch, "refs/heads/")] = true
	}
	return branches
}

func (g *Git) discoverOriginDefault(ctx context.Context, root string) string {
	if ref, err := g.runAt(ctx, root, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		if branch := strings.TrimPrefix(ref, "refs/remotes/origin/"); branch != ref && branch != "" {
			return branch
		}
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := g.runAt(discoveryCtx, root, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
			return strings.TrimPrefix(fields[1], "refs/heads/")
		}
	}
	return ""
}
func (g *Git) run(ctx context.Context, args ...string) (string, error) {
	return g.runAt(ctx, g.descriptor.Root, args...)
}
func (*Git) runAt(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	c, finishGit := gitCommand(ctx, args...)

	defer finishGit()
	c.Dir = dir
	var e bytes.Buffer
	c.Stderr = &e
	b, err := c.Output()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(e.String()))
	}
	return strings.TrimSpace(string(b)), nil
}

func (g *Git) Refs(ctx context.Context) ([]repository.Ref, error) {
	patterns := []string{"refs/heads/holark", "refs/holark/browse/origin"}
	managedBranches := g.managedWorkspaceBranches(ctx)
	for branch := range managedBranches {
		if !strings.HasPrefix(branch, "holark/") {
			patterns = append(patterns, "refs/heads/"+branch)
		}
	}
	if !g.hasOrigin && g.attachedBranch != "" {
		patterns = append(patterns, "refs/heads/"+g.attachedBranch)
	}
	args := append([]string{"for-each-ref", "--format=%(refname)%00%(objectname)%00%(committerdate:unix)%00%(authorname)%00%(subject)"}, patterns...)
	out, err := g.run(ctx, args...)
	if err != nil {
		return nil, repository.ErrRepositoryUnavailable
	}
	refs := []repository.Ref{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		p := strings.Split(line, "\x00")
		if len(p) != 5 {
			continue
		}
		name := p[0]
		short, kind := "", ""
		switch {
		case strings.HasPrefix(name, "refs/holark/browse/origin/"):
			short, kind = strings.TrimPrefix(name, "refs/holark/browse/origin/"), "branch"
		case strings.HasPrefix(name, "refs/heads/holark/"):
			short, kind = strings.TrimPrefix(name, "refs/heads/"), "holark_branch"
		case strings.HasPrefix(name, "refs/heads/") && managedBranches[strings.TrimPrefix(name, "refs/heads/")]:
			short, kind = strings.TrimPrefix(name, "refs/heads/"), "holark_branch"
		case !g.hasOrigin && name == "refs/heads/"+g.attachedBranch:
			short, kind = g.attachedBranch, "branch"
		default:
			continue
		}
		unix, _ := strconv.ParseInt(p[2], 10, 64)
		refs = append(refs, repository.Ref{Name: name, ShortName: short, Kind: kind, Target: p[1], CommittedAt: time.Unix(unix, 0).UTC(), AuthorName: p[3], Subject: p[4]})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ShortName < refs[j].ShortName })
	return refs, nil
}
func (g *Git) Refresh(ctx context.Context) ([]repository.Ref, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if !g.hasOrigin {
		return g.Refs(ctx)
	}
	if err := g.refsMu.lock(ctx, "Refresh"); err != nil {
		return nil, err
	}
	defer g.refsMu.Unlock()
	refspec := "+refs/heads/*:refs/holark/browse/origin/*"
	if _, err := g.run(ctx, "fetch", "--prune", "--no-tags", "--no-write-fetch-head", "--refmap=", "--", "origin", refspec); err != nil {
		return nil, err
	}
	return g.Refs(ctx)
}

func (g *Git) PrepareBranch(ctx context.Context, branch string) (repository.Preparation, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	branch = repository.CanonicalProviderBranch(branch)
	if branch == "" || strings.HasPrefix(branch, "-") {
		return repository.Preparation{}, repository.ErrRefNotFound
	}
	if _, err := g.run(ctx, "check-ref-format", "refs/heads/"+branch); err != nil {
		return repository.Preparation{}, repository.ErrRefNotFound
	}
	_, originErr := g.run(ctx, "remote", "get-url", "origin")
	if originErr != nil {
		if g.attachedBranch == "" || branch != g.attachedBranch {
			return repository.Preparation{}, repository.ErrRepositoryUnavailable
		}
		commit, err := g.resolve(ctx, "refs/heads/"+branch)
		if err != nil {
			return repository.Preparation{}, err
		}
		return repository.Preparation{Branch: branch, Commit: commit}, nil
	}
	if err := g.refsMu.lock(ctx, "PrepareBranch"); err != nil {
		return repository.Preparation{}, err
	}
	defer g.refsMu.Unlock()
	source := "refs/heads/" + branch
	destination := "refs/holark/browse/origin/" + branch
	if _, err := g.run(ctx, "fetch", "--no-tags", "--no-write-fetch-head", "--refmap=", "--", "origin", "+"+source+":"+destination); err != nil {
		return repository.Preparation{}, err
	}
	commit, err := g.resolve(ctx, destination)
	if err != nil {
		return repository.Preparation{}, err
	}
	return repository.Preparation{Branch: branch, Commit: commit}, nil
}

func (g *Git) ResolveProviderBranch(ctx context.Context, branch string) (string, error) {
	branch = repository.CanonicalProviderBranch(branch)
	if branch == "" {
		branch = g.descriptor.DefaultBranch
	}
	if _, err := g.run(ctx, "check-ref-format", "refs/heads/"+branch); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", repository.ErrRefNotFound
		}
		return "", fmt.Errorf("%w: validate provider branch %q: %w", repository.ErrRepositoryUnavailable, branch, err)
	}
	if g.hasOrigin {
		return g.resolve(ctx, "refs/holark/browse/origin/"+branch)
	}
	if g.attachedBranch == "" || branch != g.attachedBranch {
		return "", repository.ErrRefNotFound
	}
	return g.resolve(ctx, "refs/heads/"+branch)
}
func cleanPath(path string) (string, error) {
	path = strings.TrimPrefix(path, "/")
	c := filepath.ToSlash(filepath.Clean(path))
	if c == "." {
		return "", nil
	}
	if c == ".." || strings.HasPrefix(c, "../") || strings.Contains(c, "\x00") {
		return "", repository.ErrInvalidPath
	}
	return c, nil
}
func (g *Git) resolve(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		ref = g.descriptor.DefaultBranch
	}
	candidates := []string{ref}
	if !strings.HasPrefix(ref, "refs/") {
		candidates = []string{"refs/heads/" + ref, "refs/holark/browse/" + ref, ref}
	}
	for _, c := range candidates {
		// Quiet verification exits with 1 for a missing revision. Other
		// failures (including cancellation) must retain their actual cause.
		v, e := g.run(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", c+"^{commit}")
		if e == nil {
			return v, nil
		}
		var exitErr *exec.ExitError
		if !errors.As(e, &exitErr) || exitErr.ExitCode() != 1 {
			return "", fmt.Errorf("%w: resolve %q: %w", repository.ErrRepositoryUnavailable, ref, e)
		}
	}
	return "", fmt.Errorf("%w: %q", repository.ErrRefNotFound, ref)
}
func (g *Git) Resolve(ctx context.Context, ref string) (string, error) { return g.resolve(ctx, ref) }
func (g *Git) resolveBrowse(ctx context.Context, ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if !g.hasOrigin && g.attachedBranch == "" {
			commit, err := g.resolve(ctx, "HEAD")
			return commit, "HEAD", err
		}
		ref = g.descriptor.DefaultBranch
	}
	managedBranch := strings.TrimPrefix(ref, "refs/heads/")
	if strings.HasPrefix(ref, "refs/holark/") || strings.HasPrefix(ref, "refs/heads/holark/") || g.managedWorkspaceBranches(ctx)[managedBranch] || isCommitID(ref) || (!g.hasOrigin && ref == "HEAD") {
		commit, err := g.resolve(ctx, ref)
		return commit, ref, err
	}
	branch := repository.CanonicalProviderBranch(ref)
	commit, err := g.ResolveProviderBranch(ctx, branch)
	return commit, branch, err
}

func isCommitID(value string) bool {
	if len(value) < 7 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}
func (g *Git) Tree(ctx context.Context, ref, path string) (repository.Tree, error) {
	commit, requestedRef, e := g.resolveBrowse(ctx, ref)
	if e != nil {
		return repository.Tree{}, e
	}
	path, e = cleanPath(path)
	if e != nil {
		return repository.Tree{}, e
	}
	if path != "" {
		_, kind, _, kindErr := g.browseObject(ctx, commit+":"+path)
		if kindErr != nil {
			return repository.Tree{}, kindErr
		}
		if kind != "tree" {
			return repository.Tree{}, repository.ErrPathNotDirectory
		}
	}
	spec := commit
	if path != "" {
		spec += ":" + path
	}
	out, e := g.run(ctx, "ls-tree", "-z", "-l", spec)
	if e != nil {
		return repository.Tree{}, browseFailure(ctx, "read tree", e)
	}
	t := repository.Tree{Commit: commit, Ref: requestedRef, Path: path, Entries: []repository.TreeEntry{}}
	for _, line := range strings.Split(out, "\x00") {
		if line == "" {
			continue
		}
		meta, name, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) < 4 {
			continue
		}
		size, _ := strconv.ParseInt(f[3], 10, 64)
		typ := "file"
		if f[1] == "tree" {
			typ = "directory"
			size = 0
		}
		p := name
		if path != "" {
			p = path + "/" + name
		}
		t.Entries = append(t.Entries, repository.TreeEntry{Name: name, Path: p, Type: typ, Mode: f[0], Size: size, Language: language(name)})
	}
	sort.SliceStable(t.Entries, func(i, j int) bool {
		leftDirectory, rightDirectory := t.Entries[i].Type == "directory", t.Entries[j].Type == "directory"
		if leftDirectory != rightDirectory {
			return leftDirectory
		}
		return strings.ToLower(t.Entries[i].Name) < strings.ToLower(t.Entries[j].Name)
	})
	return t, nil
}
func (g *Git) Blob(ctx context.Context, ref, path string) (repository.Blob, error) {
	commit, requestedRef, e := g.resolveBrowse(ctx, ref)
	if e != nil {
		return repository.Blob{}, e
	}
	path, e = cleanPath(path)
	if e != nil || path == "" {
		return repository.Blob{}, repository.ErrInvalidPath
	}
	object, kind, size, err := g.browseObject(ctx, commit+":"+path)
	if err != nil {
		return repository.Blob{}, err
	}
	if kind != "blob" {
		return repository.Blob{}, repository.ErrPathNotFile
	}
	if size > maxBlob {
		return repository.Blob{}, repository.ErrFileTooLarge
	}
	c, finishGit := gitCommand(ctx, "cat-file", "blob", object)
	defer finishGit()
	c.Dir = g.descriptor.Root
	b, e := c.Output()
	if e != nil {
		return repository.Blob{}, browseFailure(ctx, "read blob", e)
	}
	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		return repository.Blob{}, repository.ErrBinaryFile
	}
	return repository.Blob{Commit: commit, Ref: requestedRef, Path: path, Language: language(path), Content: string(b), Encoding: "utf-8", Size: size}, nil
}

// Batch lookup reports missing paths in its output. A failed Git process is a
// repository error, not evidence that the requested path does not exist.
func (g *Git) browseObject(ctx context.Context, spec string) (object, kind string, size int64, err error) {
	c, finish := gitCommand(ctx, "cat-file", "--batch-check", "-z")
	defer finish()
	c.Dir = g.descriptor.Root
	c.Stdin = strings.NewReader(spec + "\x00")
	out, err := c.Output()
	if err != nil {
		return "", "", 0, browseFailure(ctx, "inspect object", err)
	}
	if string(out) == spec+" missing\n" {
		return "", "", 0, repository.ErrPathNotFound
	}
	if _, err := fmt.Sscanf(strings.TrimSuffix(string(out), "\n"), "%s %s %d", &object, &kind, &size); err != nil {
		return "", "", 0, browseFailure(ctx, "inspect object", err)
	}
	return object, kind, size, nil
}

func browseFailure(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: %s: %w", repository.ErrRepositoryUnavailable, operation, err)
}

func (g *Git) Commits(ctx context.Context, ref string, limit int) ([]repository.Commit, error) {
	commit, _, e := g.resolveBrowse(ctx, ref)
	if e != nil {
		return nil, e
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	return g.CommitPage(ctx, commit, limit, 0)
}

// CommitPage traverses the same pinned history on every page, including merges.
func (g *Git) CommitPage(ctx context.Context, commit string, limit, offset int) ([]repository.Commit, error) {
	out, e := g.run(ctx, "log", "-n", strconv.Itoa(limit), "--skip="+strconv.Itoa(offset), "-z", "--format=%H%x00%B%x00%an%x00%ae%x00%aI", commit, "--")
	if e != nil {
		return nil, e
	}
	result := []repository.Commit{}
	// NUL-delimited fields and records preserve multiline commit messages.
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	for len(fields) >= 5 {
		at, _ := time.Parse(time.RFC3339, fields[4])
		result = append(result, repository.Commit{SHA: fields[0], Message: strings.TrimRight(fields[1], "\n"), AuthorName: fields[2], AuthorEmail: fields[3], AuthoredAt: at})
		fields = fields[5:]
	}
	return result, nil
}

func (g *Git) MergeBase(ctx context.Context, base, head string) (string, error) {
	b, err := g.resolve(ctx, base)
	if err != nil {
		return "", err
	}
	h, err := g.resolve(ctx, head)
	if err != nil {
		return "", err
	}
	merged, err := g.run(ctx, "merge-base", b, h)
	if err != nil {
		return "", fmt.Errorf("%w: merge base of %s and %s: %w", repository.ErrRepositoryUnavailable, b, h, err)
	}
	return merged, nil
}

func (g *Git) CommitRange(ctx context.Context, base, head string) ([]repository.Commit, error) {
	b, err := g.resolve(ctx, base)
	if err != nil {
		return nil, err
	}
	h, err := g.resolve(ctx, head)
	if err != nil {
		return nil, err
	}
	out, err := g.run(ctx, "log", "--reverse", "-z", "--format=%H%x00%an%x00%ae%x00%aI%x00%B", b+".."+h)
	if err != nil {
		return nil, fmt.Errorf("%w: commits between %s and %s: %w", repository.ErrRepositoryUnavailable, b, h, err)
	}
	result := []repository.Commit{}
	// NUL-delimited fields preserve message bodies, including paragraph breaks.
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	for len(fields) >= 5 {
		at, _ := time.Parse(time.RFC3339, fields[3])
		result = append(result, repository.Commit{
			SHA: fields[0], AuthorName: fields[1], AuthorEmail: fields[2],
			AuthoredAt: at, Message: strings.TrimRight(fields[4], "\n"),
		})
		fields = fields[5:]
	}
	return result, nil
}
func (g *Git) Changes(ctx context.Context, base, target string) ([]repository.Change, error) {
	b, e := g.resolve(ctx, base)
	if e != nil {
		return nil, e
	}
	t, e := g.resolve(ctx, target)
	if e != nil {
		return nil, e
	}
	names, e := g.run(ctx, "diff", "--name-status", b+"..."+t)
	if e != nil {
		return nil, e
	}
	changes := []repository.Change{}
	for _, line := range strings.Split(names, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		path := f[len(f)-1]
		patch, _ := g.run(ctx, "diff", "--no-ext-diff", b+"..."+t, "--", path)
		changes = append(changes, repository.Change{Path: path, Status: f[0], Patch: patch})
	}
	return changes, nil
}

func (g *Git) Publish(ctx context.Context, commit, branch string) error {
	commit = strings.TrimSpace(commit)
	branch = strings.TrimSpace(branch)
	if commit == "" || branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " ~^:?*[\\") {
		return repository.ErrInvalidRepository
	}
	_, err := g.run(ctx, "push", "origin", commit+":refs/heads/"+branch)
	return err
}
func (g *Git) Dirty(ctx context.Context) (repository.DirtyState, error) {
	command, finishGit := gitCommand(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")

	defer finishGit()
	command.Dir = g.descriptor.Root
	status, e := command.Output()
	if e != nil {
		return repository.DirtyState{}, e
	}
	if len(status) == 0 {
		return repository.DirtyState{}, nil
	}
	fingerprint := sha256.New()
	fingerprint.Write(status)
	for _, args := range [][]string{{"diff", "--binary"}, {"diff", "--cached", "--binary"}} {
		c, finishGit := gitCommand(ctx, args...)

		defer finishGit()
		c.Dir = g.descriptor.Root
		content, err := c.Output()
		if err != nil {
			return repository.DirtyState{}, err
		}
		fingerprint.Write(content)
	}
	d := repository.DirtyState{Dirty: true}
	for _, line := range strings.Split(string(status), "\x00") {
		if len(line) < 3 {
			continue
		}
		if line[0] == '?' {
			d.Untracked++
			content, err := os.ReadFile(filepath.Join(g.descriptor.Root, line[3:]))
			if err == nil {
				fingerprint.Write(content)
			}
			continue
		}
		if line[0] != ' ' {
			d.Staged++
		}
		if line[1] != ' ' {
			d.Unstaged++
		}
	}
	d.Fingerprint = hex.EncodeToString(fingerprint.Sum(nil))
	return d, nil
}
func language(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".md":
		return "markdown"
	case ".json":
		return "json"
	case ".py":
		return "python"
	case ".yaml", ".yml":
		return "yaml"
	case ".html":
		return "html"
	case ".css":
		return "css"
	case ".sh":
		return "shell"
	default:
		return "plaintext"
	}
}

var _ repository.Store = (*Git)(nil)
var _ = errors.Is
