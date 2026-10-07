package pullrequestcomments

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
)

type Client interface {
	Request(context.Context, string, string, any, any) error
	RequestMutation(context.Context, string, string, any, any) error
	GraphQL(context.Context, string, map[string]any, any) error
}

type Gateway struct{ client Client }

func New(client Client) *Gateway { return &Gateway{client: client} }

type issueComment struct {
	ID        int64          `json:"id"`
	NodeID    string         `json:"node_id"`
	Body      string         `json:"body"`
	User      githubapi.User `json:"user"`
	CreatedAt string         `json:"created_at"`
	UpdatedAt string         `json:"updated_at"`
}

func (gateway *Gateway) ListComments(ctx context.Context, target pullrequestcomments.ProviderTarget) ([]pullrequestcomments.RemoteComment, error) {
	repository, err := githubapi.ParseRepositoryURL(target.RepositoryURL)
	if err != nil {
		return nil, pullrequestcomments.PermanentProviderError(err)
	}
	conversation := make([]issueComment, 0)
	for page := 1; ; page++ {
		var pageComments []issueComment
		endpoint := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100&page=%d", repository.Owner, repository.Name, target.PullRequestNumber, page)
		if err := gateway.client.Request(ctx, "GET", endpoint, nil, &pageComments); err != nil {
			return nil, refreshProviderError(err)
		}
		conversation = append(conversation, pageComments...)
		if len(pageComments) < 100 {
			break
		}
	}
	result := normalizeIssueComments(conversation)
	inline, err := gateway.listInline(ctx, repository, target.PullRequestNumber)
	if err != nil {
		return nil, err
	}
	return append(result, inline...), nil
}

func (gateway *Gateway) listInline(ctx context.Context, repository githubapi.Repository, number int) ([]pullrequestcomments.RemoteComment, error) {
	sides, err := gateway.listInlineCommentSides(ctx, repository, number)
	if err != nil {
		return nil, err
	}
	return gateway.listInlinePage(ctx, repository, number, nil, sides)
}

type reviewCommentSide struct {
	ID   int64  `json:"id"`
	Side string `json:"side"`
}

func (gateway *Gateway) listInlineCommentSides(ctx context.Context, repository githubapi.Repository, number int) (map[int64]string, error) {
	sides := make(map[int64]string)
	for page := 1; ; page++ {
		var comments []reviewCommentSide
		endpoint := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=100&page=%d", repository.Owner, repository.Name, number, page)
		if err := gateway.client.Request(ctx, "GET", endpoint, nil, &comments); err != nil {
			return nil, refreshProviderError(err)
		}
		for _, comment := range comments {
			sides[comment.ID] = comment.Side
		}
		if len(comments) < 100 {
			return sides, nil
		}
	}
}

