//go:build integration

package pullrequestcomments_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	githubpullrequestcomments "github.com/holark-ai/holark/internal/codehost/github/pullrequestcomments"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	"github.com/holark-ai/holark/internal/pullrequestcomments/sqliteadapter"
)

type githubLifecycleComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type githubLifecycleClient struct {
	comments            map[int][]githubLifecycleComment
	nextID              int64
	listFailures        int
	createFailures      int
	permanentFailures   int
	lostCreateResponses int
	posts               []string
	patches             []string
}

func (client *githubLifecycleClient) Request(_ context.Context, method, path string, input, response any) error {
	parts := strings.Split(strings.Split(path, "?")[0], "/")
	if method == "GET" && len(parts) >= 7 && parts[4] == "issues" && parts[6] == "comments" {
		if client.listFailures > 0 {
			client.listFailures--
			return errors.New("temporary list failure")
		}
		number, _ := strconv.Atoi(parts[5])
		return decodeGitHubFixture(response, client.comments[number])
	}
	if method == "GET" && len(parts) >= 7 && parts[4] == "pulls" && parts[6] == "comments" {
		return decodeGitHubFixture(response, []map[string]any{})
	}
	if method == "POST" && len(parts) >= 7 && parts[4] == "issues" && parts[6] == "comments" {
		number, _ := strconv.Atoi(parts[5])
		body := input.(map[string]string)["body"]
		client.posts = append(client.posts, body)
		if client.permanentFailures > 0 {
			client.permanentFailures--
			return &githubapi.RejectionError{Status: 422, Message: "permanent create failure"}
		}
		if client.createFailures > 0 {
			client.createFailures--
			return &githubapi.RejectionError{Status: 429, Message: "rate limit exceeded"}
		}
		client.nextID++
		now := time.Date(2026, 1, 2, 0, 0, int(client.nextID%60), 0, time.UTC).Format(time.RFC3339Nano)
		comment := githubLifecycleComment{ID: client.nextID, Body: body, CreatedAt: now, UpdatedAt: now}
		client.comments[number] = append(client.comments[number], comment)
		if client.lostCreateResponses > 0 {
			client.lostCreateResponses--
			return &githubapi.Error{Code: githubapi.ErrorCodeMutationUncertain, Err: errors.New("connection lost after create")}
		}
		return decodeGitHubFixture(response, comment)
	}
	if method == "PATCH" && len(parts) >= 7 && parts[4] == "issues" && parts[5] == "comments" {
		id, _ := strconv.ParseInt(parts[6], 10, 64)
		body := input.(map[string]string)["body"]
		client.patches = append(client.patches, body)
		for number, comments := range client.comments {
			for index := range comments {
				if comments[index].ID == id {
					comments[index].Body = body
					comments[index].UpdatedAt = "2026-01-03T00:00:00Z"
					client.comments[number] = comments
					return nil
				}
			}
		}
		return errors.New("comment not found")
	}
	return fmt.Errorf("unexpected GitHub request: %s %s", method, path)
}

func (client *githubLifecycleClient) GraphQL(_ context.Context, query string, _ map[string]any, response any) error {
	if !strings.Contains(query, "reviewThreads") {
		return fmt.Errorf("unexpected GitHub GraphQL query: %s", query)
	}
	return decodeGitHubFixture(response, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviewThreads": map[string]any{
		"nodes": []map[string]any{}, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
	}}}}})
}

func (client *githubLifecycleClient) setBody(number int, id int64, body string) {
	for index := range client.comments[number] {
		if client.comments[number][index].ID == id {
			client.comments[number][index].Body = body
			client.comments[number][index].UpdatedAt = "2026-01-04T00:00:00Z"
			return
		}
	}
}

func (client *githubLifecycleClient) remove(number int, id int64) {
	comments := client.comments[number]
	for index := range comments {
		if comments[index].ID == id {
			client.comments[number] = append(comments[:index:index], comments[index+1:]...)
			return
		}
	}
}

func lifecycleIssueComment(id int64, body string, second int) githubLifecycleComment {
	timestamp := time.Date(2026, 1, 1, 0, 0, second, 0, time.UTC).Format(time.RFC3339Nano)
	return githubLifecycleComment{ID: id, Body: body, CreatedAt: timestamp, UpdatedAt: timestamp}
}

