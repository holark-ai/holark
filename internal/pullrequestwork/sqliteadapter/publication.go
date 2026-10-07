package sqliteadapter

import (
	"context"
	"database/sql"
	"strings"

	pullrequestsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

// PublicationCommitter atomically completes published work and advances both
// durable pull-request catalog representations.
type CompletionCommitter struct {
	db      *sql.DB
	work    *Store
	catalog *pullrequestsqlite.Store
}

// ContinuePublicationCommitter atomically advances a long-lived Continue
// worker checkpoint and both durable pull-request catalog representations.
type ContinuePublicationCommitter struct {
	db      *sql.DB
	work    *Store
	catalog *pullrequestsqlite.Store
}

func NewCompletionCommitter(work *Store, catalog *pullrequestsqlite.Store) *CompletionCommitter {
	return &CompletionCommitter{db: work.db, work: work, catalog: catalog}
}

func NewContinuePublicationCommitter(work *Store, catalog *pullrequestsqlite.Store) *ContinuePublicationCommitter {
	return &ContinuePublicationCommitter{db: work.db, work: work, catalog: catalog}
}

func (c *ContinuePublicationCommitter) CommitContinuePublication(ctx context.Context, work pullrequestwork.Work, expectedHead, publishedHead string) error {
	if c == nil || c.db == nil || c.work == nil || c.catalog == nil || work.Kind != pullrequestwork.KindWorker || work.Mode != pullrequestwork.ModeContinue || (work.Status != pullrequestwork.StatusRunning && work.Status != pullrequestwork.StatusWaiting) || strings.TrimSpace(expectedHead) == "" || strings.TrimSpace(publishedHead) == "" || expectedHead == publishedHead || work.HeadCommit != publishedHead || work.BaseHeadCommit != publishedHead || work.ResultHeadCommit != publishedHead || work.PublicationState != "published" {
		return pullrequestwork.ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	matched, err := c.catalog.AdvancePublishedHeadInTransaction(ctx, tx, work.PullRequestID, expectedHead, publishedHead)
	if err != nil {
		return err
	}
	if !matched {
		return pullrequestwork.ErrStaleHead
	}
	if err = c.work.UpdateInTransaction(ctx, tx, work); err != nil {
		return err
	}
	if work.PublicationOperationID != "" {
		if _, err := c.catalog.CompleteOperationInTransaction(ctx, tx, work.PublicationOperationID, "succeeded", ""); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.catalog.NotifyPublicationCompletion(work.PullRequestID)
	return nil
}

func (c *CompletionCommitter) CommitCompletion(ctx context.Context, completion pullrequestwork.CompletionCommit) error {
	work := completion.Work
	expectedHead := completion.ExpectedSourceHead
	publishedHead := completion.ResultingHead
	if c == nil || c.db == nil || c.work == nil || c.catalog == nil || work.Status != pullrequestwork.StatusCompleted || work.PublicationState != "published" || strings.TrimSpace(expectedHead) == "" || strings.TrimSpace(publishedHead) == "" || (work.HeadCommit != expectedHead && work.PublicationTargetCommit != expectedHead) || work.ResultHeadCommit != publishedHead {
		return pullrequestwork.ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var targetBase, targetDiffBase *string
	if completion.Rebase != nil {
		targetBase = &completion.Rebase.TargetBaseCommit
		targetDiffBase = &completion.Rebase.TargetDiffBaseCommit
	}
	matched, err := c.catalog.CompleteHeadInTransaction(ctx, tx, work.PullRequestID, expectedHead, publishedHead, targetBase, targetDiffBase)
	if err != nil {
		return err
	}
	if !matched {
		return pullrequestwork.ErrStaleHead
	}
	if err = c.work.UpdateInTransaction(ctx, tx, work); err != nil {
		return err
	}
	if work.PublicationOperationID != "" {
		if _, err := c.catalog.CompleteOperationInTransaction(ctx, tx, work.PublicationOperationID, "succeeded", ""); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.catalog.NotifyActionCompletion(work.PullRequestID)
	return nil
}
