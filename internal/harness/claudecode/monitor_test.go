package claudecode

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOutsideWorktreeDiagnosticBoundary(t *testing.T) {
	worktree := filepath.Join(string(os.PathSeparator), "tmp", "worktree")
	if outsideWorktree(worktree, filepath.Join(worktree, "src")) {
		t.Fatal("worktree child was treated as outside")
	}
	if !outsideWorktree(worktree, filepath.Join(string(os.PathSeparator), "tmp", "other")) {
		t.Fatal("outside directory was not diagnosed")
	}
}
