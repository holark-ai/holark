package gitadapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var ephemeralWorkspaceDirectory = regexp.MustCompile(`^holon_[0-9a-f]{24}$`)

type registeredWorktree struct {
	path   string
	branch string
}

// CleanupWorktreeRoot removes linked worktrees and branches created beneath a
// disposable Holark home. It is intentionally forceful because ephemeral mode
// promises to discard the run, including dirty worktrees.
func CleanupWorktreeRoot(ctx context.Context, commonDirectory, worktreesRoot string) error {
	root, err := filepath.Abs(worktreesRoot)
	if err != nil {
		return fmt.Errorf("resolve ephemeral worktree root: %w", err)
	}
	if _, err := os.Stat(root); err == nil {
		if canonical, canonicalErr := filepath.EvalSymlinks(root); canonicalErr == nil {
			root = canonical
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect ephemeral worktree root: %w", err)
	}
	git := &Git{}
	output, err := git.runAt(ctx, commonDirectory, "--git-dir", commonDirectory, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return fmt.Errorf("list ephemeral worktrees: %w", err)
	}
	var cleanupErr error
	registeredDirectories := map[string]bool{}
	for _, worktree := range parseRegisteredWorktrees(output) {
		if !contained(root, worktree.path) || filepath.Clean(worktree.path) == root {
			continue
		}
		relative, _ := filepath.Rel(root, worktree.path)
		if parts := strings.Split(relative, string(os.PathSeparator)); len(parts) > 0 {
			registeredDirectories[parts[0]] = true
		}
		if err := removeEphemeralWorktree(ctx, git, commonDirectory, worktree.path); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove ephemeral worktree %q: %w", worktree.path, err))
			continue
		}
		if strings.HasPrefix(worktree.branch, "refs/heads/") {
			if _, err := git.runAt(ctx, commonDirectory, "--git-dir", commonDirectory, "update-ref", "-d", worktree.branch); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove ephemeral branch %q: %w", worktree.branch, err))
			}
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("read ephemeral worktree directories: %w", err))
	}
	for _, entry := range entries {
		if !entry.IsDir() || registeredDirectories[entry.Name()] || !ephemeralWorkspaceDirectory.MatchString(entry.Name()) {
			continue
		}
		branch := "refs/heads/holark/" + entry.Name()
		if _, err := git.runAt(ctx, commonDirectory, "--git-dir", commonDirectory, "update-ref", "-d", branch); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove orphaned ephemeral branch %q: %w", branch, err))
		}
	}
	return cleanupErr
}

func removeEphemeralWorktree(ctx context.Context, git *Git, commonDirectory, path string) error {
	_, removeErr := git.runAt(ctx, commonDirectory, "--git-dir", commonDirectory, "worktree", "remove", "--force", "--", path)
	if removeErr == nil {
		return nil
	}
	if ctx.Err() != nil {
		return removeErr
	}
	if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil || !errors.Is(err, os.ErrNotExist) {
		return removeErr
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return errors.Join(removeErr, fmt.Errorf("recreate missing worktree directory: %w", err))
	}
	// An interrupted removal can delete the worktree's .git marker before Git
	// removes its registration. Repair this Holark-owned path, then retry. Some
	// Git versions recreate the marker while still reporting a repair error, so
	// the removal retry is the authoritative result.
	_, repairErr := git.runAt(ctx, commonDirectory, "--git-dir", commonDirectory, "worktree", "repair", "--", path)
	if _, retryErr := git.runAt(ctx, commonDirectory, "--git-dir", commonDirectory, "worktree", "remove", "--force", "--", path); retryErr != nil {
		if repairErr != nil {
			repairErr = fmt.Errorf("repair missing worktree metadata: %w", repairErr)
		}
		return errors.Join(removeErr, repairErr, fmt.Errorf("retry worktree removal: %w", retryErr))
	}
	return nil
}

func parseRegisteredWorktrees(output string) []registeredWorktree {
	worktrees := []registeredWorktree{}
	current := registeredWorktree{}
	for _, field := range strings.Split(output, "\x00") {
		if field == "" {
			if current.path != "" {
				worktrees = append(worktrees, current)
				current = registeredWorktree{}
			}
			continue
		}
		key, value, _ := strings.Cut(field, " ")
		switch key {
		case "worktree":
			current.path = filepath.Clean(value)
		case "branch":
			current.branch = value
		}
	}
	if current.path != "" {
		worktrees = append(worktrees, current)
	}
	return worktrees
}
