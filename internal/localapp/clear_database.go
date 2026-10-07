package localapp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/holark-ai/holark/internal/repository/gitadapter"
)

func repositoryDatabaseKey(repositoryID string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(repositoryID)))
}

// ClearRepositoryDatabase removes the SQLite state for one repository while
// holding the same lock used when Holark runs for that repository.
// confirm is called with the database path only when state files exist.
func ClearRepositoryDatabase(ctx context.Context, repositoryPath, homeDirectory string, confirm func(string) (bool, error)) (bool, error) {
	repositoryID, err := gitadapter.RepositoryID(ctx, repositoryPath)
	if err != nil {
		return false, err
	}
	key := repositoryDatabaseKey(repositoryID)
	lock, err := acquireRepositoryInstanceLock(homeDirectory, key)
	if err != nil {
		return false, err
	}
	defer lock.Close()

	databasePath := filepath.Join(homeDirectory, "state", "repositories", key+".sqlite")
	paths := []string{databasePath, databasePath + "-wal", databasePath + "-shm"}
	found := false
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			found = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("inspect database file %s: %w", path, err)
		}
	}
	if !found {
		return false, nil
	}
	approved, err := confirm(databasePath)
	if err != nil || !approved {
		return false, err
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("remove database file %s: %w", path, err)
		}
	}
	return true, nil
}
