package localapp

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

// localPullRequestRetirement coordinates the existing inactive-PR hook. Failures
// remain retryable on the next sync and never invalidate a successful merge or close.
type localPullRequestRetirement struct {
	mu      sync.Mutex
	catalog interface {
		GetPullRequest(string) (pullrequestlifecycle.PullRequest, bool)
		MarkPullRequestActivityRetired(string, int64) error
	}
	holons   *holons.Service
	links    localPullRequestHolonLinks
	metadata interface {
		RetireInactive(context.Context, string) error
	}
	work *pullrequestwork.Service
}

func (r *localPullRequestRetirement) RetireInactive(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	pr, ok := r.catalog.GetPullRequest(id)
	if !ok {
		return pullrequestlifecycle.ErrNotFound
	}
	if pr.Status.Active() || pr.ActivityRetired {
		return nil
	}
	var result error
	var ids []string
	if pr.Status == pullrequestlifecycle.StatusMerged || pr.Status == pullrequestlifecycle.StatusClosed {
		links, err := r.links.forPullRequest(ctx, pr)
		result = errors.Join(result, err)
		ids = make([]string, 0, len(links))
		for _, link := range links {
			ids = append(ids, link.HolonID)
		}
		// Link discovery may overlap a verified external reopen. Cleanup only
		// owns the terminal lifecycle generation it originally observed.
		latest, found := r.catalog.GetPullRequest(id)
		if !found || latest.Status.Active() || latest.LifecycleGeneration != pr.LifecycleGeneration {
			return result
		}
		result = errors.Join(result, r.work.CancelPullRequest(ctx, id, ids...))
	}
	// Preserve the metadata link when cleanup fails so a retry can find it.
	if result == nil {
		latest, found := r.catalog.GetPullRequest(id)
		if !found || latest.Status.Active() || latest.LifecycleGeneration != pr.LifecycleGeneration {
			return nil
		}
		result = r.metadata.RetireInactive(ctx, id)
	}
	if result == nil {
		// A successful shutdown request can still be waiting for process exit
		// or publication settlement. Keep retrying until that work finishes.
		for _, kind := range []pullrequestwork.Kind{pullrequestwork.KindReview, pullrequestwork.KindWorker, pullrequestwork.KindRebase} {
			work, err := r.work.List(ctx, id, kind)
			if err != nil {
				return err
			}
			for _, item := range work {
				if item.Active() {
					return nil
				}
			}
		}
		// Ordinary and metadata Holons have no work row. Their shutdown
		// request can succeed before the completion callback archives them.
		for _, id := range ids {
			h, err := r.holons.Get(ctx, id)
			if err != nil {
				return err
			}
			if !holons.IsTerminal(h.Status) {
				return nil
			}
			if !h.ReadOnly && h.WorktreePath != "" && h.WorktreeBranch != "" && h.ArchivedAt == nil {
				return nil
			}
		}
		result = r.catalog.MarkPullRequestActivityRetired(id, pr.LifecycleGeneration)
	}
	if result != nil {
		slog.ErrorContext(ctx, "retire pull request activity", "pull_request_id", id, "error", result)
	}
	return result
}