func TestGitHubOverviewCommentLifecycle(t *testing.T) {
	db := openCommentsDB(t)
	store, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	client := &githubLifecycleClient{nextID: 100, comments: map[int][]githubLifecycleComment{7: {
		lifecycleIssueComment(1, "Root\n\nMore context", 1),
		lifecycleIssueComment(2, "> Root\n>\n> More context\n\nFull reply", 2),
		lifecycleIssueComment(3, "> More context\n\nPartial reply", 3),
		lifecycleIssueComment(4, "> > Root\n> >\n> >\n> > More context\n>\n> Full reply\n\nNested reply", 4),
		lifecycleIssueComment(5, "Alpha shared phrase omega", 5),
		lifecycleIssueComment(6, "Beta shared phrase end", 6),
		lifecycleIssueComment(7, "> shared phrase\n\nAmbiguous reply", 7),
		lifecycleIssueComment(8, "> absent text\n\nMissing reply", 8),
		lifecycleIssueComment(9, "> Future root\n\nFuture-only reply", 9),
		lifecycleIssueComment(10, "Future root", 10),
		lifecycleIssueComment(11, "> Root\n>\n> More context", 11),
		lifecycleIssueComment(12, "> User-authored blockquote\n\nLegacy reply\n\n<!-- holark-comment:legacy-local parent:conversation:1 -->", 12),
		lifecycleIssueComment(13, "Marked root\n\n<!-- holark-comment:minimal-local -->", 13),
	}}}
	service := pullrequestcomments.NewService(
		store,
		providerTargets(),
		pullrequestcomments.WithProviderGateway(githubpullrequestcomments.New(client), nil),
	)
	makeDue := func(commentID string) {
		t.Helper()
		if _, err := db.Exec(`update pull_request_comment_publications set next_attempt_at = '1970-01-01T00:00:00Z' where comment_id = ?`, commentID); err != nil {
			t.Fatal(err)
		}
	}

	comments, err := service.Sync(t.Context(), "pr-one")
	if err != nil {
		t.Fatal(err)
	}
	byExternal := commentsByExternalID(comments)
	root := byExternal["conversation:1"]
	for externalID, wantBody := range map[string]string{
		"conversation:2": "Full reply",
		"conversation:3": "Partial reply",
		"conversation:4": "Nested reply",
	} {
		reply := byExternal[externalID]
		if reply.ParentCommentID != root.ID || reply.Body != wantBody {
			t.Fatalf("%s normalized reply = %#v", externalID, reply)
		}
	}
	for externalID, wantBody := range map[string]string{
		"conversation:7":  "> shared phrase\n\nAmbiguous reply",
		"conversation:8":  "> absent text\n\nMissing reply",
		"conversation:9":  "> Future root\n\nFuture-only reply",
		"conversation:11": "> Root\n>\n> More context",
	} {
		comment := byExternal[externalID]
		if comment.ParentCommentID != "" || comment.Body != wantBody {
			t.Fatalf("%s non-match = %#v", externalID, comment)
		}
	}
	legacy := byExternal["conversation:12"]
	if legacy.ParentCommentID != root.ID || legacy.Body != "> User-authored blockquote\n\nLegacy reply" {
		t.Fatalf("legacy marker normalization = %#v", legacy)
	}
	if marked := byExternal["conversation:13"]; marked.Body != "Marked root" || marked.ParentCommentID != "" {
		t.Fatalf("minimal marker normalization = %#v", marked)
	}

	client.setBody(7, 2, "Full reply without quote")
	comments, err = service.Sync(t.Context(), "pr-one")
	if err != nil {
		t.Fatal(err)
	}
	preserved := commentsByExternalID(comments)["conversation:2"]
	if preserved.ParentCommentID != root.ID || preserved.Body != "Full reply without quote" {
		t.Fatalf("relationship after quote removal = %#v", preserved)
	}
	client.setBody(7, 2, "> absent text\n\nAltered reply")
	comments, err = service.Sync(t.Context(), "pr-one")
	if err != nil {
		t.Fatal(err)
	}
	preserved = commentsByExternalID(comments)["conversation:2"]
	if preserved.ParentCommentID != root.ID || preserved.Body != "> absent text\n\nAltered reply" {
		t.Fatalf("relationship after quote alteration = %#v", preserved)
	}

	created, err := createAndPublish(t, service, pullrequestcomments.CreateComment{
		PullRequestID: "pr-one", ParentCommentID: root.ID, Body: "Created reply", Origin: pullrequestcomments.UserOrigin(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCreate := "> > User-authored blockquote\n>\n> Legacy reply\n\nCreated reply\n\n<!-- holark-comment:" + created.ID + " -->"
	if got := client.posts[len(client.posts)-1]; got != wantCreate {
		t.Fatalf("GitHub create body = %q, want %q", got, wantCreate)
	}
	comments, err = service.Sync(t.Context(), "pr-one")
	if err != nil {
		t.Fatal(err)
	}
	roundTripped := commentsByExternalID(comments)[created.ProviderIdentity.ExternalID]
	if roundTripped.ID != created.ID || roundTripped.ParentCommentID != root.ID || roundTripped.Body != "Created reply" {
		t.Fatalf("round-tripped create = %#v", roundTripped)
	}
	if _, err := service.UpdateBody(t.Context(), created.ID, "Edited reply"); err != nil {
		t.Fatal(err)
	}
	wantUpdate := "> > User-authored blockquote\n>\n> Legacy reply\n\nEdited reply"
	if got := client.patches[len(client.patches)-1]; got != wantUpdate || strings.Contains(got, "holark-comment:") {
		t.Fatalf("ordinary update body = %q", got)
	}

	client.listFailures = 1
	frozen, err := createAndPublish(t, service, pullrequestcomments.CreateComment{
		PullRequestID: "pr-one", ParentCommentID: root.ID, Body: "Frozen retry", Origin: pullrequestcomments.UserOrigin(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if frozen.PublicationState != pullrequestcomments.PublicationPending || frozen.ProviderIdentity.ExternalID != "" {
		t.Fatalf("list failure did not leave a retryable publication: %#v", frozen)
	}
	var payload string
	if err := db.QueryRow(`select payload from pull_request_comment_publications where comment_id = ?`, frozen.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"quote_external_id":"`+created.ProviderIdentity.ExternalID+`"`) || strings.Contains(payload, "ReplyContext") || strings.Contains(payload, "IdempotencyKey") {
		t.Fatalf("queued payload compatibility = %s", payload)
	}
	client.comments[7] = append(client.comments[7], lifecycleIssueComment(14, "> Root\n>\n> More context\n\nLater published sibling", 14))
	if _, err := service.Sync(t.Context(), "pr-one"); err != nil {
		t.Fatal(err)
	}
	makeDue(frozen.ID)
	if _, err := service.Sync(t.Context(), "pr-one"); err != nil {
		t.Fatal(err)
	}
	wantFrozen := "> > > User-authored blockquote\n> >\n> > Legacy reply\n>\n> Edited reply\n\nFrozen retry\n\n<!-- holark-comment:" + frozen.ID + " -->"
	if got := client.posts[len(client.posts)-1]; got != wantFrozen {
		t.Fatalf("retry changed its frozen predecessor: %q, want %q", got, wantFrozen)
	}
	frozen, err = service.Get(t.Context(), frozen.ID)
	if err != nil {
		t.Fatal(err)
	}

	client.createFailures = 1
	remoteFallback, err := createAndPublish(t, service, pullrequestcomments.CreateComment{
		PullRequestID: "pr-one", ParentCommentID: root.ID, Body: "Remote-root fallback", Origin: pullrequestcomments.UserOrigin(),
	})
	if err != nil {
		t.Fatal(err)
	}
	frozenID, _ := strconv.ParseInt(strings.TrimPrefix(frozen.ProviderIdentity.ExternalID, "conversation:"), 10, 64)
	client.remove(7, frozenID)
	makeDue(remoteFallback.ID)
	if _, err := service.Sync(t.Context(), "pr-one"); err != nil {
		t.Fatal(err)
	}
	wantRemoteFallback := "> Root\n>\n> More context\n\nRemote-root fallback\n\n<!-- holark-comment:" + remoteFallback.ID + " -->"
	if got := client.posts[len(client.posts)-1]; got != wantRemoteFallback {
		t.Fatalf("deleted predecessor remote-root fallback = %q, want %q", got, wantRemoteFallback)
	}
	remoteFallback, err = service.Get(t.Context(), remoteFallback.ID)
	if err != nil {
		t.Fatal(err)
	}

	client.createFailures = 1
	storedFallback, err := createAndPublish(t, service, pullrequestcomments.CreateComment{
		PullRequestID: "pr-one", ParentCommentID: root.ID, Body: "Stored-root fallback", Origin: pullrequestcomments.UserOrigin(),
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteFallbackID, _ := strconv.ParseInt(strings.TrimPrefix(remoteFallback.ProviderIdentity.ExternalID, "conversation:"), 10, 64)
	client.remove(7, remoteFallbackID)
	client.remove(7, 1)
	makeDue(storedFallback.ID)
	if _, err := service.Sync(t.Context(), "pr-one"); err != nil {
		t.Fatal(err)
	}
	wantStoredFallback := "> Root\n>\n> More context\n\nStored-root fallback\n\n<!-- holark-comment:" + storedFallback.ID + " -->"
	if got := client.posts[len(client.posts)-1]; got != wantStoredFallback {
		t.Fatalf("missing remote root stored fallback = %q, want %q", got, wantStoredFallback)
	}

	client.lostCreateResponses = 1
	remoteCount := len(client.comments[7])
	lost, err := createAndPublish(t, service, pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "Lost response", Origin: pullrequestcomments.UserOrigin()})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.comments[7]) != remoteCount+1 || lost.ProviderIdentity.ExternalID != "" {
		t.Fatalf("lost response setup = %#v, remote=%d", lost, len(client.comments[7]))
	}
	postsAfterLostResponse := len(client.posts)
	makeDue(lost.ID)
	if _, err := service.Sync(t.Context(), "pr-one"); err != nil {
		t.Fatal(err)
	}
	lost, err = service.Get(t.Context(), lost.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lost.ProviderIdentity.ExternalID == "" || len(client.posts) != postsAfterLostResponse || len(client.comments[7]) != remoteCount+1 {
		t.Fatalf("lost-create recovery duplicated or failed: %#v posts=%d remote=%d", lost, len(client.posts), len(client.comments[7]))
	}
	if got := client.patches[len(client.patches)-1]; !strings.Contains(got, "<!-- holark-comment:"+lost.ID+" -->") {
		t.Fatalf("recovery update dropped idempotency marker: %q", got)
	}
	if _, err := service.UpdateBody(t.Context(), lost.ID, "Ordinary edit"); err != nil {
		t.Fatal(err)
	}
	if got := client.patches[len(client.patches)-1]; got != "Ordinary edit" {
		t.Fatalf("ordinary top-level update retained recovery metadata: %q", got)
	}

	client.permanentFailures = 1
	failed, err := createAndPublish(t, service, pullrequestcomments.CreateComment{
		PullRequestID: "pr-one", ParentCommentID: root.ID, Body: "Failed predecessor", Origin: pullrequestcomments.UserOrigin(),
	})
	if err != nil {
		t.Fatal(err)
	}
	client.createFailures = 1
	pending, err := createAndPublish(t, service, pullrequestcomments.CreateComment{
		PullRequestID: "pr-one", ParentCommentID: root.ID, Body: "Pending predecessor", Origin: pullrequestcomments.UserOrigin(),
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := createAndPublish(t, service, pullrequestcomments.CreateComment{
		PullRequestID: "pr-one", ParentCommentID: root.ID, Body: "Selection target", Origin: pullrequestcomments.UserOrigin(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if failed.PublicationState != pullrequestcomments.PublicationFailed || pending.PublicationState != pullrequestcomments.PublicationPending || target.ProviderIdentity.ExternalID == "" {
		t.Fatalf("published predecessor selection states: failed=%#v pending=%#v target=%#v", failed, pending, target)
	}
	if got := client.posts[len(client.posts)-1]; strings.Contains(got, "Failed predecessor") || strings.Contains(got, "Pending predecessor") {
		t.Fatalf("pending or failed predecessor was selected: %q", got)
	}
}

func (client *githubLifecycleClient) RequestMutation(ctx context.Context, method, path string, body, response any) error {
	return client.Request(ctx, method, path, body, response)
}

func TestPublicationRecoversAfterIdentityPersistenceFails(t *testing.T) {
	db := openCommentsDB(t)
	store, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	client := &githubLifecycleClient{comments: map[int][]githubLifecycleComment{}}
	service := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(githubpullrequestcomments.New(client), nil))
	saved, err := service.Create(t.Context(), pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "Save identity later"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create trigger fail_identity before update of sync_external_id on pull_request_comments when new.sync_external_id is not null begin select raise(abort, 'disk failure'); end`); err != nil {
		t.Fatal(err)
	}
	publishComments(t, service)
	jobs, err := store.PendingPublications(t.Context(), "pr-one")
	if err != nil || len(jobs) != 1 || jobs[0].PermanentError != "" || len(client.posts) != 1 {
		t.Fatalf("persistence failure lost recovery: jobs=%+v posts=%d err=%v", jobs, len(client.posts), err)
	}
	if _, err := db.Exec(`drop trigger fail_identity`); err != nil {
		t.Fatal(err)
	}
	publishComments(t, service)
	published, err := service.Get(t.Context(), saved.ID)
	if err != nil || published.PublicationState != pullrequestcomments.PublicationPublished || len(client.posts) != 1 {
		t.Fatalf("identity recovery duplicated creation: %+v posts=%d err=%v", published, len(client.posts), err)
	}
}
