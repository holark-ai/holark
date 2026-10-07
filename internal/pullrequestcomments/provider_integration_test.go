//go:build integration

package pullrequestcomments_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	githubpullrequestcomments "github.com/holark-ai/holark/internal/codehost/github/pullrequestcomments"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	"github.com/holark-ai/holark/internal/pullrequestcomments/sqliteadapter"
)

type fakeGateway struct {
	mu             sync.Mutex
	remote         []pullrequestcomments.RemoteComment
	createFailures int
	creates        int
}

type lifecycleGateway struct {
	mu             sync.Mutex
	remote         []pullrequestcomments.RemoteComment
	createFailures int
	creates        []pullrequestcomments.RemoteMutation
	updates        []pullrequestcomments.RemoteMutation
	updateError    error
	resolves       []pullrequestcomments.RemoteMutation
	deletes        []pullrequestcomments.RemoteMutation
}

type blockingGateway struct {
	lifecycleGateway
	blocked chan struct{}
	release chan struct{}
	once    sync.Once
}

type workerReferences map[string]bool

func (references workerReferences) HasWorkerReference(_ context.Context, commentID string) (bool, error) {
	return references[commentID], nil
}

type inlineReplyGitHubClient struct{}

func (inlineReplyGitHubClient) Request(_ context.Context, method, path string, _ any, response any) error {
	if method != "GET" {
		return fmt.Errorf("unexpected GitHub request: %s %s", method, path)
	}
	var fixture any
	switch {
	case strings.Contains(path, "/issues/7/comments"):
		fixture = []map[string]any{}
	case strings.Contains(path, "/pulls/7/comments"):
		fixture = []map[string]any{{"id": 101, "side": "RIGHT"}, {"id": 102, "side": "RIGHT"}}
	default:
		return fmt.Errorf("unexpected GitHub request: %s %s", method, path)
	}
	return decodeGitHubFixture(response, fixture)
}

func (inlineReplyGitHubClient) GraphQL(_ context.Context, query string, _ map[string]any, response any) error {
	if !strings.Contains(query, "reviewThreads") {
		return fmt.Errorf("unexpected GitHub GraphQL query: %s", query)
	}
	return decodeGitHubFixture(response, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviewThreads": map[string]any{
		"nodes": []map[string]any{{
			"id":         "thread-one",
			"isResolved": false,
			"comments": map[string]any{
				"nodes": []map[string]any{
					{"databaseId": 101, "body": "Inline root", "path": "main.go", "line": 12, "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"},
					{"databaseId": 102, "body": "Inline reply", "path": "main.go", "line": 12, "replyTo": map[string]any{"databaseId": 101}, "createdAt": "2026-01-01T00:01:00Z", "updatedAt": "2026-01-01T00:01:00Z"},
				},
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
			},
		}},
		"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
	}}}}})
}

