package pullrequestparticipants

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
)

type Service struct {
	store      Store
	targets    PullRequestTargetReader
	identities IdentityResolver
	provider   Provider
	logger     *slog.Logger
	locks      sync.Map
	sequence   atomic.Uint64
	accepted   sync.Map
	refresh    func(context.Context, string)
}

type Option func(*Service)

func WithLogger(logger *slog.Logger) Option {
	return func(service *Service) {
		if logger != nil {
			service.logger = logger
		}
	}
}

func NewService(store Store, targets PullRequestTargetReader, identities IdentityResolver, provider Provider, options ...Option) *Service {
	service := &Service{store: store, targets: targets, identities: identities, provider: provider, logger: slog.Default()}
	for _, option := range options {
		option(service)
	}
	return service
}

func (service *Service) Get(ctx context.Context, id string) (Snapshot, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Snapshot{}, ErrInvalidRequest
	}
	if service.targets != nil {
		if _, err := service.targets.GetParticipantTarget(ctx, id); err != nil {
			if errors.Is(err, ErrPullRequestNotFound) {
				return Snapshot{}, err
			}
			return Snapshot{}, fmt.Errorf("%w: %v", ErrReadFailed, err)
		}
	}
	if service.store == nil {
		return EmptySnapshot(id), nil
	}
	snapshot, err := service.store.Get(ctx, id)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrReadFailed, err)
	}
	return nonNilSnapshot(snapshot), nil
}

func (service *Service) GetMany(ctx context.Context, ids []string) (map[string]Snapshot, error) {
	result := make(map[string]Snapshot, len(ids))
	clean := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, ErrInvalidRequest
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		clean = append(clean, id)
		result[id] = EmptySnapshot(id)
	}
	if len(clean) == 0 || service.store == nil {
		return result, nil
	}
	stored, err := service.store.GetMany(ctx, clean)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrReadFailed, err)
	}
	for id, snapshot := range stored {
		result[id] = nonNilSnapshot(snapshot)
	}
	return result, nil
}

func (service *Service) ReplaceAssignees(ctx context.Context, id string, ids []string) (Snapshot, error) {
	return service.mutate(ctx, id, func(ctx context.Context, target PullRequestTarget, providerTarget ProviderTarget) (Snapshot, error) {
		normalized, err := normalizeIDs(ids)
		if err != nil {
			return Snapshot{}, err
		}
		logins, err := service.resolveLogins(ctx, target.RepositoryID, normalized)
		if err != nil {
			return Snapshot{}, err
		}
		if err := service.provider.ReplaceAssignees(ctx, providerTarget, logins); err != nil {
			return Snapshot{}, normalizeProviderError(err)
		}
		return service.updated(service.store.ReplaceAssignees(ctx, target.ID, normalized))
	})
}

func (service *Service) AddAssignee(ctx context.Context, id, memberID string) (Snapshot, error) {
	return service.mutateOne(ctx, id, memberID,
		func(ctx context.Context, target ProviderTarget, login string) error {
			return service.provider.AddAssignee(ctx, target, login)
		},
		func(ctx context.Context, id, memberID string) (Snapshot, error) {
			return service.store.AddAssignee(ctx, id, memberID)
		},
	)
}

func (service *Service) RemoveAssignee(ctx context.Context, id, memberID string) (Snapshot, error) {
	return service.mutateOne(ctx, id, memberID,
		func(ctx context.Context, target ProviderTarget, login string) error {
			return service.provider.RemoveAssignee(ctx, target, login)
		},
		func(ctx context.Context, id, memberID string) (Snapshot, error) {
			return service.store.RemoveAssignee(ctx, id, memberID)
		},
	)
}

func (service *Service) ReplaceRequestedReviewers(ctx context.Context, id string, ids []string) (Snapshot, error) {
	return service.mutate(ctx, id, func(ctx context.Context, target PullRequestTarget, providerTarget ProviderTarget) (Snapshot, error) {
		normalized, err := normalizeIDs(ids)
		if err != nil {
			return Snapshot{}, err
		}
		logins, err := service.resolveLogins(ctx, target.RepositoryID, normalized)
		if err != nil {
			return Snapshot{}, err
		}
		if err := service.provider.ReplaceRequestedReviewers(ctx, providerTarget, logins); err != nil {
			return Snapshot{}, normalizeProviderError(err)
		}
		return service.updated(service.store.ReplaceRequestedReviewers(ctx, target.ID, normalized))
	})
}

