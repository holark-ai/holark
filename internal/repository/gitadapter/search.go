package gitadapter

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/holark-ai/holark/internal/repository"
)

// Search reads one immutable tree so filenames and contents use the same revision.
func (g *Git) Search(ctx context.Context, ref, query string) (repository.SearchResults, error) {
	commit, requestedRef, err := g.resolveBrowse(ctx, ref)
	if err != nil {
		return repository.SearchResults{}, err
	}
	result := repository.SearchResults{Ref: requestedRef, Matches: []repository.SearchMatch{}}
	tree, err := g.run(ctx, "ls-tree", "-r", "-z", commit)
	if err != nil {
		return result, err
	}
	// Literal, case-insensitive matching; binary files are still searchable by name.
	content, err := g.run(ctx, "grep", "--no-textconv", "-I", "-i", "-F", "-l", "-z", "-e", query, commit, "--")
	if err != nil {
		var exitErr *exec.ExitError
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return result, err
		}
	}
	contentPaths := make(map[string]bool)
	for _, name := range strings.Split(content, "\x00") {
		if path, ok := strings.CutPrefix(name, commit+":"); ok {
			contentPaths[path] = true
		}
	}
	needle := strings.ToLower(query)
	for _, entry := range strings.Split(tree, "\x00") {
		metadata, path, ok := strings.Cut(entry, "\t")
		if !ok || !strings.Contains(metadata, " blob ") {
			continue
		}
		nameMatch := strings.Contains(strings.ToLower(path), needle)
		if !nameMatch && !contentPaths[path] {
			continue
		}
		if len(result.Matches) == 200 {
			result.Truncated = true
			break
		}
		result.Matches = append(result.Matches, repository.SearchMatch{Path: path, NameMatch: nameMatch, ContentMatch: contentPaths[path]})
	}
	return result, nil
}
