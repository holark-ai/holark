package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkStateUsesOneCompleteObservationWithLiveForkTargets(t *testing.T) {
	for _, scenario := range []string{"open", "draft", "closed", "merged", "deleted base", "deleted head", "missing target", "missing repository", "missing title", "missing body", "missing draft", "missing closedAt", "missing mergedAt", "missing mergeCommit", "invalid state", "wrong repository", "wrong number", "wrong source", "bad oid", "partial errors", "malformed JSON"} {
		t.Run(scenario, func(t *testing.T) {
			var p map[string]any
			if err := json.Unmarshal([]byte(`{"id":"PR_1","number":1,"title":"Fork","body":"","url":"https://github.com/owner/repo/pull/1","state":"OPEN","isDraft":false,"createdAt":"2026-01-01T12:00:00Z","updatedAt":"2026-01-01T12:00:00Z","baseRefName":"main","headRefName":"main","baseRefOid":"historical-base","headRefOid":"historical-head","baseRepository":{"nameWithOwner":"owner/repo","url":"https://github.com/owner/repo"},"headRepository":{"nameWithOwner":"contributor/fork","url":"https://github.com/contributor/fork"},"baseRef":{"target":{"__typename":"Commit","oid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},"headRef":{"target":{"__typename":"Commit","oid":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}`), &p); err != nil {
				t.Fatal(err)
			}
			repo := map[string]any{"nameWithOwner": "owner/repo", "pullRequest": p}
			p["closedAt"], p["mergedAt"], p["mergeCommit"] = nil, nil, nil
			response := map[string]any{"data": map[string]any{"repository": repo}}
			switch scenario {
			case "draft":
				p["isDraft"] = true
			case "closed", "merged":
				p["state"] = strings.ToUpper(scenario)
				p["closedAt"] = "2026-01-02T12:00:00Z"
				if scenario == "merged" {
					p["mergedAt"] = p["closedAt"]
					p["mergeCommit"] = map[string]any{"oid": strings.Repeat("c", 40)}
				}
			case "deleted base":
				p["baseRef"] = nil
			case "deleted head":
				p["headRef"] = nil
			case "missing target":
				p["headRef"] = map[string]any{}
			case "missing repository":
				p["headRepository"] = nil
			case "missing title":
				delete(p, "title")
			case "missing body":
				delete(p, "body")
			case "missing draft":
				delete(p, "isDraft")
			case "missing closedAt", "missing mergedAt", "missing mergeCommit":
				delete(p, strings.TrimPrefix(scenario, "missing "))
			case "invalid state":
				p["state"] = "UNKNOWN"
			case "wrong repository":
				repo["nameWithOwner"] = "other/repo"
			case "wrong number":
				p["number"] = 2
			case "wrong source":
				p["baseRepository"].(map[string]any)["url"] = "https://github.com/other/repo"
			case "bad oid":
				p["headRef"].(map[string]any)["target"].(map[string]any)["oid"] = "bad"
			case "partial errors":
				response["errors"] = []any{map[string]any{"message": "partial response"}}
			}
			body, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "malformed JSON" {
				body = []byte("{")
			}
			t.Setenv("GH_WORK_RESPONSE", string(body))
			log := filepath.Join(t.TempDir(), "requests")
			t.Setenv("GH_WORK_LOG", log)
			client := &CLIClient{Command: fakeGH(t, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$GH_WORK_LOG\"\nprintf '%s\\n' \"$GH_WORK_RESPONSE\"\n")}
			result, err := client.PullRequestWorkState(t.Context(), Repository{Owner: "owner", Name: "repo"}, 1)
			requests, readErr := os.ReadFile(log)
			if readErr != nil || strings.Count(string(requests), "\n") != 1 || !strings.Contains(string(requests), "baseRef{target{__typename oid}}") || !strings.Contains(string(requests), "api graphql") || strings.Contains(string(requests), "timelineItems") {
				t.Fatalf("requests=%s error=%v", requests, readErr)
			}
			if scenario != "open" && scenario != "draft" && scenario != "closed" && scenario != "merged" {
				if err == nil {
					t.Fatalf("accepted incomplete work state: %+v", result)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantState := "open"
			if scenario == "closed" || scenario == "merged" {
				wantState = "closed"
			}
			if result.State != wantState || result.Merged != (scenario == "merged") {
				t.Fatalf("work status=%+v", result)
			}
			if result.Base.SHA != strings.Repeat("a", 40) || result.Head.SHA != strings.Repeat("b", 40) || result.Head.Repository.CloneURL != "https://github.com/contributor/fork.git" || result.Draft != (scenario == "draft") {
				t.Fatalf("work observation=%+v", result)
			}
		})
	}
}
