package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type SkillSource struct {
	FS   fs.FS
	Root string
}

type SkillInstall struct {
	TargetDir string
	Sources   []SkillSource
}

func (manager *Manager) InstallSkill(ctx context.Context, worktree string, install SkillInstall) error {
	if manager == nil {
		return errors.New("workspace manager unavailable")
	}
	targetDir, err := cleanSkillTargetDir(install.TargetDir)
	if err != nil {
		return err
	}
	if len(install.Sources) == 0 {
		return errors.New("skill install has no sources")
	}
	sources := make([]SkillSource, 0, len(install.Sources))
	for _, source := range install.Sources {
		root, err := cleanSkillSourceRoot(source.Root)
		if err != nil {
			return err
		}
		if source.FS == nil {
			return errors.New("skill install source filesystem is nil")
		}
		sources = append(sources, SkillSource{FS: source.FS, Root: root})
	}
	worktree, err = filepath.Abs(worktree)
	if err != nil {
		return fmt.Errorf("resolve worktree path %q: %w", worktree, err)
	}
	worktree = filepath.Clean(worktree)
	target := filepath.Clean(filepath.Join(worktree, targetDir))
	if !manager.contains(target) {
		return errors.New("skill install path escapes workspace root")
	}
	parent := filepath.Dir(target)
	if !manager.contains(parent) {
		return errors.New("skill install parent path escapes workspace root")
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create skill parent directory %q: %w", parent, err)
	}
	if err := cleanupStagedSkillSiblings(parent, filepath.Base(target)); err != nil {
		return err
	}
	if err := manager.excludeSkill(ctx, worktree, targetDir); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(parent, "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return fmt.Errorf("create staged skill directory: %w", err)
	}
	tempActive := true
	defer func() {
		if tempActive {
			_ = os.RemoveAll(temp)
		}
	}()
	for _, source := range sources {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := copySkillLayer(source.FS, source.Root, temp); err != nil {
			return err
		}
	}
	if err := replaceSkillDirectory(target, temp); err != nil {
		return err
	}
	tempActive = false
	return nil
}

func cleanupStagedSkillSiblings(parent, targetBase string) error {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return fmt.Errorf("read skill parent directory %q: %w", parent, err)
	}
	tempPrefix := "." + targetBase + ".tmp-"
	oldPrefix := "." + targetBase + ".old-"
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, tempPrefix) && !strings.HasPrefix(name, oldPrefix) {
			continue
		}
		path := filepath.Join(parent, name)
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove stale staged skill directory %q: %w", path, err)
		}
	}
	return nil
}

func cleanSkillTargetDir(targetDir string) (string, error) {
	if strings.TrimSpace(targetDir) == "" {
		return "", errors.New("skill install target directory is empty")
	}
	if filepath.IsAbs(targetDir) {
		return "", errors.New("skill install target directory is absolute")
	}
	clean := path.Clean(filepath.ToSlash(targetDir))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", errors.New("skill install target directory escapes worktree")
	}
	return filepath.FromSlash(clean), nil
}

func cleanSkillSourceRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("skill install source root is empty")
	}
	if strings.Contains(root, "\\") || path.Clean(root) != root || root == "." || !fs.ValidPath(root) {
		return "", fmt.Errorf("unsafe skill install source root %q", root)
	}
	return root, nil
}

func copySkillLayer(sourceFS fs.FS, sourceRoot, targetRoot string) error {
	return fs.WalkDir(sourceFS, sourceRoot, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative := strings.TrimPrefix(filePath, sourceRoot)
		relative = strings.TrimPrefix(relative, "/")
		target := targetRoot
		if relative != "" {
			target = filepath.Join(targetRoot, filepath.FromSlash(relative))
		}
		if entry.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create skill directory %q: %w", target, err)
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect skill source file %q: %w", filePath, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("skill source file %q is not regular", filePath)
		}
		data, err := fs.ReadFile(sourceFS, filePath)
		if err != nil {
			return fmt.Errorf("read skill source file %q: %w", filePath, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create skill directory %q: %w", filepath.Dir(target), err)
		}
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return fmt.Errorf("install skill file %q: %w", target, err)
		}
		return nil
	})
}

func replaceSkillDirectory(target, temp string) error {
	backup := ""
	if _, err := os.Lstat(target); err == nil {
		placeholder, err := os.MkdirTemp(filepath.Dir(target), "."+filepath.Base(target)+".old-")
		if err != nil {
			return fmt.Errorf("create backup skill directory placeholder: %w", err)
		}
		if err := os.Remove(placeholder); err != nil {
			return fmt.Errorf("remove backup skill directory placeholder %q: %w", placeholder, err)
		}
		backup = placeholder
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("stage previous skill directory %q: %w", target, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect skill directory %q: %w", target, err)
	}
	if err := os.Rename(temp, target); err != nil {
		if backup != "" {
			if restoreErr := os.Rename(backup, target); restoreErr != nil {
				return fmt.Errorf("replace skill directory %q: %w; restore previous directory: %v", target, err, restoreErr)
			}
		}
		return fmt.Errorf("replace skill directory %q: %w", target, err)
	}
	if backup != "" {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("remove previous skill directory %q: %w", backup, err)
		}
	}
	return nil
}

func (manager *Manager) excludeSkill(ctx context.Context, worktree, targetDir string) error {
	excludeLine := strings.TrimSuffix(filepath.ToSlash(targetDir), "/") + "/"
	excludePath, err := manager.worktreeGitPath(ctx, worktree, "info/exclude")
	if err != nil {
		return err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	data, err := os.ReadFile(excludePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read worktree exclude file %q: %w", excludePath, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == excludeLine {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o700); err != nil {
		return fmt.Errorf("create worktree exclude directory %q: %w", filepath.Dir(excludePath), err)
	}
	prefix := ""
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		prefix = "\n"
	}
	file, err := os.OpenFile(excludePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open worktree exclude file %q: %w", excludePath, err)
	}
	defer file.Close()
	if _, err := file.WriteString(prefix + excludeLine + "\n"); err != nil {
		return fmt.Errorf("append worktree exclude file %q: %w", excludePath, err)
	}
	return nil
}

func (manager *Manager) worktreeGitDir(ctx context.Context, worktree string) (string, error) {
	gitDir, err := manager.gitOutput(ctx, "-C", worktree, "rev-parse", "--git-dir")
	if err != nil {
		return "", fmt.Errorf("resolve worktree git dir: %w", err)
	}
	gitDirPath := strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDirPath) {
		gitDirPath = filepath.Join(worktree, gitDirPath)
	}
	return gitDirPath, nil
}

func (manager *Manager) worktreeGitPath(ctx context.Context, worktree, gitPath string) (string, error) {
	resolved, err := manager.gitOutput(ctx, "-C", worktree, "rev-parse", "--git-path", gitPath)
	if err != nil {
		return "", fmt.Errorf("resolve worktree git path %s: %w", gitPath, err)
	}
	path := strings.TrimSpace(resolved)
	if !filepath.IsAbs(path) {
		path = filepath.Join(worktree, path)
	}
	return path, nil
}
