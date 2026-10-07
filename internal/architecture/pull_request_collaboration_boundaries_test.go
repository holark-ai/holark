package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPullRequestCollaborationDomainsOwnTheirBoundaries(t *testing.T) {
	root := repositoryRoot(t)
	for _, domain := range []string{"pullrequestcomments", "pullrequestparticipants", "pullrequestmetadata", "pullrequestwork"} {
		path := filepath.Join(root, "internal", domain)
		err := filepath.WalkDir(path, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && filepath.Ext(path) == ".go" && !strings.HasSuffix(path, "_test.go") {
				assertNoImports(t, path, "internal/controlplane", "internal/httpapi", "internal/store")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
