package pullrequests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
)

// Exercise the CLI decoder and provider together across complete paginated
// responses. GitHub is controlled here; no agent CLI is involved.
func TestActiveListingRequiresCompletePages(t *testing.T) {
	for _, scenario := range []string{"complete", "partial", "missing page info", "cursor cycle"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			for page := 1; page <= 3; page++ {
				node := map[string]any{"id": "PR_1", "number": page, "title": "Fixture", "state": "OPEN", "baseRefName": "main", "headRefName": "main", "baseRefOid": "historical-base", "headRefOid": "historical-head", "baseRepository": map[string]any{"nameWithOwner": "owner/repo", "url": "https://github.com/owner/repo"}, "headRepository": map[string]any{"nameWithOwner": "contributor/fork", "url": "https://github.com/contributor/fork"}}
				listing := map[string]any{"nodes": []any{node}, "pageInfo": map[string]any{"hasNextPage": page < 3, "endCursor": []string{"", "second", "third", "last"}[page]}}
				if scenario == "missing page info" && page == 2 {
					delete(listing, "pageInfo")
				}
				if scenario == "cursor cycle" && page == 3 {
					listing["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": "second"}
				}
				response := map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequests": listing}}}
				if scenario == "partial" && page == 2 {
					response["errors"] = []any{map[string]any{"message": "inaccessible page"}}
				}
				raw, _ := json.Marshal(response)
				if err := os.WriteFile(filepath.Join(root, []string{"", "first", "second", "third"}[page]), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			command := filepath.Join(root, "gh")
			script := "#!/bin/sh\ncd '" + root + "'\ninput=$(cat)\nprintf 'HTTP/1.1 200 OK\\r\\nContent-Type: application/json\\r\\n\\r\\n'\ncase \"$input\" in *'\"cursor\":\"second\"'*) cat second;; *'\"cursor\":\"third\"'*) cat third;; *) cat first;; esac\n"
			if err := os.WriteFile(command, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			provider := New(&githubapi.CLIClient{Command: command})
			values, err := provider.ListActive(t.Context(), "https://github.com/owner/repo.git")
			if scenario == "partial" || scenario == "missing page info" || scenario == "cursor cycle" {
				if err == nil || values != nil {
					t.Fatalf("partial listing escaped: %v %v", values, err)
				}
				return
			}
			if err != nil || len(values) != 3 {
				t.Fatalf("listing=%+v %v", values, err)
			}
			if values[0].BaseCommit != "historical-base" || values[0].HeadCommit != "historical-head" {
				t.Fatal("live evidence overwrote historical PR fields")
			}
		})
	}
}
