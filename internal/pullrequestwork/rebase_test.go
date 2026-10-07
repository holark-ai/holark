package pullrequestwork

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type rebaseRepositoryStub struct {
	pr    PullRequest
	calls []string
	err   error
}

func (r *rebaseRepositoryStub) SyncPullRequest(context.Context, string) error {
	r.calls = append(r.calls, "sync")
	return r.err
}
func (r *rebaseRepositoryStub) PullRequest(string) (PullRequest, bool) {
	r.calls = append(r.calls, "get")
	return r.pr, r.pr.ID != ""
}

type pinnedRebaserStub struct {
	result                 RebasePreparation
	err                    error
	calls                  int
	pr                     PullRequest
	baseCommit, headCommit string
}

type pinnedPreviewStub struct {
	result                 RebasePreview
	err                    error
	calls                  int
	pr                     PullRequest
	baseCommit, headCommit string
}

func (preview *pinnedPreviewStub) PreviewRebasePinned(_ context.Context, pr PullRequest, baseCommit, headCommit string) (RebasePreview, error) {
	preview.calls++
	preview.pr, preview.baseCommit, preview.headCommit = pr, baseCommit, headCommit
	return preview.result, preview.err
}

func TestRebaseReadinessPinsCommitsAndProjectsPreview(t *testing.T) {
	tests := []struct {
		name      string
		preview   RebasePreview
		freshness BranchFreshness
		conflicts RebaseConflictState
	}{
		{name: "up to date", preview: RebasePreview{UpToDate: true}, freshness: BranchUpToDate, conflicts: RebaseNotApplicable},
		{name: "clean", preview: RebasePreview{BaseCommitsAhead: 3}, freshness: BranchNotUpToDate, conflicts: RebaseClean},
		{name: "conflicting", preview: RebasePreview{Conflicts: true, BaseCommitsAhead: 3}, freshness: BranchNotUpToDate, conflicts: RebaseConflicting},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := PullRequest{ID: "pr-1", BaseBranch: "main", HeadBranch: "feature", HeadCommit: "live-head", BaseCommit: "live-base", Active: true}
			repository := &rebaseRepositoryStub{pr: original}
			preview := &pinnedPreviewStub{result: test.preview}
			readiness, err := NewRebaseCoordinator(repository, nil, preview).RebaseReadiness(t.Context(), original)
			if err != nil {
				t.Fatal(err)
			}
			if readiness.BaseCommit != "live-base" || readiness.HeadCommit != "live-head" || readiness.BaseCommitsAhead != test.preview.BaseCommitsAhead || readiness.BranchFreshness != test.freshness || readiness.RebaseConflictState != test.conflicts {
				t.Fatalf("readiness=%+v", readiness)
			}
			if preview.calls != 1 || preview.pr != original || preview.baseCommit != "live-base" || preview.headCommit != "live-head" {
				t.Fatalf("preview=%+v", preview)
			}
		})
	}
}