func decodeGitHubFixture(target, fixture any) error {
	data, err := json.Marshal(fixture)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func (gateway *blockingGateway) CreateComment(ctx context.Context, target pullrequestcomments.ProviderTarget, request pullrequestcomments.RemoteMutation) (pullrequestcomments.ProviderIdentity, error) {
	if target.PullRequestID == "pr-one" {
		gateway.once.Do(func() { close(gateway.blocked) })
		select {
		case <-gateway.release:
		case <-ctx.Done():
			return pullrequestcomments.ProviderIdentity{}, ctx.Err()
		}
	}
	return gateway.lifecycleGateway.CreateComment(ctx, target, request)
}

func (gateway *lifecycleGateway) ListComments(_ context.Context, _ pullrequestcomments.ProviderTarget) ([]pullrequestcomments.RemoteComment, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return append([]pullrequestcomments.RemoteComment(nil), gateway.remote...), nil
}

func (gateway *lifecycleGateway) CreateComment(_ context.Context, _ pullrequestcomments.ProviderTarget, request pullrequestcomments.RemoteMutation) (pullrequestcomments.ProviderIdentity, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.creates = append(gateway.creates, request)
	if gateway.createFailures > 0 {
		gateway.createFailures--
		return pullrequestcomments.ProviderIdentity{}, pullrequestcomments.RetryableProviderError(errors.New("temporary create failure"), 0)
	}
	number := len(gateway.creates)
	kind := "conversation"
	if request.Scope != pullrequestcomments.ScopePullRequest {
		kind = "inline"
	}
	identity := pullrequestcomments.ProviderIdentity{Provider: "github", ExternalID: kind + ":" + fmt.Sprint(number), Kind: kind}
	remoteIdentity := identity
	remoteParentExternalID := ""
	if kind == "inline" {
		remoteIdentity.ThreadID = "thread-" + fmt.Sprint(number)
		remoteParentExternalID = request.ParentExternalID
	}
	gateway.remote = append(gateway.remote, pullrequestcomments.RemoteComment{
		ProviderIdentity: remoteIdentity, ParentExternalID: remoteParentExternalID, Body: request.Body,
		Scope: request.Scope, Path: request.Path, Side: request.Side, Line: request.Line,
		OriginalHeadCommit: request.OriginalHeadCommit, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	return identity, nil
}

func (gateway *lifecycleGateway) UpdateComment(_ context.Context, _ pullrequestcomments.ProviderTarget, mutation pullrequestcomments.RemoteMutation) error {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.updates = append(gateway.updates, mutation)
	return gateway.updateError
}
func (gateway *lifecycleGateway) DeleteComment(_ context.Context, _ pullrequestcomments.ProviderTarget, mutation pullrequestcomments.RemoteMutation) error {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.deletes = append(gateway.deletes, mutation)
	for i, remote := range gateway.remote {
		if remote.ProviderIdentity.ExternalID == mutation.ProviderIdentity.ExternalID {
			gateway.remote = append(gateway.remote[:i], gateway.remote[i+1:]...)
			break
		}
	}
	return nil
}
func (gateway *lifecycleGateway) ResolveThread(_ context.Context, _ pullrequestcomments.ProviderTarget, mutation pullrequestcomments.RemoteMutation) error {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.resolves = append(gateway.resolves, mutation)
	return nil
}
func (gateway *lifecycleGateway) ReopenThread(context.Context, pullrequestcomments.ProviderTarget, pullrequestcomments.RemoteMutation) error {
	return nil
}

func TestProviderMutationOnAnotherPullRequestDoesNotWaitForBlockedPublication(t *testing.T) {
	commentStore, err := sqliteadapter.New(t.Context(), openCommentsDB(t))
	if err != nil {
		t.Fatal(err)
	}
	gateway := &blockingGateway{blocked: make(chan struct{}), release: make(chan struct{})}
	service := pullrequestcomments.NewService(commentStore, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil))

	startCommentPublisher(t, service)
	first := make(chan error, 1)
	go func() {
		_, err := service.Create(t.Context(), pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "Blocked", Origin: pullrequestcomments.UserOrigin()})
		first <- err
	}()
	select {
	case <-gateway.blocked:
	case <-time.After(time.Second):
		t.Fatal("provider call did not block")
	}
	second := make(chan error, 1)
	go func() {
		_, err := service.Create(t.Context(), pullrequestcomments.CreateComment{PullRequestID: "pr-two", Body: "Independent", Origin: pullrequestcomments.UserOrigin()})
		second <- err
	}()
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated pull request waited for the blocked publication")
	}
	close(gateway.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func (gateway *fakeGateway) ListComments(_ context.Context, _ pullrequestcomments.ProviderTarget) ([]pullrequestcomments.RemoteComment, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return append([]pullrequestcomments.RemoteComment(nil), gateway.remote...), nil
}

func (gateway *fakeGateway) CreateComment(_ context.Context, _ pullrequestcomments.ProviderTarget, request pullrequestcomments.RemoteMutation) (pullrequestcomments.ProviderIdentity, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.creates++
	if gateway.createFailures > 0 {
		gateway.createFailures--
		return pullrequestcomments.ProviderIdentity{}, pullrequestcomments.RetryableProviderError(errors.New("temporary outage"), 0)
	}
	identity := pullrequestcomments.ProviderIdentity{Provider: "github", ExternalID: "remote-created", Kind: "conversation"}
	gateway.remote = append(gateway.remote, pullrequestcomments.RemoteComment{ProviderIdentity: identity, Body: request.Body, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()})
	return identity, nil
}

func (gateway *fakeGateway) UpdateComment(context.Context, pullrequestcomments.ProviderTarget, pullrequestcomments.RemoteMutation) error {
	return nil
}
func (gateway *fakeGateway) DeleteComment(context.Context, pullrequestcomments.ProviderTarget, pullrequestcomments.RemoteMutation) error {
	return nil
}
func (gateway *fakeGateway) ResolveThread(context.Context, pullrequestcomments.ProviderTarget, pullrequestcomments.RemoteMutation) error {
	return nil
}
func (gateway *fakeGateway) ReopenThread(context.Context, pullrequestcomments.ProviderTarget, pullrequestcomments.RemoteMutation) error {
	return nil
}

func TestProviderSyncRetriesDurablyBeforeInboundReconciliation(t *testing.T) {
	db := openCommentsDB(t)
	store, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	targets := targetReader{targets: map[string]pullrequestcomments.PullRequestTarget{
		"pr-one": {ID: "pr-one", RepositoryID: "project-one", Status: pullrequestcomments.PullRequestOpen, HeadCommit: "head", SyncProvider: "github", RepositoryURL: "https://github.com/owner/repo", ProviderPullRequest: 7},
	}}
	gateway := &fakeGateway{createFailures: 1}
	service := pullrequestcomments.NewService(store, targets, pullrequestcomments.WithProviderGateway(gateway, nil))
	created, err := createAndPublish(t, service, pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "Local intent", Origin: pullrequestcomments.UserOrigin()})
	if err != nil {
		t.Fatal(err)
	}
	if created.ProviderIdentity.ExternalID != "" || gateway.creates != 1 {
		t.Fatalf("failed immediate publication changed local result or was not attempted: %#v, creates=%d", created, gateway.creates)
	}

	restartedStore, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update pull_request_comment_publications set next_attempt_at = '1970-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	restarted := pullrequestcomments.NewService(restartedStore, targets, pullrequestcomments.WithProviderGateway(gateway, nil))
	if _, err := restarted.Sync(t.Context(), "pr-one"); err != nil {
		t.Fatal(err)
	}
	stored, err := restarted.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProviderIdentity.ExternalID != "remote-created" || gateway.creates != 2 {
		t.Fatalf("durable retry did not publish once: %#v, creates=%d", stored, gateway.creates)
	}
}

func TestProviderSyncReconcilesGitHubInlineResolution(t *testing.T) {
	db := openCommentsDB(t)
	store, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	targets := targetReader{targets: map[string]pullrequestcomments.PullRequestTarget{
		"pr-one": {ID: "pr-one", Status: pullrequestcomments.PullRequestOpen, HeadCommit: "head", SyncProvider: "github", RepositoryURL: "https://github.com/owner/repo", ProviderPullRequest: 7},
	}}
	now := time.Now().UTC()
	gateway := &fakeGateway{remote: []pullrequestcomments.RemoteComment{{
		ProviderIdentity: pullrequestcomments.ProviderIdentity{Provider: "github", ExternalID: "remote-one", Kind: "inline", ThreadID: "thread-one"},
		Body:             "Remote review", Scope: pullrequestcomments.ScopeLine, Path: "main.go", Side: "RIGHT", Line: intPointer(12),
		OriginalHeadCommit: "head", Resolved: false, CreatedAt: now, UpdatedAt: now,
	}}}
	service := pullrequestcomments.NewService(store, targets, pullrequestcomments.WithProviderGateway(gateway, nil))
	comments, err := service.Sync(t.Context(), "pr-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Status != pullrequestcomments.Unresolved || comments[0].ResolvedAt != nil || comments[0].AuthorType != pullrequestcomments.AuthorUser {
		t.Fatalf("unexpected import: %#v", comments)
	}
	commentID := comments[0].ID
	resolvedAt := now.Add(time.Minute)
	gateway.remote[0].Resolved, gateway.remote[0].UpdatedAt = true, resolvedAt
	comments, err = service.Sync(t.Context(), "pr-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].ID != commentID || comments[0].Status != pullrequestcomments.Resolved || comments[0].ResolvedAt == nil || !comments[0].ResolvedAt.Equal(resolvedAt) {
		t.Fatalf("remote resolution was not reconciled: %#v", comments)
	}

	gateway.remote[0].Resolved = false
	gateway.remote[0].UpdatedAt = now.Add(2 * time.Minute)
	comments, err = service.Sync(t.Context(), "pr-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].ID != commentID || comments[0].Status != pullrequestcomments.Unresolved || comments[0].ResolvedAt != nil {
		t.Fatalf("remote reopening was not reconciled: %#v", comments)
	}
}

func TestProviderSyncRemovesDeletedCommentsUnlessWorkReferencesThem(t *testing.T) {
	for _, test := range []struct {
		name   string
		retain bool
	}{
		{name: "unreferenced"},
		{name: "referenced by work", retain: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := sqliteadapter.New(t.Context(), openCommentsDB(t))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			gateway := &lifecycleGateway{remote: []pullrequestcomments.RemoteComment{{
				ProviderIdentity: pullrequestcomments.ProviderIdentity{Provider: "github", ExternalID: "inline:7", Kind: "inline"},
				Body:             "Remote comment", Scope: pullrequestcomments.ScopeLine, Path: "main.go", Side: "RIGHT", Line: intPointer(7),
				CreatedAt: now, UpdatedAt: now,
			}}}
			references := workerReferences{}
			service := pullrequestcomments.NewService(store, providerTargets(),
				pullrequestcomments.WithProviderGateway(gateway, nil),
				pullrequestcomments.WithWorkerReferenceReader(references),
			)
			comments, err := service.Sync(t.Context(), "pr-one")
			if err != nil || len(comments) != 1 {
				t.Fatalf("initial sync comments=%+v err=%v", comments, err)
			}
			if test.retain {
				references[comments[0].ID] = true
			}
			gateway.remote = nil

			comments, err = service.Sync(t.Context(), "pr-one")
			if err != nil {
				t.Fatal(err)
			}
			if got := len(comments); got != boolCount(test.retain) {
				t.Fatalf("comments after remote deletion = %d, want %d", got, boolCount(test.retain))
			}
		})
	}
}

