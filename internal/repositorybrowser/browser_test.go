package repositorybrowser

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerImportsListsAndReadsRepository(t *testing.T) {
	remote := createRemote(t)
	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}

	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	root, err := manager.Tree(context.Background(), repository, "", "")
	if err != nil {
		t.Fatalf("root tree: %v", err)
	}
	if len(root.Entries) < 2 || root.Entries[0].Name != "docs" || root.Entries[0].Type != "directory" {
		t.Fatalf("root entries = %+v", root.Entries)
	}
	nested, err := manager.Tree(context.Background(), repository, "main", "docs")
	if err != nil {
		t.Fatalf("nested tree: %v", err)
	}
	if len(nested.Entries) != 1 || nested.Entries[0].Path != "docs/guide.md" {
		t.Fatalf("nested entries = %+v", nested.Entries)
	}
	blob, err := manager.Blob(context.Background(), repository, "", "README.md")
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	if blob.Content != "# Holark\n" || blob.Language != "markdown" || blob.Encoding != "utf-8" {
		t.Fatalf("blob = %+v", blob)
	}
}

func TestManagerReadsOnlySelectedProviderBranch(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	mainCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "tag", "release")
	git(t, source, "push", "origin", "release")
	git(t, source, "checkout", "-b", "feature/foo")
	writeFile(t, source, "README.md", "# Feature\n")
	writeFile(t, source, "feature-only.txt", "feature only\n")
	git(t, source, "add", ".")
	git(t, source, "commit", "-m", "feature content")
	git(t, source, "push", "origin", "feature/foo")

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	git(t, source, "push", manager.repositoryPath(repository.ID), mainCommit+":refs/holark/sessions/release")

	featureTree, err := manager.Tree(context.Background(), repository, "feature/foo", "")
	if err != nil {
		t.Fatalf("feature tree: %v", err)
	}
	if featureTree.Ref != "feature/foo" || !treeHasPath(featureTree, "feature-only.txt") {
		t.Fatalf("feature tree = %+v", featureTree)
	}
	featureBlob, err := manager.Blob(context.Background(), repository, "feature/foo", "README.md")
	if err != nil {
		t.Fatalf("feature blob: %v", err)
	}
	if featureBlob.Ref != "feature/foo" || featureBlob.Content != "# Feature\n" {
		t.Fatalf("feature blob = %+v", featureBlob)
	}
	defaultBlob, err := manager.Blob(context.Background(), repository, "", "README.md")
	if err != nil {
		t.Fatalf("default blob: %v", err)
	}
	if defaultBlob.Ref != "main" || defaultBlob.Content != "# Holark\n" {
		t.Fatalf("default blob = %+v", defaultBlob)
	}

	tests := []struct {
		name string
		ref  string
		want error
	}{
		{name: "tag only", ref: "release", want: ErrRefNotFound},
		{name: "missing session-prefixed branch", ref: "refs/holark/sessions/release", want: ErrRefNotFound},
		{name: "missing refs-prefixed branch", ref: "refs/heads/feature/foo", want: ErrRefNotFound},
		{name: "revision expression", ref: "feature/foo~1", want: ErrInvalidRepository},
		{name: "malformed", ref: "feature..foo", want: ErrInvalidRepository},
		{name: "missing branch", ref: "missing", want: ErrRefNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := manager.Tree(context.Background(), repository, test.ref, ""); !errors.Is(err, test.want) {
				t.Fatalf("tree error = %v, want %v", err, test.want)
			}
			if _, err := manager.Blob(context.Background(), repository, test.ref, "README.md"); !errors.Is(err, test.want) {
				t.Fatalf("blob error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestManagerListsAndReadsBranchWhoseShortNameStartsWithRefs(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	git(t, source, "checkout", "-b", "refs/foo")
	writeFile(t, source, "README.md", "# Refs branch\n")
	git(t, source, "add", "README.md")
	git(t, source, "commit", "-m", "refs branch content")
	git(t, source, "push", "origin", "refs/foo")

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	refs, err := manager.Refs(context.Background(), repository)
	if err != nil {
		t.Fatalf("refs: %v", err)
	}
	var branch Ref
	for _, ref := range refs {
		if ref.Name == "refs/heads/refs/foo" {
			branch = ref
			break
		}
	}
	if branch.ShortName != "refs/foo" || branch.Kind != "branch" {
		t.Fatalf("refs branch = %+v", branch)
	}

	tree, err := manager.Tree(context.Background(), repository, branch.ShortName, "")
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if tree.Ref != branch.ShortName || !treeHasPath(tree, "README.md") {
		t.Fatalf("tree = %+v", tree)
	}
	blob, err := manager.Blob(context.Background(), repository, branch.ShortName, "README.md")
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	if blob.Ref != branch.ShortName || blob.Content != "# Refs branch\n" {
		t.Fatalf("blob = %+v", blob)
	}
}

func TestManagerRejectsMissingAndUnsafePaths(t *testing.T) {
	remote := createRemote(t)
	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	if _, err := manager.Tree(context.Background(), repository, "", "missing"); !errors.Is(err, ErrPathNotFound) {
		t.Fatalf("missing tree error = %v, want %v", err, ErrPathNotFound)
	}
	if _, err := manager.Blob(context.Background(), repository, "", "../README.md"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("traversal blob error = %v, want %v", err, ErrInvalidPath)
	}
	if _, err := manager.Blob(context.Background(), repository, "", `..\README.md`); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("backslash traversal blob error = %v, want %v", err, ErrInvalidPath)
	}
	if _, err := manager.Blob(context.Background(), repository, "", "docs"); !errors.Is(err, ErrPathNotFile) {
		t.Fatalf("directory blob error = %v, want %v", err, ErrPathNotFile)
	}
}

func TestManagerRejectsOriginMismatch(t *testing.T) {
	remote := createRemote(t)
	otherRemote := createRemote(t)
	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	repository.RepositoryURL = otherRemote
	if err := manager.Ensure(context.Background(), repository); !errors.Is(err, ErrOriginMismatch) {
		t.Fatalf("origin mismatch error = %v, want %v", err, ErrOriginMismatch)
	}
}

func TestManagerRebasesHeadBranchWithForceLease(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	baseCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "checkout", "-b", "feature")
	writeFile(t, source, "feature.txt", "feature\n")
	git(t, source, "add", "feature.txt")
	git(t, source, "commit", "-m", "feature commit")
	headCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "push", "origin", "feature")
	git(t, source, "checkout", "main")
	writeFile(t, source, "base.txt", "base\n")
	git(t, source, "add", "base.txt")
	git(t, source, "commit", "-m", "base commit")
	git(t, source, "push", "origin", "main")
	newBaseCommit := gitOutput(t, source, "rev-parse", "HEAD")
	if baseCommit == newBaseCommit {
		t.Fatal("base did not advance")
	}

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	result, err := manager.Rebase(context.Background(), repository, RebaseRequest{BaseCommit: newBaseCommit, BaseBranch: "main", HeadBranch: "feature", ExpectedHeadCommit: headCommit})
	if err != nil {
		t.Fatalf("rebase: %v", err)
	}
	if !result.Rebased || result.HeadCommit == "" || result.HeadCommit == headCommit {
		t.Fatalf("rebase result = %+v, old head = %s", result, headCommit)
	}
	backupRef := "refs/holark/rebase-backups/feature.backup.rebase.1"
	if backupCommit := gitOutput(t, manager.repositoryPath(repository.ID), "rev-parse", "--verify", backupRef); backupCommit != headCommit {
		t.Fatalf("rebase backup commit = %q, want original head %q", backupCommit, headCommit)
	}
	git(t, source, "fetch", "origin", "feature")
	remoteFeature := gitOutput(t, source, "rev-parse", "origin/feature")
	if remoteFeature != result.HeadCommit {
		t.Fatalf("origin/feature = %s, want %s", remoteFeature, result.HeadCommit)
	}
	git(t, source, "merge-base", "--is-ancestor", newBaseCommit, remoteFeature)

	// A pinned execution selected before another writer pushed must retain its
	// explicit lease, even though its own cache still contains the original head.
	git(t, source, "checkout", "feature")
	git(t, source, "commit", "--allow-empty", "-m", "competing publication")
	externalHead := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "push", "--force-with-lease=refs/heads/feature:"+remoteFeature, "origin", "feature")
	_, err = manager.RebasePinned(t.Context(), repository, RebaseRequest{BaseCommit: newBaseCommit, BaseBranch: "main", HeadBranch: "feature", ExpectedHeadCommit: headCommit})
	if !errors.Is(err, ErrPushRejected) {
		t.Fatalf("stale lease was not rejected: %v", err)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != externalHead {
		t.Fatalf("competing publication overwritten: %s", got)
	}
}

