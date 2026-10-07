package gitadapter

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/repository"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v: %s", args, e, b)
	}
	return string(b)
}
func fixture(t *testing.T) (string, string) {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	git(t, "/", "init", "--bare", remote)
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Test")
	git(t, root, "config", "user.email", "test@example.com")
	os.WriteFile(filepath.Join(root, "README.md"), []byte("main\n"), 0600)
	git(t, root, "add", "README.md")
	git(t, root, "commit", "-m", "initial")
	git(t, root, "remote", "add", "origin", remote)
	git(t, root, "push", "-u", "origin", "main")
	return root, remote
}

func TestOpenBrowseAndStableCommonDirectoryIdentity(t *testing.T) {
	root, _ := fixture(t)
	os.Mkdir(filepath.Join(root, "zDir"), 0700)
	os.Mkdir(filepath.Join(root, "aDir"), 0700)
	os.WriteFile(filepath.Join(root, "zDir", "nested.txt"), []byte("nested\n"), 0600)
	os.WriteFile(filepath.Join(root, "aDir", "nested.txt"), []byte("nested\n"), 0600)
	os.WriteFile(filepath.Join(root, "alpha.txt"), []byte("alpha\n"), 0600)
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "tree ordering")
	git(t, root, "push", "origin", "main")
	g, e := Open(t.Context(), filepath.Join(root, "."))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = g.Refresh(t.Context()); e != nil {
		t.Fatal(e)
	}
	nested := filepath.Join(root, "nested")
	os.Mkdir(nested, 0700)
	again, e := Open(t.Context(), nested)
	if e != nil {
		t.Fatal(e)
	}
	if g.Descriptor().ID != again.Descriptor().ID {
		t.Fatalf("identity changed: %s != %s", g.Descriptor().ID, again.Descriptor().ID)
	}
	discoveredID, e := RepositoryID(t.Context(), nested)
	if e != nil || discoveredID != g.Descriptor().ID {
		t.Fatalf("discovered identity = %q, error = %v, want %q", discoveredID, e, g.Descriptor().ID)
	}
	tree, e := g.Tree(t.Context(), "main", "")
	if e != nil || len(tree.Entries) != 4 {
		t.Fatalf("tree=%+v err=%v", tree, e)
	}
	if tree.Ref != "main" {
		t.Fatalf("tree ref=%q", tree.Ref)
	}
	gotEntries := []string{}
	for _, entry := range tree.Entries {
		gotEntries = append(gotEntries, entry.Type+":"+entry.Name)
	}
	wantEntries := []string{"directory:aDir", "directory:zDir", "file:alpha.txt", "file:README.md"}
	if !reflect.DeepEqual(gotEntries, wantEntries) {
		t.Fatalf("entries=%v, want %v", gotEntries, wantEntries)
	}
	blob, e := g.Blob(t.Context(), "main", "README.md")
	if e != nil || blob.Content != "main\n" || blob.Ref != "main" {
		t.Fatalf("blob=%+v err=%v", blob, e)
	}
	commits, e := g.Commits(t.Context(), "main", 10)
	if e != nil || len(commits) != 2 {
		t.Fatalf("commits=%+v err=%v", commits, e)
	}
}

func TestCommitsPreservesFullMessages(t *testing.T) {
	root, _ := fixture(t)
	messages := []string{
		"first\n\nExplain the first change.\nInclude another line.\n\nDescribe why it is needed.",
		"second\n\nExplain the follow-up change.\n\nDescribe its impact.",
	}
	var shas []string
	for _, message := range messages {
		git(t, root, "commit", "--allow-empty", "-m", message)
		shas = append(shas, strings.TrimSpace(git(t, root, "rev-parse", "HEAD")))
	}
	adapter, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	commits, err := adapter.Commits(t.Context(), shas[1], 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("commits=%+v, want two commits", commits)
	}
	for i, commit := range commits {
		want := len(messages) - 1 - i
		if commit.SHA != shas[want] || commit.Message != messages[want] {
			t.Errorf("commit %d = %+v, want SHA %q and message %q", i, commit, shas[want], messages[want])
		}
		if commit.AuthorName != "Test" || commit.AuthorEmail != "test@example.com" || commit.AuthoredAt.IsZero() {
			t.Errorf("commit %d metadata=%+v", i, commit)
		}
	}
}