func TestProviderSyncImportsGitHubInlineReplyHierarchy(t *testing.T) {
	store, err := sqliteadapter.New(t.Context(), openCommentsDB(t))
	if err != nil {
		t.Fatal(err)
	}
	service := pullrequestcomments.NewService(
		store,
		providerTargets(),
		pullrequestcomments.WithProviderGateway(githubpullrequestcomments.New(inlineReplyGitHubClient{}), nil),
	)

	comments, err := service.Sync(t.Context(), "pr-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 {
		t.Fatalf("imported comments = %#v, want inline root and reply", comments)
	}
	var root, reply pullrequestcomments.Comment
	for _, comment := range comments {
		switch comment.Body {
		case "Inline root":
			root = comment
		case "Inline reply":
			reply = comment
		}
	}
	if root.ID == "" || reply.ID == "" {
		t.Fatalf("imported comments = %#v, want identifiable inline root and reply", comments)
	}
	if root.ParentCommentID != "" {
		t.Fatalf("root parent = %q, want no parent", root.ParentCommentID)
	}
	if reply.ParentCommentID != root.ID {
		t.Fatalf("reply parent = %q, want local root ID %q", reply.ParentCommentID, root.ID)
	}
}

func TestReplyPublicationWaitsForPendingInlineParent(t *testing.T) {
	db := openCommentsDB(t)
	store, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &lifecycleGateway{createFailures: 1}
	service := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil))
	parent, err := createAndPublish(t, service, pullrequestcomments.CreateComment{
		PullRequestID: "pr-one", Body: "Inline root", Scope: pullrequestcomments.ScopeLine,
		Path: "main.go", Side: "RIGHT", Line: intPointer(10), Origin: pullrequestcomments.UserOrigin(),
	})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := createAndPublish(t, service, pullrequestcomments.CreateComment{PullRequestID: "pr-one", ParentCommentID: parent.ID, Body: "Fixed", Origin: pullrequestcomments.UserOrigin()})
	if err != nil {
		t.Fatal(err)
	}
	if len(gateway.creates) != 1 || reply.ProviderIdentity.ExternalID != "" {
		t.Fatalf("pending reply was published before its parent: creates=%#v reply=%#v", gateway.creates, reply)
	}
	if _, err := db.Exec(`update pull_request_comment_publications set next_attempt_at = '1970-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Sync(t.Context(), "pr-one"); err != nil {
		t.Fatal(err)
	}
	if len(gateway.creates) != 3 || gateway.creates[2].ParentExternalID != "inline:2" {
		t.Fatalf("reply publication did not use the published parent identity: %#v", gateway.creates)
	}
}

func TestDeletingPendingCreateCancelsPublication(t *testing.T) {
	db := openCommentsDB(t)
	store, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &lifecycleGateway{createFailures: 10}
	service := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil))
	comment, err := createAndPublish(t, service, pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "Do not publish", Origin: pullrequestcomments.UserOrigin()})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(t.Context(), comment.ID); err != nil {
		t.Fatal(err)
	}
	var publications int
	if err := db.QueryRow(`select count(*) from pull_request_comment_publications where comment_id = ?`, comment.ID).Scan(&publications); err != nil {
		t.Fatal(err)
	}
	if publications != 0 {
		t.Fatalf("pending create publications after delete = %d", publications)
	}
	if _, err := service.Sync(t.Context(), "pr-one"); err != nil {
		t.Fatal(err)
	}
	if len(gateway.creates) != 1 {
		t.Fatalf("deleted comment was recreated remotely: creates=%d", len(gateway.creates))
	}
}

func TestResolveReloadsAndEnrichesNewInlineIdentity(t *testing.T) {
	db := openCommentsDB(t)
	store, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &lifecycleGateway{}
	service := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil))
	comment, err := createAndPublish(t, service, pullrequestcomments.CreateComment{
		PullRequestID: "pr-one", Body: "Inline", Scope: pullrequestcomments.ScopeLine,
		Path: "main.go", Side: "RIGHT", Line: intPointer(8), Origin: pullrequestcomments.UserOrigin(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if comment.ProviderIdentity.ThreadID != "" {
		t.Fatalf("new inline comment unexpectedly had thread identity: %#v", comment.ProviderIdentity)
	}
	if _, err := service.Resolve(t.Context(), comment.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := service.Get(t.Context(), comment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gateway.resolves) != 1 || gateway.resolves[0].ProviderIdentity.ThreadID == "" || stored.ProviderIdentity.ThreadID == "" {
		t.Fatalf("resolve did not enrich the inline identity: calls=%#v stored=%#v", gateway.resolves, stored)
	}
}

func TestResolveWaitsForPendingInlineCreate(t *testing.T) {
	db := openCommentsDB(t)
	store, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &lifecycleGateway{createFailures: 1}
	service := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil))
	comment, err := createAndPublish(t, service, pullrequestcomments.CreateComment{
		PullRequestID: "pr-one", Body: "Pending inline", Scope: pullrequestcomments.ScopeLine,
		Path: "main.go", Side: "RIGHT", Line: intPointer(8), Origin: pullrequestcomments.UserOrigin(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(t.Context(), comment.ID); err != nil {
		t.Fatal(err)
	}
	if len(gateway.resolves) != 0 {
		t.Fatalf("resolve ran before inline creation: %#v", gateway.resolves)
	}
	if _, err := db.Exec(`update pull_request_comment_publications set next_attempt_at = '1970-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Sync(t.Context(), "pr-one"); err != nil {
		t.Fatal(err)
	}
	if len(gateway.resolves) != 1 || gateway.resolves[0].ProviderIdentity.ThreadID == "" {
		t.Fatalf("deferred resolve did not run after inline creation: %#v", gateway.resolves)
	}
}