func TestManagerPreviewsCleanAndConflictingRebasesWithoutChangingRefs(t *testing.T) {
	for _, test := range []struct {
		name      string
		feature   string
		base      string
		conflicts bool
	}{
		{name: "clean", feature: "feature.txt", base: "base.txt"},
		{name: "conflicting", feature: "README.md", base: "README.md", conflicts: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote, source := createRemoteWithSource(t)
			git(t, source, "checkout", "-b", "feature")
			writeFile(t, source, test.feature, "feature\n")
			git(t, source, "add", test.feature)
			git(t, source, "commit", "-m", "feature commit")
			headCommit := gitOutput(t, source, "rev-parse", "HEAD")
			git(t, source, "push", "origin", "feature")
			git(t, source, "checkout", "main")
			writeFile(t, source, test.base, "base\n")
			git(t, source, "add", test.base)
			git(t, source, "commit", "-m", "base commit")
			baseCommit := gitOutput(t, source, "rev-parse", "HEAD")
			git(t, source, "push", "origin", "main")

			manager := newTestManager(t)
			repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
			if err := manager.Ensure(t.Context(), repository); err != nil {
				t.Fatal(err)
			}
			mirror := manager.repositoryPath(repository.ID)
			refsBefore := gitOutput(t, mirror, "for-each-ref", "--format=%(refname) %(objectname)")
			worktreesBefore := gitOutput(t, mirror, "worktree", "list", "--porcelain")

			result, err := manager.PreviewRebase(t.Context(), repository, RebaseRequest{BaseCommit: baseCommit, BaseBranch: "main", HeadBranch: "feature", ExpectedHeadCommit: headCommit})
			if err != nil {
				t.Fatal(err)
			}
			if result.UpToDate || result.Conflicts != test.conflicts || result.BaseCommitsAhead != 1 {
				t.Fatalf("preview=%+v", result)
			}
			if got := gitOutput(t, remote, "rev-parse", "refs/heads/feature"); got != headCommit {
				t.Fatalf("remote feature=%s want=%s", got, headCommit)
			}
			if refsAfter := gitOutput(t, mirror, "for-each-ref", "--format=%(refname) %(objectname)"); refsAfter != refsBefore {
				t.Fatalf("refs changed\nbefore:\n%s\nafter:\n%s", refsBefore, refsAfter)
			}
			if worktreesAfter := gitOutput(t, mirror, "worktree", "list", "--porcelain"); worktreesAfter != worktreesBefore {
				t.Fatalf("worktrees changed\nbefore:\n%s\nafter:\n%s", worktreesBefore, worktreesAfter)
			}
			if matches, err := filepath.Glob(filepath.Join(manager.root, ".tmp", "rebase-holark-*")); err != nil || len(matches) != 0 {
				t.Fatalf("temporary worktrees=%v err=%v", matches, err)
			}
		})
	}
}

