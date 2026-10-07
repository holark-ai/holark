package pullrequestwork_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/pullrequestwork/sqliteadapter"
)

type conflictedRebaser struct{}

func (conflictedRebaser) Rebase(context.Context, pullrequestwork.PullRequest, string, string) (pullrequestwork.RebasePreparation, error) {
	return pullrequestwork.RebasePreparation{Conflicts: true}, nil
}

func TestAddressQueuesUntilActiveRebaseSettles(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(filepath.Join(t.TempDir(), "work.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := sqliteadapter.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	catalog := recoveryCatalog{pr: pullrequestwork.PullRequest{ID: "pr", BaseCommit: "base", HeadCommit: "head", Active: true}}
	var launches []pullrequestwork.Work
	launcher := recoveryLaunchFunc(func(_ context.Context, _ pullrequestwork.PullRequest, w pullrequestwork.Work, _ string) (string, error) {
		launches = append(launches, w)
		return "session-" + w.ID, nil
	})
	service := newTestWorkService(store, catalog, launcher, nil, conflictedRebaser{})
	rebases, err := service.Start(ctx, pullrequestwork.Start{PullRequestID: "pr", Kind: pullrequestwork.KindRebase})
	if err != nil || len(rebases) != 1 || rebases[0].Status != pullrequestwork.StatusQueued || len(launches) != 0 {
		t.Fatalf("start rebase: works=%+v launches=%d err=%v", rebases, len(launches), err)
	}

	if err := service.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	var queued []pullrequestwork.Work
	for _, status := range []pullrequestwork.Status{pullrequestwork.StatusRunning, pullrequestwork.StatusWaiting} {
		if status == pullrequestwork.StatusWaiting {
			if err := service.Wait(ctx, rebases[0].ID, "Resolve conflicts"); err != nil {
				t.Fatal(err)
			}
		}
		works, err := service.Start(ctx, pullrequestwork.Start{PullRequestID: "pr", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, CommentIDs: []string{"comment-" + string(status)}})
		if err != nil || len(works) != 1 {
			t.Fatalf("admit Address during %s rebase: works=%+v err=%v", status, works, err)
		}
		if err := service.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		work, err := store.Get(ctx, works[0].ID)
		if err != nil || work.Status != pullrequestwork.StatusQueued || work.SessionID != "" || work.HeadCommit != "" || len(launches) != 1 {
			t.Fatalf("Address during %s rebase: work=%+v launches=%d err=%v", status, work, len(launches), err)
		}
		queued = append(queued, work)
	}

	if err := service.Fail(ctx, rebases[0].ID, "Rebase stopped"); err != nil {
		t.Fatal(err)
	}
	if err := service.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := store.Get(ctx, queued[0].ID)
	if err != nil || first.Status != pullrequestwork.StatusRunning || first.HeadCommit != "head" || len(launches) != 2 || launches[1].ID != first.ID {
		t.Fatalf("Address after rebase settles: work=%+v launches=%+v err=%v", first, launches, err)
	}
	second, err := store.Get(ctx, queued[1].ID)
	if err != nil || second.Status != pullrequestwork.StatusQueued {
		t.Fatalf("second Address must stay queued: work=%+v err=%v", second, err)
	}
}
