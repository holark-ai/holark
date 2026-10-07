package sqliteadapter

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/holark-ai/holark/internal/holons"
	holonsqlite "github.com/holark-ai/holark/internal/holons/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

type HolonPublicationCommitter struct {
	catalog *Store
	holons  *holonsqlite.Store
}

func NewHolonPublicationCommitter(catalog *Store, workspaces *holonsqlite.Store) *HolonPublicationCommitter {
	return &HolonPublicationCommitter{catalog: catalog, holons: workspaces}
}

func (c *HolonPublicationCommitter) CommitHolonPublication(ctx context.Context, id, expected, requestID string, h holons.Holon) (resultErr error) {
	// Notify only after the transaction and catalog lock have been released.
	defer func() {
		if resultErr == nil {
			c.catalog.NotifyPublicationCompletion(id)
		}
	}()
	// Match catalog operations' lock order: mutex before taking the single
	// database connection, so concurrent PR polling cannot deadlock publication.
	c.catalog.mu.Lock()
	defer c.catalog.mu.Unlock()
	tx, err := c.catalog.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRowContext(ctx, `select document from pull_request_catalog where id = ?`, id).Scan(&raw); err != nil {
		return err
	}
	var pr pullrequestlifecycle.PullRequest
	if err = json.Unmarshal([]byte(raw), &pr); err != nil {
		return err
	}
	if !pr.Status.Active() {
		return pullrequestlifecycle.ErrPublicationInactive
	}
	if pr.HeadBranch != h.UpstreamBranch {
		return pullrequestlifecycle.ErrPublicationStale
	}
	matched, err := c.catalog.AdvancePublishedHeadInTransaction(ctx, tx, id, expected, h.UpstreamHeadCommit)
	if err != nil {
		return err
	}
	if !matched {
		return pullrequestlifecycle.ErrPublicationStale
	}
	if err = c.holons.UpdatePublicationInTransaction(ctx, tx, h); err != nil {
		return err
	}
	if _, err = c.catalog.CompleteOperationInTransaction(ctx, tx, requestID, "succeeded", ""); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// HasPublicationAttempt recognizes a push whose PR observation arrived before
// the workspace checkpoint. The remote head itself is verified by shared sync.
func (s *Store) HasPublicationAttempt(ctx context.Context, prID, holonID, head string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `select exists(select 1 from pull_request_operations where pull_request_id=? and holon_id=? and json_extract(document,'$.kind') in ('publish','work_publish') and json_extract(document,'$.steps.publication.head_commit')=? and json_extract(document,'$.steps.publication.status') in ('running','succeeded'))`, prID, holonID, head).Scan(&exists)
	return exists, err
}

// ReservePublicationTarget composes catalog validation with the Holon write on
// the shared connection. No network or workspace inspection runs in this tx.
func (c *HolonPublicationCommitter) ReservePublicationTarget(ctx context.Context, h holons.Holon, target holons.PublicationReadiness) error {
	tx, err := c.catalog.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := getInTransaction(ctx, tx, target.TargetPullRequestID)
	if err != nil {
		return err
	}
	if !p.Status.Active() || !p.HasCurrentComparison() || p.HeadBranch != target.TargetBranch || p.HeadCommit != target.TargetCommit || fmt.Sprintf("%#v", *pullrequestlifecycle.CaptureMutationInputs(p)) != target.TargetVersion {
		return holons.ErrRebaseRequired
	}
	pair, err := captureComparison(ctx, tx, p)
	if err != nil {
		return err
	}
	if !sameBranchVersion(pair.Base, p.Comparison.Inputs.Base) || !sameBranchVersion(pair.Head, p.Comparison.Inputs.Head) {
		return holons.ErrRebaseRequired
	}
	if err := c.holons.ReserveSynchronizationInTransaction(ctx, tx, h); err != nil {
		return err
	}
	return tx.Commit()
}
