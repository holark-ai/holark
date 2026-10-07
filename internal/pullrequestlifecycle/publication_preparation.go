package pullrequestlifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/holark-ai/holark/internal/repository"
)

// PreparePublication verifies reusable preparation before a manual publication.
// A failed verification is only a cache miss: the existing synchronization and
// publisher validation remain authoritative. Never hold a remote slot while
// waiting for the fallback's independently scheduled pass.
func (c *Coordinator) PreparePublication(ctx context.Context, id string) error {
	ctx = WithRequestID(ctx, RequestID(ctx))
	if err := ctx.Err(); err != nil {
		return err
	}
	current, ok := c.registry.GetPullRequest(id)
	if !ok {
		return ErrNotFound
	}
	started := time.Now()
	err := c.options.Refresh.Do(ctx, RefreshKey{RepositoryID: current.RepositoryID, PullRequestID: id, Section: "publication_verification"}, func(passCtx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		passCtx, cancel := context.WithCancel(passCtx)
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		defer cancel()
		passCtx = WithRequestID(passCtx, RequestID(ctx))
		slog.DebugContext(passCtx, "Publication verification scheduler acquired", "pull_request_id", id, "request_id", RequestID(ctx), "duration", time.Since(started))
		verificationStarted := time.Now()
		err := c.verifyPublication(passCtx, id)
		slog.DebugContext(passCtx, "Publication observation completed", "pull_request_id", id, "request_id", RequestID(ctx), "duration", time.Since(verificationStarted), "error", err)
		return err
	})
	slog.DebugContext(ctx, "Publication verification completed", "pull_request_id", id, "operation_id", RequestID(ctx), "duration", time.Since(started), "fallback_reason", err)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// A coalesced verification may belong to another cancelled caller.
	// Only this caller's cancellation ends preparation without recovery.
	if err != nil {
		fallbackStarted := time.Now()
		err := c.SyncPullRequest(ctx, id)
		slog.DebugContext(ctx, "Publication fallback sync completed", "pull_request_id", id, "request_id", RequestID(ctx), "duration", time.Since(fallbackStarted), "error", err)
		return err
	}
	return nil
}

func (c *Coordinator) currentComparison(ctx context.Context, p PullRequest) bool {
	catalog, ok := c.registry.(ComparisonCatalog)
	if !ok || !p.HasCurrentComparison() {
		return false
	}
	pair, err := catalog.CaptureComparison(ctx, p.ID)
	return err == nil && SameComparisonVersion(pair, p.Comparison.Inputs)
}

