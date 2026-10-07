package pullrequestlifecycle

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// PrepareMerge reuses verified local preparation, then refreshes GitHub readiness.
// Verification and readiness release their scheduler slots before the single
// full-sync fallback. The merge coordinator must reload the PR afterward.
func (c *Coordinator) PrepareMerge(ctx context.Context, id string) (resultErr error) {
	ctx = WithRequestID(ctx, RequestID(ctx))
	started := time.Now()
	reason := "none"
	defer func() {
		slog.DebugContext(ctx, "Merge preparation completed", "pull_request_id", id, "request_id", RequestID(ctx), "duration", time.Since(started), "fallback_reason", reason, "error", resultErr)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	current, ok := c.registry.GetPullRequest(id)
	if !ok {
		return ErrNotFound
	}
	reader, supported := c.options.GitHubTransport.(GitHubWorkStateReader)
	if current.SyncProvider != SyncProviderGitHub || !supported {
		reason = "unsupported_provider"
	} else if current.Status != StatusOpen {
		reason = "local_preparation_stale"
	} else {
		verified, err := c.schedulePreparationVerification(ctx, current, reader, "merge_verification")
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrNotFound) {
			return err
		}
		if err == nil && verified.Status != StatusOpen {
			err = workVerificationMiss("local_preparation_stale")
		}
		if err == nil {
			readinessStarted := time.Now()
			_, err = c.RefreshGitHubReadiness(ctx, verified)
			slog.DebugContext(ctx, "Merge preparation readiness completed", "pull_request_id", id, "request_id", RequestID(ctx), "duration", time.Since(readinessStarted), "error", err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Transport failures must not allow cached permission to authorize a merge.
			// A rejected observation instead requires full preparation of the new state.
			if err != nil && !errors.Is(err, ErrSynchronizationStale) && !errors.Is(err, ErrComparisonUnavailable) && !errors.Is(err, ErrGitHubReadinessInactive) && !errors.Is(err, ErrGitHubReadinessUnsupported) {
				return err
			}
			if err == nil {
				err = c.preparationStillCurrent(ctx, verified)
			}
			if errors.Is(err, ErrNotFound) {
				return err
			}
			if err == nil {
				return nil
			}
			reason = "readiness_preparation_stale"
		} else {
			reason = "verification_failed"
			var miss workVerificationMiss
			if errors.As(err, &miss) {
				reason = string(miss)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fallbackStarted := time.Now()
	err := c.SyncPullRequest(ctx, id)
	slog.DebugContext(ctx, "Merge preparation fallback sync completed", "pull_request_id", id, "request_id", RequestID(ctx), "duration", time.Since(fallbackStarted), "fallback_reason", reason, "error", err)
	return err
}
