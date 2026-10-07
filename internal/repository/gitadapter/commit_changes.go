package gitadapter

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/holark-ai/holark/internal/repository"
)

func (g *Git) CommitChanges(ctx context.Context, ref string) (repository.CommitChanges, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	commit, err := g.resolve(ctx, ref)
	if err != nil {
		return repository.CommitChanges{}, err
	}
	result := repository.CommitChanges{Commit: commit, Files: []repository.Change{}}
	// Read actual parents, including shallow boundaries; never silently treat a
	// missing parent as an initial commit.
	object, err := g.run(ctx, "cat-file", "-p", commit)
	if err != nil {
		return result, err
	}
	for _, line := range strings.Split(strings.SplitN(object, "\n\n", 2)[0], "\n") {
		if strings.HasPrefix(line, "parent ") {
			result.Parent = strings.TrimPrefix(line, "parent ")
			break
		}
	}
	revisions := []string{commit}
	if result.Parent != "" {
		revisions = []string{result.Parent, commit}
	}
	args := append([]string{"diff-tree", "--root", "--no-commit-id", "-r", "--no-renames", "--name-status", "-z"}, revisions...)
	names, err := g.run(ctx, append(args, "--")...)
	if err != nil {
		return result, err
	}
	fields := strings.Split(strings.TrimSuffix(names, "\x00"), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		status, path := fields[i], fields[i+1]
		args := append([]string{"--literal-pathspecs", "diff-tree", "--root", "--no-commit-id", "-r", "--no-renames", "--no-ext-diff", "--no-textconv", "--no-color", "-p"}, revisions...)
		command, finish := gitCommand(ctx, append(args, "--", path)...)
		command.Dir = g.descriptor.Root
		var stderr bytes.Buffer
		command.Stderr = &stderr
		output, err := command.Output()
		finish()
		patch := string(output)
		if err != nil {
			return result, fmt.Errorf("read commit diff: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		change := repository.Change{Path: path, Status: status, Binary: strings.Contains(patch, "\nBinary files ")}
		const maxPatch = 256 * 1024
		if len(patch) > maxPatch {
			patch = patch[:maxPatch]
			if end := strings.LastIndexByte(patch, '\n'); end >= 0 {
				patch = patch[:end+1]
			}
			change.Truncated = true
		}
		change.Patch = patch
		result.Files = append(result.Files, change)
	}
	return result, nil
}
