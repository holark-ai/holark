package sqliteadapter

import (
	"context"
	"database/sql"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/repository"
)

var ErrBranchChanged = repository.ErrBranchChanged

func CreatePublishedBranchSchema(ctx context.Context, db *sql.DB) error {
	return database.CreateSchema(ctx, db, `
 create table if not exists published_branches (
 repository text not null, ref text not null, commit_id text not null default '',
 ref_exists integer not null default 0, generation integer not null default 0,
 observation integer not null default 0, mutation_epoch integer not null default 0,
 owner text not null default '', primary key(repository,ref)
 );
 create table if not exists published_branch_sequence(id integer primary key check(id=1), value integer not null);
 insert or ignore into published_branch_sequence values(1,0);`)
}

func EnsureBranch(ctx context.Context, tx *sql.Tx, id repository.BranchIdentity) error {
	if !id.Valid() {
		return repository.ErrInvalidRepository
	}
	_, err := tx.ExecContext(ctx, `insert or ignore into published_branches(repository,ref) values(?,?)`, id.Repository, id.Ref)
	return err
}
func ReadBranch(ctx context.Context, tx *sql.Tx, id repository.BranchIdentity) (repository.PublishedBranch, string, error) {
	b := repository.PublishedBranch{Identity: id}
	var owner string
	err := tx.QueryRowContext(ctx, `select commit_id,ref_exists,generation,observation,mutation_epoch,owner from published_branches where repository=? and ref=?`, id.Repository, id.Ref).Scan(&b.Commit, &b.Exists, &b.Generation, &b.Observation, &b.MutationEpoch, &owner)
	return b, owner, err
}
func BeginBranchRead(ctx context.Context, tx *sql.Tx, ids []repository.BranchIdentity) (repository.BranchRead, error) {
	var read repository.BranchRead
	if err := tx.QueryRowContext(ctx, `update published_branch_sequence set value=value+1 where id=1 returning value`).Scan(&read.Sequence); err != nil {
		return read, err
	}
	for _, id := range ids {
		if err := EnsureBranch(ctx, tx, id); err != nil {
			return read, err
		}
		b, _, err := ReadBranch(ctx, tx, id)
		if err != nil {
			return read, err
		}
		read.Branches = append(read.Branches, b)
	}
	return read, nil
}
func AcceptBranchRead(ctx context.Context, tx *sql.Tx, before repository.PublishedBranch, sequence int64, observed repository.BranchObservation) (bool, error) {
	b, owner, err := ReadBranch(ctx, tx, before.Identity)
	if err != nil {
		return false, err
	}
	if owner != "" || b.MutationEpoch != before.MutationEpoch {
		return false, ErrBranchChanged
	}
	if sequence <= b.Observation {
		if observed.Commit == b.Commit && observed.Exists == b.Exists {
			return false, nil
		}
		return false, ErrBranchChanged
	}
	if observed.Exists && observed.Commit == "" || !observed.Exists && observed.Commit != "" {
		return false, repository.ErrRefNotFound
	}
	changed := b.Exists != observed.Exists || b.Commit != observed.Commit
	if changed {
		b.Generation++
	}
	_, err = tx.ExecContext(ctx, `update published_branches set commit_id=?,ref_exists=?,generation=?,observation=? where repository=? and ref=?`, observed.Commit, observed.Exists, b.Generation, sequence, b.Identity.Repository, b.Identity.Ref)
	return changed, err
}
func ClaimBranch(ctx context.Context, tx *sql.Tx, expected repository.PublishedBranch, owner string) error {
	b, held, err := ReadBranch(ctx, tx, expected.Identity)
	if err != nil {
		return err
	}
	if held != "" && held != owner || b.Generation != expected.Generation || b.Commit != expected.Commit || b.Exists != expected.Exists || b.MutationEpoch != expected.MutationEpoch {
		return ErrBranchChanged
	}
	if held == owner {
		return nil
	}
	_, err = tx.ExecContext(ctx, `update published_branches set owner=?,mutation_epoch=mutation_epoch+1 where repository=? and ref=?`, owner, b.Identity.Repository, b.Identity.Ref)
	return err
}
func ReleaseBranches(ctx context.Context, tx *sql.Tx, owner string) error {
	_, err := tx.ExecContext(ctx, `update published_branches set owner='',mutation_epoch=mutation_epoch+1 where owner=?`, owner)
	return err
}

// CompleteBranch never substitutes another accepted head during recovery.
func CompleteBranch(ctx context.Context, tx *sql.Tx, id repository.BranchIdentity, expected, published string) (bool, error) {
	b, _, err := ReadBranch(ctx, tx, id)
	if err != nil {
		return false, err
	}
	if b.Commit != expected && b.Commit != published {
		return false, ErrBranchChanged
	}
	if b.Commit == published && b.Exists {
		return false, nil
	}
	_, err = tx.ExecContext(ctx, `update published_branches set commit_id=?,ref_exists=1,generation=generation+1,mutation_epoch=mutation_epoch+1 where repository=? and ref=?`, published, id.Repository, id.Ref)
	return true, err
}
