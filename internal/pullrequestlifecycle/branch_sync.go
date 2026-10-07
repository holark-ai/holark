package pullrequestlifecycle

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/holark-ai/holark/internal/repository"
)

type branchBatch struct {
	source string
	refs   map[string]repository.BranchIdentity
}

func (c *Coordinator) branchSources(p PullRequest) (string, string) {
	source := DecodeGitHubObservation(p.SyncData)
	if p.SyncExternalID == "" {
		source := c.PublicationRepositoryURL(p.RepositoryID)
		return source, source
	}
	return source.BaseRepositoryURL, source.HeadRepositoryURL
}

// refreshPublishedBranches runs inside its caller’s remote slot. Branch passes
// serialize separately, and each adapter call owns at most one Git permit.
func (c *Coordinator) refreshPublishedBranches(ctx context.Context, values []PullRequest) error {
	_, err := c.refreshPublishedBranchResults(ctx, values)
	return err
}

func (c *Coordinator) refreshPublishedBranchResults(ctx context.Context, values []PullRequest) (map[repository.BranchIdentity]error, error) {
	return c.refreshBranchResults(ctx, values)
}

func (c *Coordinator) refreshBranchResults(ctx context.Context, values []PullRequest) (map[repository.BranchIdentity]error, error) {
	// Keep observation, acceptance, and cache updates ordered per application
	// repository. This is separate from Git permits and browsing refreshes.
	failed := map[repository.BranchIdentity]error{}
	groups := map[string][]PullRequest{}
	for _, p := range values {
		groups[p.RepositoryID] = append(groups[p.RepositoryID], p)
	}
	var result error
	for id, group := range groups {
		gate := c.options.Refresh.limits(id).branches
		select {
		case gate <- struct{}{}:
		case <-ctx.Done():
			return failed, errors.Join(result, ctx.Err())
		}
		failures, err := c.refreshRepositoryBranchResults(ctx, group)
		<-gate
		for branch, err := range failures {
			failed[branch] = err
		}
		if err != nil && len(failures) == 0 {
			for _, p := range group {
				failed[p.BaseRef], failed[p.HeadRef] = err, err
			}
		}
		result = errors.Join(result, err)
	}
	return failed, result
}

