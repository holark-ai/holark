package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectSubmoduleContentsPreservesPatch(t *testing.T) {
	remote := localRemote(t, "submodule")
	worktree := t.TempDir()
	runGit(t, worktree, "init", "--initial-branch=main")
	runGit(t, worktree, "config", "user.name", "Holark Test")
	runGit(t, worktree, "config", "user.email", "test@example.com")
	runGit(t, worktree, "commit", "--allow-empty", "-m", "initial")
	initial := strings.TrimSpace(worktreeGitOutput(t, worktree, "rev-parse", "HEAD"))
	const path = "modules/submodule"
	runGit(t, worktree, "-c", "protocol.file.allow=always", "submodule", "add", "-b", "main", remote, path)
	runGit(t, worktree, "commit", "-am", "add submodule")
	base := strings.TrimSpace(worktreeGitOutput(t, worktree, "rev-parse", "HEAD"))
	submodule := filepath.Join(worktree, path)
	if err := os.WriteFile(filepath.Join(submodule, "README.md"), []byte("updated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, submodule, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-am", "update submodule")
	runGit(t, worktree, "commit", "-am", "update gitlink")
	head := strings.TrimSpace(worktreeGitOutput(t, worktree, "rev-parse", "HEAD"))

	for _, test := range []struct {
		name, base, target string
	}{
		{"added", initial, "commit:" + base},
		{"modified", base, "commit:" + head},
		{"deleted", base, "commit:" + initial},
		{"worktree", base, "worktree"},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := InspectOptions{BaseRef: "commit:" + test.base, TargetRef: test.target, Path: path}
			patch, err := InspectWithOptions(t.Context(), worktree, "main", "main", initial, options)
			if err != nil {
				t.Fatal(err)
			}
			options.Contents = true
			inspection, err := InspectWithOptions(t.Context(), worktree, "main", "main", initial, options)
			if err != nil {
				t.Fatal(err)
			}
			if len(inspection.Files) != 1 || len(patch.Files) != 1 {
				t.Fatalf("expected one submodule diff, got %+v and %+v", inspection.Files, patch.Files)
			}
			file := inspection.Files[0]
			if file.ContentStatus != "unsupported" || file.Contents != nil {
				t.Fatalf("submodule contents = %+v", file)
			}
			if file.Diff != patch.Files[0].Diff || !strings.Contains(file.Diff, "Subproject commit ") || len(file.Diff) > maxTotalDiffBytes || file.DiffTruncated != patch.Files[0].DiffTruncated {
				t.Fatalf("submodule patch was not preserved: %+v", file)
			}
		})
	}
}
