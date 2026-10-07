package gitadapter

import (
	"github.com/holark-ai/holark/internal/repository"
	"path/filepath"
	"testing"
)

func TestProviderPublicationPreservesConfiguredPushTransport(t *testing.T) {
	for _, forked := range []bool{false, true} {
		name := "origin"
		if forked {
			name = "fork"
		}
		t.Run(name, func(t *testing.T) {
			repo, base := workspaceRepository(t)
			origin := filepath.Join(t.TempDir(), "origin.git")
			gitTest(t, repo, "init", "--bare", origin)
			gitTest(t, repo, "remote", "add", "origin", origin)
			gitTest(t, repo, "push", "origin", base+":refs/heads/main")
			fork := filepath.Join(t.TempDir(), "fork.git")
			gitTest(t, repo, "clone", "--bare", origin, fork)
			adapter := openWorkspaceAdapter(t, repo)
			w, err := adapter.CreateWorkspace(t.Context(), "transport", base)
			if err != nil {
				t.Fatal(err)
			}
			head := commitFile(t, w.Path, "change.txt", "change\n", "change")
			gitTest(t, repo, "remote", "set-url", "origin", "https://github.com/owner/project.git")
			pushURL := "ssh://writer@github.com:2222/owner/project.git"
			gitTest(t, repo, "config", "remote.origin.pushurl", pushURL)
			gitTest(t, repo, "config", "url."+origin+".insteadOf", pushURL)
			gitTest(t, repo, "config", "url."+fork+".insteadOf", "ssh://writer@github.com:2222/contributor/project.git")
			gitTest(t, repo, "config", "protocol.https.allow", "never")
			source := "https://github.com/owner/project.git"
			target := origin
			if forked {
				source = "https://github.com/contributor/project.git"
				target = fork
			}
			result, err := adapter.PublishWorkspace(t.Context(), repository.PublishRequest{WorkspaceID: w.ID, Remote: source, UpstreamBranch: "main", ExpectedRemoteHead: base, ExpectedWorkspaceHead: head})
			if err != nil || result.HeadCommit != head {
				t.Fatalf("publish = %+v, %v", result, err)
			}
			if actual := gitText(t, target, "rev-parse", "refs/heads/main"); actual != head {
				t.Fatalf("published %s, want %s", actual, head)
			}
			if forked && gitText(t, origin, "rev-parse", "refs/heads/main") != base {
				t.Fatal("fork publication changed the base repository")
			}
			if got := gitText(t, repo, "config", "--get", "remote.origin.pushurl"); got != pushURL {
				t.Fatalf("push URL changed to %s", got)
			}
		})
	}
}
