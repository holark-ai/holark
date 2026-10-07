package pullrequestwork_test

import (
	"errors"
	"github.com/holark-ai/holark/internal/testfixture/branchfixture"
	"testing"

	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/pullrequestwork/sqliteadapter"
)

func TestPullRequestCancellationPreservesPublicationCheckpoints(t *testing.T) {
	for _, complete := range []bool{false, true} {
		name := "incomplete"
		if complete {
			name = "complete"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			db, store := mixedQueueStore(t)
			if _, err := db.ExecContext(ctx, "create table repositories(id text primary key); insert into repositories(id) values('repo')"); err != nil {
				t.Fatal(err)
			}
			prs, err := prsqlite.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = branchfixture.Create(ctx, prs, pullrequestlifecycle.PullRequest{ID: "pr", RepositoryID: "repo", Status: pullrequestlifecycle.StatusMerged, HeadCommit: "head"}); err != nil {
				t.Fatal(err)
			}
			checkpoint := pullrequestwork.Work{ID: "published", PullRequestID: "pr", Kind: pullrequestwork.KindRebase, Status: pullrequestwork.StatusRunning, HeadCommit: "head", PublicationState: "published"}
			if complete {
				checkpoint.ResultHeadCommit = "published-head"
			}
			if err = store.CreateBatch(ctx, []pullrequestwork.Work{checkpoint, {ID: "queued", PullRequestID: "pr", Kind: pullrequestwork.KindRebase, Status: pullrequestwork.StatusQueued}}); err != nil {
				t.Fatal(err)
			}
			// Any attempt to publish or dispatch work would call an absent dependency.
			service := newTestWorkService(store, nil, nil, nil)
			service.SetCompletionCommitter(sqliteadapter.NewCompletionCommitter(store, prs))
			for attempt := 0; attempt < 2; attempt++ {
				err := service.CancelPullRequest(ctx, "pr")
				if complete && err != nil {
					t.Fatal(err)
				}
				if !complete && !errors.Is(err, pullrequestwork.ErrPublish) {
					t.Fatalf("checkpoint error = %v", err)
				}
				got, err := store.Get(ctx, checkpoint.ID)
				if err != nil {
					t.Fatal(err)
				}
				want := pullrequestwork.StatusRunning
				if complete {
					want = pullrequestwork.StatusCompleted
				}
				if got.Status != want || got.PublicationState != "published" || got.ResultHeadCommit != checkpoint.ResultHeadCommit {
					t.Fatalf("checkpoint lost: %+v", got)
				}
				queued, err := store.Get(ctx, "queued")
				if err != nil || queued.Status != pullrequestwork.StatusCancelled || queued.SessionID != "" {
					t.Fatalf("queued work dispatched: %+v, %v", queued, err)
				}
			}
			pr, _ := prs.GetPullRequest("pr")
			if pr.Status != pullrequestlifecycle.StatusMerged || pr.HeadCommit != "head" {
				t.Fatalf("PR checkpoint = %+v", pr)
			}
		})
	}
}
