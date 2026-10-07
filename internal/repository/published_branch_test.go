package repository

import "testing"

func TestPublishedRepositoryIdentity(t *testing.T) {
	want := PublishedBranchIdentity("https://github.com/Owner/Repo.git", "main")
	for _, source := range []string{"git@github.com:owner/repo.git", "ssh://git@github.com:2222/owner/repo", "https://github.com/owner/repo/"} {
		if got := PublishedBranchIdentity(source, "refs/heads/main"); got != want {
			t.Fatalf("%s: %+v != %+v", source, got, want)
		}
	}
	if PublishedBranchIdentity("git@github.com:fork/repo.git", "main") == want {
		t.Fatal("fork shares base identity")
	}
	if PublishedBranchIdentity("", "main").Valid() {
		t.Fatal("missing identity accepted")
	}
	if RepositoryIdentity("ssh://git@example.com/Owner/Repo.git") == RepositoryIdentity("https://example.com/owner/repo.git") {
		t.Fatal("case sensitive paths conflated")
	}
}
