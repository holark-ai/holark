package workspace

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/protocol"
	"golang.org/x/sys/unix"
)

const maxContentBytes = 256 * 1024

func (inspector inspector) loadContents(ctx context.Context, base, target resolvedDiffRef, file *protocol.WorkspaceFileDiff) error {
	originalPath := file.Path
	if file.OldPath != "" {
		originalPath = file.OldPath
	}
	original, originalStatus, err := inspector.readContent(ctx, base, originalPath, file.Status == "A")
	if err != nil {
		return err
	}
	modified, modifiedStatus, err := inspector.readContent(ctx, target, file.Path, file.Status == "D")
	if err != nil {
		return err
	}
	file.ContentStatus = "ready"
	for _, status := range []string{originalStatus, modifiedStatus} {
		if status != "ready" {
			file.ContentStatus = status
			break
		}
	}
	if originalStatus == "binary" || modifiedStatus == "binary" {
		file.ContentStatus = "binary"
		file.Binary = true
	}
	if file.ContentStatus == "ready" {
		file.Contents = &protocol.WorkspaceContents{Original: original, Modified: modified}
	}
	return nil
}

func (inspector inspector) readContent(ctx context.Context, ref resolvedDiffRef, path string, absent bool) (string, string, error) {
	if unsafeRelative(path) {
		return "", "", ErrInvalidDiffPath
	}
	if absent {
		return "", "ready", nil
	}
	var data []byte
	if ref.Worktree {
		var status string
		var err error
		data, status, err = readWorktreeContent(inspector.worktree, path)
		if err != nil || status != "ready" {
			return "", status, err
		}
	} else {
		// Gitlink commits live in the submodule repository, so inspect the tree
		// entry before trying to open an object in the parent repository.
		entry, err := inspector.gitOutput(ctx, "--literal-pathspecs", "ls-tree", "-z", ref.Commit, "--", path)
		if err != nil {
			return "", "", err
		}
		if strings.HasPrefix(entry, "160000 ") {
			return "", "unsupported", nil
		}
		// Resolve the path to a blob before reading it. cat-file preserves bytes and
		// trailing newlines, and the size check avoids loading oversized Git objects.
		object := ref.Commit + ":" + path
		kind, err := inspector.gitOutput(ctx, "cat-file", "-t", object)
		if err != nil {
			return "", "", err
		}
		if strings.TrimSpace(kind) != "blob" {
			return "", "unsupported", nil
		}
		sizeText, err := inspector.gitOutput(ctx, "cat-file", "-s", object)
		if err != nil {
			return "", "", err
		}
		size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
		if err != nil {
			return "", "", err
		}
		if size > maxContentBytes {
			return "", "too_large", nil
		}
		content, err := inspector.gitOutput(ctx, "cat-file", "blob", object)
		if err != nil {
			return "", "", err
		}
		data = []byte(content)
	}
	if len(data) > maxContentBytes {
		return "", "too_large", nil
	}
	if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
		return "", "binary", nil
	}
	return string(data), "ready", nil
}

func readWorktreeContent(worktree, path string) ([]byte, string, error) {
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	// Open the parent beneath the root, then operate on the final component
	// without following links. The directory descriptor also closes rename races.
	parent, err := root.Open(filepath.Dir(path))
	if err != nil {
		return nil, "unsupported", nil
	}
	defer parent.Close()
	name := filepath.Base(path)
	var stat unix.Stat_t
	if err = unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, "", err
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		data := make([]byte, maxContentBytes+1)
		n, err := unix.Readlinkat(int(parent.Fd()), name, data)
		if err != nil {
			return nil, "", err
		}
		return data[:n], "ready", nil
	case unix.S_IFREG:
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			if errors.Is(err, unix.ELOOP) {
				return nil, "unsupported", nil
			}
			return nil, "", err
		}
		file := os.NewFile(uintptr(fd), name)
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return nil, "", err
		}
		if !info.Mode().IsRegular() {
			return nil, "unsupported", nil
		}
		if info.Size() > maxContentBytes {
			return nil, "too_large", nil
		}
		data, err := io.ReadAll(io.LimitReader(file, maxContentBytes+1))
		return data, "ready", err
	default:
		return nil, "unsupported", nil
	}
}
