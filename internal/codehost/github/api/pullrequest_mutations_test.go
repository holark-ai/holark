package api

import (
	"context"
	"errors"
	"testing"
)

func TestObservedPullRequestMutationsDistinguishConfirmedRejectedAndUnknownWrites(t *testing.T) {
	for _, outcome := range []struct {
		name, status, body string
		exit               string
		uncertain          bool
		success            bool
	}{
		{name: "confirmed", status: "200 OK", body: `{"number":1,"title":"Saved","body":"Body","updated_at":"2026-01-01T12:00:00Z","merged":true,"sha":"merged"}`, exit: "0", success: true},
		{name: "provider rejection", status: "403 Forbidden", body: `{"message":"Write denied"}`, exit: "1"},
		{name: "server failure", status: "502 Bad Gateway", body: `{"message":"upstream failed"}`, exit: "1", uncertain: true},
		{name: "lost response", exit: "1", uncertain: true},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			t.Setenv("GH_RESPONSE_STATUS", outcome.status)
			t.Setenv("GH_RESPONSE_BODY", outcome.body)
			t.Setenv("GH_RESPONSE_EXIT", outcome.exit)
			client := &CLIClient{Command: fakeGH(t, `#!/bin/sh
case "$*" in
*"--include --method "*) ;;
*) echo "missing response headers" >&2; exit 1;;
esac
cat >/dev/null
if [ -n "$GH_RESPONSE_STATUS" ]; then
 printf 'HTTP/1.1 %s\r\nContent-Type: application/json\r\n\r\n' "$GH_RESPONSE_STATUS"
 printf '%s' "$GH_RESPONSE_BODY"
fi
exit "$GH_RESPONSE_EXIT"
`)}
			repository := Repository{Owner: "owner", Name: "repo"}
			mutations := map[string]func(context.Context) error{
				"create": func(ctx context.Context) error {
					_, err := client.CreatePullRequest(ctx, repository, CreatePullRequestRequest{Title: "Saved", Body: "Body", Head: "topic", Base: "main"})
					return err
				},
				"lifecycle": func(ctx context.Context) error {
					_, err := client.UpdatePullRequestState(ctx, repository, 1, "closed")
					return err
				},
				"metadata": func(ctx context.Context) error {
					_, err := client.UpdatePullRequest(ctx, repository, 1, UpdatePullRequestRequest{Title: "Saved", Body: "Body"})
					return err
				},
				"merge": func(ctx context.Context) error {
					_, err := client.MergePullRequest(ctx, repository, 1, MergePullRequestRequest{ExpectedHeadSHA: "head"})
					return err
				},
			}
			for name, mutate := range mutations {
				t.Run(name, func(t *testing.T) {
					err := mutate(t.Context())
					if outcome.success {
						if err != nil {
							t.Fatal(err)
						}
						return
					}
					var state interface{ Uncertain() bool }
					if err == nil || !errors.As(err, &state) || state.Uncertain() != outcome.uncertain {
						t.Fatalf("mutation error = %v, uncertainty=%v, want %t", err, state, outcome.uncertain)
					}
				})
			}
		})
	}
}