func (service *Service) AddRequestedReviewer(ctx context.Context, id, memberID string) (Snapshot, error) {
	return service.mutateOne(ctx, id, memberID,
		func(ctx context.Context, target ProviderTarget, login string) error {
			return service.provider.AddRequestedReviewer(ctx, target, login)
		},
		func(ctx context.Context, id, memberID string) (Snapshot, error) {
			return service.store.AddRequestedReviewer(ctx, id, memberID)
		},
	)
}

func (service *Service) RemoveRequestedReviewer(ctx context.Context, id, memberID string) (Snapshot, error) {
	return service.mutateOne(ctx, id, memberID,
		func(ctx context.Context, target ProviderTarget, login string) error {
			return service.provider.RemoveRequestedReviewer(ctx, target, login)
		},
		func(ctx context.Context, id, memberID string) (Snapshot, error) {
			return service.store.RemoveRequestedReviewer(ctx, id, memberID)
		},
	)
}

type providerOneMutation func(context.Context, ProviderTarget, string) error
type storeOneMutation func(context.Context, string, string) (Snapshot, error)

func (service *Service) mutateOne(ctx context.Context, id, memberID string, providerMutation providerOneMutation, storeMutation storeOneMutation) (Snapshot, error) {
	return service.mutate(ctx, id, func(ctx context.Context, target PullRequestTarget, providerTarget ProviderTarget) (Snapshot, error) {
		normalized, err := normalizeIDs([]string{memberID})
		if err != nil || len(normalized) != 1 {
			return Snapshot{}, ErrInvalidRequest
		}
		logins, err := service.resolveLogins(ctx, target.RepositoryID, normalized)
		if err != nil {
			return Snapshot{}, err
		}
		if err := providerMutation(ctx, providerTarget, logins[0]); err != nil {
			return Snapshot{}, normalizeProviderError(err)
		}
		return service.updated(storeMutation(ctx, target.ID, normalized[0]))
	})
}

func (service *Service) mutate(ctx context.Context, id string, operation func(context.Context, PullRequestTarget, ProviderTarget) (Snapshot, error)) (Snapshot, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Snapshot{}, ErrInvalidRequest
	}
	return withPRLock(service, id, func() (Snapshot, error) {
		target, err := service.readTarget(ctx, id)
		if err != nil {
			return Snapshot{}, err
		}
		if target.Status != PullRequestDraft && target.Status != PullRequestOpen {
			return Snapshot{}, ErrPullRequestReadOnly
		}
		providerTarget, err := validateProviderTarget(target)
		if err != nil {
			return Snapshot{}, err
		}
		if service.store == nil || service.identities == nil || service.provider == nil {
			return Snapshot{}, ErrUpdateFailed
		}
		snapshot, err := operation(ctx, target, providerTarget)
		if err == nil {
			service.accepted.Store(id, acceptedObservation{token: service.sequence.Add(1), mutation: true})
		}
		return snapshot, err
	})
}

func (service *Service) Sync(ctx context.Context, id string) (Snapshot, error) {
	return service.sync(ctx, id, false)
}

// Reconcile skips queued mutation reconciliation already satisfied by a complete refresh.
func (service *Service) Reconcile(ctx context.Context, id string) (Snapshot, error) {
	return service.sync(ctx, id, true)
}

