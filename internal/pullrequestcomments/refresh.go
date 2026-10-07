package pullrequestcomments

import (
	"context"
	"errors"
	"sync"
	"time"
)

const commentCheckInterval = 15 * time.Second
const commentSyncInterval = 2 * time.Minute

// CommentIndicators are hints only: edits and resolutions may leave them unchanged.
type CommentIndicators struct {
	UpdatedAt           string
	TotalCommentsCount  int
	GeneralCommentCount int
	ReviewThreadCount   int
}

type IndicatorProvider interface {
	CommentIndicators(context.Context, ProviderTarget) (CommentIndicators, error)
}

type RefreshScheduler interface {
	Schedule(context.Context, string, string, func(context.Context) error) error
}
type RefreshSchedulerFunc func(context.Context, string, string, func(context.Context) error) error

func (f RefreshSchedulerFunc) Schedule(ctx context.Context, repositoryID, pullRequestID string, work func(context.Context) error) error {
	return f(ctx, repositoryID, pullRequestID, work)
}
func WithRefreshScheduler(scheduler RefreshScheduler) ServiceOption {
	return func(options *serviceOptions) { options.scheduler = scheduler }
}

// WithRefreshClock allows the refresh policy to run against a controlled clock.
func WithRefreshClock(now func() time.Time) ServiceOption {
	return func(options *serviceOptions) { options.clock = now }
}

type RefreshResult struct {
	Refreshed      bool       `json:"refreshed"`
	SyncedAt       *time.Time `json:"synced_at"`
	SyncApplicable bool       `json:"sync_applicable"`
}
type commentRefreshKey struct {
	repositoryID, pullRequestID, provider, repositoryURL string
	number                                               int
}
type commentRefreshPass struct {
	done    chan struct{}
	force   bool
	syncing bool
	result  RefreshResult
	err     error
}

type RefreshStatus struct {
	Syncing bool `json:"syncing"`
}

// RefreshStatus observes the shared refresh without starting work or contacting GitHub.
func (service *Service) RefreshStatus(ctx context.Context, pullRequestID string) (RefreshStatus, error) {
	target, err := service.pullRequests.GetCommentTarget(ctx, pullRequestID)
	if err != nil {
		return RefreshStatus{}, err
	}
	if target.ID == "" {
		return RefreshStatus{}, ErrPullRequestNotFound
	}
	key := commentRefreshKey{target.RepositoryID, target.ID, target.SyncProvider, target.RepositoryURL, target.ProviderPullRequest}
	service.refreshes.mu.Lock()
	defer service.refreshes.mu.Unlock()
	state := service.refreshes.states[key]
	return RefreshStatus{Syncing: state != nil && state.flight != nil && state.flight.syncing}, nil
}

type commentRefreshState struct {
	baseline                                      *CommentIndicators
	checkedAt, syncedAt, retryAt, providerRetryAt time.Time
	failures                                      int
	lastError                                     error
	flight                                        *commentRefreshPass
}
type commentRefreshes struct {
	mu        sync.Mutex
	states    map[commentRefreshKey]*commentRefreshState
	scheduler RefreshScheduler
	now       func() time.Time
}

func newCommentRefreshes(scheduler RefreshScheduler, now func() time.Time) *commentRefreshes {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if scheduler == nil {
		scheduler = RefreshSchedulerFunc(func(ctx context.Context, _, _ string, work func(context.Context) error) error {
			ctx, cancel := context.WithTimeout(ctx, commentSyncInterval)
			defer cancel()
			return work(ctx)
		})
	}
	return &commentRefreshes{states: make(map[commentRefreshKey]*commentRefreshState), scheduler: scheduler, now: now}
}
func (state *commentRefreshState) result() RefreshResult {
	result := RefreshResult{SyncApplicable: true}
	if !state.syncedAt.IsZero() {
		synced := state.syncedAt
		result.SyncedAt = &synced
	}
	return result
}

// Sync preserves the legacy list response while sharing refresh state and work.
func (service *Service) Sync(ctx context.Context, pullRequestID string) ([]Comment, error) {
	if _, err := service.Refresh(ctx, pullRequestID, true); err != nil {
		return nil, err
	}
	return service.ListByPullRequest(ctx, pullRequestID)
}

