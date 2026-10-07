package localapp

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRepositoryInstanceLockReleasedWhenProcessExits(t *testing.T) {
	if os.Getenv("HOLARK_REPOSITORY_LOCK_CHILD") == "1" {
		if _, err := acquireRepositoryInstanceLock(os.Getenv("HOLARK_REPOSITORY_LOCK_HOME"), "repository"); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}

	home := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestRepositoryInstanceLockReleasedWhenProcessExits$")
	command.Env = append(os.Environ(), "HOLARK_REPOSITORY_LOCK_CHILD=1", "HOLARK_REPOSITORY_LOCK_HOME="+home)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("lock owner subprocess: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "repositories", "repository.lock")); err != nil {
		t.Fatalf("persistent lock file after process exit: %v", err)
	}

	lock, err := acquireRepositoryInstanceLock(home, "repository")
	if err != nil {
		t.Fatalf("acquire after process exit: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}