func (service *Service) sync(ctx context.Context, id string, reconciliation bool) (Snapshot, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Snapshot{}, ErrInvalidRequest
	}
	return withPRLock(service, id, func() (Snapshot, error) {
		if reconciliation {
			accepted, ok := service.accepted.Load(id)
			if !ok || !accepted.(acceptedObservation).mutation {
				return service.Get(ctx, id)
			}
		}
		target, err := service.readTarget(ctx, id)
		if err != nil {
			return Snapshot{}, service.syncFailure(PullRequestTarget{ID: id}, SyncStageTargetRead, err, nil)
		}
		providerTarget, err := validateProviderTarget(target)
		if err != nil {
			return Snapshot{}, service.syncFailure(target, SyncStageTargetValidation, err, nil)
		}
		if service.provider == nil {
			return Snapshot{}, service.syncFailure(target, SyncStageProviderFetch, ErrProviderUnavailable, nil)
		}
		remote, err := service.provider.GetSnapshot(ctx, providerTarget)
		if err != nil {
			return Snapshot{}, service.syncFailure(target, SyncStageProviderFetch, normalizeProviderError(err), nil)
		}
		if !remote.Complete {
			return Snapshot{}, service.syncFailure(target, SyncStageProviderFetch, ErrIncompleteObservation, nil)
		}
		if service.identities == nil {
			err := ErrProjectMemberUnresolved
			return Snapshot{}, service.syncFailure(target, SyncStageIdentityResolution, err, appendCopy(remote.AssigneeGitHubNodeIDs, remote.RequestedReviewerGitHubNodeIDs...))
		}
		if err := service.observeMembers(ctx, target.RepositoryID, remote); err != nil {
			return Snapshot{}, err
		}
		assignees, err := service.identities.ResolveProjectMemberIDs(ctx, target.RepositoryID, remote.AssigneeGitHubNodeIDs)
		if err != nil {
			wrapped := fmt.Errorf("%w: %v", ErrProjectMemberUnresolved, err)
			return Snapshot{}, service.syncFailure(target, SyncStageIdentityResolution, wrapped, remote.AssigneeGitHubNodeIDs)
		}
		reviewers, err := service.identities.ResolveProjectMemberIDs(ctx, target.RepositoryID, remote.RequestedReviewerGitHubNodeIDs)
		if err != nil {
			wrapped := fmt.Errorf("%w: %v", ErrProjectMemberUnresolved, err)
			return Snapshot{}, service.syncFailure(target, SyncStageIdentityResolution, wrapped, remote.RequestedReviewerGitHubNodeIDs)
		}
		assignees, err = normalizeIDs(assignees)
		if err != nil {
			return Snapshot{}, service.syncFailure(target, SyncStageIdentityResolution, ErrProjectMemberUnresolved, remote.AssigneeGitHubNodeIDs)
		}
		reviewers, err = normalizeIDs(reviewers)
		if err != nil {
			return Snapshot{}, service.syncFailure(target, SyncStageIdentityResolution, ErrProjectMemberUnresolved, remote.RequestedReviewerGitHubNodeIDs)
		}
		if service.store == nil {
			return Snapshot{}, service.syncFailure(target, SyncStageSnapshotReplace, ErrUpdateFailed, nil)
		}
		snapshot, err := service.store.ReplaceSnapshot(ctx, Snapshot{PullRequestID: target.ID, AssigneeHolarkIDs: assignees, RequestedReviewerHolarkIDs: reviewers})
		if err != nil {
			return Snapshot{}, service.syncFailure(target, SyncStageSnapshotReplace, fmt.Errorf("%w: %v", ErrUpdateFailed, err), nil)
		}
		service.accepted.Store(id, acceptedObservation{token: service.sequence.Add(1)})
		return nonNilSnapshot(snapshot), nil
	})
}

func (service *Service) readTarget(ctx context.Context, id string) (PullRequestTarget, error) {
	if service.targets == nil {
		return PullRequestTarget{}, ErrPullRequestNotFound
	}
	target, err := service.targets.GetParticipantTarget(ctx, id)
	if err != nil {
		if errors.Is(err, ErrPullRequestNotFound) {
			return PullRequestTarget{}, err
		}
		return PullRequestTarget{}, fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}
	target.ID = strings.TrimSpace(target.ID)
	if target.ID == "" {
		target.ID = id
	}
	return target, nil
}

func (service *Service) resolveLogins(ctx context.Context, projectID string, ids []string) ([]string, error) {
	logins, err := service.identities.ResolveProjectMemberLogins(ctx, projectID, ids)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProjectMemberUnresolved, err)
	}
	logins, err = normalizeIDs(logins)
	if err != nil || len(logins) != len(ids) {
		return nil, ErrProjectMemberUnresolved
	}
	return logins, nil
}

