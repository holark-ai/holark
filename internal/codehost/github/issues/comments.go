package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"github.com/holark-ai/holark/internal/issues"
	"github.com/holark-ai/holark/internal/issues/comments"
	"github.com/holark-ai/holark/internal/issues/workflow"
)

type commentClient interface {
	GraphQL(context.Context, string, map[string]any, any) error
	RequestMutation(context.Context, string, string, any, any) error
}
type graphComment struct {
	FullDatabaseID  json.Number `json:"fullDatabaseId"`
	ID              string      `json:"id"`
	Body            string      `json:"body"`
	URL             string      `json:"url"`
	CreatedAt       time.Time   `json:"createdAt"`
	UpdatedAt       time.Time   `json:"updatedAt"`
	ViewerCanUpdate bool        `json:"viewerCanUpdate"`
	ViewerCanDelete bool        `json:"viewerCanDelete"`
	Author          *struct {
		Login     string `json:"login"`
		AvatarURL string `json:"avatarUrl"`
		URL       string `json:"url"`
	} `json:"author"`
}

const commentFields = `id fullDatabaseId body url createdAt updatedAt viewerCanUpdate viewerCanDelete author{login avatarUrl url}`

func (c graphComment) local(issueID string) (comments.Comment, error) {
	id, err := strconv.ParseInt(string(c.FullDatabaseID), 10, 64)
	out := comments.Comment{IssueID: issueID, GitHubNodeID: c.ID, GitHubID: string(c.FullDatabaseID), Body: c.Body, URL: c.URL, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, CanEdit: c.ViewerCanUpdate, CanDelete: c.ViewerCanDelete}
	if err != nil || c.ID == "" || id <= 0 || c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		return out, errors.New("incomplete GitHub comment response")
	}
	if c.Author != nil {
		out.Author = comments.Author{Login: c.Author.Login, AvatarURL: c.Author.AvatarURL, URL: c.Author.URL}
	}
	return out, nil
}
func (p *Provider) commentDependencies(issue issues.Issue, repositoryURL string) (commentClient, githubapi.Repository, int, error) {
	repo, err := githubapi.ParseRepositoryURL(repositoryURL)
	if err != nil {
		return nil, repo, 0, classifyTransportError(err)
	}
	ref, err := issueReference(issue)
	if err != nil {
		return nil, repo, 0, classifyTransportError(err)
	}
	client, err := clientAs[commentClient](p.client)
	return client, repo, ref.Number, classifyTransportError(err)
}
func (p *Provider) ListComments(ctx context.Context, issue issues.Issue, repositoryURL string) ([]comments.Comment, *bool, error) {
	client, repo, number, err := p.commentDependencies(issue, repositoryURL)
	if err != nil {
		return nil, nil, err
	}
	result := []comments.Comment{}
	var cursor any
	seen := map[string]bool{}
	for {
		var response struct {
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
			Data struct {
				Repository *struct {
					IsArchived bool `json:"isArchived"`
					IsDisabled bool `json:"isDisabled"`
					Issue      *struct {
						Comments *struct {
							Nodes    []*graphComment `json:"nodes"`
							PageInfo *struct {
								HasNextPage bool   `json:"hasNextPage"`
								EndCursor   string `json:"endCursor"`
							} `json:"pageInfo"`
						} `json:"comments"`
					} `json:"issue"`
				} `json:"repository"`
			} `json:"data"`
		}
		query := `query($owner:String!,$name:String!,$number:Int!,$cursor:String){repository(owner:$owner,name:$name){isArchived isDisabled issue(number:$number){comments(first:100,after:$cursor){nodes{` + commentFields + `} pageInfo{hasNextPage endCursor}}}}}`
		err = client.GraphQL(ctx, query, map[string]any{"owner": repo.Owner, "name": repo.Name, "number": number, "cursor": cursor}, &response)
		if err != nil {
			return nil, nil, classifyTransportError(err)
		}
		if len(response.Errors) > 0 || response.Data.Repository == nil || response.Data.Repository.Issue == nil {
			return nil, nil, classifyTransportError(errors.New("GitHub discussion is unavailable or incomplete"))
		}
		page := response.Data.Repository.Issue
		if page.Comments == nil || page.Comments.PageInfo == nil {
			return nil, nil, classifyTransportError(errors.New("missing comment connection"))
		}
		for _, node := range page.Comments.Nodes {
			if node == nil {
				return nil, nil, classifyTransportError(errors.New("missing comment node"))
			}
			c, e := node.local(issue.ID)
			if e != nil {
				return nil, nil, classifyTransportError(e)
			}
			result = append(result, c)
		}
		info := page.Comments.PageInfo
		if !info.HasNextPage {
			repo := response.Data.Repository
			// Reads establish repository restrictions, not permission to publish.
			if repo.IsArchived || repo.IsDisabled {
				allowed := false
				return result, &allowed, nil
			}
			return result, nil, nil
		}
		if info.EndCursor == "" || seen[info.EndCursor] {
			return nil, nil, classifyTransportError(errors.New("invalid comment pagination cursor"))
		}
		seen[info.EndCursor] = true
		cursor = info.EndCursor
	}
}
func (p *Provider) CreateComment(ctx context.Context, issue issues.Issue, repositoryURL, body string) (comments.Comment, error) {
	return p.writeComment(ctx, issue, repositoryURL, comments.Comment{}, body, "addComment")
}
func (p *Provider) UpdateComment(ctx context.Context, issue issues.Issue, repositoryURL string, c comments.Comment, body string) (comments.Comment, error) {
	return p.writeComment(ctx, issue, repositoryURL, c, body, "updateIssueComment")
}
func (p *Provider) DeleteComment(ctx context.Context, issue issues.Issue, repositoryURL string, c comments.Comment) error {
	_, err := p.writeComment(ctx, issue, repositoryURL, c, "", "deleteIssueComment")
	return err
}
func (p *Provider) writeComment(ctx context.Context, issue issues.Issue, repositoryURL string, c comments.Comment, body, operation string) (comments.Comment, error) {
	client, repo, number, err := p.commentDependencies(issue, repositoryURL)
	if err != nil {
		return c, err
	}
	id := c.GitHubNodeID
	input, fields, variables := "id:$id,body:$body", "comment:issueComment{"+commentFields+"}", "$id:ID!,$body:String!"
	if operation == "addComment" {
		var data syncDataEnvelope
		if err := json.Unmarshal(issue.SyncData, &data); err != nil {
			return c, classifyTransportError(err)
		}
		id = data.GitHub.NodeID
		// Older cached issues may predate node IDs. Resolve once before writing.
		if id == "" {
			var response struct {
				Data struct {
					Repository struct{ Issue struct{ ID string } }
				}
			}
			err := client.GraphQL(ctx, `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){issue(number:$number){id}}}`, map[string]any{"owner": repo.Owner, "name": repo.Name, "number": number}, &response)
			if err != nil {
				return c, classifyTransportError(err)
			}
			id = response.Data.Repository.Issue.ID
		}
		input, fields = "subjectId:$id,body:$body", "commentEdge{node{"+commentFields+"}}"
	} else if operation == "deleteIssueComment" {
		input, fields, variables = "id:$id", "clientMutationId", "$id:ID!"
	}
	if id == "" {
		return c, classifyTransportError(errors.New("missing GitHub node identity; sync before retrying"))
	}
	values := map[string]any{"id": id}
	if operation != "deleteIssueComment" {
		values["body"] = body
	}
	var response struct {
		Errors []struct {
			Type    string
			Message string
		}
		Data struct {
			Result *struct {
				Comment     *graphComment
				CommentEdge *struct{ Node *graphComment }
			}
		}
	}
	query := fmt.Sprintf("mutation(%s){result:%s(input:{%s}){%s}}", variables, operation, input, fields)
	err = client.RequestMutation(ctx, "POST", "graphql", map[string]any{"query": query, "variables": values}, &response)
	if err != nil {
		return c, commentMutationError(err, c)
	}
	result := response.Data.Result
	if result != nil {
		if operation == "deleteIssueComment" {
			return c, nil
		}
		node := result.Comment
		if result.CommentEdge != nil {
			node = result.CommentEdge.Node
		}
		if node != nil {
			canonical, err := node.local(issue.ID)
			if err != nil {
				return canonical, pendingComment(canonical, err)
			}
			return canonical, nil
		}
	}
	for _, e := range response.Errors {
		if result == nil && (e.Type == "FORBIDDEN" || e.Type == "NOT_FOUND" || e.Type == "UNPROCESSABLE") {
			return c, &workflow.CommentRejectedError{Status: 422, Message: e.Message}
		}
	}
	return c, &workflow.CommentMutationError{Outcome: workflow.CommentUncertain, GitHubID: c.GitHubID, URL: c.URL, Err: errors.New("GitHub did not return the comment mutation result")}
}
func pendingComment(c comments.Comment, err error) error {
	return &workflow.CommentMutationError{Outcome: workflow.CommentAccepted, GitHubID: c.GitHubID, URL: c.URL, Err: err}
}
func commentMutationError(err error, c comments.Comment) error {
	var rejection *githubapi.RejectionError
	if errors.As(err, &rejection) {
		return &workflow.CommentRejectedError{Status: rejection.Status, Message: rejection.Message}
	}
	var provider *githubapi.Error
	if errors.As(err, &provider) {
		if provider.Code == githubapi.ErrorCodeGHUnavailable {
			return classifyTransportError(err)
		}
	}
	// HTTP success alone cannot establish whether a GraphQL mutation executed.
	return &workflow.CommentMutationError{Outcome: workflow.CommentUncertain, GitHubID: c.GitHubID, URL: c.URL, Err: err}
}

var _ workflow.CommentTransport = (*Provider)(nil)
