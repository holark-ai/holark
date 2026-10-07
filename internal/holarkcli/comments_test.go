package holarkcli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIssueCommentBodySourcesAndJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "body.md")
	body := "> quote\n\nMarkdown 界\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		action string
		args   []string
		stdin  string
	}{{"create", []string{"--body", body}, ""}, {"create", []string{"--body-file", path}, ""}, {"edit", []string{"--body-file", "-"}, body}} {
		t.Run(tc.action+strings.Join(tc.args, ""), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				wantMethod, wantPath := "POST", "/api/v1/issues/issue/comments"
				if tc.action == "edit" {
					wantMethod, wantPath = "PATCH", "/api/v1/issue-comments/issue"
				}
				if r.Method != wantMethod || r.URL.Path != wantPath {
					t.Errorf("request=%s %s", r.Method, r.URL.Path)
				}
				var in struct {
					Body string `json:"body"`
				}
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
				}
				if in.Body != body {
					t.Errorf("body=%q", in.Body)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(201)
				_, _ = w.Write([]byte(`{"id":"comment","body":"canonical"}`))
			}))
			defer server.Close()
			args := []string{"--server", server.URL, "issue", "comment", tc.action, "issue", "--json"}
			args = append(args, tc.args...)
			out, stderr, code := runTestCommand(args, tc.stdin)
			if code != 0 || calls != 1 || !strings.Contains(out, `"body": "canonical"`) {
				t.Fatalf("code=%d calls=%d out=%s err=%s", code, calls, out, stderr)
			}
		})
	}
}
func TestIssueCommentReadDeleteAndRecovery(t *testing.T) {
	for _, action := range []string{"list", "sync", "delete", "refresh"} {
		t.Run(action, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if action == "delete" {
					if r.Method != "DELETE" || r.URL.Path != "/api/v1/issue-comments/id" {
						t.Errorf("delete: %s %s", r.Method, r.URL.Path)
					}
					w.WriteHeader(204)
					return
				}
				wantMethod := "GET"
				if action == "sync" || action == "refresh" {
					wantMethod = "POST"
				}
				if r.Method != wantMethod {
					t.Errorf("method=%s", r.Method)
				}
				_, _ = w.Write([]byte(`{"comments":[],"synced_at":null,"can_comment":null}`))
			}))
			defer server.Close()
			command := action
			if command == "refresh" {
				command = "list"
			}
			args := []string{"--server", server.URL, "issue", "comment", command, "id", "--json"}
			if action == "refresh" {
				args = append(args, "--refresh")
			}
			out, stderr, code := runTestCommand(args, "")
			if code != 0 || !json.Valid([]byte(out)) {
				t.Fatalf("code=%d out=%s error=%s", code, out, stderr)
			}
		})
	}
	for _, status := range []int{202, 502} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(status)
			code := "issue_comment_projection_pending"
			if status == 502 {
				code = "issue_comment_outcome_uncertain"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": "Sync before retrying", "github_comment_url": "https://github.com/comment", "safe_to_retry": false})
		}))
		out, stderr, code := runTestCommand([]string{"--server", server.URL, "issue", "comment", "create", "id", "--body", "body", "--json"}, "")
		server.Close()
		if code != 1 || out != "" || calls != 1 || !strings.Contains(stderr, "Sync before retrying") || !strings.Contains(stderr, "https://github.com/comment") {
			t.Fatalf("code=%d calls=%d out=%s err=%s", code, calls, out, stderr)
		}
	}
}
func TestIssueCommentRequiresExactlyOneBodySource(t *testing.T) {
	for _, args := range [][]string{{}, {"--body", ""}, {"--body", "body", "--body-file", "-"}, {"--body", "", "--body-file", "-"}, {"--body-file", ""}} {
		_, _, code := runTestCommand(append([]string{"issue", "comment", "create", "id"}, args...), "body")
		if code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
}