func (service *Service) updated(snapshot Snapshot, err error) (Snapshot, error) {
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}
	return nonNilSnapshot(snapshot), nil
}

func (service *Service) syncFailure(target PullRequestTarget, stage SyncStage, err error, unresolved []string) error {
	publicCode, publicMessage := diagnostic(err)
	attributes := []any{
		"code", publicCode, "message", publicMessage, "repository_id", target.RepositoryID,
		"pull_request_id", target.ID, "provider", target.SyncProvider, "repository", target.RepositoryURL,
		"provider_pull_request", target.ProviderPullRequest, "stage", stage, "cause", err.Error(),
	}
	if len(unresolved) > 0 {
		attributes = append(attributes, "unresolved_provider_runtime_ids", unresolved)
	}
	service.logger.Error("pull request participant sync failed", attributes...)
	return &SyncError{PullRequestID: target.ID, Stage: stage, Err: err}
}

func validateProviderTarget(target PullRequestTarget) (ProviderTarget, error) {
	provider := strings.TrimSpace(target.SyncProvider)
	if provider == "" {
		return ProviderTarget{}, ErrProviderIdentityRequired
	}
	if provider != "github" {
		return ProviderTarget{}, ErrUnsupportedProvider
	}
	if strings.TrimSpace(target.RepositoryURL) == "" || target.ProviderPullRequest <= 0 {
		return ProviderTarget{}, ErrProviderIdentityRequired
	}
	if !isGitHubRepository(target.RepositoryURL) {
		return ProviderTarget{}, ErrUnsupportedProvider
	}
	return ProviderTarget{RepositoryURL: strings.TrimSpace(target.RepositoryURL), PullRequestNumber: target.ProviderPullRequest}, nil
}

func isGitHubRepository(raw string) bool {
	value := strings.TrimSpace(raw)
	if strings.HasPrefix(value, "git@github.com:") {
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(value, "git@github.com:"), ".git"), "/")
		return len(parts) == 2 && parts[0] != "" && parts[1] != ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() != "github.com" || (parsed.Scheme != "https" && parsed.Scheme != "ssh") {
		return false
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/"), "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

func normalizeIDs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, ErrInvalidRequest
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func normalizeProviderError(err error) error {
	if errors.Is(err, ErrProviderUnavailable) || errors.Is(err, ErrProviderFailed) || errors.Is(err, ErrUnsupportedProvider) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrProviderFailed, err)
}

func diagnostic(err error) (string, string) {
	switch {
	case errors.Is(err, ErrPullRequestNotFound):
		return "pull_request_not_found", "Pull request not found."
	case errors.Is(err, ErrProviderIdentityRequired):
		return "provider_identity_required", "A provider-backed pull request is required."
	case errors.Is(err, ErrUnsupportedProvider):
		return "unsupported_provider", "The pull request participant provider is unsupported."
	case errors.Is(err, ErrProjectMemberUnresolved):
		return "project_member_unresolved", "A GitHub participant is not a project member."
	case errors.Is(err, ErrProviderUnavailable):
		return "gh_unavailable", "GitHub is unavailable. Try again."
	case errors.Is(err, ErrProviderFailed):
		return "github_sync_failed", "GitHub participant synchronization failed."
	default:
		return "pull_request_participants_update_failed", "Pull request participants could not be updated."
	}
}

func nonNilSnapshot(snapshot Snapshot) Snapshot {
	if snapshot.AssigneeHolarkIDs == nil {
		snapshot.AssigneeHolarkIDs = []string{}
	}
	if snapshot.RequestedReviewerHolarkIDs == nil {
		snapshot.RequestedReviewerHolarkIDs = []string{}
	}
	return snapshot
}

func appendCopy(first []string, rest ...string) []string {
	result := append([]string(nil), first...)
	return append(result, rest...)
}

func withPRLock(service *Service, id string, operation func() (Snapshot, error)) (Snapshot, error) {
	value, _ := service.locks.LoadOrStore(id, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	return operation()
}