func TestManagerPreviewReportsUpToDateWithoutChangingRefs(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	git(t, source, "checkout", "-b", "feature")
	writeFile(t, source, "feature.txt", "feature\n")
	git(t, source, "add", "feature.txt")
	git(t, source, "commit", "-m", "feature commit")
	headCommit := gitOutput(t, source, "rev-parse", "HEAD")
	baseCommit := gitOutput(t, source, "rev-parse", "main")
	git(t, source, "push", "origin", "feature")
	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	result, err := manager.PreviewRebase(t.Context(), repository, RebaseRequest{BaseCommit: baseCommit, BaseBranch: "main", HeadBranch: "feature", ExpectedHeadCommit: headCommit})
	if err != nil || !result.UpToDate || result.Conflicts || result.BaseCommitsAhead != 0 {
		t.Fatalf("preview=%+v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(manager.root, ".tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary rebase directory was created: %v", err)
	}
}

func TestManagerReadsLocalStateUntilRefresh(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}

	writeFile(t, source, "later.txt", "updated\n")
	git(t, source, "add", "later.txt")
	git(t, source, "commit", "-m", "add later")
	git(t, source, "push", "origin", "main")

	if _, err := manager.Blob(context.Background(), repository, "", "later.txt"); !errors.Is(err, ErrPathNotFound) {
		t.Fatalf("pre-refresh blob error = %v, want %v", err, ErrPathNotFound)
	}
	if err := manager.Refresh(context.Background(), repository); err != nil {
		t.Fatalf("refresh repository: %v", err)
	}
	blob, err := manager.Blob(context.Background(), repository, "", "later.txt")
	if err != nil {
		t.Fatalf("post-refresh blob: %v", err)
	}
	if blob.Content != "updated\n" {
		t.Fatalf("post-refresh blob content = %q", blob.Content)
	}
}

func TestManagerPreparesOnlySelectedBranchWithCheckoutObjects(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	git(t, source, "checkout", "-b", "other")
	writeFile(t, source, "other.txt", "original other\n")
	git(t, source, "add", "other.txt")
	git(t, source, "commit", "-m", "add other branch")
	git(t, source, "push", "origin", "other")
	git(t, source, "checkout", "main")

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	mirror := manager.repositoryPath(repository.ID)
	originalOther := gitOutput(t, mirror, "rev-parse", "refs/heads/other")

	writeFile(t, source, "prepared.txt", "prepared object\n")
	git(t, source, "add", "prepared.txt")
	git(t, source, "commit", "-m", "advance main")
	git(t, source, "push", "origin", "main")
	preparedCommit := gitOutput(t, source, "rev-parse", "HEAD")

	git(t, source, "checkout", "other")
	writeFile(t, source, "other.txt", "advanced other\n")
	git(t, source, "add", "other.txt")
	git(t, source, "commit", "-m", "advance other")
	git(t, source, "push", "origin", "other")
	advancedOther := gitOutput(t, source, "rev-parse", "HEAD")

	git(t, source, "checkout", "main")
	git(t, source, "checkout", "-b", "pull-request")
	writeFile(t, source, "pull-request.txt", "pull request\n")
	git(t, source, "add", "pull-request.txt")
	git(t, source, "commit", "-m", "pull request")
	git(t, source, "push", "origin", "HEAD:refs/pull/42/head")

	resolved, err := manager.PrepareBranch(context.Background(), repository, "main")
	if err != nil {
		t.Fatalf("prepare branch: %v", err)
	}
	if resolved != preparedCommit {
		t.Fatalf("prepared commit = %q, want %q", resolved, preparedCommit)
	}
	if content := gitOutput(t, mirror, "show", resolved+":prepared.txt"); content != "prepared object" {
		t.Fatalf("prepared object content = %q", content)
	}
	if other := gitOutput(t, mirror, "rev-parse", "refs/heads/other"); other != originalOther || other == advancedOther {
		t.Fatalf("other branch = %q, want unchanged %q (remote advanced to %q)", other, originalOther, advancedOther)
	}
	command := exec.Command("git", "--git-dir", mirror, "rev-parse", "--verify", "refs/pull/42/head")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("pull request ref was fetched: %s", strings.TrimSpace(string(output)))
	}
}

