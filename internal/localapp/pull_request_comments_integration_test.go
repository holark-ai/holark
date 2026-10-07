package localapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/pullrequestcomments"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
)

func TestApplicationLegacyCommentSyncReturnsRefreshedComments(t *testing.T) {
	// Control only GitHub; keep the application's routes, provider adapter,
	// comment service, refresh coordinator, and SQLite storage wired together.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(`#!/bin/sh
# Match gh api --include, as required by the retry-header reader.
if [ "$1 $2" = "api --include" ]; then
  shift 2
  set -- api "$@"
  printf 'HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'
fi
case "$1 $2" in
  'api /repos/fixture/repo/issues/1/comments?per_page=100&page=1')
    printf '%s\n' '[{"id":42,"body":"Fresh from GitHub","user":{"node_id":"author","login":"reviewer"}}]'
    ;;
  'api /repos/fixture/repo/pulls/1/comments?per_page=100&page=1')
    printf '%s\n' '[]'
    ;;
  'api graphql')
    cat >/dev/null
    printf '%s\n' '{"data":{"repository":{"pullRequest":{"updatedAt":"2026-01-01T00:00:00Z","totalCommentsCount":1,"comments":{"totalCount":1},"reviewThreads":{"totalCount":0,"nodes":[]}}}}}'
    ;;
  *) exit 1 ;;
esac
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := t.TempDir()
	applicationLockGit(t, root, "init", "-b", "main")
	applicationLockGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	repositoryURL := "https://github.com/fixture/repo"
	app, err := New(t.Context(), Options{RepositoryPath: root, HomeDirectory: t.TempDir(), GitHubRepositoryURL: &repositoryURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	catalog, err := prsqlite.New(t.Context(), app.Database)
	if err != nil {
		t.Fatal(err)
	}
	pr, err := catalog.CreatePullRequest(pullrequestlifecycle.PullRequest{
		ID: "pr", RepositoryID: app.RepositoryID, Status: pullrequestlifecycle.StatusOpen,
		SyncProvider: "github", SyncExternalID: "PR_1", SyncData: json.RawMessage(`{"github":{"number":1}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	response := httptest.NewRecorder()
	app.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/"+pr.ID+"/comments/sync", nil).WithContext(ctx))
	if ctx.Err() != nil {
		t.Fatalf("legacy comment sync did not finish before the request deadline: %v", ctx.Err())
	}
	if response.Code != http.StatusOK {
		t.Fatalf("sync status = %d: %s", response.Code, response.Body.String())
	}
	var comments []pullrequestcomments.Comment
	if err := json.Unmarshal(response.Body.Bytes(), &comments); err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Body != "Fresh from GitHub" {
		t.Fatalf("sync returned cached comments instead of refreshed comments: %+v", comments)
	}
}
