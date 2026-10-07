package gitadapter

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/holark-ai/holark/internal/repository"
)

func (g *Git) InspectSynchronization(ctx context.Context, id, target string, ignored []string) (repository.SynchronizationInspection, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := g.mu.lock(ctx, "InspectSynchronization"); err != nil {
		return repository.SynchronizationInspection{}, err
	}
	defer g.mu.Unlock()
	w, err := g.workspaceAllowDetached(ctx, id, true)
	if err != nil {
		return repository.SynchronizationInspection{}, err
	}
	i := repository.SynchronizationInspection{Branch: w.Branch, HeadCommit: w.HeadCommit}
	args, err := workspaceStatusArgs(ignored)
	if err != nil {
		return i, err
	}
	status, err := runGitAt(ctx, w.Path, args...)
	if err != nil {
		return i, err
	}
	i.Dirty = status != ""
	unmerged, err := runGitAt(ctx, w.Path, "ls-files", "--unmerged")
	if err != nil {
		return i, err
	}
	i.Conflicted = unmerged != ""
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		path, e := runGitAt(ctx, w.Path, "rev-parse", "--path-format=absolute", "--git-path", name)
		if e != nil {
			return i, e
		}
		if _, e = os.Stat(path); e == nil {
			i.Rebasing = true
			branch, readErr := os.ReadFile(filepath.Join(path, "head-name"))
			if readErr != nil {
				return i, readErr
			}
			onto, readErr := os.ReadFile(filepath.Join(path, "onto"))
			if readErr != nil {
				return i, readErr
			}
			i.RebaseBranch = strings.TrimPrefix(strings.TrimSpace(string(branch)), "refs/heads/")
			i.RebaseTarget = strings.TrimSpace(string(onto))
		} else if !errors.Is(e, os.ErrNotExist) {
			return i, e
		}
	}
	command, finishGit := gitCommand(ctx, "merge-base", "--is-ancestor", target, w.HeadCommit)

	defer finishGit()
	command.Dir = w.Path
	err = command.Run()
	if err == nil {
		i.Incorporated = true
		return i, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return i, nil
	}
	return i, err
}