func TestRefreshPrunesDeletedRemoteHeadOnlyFromPrivateNamespace(t *testing.T) {
	root, remote := fixture(t)
	other := t.TempDir()
	git(t, other, "clone", remote, ".")
	git(t, other, "config", "user.name", "Test")
	git(t, other, "config", "user.email", "test@example.com")
	git(t, other, "switch", "-c", "deleted")
	os.WriteFile(filepath.Join(other, "deleted.txt"), []byte("gone\n"), 0600)
	git(t, other, "add", ".")
	git(t, other, "commit", "-m", "deleted branch")
	git(t, other, "push", "origin", "deleted")
	g, _ := Open(t.Context(), root)
	if _, err := g.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if out := git(t, root, "show-ref"); !contains(out, "refs/holark/browse/origin/deleted") {
		t.Fatalf("private ref missing: %s", out)
	}
	os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("unchanged\n"), 0600)
	before := snapshot(t, root)
	git(t, other, "push", "origin", "--delete", "deleted")
	if _, err := g.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("checkout mutated\nbefore=%q\nafter=%q", before, after)
	}
	out := git(t, root, "show-ref")
	if contains(out, "refs/holark/browse/origin/deleted") {
		t.Fatalf("deleted private ref remains: %s", out)
	}
	if contains(out, "refs/remotes/origin/deleted") || contains(out, "refs/heads/deleted") {
		t.Fatalf("user ref changed: %s", out)
	}
}

func TestRefreshRemoteHeadsDoesNotMutateCheckout(t *testing.T) {
	root, remote := fixture(t)
	other := t.TempDir()
	git(t, other, "clone", remote, ".")
	git(t, other, "config", "user.name", "Test")
	git(t, other, "config", "user.email", "test@example.com")
	git(t, other, "switch", "-c", "feature")
	os.WriteFile(filepath.Join(other, "feature.txt"), []byte("remote\n"), 0600)
	git(t, other, "add", "feature.txt")
	git(t, other, "commit", "-m", "feature")
	git(t, other, "push", "origin", "feature")
	os.WriteFile(filepath.Join(root, "README.md"), []byte("dirty\n"), 0600)
	os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("keep\n"), 0600)
	git(t, root, "add", "README.md")
	os.WriteFile(filepath.Join(root, "README.md"), []byte("dirtier\n"), 0600)
	before := snapshot(t, root)
	g, e := Open(t.Context(), root)
	if e != nil {
		t.Fatal(e)
	}
	refs, e := g.Refresh(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	after := snapshot(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("checkout mutated\nbefore=%q\nafter=%q", before, after)
	}
	found := false
	for _, r := range refs {
		if r.Name == "refs/holark/browse/origin/feature" {
			found = true
		}
	}
	if !found {
		t.Fatalf("remote ref absent: %+v", refs)
	}
	if out := git(t, root, "show-ref"); contains(out, "refs/remotes/origin/feature") || contains(out, "refs/heads/feature") {
		t.Fatalf("user-visible ref changed: %s", out)
	}
	if _, e = os.Stat(filepath.Join(root, ".git", "FETCH_HEAD")); e == nil {
		t.Fatal("FETCH_HEAD was written")
	}
}

