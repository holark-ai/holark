package repository

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
)

// BranchIdentity names a published ref independently of fetch/push transport.
type BranchIdentity struct {
	Repository string `json:"repository"`
	Ref        string `json:"ref"`
}

// RepositoryIdentity coalesces SSH and HTTPS aliases without conflating forks.
// Local paths retain case; GitHub repository paths are case insensitive.
func RepositoryIdentity(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "origin" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		if prefix, path, ok := strings.Cut(raw, ":"); ok && !strings.ContainsAny(prefix, "/\\") {
			_, host, found := strings.Cut(prefix, "@")
			if !found {
				host = prefix
			}
			raw = "ssh://" + host + "/" + path
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if parsed.Hostname() != "" {
		host := strings.ToLower(parsed.Hostname())
		path := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
		if host == "github.com" {
			path = strings.ToLower(path)
		}
		if path == "" {
			return ""
		}
		return host + "/" + path
	}
	if parsed.Scheme != "" && parsed.Scheme != "file" {
		return ""
	}
	path, err := filepath.Abs(parsed.Path)
	if err != nil {
		return ""
	}
	return "file://" + filepath.Clean(path)
}

func PublishedBranchIdentity(repositoryURL, branch string) BranchIdentity {
	ref := strings.TrimSpace(branch)
	if !strings.HasPrefix(ref, "refs/heads/") {
		ref = "refs/heads/" + CanonicalProviderBranch(ref)
	}
	return BranchIdentity{Repository: RepositoryIdentity(repositoryURL), Ref: ref}
}
func (b BranchIdentity) Valid() bool {
	return b.Repository != "" && strings.HasPrefix(b.Ref, "refs/heads/") && len(b.Ref) > len("refs/heads/")
}

type PublishedBranch struct {
	Identity      BranchIdentity `json:"identity"`
	Commit        string         `json:"commit"`
	Exists        bool           `json:"exists"`
	Generation    int64          `json:"generation"`
	Observation   int64          `json:"observation"`
	MutationEpoch int64          `json:"mutation_epoch"`
}

type BranchObservation struct {
	Commit string
	Exists bool
}

// ObserveBranches returns an entry for every requested full ref. A missing ref
// is successful evidence; authentication and network failures return an error.
type BranchObserver interface {
	ObserveBranches(context.Context, string, []string) (map[string]BranchObservation, error)
}

func (s *Service) ObserveBranches(ctx context.Context, source string, refs []string) (map[string]BranchObservation, error) {
	observer, ok := s.store.(BranchObserver)
	if !ok {
		return nil, ErrRepositoryUnavailable
	}
	return observer.ObserveBranches(ctx, source, refs)
}

// BranchRead captures ordering before network work starts. MutationEpoch fences
// reads that overlap a protected writer, even when the ref returns to one SHA.
type BranchRead struct {
	Sequence int64
	Branches []PublishedBranch
}

var ErrBranchChanged = errors.New("published branch changed or is protected")

// PublishedBranchCache maintains navigation refs from already accepted evidence.
// It imports the exact commit, never substitutes a fresh branch-head lookup.
type PublishedBranchCache interface {
	CachePublishedBranch(context.Context, string, string, BranchObservation) error
}

func (s *Service) CachePublishedBranch(ctx context.Context, source, ref string, observed BranchObservation) error {
	cache, ok := s.store.(PublishedBranchCache)
	if !ok {
		return ErrRepositoryUnavailable
	}
	return cache.CachePublishedBranch(ctx, source, ref, observed)
}