func (gateway *Gateway) listInlinePage(ctx context.Context, repository githubapi.Repository, number int, threadCursor any, sides map[int64]string) ([]pullrequestcomments.RemoteComment, error) {
	var response struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							ID         string `json:"id"`
							IsResolved bool   `json:"isResolved"`
							Comments   struct {
								Nodes    []inlineComment `json:"nodes"`
								PageInfo struct {
									HasNextPage bool   `json:"hasNextPage"`
									EndCursor   string `json:"endCursor"`
								} `json:"pageInfo"`
							} `json:"comments"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	query := `query($owner:String!,$name:String!,$number:Int!,$threadCursor:String){repository(owner:$owner,name:$name){pullRequest(number:$number){reviewThreads(first:100,after:$threadCursor){nodes{id isResolved comments(first:100){nodes{databaseId body path line originalLine diffHunk createdAt updatedAt originalCommit{oid} replyTo{databaseId} pullRequestReview{databaseId} author{login avatarUrl url ... on Node{id} ... on User{name}}} pageInfo{hasNextPage endCursor}}} pageInfo{hasNextPage endCursor}}}}}`
	variables := map[string]any{"owner": repository.Owner, "name": repository.Name, "number": number, "threadCursor": threadCursor}
	if err := gateway.client.GraphQL(ctx, query, variables, &response); err != nil {
		return nil, refreshProviderError(err)
	}
	result := make([]pullrequestcomments.RemoteComment, 0)
	for _, thread := range response.Data.Repository.PullRequest.ReviewThreads.Nodes {
		for _, comment := range thread.Comments.Nodes {
			result = append(result, remoteInlineComment(thread.ID, thread.IsResolved, comment, sides[comment.DatabaseID]))
		}
		if thread.Comments.PageInfo.HasNextPage {
			more, err := gateway.listInlineComments(ctx, thread.ID, thread.IsResolved, thread.Comments.PageInfo.EndCursor, sides)
			if err != nil {
				return nil, err
			}
			result = append(result, more...)
		}
	}
	if response.Data.Repository.PullRequest.ReviewThreads.PageInfo.HasNextPage {
		more, err := gateway.listInlinePage(ctx, repository, number, response.Data.Repository.PullRequest.ReviewThreads.PageInfo.EndCursor, sides)
		if err != nil {
			return nil, err
		}
		result = append(result, more...)
	}
	return result, nil
}

type inlineComment struct {
	DatabaseID     int64  `json:"databaseId"`
	Body           string `json:"body"`
	Path           string `json:"path"`
	Line           *int   `json:"line"`
	OriginalLine   *int   `json:"originalLine"`
	DiffHunk       string `json:"diffHunk"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
	OriginalCommit *struct {
		OID string `json:"oid"`
	} `json:"originalCommit"`
	ReplyTo *struct {
		DatabaseID int64 `json:"databaseId"`
	} `json:"replyTo"`
	PullRequestReview *struct {
		DatabaseID int64 `json:"databaseId"`
	} `json:"pullRequestReview"`
	Author *struct {
		ID        string `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatarUrl"`
		URL       string `json:"url"`
	} `json:"author"`
}

