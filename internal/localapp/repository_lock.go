package localapp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/holark-ai/holark/internal/filelock"
)

// ErrRepositoryAlreadyRunning reports that another application owns the
// repository state in the configured Holark home directory.
var ErrRepositoryAlreadyRunning = errors.New("already running for this repository")

type repositoryInstanceLock = filelock.Lock

func acquireRepositoryInstanceLock(homeDirectory, repositoryKey string) (*repositoryInstanceLock, error) {
	directory := filepath.Join(homeDirectory, "state", "repositories")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create repository state directory: %w", err)
	}
	lock, err := filelock.Acquire(filepath.Join(directory, repositoryKey+".lock"))
	if errors.Is(err, filelock.ErrLocked) {
		return nil, ErrRepositoryAlreadyRunning
	}
	if err != nil {
		return nil, fmt.Errorf("lock repository: %w", err)
	}
	return lock, nil
}
