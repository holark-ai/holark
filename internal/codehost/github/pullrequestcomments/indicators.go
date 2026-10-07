package pullrequestcomments

import (
	"context"
	"errors"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
)

func (gateway *Gateway) CommentIndicators(ctx context.Context, target pullrequestcomments.ProviderTarget) (pullrequestcomments.CommentIndicators, error) {
	repository, err := githubapi.ParseRepositoryURL(target.RepositoryURL)
	if err != nil {
		return pullrequestcomments.CommentIndicators{}, pullrequestcomments.PermanentProviderError(err)
	}
	var response struct {
		Data struct {
			Repository struct {
				PullRequest *struct {
					UpdatedAt          string `json:"updatedAt"`
					TotalCommentsCount int    `json:"totalCommentsCount"`
					Comments           struct {
						TotalCount int `json:"totalCount"`
					} `json:"comments"`
					ReviewThreads struct {
						TotalCount int `json:"totalCount"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	const query = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){updatedAt totalCommentsCount comments{totalCount} reviewThreads{totalCount}}}}`
	if err := gateway.client.GraphQL(ctx, query, map[string]any{"owner": repository.Owner, "name": repository.Name, "number": target.PullRequestNumber}, &response); err != nil {
		return pullrequestcomments.CommentIndicators{}, refreshProviderError(err)
	}
	pr := response.Data.Repository.PullRequest
	if pr == nil || pr.UpdatedAt == "" {
		return pullrequestcomments.CommentIndicators{}, errors.New("GitHub comment indicators unavailable")
	}
	return pullrequestcomments.CommentIndicators{UpdatedAt: pr.UpdatedAt, TotalCommentsCount: pr.TotalCommentsCount, GeneralCommentCount: pr.Comments.TotalCount, ReviewThreadCount: pr.ReviewThreads.TotalCount}, nil
}
