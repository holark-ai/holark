package api

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestActivePullRequestObservationsPaginateAndPreserveFork(t *testing.T) {
	client := &CLIClient{Command: fakeGH(t, `#!/bin/sh
input=$(cat)
printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n'
case "$input" in
*'"cursor":"second"'*)
 printf '%s\n' '{"data":{"repository":{"pullRequests":{"nodes":[{"id":"PR_2","number":2,"title":"Second","state":"OPEN","baseRefName":"main","baseRefOid":"base","headRefName":"topic","headRefOid":"head-2"}],"pageInfo":{"hasNextPage":false,"endCursor":"last"}}}}}'
 ;;
*)
 printf '%s\n' '{"data":{"repository":{"pullRequests":{"nodes":[{"id":"PR_1","number":1,"title":"Fork","state":"OPEN","baseRefName":"main","baseRefOid":"base","headRefName":"main","headRefOid":"fork-head","baseRepository":{"nameWithOwner":"owner/repo","url":"https://github.com/owner/repo"},"headRepository":{"nameWithOwner":"contributor/fork","url":"https://github.com/contributor/fork"},"timelineItems":{"nodes":[{"__typename":"ReadyForReviewEvent","id":"ready-event","createdAt":"2026-01-01T12:00:00Z"}]}}],"pageInfo":{"hasNextPage":true,"endCursor":"second"}}}}}'
 ;;
esac
`)}
	result, err := client.ListActivePullRequests(t.Context(), Repository{Owner: "owner", Name: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || result[1].Number != 2 {
		t.Fatalf("pagination result = %+v", result)
	}
	first := result[0]
	if first.Head.SHA != "fork-head" || first.Head.Repository == nil || first.Head.Repository.CloneURL != "https://github.com/contributor/fork.git" {
		t.Fatalf("fork identity = %+v", first)
	}

}

func TestActivePullRequestObservationsRejectPartialListing(t *testing.T) {
	client := &CLIClient{Command: fakeGH(t, `#!/bin/sh
input=$(cat)
printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n'
case "$input" in
*'"cursor":"second"'*) printf '%s\n' '{"errors":[{"message":"rate limit"}],"data":{"repository":null}}';;
*) printf '%s\n' '{"data":{"repository":{"pullRequests":{"nodes":[{"id":"PR_1","number":1}],"pageInfo":{"hasNextPage":true,"endCursor":"second"}}}}}';;
esac
`)}
	result, err := client.ListActivePullRequests(t.Context(), Repository{Owner: "owner", Name: "repo"})
	if err == nil || result != nil {
		t.Fatalf("partial listing returned %v, %v; want no accepted list and error", result, err)
	}
}

func TestLifecycleMutationReturnsConfirmedState(t *testing.T) {
	client := &CLIClient{Command: fakeGH(t, `#!/bin/sh
printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n'
printf '%s\n' '{"data":{"markPullRequestReadyForReview":{"pullRequest":{"id":"PR_1","number":1,"state":"OPEN","isDraft":false,"headRefName":"topic","headRefOid":"head","baseRefName":"main","baseRefOid":"base","timelineItems":{"nodes":[{"__typename":"ConvertToDraftEvent","id":"prior-draft-event","createdAt":"2026-01-01T12:00:00Z"},{"__typename":"ReadyForReviewEvent","id":"confirmed-ready-event","createdAt":"2026-01-01T12:00:00Z"}]}}}}}'
`)}
	result, err := client.MarkPullRequestReadyForReview(t.Context(), "PR_1")
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "open" || result.Draft || result.Head.SHA != "head" {
		t.Fatalf("mutation snapshot = %+v", result)
	}
}

func TestLifecycleMutationMissingSuccessBodyRemainsUncertain(t *testing.T) {
	client := &CLIClient{Command: fakeGH(t, "#!/bin/sh\nprintf '%s\\n' '{}'\n")}
	_, err := client.MarkPullRequestReadyForReview(t.Context(), "PR_1")
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.Code != ErrorCodeMutationAccepted {
		t.Fatalf("missing mutation result = %v", err)
	}
}

func TestMergeMissingOutcomeIsUncertain(t *testing.T) {
	client := &CLIClient{Command: fakeGH(t, "#!/bin/sh\nprintf '%s\\n' '{}'\n")}
	_, err := client.MergePullRequest(t.Context(), Repository{Owner: "owner", Name: "repo"}, 1, MergePullRequestRequest{ExpectedHeadSHA: "head"})
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.Code != ErrorCodeMutationAccepted {
		t.Fatalf("missing outcome = %v", err)
	}
}

func TestBulkListingIncludesCompleteParticipantsAndPaginatesOverflow(t *testing.T) {
	client := &CLIClient{Command: fakeGH(t, `#!/bin/sh
input=$(cat)
printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n'
case "$input" in
*'node(id:'*)
 printf '%s\n' '{"data":{"node":{"reviewRequests":{"nodes":[{"requestedReviewer":{"id":"U_second","login":"second"}}],"pageInfo":{"hasNextPage":false,"endCursor":"done"}}}}}'
 ;;
*)
 printf '%s\n' '{"data":{"repository":{"pullRequests":{"nodes":[{"id":"PR_1","number":1,"state":"OPEN","author":{"id":"U_author","login":"author"},"assignees":{"nodes":[],"pageInfo":{"hasNextPage":false}},"reviewRequests":{"nodes":[{"requestedReviewer":{"id":"U_first","login":"first"}},{"requestedReviewer":{}}],"pageInfo":{"hasNextPage":true,"endCursor":"more"}}},{"id":"PR_2","number":2,"state":"OPEN"}],"pageInfo":{"hasNextPage":false}}}}}'
 ;;
esac
`)}
	result, err := client.ListActivePullRequests(t.Context(), Repository{Owner: "owner", Name: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatal(result)
	}
	if result[0].User.NodeID != "U_author" || result[0].Assignees == nil || len(result[0].Assignees) != 0 || len(result[0].RequestedReviewers) != 2 || result[0].RequestedReviewers[1].Login != "second" {
		t.Fatalf("metadata: %+v", result[0])
	}
	if result[1].Assignees != nil || result[1].RequestedReviewers != nil {
		t.Fatalf("missing fields treated as authoritative empty: %+v", result[1])
	}
}

func TestActivePullRequestObservationsHonorRateLimitHeaders(t *testing.T) {
	for _, test := range []struct{ name, header string }{
		{name: "retry after", header: "Retry-After: 120"},
		{name: "rate limit reset", header: fmt.Sprintf("X-RateLimit-Reset: %d", time.Now().Add(2*time.Minute).Unix())},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &CLIClient{Command: fakeGH(t, "#!/bin/sh\ncat >/dev/null\nprintf 'HTTP/1.1 429 Too Many Requests\\r\\n"+test.header+"\\r\\n\\r\\n'\nexit 1\n")}
			_, err := client.ListActivePullRequests(t.Context(), Repository{Owner: "owner", Name: "repo"})
			var retry interface{ RetryAfter() time.Duration }
			if !errors.As(err, &retry) || retry.RetryAfter() < 110*time.Second {
				t.Fatalf("retry deadline = %v", err)
			}
		})
	}
}

func TestActivePullRequestObservationsAcceptLowRemainingQuota(t *testing.T) {
	client := &CLIClient{Command: fakeGH(t, `#!/bin/sh
cat >/dev/null
printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nX-RateLimit-Remaining: 1\r\n\r\n'
printf '%s\n' '{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}'
`)}
	result, err := client.ListActivePullRequests(t.Context(), Repository{Owner: "owner", Name: "repo"})
	if err != nil || len(result) != 0 {
		t.Fatalf("listing = %v, %v", result, err)
	}
}
