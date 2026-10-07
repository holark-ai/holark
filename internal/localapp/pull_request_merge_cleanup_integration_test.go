package localapp_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestmerge"
)

func TestPullRequestMergeAndSyncEndOriginalActivity(t *testing.T) {
	for _, trigger := range []string{"merge", "sync", "close", "background merge", "background close"} {
		t.Run(trigger, func(t *testing.T) {
			fixture := newCreationFixture(t)
			fixture.addRemote(t)
			created := fixture.create(t, pullrequestlifecycle.StatusOpen)
			fixture.complete(t, fixture.metadata(t, created.ID))
			pr := fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusOpen)
			before, err := fixture.scenario.Application.Holons.Get(t.Context(), fixture.scenario.State.HolonID)
			if err != nil {
				t.Fatal(err)
			}
			switch trigger {
			case "merge":
				fixture.request(t, http.MethodPost, "/api/v1/pull-requests/"+pr.ID+"/sync", `{}`, http.StatusNoContent, nil)
				fixture.request(t, http.MethodPost, "/api/v1/pull-requests/"+pr.ID+"/github-readiness", `{}`, http.StatusOK, nil)
				fixture.request(t, http.MethodPost, "/api/v1/pull-requests/"+pr.ID+"/merge", `{"strategy":"squash"}`, http.StatusOK, nil)
				fixture.await(t, func() bool {
					h, err := fixture.scenario.Application.Holons.Get(t.Context(), fixture.scenario.State.HolonID)
					return err == nil && h.ArchivedAt != nil
				})
			case "sync":
				// Merge externally, then exercise the real application's GitHub sync hook.
				if _, err := fixture.scenario.GitHub.Squash(t.Context(), pullrequestmerge.GitHubMergeRequest{ExpectedHeadSHA: pr.HeadCommit, Title: pr.Title}); err != nil {
					t.Fatal(err)
				}
				path := "/api/v1/pull-requests/" + pr.ID + "/sync"
				expected := http.StatusOK
				if strings.HasSuffix(path, "/sync") {
					expected = http.StatusNoContent
				}
				fixture.request(t, http.MethodPost, path, `{}`, expected, nil)
			case "background merge", "background close":
				if trigger == "background merge" {
					if _, err := fixture.scenario.GitHub.Squash(t.Context(), pullrequestmerge.GitHubMergeRequest{ExpectedHeadSHA: pr.HeadCommit, Title: pr.Title}); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := fixture.scenario.GitHub.UpdateState(t.Context(), pullrequestlifecycle.GitHubPullRequestTarget{ExternalID: pr.SyncExternalID}, "closed"); err != nil {
						t.Fatal(err)
					}
				}
				// No sync or PR-page request: the application reconciles and retires.
				fixture.await(t, func() bool {
					h, err := fixture.scenario.Application.Holons.Get(t.Context(), fixture.scenario.State.HolonID)
					return err == nil && h.ArchivedAt != nil
				})
			case "close":
				fixture.request(t, http.MethodPost, "/api/v1/pull-requests/"+pr.ID+"/transition", `{"status":"closed"}`, http.StatusOK, nil)
			}
			for attempt := 0; attempt < 2; attempt++ {
				h, err := fixture.scenario.Application.Holons.Get(t.Context(), fixture.scenario.State.HolonID)
				want := before.Status
				wantArchived := true
				if err != nil || h.Status != want || (h.ArchivedAt != nil) != wantArchived || h.WorktreePath == "" {
					t.Fatalf("original session = %+v, %v", h, err)
				}
				// Retrying sync must remain successful after every associated session stopped.
				fixture.request(t, http.MethodPost, "/api/v1/pull-requests/"+pr.ID+"/sync", `{}`, http.StatusNoContent, nil)
			}
		})
	}
}
