package gitadapter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteCommitFetchUsesForkAndRemoteHeadAllowsBackwardForcePush(t *testing.T) {
	root, origin := fixture(t)
	base := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	fork := filepath.Join(t.TempDir(), "fork.git")
	git(t, root, "clone", "--bare", origin, fork)
	contributor := t.TempDir()
	git(t, contributor, "clone", "--branch", "main", fork, ".")
	git(t, contributor, "config", "user.name", "Contributor")
	git(t, contributor, "config", "user.email", "contributor@example.com")
	if err := os.WriteFile(filepath.Join(contributor, "fork.txt"), []byte("new fork commit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, contributor, "add", "fork.txt")
	git(t, contributor, "commit", "-m", "Fork change")
	git(t, contributor, "push", "origin", "main")
	head := strings.TrimSpace(git(t, contributor, "rev-parse", "HEAD"))
	adapter, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.EnsureRemoteCommit(t.Context(), fork, head); err != nil {
		t.Fatal(err)
	}
	actual, err := adapter.RemoteBranchHead(t.Context(), fork, "main")
	if err != nil || actual != head {
		t.Fatalf("fork head = %q, %v; want %q", actual, err, head)
	}
	if got := strings.TrimSpace(git(t, root, "rev-parse", "refs/heads/main")); got != base {
		t.Fatalf("fetch changed local branch to %s", got)
	}
	if got, err := adapter.MergeBase(t.Context(), base, head); err != nil || got != base {
		t.Fatalf("pinned merge base = %q, %v", got, err)
	}
	git(t, contributor, "push", "--force", "origin", base+":refs/heads/main")
	actual, err = adapter.RemoteBranchHead(t.Context(), fork, "main")
	if err != nil || actual != base {
		t.Fatalf("verified backward force-push = %q, %v; want %q", actual, err, base)
	}
}

func TestProviderRemoteReadsPreserveSSHTransport(t *testing.T) {
	for _, namedFork := range []bool{false, true} {
		t.Run(fmt.Sprintf("named_fork=%t", namedFork), func(t *testing.T) {
			root, origin := fixture(t)
			base := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
			fork := filepath.Join(t.TempDir(), "fork.git")
			git(t, root, "clone", "--bare", origin, fork)
			contributor := t.TempDir()
			git(t, contributor, "clone", "--branch", "main", fork, ".")
			git(t, contributor, "config", "user.name", "Contributor")
			git(t, contributor, "config", "user.email", "contributor@example.com")
			if err := os.WriteFile(filepath.Join(contributor, "fork.txt"), []byte("fork\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git(t, contributor, "add", "fork.txt")
			git(t, contributor, "commit", "-m", "Fork change")
			git(t, contributor, "push", "origin", "main")
			head := strings.TrimSpace(git(t, contributor, "rev-parse", "HEAD"))
			originURL := "git@github.com:owner/project.git"
			forkURL := "git@github.com:contributor/project.git"
			git(t, root, "remote", "set-url", "origin", originURL)
			if namedFork {
				// An explicit fork remote wins over the origin's SSH user/port.
				forkURL = "ssh://fork-user@github.com:2222/contributor/project.git"
				git(t, root, "remote", "add", "fork", forkURL)
			}
			git(t, root, "config", "url."+origin+".insteadOf", originURL)
			git(t, root, "config", "url."+fork+".insteadOf", forkURL)
			git(t, root, "config", "protocol.https.allow", "never")
			adapter, err := Open(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			for _, target := range []struct{ url, head string }{
				{"https://github.com/owner/project.git", base},
				{"https://github.com/contributor/project.git", head},
			} {
				actual, err := adapter.RemoteBranchHead(t.Context(), target.url, "main")
				if err != nil || actual != target.head {
					t.Fatalf("head for %s = %s, %v", target.url, actual, err)
				}
			}
			if err := adapter.EnsureRemoteCommit(t.Context(), "https://github.com/contributor/project.git", head); err != nil {
				t.Fatal(err)
			}
			if got, err := adapter.MergeBase(t.Context(), base, head); err != nil || got != base {
				t.Fatalf("merge base = %s, %v", got, err)
			}
			if got := strings.TrimSpace(git(t, root, "config", "--get", "remote.origin.url")); got != originURL {
				t.Fatalf("origin changed to %s", got)
			}
			if _, err := adapter.RemoteBranchHead(t.Context(), "https://elsewhere.invalid/contributor/project.git", "main"); err == nil {
				t.Fatal("unrelated host was redirected to a configured repository")
			}
		})
	}
}

func TestObserveBranchesDistinguishesMissingRefsAndFailedReads(t *testing.T) {
	root, origin := fixture(t)
	adapter, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	refs := []string{"refs/heads/main", "refs/heads/missing"}
	observed, err := adapter.ObserveBranches(t.Context(), origin, refs)
	if err != nil {
		t.Fatal(err)
	}
	if !observed[refs[0]].Exists || observed[refs[0]].Commit == "" || observed[refs[1]].Exists {
		t.Fatalf("observations = %+v", observed)
	}
	if _, err := adapter.ObserveBranches(t.Context(), filepath.Join(t.TempDir(), "absent.git"), refs); err == nil {
		t.Fatal("failed read accepted as deletion")
	}
	if _, err := adapter.ObserveBranches(t.Context(), "", refs); err == nil {
		t.Fatal("unknown fork fell back to origin")
	}
}