func TestSyncRetainsRemoteAbsenceWithPublicationIntent(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "pending", err: pullrequestcomments.RetryableProviderError(errors.New("temporary"), 0)},
		{name: "permanently failed", err: pullrequestcomments.PermanentProviderError(errors.New("rejected"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openCommentsDB(t)
			store, err := sqliteadapter.New(t.Context(), db)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			gateway := &lifecycleGateway{remote: []pullrequestcomments.RemoteComment{{
				ProviderIdentity: pullrequestcomments.ProviderIdentity{Provider: "github", ExternalID: "conversation:7", Kind: "conversation"},
				Body:             "Remote", Scope: pullrequestcomments.ScopePullRequest, CreatedAt: now, UpdatedAt: now,
			}}}
			service := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil))
			comments, err := service.Sync(t.Context(), "pr-one")
			if err != nil {
				t.Fatal(err)
			}
			gateway.remote = nil
			gateway.updateError = test.err
			if _, err := service.UpdateBody(t.Context(), comments[0].ID, "Local intent"); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Sync(t.Context(), "pr-one"); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Get(t.Context(), comments[0].ID); err != nil {
				t.Fatalf("comment with publication intent was removed: %v", err)
			}
		})
	}
}

func providerTargets() targetReader {
	return targetReader{targets: map[string]pullrequestcomments.PullRequestTarget{
		"pr-one": {ID: "pr-one", Status: pullrequestcomments.PullRequestOpen, HeadCommit: "head", SyncProvider: "github", RepositoryURL: "https://github.com/owner/repo", ProviderPullRequest: 7},
		"pr-two": {ID: "pr-two", Status: pullrequestcomments.PullRequestOpen, HeadCommit: "head", SyncProvider: "github", RepositoryURL: "https://github.com/owner/repo", ProviderPullRequest: 8},
	}}
}

