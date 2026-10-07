package pullrequestlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/holark-ai/holark/internal/repository"
	"strings"
	"unicode/utf8"
)

const (
	MaxCreationTitleBytes   = 160
	MaxCreationSummaryBytes = 4000
)

var (
	ErrInvalidCreation   = errors.New("invalid pull request request")
	ErrPullRequestExists = errors.New("pull request already exists for holon")
	ErrNoChanges         = errors.New("workspace has no changes")
	ErrWorkspaceDirty    = errors.New("workspace has uncommitted changes")
	ErrSourceNotReady    = errors.New("pull request source is not ready")
)

type PullRequestSource struct {
	HolonID       string
	BaseBranch    string
	BaseCommit    string
	HeadBranch    string
	HeadCommit    string
	PullRequestID string
	HasChanges    bool
	Dirty         bool
}

type CreationSource interface {
	PullRequestSource(context.Context, string) (PullRequestSource, error)
}

type CreationCatalog interface {
	ActionRegistry
	ListPullRequests(string) []PullRequest
	CreateOperationPullRequest(context.Context, string, PullRequest) (PullRequest, error)
}

type BranchPublisher interface {
	Publish(context.Context, string, string) error
}

type TransitionRequester interface {
	RequestTransition(context.Context, PullRequest, Status) (PullRequest, error)
}

type CreatePullRequest struct {
	HolonID      string
	RepositoryID string
	Title        string
	Summary      string
	Target       Status
}

type CreationCoordinator struct {
	catalog   CreationCatalog
	sources   CreationSource
	publisher BranchPublisher
	lifecycle TransitionRequester
	topology  TopologyRepository
}

func NewCreationCoordinator(catalog CreationCatalog, sources CreationSource, publisher BranchPublisher, lifecycle TransitionRequester, topology TopologyRepository) *CreationCoordinator {
	return &CreationCoordinator{catalog: catalog, sources: sources, publisher: publisher, lifecycle: lifecycle, topology: topology}
}

