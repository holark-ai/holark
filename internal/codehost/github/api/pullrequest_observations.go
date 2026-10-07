package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const pullRequestObservationFields = `author{login avatarUrl url ... on User{id} ... on Bot{id}} assignees(first:100){nodes{id login avatarUrl url} pageInfo{hasNextPage endCursor}} reviewRequests(first:100){nodes{requestedReviewer{... on User{id login avatarUrl url}}} pageInfo{hasNextPage endCursor}} id number title body url state isDraft createdAt updatedAt closedAt mergedAt mergeCommit{oid} baseRefName baseRefOid headRefName headRefOid baseRepository{nameWithOwner url} headRepository{nameWithOwner url}`

type observationUser struct {
	ID        string `json:"id"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatarUrl"`
	URL       string `json:"url"`
}

func (u observationUser) user() User {
	return User{NodeID: u.ID, Login: u.Login, AvatarURL: u.AvatarURL, HTMLURL: u.URL}
}

type participantPageInfo struct {
	HasNextPage *bool  `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}
type assigneeConnection struct {
	Nodes    *[]observationUser   `json:"nodes"`
	PageInfo *participantPageInfo `json:"pageInfo"`
}
type reviewRequestNode struct {
	RequestedReviewer *observationUser `json:"requestedReviewer"`
}
type reviewerConnection struct {
	Nodes    *[]reviewRequestNode `json:"nodes"`
	PageInfo *participantPageInfo `json:"pageInfo"`
}
type pullRequestObservation struct {
	Author         *observationUser    `json:"author"`
	Assignees      *assigneeConnection `json:"assignees"`
	ReviewRequests *reviewerConnection `json:"reviewRequests"`
	ID             string              `json:"id"`
	Number         int                 `json:"number"`
	Title          string              `json:"title"`
	Body           string              `json:"body"`
	URL            string              `json:"url"`
	State          string              `json:"state"`
	IsDraft        bool                `json:"isDraft"`
	CreatedAt      time.Time           `json:"createdAt"`
	UpdatedAt      time.Time           `json:"updatedAt"`
	ClosedAt       *time.Time          `json:"closedAt"`
	MergedAt       *time.Time          `json:"mergedAt"`
	MergeCommit    *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
	BaseRefName    string `json:"baseRefName"`
	BaseRefOID     string `json:"baseRefOid"`
	HeadRefName    string `json:"headRefName"`
	HeadRefOID     string `json:"headRefOid"`
	BaseRepository *struct {
		NameWithOwner string `json:"nameWithOwner"`
		URL           string `json:"url"`
	} `json:"baseRepository"`
	HeadRepository *struct {
		NameWithOwner string `json:"nameWithOwner"`
		URL           string `json:"url"`
	} `json:"headRepository"`
}

func (p pullRequestObservation) pullRequest() PullRequest {
	value := PullRequest{Number: p.Number, NodeID: p.ID, Title: p.Title, Body: p.Body, HTMLURL: p.URL, State: strings.ToLower(p.State), Draft: p.IsDraft, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, ClosedAt: p.ClosedAt, MergedAt: p.MergedAt, Merged: p.State == "MERGED", Base: Ref{Ref: p.BaseRefName, SHA: p.BaseRefOID}, Head: Ref{Ref: p.HeadRefName, SHA: p.HeadRefOID}}
	if p.Author != nil {
		value.User = p.Author.user()
	}
	if p.Assignees != nil && p.Assignees.Nodes != nil && p.Assignees.PageInfo != nil && p.Assignees.PageInfo.HasNextPage != nil && !*p.Assignees.PageInfo.HasNextPage {
		value.Assignees = []User{}
		for _, u := range *p.Assignees.Nodes {
			value.Assignees = append(value.Assignees, u.user())
		}
	}
	if p.ReviewRequests != nil && p.ReviewRequests.Nodes != nil && p.ReviewRequests.PageInfo != nil && p.ReviewRequests.PageInfo.HasNextPage != nil && !*p.ReviewRequests.PageInfo.HasNextPage {
		value.RequestedReviewers = []User{}
		for _, n := range *p.ReviewRequests.Nodes {
			if n.RequestedReviewer != nil && n.RequestedReviewer.ID != "" {
				value.RequestedReviewers = append(value.RequestedReviewers, n.RequestedReviewer.user())
			}
		}
	}
	if value.Merged {
		value.State = "closed"
	}
	if p.MergeCommit != nil {
		value.MergeCommitSHA = p.MergeCommit.OID
	}
	if p.BaseRepository != nil {
		value.Base.Repository = &RefRepository{CloneURL: p.BaseRepository.URL + ".git", FullName: p.BaseRepository.NameWithOwner}
	}
	if p.HeadRepository != nil {
		value.Head.Repository = &RefRepository{CloneURL: p.HeadRepository.URL + ".git", FullName: p.HeadRepository.NameWithOwner}
	}
	return value
}

func (client *CLIClient) ListActivePullRequests(ctx context.Context, repository Repository) ([]PullRequest, error) {
	fields := pullRequestObservationFields
	query := `query($owner:String!,$name:String!,$cursor:String){repository(owner:$owner,name:$name){pullRequests(states:OPEN,first:100,after:$cursor){nodes{` + fields + `}pageInfo{hasNextPage endCursor}}}}`
	var result []PullRequest
	seenCursors := map[string]bool{}
	cursor := ""
	for {
		var response struct {
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
			Data struct {
				Repository *struct {
					PullRequests *struct {
						Nodes    *[]pullRequestObservation `json:"nodes"`
						PageInfo *struct {
							HasNextPage *bool  `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"pullRequests"`
				} `json:"repository"`
			} `json:"data"`
		}
		variables := map[string]any{"owner": repository.Owner, "name": repository.Name}
		if cursor != "" {
			variables["cursor"] = cursor
		}
		if err := client.GraphQL(ctx, query, variables, &response); err != nil {
			return nil, err
		}
		if len(response.Errors) > 0 {
			return nil, &Error{Code: ErrorCodeSyncFailed, Err: errors.New(response.Errors[0].Message)}
		}
		if response.Data.Repository == nil {
			return nil, &Error{Code: ErrorCodeSyncFailed, Err: errors.New("GitHub repository was not returned")}
		}
		page := response.Data.Repository.PullRequests
		if page == nil || page.Nodes == nil || page.PageInfo == nil || page.PageInfo.HasNextPage == nil {
			return nil, &Error{Code: ErrorCodeSyncFailed, Err: errors.New("GitHub returned an incomplete pull request page")}
		}
		for _, node := range *page.Nodes {
			if err := client.completeParticipants(ctx, &node); err != nil {
				return nil, err
			}
			result = append(result, node.pullRequest())
		}
		if !*page.PageInfo.HasNextPage {
			return result, nil
		}
		if page.PageInfo.EndCursor == "" || seenCursors[page.PageInfo.EndCursor] {
			return nil, &Error{Code: ErrorCodeSyncFailed, Err: errors.New("GitHub returned an incomplete pull request page")}
		}
		cursor = page.PageInfo.EndCursor
		seenCursors[cursor] = true
	}
}
func (client *CLIClient) MarkPullRequestReadyForReview(ctx context.Context, nodeID string) (PullRequest, error) {
	return client.pullRequestNodeMutationResult(ctx, nodeID, "markPullRequestReadyForReview")
}
func (client *CLIClient) ConvertPullRequestToDraft(ctx context.Context, nodeID string) (PullRequest, error) {
	return client.pullRequestNodeMutationResult(ctx, nodeID, "convertPullRequestToDraft")
}
func (client *CLIClient) pullRequestNodeMutationResult(ctx context.Context, nodeID, mutation string) (PullRequest, error) {
	if strings.TrimSpace(nodeID) == "" {
		return PullRequest{}, &Error{Code: ErrorCodeSyncFailed, Err: errors.New("GitHub pull request node ID is required")}
	}
	query := `mutation($pullRequestId:ID!){` + mutation + `(input:{pullRequestId:$pullRequestId}){pullRequest{` + pullRequestObservationFields + `}}}`
	var response struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Data map[string]struct {
			PullRequest pullRequestObservation `json:"pullRequest"`
		} `json:"data"`
	}
	if err := client.RequestMutation(ctx, "POST", "graphql", map[string]any{"query": query, "variables": map[string]any{"pullRequestId": nodeID}}, &response); err != nil {
		return PullRequest{}, err
	}

	if len(response.Errors) > 0 {
		return PullRequest{}, &Error{Code: ErrorCodeSyncFailed, Err: errors.New(response.Errors[0].Message)}
	}
	result := response.Data[mutation].PullRequest
	if result.ID != nodeID || result.Number == 0 {
		return PullRequest{}, &Error{Code: ErrorCodeMutationAccepted, Err: errors.New("GitHub mutation returned no matching pull request")}
	}
	return result.pullRequest(), nil
}

// Only overflowing nested connections need another request. Ordinary bulk pages
// already contain complete participant snapshots, including authoritative empties.
func (c *CLIClient) completeParticipants(ctx context.Context, p *pullRequestObservation) error {
	for _, kind := range []string{"assignees", "reviewRequests"} {
		var info *participantPageInfo
		if kind == "assignees" && p.Assignees != nil {
			if p.Assignees.Nodes == nil {
				return errors.New("missing assignee nodes")
			}
			info = p.Assignees.PageInfo
		}
		if kind == "reviewRequests" && p.ReviewRequests != nil {
			if p.ReviewRequests.Nodes == nil {
				return errors.New("missing reviewer nodes")
			}
			info = p.ReviewRequests.PageInfo
		}
		seen := map[string]bool{}
		for info != nil && info.HasNextPage != nil && *info.HasNextPage {
			if info.EndCursor == "" || seen[info.EndCursor] {
				return fmt.Errorf("incomplete %s connection", kind)
			}
			seen[info.EndCursor] = true
			fields := "nodes{id login avatarUrl url}"
			if kind == "reviewRequests" {
				fields = "nodes{requestedReviewer{... on User{id login avatarUrl url}}}"
			}
			query := `query($id:ID!,$cursor:String!){node(id:$id){... on PullRequest{` + kind + `(first:100,after:$cursor){` + fields + ` pageInfo{hasNextPage endCursor}}}}}`
			var response struct {
				Errors []struct{ Message string }
				Data   struct{ Node pullRequestObservation }
			}
			if err := c.Request(ctx, "POST", "graphql", map[string]any{"query": query, "variables": map[string]any{"id": p.ID, "cursor": info.EndCursor}}, &response); err != nil {
				return err
			}
			if len(response.Errors) > 0 {
				return errors.New(response.Errors[0].Message)
			}
			next := response.Data.Node
			if kind == "assignees" {
				if next.Assignees == nil || next.Assignees.Nodes == nil || next.Assignees.PageInfo == nil || next.Assignees.PageInfo.HasNextPage == nil {
					return errors.New("incomplete assignee page")
				}
				*p.Assignees.Nodes = append(*p.Assignees.Nodes, *next.Assignees.Nodes...)
				p.Assignees.PageInfo = next.Assignees.PageInfo
				info = p.Assignees.PageInfo
			} else {
				if next.ReviewRequests == nil || next.ReviewRequests.Nodes == nil || next.ReviewRequests.PageInfo == nil || next.ReviewRequests.PageInfo.HasNextPage == nil {
					return errors.New("incomplete reviewer page")
				}
				*p.ReviewRequests.Nodes = append(*p.ReviewRequests.Nodes, *next.ReviewRequests.Nodes...)
				p.ReviewRequests.PageInfo = next.ReviewRequests.PageInfo
				info = p.ReviewRequests.PageInfo
			}
		}
	}
	return nil
}