func (gateway *Gateway) listInlineComments(ctx context.Context, threadID string, resolved bool, cursor any, sides map[int64]string) ([]pullrequestcomments.RemoteComment, error) {
	result := make([]pullrequestcomments.RemoteComment, 0)
	query := `query($threadID:ID!,$commentsCursor:String){node(id:$threadID){... on PullRequestReviewThread{comments(first:100,after:$commentsCursor){nodes{databaseId body path line originalLine diffHunk createdAt updatedAt originalCommit{oid} replyTo{databaseId} pullRequestReview{databaseId} author{login avatarUrl url ... on Node{id} ... on User{name}}} pageInfo{hasNextPage endCursor}}}}}`
	for {
		var response struct {
			Data struct {
				Node struct {
					Comments struct {
						Nodes    []inlineComment `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"comments"`
				} `json:"node"`
			} `json:"data"`
		}
		if err := gateway.client.GraphQL(ctx, query, map[string]any{"threadID": threadID, "commentsCursor": cursor}, &response); err != nil {
			return nil, refreshProviderError(err)
		}
		for _, comment := range response.Data.Node.Comments.Nodes {
			result = append(result, remoteInlineComment(threadID, resolved, comment, sides[comment.DatabaseID]))
		}
		if !response.Data.Node.Comments.PageInfo.HasNextPage {
			return result, nil
		}
		cursor = response.Data.Node.Comments.PageInfo.EndCursor
	}
}

func remoteInlineComment(threadID string, resolved bool, comment inlineComment, side string) pullrequestcomments.RemoteComment {
	created, _ := parseGitHubTime(comment.CreatedAt)
	updated, _ := parseGitHubTime(comment.UpdatedAt)
	line := comment.Line
	if line == nil {
		line = comment.OriginalLine
	}
	scope := pullrequestcomments.ScopeLine
	if line == nil {
		scope = pullrequestcomments.ScopeFile
		side = ""
	} else if side == "" {
		side = "RIGHT"
	}
	publishedBody, idempotencyKey, markerParent := parsePublicationMarker(comment.Body)
	remote := pullrequestcomments.RemoteComment{
		ProviderIdentity: pullrequestcomments.ProviderIdentity{Provider: "github", ExternalID: "inline:" + strconv.FormatInt(comment.DatabaseID, 10), Kind: "inline", ThreadID: threadID},
		Body:             publishedBody, PublishedBody: publishedBody, IdempotencyKey: idempotencyKey,
		Scope: scope, Path: comment.Path, Side: side, Line: line, DiffHunk: comment.DiffHunk, Resolved: resolved, CreatedAt: created, UpdatedAt: updated,
	}
	if comment.OriginalCommit != nil {
		remote.OriginalHeadCommit = comment.OriginalCommit.OID
	}
	if comment.ReplyTo != nil {
		remote.ParentExternalID = "inline:" + strconv.FormatInt(comment.ReplyTo.DatabaseID, 10)
	} else {
		remote.ParentExternalID = markerParent
	}
	if comment.PullRequestReview != nil {
		remote.ProviderIdentity.ReviewID = strconv.FormatInt(comment.PullRequestReview.DatabaseID, 10)
	}
	if comment.Author != nil {
		remote.Author = pullrequestcomments.ProviderAuthor{Provider: "github", ExternalID: comment.Author.ID, Login: comment.Author.Login, Name: comment.Author.Name, AvatarURL: comment.Author.AvatarURL, ProfileURL: comment.Author.URL}
	}
	return remote
}

func (gateway *Gateway) CreateComment(ctx context.Context, target pullrequestcomments.ProviderTarget, mutation pullrequestcomments.RemoteMutation) (pullrequestcomments.ProviderIdentity, error) {
	repository, err := githubapi.ParseRepositoryURL(target.RepositoryURL)
	if err != nil {
		return pullrequestcomments.ProviderIdentity{}, pullrequestcomments.PermanentProviderError(err)
	}
	var response issueComment
	if mutation.Scope == pullrequestcomments.ScopePullRequest {
		endpoint := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", repository.Owner, repository.Name, target.PullRequestNumber)
		err = gateway.client.RequestMutation(ctx, "POST", endpoint, map[string]string{"body": renderBody(mutation)}, &response)
		if err != nil {
			return pullrequestcomments.ProviderIdentity{}, providerError(err)
		}
		return pullrequestcomments.ProviderIdentity{Provider: "github", ExternalID: "conversation:" + strconv.FormatInt(response.ID, 10), Kind: "conversation"}, nil
	}
	request := map[string]any{"body": renderBody(mutation)}
	endpoint := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments", repository.Owner, repository.Name, target.PullRequestNumber)
	if parentID, ok := externalDatabaseID(mutation.ParentExternalID, "inline:"); ok {
		endpoint = fmt.Sprintf("/repos/%s/%s/pulls/%d/comments/%d/replies", repository.Owner, repository.Name, target.PullRequestNumber, parentID)
	} else {
		request["commit_id"], request["path"] = mutation.OriginalHeadCommit, mutation.Path
		if mutation.Scope == pullrequestcomments.ScopeFile {
			request["subject_type"] = "file"
		} else {
			request["side"], request["line"] = mutation.Side, mutation.Line
		}
	}
	if err := gateway.client.RequestMutation(ctx, "POST", endpoint, request, &response); err != nil {
		return pullrequestcomments.ProviderIdentity{}, providerError(err)
	}
	return pullrequestcomments.ProviderIdentity{Provider: "github", ExternalID: "inline:" + strconv.FormatInt(response.ID, 10), Kind: "inline"}, nil
}

func (gateway *Gateway) UpdateComment(ctx context.Context, target pullrequestcomments.ProviderTarget, mutation pullrequestcomments.RemoteMutation) error {
	return gateway.commentREST(ctx, target, mutation, "PATCH")
}
func (gateway *Gateway) DeleteComment(ctx context.Context, target pullrequestcomments.ProviderTarget, mutation pullrequestcomments.RemoteMutation) error {
	return gateway.commentREST(ctx, target, mutation, "DELETE")
}

func (gateway *Gateway) commentREST(ctx context.Context, target pullrequestcomments.ProviderTarget, mutation pullrequestcomments.RemoteMutation, method string) error {
	repository, err := githubapi.ParseRepositoryURL(target.RepositoryURL)
	if err != nil {
		return pullrequestcomments.PermanentProviderError(err)
	}
	kind, id, ok := parseExternalID(mutation.ProviderIdentity.ExternalID)
	if !ok {
		return pullrequestcomments.PermanentProviderError(errors.New("invalid GitHub comment identity"))
	}
	resource := "issues/comments"
	if kind == "inline" {
		resource = "pulls/comments"
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/%s/%d", repository.Owner, repository.Name, resource, id)
	var input any
	if method == "PATCH" {
		input = map[string]string{"body": renderBody(mutation)}
	}
	if err := gateway.client.RequestMutation(ctx, method, endpoint, input, nil); err != nil {
		return providerError(err)
	}
	return nil
}

func (gateway *Gateway) ResolveThread(ctx context.Context, target pullrequestcomments.ProviderTarget, mutation pullrequestcomments.RemoteMutation) error {
	return gateway.setThreadResolution(ctx, mutation.ProviderIdentity.ThreadID, true)
}
func (gateway *Gateway) ReopenThread(ctx context.Context, target pullrequestcomments.ProviderTarget, mutation pullrequestcomments.RemoteMutation) error {
	return gateway.setThreadResolution(ctx, mutation.ProviderIdentity.ThreadID, false)
}

func (gateway *Gateway) setThreadResolution(ctx context.Context, threadID string, resolved bool) error {
	if threadID == "" {
		return pullrequestcomments.PermanentProviderError(errors.New("provider review thread identity is unavailable"))
	}
	name := "resolveReviewThread"
	query := `mutation($id:ID!){resolveReviewThread(input:{threadId:$id}){thread{id}}}`
	if !resolved {
		name = "unresolveReviewThread"
		query = `mutation($id:ID!){unresolveReviewThread(input:{threadId:$id}){thread{id}}}`
	}
	var response map[string]any
	if err := gateway.client.GraphQL(ctx, query, map[string]any{"id": threadID}, &response); err != nil {
		return providerError(fmt.Errorf("%s: %w", name, err))
	}
	return nil
}

func providerError(err error) error {
	var rejection *githubapi.RejectionError
	if errors.As(err, &rejection) {
		// GitHub also uses 403 for secondary rate limits. Preserve retries for it.
		if rejection.Status == 429 || rejection.Status == 403 {
			return pullrequestcomments.RetryableProviderError(err, 0)
		}
		return pullrequestcomments.PermanentProviderError(err)
	}
	var githubError *githubapi.Error
	if errors.As(err, &githubError) {
		return pullrequestcomments.RetryableProviderError(err, 0)
	}
	return err
}

func parseExternalID(value string) (string, int64, bool) {
	for _, kind := range []string{"conversation", "inline"} {
		if id, ok := externalDatabaseID(value, kind+":"); ok {
			return kind, id, true
		}
	}
	return "", 0, false
}
func externalDatabaseID(value, prefix string) (int64, bool) {
	if !strings.HasPrefix(value, prefix) {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(value, prefix), 10, 64)
	return id, err == nil && id > 0
}
func providerAuthor(user githubapi.User) pullrequestcomments.ProviderAuthor {
	return pullrequestcomments.ProviderAuthor{Provider: "github", ExternalID: user.NodeID, Login: user.Login}
}
func parseGitHubTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, value)
}

var _ pullrequestcomments.ProviderGateway = (*Gateway)(nil)
