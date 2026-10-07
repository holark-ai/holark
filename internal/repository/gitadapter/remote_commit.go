package gitadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/gittransport"
	"github.com/holark-ai/holark/internal/repository"
)

func (g *Git) EnsureRemoteCommit(ctx context.Context, repositoryURL, commit string) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := g.refsMu.lock(ctx, "EnsureRemoteCommit"); err != nil {
		return err
	}
	defer g.refsMu.Unlock()
	return g.ensureRemoteCommit(ctx, repositoryURL, commit)
}

// ensureRemoteCommit requires refsMu, shared with browsing and cache writes.
func (g *Git) ensureRemoteCommit(ctx context.Context, repositoryURL, commit string) error {
	commit = strings.TrimSpace(commit)
	if len(commit) != 40 && len(commit) != 64 {
		return repository.ErrRefNotFound
	}
	if _, err := hex.DecodeString(commit); err != nil {
		return repository.ErrRefNotFound
	}
	if _, err := g.run(ctx, "cat-file", "-e", commit+"^{commit}"); err == nil {
		return nil
	}
	source, err := gittransport.Resolve(ctx, g.descriptor.Root, repositoryURL, false)
	if err != nil {
		return err
	}
	started := time.Now()
	_, err = g.run(ctx, "fetch", "--no-tags", "--no-write-fetch-head", "--refmap=", "--", source, commit)
	slog.DebugContext(ctx, "Pull request commit fetch", "repository_id", g.descriptor.ID, "commit", commit, "duration", time.Since(started), "error", err)
	if err != nil {
		return err
	}
	_, err = g.run(ctx, "cat-file", "-e", commit+"^{commit}")
	return err
}

func (g *Git) RemoteBranchHead(ctx context.Context, repositoryURL, branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if _, err := g.run(ctx, "check-ref-format", "refs/heads/"+branch); err != nil {
		return "", repository.ErrRefNotFound
	}
	source, err := gittransport.Resolve(ctx, g.descriptor.Root, repositoryURL, false)
	if err != nil {
		return "", err
	}
	output, err := g.run(ctx, "ls-remote", "--heads", "--", source, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) != 2 || fields[1] != "refs/heads/"+branch {
		return "", repository.ErrRefNotFound
	}
	return fields[0], nil
}

func (g *Git) ObserveBranches(ctx context.Context, repositoryURL string, refs []string) (map[string]repository.BranchObservation, error) {
	if repository.RepositoryIdentity(repositoryURL) == "" {
		return nil, repository.ErrInvalidRepository
	}
	result := make(map[string]repository.BranchObservation, len(refs))
	for _, ref := range refs {
		if !strings.HasPrefix(ref, "refs/heads/") {
			return nil, repository.ErrRefNotFound
		}
		if _, err := g.run(ctx, "check-ref-format", ref); err != nil {
			return nil, repository.ErrRefNotFound
		}
		result[ref] = repository.BranchObservation{}
	}
	if len(refs) == 0 {
		return result, nil
	}
	source, err := gittransport.Resolve(ctx, g.descriptor.Root, repositoryURL, false)
	if err != nil {
		return nil, err
	}
	args := append([]string{"ls-remote", "--heads", "--", source}, refs...)
	output, err := g.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if _, requested := result[fields[1]]; requested {
			result[fields[1]] = repository.BranchObservation{Commit: fields[0], Exists: true}
		}
	}
	return result, nil
}

func (g *Git) CachePublishedBranch(ctx context.Context, source, ref string, observed repository.BranchObservation) error {
	identity := repository.RepositoryIdentity(source)
	if identity == "" {
		return repository.ErrInvalidRepository
	}
	if !strings.HasPrefix(ref, "refs/heads/") {
		return repository.ErrRefNotFound
	}
	if _, err := g.run(ctx, "check-ref-format", ref); err != nil {
		return repository.ErrRefNotFound
	}
	branch := strings.TrimPrefix(ref, "refs/heads/")
	prefix := fmt.Sprintf("refs/holark/browse/repositories/%x/", sha256.Sum256([]byte(identity)))
	if identity == repository.RepositoryIdentity(g.descriptor.RepositoryURL) {
		prefix = "refs/holark/browse/origin/"
	}
	destination := prefix + branch
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := g.refsMu.lock(ctx, "CachePublishedBranch"); err != nil {
		return err
	}
	defer g.refsMu.Unlock()
	if !observed.Exists {
		_, err := g.run(ctx, "update-ref", "-d", destination)
		return err
	}
	if err := g.ensureRemoteCommit(ctx, source, observed.Commit); err != nil {
		return err
	}
	if _, err := g.run(ctx, "update-ref", destination, observed.Commit); err == nil {
		return nil
	}
	// Closed PRs no longer refresh their branches, so their cache refs can
	// outlive deleted branches and block a later topic/sub (or topic) ref.
	if err := g.pruneConflictingCacheRefs(ctx, source, prefix, destination); err != nil {
		return err
	}
	_, err := g.run(ctx, "update-ref", destination, observed.Commit)
	return err
}

// pruneConflictingCacheRefs requires refsMu. Only a successful remote read can
// confirm deletion; a live branch or an unavailable remote must keep its cache.
func (g *Git) pruneConflictingCacheRefs(ctx context.Context, source, prefix, destination string) error {
	output, err := g.run(ctx, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return err
	}
	var refs []string
	for _, cached := range strings.Fields(output) {
		if strings.HasPrefix(destination, cached+"/") || strings.HasPrefix(cached, destination+"/") {
			refs = append(refs, "refs/heads/"+strings.TrimPrefix(cached, prefix))
		}
	}
	observed, err := g.ObserveBranches(ctx, source, refs)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if observed[ref].Exists {
			continue
		}
		if _, err := g.run(ctx, "update-ref", "-d", prefix+strings.TrimPrefix(ref, "refs/heads/")); err != nil {
			return err
		}
	}
	return nil
}