func (c *Coordinator) refreshRepositoryBranchResults(ctx context.Context, values []PullRequest) (map[repository.BranchIdentity]error, error) {
	failed := map[repository.BranchIdentity]error{}
	catalog, ok := c.registry.(ComparisonCatalog)
	if !ok {
		return failed, repository.ErrRepositoryUnavailable
	}
	observer, ok := c.options.Repository.(repository.BranchObserver)
	if !ok {
		return failed, repository.ErrRepositoryUnavailable
	}
	cache, ok := c.options.Repository.(repository.PublishedBranchCache)
	if !ok {
		return failed, repository.ErrRepositoryUnavailable
	}
	batches := map[string]*branchBatch{}
	var result error
	for _, p := range values {
		baseSource, headSource := c.branchSources(p)
		for _, v := range []struct {
			id     repository.BranchIdentity
			source string
		}{{p.BaseRef, baseSource}, {p.HeadRef, headSource}} {
			if !v.id.Valid() || repository.RepositoryIdentity(v.source) != v.id.Repository {
				result = errors.Join(result, repository.ErrInvalidRepository)
				failed[v.id] = repository.ErrInvalidRepository
				continue
			}
			batch := batches[v.id.Repository]
			if batch == nil {
				batch = &branchBatch{source: v.source, refs: map[string]repository.BranchIdentity{}}
				batches[v.id.Repository] = batch
			}
			batch.refs[v.id.Ref] = v.id
		}
	}
	keys := make([]string, 0, len(batches))
	for key := range batches {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		batch := batches[key]
		refs := make([]string, 0, len(batch.refs))
		for ref := range batch.refs {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		ids := make([]repository.BranchIdentity, 0, len(refs))
		for _, ref := range refs {
			ids = append(ids, batch.refs[ref])
		}

		read, err := catalog.BeginBranchObservation(ctx, ids)
		var observed map[string]repository.BranchObservation
		if err == nil {
			observed, err = observer.ObserveBranches(ctx, batch.source, refs)
		}
		if err != nil {
			for _, id := range ids {
				failed[id] = err
			}
			result = errors.Join(result, err)
			continue
		}
		// One protected ref must not discard independent evidence in its batch.
		for _, branch := range read.Branches {
			one := repository.BranchRead{Sequence: read.Sequence, Branches: []repository.PublishedBranch{branch}}
			if err := catalog.AcceptBranchObservation(ctx, one, observed); err != nil {
				failed[branch.Identity] = err
				result = errors.Join(result, err)
				continue
			}
			if err := cache.CachePublishedBranch(ctx, batch.source, branch.Identity.Ref, observed[branch.Identity.Ref]); err != nil {
				err = fmt.Errorf("cache published branch %s: %w", branch.Identity.Ref, err)
				failed[branch.Identity] = err
				result = errors.Join(result, err)
			}
		}
	}

	return failed, result
}

func (c *Coordinator) prepareComparison(ctx context.Context, id string) error {
	catalog, ok := c.registry.(ComparisonCatalog)
	if !ok || c.options.Repository == nil {
		return repository.ErrRepositoryUnavailable
	}
	p, ok := c.registry.GetPullRequest(id)
	if !ok {
		return ErrNotFound
	}
	if !p.Status.Active() {
		return nil
	}
	inputs, err := catalog.CaptureComparison(ctx, id)
	if err != nil {
		return err
	}
	baseSource, headSource := c.branchSources(p)
	if repository.RepositoryIdentity(baseSource) != inputs.Base.Identity.Repository || repository.RepositoryIdentity(headSource) != inputs.Head.Identity.Repository {
		return ErrComparisonUnavailable
	}
	if err := c.options.Repository.EnsureRemoteCommit(ctx, baseSource, inputs.Base.Commit); err != nil {
		return err
	}
	if err := c.options.Repository.EnsureRemoteCommit(ctx, headSource, inputs.Head.Commit); err != nil {
		return err
	}
	mergeBase, err := c.options.Repository.MergeBase(ctx, inputs.Base.Commit, inputs.Head.Commit)
	if err != nil {
		return err
	}
	return catalog.AcceptComparison(ctx, id, inputs, strings.TrimSpace(mergeBase))
}
func (c *Coordinator) enqueueActiveBranches(ctx context.Context, projectID string) {
	c.options.Refresh.Trigger(ctx, RefreshKey{RepositoryID: projectID, Section: "published_branches"}, func(ctx context.Context) error {
		var active []PullRequest
		for _, p := range c.registry.ListPullRequests(projectID) {
			if p.Status.Active() {
				active = append(active, p)
			}
		}
		failed, err := c.refreshBranchResults(ctx, active)
		for _, p := range c.registry.ListPullRequests(projectID) {
			if !p.Status.Active() {
				continue
			}
			if failed[p.BaseRef] != nil || failed[p.HeadRef] != nil || err != nil && len(failed) == 0 {
				if catalog, ok := c.registry.(ComparisonCatalog); ok {
					_ = catalog.InvalidateComparison(ctx, p.ID)
				}
				continue
			}
			if !p.HasCurrentComparison() {
				id := p.ID
				c.options.Refresh.Trigger(ctx, RefreshKey{RepositoryID: projectID, PullRequestID: id, Section: "comparison"}, func(ctx context.Context) error { return c.prepareComparison(ctx, id) })
			}
		}
		return err
	})
}

func (c *Coordinator) synchronizeAccepted(ctx context.Context, current PullRequest) error {
	return c.synchronizePublication(ctx, current, false)
}

func (c *Coordinator) synchronizePublication(ctx context.Context, current PullRequest, publication bool) error {
	if c.options.Projects == nil {
		return ErrNotFound
	}
	project, ok := c.options.Projects.Project(current.RepositoryID)
	if !ok {
		return ErrNotFound
	}
	token, err := c.beginObservation(ctx, current.RepositoryID)
	if err != nil {
		return err
	}
	github := current.SyncProvider == SyncProviderGitHub && current.SyncExternalID != ""
	if github {
		if err := c.requireGitHubPorts(); err != nil {
			return err
		}
		remote, err := c.options.GitHubTransport.Get(ctx, c.githubTarget(current))
		if err != nil {
			return err
		}
		if remote.ExternalID != current.SyncExternalID {
			return ErrNotFound
		}
		values, err := c.reconcileGitHubSync([]PullRequest{current}, []PullRequest{pullRequestFromGitHub(project.ID, remote, "", c.now())})
		if err != nil {
			return err
		}
		values[0].BaseCommit, values[0].HeadCommit, values[0].DiffBaseCommit = current.BaseCommit, current.HeadCommit, current.DiffBaseCommit
		if _, _, err := c.upsertObserved(project.ID, values, token); err != nil {
			return err
		}
	} else if current.SyncProvider != "" && current.SyncProvider != SyncProviderGitHub {
		return ErrGitHubSyncUnsupported
	}
	accepted, ok := c.registry.GetPullRequest(current.ID)
	if !ok {
		return ErrNotFound
	}
	var result error
	if github && accepted.MetadataObservation < token.Sequence {
		result = ErrComparisonUnavailable
	}
	if !accepted.Status.Active() {
		if current.Status.Active() {
			return errors.Join(result, c.refreshPublishedBranches(ctx, []PullRequest{accepted}))
		}
		return result
	}
	branchErr := c.refreshPublishedBranches(ctx, []PullRequest{accepted})
	var comparisonErr error
	if branchErr == nil && (!publication || !c.currentComparison(ctx, c.refreshed(accepted))) {
		comparisonErr = c.prepareComparison(ctx, accepted.ID)
	}
	if branchErr != nil || comparisonErr != nil {
		if catalog, ok := c.registry.(ComparisonCatalog); ok {
			comparisonErr = errors.Join(comparisonErr, catalog.InvalidateComparison(ctx, accepted.ID))
		}
	}
	accepted, ok = c.registry.GetPullRequest(current.ID)
	if !ok {
		return errors.Join(result, branchErr, comparisonErr, ErrNotFound)
	}
	var readinessErr error
	if github && !publication {
		_, readinessErr = c.refreshGitHubReadiness(ctx, accepted)
	}
	latest, found := c.registry.GetPullRequest(current.ID)
	if !found || !latest.HasCurrentComparison() {
		result = errors.Join(result, ErrComparisonUnavailable)
	} else if catalog, ok := c.registry.(ComparisonCatalog); ok {
		pair, err := catalog.CaptureComparison(ctx, current.ID)
		if err != nil {
			result = errors.Join(result, err)
		} else if !SameComparisonVersion(pair, latest.Comparison.Inputs) {
			result = errors.Join(result, ErrComparisonUnavailable)
		}
	}
	return errors.Join(result, branchErr, comparisonErr, readinessErr)
}
