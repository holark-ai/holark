package pullrequestcomments

import (
	"context"
	"time"
)

// RunPublisher belongs to the application lifetime, independently of HTTP callers.
// Each sweep takes a finite snapshot, giving every due PR one bounded pass.
func (service *Service) RunPublisher(ctx context.Context, scheduler RefreshScheduler) {
	if service.provider == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		ids, err := service.provider.publications.DuePublicationPullRequests(ctx, service.now())
		if err != nil && ctx.Err() == nil {
			service.provider.report(err)
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			target, err := service.pullRequests.GetCommentTarget(ctx, id)
			if err != nil {
				service.provider.report(err)
				continue
			}
			remote, eligible := providerTarget(target)
			if !eligible {
				continue
			}
			pass := func(ctx context.Context) error {
				ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				unlock, err := service.remoteLocks.lockContext(ctx, id)
				if err != nil {
					return err
				}
				defer unlock()
				service.provider.flush(ctx, remote)
				return nil
			}
			if scheduler != nil {
				err = scheduler.Schedule(ctx, target.RepositoryID, id, pass)
			} else {
				err = pass(ctx)
			}
			if err != nil && ctx.Err() == nil {
				service.provider.report(err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-service.publicationWake:
		case <-ticker.C:
		}
	}
}