func snapshot(t *testing.T, root string) []string {
	return []string{git(t, root, "symbolic-ref", "HEAD"), git(t, root, "status", "--porcelain=v1", "--untracked-files=all"), git(t, root, "diff", "--cached"), git(t, root, "diff"), string(mustRead(t, filepath.Join(root, "untracked.txt")))}
}
func mustRead(t *testing.T, p string) []byte {
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestDirtyFingerprintChangesWithMaterialState(t *testing.T) {
	root, _ := fixture(t)
	g, _ := Open(context.Background(), root)
	clean, e := g.Dirty(t.Context())
	if e != nil || clean.Dirty {
		t.Fatalf("clean=%+v err=%v", clean, e)
	}
	os.WriteFile(filepath.Join(root, "README.md"), []byte("one\n"), 0600)
	first, _ := g.Dirty(t.Context())
	os.WriteFile(filepath.Join(root, "README.md"), []byte("two\n"), 0600)
	second, _ := g.Dirty(t.Context())
	if first.Fingerprint == second.Fingerprint {
		t.Fatalf("fingerprint did not change: %s", first.Fingerprint)
	}
}

func TestRepositoryPathErrorsRemainSpecific(t *testing.T) {
	root, _ := fixture(t)
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "guide.md"), []byte("guide\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "docs/guide.md")
	git(t, root, "commit", "-m", "docs")
	git(t, root, "push", "origin", "main")
	adapter, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.Tree(t.Context(), "main", "README.md"); !errors.Is(err, repository.ErrPathNotDirectory) {
		t.Fatalf("tree file error=%v", err)
	}
	if _, err = adapter.Blob(t.Context(), "main", "docs"); !errors.Is(err, repository.ErrPathNotFile) {
		t.Fatalf("blob directory error=%v", err)
	}
	if _, err = adapter.Blob(t.Context(), "main", "missing"); !errors.Is(err, repository.ErrPathNotFound) {
		t.Fatalf("missing blob error=%v", err)
	}
	if _, err = adapter.Tree(t.Context(), "missing-ref", ""); !errors.Is(err, repository.ErrRefNotFound) {
		t.Fatalf("missing ref error=%v", err)
	}
}

func TestProviderBranchBrowsingAndPreparationIgnoreBehindLocalBranch(t *testing.T) {
	root, remote := fixture(t)
	git(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	localCommit := strings.TrimSpace(git(t, root, "rev-parse", "main"))
	provider := t.TempDir()
	git(t, provider, "clone", remote, ".")
	git(t, provider, "config", "user.name", "Provider")
	git(t, provider, "config", "user.email", "provider@example.com")
	os.WriteFile(filepath.Join(provider, "README.md"), []byte("provider\n"), 0o600)
	git(t, provider, "commit", "-am", "provider main")
	git(t, provider, "push", "origin", "main")
	providerCommit := strings.TrimSpace(git(t, provider, "rev-parse", "HEAD"))

	adapter, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	blob, err := adapter.Blob(t.Context(), "origin/main", "README.md")
	if err != nil || blob.Content != "provider\n" || blob.Ref != "main" {
		t.Fatalf("blob=%+v err=%v", blob, err)
	}
	if resolved, err := adapter.Resolve(t.Context(), "main"); err != nil || resolved != localCommit {
		t.Fatalf("generic resolve=%q err=%v, want local %q", resolved, err, localCommit)
	}
	prepared, err := adapter.PrepareBranch(t.Context(), "origin/main")
	if err != nil || prepared.Branch != "main" || prepared.Commit != providerCommit {
		t.Fatalf("preparation=%+v err=%v", prepared, err)
	}
}

func TestTargetedPreparationUpdatesOnlySelectedProviderRefAndNeverUsesStaleRef(t *testing.T) {
	root, remote := fixture(t)
	git(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	provider := t.TempDir()
	git(t, provider, "clone", remote, ".")
	git(t, provider, "config", "user.name", "Provider")
	git(t, provider, "config", "user.email", "provider@example.com")
	git(t, provider, "switch", "-c", "feature")
	os.WriteFile(filepath.Join(provider, "feature.txt"), []byte("one\n"), 0o600)
	git(t, provider, "add", "feature.txt")
	git(t, provider, "commit", "-m", "feature one")
	git(t, provider, "push", "origin", "feature")

	adapter, _ := Open(t.Context(), root)
	if _, err := adapter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	oldFeature, err := adapter.ResolveProviderBranch(t.Context(), "feature")
	if err != nil {
		t.Fatal(err)
	}
	git(t, provider, "switch", "main")
	os.WriteFile(filepath.Join(provider, "main.txt"), []byte("two\n"), 0o600)
	git(t, provider, "add", "main.txt")
	git(t, provider, "commit", "-m", "main two")
	git(t, provider, "push", "origin", "main")
	newMain := strings.TrimSpace(git(t, provider, "rev-parse", "HEAD"))
	git(t, provider, "switch", "feature")
	os.WriteFile(filepath.Join(provider, "feature.txt"), []byte("two\n"), 0o600)
	git(t, provider, "commit", "-am", "feature two")
	git(t, provider, "push", "origin", "feature")

	prepared, err := adapter.PrepareBranch(t.Context(), "main")
	if err != nil || prepared.Commit != newMain {
		t.Fatalf("preparation=%+v err=%v", prepared, err)
	}
	if feature, err := adapter.ResolveProviderBranch(t.Context(), "feature"); err != nil || feature != oldFeature {
		t.Fatalf("feature=%q err=%v, want cached %q", feature, err, oldFeature)
	}
	git(t, provider, "push", "origin", "--delete", "feature")
	if _, err := adapter.PrepareBranch(t.Context(), "feature"); err == nil {
		t.Fatal("preparing deleted branch succeeded from stale cache")
	}
	if cached, err := adapter.ResolveProviderBranch(t.Context(), "feature"); err != nil || cached != oldFeature {
		t.Fatalf("failed preparation unexpectedly changed cache: %q %v", cached, err)
	}
}

func TestProviderDefaultAndVisibleRefProjection(t *testing.T) {
	root, remote := fixture(t)
	git(t, root, "branch", "trunk")
	git(t, root, "push", "origin", "trunk")
	git(t, remote, "symbolic-ref", "HEAD", "refs/heads/trunk")
	git(t, root, "branch", "local-only")
	git(t, root, "branch", "holark/work")
	adapter, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Descriptor().DefaultBranch != "trunk" || adapter.DefaultRef() != "refs/holark/browse/origin/trunk" {
		t.Fatalf("descriptor=%+v default_ref=%q", adapter.Descriptor(), adapter.DefaultRef())
	}
	refs, err := adapter.Refresh(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, ref := range refs {
		got[ref.ShortName] = ref.Kind
	}
	if got["main"] != "branch" || got["trunk"] != "branch" || got["holark/work"] != "holark_branch" {
		t.Fatalf("refs=%+v", refs)
	}
	if _, ok := got["local-only"]; ok {
		t.Fatalf("local branch leaked into refs: %+v", refs)
	}
}

func TestOriginlessAttachedAndDetachedFallback(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Test")
	git(t, root, "config", "user.email", "test@example.com")
	os.WriteFile(filepath.Join(root, "README.md"), []byte("local\n"), 0o600)
	git(t, root, "add", "README.md")
	git(t, root, "commit", "-m", "local")
	commit := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	git(t, root, "branch", "hidden")
	adapter, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := adapter.Refresh(t.Context())
	if err != nil || len(refs) != 1 || refs[0].ShortName != "main" {
		t.Fatalf("refs=%+v err=%v", refs, err)
	}
	prepared, err := adapter.PrepareBranch(t.Context(), "origin/main")
	if err != nil || prepared.Commit != commit || prepared.Branch != "main" {
		t.Fatalf("prepared=%+v err=%v", prepared, err)
	}
	git(t, root, "checkout", "--detach")
	detached, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if detached.DefaultRef() != "HEAD" {
		t.Fatalf("default ref=%q", detached.DefaultRef())
	}
	if blob, err := detached.Blob(t.Context(), "", "README.md"); err != nil || blob.Content != "local\n" {
		t.Fatalf("blob=%+v err=%v", blob, err)
	}
	if _, err := detached.PrepareBranch(t.Context(), "main"); !errors.Is(err, repository.ErrRepositoryUnavailable) {
		t.Fatalf("detached preparation error=%v", err)
	}
}

func TestBrowseSnapshotsAndPreviewLimits(t *testing.T) {
	root, _ := fixture(t)
	files := map[string][]byte{
		"pnpm-lock.yaml": []byte(strings.Repeat("dependency: version\n", 20000)),
		"binary.dat":     {0, 1, 2},
		"large.txt":      []byte(strings.Repeat("x", maxBlob+1)),
		"100%25\n.txt":   []byte("  exact bytes\n\n"),
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "preview files")
	git(t, root, "push", "origin", "main")
	adapter, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	tree, err := adapter.Tree(t.Context(), "main", "")
	if err != nil || tree.Commit != strings.TrimSpace(git(t, root, "rev-parse", "HEAD")) {
		t.Fatalf("snapshot = %+v, error = %v", tree, err)
	}
	if err := os.WriteFile(filepath.Join(root, "pnpm-lock.yaml"), []byte("updated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "advance branch")
	git(t, root, "push", "origin", "main")
	if _, err := adapter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	blob, err := adapter.Blob(t.Context(), tree.Commit, "pnpm-lock.yaml")
	if err != nil || blob.Commit != tree.Commit || blob.Language != "yaml" || blob.Content != string(files["pnpm-lock.yaml"]) {
		t.Fatalf("snapshot blob: commit=%s language=%s bytes=%d error=%v", blob.Commit, blob.Language, len(blob.Content), err)
	}
	current, err := adapter.Blob(t.Context(), "main", "pnpm-lock.yaml")
	if err != nil || current.Commit == tree.Commit || current.Content != "updated\n" {
		t.Fatalf("current blob = %+v, error = %v", current, err)
	}
	literal, err := adapter.Blob(t.Context(), tree.Commit, "100%25\n.txt")
	if err != nil || literal.Content != string(files["100%25\n.txt"]) {
		t.Fatalf("literal filename blob = %+v, error = %v", literal, err)
	}
	for _, test := range []struct {
		path string
		want error
	}{
		{"binary.dat", repository.ErrBinaryFile},
		{"large.txt", repository.ErrFileTooLarge},
		{"absent", repository.ErrPathNotFound},
	} {
		if _, err := adapter.Blob(t.Context(), tree.Commit, test.path); !errors.Is(err, test.want) {
			t.Errorf("%s: error = %v, want %v", test.path, err, test.want)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := adapter.Blob(ctx, tree.Commit, "README.md"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
	if err := os.Rename(filepath.Join(root, ".git"), filepath.Join(root, "unavailable.git")); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Blob(t.Context(), tree.Commit, "README.md"); !errors.Is(err, repository.ErrRepositoryUnavailable) {
		t.Fatalf("unavailable repository: %v", err)
	}
}
