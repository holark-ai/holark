package localapp

import (
	"context"
	"log"
	"slices"
	"strings"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

type pullRequestPanelPinner interface {
	Pin(context.Context, string) error
}

type pinningPullRequestCreator struct {
	creator interface {
		Create(context.Context, pullrequestlifecycle.CreatePullRequest) (pullrequestlifecycle.PullRequest, error)
	}
	pins   pullRequestPanelPinner
	logger *log.Logger
}

func (creator pinningPullRequestCreator) Create(ctx context.Context, request pullrequestlifecycle.CreatePullRequest) (pullrequestlifecycle.PullRequest, error) {
	pullRequest, err := creator.creator.Create(ctx, request)
	if err == nil {
		pinPullRequestToPanel(ctx, creator.pins, creator.logger, pullRequest.ID, "creation")
	}
	return pullRequest, err
}

func pinPullRequestToPanel(ctx context.Context, pins pullRequestPanelPinner, logger *log.Logger, pullRequestID, activity string) {
	pullRequestID = strings.TrimSpace(pullRequestID)
	if pins == nil || pullRequestID == "" {
		return
	}
	// Successful activity must retain its PR even if the initiating request disconnects.
	if err := pins.Pin(context.WithoutCancel(ctx), pullRequestID); err != nil && logger != nil {
		logger.Printf("Pin pull request after %s: pull_request_id=%s error=%v", activity, pullRequestID, err)
	}
}

func (s *terminalHolonService) pinHolonPullRequests(ctx context.Context, h holons.Holon) {
	pinPullRequestToPanel(ctx, s.panelPins, s.logger, h.PullRequestID, "Holon activity")
	if s.panelPins == nil || s.pullRequestCatalog == nil {
		return
	}
	// Locally created PRs link their source Holon through the catalog without
	// setting PullRequestID on that Holon.
	for _, pr := range s.pullRequestCatalog.ListPullRequests(s.repositoryID) {
		if pr.ID != h.PullRequestID && slices.Contains(pr.LinkedHolonIDs, h.ID) {
			pinPullRequestToPanel(ctx, s.panelPins, s.logger, pr.ID, "Holon activity")
		}
	}
}