func TestManagerReviewsCommitRange(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	baseCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "checkout", "-b", "feature")
	writeFile(t, source, "README.md", "# Holark\n\nReviewed locally.\n")
	git(t, source, "add", "README.md")
	git(t, source, "commit", "-m", "review change")
	headCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "push", "origin", "feature")

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	mergeBase, err := manager.MergeBase(context.Background(), repository, baseCommit, headCommit)
	if err != nil {
		t.Fatalf("merge base: %v", err)
	}
	if mergeBase != baseCommit {
		t.Fatalf("merge base = %q, want %q", mergeBase, baseCommit)
	}
	commits, err := manager.Commits(context.Background(), repository, mergeBase, headCommit)
	if err != nil {
		t.Fatalf("commits: %v", err)
	}
	if len(commits) != 1 || commits[0].SHA != headCommit || commits[0].Message != "review change" {
		t.Fatalf("commits = %+v", commits)
	}
	inspection, err := manager.Changes(context.Background(), repository, "feature", "main", mergeBase, headCommit)
	if err != nil {
		t.Fatalf("changes: %v", err)
	}
	if !inspection.HasChanges || inspection.Dirty || inspection.HeadCommit != headCommit || len(inspection.Files) != 1 ||
		inspection.Files[0].Path != "README.md" || inspection.Files[0].Additions != 2 || inspection.Files[0].Diff == "" {
		t.Fatalf("inspection = %+v", inspection)
	}
}

func TestManagerReviewsPullRequestHeadRef(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	baseCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "checkout", "-b", "feature")
	writeFile(t, source, "README.md", "# Holark\n\nReviewed from a PR ref.\n")
	git(t, source, "add", "README.md")
	git(t, source, "commit", "-m", "review PR ref")
	headCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "push", "origin", "HEAD:refs/pull/42/head")

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	if resolved := gitOutput(t, manager.repositoryPath(repository.ID), "rev-parse", "--verify", "refs/pull/42/head"); resolved != headCommit {
		t.Fatalf("resolved PR ref = %q, want %q", resolved, headCommit)
	}
	mergeBase, err := manager.MergeBase(context.Background(), repository, baseCommit, headCommit)
	if err != nil {
		t.Fatalf("merge base: %v", err)
	}
	if mergeBase != baseCommit {
		t.Fatalf("merge base = %q, want %q", mergeBase, baseCommit)
	}
}

func TestManagerExportsExactCommitToBranch(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	git(t, source, "checkout", "-b", "agent")
	writeFile(t, source, "agent.txt", "agent\n")
	git(t, source, "add", "agent.txt")
	git(t, source, "commit", "-m", "agent change")
	headCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "push", "origin", "agent")

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	if err := manager.ExportBranch(context.Background(), repository, headCommit, "holark/session-1"); err != nil {
		t.Fatalf("export branch: %v", err)
	}
	exported := gitOutput(t, remote, "rev-parse", "--verify", "refs/heads/holark/session-1")
	if exported != headCommit {
		t.Fatalf("exported commit = %q, want %q", exported, headCommit)
	}
}

func TestManagerExportBranchReportsNonFastForwardRejection(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	git(t, source, "checkout", "-b", "holark/session-1")
	writeFile(t, source, "old.txt", "old\n")
	git(t, source, "add", "old.txt")
	git(t, source, "commit", "-m", "old session")
	existingCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "push", "origin", "HEAD:refs/heads/holark/session-1")

	git(t, source, "checkout", "main")
	git(t, source, "checkout", "-b", "agent")
	writeFile(t, source, "agent.txt", "agent\n")
	git(t, source, "add", "agent.txt")
	git(t, source, "commit", "-m", "agent change")
	headCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "push", "origin", "agent")

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	err := manager.ExportBranch(context.Background(), repository, headCommit, "holark/session-1")
	if !errors.Is(err, ErrPushRejected) {
		t.Fatalf("export branch error = %v, want %v", err, ErrPushRejected)
	}
	if exported := gitOutput(t, remote, "rev-parse", "--verify", "refs/heads/holark/session-1"); exported != existingCommit {
		t.Fatalf("exported commit = %q, want retained %q", exported, existingCommit)
	}
}

