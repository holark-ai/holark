package workspace

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/holark-ai/holark/internal/protocol"
)

// Verifies sessions get separate worktrees and per-project bare repositories.
func TestPrepareIsolatedWorktreesAndProjectMirrors(t *testing.T) {
	remoteOne := localRemote(t, "one")
	remoteTwo := localRemote(t, "two")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}

	first, err := manager.Prepare(context.Background(), "one", "session-1", remoteOne, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Prepare(context.Background(), "one", "session-2", remoteOne, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	third, err := manager.Prepare(context.Background(), "two", "session-3", remoteTwo, "main", "")
	if err != nil {
		t.Fatal(err)
	}

	if first.Path == second.Path || first.Branch == second.Branch {
		t.Fatal("sessions did not receive isolated worktrees and branches")
	}
	for _, prepared := range []Prepared{first, second, third} {
		if _, err := os.Stat(filepath.Join(prepared.Path, "README.md")); err != nil {
			t.Fatalf("prepared worktree %q: %v", prepared.Path, err)
		}
	}
	root := manager.root
	for _, mirror := range []string{"one.git", "two.git"} {
		if _, err := os.Stat(filepath.Join(root, "mirrors", mirror)); err != nil {
			t.Fatalf("mirror %q: %v", mirror, err)
		}
	}
}

func TestPrepareDoesNotInstallHolarkSkill(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(prepared.Path, ".agents", "skills", "holark-integration")); !os.IsNotExist(err) {
		t.Fatalf("prepared worktree skill directory error = %v, want not installed", err)
	}
	if status := strings.TrimSpace(worktreeGitOutput(t, prepared.Path, "status", "--porcelain=v1")); status != "" {
		t.Fatalf("prepared worktree status = %q, want clean", status)
	}
}

func TestInstallSkillStagesLayersReplacesTargetAndKeepsGitStatusClean(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	install := SkillInstall{
		TargetDir: ".agents/skills/holark-integration",
		Sources: []SkillSource{
			{FS: fstest.MapFS{
				"base/SKILL.md":      {Data: []byte("base skill")},
				"base/shared.txt":    {Data: []byte("base shared")},
				"base/base-only.txt": {Data: []byte("base only")},
			}, Root: "base"},
			{FS: fstest.MapFS{
				"overlay/shared.txt":         {Data: []byte("overlay shared")},
				"overlay/agents/openai.yaml": {Data: []byte("display: Holark")},
			}, Root: "overlay"},
		},
	}

	if err := manager.InstallSkill(context.Background(), prepared.Path, install); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(prepared.Path, ".agents", "skills", "holark-integration")
	assertFileContent(t, filepath.Join(target, "SKILL.md"), "base skill")
	assertFileContent(t, filepath.Join(target, "shared.txt"), "overlay shared")
	assertFileContent(t, filepath.Join(target, "base-only.txt"), "base only")
	assertFileContent(t, filepath.Join(target, "agents", "openai.yaml"), "display: Holark")
	if err := os.WriteFile(filepath.Join(target, "stale.txt"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.InstallSkill(context.Background(), prepared.Path, install); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale skill file error = %v, want removed", err)
	}
	if status := strings.TrimSpace(worktreeGitOutput(t, prepared.Path, "status", "--porcelain=v1")); status != "" {
		t.Fatalf("worktree status = %q, want clean", status)
	}
}

func TestInstallSkillCopyFailurePreservesExistingTarget(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	install := SkillInstall{
		TargetDir: ".agents/skills/holark-integration",
		Sources:   []SkillSource{{FS: fstest.MapFS{"skill/SKILL.md": {Data: []byte("existing")}}, Root: "skill"}},
	}
	if err := manager.InstallSkill(context.Background(), prepared.Path, install); err != nil {
		t.Fatal(err)
	}

	broken := SkillInstall{
		TargetDir: ".agents/skills/holark-integration",
		Sources:   []SkillSource{{FS: fstest.MapFS{"other/SKILL.md": {Data: []byte("new")}}, Root: "missing"}},
	}
	if err := manager.InstallSkill(context.Background(), prepared.Path, broken); err == nil {
		t.Fatal("broken install succeeded")
	}
	assertFileContent(t, filepath.Join(prepared.Path, ".agents", "skills", "holark-integration", "SKILL.md"), "existing")
}

func TestInstallSkillSweepsStaleStagingSiblings(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(prepared.Path, ".agents", "skills")
	for _, name := range []string{".holark-integration.tmp-leftover", ".holark-integration.old-leftover"} {
		if err := os.MkdirAll(filepath.Join(parent, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(parent, name, "stale.txt"), []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	install := SkillInstall{
		TargetDir: ".agents/skills/holark-integration",
		Sources:   []SkillSource{{FS: fstest.MapFS{"skill/SKILL.md": {Data: []byte("skill")}}, Root: "skill"}},
	}

	if err := manager.InstallSkill(context.Background(), prepared.Path, install); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".holark-integration.tmp-leftover", ".holark-integration.old-leftover"} {
		if _, err := os.Stat(filepath.Join(parent, name)); !os.IsNotExist(err) {
			t.Fatalf("stale sibling %s error = %v, want removed", name, err)
		}
	}
	if status := strings.TrimSpace(worktreeGitOutput(t, prepared.Path, "status", "--porcelain=v1")); status != "" {
		t.Fatalf("worktree status = %q, want clean", status)
	}
}

func TestInstallSkillInstallsTwoHarnessTargetsInSameWorktree(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	codexInstall := SkillInstall{
		TargetDir: ".agents/skills/holark-integration",
		Sources:   []SkillSource{{FS: fstest.MapFS{"skill/SKILL.md": {Data: []byte("codex")}}, Root: "skill"}},
	}
	claudeInstall := SkillInstall{
		TargetDir: ".claude/skills/holark-integration",
		Sources:   []SkillSource{{FS: fstest.MapFS{"skill/SKILL.md": {Data: []byte("claude")}}, Root: "skill"}},
	}

	if err := manager.InstallSkill(context.Background(), prepared.Path, codexInstall); err != nil {
		t.Fatal(err)
	}
	if err := manager.InstallSkill(context.Background(), prepared.Path, claudeInstall); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(prepared.Path, ".agents", "skills", "holark-integration", "SKILL.md"), "codex")
	assertFileContent(t, filepath.Join(prepared.Path, ".claude", "skills", "holark-integration", "SKILL.md"), "claude")
	exclude, err := os.ReadFile(resolvedGitExcludePath(t, prepared.Path))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{".agents/skills/holark-integration/", ".claude/skills/holark-integration/"} {
		if count := strings.Count(string(exclude), line); count != 1 {
			t.Fatalf("exclude line %q count = %d in %q, want 1", line, count, exclude)
		}
	}
	if status := strings.TrimSpace(worktreeGitOutput(t, prepared.Path, "status", "--porcelain=v1")); status != "" {
		t.Fatalf("worktree status = %q, want clean", status)
	}
}

func TestInstallSkillDedupesGitExclude(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	install := SkillInstall{
		TargetDir: ".claude/skills/holark-integration",
		Sources:   []SkillSource{{FS: fstest.MapFS{"skill/SKILL.md": {Data: []byte("skill")}}, Root: "skill"}},
	}

	if err := manager.InstallSkill(context.Background(), prepared.Path, install); err != nil {
		t.Fatal(err)
	}
	if err := manager.InstallSkill(context.Background(), prepared.Path, install); err != nil {
		t.Fatal(err)
	}
	excludePath := resolvedGitExcludePath(t, prepared.Path)
	exclude, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(exclude), ".claude/skills/holark-integration/"); count != 1 {
		t.Fatalf("exclude line count = %d in %q, want 1", count, exclude)
	}
}

func TestInspectReportsCurrentGitBranchAfterRename(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, prepared.Path, "branch", "-m", "holark/renamed-flow")

	inspection, err := Inspect(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Branch != "holark/renamed-flow" {
		t.Fatalf("inspection branch = %q, want renamed branch", inspection.Branch)
	}
}

func TestInspectRejectsDetachedHead(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, prepared.Path, "checkout", "--detach", "HEAD")

	_, err = Inspect(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit)
	if !errors.Is(err, ErrDetachedHead) {
		t.Fatalf("Inspect error = %v, want %v", err, ErrDetachedHead)
	}
}