func (coordinator *CreationCoordinator) Create(ctx context.Context, request CreatePullRequest) (result PullRequest, resultErr error) {
	request.HolonID = strings.TrimSpace(request.HolonID)
	request.RepositoryID = strings.TrimSpace(request.RepositoryID)
	request.Title = strings.TrimSpace(request.Title)
	request.Summary = strings.TrimSpace(request.Summary)
	if coordinator == nil || coordinator.catalog == nil || coordinator.sources == nil || coordinator.publisher == nil || coordinator.lifecycle == nil || coordinator.topology == nil ||
		request.HolonID == "" || request.RepositoryID == "" || request.Title == "" ||
		len(request.Title) > MaxCreationTitleBytes || len(request.Summary) > MaxCreationSummaryBytes ||
		!utf8.ValidString(request.Title) || !utf8.ValidString(request.Summary) || !request.Target.Active() {
		return PullRequest{}, ErrInvalidCreation
	}
	var operation Operation
	{
		registry := coordinator.catalog
		var created bool
		var err error
		operation, created, err = registry.BeginOperation(ctx, Operation{RequestID: RequestID(ctx), HolonID: request.HolonID, Kind: "create", RequestedStatus: request.Target, Groups: []FieldGroup{LifecycleGroup, TopologyGroup}})
		if err != nil {
			return PullRequest{}, err
		}
		if !created {
			if operation.Status == "failed" {
				return PullRequest{}, StoredOperationError(operation)
			}
			if operation.PullRequestID != "" {
				for _, current := range coordinator.catalog.ListPullRequests(request.RepositoryID) {
					if current.ID == operation.PullRequestID {
						return current, nil
					}
				}
			}
			return PullRequest{}, ErrOperationInProgress
		}
		defer func() {
			if resultErr == nil {
				return
			}
			outcome, message := "failed", resultErr.Error()
			if resultErr != nil {
				outcome, message = "failed", resultErr.Error()
				var uncertain interface{ Uncertain() bool }
				if errors.As(resultErr, &uncertain) && uncertain.Uncertain() {
					outcome = "uncertain"
				}
			}
			_, _ = registry.CompleteOperation(context.WithoutCancel(ctx), operation.RequestID, outcome, message)
		}()
	}
	recordStep := func(id, name string, step OperationStep) error {
		return coordinator.catalog.RecordOperationStep(context.WithoutCancel(ctx), operation.RequestID, id, name, step)
	}

	for _, pullRequest := range coordinator.catalog.ListPullRequests(request.RepositoryID) {
		for _, holonID := range pullRequest.LinkedHolonIDs {
			if holonID == request.HolonID {
				return PullRequest{}, ErrPullRequestExists
			}
		}
	}
	source, err := coordinator.sources.PullRequestSource(ctx, request.HolonID)
	if err != nil {
		return PullRequest{}, err
	}
	if source.PullRequestID != "" {
		return PullRequest{}, ErrPullRequestExists
	}
	if source.Dirty {
		return PullRequest{}, ErrWorkspaceDirty
	}
	if !source.HasChanges {
		return PullRequest{}, ErrNoChanges
	}
	if source.HolonID == "" || source.BaseBranch == "" || source.BaseCommit == "" || source.HeadBranch == "" || source.HeadCommit == "" {
		return PullRequest{}, ErrSourceNotReady
	}
	var baseRef, headRef repository.BranchIdentity
	var branchRead *repository.BranchRead
	sourceURL := ""
	if identity, ok := coordinator.lifecycle.(interface{ PublicationRepositoryURL(string) string }); ok {
		sourceURL = identity.PublicationRepositoryURL(request.RepositoryID)
	}
	if sourceURL == "" {
		if descriptor, ok := coordinator.topology.(interface{ Descriptor() repository.Descriptor }); ok {
			sourceURL = descriptor.Descriptor().RepositoryURL
		}
	}
	baseRef = repository.PublishedBranchIdentity(sourceURL, source.BaseBranch)
	headRef = repository.PublishedBranchIdentity(sourceURL, source.HeadBranch)
	if catalog, ok := coordinator.catalog.(ComparisonCatalog); ok && baseRef.Valid() && headRef.Valid() {
		read, err := catalog.BeginBranchObservation(ctx, []repository.BranchIdentity{baseRef, headRef})
		if err != nil {
			return PullRequest{}, err
		}
		branchRead = &read
	}

	pullRequest, err := newPullRequestTopologyReconciler(coordinator.topology).Reconcile(ctx, PullRequest{
		BaseRef: baseRef, HeadRef: headRef, RepositoryID: request.RepositoryID, Title: request.Title, Summary: request.Summary,
		Status: StatusWIP, BaseBranch: source.BaseBranch, BaseCommit: source.BaseCommit,
		HeadBranch: source.HeadBranch, HeadCommit: source.HeadCommit,
		LinkedHolonIDs: []string{source.HolonID}, SyncData: json.RawMessage(`{}`),
	})
	if err != nil {
		return PullRequest{}, err
	}
	if err := recordStep("", "preparation", OperationStep{Status: "succeeded", HeadCommit: source.HeadCommit, Creation: &CreationInputs{BaseRef: baseRef, HeadRef: headRef, BranchRead: branchRead, RepositoryID: request.RepositoryID, Title: pullRequest.Title, Summary: pullRequest.Summary, BaseBranch: pullRequest.BaseBranch, BaseCommit: pullRequest.BaseCommit, HeadBranch: pullRequest.HeadBranch, DiffBaseCommit: pullRequest.DiffBaseCommit}}); err != nil {
		return PullRequest{}, err
	}
	if err := coordinator.publisher.Publish(ctx, source.HeadCommit, source.HeadBranch); err != nil {
		return PullRequest{}, err
	}
	if err := recordStep("", "publication", OperationStep{Status: "succeeded", HeadCommit: source.HeadCommit}); err != nil {
		return PullRequest{}, err
	}
	pullRequest, err = coordinator.catalog.CreateOperationPullRequest(context.WithoutCancel(ctx), operation.RequestID, pullRequest)
	if err != nil {
		return PullRequest{}, err
	}

	// The creation milestone is durable. Opening continues under its own gate,
	// owned by the existing metadata preparation workflow.
	{
		registry := coordinator.catalog
		if _, err := registry.CompleteOperation(context.WithoutCancel(ctx), operation.RequestID, "succeeded", ""); err != nil {
			return pullRequest, err
		}
	}
	if request.Target != StatusWIP {
		updated, _ := coordinator.lifecycle.RequestTransition(WithRequestID(ctx, operation.RequestID+":opening"), pullRequest, request.Target)
		// Retain the local WIP on launch or publication failure so its page
		// offers the existing metadata recovery or draft transition action.
		if updated.ID != "" {
			pullRequest = updated
		}
	}
	return pullRequest, nil
}

func (c *Coordinator) PublicationRepositoryURL(id string) string {
	if c.options.Projects != nil {
		if project, ok := c.options.Projects.Project(id); ok && project.RepositoryURL != "" {
			return project.RepositoryURL
		}
	}
	if descriptor, ok := c.options.Repository.(interface{ Descriptor() repository.Descriptor }); ok {
		return descriptor.Descriptor().RepositoryURL
	}
	return ""
}