func (service *Service) Refresh(ctx context.Context, pullRequestID string, force bool) (RefreshResult, error) {
	target, err := service.pullRequests.GetCommentTarget(ctx, pullRequestID)
	if err != nil {
		return RefreshResult{}, err
	}
	if target.ID == "" {
		return RefreshResult{}, ErrPullRequestNotFound
	}
	remoteTarget, eligible := providerTarget(target)
	if service.provider == nil || !eligible || (target.Status != PullRequestDraft && target.Status != PullRequestOpen) {
		return RefreshResult{}, nil
	}
	refreshes := service.refreshes
	key := commentRefreshKey{target.RepositoryID, target.ID, target.SyncProvider, target.RepositoryURL, target.ProviderPullRequest}
	refreshes.mu.Lock()
	state := refreshes.states[key]
	if state == nil {
		state = &commentRefreshState{}
		refreshes.states[key] = state
	}
	pass := state.flight
	if pass == nil {
		now := refreshes.now()
		if now.Before(state.providerRetryAt) || (!force && now.Before(state.retryAt)) {
			result, err := state.result(), state.lastError
			refreshes.mu.Unlock()
			return result, err
		}
		if !force && !state.checkedAt.IsZero() && now.Sub(state.checkedAt) < commentCheckInterval {
			result := state.result()
			refreshes.mu.Unlock()
			return result, nil
		}
		pass = &commentRefreshPass{done: make(chan struct{})}
		state.flight = pass
		state.checkedAt = now
		// The shared work survives an individual tab disconnecting. The scheduler
		// owns its bounded lifetime and the application's shutdown cancellation.
		go service.runCommentRefresh(context.WithoutCancel(ctx), target, remoteTarget, state, pass, force)
	}
	pass.force = pass.force || force
	refreshes.mu.Unlock()
	select {
	case <-ctx.Done():
		return RefreshResult{}, ctx.Err()
	case <-pass.done:
		return pass.result, pass.err
	}
}

func (service *Service) runCommentRefresh(ctx context.Context, target PullRequestTarget, remoteTarget ProviderTarget, state *commentRefreshState, pass *commentRefreshPass, force bool) {
	refreshes := service.refreshes
	var indicators *CommentIndicators
	var indicatorErr error
	synced := false
	err := refreshes.scheduler.Schedule(ctx, target.RepositoryID, target.ID, func(ctx context.Context) error {
		due := force || state.syncedAt.IsZero() || refreshes.now().Sub(state.syncedAt) >= commentSyncInterval
		if provider, ok := service.provider.gateway.(IndicatorProvider); ok {
			// Leave time for an already-due full sync even if the hint query stalls.
			indicatorCtx, cancel := context.WithTimeout(ctx, commentCheckInterval)
			value, err := provider.CommentIndicators(indicatorCtx, remoteTarget)
			cancel()
			indicatorErr = err
			if err == nil {
				indicators = &value
			}
		}
		if indicatorErr != nil {
			// Provider backoff applies even when a full sync is already due.
			var retry interface{ RetryAfter() time.Duration }
			var rateLimit interface{ RateLimited() bool }
			if !due || (errors.As(indicatorErr, &retry) && retry.RetryAfter() > 0) ||
				(errors.As(indicatorErr, &rateLimit) && rateLimit.RateLimited()) {
				return indicatorErr
			}
		}
		if !due && indicators != nil && state.baseline != nil && *indicators == *state.baseline {
			return nil
		}
		refreshes.mu.Lock()
		pass.syncing = true
		refreshes.mu.Unlock()
		defer func() {
			refreshes.mu.Lock()
			pass.syncing = false
			refreshes.mu.Unlock()
		}()
		if _, err := service.synchronize(ctx, target.ID); err != nil {
			return err
		}
		synced = true
		return nil
	})
	refreshes.mu.Lock()
	defer refreshes.mu.Unlock()
	if err == nil && !synced && !force && pass.force {
		// A manual refresh joined an indicator-only check. Keep all callers
		// waiting on this pass until one forced follow-up fetches the comments.
		go service.runCommentRefresh(ctx, target, remoteTarget, state, pass, true)
		return
	}
	now := refreshes.now()
	if err != nil {
		state.failures++
		delay := commentCheckInterval
		for attempt := 1; attempt < state.failures && delay < commentSyncInterval; attempt++ {
			delay *= 2
		}
		if delay > commentSyncInterval {
			delay = commentSyncInterval
		}
		// A due full sync may fail differently from its preceding hint query.
		// Preserve the longer provider deadline from either failed request.
		rateLimited := false
		for _, failure := range []error{err, indicatorErr} {
			var retry interface{ RetryAfter() time.Duration }
			if errors.As(failure, &retry) && retry.RetryAfter() > 0 {
				deadline := now.Add(retry.RetryAfter())
				if deadline.After(state.providerRetryAt) {
					state.providerRetryAt = deadline
				}
				if retry.RetryAfter() > delay {
					delay = retry.RetryAfter()
				}
			}
			var rateLimit interface{ RateLimited() bool }
			if errors.As(failure, &rateLimit) && rateLimit.RateLimited() {
				rateLimited = true
			}
		}
		if rateLimited {
			state.providerRetryAt = now.Add(delay)
		}
		state.retryAt, state.lastError = now.Add(delay), err
	} else {
		state.failures, state.lastError = 0, nil
		state.retryAt, state.providerRetryAt = time.Time{}, time.Time{}
		if synced {
			state.syncedAt, state.baseline = now, indicators
		}
	}
	pass.result, pass.err = state.result(), err
	pass.result.Refreshed = synced
	state.flight = nil
	close(pass.done)
}