func TestInspectSummaryOmitsPatchesAndReturnsRefs(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Path, "README.md"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Path, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Path, "large.txt"), []byte(strings.Repeat("x", maxUntrackedBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}

	inspection, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, InspectOptions{SummaryOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.SummaryOnly || inspection.SelectedBaseRef != "session-start" || inspection.SelectedTargetRef != "worktree" {
		t.Fatalf("range metadata = %+v", inspection)
	}
	if inspection.BranchBaseCommit != prepared.BaseCommit || inspection.WorkSessionStartCommit != prepared.BaseCommit || inspection.WorkspaceHeadCommit != prepared.HeadCommit || len(inspection.Commits) != 0 {
		t.Fatalf("history metadata = %+v, want existing-session fallback to recorded base", inspection)
	}
	if !inspection.Dirty || !inspection.HasChanges || len(inspection.Files) != 3 {
		t.Fatalf("inspection = %+v", inspection)
	}
	for _, file := range inspection.Files {
		if file.Diff != "" {
			t.Fatalf("summary file %s included patch", file.Path)
		}
	}
	newFile := fileByPath(inspection.Files, "new.txt")
	if newFile == nil || newFile.Additions != 1 || newFile.Deletions != 0 || newFile.Binary || newFile.DiffTruncated {
		t.Fatalf("new.txt summary = %+v, want exact stats without a patch", newFile)
	}
	largeFile := fileByPath(inspection.Files, "large.txt")
	if largeFile == nil || !largeFile.DiffTruncated || largeFile.Additions != 1 || largeFile.Deletions != 0 {
		t.Fatalf("large.txt summary = %+v, want exact stats with truncated patch metadata", largeFile)
	}
	if !hasDiffRef(inspection.RefOptions, "session-start") || !hasDiffRef(inspection.RefOptions, "worktree") || !hasDiffRef(inspection.RefOptions, "main") {
		t.Fatalf("ref options = %+v", inspection.RefOptions)
	}
}

func TestInspectIgnoresGitLineEndingWarnings(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, prepared.Path, "config", "core.autocrlf", "true")
	runGit(t, prepared.Path, "config", "core.safecrlf", "warn")
	if err := os.WriteFile(filepath.Join(prepared.Path, "README.md"), []byte("updated\nextra\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	inspection, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, InspectOptions{SummaryOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Files) != 1 {
		t.Fatalf("files = %+v, want one modified file", inspection.Files)
	}
	file := inspection.Files[0]
	if file.Path != "README.md" || file.Status != "M" || file.Additions != 2 || file.Deletions != 1 {
		t.Fatalf("file = %+v, want README.md modified with 2 additions and 1 deletion", file)
	}
}

func TestInspectUntrackedSymlinksAsLinkBlobs(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}

	outsidePath := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("target contents must not be inspected\nsecond line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	links := []struct {
		path   string
		target string
	}{
		{path: "regular-link", target: outsidePath},
		{path: "dangling-link", target: "missing-target"},
	}
	for _, link := range links {
		if err := os.Symlink(link.target, filepath.Join(prepared.Path, link.path)); err != nil {
			t.Skipf("create symlink: %v", err)
		}
	}

	options := InspectOptions{BaseRef: "commit:" + prepared.HeadCommit, TargetRef: "worktree", SummaryOnly: true}
	summary, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Files) != len(links) {
		t.Fatalf("summary files = %+v, want both symlinks", summary.Files)
	}
	for _, link := range links {
		file := fileByPath(summary.Files, link.path)
		if file == nil || file.Additions != 1 || file.Deletions != 0 || file.Binary || file.Diff != "" || file.DiffTruncated {
			t.Fatalf("%s summary = %+v, want one-line link blob", link.path, file)
		}
	}

	options.SummaryOnly = false
	inspection, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Files) != len(links) {
		t.Fatalf("inspection files = %+v, want both symlinks", inspection.Files)
	}
	for _, link := range links {
		file := fileByPath(inspection.Files, link.path)
		if file == nil || file.Additions != 1 || file.Deletions != 0 || file.Binary || file.DiffTruncated {
			t.Fatalf("%s inspection = %+v, want one-line link blob", link.path, file)
		}
		if !strings.Contains(file.Diff, "new file mode 120000") || !strings.Contains(file.Diff, "+"+link.target) {
			t.Fatalf("%s diff = %q, want mode 120000 and link target", link.path, file.Diff)
		}
	}
	if strings.Contains(fileByPath(inspection.Files, "regular-link").Diff, "target contents must not be inspected") {
		t.Fatal("regular symlink diff included the linked file contents")
	}
}

