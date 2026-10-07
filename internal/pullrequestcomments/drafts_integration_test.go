//go:build integration

package pullrequestcomments_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/holark-ai/holark/internal/pullrequestcomments"
	"github.com/holark-ai/holark/internal/pullrequestcomments/sqliteadapter"
)

func TestPublishDraftsRecoversAcceptedBatchAfterPullRequestChanges(t *testing.T) {
	for _, state := range []pullrequestcomments.PublicationState{pullrequestcomments.PublicationPending, pullrequestcomments.PublicationPublished} {
		for _, change := range []struct {
			name              string
			head              string
			status            pullrequestcomments.PullRequestStatus
			comparisonCurrent bool
			want              error
		}{
			{"head advanced", "new-head", pullrequestcomments.PullRequestOpen, true, pullrequestcomments.ErrStaleDraft},
			{"comparison stale with previous head", "head", pullrequestcomments.PullRequestOpen, false, pullrequestcomments.ErrComparisonNotReady},
			{"closed", "head", pullrequestcomments.PullRequestClosed, true, pullrequestcomments.ErrReadOnly},
			{"merged", "head", pullrequestcomments.PullRequestMerged, true, pullrequestcomments.ErrReadOnly},
		} {
			t.Run(string(state)+"/"+change.name, func(t *testing.T) {
				ctx := t.Context()
				store, err := sqliteadapter.New(ctx, openCommentsDB(t))
				if err != nil {
					t.Fatal(err)
				}
				targets := providerTargets()
				target := targets.targets["pr-one"]
				target.ComparisonCurrent = true
				targets.targets["pr-one"] = target
				gateway := &lifecycleGateway{}
				service := pullrequestcomments.NewService(store, targets, pullrequestcomments.WithProviderGateway(gateway, nil))
				drafts, err := service.CreateReviewComments(ctx, pullrequestcomments.CreateReviewComments{
					PullRequestID: "pr-one", Draft: true, Origin: pullrequestcomments.ReviewOrigin("session", "review", "head"),
					Comments: []pullrequestcomments.ReviewComment{
						{Body: "First finding", Scope: pullrequestcomments.ScopePullRequest},
						{Body: "Second finding", Scope: pullrequestcomments.ScopePullRequest},
						{Body: "Unselected finding", Scope: pullrequestcomments.ScopePullRequest},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				ids := []string{drafts[0].ID, drafts[1].ID}
				// Discard the accepted response, as if it never reached the client.
				if _, err := service.PublishDrafts(ctx, "pr-one", "head", ids); err != nil {
					t.Fatal(err)
				}
				if state == pullrequestcomments.PublicationPublished {
					publishComments(t, service)
				}
				before, err := service.ListByPullRequest(ctx, "pr-one")
				if err != nil {
					t.Fatal(err)
				}
				jobs, err := store.PendingPublications(ctx, "pr-one")
				if err != nil {
					t.Fatal(err)
				}
				for _, comment := range before {
					wantState := state
					if comment.ID == drafts[2].ID {
						wantState = pullrequestcomments.PublicationDraft
					}
					if comment.PublicationState != wantState {
						t.Fatalf("comment state = %s, want %s", comment.PublicationState, wantState)
					}
				}

				target.HeadCommit, target.Status = change.head, change.status
				target.ComparisonCurrent = change.comparisonCurrent
				targets.targets["pr-one"] = target
				service = pullrequestcomments.NewService(store, targets, pullrequestcomments.WithProviderGateway(gateway, nil))
				after, err := service.PublishDrafts(ctx, "pr-one", "head", ids)
				if err != nil || !reflect.DeepEqual(after, before) {
					t.Fatalf("accepted batch recovery: comments=%+v err=%v", after, err)
				}

				// Neither a new batch nor a mixed selection may publish stale drafts.
				for _, selection := range [][]string{{drafts[2].ID}, {drafts[0].ID, drafts[2].ID}} {
					if _, err := service.PublishDrafts(ctx, "pr-one", "head", selection); !errors.Is(err, change.want) {
						t.Fatalf("unaccepted selection: err=%v, want %v", err, change.want)
					}
				}
				if change.head != "head" {
					if _, err := service.PublishDrafts(ctx, "pr-one", change.head, []string{drafts[2].ID}); !errors.Is(err, pullrequestcomments.ErrStaleDraft) {
						t.Fatalf("old draft with current head: %v", err)
					}
				}
				if _, err := service.PublishDrafts(ctx, "pr-two", "head", ids); !errors.Is(err, pullrequestcomments.ErrInvalidComment) {
					t.Fatalf("accepted comments from another PR: %v", err)
				}
				after, err = service.ListByPullRequest(ctx, "pr-one")
				if err != nil || !reflect.DeepEqual(after, before) {
					t.Fatalf("retry changed comments: %+v err=%v", after, err)
				}
				afterJobs, err := store.PendingPublications(ctx, "pr-one")
				if err != nil || !reflect.DeepEqual(afterJobs, jobs) {
					t.Fatalf("retry changed publication jobs: %+v err=%v", afterJobs, err)
				}
			})
		}
	}
}
