package sqliteadapter

import (
	"context"
	"database/sql"
	"errors"

	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/repository"
	branches "github.com/holark-ai/holark/internal/repository/sqliteadapter"
)

func (s *Store) BeginBranchObservation(ctx context.Context, ids []repository.BranchIdentity) (repository.BranchRead, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return repository.BranchRead{}, err
	}
	defer tx.Rollback()
	read, err := branches.BeginBranchRead(ctx, tx, ids)
	if err != nil {
		return read, err
	}
	return read, tx.Commit()
}
func (s *Store) AcceptBranchObservation(ctx context.Context, read repository.BranchRead, observed map[string]repository.BranchObservation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rejected error
	for _, b := range read.Branches {
		value, ok := observed[b.Identity.Ref]
		if !ok {
			rejected = errors.Join(rejected, repository.ErrRefNotFound)
			continue
		}
		changed, err := branches.AcceptBranchRead(ctx, tx, b, read.Sequence, value)
		if errors.Is(err, repository.ErrBranchChanged) {
			rejected = errors.Join(rejected, err)
			continue
		}
		if err != nil {
			return err
		}
		if changed {
			if err := invalidateBranch(ctx, tx, b.Identity); err != nil {
				return err
			}
		}
	}
	return errors.Join(rejected, tx.Commit())
}