func commentsByExternalID(comments []pullrequestcomments.Comment) map[string]pullrequestcomments.Comment {
	result := make(map[string]pullrequestcomments.Comment, len(comments))
	for _, comment := range comments {
		result[comment.ProviderIdentity.ExternalID] = comment
	}
	return result
}

func intPointer(value int) *int { return &value }

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (client inlineReplyGitHubClient) RequestMutation(ctx context.Context, method, path string, body, response any) error {
	return client.Request(ctx, method, path, body, response)
}

// Runs the production publisher through one admitted pass, without advancing sync state.
func publishComments(t *testing.T, service *pullrequestcomments.Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	service.RunPublisher(ctx, pullrequestcomments.RefreshSchedulerFunc(func(ctx context.Context, _, _ string, work func(context.Context) error) error {
		err := work(ctx)
		cancel()
		return err
	}))
}

func createAndPublish(t *testing.T, service *pullrequestcomments.Service, request pullrequestcomments.CreateComment) (pullrequestcomments.Comment, error) {
	t.Helper()
	comment, err := service.Create(t.Context(), request)
	if err != nil {
		return comment, err
	}
	publishComments(t, service)
	return service.Get(t.Context(), comment.ID)
}

func startCommentPublisher(t *testing.T, service *pullrequestcomments.Service) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); service.RunPublisher(ctx, nil) }()
	stop := func() { cancel(); <-done }
	t.Cleanup(stop)
	return stop
}