func TestManagerCreatesSequentialRebaseBackupBranches(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	git(t, source, "checkout", "-b", "feature/rebase")
	writeFile(t, source, "feature.txt", "feature\n")
	git(t, source, "add", "feature.txt")
	git(t, source, "commit", "-m", "feature commit")
	headCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "push", "origin", "feature/rebase")

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}

	result, err := manager.CreateRebaseBackup(context.Background(), repository, RebaseBackupRequest{
		HeadBranch: "feature/rebase", ExpectedHeadCommit: headCommit,
	})
	if err != nil {
		t.Fatalf("create rebase backup: %v", err)
	}
	if result.Branch != "feature/rebase.backup.rebase.1" || result.HeadCommit != headCommit {
		t.Fatalf("rebase backup = %+v, want branch 1 at %s", result, headCommit)
	}
	if backupCommit := gitOutput(t, manager.repositoryPath(repository.ID), "rev-parse", "--verify", "refs/holark/rebase-backups/"+result.Branch); backupCommit != headCommit {
		t.Fatalf("local backup commit = %q, want %q", backupCommit, headCommit)
	}

	result, err = manager.CreateRebaseBackup(context.Background(), repository, RebaseBackupRequest{
		HeadBranch: "feature/rebase", ExpectedHeadCommit: headCommit,
	})
	if err != nil {
		t.Fatalf("create second rebase backup: %v", err)
	}
	if result.Branch != "feature/rebase.backup.rebase.2" || result.HeadCommit != headCommit {
		t.Fatalf("second rebase backup = %+v, want branch 2 at %s", result, headCommit)
	}

	git(t, manager.repositoryPath(repository.ID), "update-ref", "refs/holark/rebase-backups/feature/rebase.backup.rebase.4", headCommit)
	if err := manager.Refresh(context.Background(), repository); err != nil {
		t.Fatalf("refresh repository: %v", err)
	}
	if backupCommit := gitOutput(t, manager.repositoryPath(repository.ID), "rev-parse", "--verify", "refs/holark/rebase-backups/feature/rebase.backup.rebase.1"); backupCommit != headCommit {
		t.Fatalf("backup after refresh = %q, want %q", backupCommit, headCommit)
	}
	if refs := gitOutput(t, remote, "for-each-ref", "--format=%(refname)"); strings.Contains(refs, "backup.rebase") {
		t.Fatalf("remote contains rebase backups: %s", refs)
	}
	result, err = manager.CreateRebaseBackup(context.Background(), repository, RebaseBackupRequest{
		HeadBranch: "feature/rebase", ExpectedHeadCommit: headCommit,
	})
	if err != nil {
		t.Fatalf("create backup after numbering gap: %v", err)
	}
	if result.Branch != "feature/rebase.backup.rebase.5" || result.HeadCommit != headCommit {
		t.Fatalf("backup after numbering gap = %+v, want branch 5 at %s", result, headCommit)
	}
	if originalHead := gitOutput(t, remote, "rev-parse", "--verify", "refs/heads/feature/rebase"); originalHead != headCommit {
		t.Fatalf("original branch head = %q, want unchanged %q", originalHead, headCommit)
	}
}

func TestManagerRebaseBackupSkipsSequenceWithDescendantRef(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	git(t, source, "checkout", "-b", "feature")
	writeFile(t, source, "feature.txt", "feature\n")
	git(t, source, "add", "feature.txt")
	git(t, source, "commit", "-m", "feature commit")
	headCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "push", "origin", "feature")

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}

	descendantRef := "refs/holark/rebase-backups/feature.backup.rebase.1/archive"
	git(t, manager.repositoryPath(repository.ID), "update-ref", descendantRef, headCommit)
	if err := manager.Refresh(context.Background(), repository); err != nil {
		t.Fatalf("refresh repository: %v", err)
	}

	result, err := manager.CreateRebaseBackup(context.Background(), repository, RebaseBackupRequest{
		HeadBranch: "feature", ExpectedHeadCommit: headCommit,
	})
	if err != nil {
		t.Fatalf("create rebase backup: %v", err)
	}
	if result.Branch != "feature.backup.rebase.2" || result.HeadCommit != headCommit {
		t.Fatalf("rebase backup = %+v, want branch 2 at %s", result, headCommit)
	}
	if descendantCommit := gitOutput(t, manager.repositoryPath(repository.ID), "rev-parse", "--verify", descendantRef); descendantCommit != headCommit {
		t.Fatalf("descendant backup commit = %q, want %q", descendantCommit, headCommit)
	}
}

