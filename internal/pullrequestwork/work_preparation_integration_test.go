package pullrequestwork_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/pullrequestwork/sqliteadapter"
)

type preparingCatalog struct {
	recoveryCatalog
	preparations, syncs int
	err                 error
}

func (c *preparingCatalog) PrepareWork(context.Context, string) error {
	c.preparations++
	c.pr.BaseCommit, c.pr.HeadCommit = "prepared-base", "prepared-head"
	return c.err
}
func (c *preparingCatalog) SyncPullRequest(context.Context, string) error {
	c.syncs++
	return nil
}

func TestStartupPreparesThenReloadsAndRebaseStillSynchronizes(t *testing.T) {
	for _, mode := range []string{"continue", "queue", "rebase", "continue failure", "queue failure"} {
		t.Run(mode, func(t *testing.T) {
			db, err := database.Open(filepath.Join(t.TempDir(), "work.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			store, err := sqliteadapter.New(t.Context(), db)
			if err != nil {
				t.Fatal(err)
			}
			catalog := &preparingCatalog{recoveryCatalog: recoveryCatalog{pr: pullrequestwork.PullRequest{ID: "pr", Active: true, BaseCommit: "old-base", HeadCommit: "old-head"}}}
			failure := errors.New("preparation failed")
			if mode == "continue failure" || mode == "queue failure" {
				catalog.err = failure
			}
			launches := 0
			launcher := recoveryLaunchFunc(func(_ context.Context, p pullrequestwork.PullRequest, w pullrequestwork.Work, _ string) (string, error) {
				launches++
				if p.BaseCommit != "prepared-base" || p.HeadCommit != "prepared-head" || w.BaseCommit != p.BaseCommit || w.HeadCommit != p.HeadCommit {
					t.Errorf("launch used stale inputs: pr=%+v work=%+v", p, w)
				}
				return "session", nil
			})
			service := newTestWorkService(store, catalog, launcher, nil, pullrequestwork.NewRebaseCoordinator(catalog, nil))
			request := pullrequestwork.Start{PullRequestID: "pr", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, CommentIDs: []string{"comment"}}
			if mode == "continue" || mode == "continue failure" {
				request.Mode = pullrequestwork.ModeContinue
			}
			if mode == "rebase" {
				request.Kind = pullrequestwork.KindRebase
				request.Mode = ""
			}
			admitted, startErr := service.Start(t.Context(), request)
			err = startErr
			if request.Mode != pullrequestwork.ModeContinue {
				if err != nil || len(admitted) != 1 || admitted[0].Status != pullrequestwork.StatusQueued || catalog.preparations != 0 {
					t.Fatalf("admission=%+v error=%v preparations=%d", admitted, err, catalog.preparations)
				}
				err = service.Dispatch(t.Context())
			}
			wantSync := 0
			if mode == "rebase" {
				wantSync = 1
			}
			if catalog.preparations != 1 || catalog.syncs != wantSync {
				t.Fatalf("preparations=%d full sync=%d", catalog.preparations, catalog.syncs)
			}
			if catalog.err != nil {
				if !errors.Is(err, failure) || launches != 0 {
					t.Fatalf("startup failure=%v launches=%d", err, launches)
				}
				jobs, e := store.List(t.Context(), "pr")
				if e != nil {
					t.Fatal(e)
				}
				if mode == "continue failure" {
					if len(jobs) != 0 {
						t.Fatalf("Continue persisted before preparation: %+v", jobs)
					}
				} else {
					if len(jobs) != 1 || jobs[0].Status != pullrequestwork.StatusQueued || jobs[0].SessionID == "" {
						t.Fatalf("queue failure lost pending work: %+v", jobs)
					}
					catalog.err = nil
					if err = service.Dispatch(t.Context()); err != nil {
						t.Fatal(err)
					}
					if launches != 1 {
						t.Fatal("queue did not resume after preparation recovered")
					}
				}
			} else if err != nil || launches != 1 {
				t.Fatalf("startup=%v launches=%d", err, launches)
			}
		})
	}
}