func TestInspectLargeUntrackedFileReturnsExactStatsWithTruncatedPreview(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	const lineCount = 4000
	line := strings.Repeat("x", 32) + "\n"
	if err := os.WriteFile(filepath.Join(prepared.Path, "large.txt"), []byte(strings.Repeat(line, lineCount)), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, InspectOptions{
		BaseRef:     "commit:" + prepared.HeadCommit,
		TargetRef:   "worktree",
		SummaryOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Files) != 1 || summary.Files[0].Additions != lineCount || summary.Files[0].Deletions != 0 || summary.Files[0].Diff != "" || !summary.Files[0].DiffTruncated {
		t.Fatalf("summary = %+v, want +%d with a truncated patch", summary, lineCount)
	}

	selected, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, InspectOptions{
		BaseRef:   "commit:" + prepared.HeadCommit,
		TargetRef: "worktree",
		Path:      "large.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Files) != 1 {
		t.Fatalf("selected files = %+v, want large.txt", selected.Files)
	}
	selectedFile := selected.Files[0]
	if selectedFile.Additions != lineCount || selectedFile.Deletions != 0 || selectedFile.Diff == "" || !selectedFile.DiffTruncated || !selected.DiffTruncated {
		t.Fatalf("selected file = %+v, want exact +%d and a truncated preview", selectedFile, lineCount)
	}
	if len(selectedFile.Diff) > maxTotalDiffBytes || !strings.Contains(selectedFile.Diff, "@@ -0,0 +1,4000 @@") || !strings.Contains(selectedFile.Diff, "+"+line) {
		t.Fatalf("selected patch length/content = %d/%q", len(selectedFile.Diff), selectedFile.Diff)
	}

	reversed, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, InspectOptions{
		BaseRef:   "worktree",
		TargetRef: "commit:" + prepared.HeadCommit,
		Path:      "large.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reversed.Files) != 1 {
		t.Fatalf("reversed files = %+v, want large.txt", reversed.Files)
	}
	reversedFile := reversed.Files[0]
	if reversedFile.Additions != 0 || reversedFile.Deletions != lineCount || reversedFile.Diff == "" || !reversedFile.DiffTruncated || !reversed.DiffTruncated {
		t.Fatalf("reversed file = %+v, want exact -%d and a truncated preview", reversedFile, lineCount)
	}
	if len(reversedFile.Diff) > maxTotalDiffBytes || !strings.Contains(reversedFile.Diff, "@@ -1,4000 +0,0 @@") || !strings.Contains(reversedFile.Diff, "-"+line) {
		t.Fatalf("reversed patch length/content = %d/%q", len(reversedFile.Diff), reversedFile.Diff)
	}
}

func TestInspectLargeSingleLineUntrackedFileReturnsTruncatedPreview(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	contents := strings.Repeat("x", maxTotalDiffBytes+1)
	if err := os.WriteFile(filepath.Join(prepared.Path, "large.txt"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		baseRef   string
		targetRef string
		reverse   bool
		marker    string
		additions int
		deletions int
	}{
		{
			name: "added", baseRef: "commit:" + prepared.HeadCommit, targetRef: "worktree",
			marker: "+", additions: 1,
		},
		{
			name: "deleted", baseRef: "worktree", targetRef: "commit:" + prepared.HeadCommit,
			reverse: true, marker: "-", deletions: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspection, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, InspectOptions{
				BaseRef: test.baseRef, TargetRef: test.targetRef, Path: "large.txt",
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(inspection.Files) != 1 {
				t.Fatalf("files = %+v, want large.txt", inspection.Files)
			}
			file := inspection.Files[0]
			if file.Additions != test.additions || file.Deletions != test.deletions || !file.DiffTruncated || !inspection.DiffTruncated {
				t.Fatalf("file = %+v, want +%d/-%d with a truncated preview", file, test.additions, test.deletions)
			}
			header := untrackedFileDiffHeader("large.txt", 1, "100644", test.reverse)
			wantDiff := header + test.marker + contents[:maxTotalDiffBytes-len(header)-len(test.marker)]
			if file.Diff != wantDiff {
				t.Fatalf("patch length = %d, want %d with a prefixed content preview", len(file.Diff), len(wantDiff))
			}
		})
	}
}

func TestInspectSelectedFileUsesIndependentPatchBudget(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Repeat("first line\n", 5000) + "first tail\n"
	second := strings.Repeat("second line\n", 5000) + "second tail\n"
	if err := os.WriteFile(filepath.Join(prepared.Path, "first.txt"), []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Path, "second.txt"), []byte(second), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, prepared.Path, "add", "first.txt", "second.txt")
	runGit(t, prepared.Path, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "large files")

	aggregate, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !aggregate.DiffTruncated {
		t.Fatal("aggregate diff was not truncated")
	}

	selected, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, InspectOptions{Path: "second.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if selected.DiffTruncated || len(selected.Files) != 1 || selected.Files[0].Path != "second.txt" || selected.Files[0].DiffTruncated {
		t.Fatalf("selected inspection = %+v", selected)
	}
	if !strings.Contains(selected.Files[0].Diff, "second tail") {
		t.Fatal("selected patch is incomplete")
	}
}

func TestInspectRangeSelections(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(prepared.Path, "first.txt"), []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, prepared.Path, "add", "first.txt")
	runGit(t, prepared.Path, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "first")
	commitOne := strings.TrimSpace(worktreeGitOutput(t, prepared.Path, "rev-parse", "--verify", "HEAD"))

	firstRange, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, InspectOptions{TargetRef: "commit:" + commitOne})
	if err != nil {
		t.Fatal(err)
	}
	if firstRange.HeadCommit != commitOne || firstRange.Dirty || len(firstRange.Files) != 1 || firstRange.Files[0].Path != "first.txt" || firstRange.Files[0].Diff == "" {
		t.Fatalf("session-start -> commit = %+v", firstRange)
	}

	if err := os.WriteFile(filepath.Join(prepared.Path, "second.txt"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, prepared.Path, "add", "second.txt")
	runGit(t, prepared.Path, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "second")
	commitTwo := strings.TrimSpace(worktreeGitOutput(t, prepared.Path, "rev-parse", "--verify", "HEAD"))

	commitRange, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, InspectOptions{BaseRef: "commit:" + commitOne, TargetRef: "commit:" + commitTwo})
	if err != nil {
		t.Fatal(err)
	}
	if commitRange.BaseCommit != commitOne || commitRange.HeadCommit != commitTwo || commitRange.Dirty || len(commitRange.Files) != 1 || commitRange.Files[0].Path != "second.txt" {
		t.Fatalf("commit -> commit = %+v", commitRange)
	}

	if err := os.WriteFile(filepath.Join(prepared.Path, "second.txt"), []byte("second changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Path, "untracked.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktreeRange, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, InspectOptions{BaseRef: "commit:" + commitTwo, TargetRef: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	if worktreeRange.BaseCommit != commitTwo || worktreeRange.HeadCommit != commitTwo || !worktreeRange.Dirty || len(worktreeRange.Files) != 2 {
		t.Fatalf("commit -> worktree = %+v", worktreeRange)
	}
	if !hasFile(worktreeRange.Files, "second.txt") || !hasFile(worktreeRange.Files, "untracked.txt") {
		t.Fatalf("worktree files = %+v", worktreeRange.Files)
	}
}

func TestInspectReturnsNewestFirstFirstParentHistoryAndSessionBoundary(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-history", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, prepared.Path, "config", "core.hooksPath", t.TempDir())

	commitFile := func(path, contents, subject string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(prepared.Path, path), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, prepared.Path, "add", path)
		runGit(t, prepared.Path, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", subject)
		return strings.TrimSpace(worktreeGitOutput(t, prepared.Path, "rev-parse", "--verify", "HEAD"))
	}

	sessionStart := commitFile("pre-session.txt", "before session\n", "pre-session setup")
	firstSession := commitFile("session.txt", "session\n", "first session change\n\nFirst paragraph.\n\nSecond paragraph with a tab\there.")
	runGit(t, prepared.Path, "checkout", "-b", "side-history")
	sideCommit := commitFile("side.txt", "side\n", "side branch change")
	runGit(t, prepared.Path, "checkout", prepared.Branch)
	mainline := commitFile("mainline.txt", "mainline\n", "mainline change")
	runGit(t, prepared.Path, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "merge", "--no-ff", "side-history", "-m", "merge side branch")
	mergeCommit := strings.TrimSpace(worktreeGitOutput(t, prepared.Path, "rev-parse", "--verify", "HEAD"))

	inspection, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, InspectOptions{
		WorkSessionStartCommit: sessionStart,
		SummaryOnly:            true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.BranchBaseCommit != prepared.BaseCommit || inspection.WorkSessionStartCommit != sessionStart || inspection.WorkspaceHeadCommit != mergeCommit {
		t.Fatalf("history boundaries = base %q start %q head %q", inspection.BranchBaseCommit, inspection.WorkSessionStartCommit, inspection.WorkspaceHeadCommit)
	}
	if inspection.SelectedBaseCommit != sessionStart || inspection.SelectedTargetCommit != mergeCommit {
		t.Fatalf("default selected range = %q..%q, want %q..%q", inspection.SelectedBaseCommit, inspection.SelectedTargetCommit, sessionStart, mergeCommit)
	}
	expected := []protocol.WorkspaceCommit{
		{SHA: mergeCommit, ParentCommit: mainline, Subject: "merge side branch"},
		{SHA: mainline, ParentCommit: firstSession, Subject: "mainline change"},
		{SHA: firstSession, ParentCommit: sessionStart, Subject: "first session change"},
		{SHA: sessionStart, ParentCommit: prepared.BaseCommit, Subject: "pre-session setup"},
	}
	if len(inspection.Commits) != len(expected) {
		t.Fatalf("first-parent history = %+v, want %+v", inspection.Commits, expected)
	}
	for index := range expected {
		expected[index].Author = "Holark Test"
		expected[index].AuthoredAt = strings.TrimSpace(worktreeGitOutput(t, prepared.Path, "show", "-s", "--format=%aI", expected[index].SHA))
		if expected[index].SHA == firstSession {
			expected[index].Body = "First paragraph.\n\nSecond paragraph with a tab\there."
		}
		if inspection.Commits[index] != expected[index] {
			t.Fatalf("commit %d = %+v, want %+v", index, inspection.Commits[index], expected[index])
		}
		if inspection.Commits[index].SHA == sideCommit {
			t.Fatalf("side-branch commit appeared as a separate history row: %+v", inspection.Commits)
		}
	}

	fallback, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, InspectOptions{
		WorkSessionStartCommit: sideCommit,
		SummaryOnly:            true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.WorkSessionStartCommit != prepared.BaseCommit || fallback.SelectedBaseCommit != prepared.BaseCommit {
		t.Fatalf("rewritten/non-first-parent session boundary = %q selected %q, want recorded base %q", fallback.WorkSessionStartCommit, fallback.SelectedBaseCommit, prepared.BaseCommit)
	}
}

func TestInspectInvalidRefReturnsStableError(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, InspectOptions{BaseRef: "commit:notasha"})
	if !errors.Is(err, ErrInvalidDiffRef) || !strings.Contains(err.Error(), "commit:notasha") {
		t.Fatalf("invalid ref error = %v", err)
	}
}

func hasDiffRef(refs []protocol.WorkspaceDiffRef, id string) bool {
	for _, ref := range refs {
		if ref.ID == id {
			return true
		}
	}
	return false
}

func hasFile(files []protocol.WorkspaceFileDiff, path string) bool {
	return fileByPath(files, path) != nil
}

func fileByPath(files []protocol.WorkspaceFileDiff, path string) *protocol.WorkspaceFileDiff {
	for index := range files {
		if files[index].Path == path {
			return &files[index]
		}
	}
	return nil
}

// Verifies Git command failures keep enough detail for node-side debugging.
func TestPreparePreservesGitFailureDetails(t *testing.T) {
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}

	missingRemote := filepath.Join(t.TempDir(), "missing.git")
	_, err = manager.Prepare(context.Background(), "one", "session-1", missingRemote, "main", "")
	if err == nil {
		t.Fatal("Prepare succeeded with a missing remote")
	}
	assertErrorContains(t, err, "fetch base branch", "git --git-dir", "fatal:")
}

// Verifies filesystem failures include the failing path and operation.
func TestPreparePreservesFilesystemFailureDetails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}

	_, err = manager.Prepare(context.Background(), "one", "session-1", "/tmp/missing.git", "main", "")
	if err == nil {
		t.Fatal("Prepare succeeded with a file as the workspace root")
	}
	assertErrorContains(t, err, "create mirror directory", root)
}