func TestManagerRebaseBackupLeavesExistingRemoteBranchUnchanged(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	mainCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "checkout", "-b", "feature")
	writeFile(t, source, "feature.txt", "feature\n")
	git(t, source, "add", "feature.txt")
	git(t, source, "commit", "-m", "feature commit")
	headCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "push", "origin", "feature")

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}

	backupRef := "refs/heads/feature.backup.rebase.1"
	git(t, source, "push", "origin", mainCommit+":"+backupRef)
	_, err := manager.CreateRebaseBackup(context.Background(), repository, RebaseBackupRequest{
		HeadBranch: "feature", ExpectedHeadCommit: headCommit,
	})
	if err != nil {
		t.Fatalf("create local rebase backup: %v", err)
	}
	if backupCommit := gitOutput(t, remote, "rev-parse", "--verify", backupRef); backupCommit != mainCommit {
		t.Fatalf("existing backup commit = %q, want retained %q", backupCommit, mainCommit)
	}
}

func TestManagerFastForwardMergeAdvancesBaseAndDeletesSessionRef(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	git(t, source, "checkout", "-b", "agent")
	writeFile(t, source, "agent.txt", "agent\n")
	git(t, source, "add", "agent.txt")
	git(t, source, "commit", "-m", "agent change")
	headCommit := gitOutput(t, source, "rev-parse", "HEAD")

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	sessionRef := "refs/holark/sessions/session-1"
	git(t, source, "push", manager.repositoryPath(repository.ID), "HEAD:"+sessionRef)

	result, err := manager.FastForwardMerge(context.Background(), repository, FastForwardMergeRequest{
		BaseBranch: "main", HeadRef: sessionRef, ExpectedHeadCommit: headCommit,
	})
	if err != nil {
		t.Fatalf("fast-forward merge: %v", err)
	}
	if result.MergedCommit != headCommit {
		t.Fatalf("merged commit = %q, want %q", result.MergedCommit, headCommit)
	}
	if remoteMain := gitOutput(t, remote, "rev-parse", "--verify", "refs/heads/main"); remoteMain != headCommit {
		t.Fatalf("remote main = %q, want %q", remoteMain, headCommit)
	}
	if refExists(t, manager.repositoryPath(repository.ID), sessionRef) {
		t.Fatalf("session ref %s still exists", sessionRef)
	}
}

func TestManagerFastForwardMergeRejectsNonFastForward(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	baseCommit := gitOutput(t, remote, "rev-parse", "--verify", "refs/heads/main")
	git(t, source, "checkout", "-b", "agent")
	writeFile(t, source, "agent.txt", "agent\n")
	git(t, source, "add", "agent.txt")
	git(t, source, "commit", "-m", "agent change")
	headCommit := gitOutput(t, source, "rev-parse", "HEAD")

	git(t, source, "checkout", "main")
	writeFile(t, source, "main-later.txt", "later\n")
	git(t, source, "add", "main-later.txt")
	git(t, source, "commit", "-m", "main later")
	advancedMain := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "push", "origin", "main")

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	sessionRef := "refs/holark/sessions/session-1"
	git(t, source, "push", manager.repositoryPath(repository.ID), headCommit+":"+sessionRef)

	if _, err := manager.FastForwardMerge(context.Background(), repository, FastForwardMergeRequest{
		BaseBranch: "main", HeadRef: sessionRef, ExpectedHeadCommit: headCommit,
	}); !errors.Is(err, ErrNotFastForward) {
		t.Fatalf("fast-forward merge error = %v, want %v", err, ErrNotFastForward)
	}
	if remoteMain := gitOutput(t, remote, "rev-parse", "--verify", "refs/heads/main"); remoteMain != advancedMain {
		t.Fatalf("remote main = %q, want %q", remoteMain, advancedMain)
	}
	if remoteMain := gitOutput(t, manager.repositoryPath(repository.ID), "rev-parse", "--verify", "refs/heads/main"); remoteMain != advancedMain {
		t.Fatalf("cache main = %q, want %q", remoteMain, advancedMain)
	}
	if baseCommit == advancedMain {
		t.Fatal("test did not advance main")
	}
}

