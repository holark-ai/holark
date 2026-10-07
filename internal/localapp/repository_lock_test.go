package localapp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryInstanceLockExcludesSameRepository(t *testing.T) {
	home := t.TempDir()
	first, err := acquireRepositoryInstanceLock(home, "repository")
	if err != nil {
		t.Fatal(err)
	}

	second, err := acquireRepositoryInstanceLock(home, "repository")
	if second != nil {
		_ = second.Close()
		t.Fatal("second acquisition unexpectedly succeeded")
	}
	if !errors.Is(err, ErrRepositoryAlreadyRunning) {
		t.Fatalf("second acquisition error = %v, want %v", err, ErrRepositoryAlreadyRunning)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(home, "state", "repositories", "repository.lock")
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("persistent lock file: %v", err)
	}

	afterRelease, err := acquireRepositoryInstanceLock(home, "repository")
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := afterRelease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryInstanceLockAllowsDifferentRepositories(t *testing.T) {
	home := t.TempDir()
	first, err := acquireRepositoryInstanceLock(home, "first")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second, err := acquireRepositoryInstanceLock(home, "second")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
}