// Verification does not accept observations or write Git refs. Cache acceptance
// and fetching remain owned by synchronization, so an older verification cannot
// overwrite a newer observation or a publication checkpoint.
func (c *Coordinator) verifyPublication(ctx context.Context, id string) error {
	current, ok := c.registry.GetPullRequest(id)
	if !ok {
		return ErrNotFound
	}
	if !current.Status.Active() || !c.currentComparison(ctx, current) {
		return ErrComparisonUnavailable
	}
	inputs := *CaptureMutationInputs(current)
	combined := false
	if current.SyncProvider == SyncProviderGitHub {
		if current.SyncExternalID == "" {
			return ErrGitHubSyncUnsupported
		}
		if err := c.requireGitHubPorts(); err != nil {
			return err
		}
		var remote GitHubPullRequest
		var err error
		if reader, ok := c.options.GitHubTransport.(GitHubWorkStateReader); ok {
			remote, err = reader.GetWorkState(ctx, c.githubTarget(current))
			combined = true
			if errors.Is(err, ErrGitHubClientUnsupported) {
				remote, err = c.options.GitHubTransport.Get(ctx, c.githubTarget(current))
				combined = false
			}
		} else {
			remote, err = c.options.GitHubTransport.Get(ctx, c.githubTarget(current))
		}
		if err != nil {
			return err
		}
		observed := pullRequestFromGitHub(current.RepositoryID, remote, "", c.now())
		a, b := CaptureMutationInputs(current), CaptureMutationInputs(observed)
		if a.SyncExternalID != b.SyncExternalID || a.Status != b.Status || a.BaseBranch != b.BaseBranch || a.HeadBranch != b.HeadBranch || a.BaseRepositoryURL != b.BaseRepositoryURL || a.HeadRepositoryURL != b.HeadRepositoryURL {
			return errors.New("publication identity or lifecycle changed")
		}
		if combined {
			source := DecodeGitHubObservation(observed.SyncData)
			if a.BaseRepositoryURL == "" || a.HeadRepositoryURL == "" ||
				current.BaseRef != repository.PublishedBranchIdentity(source.BaseRepositoryURL, b.BaseBranch) ||
				current.HeadRef != repository.PublishedBranchIdentity(source.HeadRepositoryURL, b.HeadBranch) ||
				b.BaseCommit == "" || b.HeadCommit == "" || a.BaseCommit != b.BaseCommit || a.HeadCommit != b.HeadCommit {
				return errors.New("publication branch targets changed")
			}
		}
	} else if current.SyncProvider != "" {
		return ErrGitHubSyncUnsupported
	}
	if !combined {
		if err := c.verifyPublicationBranches(ctx, current); err != nil {
			return err
		}
	}
	latest, ok := c.registry.GetPullRequest(id)
	if !ok || inputs != *CaptureMutationInputs(latest) || !c.currentComparison(ctx, latest) {
		return ErrSynchronizationStale
	}
	return ctx.Err()
}

func (c *Coordinator) verifyPublicationBranches(ctx context.Context, current PullRequest) error {
	observer, ok := c.options.Repository.(repository.BranchObserver)
	if !ok {
		return repository.ErrRepositoryUnavailable
	}
	baseSource, headSource := c.branchSources(current)
	batches := map[string]*branchBatch{}
	expected := map[repository.BranchIdentity]string{current.BaseRef: current.BaseCommit, current.HeadRef: current.HeadCommit}
	for _, v := range []struct {
		id     repository.BranchIdentity
		source string
	}{{current.BaseRef, baseSource}, {current.HeadRef, headSource}} {
		if !v.id.Valid() || repository.RepositoryIdentity(v.source) != v.id.Repository {
			return repository.ErrInvalidRepository
		}
		batch := batches[v.id.Repository]
		if batch == nil {
			batch = &branchBatch{source: v.source, refs: map[string]repository.BranchIdentity{}}
			batches[v.id.Repository] = batch
		}
		batch.refs[v.id.Ref] = v.id
	}
	gate := c.options.Refresh.limits(current.RepositoryID).branches
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-gate }()
	for _, batch := range batches {
		refs := make([]string, 0, len(batch.refs))
		for ref := range batch.refs {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		observed, err := observer.ObserveBranches(ctx, batch.source, refs)
		if err != nil {
			return err
		}
		for ref, identity := range batch.refs {
			branch := observed[ref]
			if !branch.Exists || branch.Commit == "" || branch.Commit != expected[identity] {
				return fmt.Errorf("published branch changed: %s", ref)
			}
		}
	}
	return nil
}

// RequestPublicationRefresh accepts post-publication observations without
// readiness work. Separate keys prevent this lighter callback from replacing a
// pending full synchronization (or publication verification).
func (c *Coordinator) RequestPublicationRefresh(ctx context.Context, id string) {
	current, ok := c.registry.GetPullRequest(id)
	if !ok {
		return
	}
	c.options.Refresh.Trigger(ctx, RefreshKey{RepositoryID: current.RepositoryID, PullRequestID: id, Section: "publication_completion"}, func(ctx context.Context) error {
		current, ok := c.registry.GetPullRequest(id)
		if !ok {
			return ErrNotFound
		}
		return c.synchronizePublication(ctx, current, true)
	})
}