func TestManagerSquashMergeCreatesSingleCommitAndDeletesSessionRef(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	baseCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "checkout", "-b", "agent")
	writeFile(t, source, "agent.txt", "agent\n")
	git(t, source, "add", "agent.txt")
	git(t, source, "commit", "-m", "agent change one")
	writeFile(t, source, "agent-2.txt", "agent two\n")
	git(t, source, "add", "agent-2.txt")
	git(t, source, "commit", "-m", "agent change two")
	headCommit := gitOutput(t, source, "rev-parse", "HEAD")

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	sessionRef := "refs/holark/sessions/session-1"
	git(t, source, "push", manager.repositoryPath(repository.ID), "HEAD:"+sessionRef)

	result, err := manager.SquashMerge(context.Background(), repository, SquashMergeRequest{
		BaseBranch:         "main",
		HeadRef:            sessionRef,
		ExpectedHeadCommit: headCommit,
		CommitTitle:        "Ship agent work",
		CommitBody:         "Body\n\n/projects/holark/pulls/pr-1",
	})
	if err != nil {
		t.Fatalf("squash merge: %v", err)
	}
	if result.MergedCommit == "" || result.MergedCommit == headCommit || result.MergedCommit == baseCommit {
		t.Fatalf("merged commit = %q, base = %q, head = %q", result.MergedCommit, baseCommit, headCommit)
	}
	if remoteMain := gitOutput(t, remote, "rev-parse", "--verify", "refs/heads/main"); remoteMain != result.MergedCommit {
		t.Fatalf("remote main = %q, want %q", remoteMain, result.MergedCommit)
	}
	if parent := gitOutput(t, remote, "rev-parse", result.MergedCommit+"^"); parent != baseCommit {
		t.Fatalf("squash parent = %q, want %q", parent, baseCommit)
	}
	if tree := gitOutput(t, remote, "rev-parse", result.MergedCommit+"^{tree}"); tree != gitOutput(t, manager.repositoryPath(repository.ID), "rev-parse", headCommit+"^{tree}") {
		t.Fatalf("squash tree = %q, want head tree", tree)
	}
	if message := gitOutput(t, remote, "log", "-1", "--format=%B", result.MergedCommit); message != "Ship agent work\n\nBody\n\n/projects/holark/pulls/pr-1" {
		t.Fatalf("squash message = %q", message)
	}
	if refExists(t, manager.repositoryPath(repository.ID), sessionRef) {
		t.Fatalf("session ref %s still exists", sessionRef)
	}
}

func TestManagerRefreshPreservesLocalHolarkSessionRefs(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	mainCommit := gitOutput(t, remote, "rev-parse", "--verify", "refs/heads/main")
	git(t, source, "checkout", "-b", "agent")
	writeFile(t, source, "agent.txt", "agent\n")
	git(t, source, "add", "agent.txt")
	git(t, source, "commit", "-m", "agent change")
	headCommit := gitOutput(t, source, "rev-parse", "HEAD")

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	git(t, source, "push", manager.repositoryPath(repository.ID), "HEAD:refs/holark/sessions/session-1")
	if refExists(t, remote, "refs/holark/sessions/session-1") {
		t.Fatal("origin unexpectedly has Holark session ref")
	}
	if err := manager.Refresh(context.Background(), repository); err != nil {
		t.Fatalf("refresh repository: %v", err)
	}
	resolved, err := manager.ResolveRef(context.Background(), repository, "refs/holark/sessions/session-1")
	if err != nil {
		t.Fatalf("resolve ref: %v", err)
	}
	if resolved != headCommit {
		t.Fatalf("resolved commit = %q, want %q", resolved, headCommit)
	}
	servedMain := gitOutput(t, manager.repositoryPath(repository.ID), "rev-parse", "--verify", "refs/heads/main")
	if servedMain != mainCommit {
		t.Fatalf("served main = %q, want %q", servedMain, mainCommit)
	}
}

func TestManagerMigratesMirrorFetchConfig(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	repoPath := manager.repositoryPath(repository.ID)
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, filepath.Dir(repoPath), "clone", "--mirror", remote, repoPath)
	git(t, source, "checkout", "-b", "agent")
	writeFile(t, source, "agent.txt", "agent\n")
	git(t, source, "add", "agent.txt")
	git(t, source, "commit", "-m", "agent change")
	headCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "push", repoPath, "HEAD:refs/holark/sessions/session-1")

	if err := manager.Refresh(context.Background(), repository); err != nil {
		t.Fatalf("refresh repository: %v", err)
	}
	if fetch := gitOutput(t, repoPath, "config", "--get-all", "remote.origin.fetch"); fetch != strings.Join(originFetchRefspecs, "\n") {
		t.Fatalf("origin fetch = %q, want %q", fetch, strings.Join(originFetchRefspecs, "\n"))
	}
	if mirrorMode, err := gitOutputMaybe(t, repoPath, "config", "--get", "remote.origin.mirror"); err == nil {
		t.Fatalf("origin mirror mode still configured: %q", mirrorMode)
	}
	resolved, err := manager.ResolveRef(context.Background(), repository, "refs/holark/sessions/session-1")
	if err != nil {
		t.Fatalf("resolve ref: %v", err)
	}
	if resolved != headCommit {
		t.Fatalf("resolved commit = %q, want %q", resolved, headCommit)
	}
	if _, err := manager.Tree(context.Background(), repository, "", ""); err != nil {
		t.Fatalf("tree after migration: %v", err)
	}
	if _, err := manager.ResolveRef(context.Background(), repository, "refs/heads/main"); err != nil {
		t.Fatalf("resolve migrated main: %v", err)
	}
}