func invalidateBranch(ctx context.Context, tx *sql.Tx, id repository.BranchIdentity) error {
	rows, err := tx.QueryContext(ctx, `select repository_id,document from pull_request_catalog where (json_extract(document,'$.base_ref.repository')=? and json_extract(document,'$.base_ref.ref')=?) or (json_extract(document,'$.head_ref.repository')=? and json_extract(document,'$.head_ref.ref')=?)`, id.Repository, id.Ref, id.Repository, id.Ref)
	if err != nil {
		return err
	}
	var values []pr.PullRequest
	for rows.Next() {
		var repo, raw string
		if err = rows.Scan(&repo, &raw); err != nil {
			break
		}
		p, e := decodePullRequest(repo, raw)
		if e != nil {
			err = e
			break
		}
		if p.Status.Active() {
			values = append(values, p)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range values {
		invalidateComparison(&p)
		if err := saveInTransaction(ctx, tx, &p); err != nil {
			return err
		}
	}
	return nil
}
func invalidateComparison(p *pr.PullRequest) {
	p.ComparisonState = pr.ComparisonUnavailable
	if p.Comparison != nil {
		p.ComparisonState = pr.ComparisonStale
	}
	p.Mergeable = nil
}
func captureComparison(ctx context.Context, tx *sql.Tx, p pr.PullRequest) (pr.ComparisonInputs, error) {
	inputs := pr.ComparisonInputs{LifecycleGeneration: p.LifecycleGeneration, RelationshipGeneration: p.RelationshipGeneration}
	if !p.Status.Active() || !p.BaseRef.Valid() || !p.HeadRef.Valid() {
		return inputs, pr.ErrComparisonUnavailable
	}
	var owner string
	var err error
	inputs.Base, owner, err = branches.ReadBranch(ctx, tx, p.BaseRef)
	if err != nil {
		return inputs, err
	}
	if owner != "" {
		return inputs, pr.ErrOperationInProgress
	}
	inputs.Head, owner, err = branches.ReadBranch(ctx, tx, p.HeadRef)
	if err != nil {
		return inputs, err
	}
	if owner != "" {
		return inputs, pr.ErrOperationInProgress
	}
	if !inputs.Base.Exists || !inputs.Head.Exists || inputs.Base.Commit == "" || inputs.Head.Commit == "" {
		return inputs, repository.ErrRefNotFound
	}
	return inputs, nil
}
func (s *Store) CaptureComparison(ctx context.Context, id string) (pr.ComparisonInputs, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return pr.ComparisonInputs{}, err
	}
	defer tx.Rollback()
	p, err := getInTransaction(ctx, tx, id)
	if err != nil {
		return pr.ComparisonInputs{}, err
	}
	return captureComparison(ctx, tx, p)
}
func sameBranchVersion(a, b repository.PublishedBranch) bool {
	return a.Identity == b.Identity && a.Generation == b.Generation && a.MutationEpoch == b.MutationEpoch && a.Commit == b.Commit && a.Exists == b.Exists
}
func (s *Store) AcceptComparison(ctx context.Context, id string, inputs pr.ComparisonInputs, mergeBase string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := getInTransaction(ctx, tx, id)
	if err != nil {
		return err
	}
	now, err := captureComparison(ctx, tx, p)
	if err != nil {
		return err
	}
	if mergeBase == "" || now.LifecycleGeneration != inputs.LifecycleGeneration || now.RelationshipGeneration != inputs.RelationshipGeneration || !sameBranchVersion(now.Base, inputs.Base) || !sameBranchVersion(now.Head, inputs.Head) {
		return pr.ErrComparisonUnavailable
	}
	p.Comparison = &pr.ComparisonSnapshot{Inputs: inputs, MergeBase: mergeBase}
	p.ComparisonState = pr.ComparisonReady
	p.BaseCommit, p.HeadCommit, p.DiffBaseCommit = inputs.Base.Commit, inputs.Head.Commit, mergeBase
	if err := saveInTransaction(ctx, tx, &p); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) InvalidateComparison(ctx context.Context, id string) error {
	_, err := s.mutate(id, func(p *pr.PullRequest) error { invalidateComparison(p); return nil })
	return err
}

// bindComparisonReferences records relationships, never provider commit tips.
func bindComparisonReferences(ctx context.Context, tx *sql.Tx, p *pr.PullRequest, previous pr.PullRequest) error {
	source := pr.DecodeGitHubObservation(p.SyncData)
	if p.SyncExternalID != "" {
		p.BaseRef = repository.PublishedBranchIdentity(source.BaseRepositoryURL, p.BaseBranch)
		p.HeadRef = repository.PublishedBranchIdentity(source.HeadRepositoryURL, p.HeadBranch)
	}
	for _, id := range []repository.BranchIdentity{p.BaseRef, p.HeadRef} {
		if id.Valid() {
			if err := branches.EnsureBranch(ctx, tx, id); err != nil {
				return err
			}
		}
	}
	p.RelationshipGeneration = previous.RelationshipGeneration
	if p.BaseRef != previous.BaseRef || p.HeadRef != previous.HeadRef || p.Status.Active() && !previous.Status.Active() {
		p.RelationshipGeneration++
		if previous.ID != "" || p.Comparison == nil {
			invalidateComparison(p)
		} else {
			p.Comparison.Inputs.RelationshipGeneration = p.RelationshipGeneration
		}
	}
	if p.Comparison != nil {
		p.BaseCommit, p.HeadCommit, p.DiffBaseCommit = p.Comparison.Inputs.Base.Commit, p.Comparison.Inputs.Head.Commit, p.Comparison.MergeBase
	}
	return nil
}

func claimOperationBranches(ctx context.Context, tx *sql.Tx, operation *pr.Operation, current pr.PullRequest) error {
	if operation.Kind != "publish" && operation.Kind != "publication" && operation.Kind != "work_publish" && operation.Kind != "rebase" && operation.Kind != "merge" {
		return nil
	}
	if !current.HasCurrentComparison() {
		return pr.ErrComparisonUnavailable
	}
	inputs, err := captureComparison(ctx, tx, current)
	if err != nil {
		return err
	}
	if !sameBranchVersion(inputs.Base, current.Comparison.Inputs.Base) || !sameBranchVersion(inputs.Head, current.Comparison.Inputs.Head) {
		return pr.ErrComparisonUnavailable
	}
	operation.BranchInputs = &inputs
	protected := []repository.PublishedBranch{inputs.Head}
	if operation.Kind == "merge" && inputs.Base.Identity != inputs.Head.Identity {
		protected = append(protected, inputs.Base)
	}
	for _, b := range protected {
		if err := branches.ClaimBranch(ctx, tx, b, operation.RequestID); err != nil {
			return pr.ErrOperationInProgress
		}
		if err := invalidateBranch(ctx, tx, b.Identity); err != nil {
			return err
		}
	}
	return nil
}

func completeCreatedBranches(ctx context.Context, tx *sql.Tx, p *pr.PullRequest, operation pr.Operation) error {
	preparation := operation.Steps["preparation"].Creation
	if preparation == nil || preparation.BranchRead == nil {
		return nil
	}
	for _, b := range preparation.BranchRead.Branches {
		if b.Identity == p.HeadRef {
			if _, err := branches.CompleteBranch(ctx, tx, b.Identity, b.Commit, p.HeadCommit); err != nil {
				return err
			}
			if err := invalidateBranch(ctx, tx, b.Identity); err != nil {
				return err
			}
		} else if b.Identity == p.BaseRef {
			changed, err := branches.AcceptBranchRead(ctx, tx, b, preparation.BranchRead.Sequence, repository.BranchObservation{Commit: p.BaseCommit, Exists: true})
			// A later accepted base is independent of this successful publication.
			if err != nil && err != branches.ErrBranchChanged {
				return err
			}
			if changed {
				if err := invalidateBranch(ctx, tx, b.Identity); err != nil {
					return err
				}
			}
		}
	}
	base, _, err := branches.ReadBranch(ctx, tx, p.BaseRef)
	if err != nil {
		return err
	}
	head, _, err := branches.ReadBranch(ctx, tx, p.HeadRef)
	if err != nil {
		return err
	}
	if base.Commit == p.BaseCommit && head.Commit == p.HeadCommit && p.DiffBaseCommit != "" {
		p.Comparison = &pr.ComparisonSnapshot{Inputs: pr.ComparisonInputs{Base: base, Head: head}, MergeBase: p.DiffBaseCommit}
		p.ComparisonState = pr.ComparisonReady
	}
	return nil
}

func completeSharedHead(ctx context.Context, tx *sql.Tx, p *pr.PullRequest, expected, published string) (bool, error) {
	changed, err := branches.CompleteBranch(ctx, tx, p.HeadRef, expected, published)
	if errors.Is(err, repository.ErrBranchChanged) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if changed {
		if err := invalidateBranch(ctx, tx, p.HeadRef); err != nil {
			return false, err
		}
	}
	if !p.Status.Active() {
		return true, nil
	}
	// Preserve the last complete display until its immutable pair is prepared.
	invalidateComparison(p)
	if err := saveInTransaction(ctx, tx, p); err != nil {
		return false, err
	}
	return true, nil
}
