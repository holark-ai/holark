package pullrequestlifecycle

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// RefreshKey identifies independently refreshable data. A request arriving while
// that data is being read schedules a follow-up, so action completion cannot lose
// its refresh by joining a request which began before the action.
type RefreshKey struct{ RepositoryID, PullRequestID, Section string }
type refreshPass struct {
	done   chan struct{}
	work   func(context.Context) (any, error)
	err    error
	result any
}
type refreshEntry struct{ pending *refreshPass }
type repositoryRefreshLimits struct {
	remote   chan struct{}
	git      chan struct{}
	branches chan struct{}
}
type RefreshCoordinator struct {
	root         context.Context
	cancel       context.CancelFunc
	workers      sync.WaitGroup
	closed       bool
	mu           sync.Mutex
	entries      map[RefreshKey]*refreshEntry
	repositories map[string]*repositoryRefreshLimits
	wait         func(context.Context, time.Duration) bool
}

func NewRefreshCoordinator() *RefreshCoordinator {
	ctx, cancel := context.WithCancel(context.Background())
	return &RefreshCoordinator{root: ctx, cancel: cancel, entries: make(map[RefreshKey]*refreshEntry), repositories: make(map[string]*repositoryRefreshLimits), wait: waitForRefresh}
}

func waitForRefresh(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func (c *RefreshCoordinator) limits(id string) *repositoryRefreshLimits {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.repositories[id] == nil {
		c.repositories[id] = &repositoryRefreshLimits{remote: make(chan struct{}, 2), git: make(chan struct{}, 1), branches: make(chan struct{}, 1)}
	}
	return c.repositories[id]
}
func (c *RefreshCoordinator) enqueue(ctx context.Context, key RefreshKey, work func(context.Context) (any, error)) *refreshPass {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		pass := &refreshPass{done: make(chan struct{}), err: context.Canceled}
		close(pass.done)
		return pass
	}
	if entry := c.entries[key]; entry != nil {
		if entry.pending == nil {
			entry.pending = &refreshPass{done: make(chan struct{}), work: work}
		} else {
			entry.pending.work = work
		}
		slog.DebugContext(ctx, "Pull request refresh coalesced", "repository_id", key.RepositoryID, "pull_request_id", key.PullRequestID, "section", key.Section)
		return entry.pending
	}
	pass := &refreshPass{done: make(chan struct{}), work: work}
	c.entries[key] = &refreshEntry{}
	// The caller may disconnect while remote work is in progress. Let registered
	// work and its follow-up finish; each pass has its own bounded lifetime.
	c.workers.Add(1)
	go c.run(c.root, key, pass)
	return pass
}
func (c *RefreshCoordinator) Do(ctx context.Context, key RefreshKey, work func(context.Context) error) error {
	_, err := c.DoResult(ctx, key, func(ctx context.Context) (any, error) { return nil, work(ctx) })
	return err
}

func (c *RefreshCoordinator) DoResult(ctx context.Context, key RefreshKey, work func(context.Context) (any, error)) (any, error) {
	pass := c.enqueue(ctx, key, work)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-pass.done:
		return pass.result, pass.err
	}
}
func (c *RefreshCoordinator) Trigger(ctx context.Context, key RefreshKey, work func(context.Context) error) {
	c.enqueue(ctx, key, func(ctx context.Context) (any, error) { return nil, work(ctx) })
}
func (c *RefreshCoordinator) run(ctx context.Context, key RefreshKey, pass *refreshPass) {
	defer c.workers.Done()
	limits := c.limits(key.RepositoryID)
	for {
		started := time.Now()
		passCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		select {
		case limits.remote <- struct{}{}:
			if passCtx.Err() != nil {
				pass.err = passCtx.Err()
			} else {
				pass.result, pass.err = pass.work(passCtx)
			}
			<-limits.remote
		case <-passCtx.Done():
			pass.err = passCtx.Err()
		}
		cancel()
		slog.DebugContext(ctx, "Pull request refresh completed", "repository_id", key.RepositoryID, "pull_request_id", key.PullRequestID, "section", key.Section, "duration", time.Since(started), "error", pass.err)
		c.mu.Lock()
		close(pass.done)
		entry := c.entries[key]
		if entry.pending == nil {
			delete(c.entries, key)
			c.mu.Unlock()
			return
		}
		pass = entry.pending
		entry.pending = nil
		c.mu.Unlock()
	}
}
func (c *RefreshCoordinator) WithGit(ctx context.Context, repositoryID string, work func(context.Context) error) error {
	limits := c.limits(repositoryID)
	select {
	case limits.git <- struct{}{}:
		defer func() { <-limits.git }()
	case <-ctx.Done():
		return ctx.Err()
	}
	started := time.Now()
	err := work(ctx)
	slog.DebugContext(ctx, "Pull request Git fetch completed", "repository_id", repositoryID, "duration", time.Since(started), "error", err)
	return err
}

// RunBasic detects active pull requests once per minute after the immediate
// startup refresh requested by Coordinator.RunBasic.
// Failures back off independently per repository and never clear cached data.
func (c *RefreshCoordinator) RunBasic(ctx context.Context, work func(context.Context) error) {
	c.RunBasicAfter(ctx, nil, work)
}

// RunBasicAfter schedules the next pass using the outcome of a startup pass
// that was already run by the caller.
func (c *RefreshCoordinator) RunBasicAfter(ctx context.Context, initialErr error, work func(context.Context) error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.workers.Add(1)
	c.mu.Unlock()
	defer c.workers.Done()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.root, cancel)
	defer stop()
	defer cancel()
	delay := basicRefreshDelay(initialErr)
	for {
		if !c.wait(ctx, delay) {
			return
		}
		err := work(ctx)
		if ctx.Err() != nil || c.root.Err() != nil {
			return
		}
		delay = basicRefreshDelay(err)
	}
}

func basicRefreshDelay(err error) time.Duration {
	delay := time.Minute
	if err == nil {
		return delay
	}
	var retry interface{ RetryAfter() time.Duration }
	if errors.As(err, &retry) && retry.RetryAfter() > delay {
		return retry.RetryAfter()
	}
	return delay
}

// Close cancels registered work and waits before the application closes stores.
func (c *RefreshCoordinator) Close() {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	c.workers.Wait()
}

func refreshResult[T any](ctx context.Context, scheduler *RefreshCoordinator, key RefreshKey, work func(context.Context) (T, error)) (T, error) {
	result, err := scheduler.DoResult(ctx, key, func(ctx context.Context) (any, error) { return work(ctx) })
	if result == nil {
		var zero T
		return zero, err
	}
	return result.(T), err
}