func TestManagerMigratesOriginTrackingBranchCache(t *testing.T) {
	remote := createRemote(t)
	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	repoPath := manager.repositoryPath(repository.ID)
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, "", "init", "--bare", repoPath)
	git(t, repoPath, "remote", "add", "origin", remote)
	git(t, repoPath, "config", "--add", "remote.origin.fetch", previousOriginFetchRefspec)
	git(t, repoPath, "fetch", "--no-tags", "origin")
	if refExists(t, repoPath, "refs/heads/main") {
		t.Fatal("test cache unexpectedly has local main before migration")
	}

	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	if fetch := gitOutput(t, repoPath, "config", "--get-all", "remote.origin.fetch"); fetch != strings.Join(originFetchRefspecs, "\n") {
		t.Fatalf("origin fetch = %q, want %q", fetch, strings.Join(originFetchRefspecs, "\n"))
	}
	if _, err := manager.ResolveRef(context.Background(), repository, "refs/heads/main"); err != nil {
		t.Fatalf("resolve migrated main: %v", err)
	}
	if _, err := manager.Tree(context.Background(), repository, "", ""); err != nil {
		t.Fatalf("tree after migration: %v", err)
	}
}

func TestManagerListsRefsAndCommitHistory(t *testing.T) {
	remote, source := createRemoteWithSource(t)
	git(t, source, "checkout", "-b", "feature/test")
	writeFile(t, source, "feature.txt", "feature\n")
	git(t, source, "add", "feature.txt")
	git(t, source, "commit", "-m", "feature commit")
	featureCommit := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "push", "origin", "feature/test")
	writeFile(t, source, "autopush.txt", "autopush\n")
	git(t, source, "add", "autopush.txt")
	git(t, source, "commit", "-m", "autopush commit")
	sessionCommit := gitOutput(t, source, "rev-parse", "HEAD")

	manager := newTestManager(t)
	repository := Repository{ID: "holark", RepositoryURL: remote, DefaultBranch: "main"}
	if err := manager.Ensure(context.Background(), repository); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}
	git(t, source, "push", manager.repositoryPath(repository.ID), "HEAD:refs/holark/sessions/session-1")
	if err := manager.Refresh(context.Background(), repository); err != nil {
		t.Fatalf("refresh repository: %v", err)
	}
	refs, err := manager.Refs(context.Background(), repository)
	if err != nil {
		t.Fatalf("refs: %v", err)
	}
	byName := make(map[string]Ref, len(refs))
	for _, ref := range refs {
		byName[ref.Name] = ref
	}
	if byName["refs/heads/feature/test"].Kind != "branch" || byName["refs/heads/feature/test"].ShortName != "feature/test" ||
		byName["refs/heads/feature/test"].Target != featureCommit {
		t.Fatalf("feature ref = %+v", byName["refs/heads/feature/test"])
	}
	if byName["refs/holark/sessions/session-1"].Kind != "holark_session" || byName["refs/holark/sessions/session-1"].ShortName != "session-1" ||
		byName["refs/holark/sessions/session-1"].Target != sessionCommit {
		t.Fatalf("session ref = %+v", byName["refs/holark/sessions/session-1"])
	}

	commits, err := manager.CommitHistory(context.Background(), repository, "refs/holark/sessions/session-1", 2)
	if err != nil {
		t.Fatalf("commit history: %v", err)
	}
	if len(commits) != 2 || commits[0].SHA != sessionCommit || commits[0].Message != "autopush commit" ||
		commits[1].SHA != featureCommit {
		t.Fatalf("commits = %+v", commits)
	}
	if _, err := manager.CommitHistory(context.Background(), repository, "refs/tags/release", 2); !errors.Is(err, ErrRefNotFound) {
		t.Fatalf("tag history error = %v, want %v", err, ErrRefNotFound)
	}
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	manager, err := NewManager(filepath.Join(t.TempDir(), "repositories"))
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func treeHasPath(tree Tree, path string) bool {
	for _, entry := range tree.Entries {
		if entry.Path == path {
			return true
		}
	}
	return false
}

func createRemote(t *testing.T) string {
	t.Helper()
	remote, _ := createRemoteWithSource(t)
	return remote
}

func createRemoteWithSource(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "--bare", remote)
	git(t, source, "init")
	git(t, source, "config", "user.name", "Holark Test")
	git(t, source, "config", "user.email", "holark@example.test")
	writeFile(t, source, "README.md", "# Holark\n")
	writeFile(t, source, "docs/guide.md", "Guide\n")
	writeFile(t, source, "main.go", "package main\n")
	git(t, source, "add", ".")
	git(t, source, "commit", "-m", "initial commit")
	git(t, source, "branch", "-M", "main")
	git(t, source, "remote", "add", "origin", remote)
	git(t, source, "push", "-u", "origin", "main")
	return remote, source
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitOutput(t *testing.T, dir string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func gitOutputMaybe(t *testing.T, dir string, arguments ...string) (string, error) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func git(t *testing.T, dir string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", arguments, err, output)
	}
}

func refExists(t *testing.T, repoPath, ref string) bool {
	t.Helper()
	_, err := gitOutputMaybe(t, repoPath, "rev-parse", "--verify", ref)
	return err == nil
}