func TestRebaseReadinessRejectsInactiveStaleAndRepositoryFailure(t *testing.T) {
	active := PullRequest{ID: "pr-1", BaseBranch: "main", HeadBranch: "feature", HeadCommit: "stored-head", Active: true}
	tests := []struct {
		name        string
		pullRequest PullRequest
		repository  *rebaseRepositoryStub
		want        error
	}{
		{name: "inactive", pullRequest: PullRequest{ID: "pr-1"}, repository: &rebaseRepositoryStub{}, want: ErrPullRequestInactive},
		{name: "stale", pullRequest: active, repository: &rebaseRepositoryStub{err: ErrStaleHead}, want: ErrStaleHead},
		{name: "repository failure", pullRequest: active, repository: &rebaseRepositoryStub{err: errors.New("fetch failed")}, want: ErrRebaseReadinessUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preview := &pinnedPreviewStub{}
			_, err := NewRebaseCoordinator(test.repository, nil, preview).RebaseReadiness(t.Context(), test.pullRequest)
			if test.name == "repository failure" {
				if err == nil || err.Error() != "fetch failed" {
					t.Fatalf("error=%v", err)
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			if preview.calls != 0 {
				t.Fatalf("preview calls=%d", preview.calls)
			}
		})
	}
}

func (r *pinnedRebaserStub) RebasePinned(_ context.Context, pr PullRequest, baseCommit, headCommit, operationID string) (RebasePreparation, error) {
	r.calls++
	r.pr, r.baseCommit, r.headCommit = pr, baseCommit, headCommit
	return r.result, r.err
}

func TestRebaseCoordinatorRefreshesReconcilesAndPinsDirectRebase(t *testing.T) {
	original := PullRequest{ID: "pr-1", BaseBranch: "main", HeadBranch: "feature", HeadCommit: "cached-head", Active: true}
	reconciled := original
	reconciled.HeadCommit, reconciled.BaseCommit = "live-head", "live-base"
	repository := &rebaseRepositoryStub{pr: reconciled}
	direct := &pinnedRebaserStub{result: RebasePreparation{HeadCommit: "rebased-head", Rebased: true}}

	prepared, err := NewRebaseCoordinator(repository, direct).Rebase(t.Context(), original, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repository.calls, []string{"sync", "get"}) {
		t.Fatalf("repository calls=%v", repository.calls)
	}

	if direct.calls != 1 || direct.pr.HeadCommit != "live-head" || direct.baseCommit != "live-base" || direct.headCommit != "live-head" {
		t.Fatalf("direct=%+v", direct)
	}
	if prepared.PullRequest.HeadCommit != "live-head" || prepared.HeadCommit != "rebased-head" || prepared.TargetBaseCommit != "live-base" || prepared.TargetDiffBaseCommit != "live-base" || !prepared.Rebased {
		t.Fatalf("prepared=%+v", prepared)
	}
}

func TestRebaseCoordinatorStopsWhenPreviewedBaseMoved(t *testing.T) {
	original := PullRequest{ID: "pr-1", BaseBranch: "main", HeadBranch: "feature", HeadCommit: "head", Active: true}
	repository := &rebaseRepositoryStub{pr: PullRequest{ID: original.ID, BaseCommit: "new-base", HeadCommit: "head", Active: true}}
	direct := &pinnedRebaserStub{}

	_, err := NewRebaseCoordinator(repository, direct).Rebase(t.Context(), original, "previewed-base", "")
	if !errors.Is(err, ErrRebaseTargetChanged) {
		t.Fatalf("error=%v", err)
	}
	if !reflect.DeepEqual(repository.calls, []string{"sync", "get"}) || direct.calls != 0 {
		t.Fatalf("repository calls=%v direct calls=%d", repository.calls, direct.calls)
	}
}

func TestRebaseCoordinatorRejectsFailedSynchronizationBeforeDirectRebase(t *testing.T) {
	original := PullRequest{ID: "pr-1", BaseBranch: "main", HeadBranch: "feature", HeadCommit: "cached-head", Active: true}
	repository := &rebaseRepositoryStub{err: ErrStaleHead}
	direct := &pinnedRebaserStub{}

	_, err := NewRebaseCoordinator(repository, direct).Rebase(t.Context(), original, "", "")
	if !errors.Is(err, ErrStaleHead) || direct.calls != 0 {
		t.Fatalf("error=%v direct calls=%d", err, direct.calls)
	}
}

func TestRebaseCoordinatorRejectsInactiveReconciledPullRequest(t *testing.T) {
	original := PullRequest{ID: "pr-1", BaseBranch: "main", HeadBranch: "feature", HeadCommit: "head", Active: true}
	repository := &rebaseRepositoryStub{pr: PullRequest{ID: original.ID, BaseCommit: "base", HeadCommit: "head", Active: false}}
	direct := &pinnedRebaserStub{}

	_, err := NewRebaseCoordinator(repository, direct).Rebase(t.Context(), original, "", "")
	if !errors.Is(err, ErrStaleHead) || direct.calls != 0 {
		t.Fatalf("error=%v direct calls=%d", err, direct.calls)
	}
}

func TestRebaseCoordinatorReturnsRemoteHeadRaceAsStale(t *testing.T) {
	original := PullRequest{ID: "pr-1", BaseBranch: "main", HeadBranch: "feature", HeadCommit: "head", Active: true}
	repository := &rebaseRepositoryStub{pr: PullRequest{ID: original.ID, BaseCommit: "base", HeadCommit: "head", Active: true}}
	direct := &pinnedRebaserStub{err: ErrStaleHead}

	_, err := NewRebaseCoordinator(repository, direct).Rebase(t.Context(), original, "", "")
	if !errors.Is(err, ErrStaleHead) || direct.calls != 1 {
		t.Fatalf("error=%v direct calls=%d", err, direct.calls)
	}
}
