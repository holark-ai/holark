package repositorybrowser

import (
	"path/filepath"
	"testing"
)

func TestPinnedRebasePublishesForkBranchAndImportsOwningBase(t *testing.T) {
	origin, source := createRemoteWithSource(t)
	fork := filepath.Join(t.TempDir(), "fork.git")
	git(t, source, "clone", "--bare", origin, fork)
	git(t, source, "checkout", "-b", "fork-work")
	writeFile(t, source, "feature.txt", "fork work\n")
	git(t, source, "add", "feature.txt")
	git(t, source, "commit", "-m", "Fork change")
	head := gitOutput(t, source, "rev-parse", "HEAD")
	git(t, source, "push", fork, head+":refs/heads/main")
	git(t, source, "checkout", "main")
	writeFile(t, source, "base.txt", "target advanced\n")
	git(t, source, "add", "base.txt")
	git(t, source, "commit", "-m", "Advance target")
	git(t, source, "push", "origin", "main")
	base := gitOutput(t, source, "rev-parse", "HEAD")
	manager := newTestManager(t)
	original := Repository{ID: "source-rebase", RepositoryURL: origin, DefaultBranch: "main"}
	target, err := manager.PrepareRebaseSources(t.Context(), original, fork, origin, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if target.ID == original.ID || target.RepositoryURL != fork {
		t.Fatalf("fork cache = %+v", target)
	}
	result, err := manager.RebasePinned(t.Context(), target, RebaseRequest{BaseCommit: base, BaseBranch: "main", HeadBranch: "main", ExpectedHeadCommit: head})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Rebased || result.HeadCommit == head {
		t.Fatalf("fork rebase = %+v", result)
	}
	if got := gitOutput(t, fork, "rev-parse", "refs/heads/main"); got != result.HeadCommit {
		t.Fatalf("fork head = %s, want %s", got, result.HeadCommit)
	}
	if got := gitOutput(t, origin, "rev-parse", "refs/heads/main"); got != base {
		t.Fatalf("target base changed to %s", got)
	}
	git(t, fork, "merge-base", "--is-ancestor", base, result.HeadCommit)
}

func TestPinnedRebasePreservesSSHTransport(t *testing.T) {
	for _, forked := range []bool{false, true} {
		name := "origin"
		if forked {
			name = "fork"
		}
		t.Run(name, func(t *testing.T) {
			origin, source := createRemoteWithSource(t)
			fork := filepath.Join(t.TempDir(), "fork.git")
			git(t, source, "clone", "--bare", origin, fork)
			target := origin
			headURL := "https://github.com/owner/project.git"
			if forked {
				target = fork
				headURL = "https://github.com/contributor/project.git"
			}
			git(t, source, "checkout", "-b", "feature")
			writeFile(t, source, "feature.txt", "feature\n")
			git(t, source, "add", "feature.txt")
			git(t, source, "commit", "-m", "Feature")
			head := gitOutput(t, source, "rev-parse", "HEAD")
			git(t, source, "push", target, head+":refs/heads/feature")
			git(t, source, "checkout", "main")
			writeFile(t, source, "base.txt", "advance base\n")
			git(t, source, "add", "base.txt")
			git(t, source, "commit", "-m", "Base")
			git(t, source, "push", "origin", "main")
			base := gitOutput(t, source, "rev-parse", "HEAD")
			originURL := "git@github.com:owner/project.git"
			forkURL := "ssh://contributor@github.com:2222/contributor/project.git"
			git(t, source, "remote", "set-url", "origin", originURL)
			git(t, source, "remote", "add", "fork", forkURL)
			// Real Git rewrites only the expected SSH transports to local repositories.
			// HTTPS is forbidden, so falling back to the provider URL fails offline.
			t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
			git(t, source, "config", "--global", "url."+origin+".insteadOf", originURL)
			git(t, source, "config", "--global", "url."+fork+".insteadOf", forkURL)
			git(t, source, "config", "--global", "protocol.https.allow", "never")
			git(t, source, "config", "--global", "user.name", "Rebase Test")
			git(t, source, "config", "--global", "user.email", "rebase@example.test")
			manager := newTestManager(t)
			original := Repository{ID: "ssh-rebase", RepositoryURL: originURL, DefaultBranch: "main", GitDirectory: source}
			prepared, err := manager.PrepareRebaseSources(t.Context(), original, headURL, "https://github.com/owner/project.git", base, head)
			if err != nil {
				t.Fatal(err)
			}
			if (prepared.ID != original.ID) != forked {
				t.Fatalf("unexpected cache identity: %+v", prepared)
			}
			result, err := manager.RebasePinned(t.Context(), prepared, RebaseRequest{BaseCommit: base, BaseBranch: "main", HeadBranch: "feature", ExpectedHeadCommit: head})
			if err != nil || !result.Rebased {
				t.Fatalf("rebase = %+v, %v", result, err)
			}
			if got := gitOutput(t, target, "rev-parse", "refs/heads/feature"); got != result.HeadCommit {
				t.Fatalf("published %s, want %s", got, result.HeadCommit)
			}
			if got := gitOutput(t, origin, "rev-parse", "refs/heads/main"); got != base {
				t.Fatalf("base branch changed to %s", got)
			}
			git(t, target, "merge-base", "--is-ancestor", base, result.HeadCommit)
		})
	}
}