// Verifies preparation uses the requested base branch and removes the staging ref.
func TestPrepareFetchesSelectedBaseBranch(t *testing.T) {
	remote := localRemote(t, "project")
	source := strings.TrimSuffix(remote, ".git") + "-source"
	runGit(t, source, "checkout", "-b", "release/2.0")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "release")
	runGit(t, source, "push", "origin", "release/2.0")

	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-release", remote, "release/2.0", "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(prepared.Path, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "release\n" {
		t.Fatalf("README = %q, want release branch content", data)
	}
	if refExists(t, filepath.Join(manager.root, "mirrors", "project.git"), "refs/holark/fetch/session-release") {
		t.Fatal("staging ref was retained after successful preparation")
	}
}

func TestPrepareRefreshesCachedBaseBranchRefWhileInspectionUsesRecordedBase(t *testing.T) {
	remote := localRemote(t, "project")
	source := strings.TrimSuffix(remote, ".git") + "-source"
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}

	first, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("base advanced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "advance base")
	newBase := strings.TrimSpace(gitOutput(t, source+"/.git", "rev-parse", "--verify", "HEAD"))
	runGit(t, source, "push", "origin", "main")

	second, err := manager.Prepare(context.Background(), "project", "session-2", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if second.BaseCommit != newBase {
		t.Fatalf("second base commit = %q, want %q", second.BaseCommit, newBase)
	}
	mirror := filepath.Join(manager.root, "mirrors", "project.git")
	cached := strings.TrimSpace(gitOutput(t, mirror, "rev-parse", "--verify", "refs/remotes/cache/main"))
	if cached != newBase {
		t.Fatalf("cached base ref = %q, want %q", cached, newBase)
	}

	inspection, err := InspectWithOptions(context.Background(), first.Path, first.Branch, "main", first.BaseCommit, InspectOptions{BaseRef: "main", TargetRef: "worktree", SummaryOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.SelectedBaseRef != "main" || inspection.SelectedBaseCommit != first.BaseCommit {
		t.Fatalf("selected base = %s %s, want recorded main base %s", inspection.SelectedBaseRef, inspection.SelectedBaseCommit, first.BaseCommit)
	}
}

func TestPrepareConfiguresHolarkRemoteMetadataAndHook(t *testing.T) {
	holarkRemote := localRemote(t, "holark")
	githubURL := "git@github.com:owner/project.git"
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}

	sshOptions := GitSSHOptions{KeyPath: filepath.Join(manager.root, "git_ssh", "id_ed25519"), KnownHostsPath: filepath.Join(manager.root, "git_ssh", "known_hosts")}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", githubURL, "main", holarkRemote, sshOptions)
	if err != nil {
		t.Fatal(err)
	}

	mirror := filepath.Join(manager.root, "mirrors", "project.git")
	if cacheRemote := strings.TrimSpace(gitOutput(t, mirror, "remote", "get-url", "cache")); cacheRemote != holarkRemote {
		t.Fatalf("mirror cache = %q, want Holark remote %q", cacheRemote, holarkRemote)
	}
	if origin := strings.TrimSpace(worktreeGitOutput(t, prepared.Path, "remote", "get-url", "origin")); origin != githubURL {
		t.Fatalf("worktree origin = %q, want GitHub URL %q", origin, githubURL)
	}
	assertWorktreeConfig(t, prepared.Path, "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	if holark := strings.TrimSpace(worktreeGitOutput(t, prepared.Path, "remote", "get-url", "holark")); holark != holarkRemote {
		t.Fatalf("worktree holark = %q, want %q", holark, holarkRemote)
	}
	assertWorktreeConfig(t, prepared.Path, "holark.sessionId", "session-1")
	assertWorktreeConfig(t, prepared.Path, "holark.projectSlug", "project")
	assertWorktreeConfig(t, prepared.Path, "holark.autopushRef", "refs/holark/sessions/session-1")
	assertWorktreeConfig(t, prepared.Path, "holark.remote", "holark")
	assertWorktreeConfig(t, prepared.Path, "holark.autoPush", "true")
	assertWorktreeConfig(t, prepared.Path, "holark.sshKeyPath", sshOptions.KeyPath)
	assertWorktreeConfig(t, prepared.Path, "holark.knownHostsPath", sshOptions.KnownHostsPath)

	hooksPath := strings.TrimSpace(worktreeGitOutput(t, prepared.Path, "config", "--get", "core.hooksPath"))
	hook, err := os.ReadFile(filepath.Join(hooksPath, "post-commit"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(hook)
	for _, expected := range []string{"holark.autoPush", "holark.autopushRef", "holark.remote", "holark.sshKeyPath", "holark.knownHostsPath", "-o StrictHostKeyChecking=yes", "UserKnownHostsFile"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("hook does not contain %q:\n%s", expected, content)
		}
	}
	if strings.Contains(content, "origin") {
		t.Fatalf("hook must not mention origin:\n%s", content)
	}
}

func TestPostCommitHookPushesOnlyToHolarkRemote(t *testing.T) {
	originRemote := localRemote(t, "github")
	holarkRemote := localRemote(t, "holark")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-hook", originRemote, "main", holarkRemote, GitSSHOptions{
		KeyPath: filepath.Join(manager.root, "git_ssh", "id_ed25519"), KnownHostsPath: filepath.Join(manager.root, "git_ssh", "known_hosts"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(prepared.Path, "agent.txt"), []byte("agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, prepared.Path, "add", "agent.txt")
	runGit(t, prepared.Path, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "agent change")

	head := strings.TrimSpace(worktreeGitOutput(t, prepared.Path, "rev-parse", "--verify", "HEAD"))
	holarkHead := strings.TrimSpace(gitOutput(t, holarkRemote, "rev-parse", "--verify", "refs/holark/sessions/session-hook"))
	if holarkHead != head {
		t.Fatalf("holark autopush ref = %q, want HEAD %q", holarkHead, head)
	}
	if refExists(t, originRemote, "refs/holark/sessions/session-hook") {
		t.Fatal("origin received the Holark autopush ref")
	}
	if refExists(t, originRemote, "refs/heads/"+prepared.Branch) {
		t.Fatal("origin received the session branch")
	}
}

// Reproduces the retained-worktree case where mirror fetches used to fail.
func TestPrepareDoesNotUpdateCheckedOutRetainedBranch(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "agent.txt"), []byte("agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, first.Path, "add", "agent.txt")
	runGit(t, first.Path, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "agent change")
	runGit(t, first.Path, "push", "origin", first.Branch+":"+first.Branch)

	second, err := manager.Prepare(context.Background(), "project", "session-2", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Branch != "holark/session-2" {
		t.Fatalf("second branch = %q", second.Branch)
	}
}

func TestPrepareFromCommitCreatesReviewBranchAtHeadCommit(t *testing.T) {
	holarkRemote := localRemote(t, "project")
	source := strings.TrimSuffix(holarkRemote, ".git") + "-source"
	baseCommit := strings.TrimSpace(gitOutput(t, holarkRemote, "rev-parse", "--verify", "refs/heads/main"))
	runGit(t, source, "checkout", "-b", "feature/review")
	if err := os.WriteFile(filepath.Join(source, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "feature.txt")
	runGit(t, source, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "feature")
	headCommit := strings.TrimSpace(gitOutput(t, source+"/.git", "rev-parse", "--verify", "HEAD"))
	runGit(t, source, "push", "origin", "feature/review")

	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	githubURL := "git@github.com:owner/project.git"
	prepared, err := manager.PrepareFromCommit(context.Background(), "project", "session-review", githubURL, protocol.WorkspaceSpec{
		Mode: protocol.WorkspaceModeCommit, BaseBranch: "main", BaseCommit: baseCommit, HeadBranch: "feature/review", HeadCommit: headCommit, BranchPrefix: "holark/review",
	}, holarkRemote)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Branch != "holark/review/session-review" || prepared.BaseCommit != baseCommit {
		t.Fatalf("prepared = %+v", prepared)
	}
	mirror := filepath.Join(manager.root, "mirrors", "project.git")
	if cacheRemote := strings.TrimSpace(gitOutput(t, mirror, "remote", "get-url", "cache")); cacheRemote != holarkRemote {
		t.Fatalf("mirror cache = %q, want Holark remote %q", cacheRemote, holarkRemote)
	}
	worktreeHead := strings.TrimSpace(gitOutput(t, prepared.Path+"/.git", "rev-parse", "--verify", "HEAD"))
	if worktreeHead != headCommit {
		t.Fatalf("worktree head = %q, want %q", worktreeHead, headCommit)
	}
	currentBranch := strings.TrimSpace(gitOutput(t, prepared.Path+"/.git", "symbolic-ref", "--short", "HEAD"))
	if currentBranch != prepared.Branch {
		t.Fatalf("current branch = %q, want %q", currentBranch, prepared.Branch)
	}
	worktreeRoot := strings.TrimSpace(worktreeGitOutput(t, prepared.Path, "rev-parse", "--show-toplevel"))
	resolvedWorktreeRoot, err := filepath.EvalSymlinks(worktreeRoot)
	if err != nil {
		t.Fatal(err)
	}
	resolvedPreparedPath, err := filepath.EvalSymlinks(prepared.Path)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedWorktreeRoot != resolvedPreparedPath {
		t.Fatalf("worktree root = %q, want %q", worktreeRoot, prepared.Path)
	}
	assertWorktreeConfig(t, prepared.Path, "remote.origin.url", githubURL)
	assertWorktreeConfig(t, prepared.Path, "holark.sessionId", "session-review")
	assertWorktreeConfig(t, prepared.Path, "holark.projectSlug", "project")
}

func TestPrepareFromCommitRefreshesCachedBaseBranchRefWhileInspectionUsesRecordedBase(t *testing.T) {
	remote := localRemote(t, "project")
	source := strings.TrimSuffix(remote, ".git") + "-source"
	baseCommit := strings.TrimSpace(gitOutput(t, remote, "rev-parse", "--verify", "refs/heads/main"))
	runGit(t, source, "checkout", "-b", "feature/review")
	if err := os.WriteFile(filepath.Join(source, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "feature.txt")
	runGit(t, source, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "feature")
	headCommit := strings.TrimSpace(gitOutput(t, source+"/.git", "rev-parse", "--verify", "HEAD"))
	runGit(t, source, "push", "origin", "feature/review")
	runGit(t, source, "checkout", "main")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("base advanced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "advance base")
	newBase := strings.TrimSpace(gitOutput(t, source+"/.git", "rev-parse", "--verify", "HEAD"))
	runGit(t, source, "push", "origin", "main")

	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.PrepareFromCommit(context.Background(), "project", "session-review", remote, protocol.WorkspaceSpec{
		Mode: protocol.WorkspaceModeCommit, BaseBranch: "main", BaseCommit: baseCommit, HeadBranch: "feature/review", HeadCommit: headCommit, BranchPrefix: "holark/review",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(manager.root, "mirrors", "project.git")
	cached := strings.TrimSpace(gitOutput(t, mirror, "rev-parse", "--verify", "refs/remotes/cache/main"))
	if cached != newBase {
		t.Fatalf("cached base ref = %q, want %q", cached, newBase)
	}

	inspection, err := InspectWithOptions(context.Background(), prepared.Path, prepared.Branch, "main", prepared.BaseCommit, InspectOptions{BaseRef: "main", TargetRef: "worktree", SummaryOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.SelectedBaseRef != "main" || inspection.SelectedBaseCommit != baseCommit {
		t.Fatalf("selected base = %s %s, want recorded main base %s", inspection.SelectedBaseRef, inspection.SelectedBaseCommit, baseCommit)
	}
}

func TestRebaseReplaysWorkspaceOntoBaseCommit(t *testing.T) {
	remote := localRemote(t, "project")
	source := strings.TrimSuffix(remote, ".git") + "-source"

	runGit(t, source, "checkout", "-b", "feature/rebase")
	if err := os.WriteFile(filepath.Join(source, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "feature.txt")
	runGit(t, source, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "feature")
	headCommit := strings.TrimSpace(gitOutput(t, source+"/.git", "rev-parse", "--verify", "HEAD"))
	runGit(t, source, "push", "origin", "feature/rebase")

	runGit(t, source, "checkout", "main")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("base advanced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "advance base")
	newBase := strings.TrimSpace(gitOutput(t, source+"/.git", "rev-parse", "--verify", "HEAD"))
	runGit(t, source, "push", "origin", "main")

	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.PrepareFromCommit(context.Background(), "project", "session-rebase", remote, protocol.WorkspaceSpec{
		Mode: protocol.WorkspaceModeCommit, BaseBranch: "main", BaseCommit: newBase, HeadBranch: "feature/rebase", HeadCommit: headCommit, BranchPrefix: "holark/worker",
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	result, err := Rebase(context.Background(), prepared.Path, prepared.BaseCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Rebased || result.HeadCommit == "" || result.HeadCommit == headCommit {
		t.Fatalf("rebase result = %+v, original head = %s", result, headCommit)
	}
	if _, err := exec.Command("git", "-C", prepared.Path, "merge-base", "--is-ancestor", newBase, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("rebased HEAD is not based on %s: %v", newBase, err)
	}
}

func TestRebaseLeavesConflictedWorkspaceForAgent(t *testing.T) {
	remote := localRemote(t, "project")
	source := strings.TrimSuffix(remote, ".git") + "-source"

	runGit(t, source, "checkout", "-b", "feature/conflict")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "feature edit")
	headCommit := strings.TrimSpace(gitOutput(t, source+"/.git", "rev-parse", "--verify", "HEAD"))
	runGit(t, source, "push", "origin", "feature/conflict")

	runGit(t, source, "checkout", "main")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "base edit")
	newBase := strings.TrimSpace(gitOutput(t, source+"/.git", "rev-parse", "--verify", "HEAD"))
	runGit(t, source, "push", "origin", "main")

	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.PrepareFromCommit(context.Background(), "project", "session-conflict", remote, protocol.WorkspaceSpec{
		Mode: protocol.WorkspaceModeCommit, BaseBranch: "main", BaseCommit: newBase, HeadBranch: "feature/conflict", HeadCommit: headCommit, BranchPrefix: "holark/worker",
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = Rebase(context.Background(), prepared.Path, prepared.BaseCommit)
	if !errors.Is(err, ErrRebaseConflicts) {
		t.Fatalf("rebase error = %v, want ErrRebaseConflicts", err)
	}
	status := worktreeGitOutput(t, prepared.Path, "status", "--porcelain=v1")
	if !strings.Contains(status, "UU README.md") {
		t.Fatalf("status = %q, want README.md conflict", status)
	}
}

func TestPublishWorkspaceToOriginPushesReviewWorktreeToOriginUpstreamBranch(t *testing.T) {
	originRemote := localRemote(t, "origin")
	source := strings.TrimSuffix(originRemote, ".git") + "-source"
	holarkRemote := filepath.Join(t.TempDir(), "holark.git")
	runGit(t, filepath.Dir(holarkRemote), "init", "--bare", holarkRemote)
	runGit(t, source, "remote", "add", "holark", holarkRemote)
	runGit(t, source, "push", "holark", "main")

	baseCommit := strings.TrimSpace(gitOutput(t, holarkRemote, "rev-parse", "--verify", "refs/heads/main"))
	runGit(t, source, "checkout", "-b", "feature/review")
	if err := os.WriteFile(filepath.Join(source, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "feature.txt")
	runGit(t, source, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "feature")
	headCommit := strings.TrimSpace(gitOutput(t, source+"/.git", "rev-parse", "--verify", "HEAD"))
	runGit(t, source, "push", "origin", "feature/review")
	runGit(t, source, "push", "holark", "feature/review")

	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.PrepareFromCommit(context.Background(), "project", "session-worker", originRemote, protocol.WorkspaceSpec{
		Mode: protocol.WorkspaceModeCommit, BaseBranch: "main", BaseCommit: baseCommit, HeadBranch: "feature/review", HeadCommit: headCommit, BranchPrefix: "holark/worker", UpstreamBranch: "feature/review",
	}, holarkRemote)
	if err != nil {
		t.Fatal(err)
	}
	assertWorktreeConfig(t, prepared.Path, "remote.origin.url", originRemote)
	if cacheRemote := strings.TrimSpace(gitOutput(t, filepath.Join(manager.root, "mirrors", "project.git"), "remote", "get-url", "cache")); cacheRemote != holarkRemote {
		t.Fatalf("mirror cache = %q, want Holark remote %q", cacheRemote, holarkRemote)
	}

	if err := os.WriteFile(filepath.Join(prepared.Path, "worker.txt"), []byte("worker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, prepared.Path, "add", "worker.txt")
	runGit(t, prepared.Path, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "worker change")
	published, err := PublishWorkspaceToOrigin(context.Background(), prepared.Path, prepared.Branch, prepared.UpstreamBranch, headCommit)
	if err != nil {
		t.Fatal(err)
	}

	originHead := strings.TrimSpace(gitOutput(t, originRemote, "rev-parse", "--verify", "refs/heads/feature/review"))
	if originHead != published.HeadCommit {
		t.Fatalf("origin feature/review head = %q, want %q", originHead, published.HeadCommit)
	}
	holarkHead := strings.TrimSpace(gitOutput(t, holarkRemote, "rev-parse", "--verify", "refs/heads/feature/review"))
	if holarkHead != headCommit {
		t.Fatalf("holark cache feature/review head = %q, want original %q", holarkHead, headCommit)
	}
}

func TestCleanupRemovesSessionWorktreeAndBranch(t *testing.T) {
	remote := localRemote(t, "project")
	source := strings.TrimSuffix(remote, ".git") + "-source"
	baseCommit := strings.TrimSpace(gitOutput(t, remote, "rev-parse", "--verify", "refs/heads/main"))
	runGit(t, source, "checkout", "-b", "feature/cleanup")
	if err := os.WriteFile(filepath.Join(source, "cleanup.txt"), []byte("cleanup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "cleanup.txt")
	runGit(t, source, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "cleanup")
	headCommit := strings.TrimSpace(gitOutput(t, source+"/.git", "rev-parse", "--verify", "HEAD"))
	runGit(t, source, "push", "origin", "feature/cleanup")

	root := filepath.Join(t.TempDir(), "workspace")
	manager, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.PrepareFromCommit(context.Background(), "project", "session-cleanup", remote, protocol.WorkspaceSpec{
		Mode: protocol.WorkspaceModeCommit, BaseBranch: "main", BaseCommit: baseCommit, HeadBranch: "feature/cleanup", HeadCommit: headCommit, BranchPrefix: "holark/review",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(prepared.Path); err != nil {
		t.Fatalf("prepared worktree: %v", err)
	}
	mirror := filepath.Join(root, "mirrors", "project.git")
	if !refExists(t, mirror, "refs/heads/"+prepared.Branch) {
		t.Fatalf("expected cleanup branch %q", prepared.Branch)
	}

	if err := manager.Cleanup(context.Background(), "project", prepared.Path, prepared.Branch); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(prepared.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session directory after cleanup error = %v, want not exist", err)
	}
	if refExists(t, mirror, "refs/heads/"+prepared.Branch) {
		t.Fatalf("cleanup branch %q still exists", prepared.Branch)
	}
}

func TestCleanupRejectsWorktreePathAtWorkspaceRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	manager, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(root, "repo")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "marker")
	if err := os.WriteFile(marker, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = manager.Cleanup(context.Background(), "project", worktree, "holark/session-1")
	if err == nil || !strings.Contains(err.Error(), "invalid workspace cleanup path") {
		t.Fatalf("Cleanup error = %v, want invalid workspace cleanup path", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("workspace root marker after rejected cleanup: %v", err)
	}
}

func TestPublishWorkspaceToOriginPushesSessionBranch(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Path, "agent.txt"), []byte("agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, prepared.Path, "add", "agent.txt")
	runGit(t, prepared.Path, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "agent change")

	published, err := PublishWorkspaceToOrigin(context.Background(), prepared.Path, prepared.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if published.Branch != prepared.Branch || published.HeadCommit == prepared.BaseCommit || published.HeadCommit == "" {
		t.Fatalf("published = %+v, base = %q", published, prepared.BaseCommit)
	}
	remoteHead := strings.TrimSpace(gitOutput(t, remote, "rev-parse", "--verify", "refs/heads/"+prepared.Branch))
	if remoteHead != published.HeadCommit {
		t.Fatalf("remote head = %q, want %q", remoteHead, published.HeadCommit)
	}
}

func TestPublishWorkspaceToOriginRejectsDirtyWorkspace(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Path, "agent.txt"), []byte("agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishWorkspaceToOrigin(context.Background(), prepared.Path, prepared.Branch); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("Publish dirty workspace error = %v", err)
	}
	if refExists(t, remote, "refs/heads/"+prepared.Branch) {
		t.Fatal("dirty workspace was pushed")
	}
}

func TestHashChangesTracksDirtyWorkspaceState(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}

	hash, dirty, err := HashChanges(context.Background(), prepared.Path)
	if err != nil {
		t.Fatal(err)
	}
	if dirty || hash != "" {
		t.Fatalf("clean hash = %q dirty = %v, want clean", hash, dirty)
	}

	if err := os.WriteFile(filepath.Join(prepared.Path, "README.md"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	trackedHash, dirty, err := HashChanges(context.Background(), prepared.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty || trackedHash == "" {
		t.Fatalf("tracked hash = %q dirty = %v, want dirty hash", trackedHash, dirty)
	}
	repeatedHash, dirty, err := HashChanges(context.Background(), prepared.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty || repeatedHash != trackedHash {
		t.Fatalf("repeated hash = %q dirty = %v, want %q", repeatedHash, dirty, trackedHash)
	}

	if err := os.WriteFile(filepath.Join(prepared.Path, "new.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstUntrackedHash, dirty, err := HashChanges(context.Background(), prepared.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty || firstUntrackedHash == trackedHash {
		t.Fatalf("untracked hash = %q dirty = %v, want new dirty hash", firstUntrackedHash, dirty)
	}
	if err := os.WriteFile(filepath.Join(prepared.Path, "new.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secondUntrackedHash, dirty, err := HashChanges(context.Background(), prepared.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty || secondUntrackedHash == firstUntrackedHash {
		t.Fatalf("changed untracked hash = %q dirty = %v, want new dirty hash", secondUntrackedHash, dirty)
	}
}

func TestHashChangesIgnoresConfiguredArtifactPath(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := ".holark/comment-reply.json"
	if err := os.MkdirAll(filepath.Join(prepared.Path, ".holark"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Path, artifactPath), []byte(`{"reply":"done"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if hash, dirty, err := HashChanges(context.Background(), prepared.Path, artifactPath); err != nil || dirty || hash != "" {
		t.Fatalf("ignored artifact hash = %q dirty = %v err = %v, want clean", hash, dirty, err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Path, "README.md"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if hash, dirty, err := HashChanges(context.Background(), prepared.Path, artifactPath); err != nil || !dirty || hash == "" {
		t.Fatalf("tracked change hash = %q dirty = %v err = %v, want dirty", hash, dirty, err)
	}
}

func TestPublishWorkspaceToOriginIgnoresConfiguredArtifactPath(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Path, "agent.txt"), []byte("agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, prepared.Path, "add", "agent.txt")
	runGit(t, prepared.Path, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "agent change")
	artifactPath := ".holark/comment-reply.json"
	if err := os.MkdirAll(filepath.Join(prepared.Path, ".holark"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Path, artifactPath), []byte(`{"reply":"done"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	published, err := PublishWorkspaceToOrigin(context.Background(), prepared.Path, prepared.Branch, prepared.Branch, "", artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	remoteHead := strings.TrimSpace(gitOutput(t, remote, "rev-parse", "--verify", "refs/heads/"+prepared.Branch))
	if remoteHead != published.HeadCommit {
		t.Fatalf("remote head = %q, want %q", remoteHead, published.HeadCommit)
	}
}

// Verifies preparation refuses to overwrite existing session or staging refs.
func TestPrepareRejectsExistingRefs(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", ""); err == nil {
		t.Fatal("Prepare reused an existing session branch")
	}

	runGit(t, filepath.Join(manager.root, "mirrors", "project.git"), "update-ref", "refs/holark/fetch/session-2", "refs/heads/holark/session-1")
	if _, err := manager.Prepare(context.Background(), "project", "session-2", remote, "main", ""); err == nil {
		t.Fatal("Prepare reused an existing staging ref")
	}
}

// Verifies old mirror repositories are migrated to non-mirror fetch config.
func TestPrepareMigratesExistingMirrorConfiguration(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(manager.root, "mirrors", "project.git")
	if err := os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(mirror), "clone", "--mirror", remote, mirror)

	if _, err := manager.Prepare(context.Background(), "project", "session-1", remote, "main", ""); err != nil {
		t.Fatal(err)
	}
	fetch := gitOutput(t, mirror, "config", "--get-all", "remote.cache.fetch")
	if strings.TrimSpace(fetch) != "+refs/heads/*:refs/remotes/cache/*" {
		t.Fatalf("fetch refspec = %q", fetch)
	}
	mirrorMode := exec.Command("git", "--git-dir", mirror, "config", "--get", "remote.cache.mirror")
	mirrorMode.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := mirrorMode.CombinedOutput(); err == nil {
		t.Fatalf("mirror mode still configured: %s", output)
	}
}

func TestEnsureCacheRemoteRepairsHolarkLoopbackPort(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(manager.root, "mirrors", "project.git")
	if err := os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(mirror), "init", "--bare", mirror)
	first := "ssh://git@127.0.0.1:2222/project.git"
	second := "ssh://git@127.0.0.1:2223/project.git"
	runGit(t, filepath.Dir(mirror), "--git-dir", mirror, "remote", "add", "cache", first)

	if err := manager.ensureCacheRemote(context.Background(), mirror, second); err != nil {
		t.Fatal(err)
	}
	if err := manager.ensureCacheRemote(context.Background(), mirror, second); err != nil {
		t.Fatal(err)
	}

	urls := strings.Fields(gitOutput(t, mirror, "remote", "get-url", "--all", "cache"))
	if len(urls) != 1 || urls[0] != second {
		t.Fatalf("cache URLs = %#v, want repaired %q", urls, second)
	}

	// Rewrite only the rotated URL to a local repository. A fetch therefore
	// fails if Git still treats the stale URL as the remote's primary URL.
	fileRemote := (&url.URL{Scheme: "file", Path: remote}).String()
	runGit(t, filepath.Dir(mirror), "--git-dir", mirror, "config", "url."+fileRemote+".insteadOf", second)
	if err := manager.configureCacheFetch(context.Background(), mirror); err != nil {
		t.Fatal(err)
	}
	if err := manager.refreshCacheRefs(context.Background(), mirror, GitSSHOptions{}, false); err != nil {
		t.Fatal(err)
	}
	if !refExists(t, mirror, "refs/remotes/cache/main") {
		t.Fatal("rotated cache remote was not fetched")
	}
}

func TestEnsureCacheRemoteRejectsDifferentHolarkProject(t *testing.T) {
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(manager.root, "mirrors", "project.git")
	if err := os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(mirror), "init", "--bare", mirror)
	runGit(t, filepath.Dir(mirror), "--git-dir", mirror, "remote", "add", "cache", "ssh://git@127.0.0.1:2222/project.git")

	err = manager.ensureCacheRemote(context.Background(), mirror, "ssh://git@127.0.0.1:2223/other.git")
	if err == nil || !strings.Contains(err.Error(), "repository mirror cache remote mismatch") {
		t.Fatalf("ensure cache remote error = %v, want mismatch", err)
	}
}

// Verifies a restarted Holark Git SSH server can reuse a mirror from the previous loopback port.
func TestPrepareRepairsLoopbackCacheRemotePortChange(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(manager.root, "mirrors", "project.git")
	if err := os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(mirror), "clone", "--mirror", remote, mirror)
	runGit(t, mirror, "remote", "rename", "origin", "cache")

	oldURL := "ssh://git@127.0.0.1:2222/project.git"
	newURL := "ssh://git@127.0.0.1:2223/project.git"
	runGit(t, mirror, "remote", "set-url", "cache", oldURL)
	runGit(t, mirror, "config", "url."+(&url.URL{Scheme: "file", Path: filepath.ToSlash(remote)}).String()+".insteadOf", newURL)

	prepared, err := manager.Prepare(context.Background(), "project", "session-1", newURL, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	baseCommit := strings.TrimSpace(gitOutput(t, remote, "rev-parse", "--verify", "refs/heads/main"))
	if prepared.BaseCommit != baseCommit {
		t.Fatalf("base commit = %q, want %q", prepared.BaseCommit, baseCommit)
	}
	if cacheRemote := strings.TrimSpace(gitOutput(t, mirror, "config", "--get", "remote.cache.url")); cacheRemote != newURL {
		t.Fatalf("mirror cache = %q, want %q", cacheRemote, newURL)
	}
}

func TestPrepareInitialMirrorKeepsRewrittenLoopbackCacheRemote(t *testing.T) {
	remote := localRemote(t, "project")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	newURL := "ssh://git@127.0.0.1:2223/project.git"
	configDir := t.TempDir()
	globalConfig := filepath.Join(configDir, "gitconfig")
	runGit(t, configDir, "config", "--file", globalConfig, "url."+(&url.URL{Scheme: "file", Path: filepath.ToSlash(remote)}).String()+".insteadOf", newURL)
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)

	prepared, err := manager.Prepare(context.Background(), "project", "session-1", newURL, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	baseCommit := strings.TrimSpace(gitOutput(t, remote, "rev-parse", "--verify", "refs/heads/main"))
	if prepared.BaseCommit != baseCommit {
		t.Fatalf("base commit = %q, want %q", prepared.BaseCommit, baseCommit)
	}
	mirror := filepath.Join(manager.root, "mirrors", "project.git")
	if cacheRemote := strings.TrimSpace(gitOutput(t, mirror, "config", "--get", "remote.cache.url")); cacheRemote != newURL {
		t.Fatalf("mirror cache = %q, want %q", cacheRemote, newURL)
	}
}

func TestPrepareFromCommitRepairsLoopbackCacheRemotePortChange(t *testing.T) {
	remote := localRemote(t, "project")
	source := strings.TrimSuffix(remote, ".git") + "-source"
	baseCommit := strings.TrimSpace(gitOutput(t, remote, "rev-parse", "--verify", "refs/heads/main"))
	runGit(t, source, "checkout", "-b", "feature/review")
	if err := os.WriteFile(filepath.Join(source, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "feature.txt")
	runGit(t, source, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "feature")
	headCommit := strings.TrimSpace(gitOutput(t, source+"/.git", "rev-parse", "--verify", "HEAD"))
	runGit(t, source, "push", "origin", "feature/review")

	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(manager.root, "mirrors", "project.git")
	if err := os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(mirror), "clone", "--mirror", remote, mirror)
	runGit(t, mirror, "remote", "rename", "origin", "cache")

	oldURL := "ssh://git@127.0.0.1:2222/project.git"
	newURL := "ssh://git@127.0.0.1:2223/project.git"
	runGit(t, mirror, "remote", "set-url", "cache", oldURL)
	runGit(t, mirror, "config", "url."+(&url.URL{Scheme: "file", Path: filepath.ToSlash(remote)}).String()+".insteadOf", newURL)

	prepared, err := manager.PrepareFromCommit(context.Background(), "project", "session-review", newURL, protocol.WorkspaceSpec{
		Mode: protocol.WorkspaceModeCommit, BaseBranch: "main", BaseCommit: baseCommit, HeadBranch: "feature/review", HeadCommit: headCommit, BranchPrefix: "holark/review",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.HeadCommit != headCommit {
		t.Fatalf("head commit = %q, want %q", prepared.HeadCommit, headCommit)
	}
	if cacheRemote := strings.TrimSpace(gitOutput(t, mirror, "config", "--get", "remote.cache.url")); cacheRemote != newURL {
		t.Fatalf("mirror cache = %q, want %q", cacheRemote, newURL)
	}
}

// Verifies an existing session directory is reported before Git worktree setup.
func TestPrepareReportsExistingWorkspacePath(t *testing.T) {
	remote := localRemote(t, "one")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Prepare(context.Background(), "one", "session-1", remote, "main", ""); err != nil {
		t.Fatal(err)
	}

	_, err = manager.Prepare(context.Background(), "one", "session-1", remote, "main", "")
	if err == nil {
		t.Fatal("Prepare succeeded with an existing session workspace")
	}
	assertErrorContains(t, err, "session workspace already exists", "session-1")
}

// Verifies invalid existing repositories report cache remote inspection failures.
func TestPrepareReportsRepositoryMirrorOriginFailures(t *testing.T) {
	remote := localRemote(t, "one")
	manager, err := NewManager(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(manager.root, "mirrors", "one.git")
	if err := os.MkdirAll(mirror, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err = manager.Prepare(context.Background(), "one", "session-1", remote, "main", "")
	if err == nil {
		t.Fatal("Prepare succeeded with an invalid existing mirror")
	}
	assertErrorContains(t, err, "read repository cache remote", "git --git-dir")
}

func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != expected {
		t.Fatalf("%s = %q, want %q", path, data, expected)
	}
}

func resolvedGitExcludePath(t *testing.T, worktree string) string {
	t.Helper()
	excludePath := strings.TrimSpace(worktreeGitOutput(t, worktree, "rev-parse", "--git-path", "info/exclude"))
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(worktree, excludePath)
	}
	return excludePath
}

func assertErrorContains(t *testing.T, err error, expected ...string) {
	t.Helper()
	for _, value := range expected {
		if !strings.Contains(err.Error(), value) {
			t.Fatalf("error %q does not contain %q", err, value)
		}
	}
}

func localRemote(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, name+"-source")
	remote := filepath.Join(root, name+".git")
	runGit(t, root, "init", "--initial-branch=main", source)
	runGit(t, source, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "-c", "user.name=Holark Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	runGit(t, root, "init", "--bare", remote)
	runGit(t, source, "push", "-u", "origin", "main")
	return remote
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func gitOutput(t *testing.T, gitDir string, arguments ...string) string {
	t.Helper()
	args := append([]string{"--git-dir", gitDir}, arguments...)
	command := exec.Command("git", args...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func worktreeGitOutput(t *testing.T, worktree string, arguments ...string) string {
	t.Helper()
	args := append([]string{"-C", worktree}, arguments...)
	command := exec.Command("git", args...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func assertWorktreeConfig(t *testing.T, worktree, key, expected string) {
	t.Helper()
	if actual := strings.TrimSpace(worktreeGitOutput(t, worktree, "config", "--get", key)); actual != expected {
		t.Fatalf("%s = %q, want %q", key, actual, expected)
	}
}

func refExists(t *testing.T, gitDir, ref string) bool {
	t.Helper()
	command := exec.Command("git", "--git-dir", gitDir, "show-ref", "--verify", "--quiet", ref)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	err := command.Run()
	if err == nil {
		return true
	}
	if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 1 {
		return false
	}
	t.Fatalf("check ref %s: %v", ref, err)
	return false
}
