package localapp

import (
	"context"
	"sort"
	"strings"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

type localPullRequestLinkCatalog interface {
	ListPullRequests(string) []pullrequestlifecycle.PullRequest
}

type localPullRequestActivity interface {
	PullRequestActivity(context.Context, string) ([]holons.Holon, error)
}

// localPullRequestHolonLinks projects direct and purpose-built Holons into the
// single relationship view consumed by the operations sidebar.
type localPullRequestHolonLinks struct {
	catalog localPullRequestLinkCatalog
	holons  localPullRequestActivity
}

func (links localPullRequestHolonLinks) List(ctx context.Context, repositoryID string) ([]pullrequestlifecycle.HolonLink, error) {
	result := []pullrequestlifecycle.HolonLink{}
	for _, pr := range links.catalog.ListPullRequests(repositoryID) {
		items, err := links.forPullRequest(ctx, pr)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PullRequestID == result[j].PullRequestID {
			return result[i].HolonID < result[j].HolonID
		}
		return result[i].PullRequestID < result[j].PullRequestID
	})
	return result, nil
}

func (links localPullRequestHolonLinks) forPullRequest(ctx context.Context, pr pullrequestlifecycle.PullRequest) ([]pullrequestlifecycle.HolonLink, error) {
	seen := map[pullrequestlifecycle.HolonLink]struct{}{}
	add := func(id string) {
		if id = strings.TrimSpace(id); id != "" && strings.TrimSpace(pr.ID) != "" {
			seen[pullrequestlifecycle.HolonLink{PullRequestID: strings.TrimSpace(pr.ID), HolonID: id}] = struct{}{}
		}
	}
	for _, id := range pr.LinkedHolonIDs {
		add(id)
	}
	activity, err := links.holons.PullRequestActivity(ctx, pr.ID)
	for _, h := range activity {
		if h.PullRequestID == pr.ID {
			add(h.ID)
		}
	}
	result := make([]pullrequestlifecycle.HolonLink, 0, len(seen))
	for link := range seen {
		result = append(result, link)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PullRequestID == result[j].PullRequestID {
			return result[i].HolonID < result[j].HolonID
		}
		return result[i].PullRequestID < result[j].PullRequestID
	})
	return result, err
}
