//go:build integration

package pullrequestcomments_test

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	githubpullrequestcomments "github.com/holark-ai/holark/internal/codehost/github/pullrequestcomments"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	"github.com/holark-ai/holark/internal/pullrequestcomments/sqliteadapter"
)

func TestGitHubCLICommentRefreshHonorsRetryHeaders(t *testing.T) {
	for _, failureAt := range []string{"graphql", "rest"} {
		for _, header := range []string{"seconds", "date", "reset", "invalid"} {
			t.Run(failureAt+"/"+header, func(t *testing.T) {
				dir := t.TempDir()
				command := filepath.Join(dir, "gh")
				calls := filepath.Join(dir, "calls")
				// Like gh, emit headers only with --include, on stdout; stderr
				// contains the failure message but no retry information.
				script := `#!/bin/sh
set -eu
printf 'call\n' >> "$GH_TEST_CALLS"
kind=rest
include=false
for arg in "$@"; do
  case "$arg" in
    --include) include=true ;;
    graphql) kind=graphql ;;
  esac
done
if [ "$kind" = graphql ]; then cat >/dev/null; fi
if [ "$GH_TEST_FAILURE" = "$kind" ]; then
  if [ "$include" = true ]; then
    printf 'HTTP/2.0 429 Too Many Requests\r\n%s\r\n\r\n' "$GH_TEST_HEADERS"
  fi
  printf '{"message":"rate limit exceeded"}'
  printf 'gh: rate limit exceeded (HTTP 429)\n' >&2
  exit 1
fi
if [ "$include" = true ]; then
  printf 'HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'
fi
if [ "$kind" = graphql ]; then
  printf '{"data":{"repository":{"pullRequest":{"updatedAt":"2026-01-01T00:00:00Z"}}}}'
else
  printf '[]'
fi
`
				if err := os.WriteFile(command, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GH_TEST_CALLS", calls)
				t.Setenv("GH_TEST_FAILURE", failureAt)
				now := time.Now().UTC()
				deadline := now.Add(5 * time.Minute).Truncate(time.Second)
				headers := "Retry-After: 300"
				switch header {
				case "date":
					headers = "Retry-After: " + deadline.Format(http.TimeFormat)
				case "reset":
					headers = fmt.Sprintf("X-RateLimit-Reset: %d", deadline.Unix())
				case "invalid":
					headers = "Retry-After: invalid\r\nX-RateLimit-Reset: invalid"
				}
				t.Setenv("GH_TEST_HEADERS", headers)
				store, err := sqliteadapter.New(t.Context(), openCommentsDB(t))
				if err != nil {
					t.Fatal(err)
				}
				gateway := githubpullrequestcomments.New(&githubapi.CLIClient{Command: command})
				service := pullrequestcomments.NewService(store, providerTargets(),
					pullrequestcomments.WithProviderGateway(gateway, nil),
					pullrequestcomments.WithRefreshClock(func() time.Time { return now }))
				result, err := service.Refresh(t.Context(), "pr-one", false)
				if err == nil || result.Refreshed || result.SyncedAt != nil {
					t.Fatalf("failed refresh = %+v, %v", result, err)
				}
				var retry *githubapi.RetryError
				if header != "invalid" {
					if !errors.As(err, &retry) || retry.RetryAfter() < 290*time.Second || retry.RetryAfter() > 300*time.Second {
						t.Fatalf("missing GitHub retry deadline: %v", err)
					}
					if header != "seconds" && !retry.RetryAt.Equal(deadline) {
						t.Fatalf("retry deadline = %s, want %s", retry.RetryAt, deadline)
					}
				}
				before, err := os.ReadFile(calls)
				if err != nil {
					t.Fatal(err)
				}
				delay := 5 * time.Minute
				if header == "invalid" {
					delay = 15 * time.Second
				}
				now = now.Add(delay - 5*time.Second)
				for _, force := range []bool{false, true} {
					if _, err := service.Refresh(t.Context(), "pr-one", force); err == nil {
						t.Fatal("refresh lost the rate limit error")
					}
				}
				after, err := os.ReadFile(calls)
				if err != nil || string(after) != string(before) {
					t.Fatalf("refresh bypassed GitHub retry deadline: calls %q -> %q, %v", before, after, err)
				}
				now = now.Add(6 * time.Second)
				t.Setenv("GH_TEST_FAILURE", "")
				requireRefresh(t, service, false, true)
			})
		}
	}
}
