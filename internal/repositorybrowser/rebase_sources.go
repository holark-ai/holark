package repositorybrowser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/holark-ai/holark/internal/gittransport"
)

// PrepareRebaseSources uses a separate cache when publication belongs to a
// fork, and imports each pinned input from its owning repository. The existing
// rebaser therefore keeps its expected-head lease against the correct origin.
func (manager *Manager) PrepareRebaseSources(ctx context.Context, value Repository, headURL, baseURL, baseCommit, headCommit string) (Repository, error) {
	headURL = strings.TrimSpace(headURL)
	baseURL = strings.TrimSpace(baseURL)
	if headURL == "" {
		headURL = value.RepositoryURL
	}
	if baseURL == "" {
		baseURL = value.RepositoryURL
	}
	// Provider URLs identify repositories; configured URLs choose transport.
	if value.GitDirectory != "" {
		var err error
		headURL, err = gittransport.Resolve(ctx, value.GitDirectory, headURL, true)
		if err != nil {
			return Repository{}, err
		}
		baseURL, err = gittransport.Resolve(ctx, value.GitDirectory, baseURL, false)
		if err != nil {
			return Repository{}, err
		}
	} else {
		headURL = gittransport.SelectURL(headURL, value.RepositoryURL)
		baseURL = gittransport.SelectURL(baseURL, value.RepositoryURL)
	}
	if strings.TrimSuffix(headURL, ".git") != strings.TrimSuffix(value.RepositoryURL, ".git") {
		identity := sha256.Sum256([]byte(value.ID + "\n" + headURL))
		value.ID = "pr-source-" + hex.EncodeToString(identity[:16])
		value.RepositoryURL = headURL
	}
	if err := manager.Ensure(ctx, value); err != nil {
		return Repository{}, err
	}
	lock := manager.repositoryLock(value.ID)
	lock.Lock()
	defer lock.Unlock()
	path := manager.repositoryPath(value.ID)
	for _, input := range []struct{ source, commit string }{{baseURL, baseCommit}, {headURL, headCommit}} {
		if !validCommitID(input.commit) || strings.HasPrefix(input.source, "-") {
			return Repository{}, ErrInvalidRepository
		}
		if _, err := manager.resolveReviewCommit(ctx, path, input.commit); err == nil {
			continue
		}
		if err := manager.git(ctx, path, "fetch", "--no-tags", "--no-write-fetch-head", "--refmap=", "--", input.source, input.commit); err != nil {
			return Repository{}, err
		}
		if _, err := manager.resolveReviewCommit(ctx, path, input.commit); err != nil {
			return Repository{}, err
		}
	}
	return value, nil
}
