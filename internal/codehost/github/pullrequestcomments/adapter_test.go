package pullrequestcomments

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/pullrequestcomments"
)

type recordingClient struct {
	method  string
	path    string
	body    any
	request func(string, any) error
	graphql func(string, map[string]any, any) error
}

func (client *recordingClient) Request(_ context.Context, method, path string, body, response any) error {
	client.method, client.path, client.body = method, path, body
	if client.request != nil {
		return client.request(path, response)
	}
	return nil
}

func (client *recordingClient) GraphQL(_ context.Context, query string, variables map[string]any, response any) error {
	if client.graphql != nil {
		return client.graphql(query, variables, response)
	}
	return nil
}

func TestCreateFileCommentUsesGitHubFileSubjectAnchor(t *testing.T) {
	client := &recordingClient{}
	gateway := New(client)

	_, err := gateway.CreateComment(t.Context(), pullrequestcomments.ProviderTarget{
		RepositoryURL:     "https://github.com/holark-ai/holark",
		PullRequestNumber: 42,
	}, pullrequestcomments.RemoteMutation{
		Body: "Consider renaming this file", Scope: pullrequestcomments.ScopeFile, Path: "internal/example.go", OriginalHeadCommit: "abc123",
	})
	if err != nil {
		t.Fatal(err)
	}
	request, ok := client.body.(map[string]any)
	if !ok {
		t.Fatalf("request body = %#v", client.body)
	}
	if request["subject_type"] != "file" || request["path"] != "internal/example.go" || request["commit_id"] != "abc123" {
		t.Fatalf("file comment request = %#v", request)
	}
	if _, ok := request["line"]; ok {
		t.Fatalf("file comment unexpectedly contains a line: %#v", request)
	}
	if _, ok := request["side"]; ok {
		t.Fatalf("file comment unexpectedly contains a side: %#v", request)
	}
}

func TestListCommentsPaginatesConversationComments(t *testing.T) {
	requests := make([]string, 0, 2)
	client := &recordingClient{request: func(path string, response any) error {
		if strings.Contains(path, "/pulls/7/comments") {
			return decodeFixture(response, []map[string]any{})
		}
		requests = append(requests, path)
		page := 1
		if strings.Contains(path, "page=2") {
			page = 2
		}
		count := 100
		if page == 2 {
			count = 1
		}
		comments := make([]map[string]any, count)
		for index := range comments {
			comments[index] = map[string]any{"id": (page-1)*100 + index + 1, "body": fmt.Sprintf("comment-%d", (page-1)*100+index+1)}
		}
		return decodeFixture(response, comments)
	}}

	comments, err := New(client).ListComments(t.Context(), pullrequestcomments.ProviderTarget{RepositoryURL: "https://github.com/owner/repo", PullRequestNumber: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 101 || len(requests) != 2 || !strings.Contains(requests[0], "page=1") || !strings.Contains(requests[1], "page=2") {
		t.Fatalf("conversation pagination returned %d comments over %#v", len(comments), requests)
	}
}

func TestListCommentsPaginatesReviewThreadsAndNestedComments(t *testing.T) {
	threadPages, commentPages, sidePages := 0, 0, 0
	client := &recordingClient{request: func(path string, response any) error {
		if !strings.Contains(path, "/pulls/7/comments") {
			return decodeFixture(response, []map[string]any{})
		}
		sidePages++
		if sidePages == 1 {
			comments := make([]map[string]any, 100)
			for index := range comments {
				comments[index] = map[string]any{"id": index + 1, "side": "RIGHT"}
			}
			comments[0]["side"] = "LEFT"
			return decodeFixture(response, comments)
		}
		return decodeFixture(response, []map[string]any{{"id": 101, "side": "RIGHT"}})
	}, graphql: func(query string, variables map[string]any, response any) error {
		if strings.Contains(query, "diffSide") {
			return fmt.Errorf("unsupported GraphQL field diffSide")
		}
		if !strings.Contains(query, "... on Node{id}") {
			return fmt.Errorf("review comment query does not request the actor node ID")
		}
		if strings.Contains(query, "reviewThreads") {
			threadPages++
			start, count, hasNext, cursor := 0, 100, true, "threads-100"
			if variables["threadCursor"] != nil {
				start, count, hasNext, cursor = 100, 1, false, ""
			}
			nodes := make([]map[string]any, count)
			for index := range nodes {
				threadNumber := start + index + 1
				comments := []map[string]any{{"databaseId": threadNumber, "body": fmt.Sprintf("root-%d", threadNumber), "line": threadNumber}}
				commentPageInfo := map[string]any{"hasNextPage": false, "endCursor": nil}
				if threadNumber == 1 {
					comments = make([]map[string]any, 100)
					for commentIndex := range comments {
						comments[commentIndex] = map[string]any{"databaseId": commentIndex + 1, "body": fmt.Sprintf("nested-%d", commentIndex+1), "line": commentIndex + 1}
					}
					comments[0]["author"] = map[string]any{"id": "BOT_node_id", "login": "review-bot", "avatarUrl": "https://example.com/bot.png", "url": "https://github.com/apps/review-bot"}
					commentPageInfo = map[string]any{"hasNextPage": true, "endCursor": "comments-100"}
				}
				nodes[index] = map[string]any{"id": fmt.Sprintf("thread-%d", threadNumber), "comments": map[string]any{"nodes": comments, "pageInfo": commentPageInfo}}
			}
			return decodeFixture(response, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviewThreads": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": hasNext, "endCursor": cursor}}}}}})
		}
		commentPages++
		return decodeFixture(response, map[string]any{"data": map[string]any{"node": map[string]any{"comments": map[string]any{
			"nodes":    []map[string]any{{"databaseId": 101, "body": "nested-101", "line": 101}},
			"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
		}}}})
	}}

	comments, err := New(client).ListComments(t.Context(), pullrequestcomments.ProviderTarget{RepositoryURL: "https://github.com/owner/repo", PullRequestNumber: 7})
	if err != nil {
		t.Fatal(err)
	}
	// 101 comments in the first thread and one in each of the other 100 threads.
	if len(comments) != 201 || threadPages != 2 || commentPages != 1 || sidePages != 2 {
		t.Fatalf("inline pagination returned %d comments (thread pages=%d comment pages=%d side pages=%d)", len(comments), threadPages, commentPages, sidePages)
	}
	if comments[0].Side != "LEFT" {
		t.Fatalf("first inline comment side = %q, want LEFT", comments[0].Side)
	}
	if comments[100].Side != "RIGHT" {
		t.Fatalf("nested inline comment side = %q, want RIGHT", comments[100].Side)
	}
	if comments[0].Author.ExternalID != "BOT_node_id" || comments[0].Author.Login != "review-bot" {
		t.Fatalf("bot inline comment author = %#v", comments[0].Author)
	}
}

func decodeFixture(target any, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func (client *recordingClient) RequestMutation(ctx context.Context, method, path string, body, response any) error {
	return client.Request(ctx, method, path, body, response)
}
