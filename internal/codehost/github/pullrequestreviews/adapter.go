package pullrequestreviews

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"github.com/holark-ai/holark/internal/pullrequestreviews"
)

type Client interface {
	Request(context.Context, string, string, any, any) error
	RequestMutation(context.Context, string, string, any, any) error
}

type Gateway struct{ client Client }

func New(client Client) *Gateway { return &Gateway{client: client} }

type remoteReview struct {
	ID       int64  `json:"id"`
	Body     string `json:"body"`
	State    string `json:"state"`
	CommitID string `json:"commit_id"`
	URL      string `json:"html_url"`
}

func endpoint(target pullrequestreviews.Target) (string, error) {
	repository, err := githubapi.ParseRepositoryURL(target.RepositoryURL)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", repository.Owner, repository.Name, target.Number), nil
}

func marker(review pullrequestreviews.Review) string {
	return "<!-- holark:manual-review:" + review.ID + " -->"
}

func (g *Gateway) Find(ctx context.Context, target pullrequestreviews.Target, review pullrequestreviews.Review) (pullrequestreviews.Receipt, bool, error) {
	path, err := endpoint(target)
	if err != nil {
		return pullrequestreviews.Receipt{}, false, err
	}
	for page := 1; ; page++ {
		var reviews []remoteReview
		if err := g.client.Request(ctx, "GET", fmt.Sprintf("%s?per_page=100&page=%d", path, page), nil, &reviews); err != nil {
			return pullrequestreviews.Receipt{}, false, err
		}
		for _, remote := range reviews {
			if strings.Contains(remote.Body, marker(review)) {
				state := "COMMENTED"
				if review.Event == pullrequestreviews.Approve {
					state = "APPROVED"
				}
				if remote.ID <= 0 || remote.CommitID != review.HeadCommit || (remote.State != state && remote.State != "DISMISSED") {
					return pullrequestreviews.Receipt{}, false, errors.New("the GitHub review could not be confirmed")
				}
				return pullrequestreviews.Receipt{ID: strconv.FormatInt(remote.ID, 10), URL: remote.URL}, true, nil
			}
		}
		if len(reviews) < 100 {
			return pullrequestreviews.Receipt{}, false, nil
		}
	}
}

func (g *Gateway) Create(ctx context.Context, target pullrequestreviews.Target, review pullrequestreviews.Review) (pullrequestreviews.Receipt, pullrequestreviews.WriteOutcome, error) {
	path, err := endpoint(target)
	if err != nil {
		return pullrequestreviews.Receipt{}, pullrequestreviews.WriteNotAttempted, err
	}
	// GitHub's commit_id selects the reviewed commit; it does not require that
	// commit to remain the PR head. Check the live head before submitting.
	var current githubapi.PullRequest
	if err := g.client.Request(ctx, "GET", strings.TrimSuffix(path, "/reviews"), nil, &current); err != nil {
		return pullrequestreviews.Receipt{}, pullrequestreviews.WriteNotAttempted, err
	}
	if current.Head.SHA == "" || current.Head.SHA != review.HeadCommit {
		return pullrequestreviews.Receipt{}, pullrequestreviews.WriteNotAttempted, pullrequestreviews.ErrStaleHead
	}
	body := marker(review)
	if review.Body != "" {
		body = review.Body + "\n\n" + body
	}
	input := struct {
		Event    string `json:"event"`
		Body     string `json:"body"`
		CommitID string `json:"commit_id"`
	}{strings.ToUpper(string(review.Event)), body, review.HeadCommit}
	var remote remoteReview
	if err := g.client.RequestMutation(ctx, "POST", path, input, &remote); err != nil {
		outcome := pullrequestreviews.WriteUnconfirmed
		var rejection *githubapi.RejectionError
		if errors.As(err, &rejection) {
			outcome = pullrequestreviews.WriteRejected
		} else if errors.Is(err, exec.ErrNotFound) {
			outcome = pullrequestreviews.WriteNotAttempted
		}
		return pullrequestreviews.Receipt{}, outcome, err
	}
	state := "COMMENTED"
	if review.Event == pullrequestreviews.Approve {
		state = "APPROVED"
	}
	if remote.ID <= 0 || remote.CommitID != review.HeadCommit || remote.State != state {
		return pullrequestreviews.Receipt{}, pullrequestreviews.WriteUnconfirmed, errors.New("GitHub did not confirm the submitted review; retry to check its status")
	}
	return pullrequestreviews.Receipt{ID: strconv.FormatInt(remote.ID, 10), URL: remote.URL}, pullrequestreviews.WriteConfirmed, nil
}
