package sqliteadapter

import (
	"testing"

	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/repository"
)

func TestLifecycleChangeRevalidatesAcceptedComparison(t *testing.T) {
	for _, transition := range []struct{ from, to pr.Status }{
		{pr.StatusOpen, pr.StatusDraft},
		{pr.StatusDraft, pr.StatusOpen},
	} {
		t.Run(string(transition.from)+"_to_"+string(transition.to), func(t *testing.T) {
			store, db := operationCatalog(t)
			ctx := t.Context()
			p, err := store.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", SyncProvider: "github", SyncExternalID: "github:owner/repo#1", Status: transition.from, BaseBranch: "main", HeadBranch: "topic", SyncData: []byte(`{"github":{"base_repository_url":"https://github.com/owner/repo.git","head_repository_url":"https://github.com/fork/repo.git"}}`)})
			if err != nil {
				t.Fatal(err)
			}
			observe := func() {
				t.Helper()
				for _, branch := range []struct {
					id     repository.BranchIdentity
					commit string
				}{{p.BaseRef, "base"}, {p.HeadRef, "head"}} {
					read, err := store.BeginBranchObservation(ctx, []repository.BranchIdentity{branch.id})
					if err != nil {
						t.Fatal(err)
					}
					if err := store.AcceptBranchObservation(ctx, read, map[string]repository.BranchObservation{branch.id.Ref: {Commit: branch.commit, Exists: true}}); err != nil {
						t.Fatal(err)
					}
				}
			}
			observe()
			original, err := store.CaptureComparison(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AcceptComparison(ctx, p.ID, original, "ancestor"); err != nil {
				t.Fatal(err)
			}
			token, err := store.BeginObservation(ctx, "repo", []pr.FieldGroup{pr.LifecycleGroup, pr.TopologyGroup, pr.MetadataGroup})
			if err != nil {
				t.Fatal(err)
			}
			incoming := p
			incoming.Status = transition.to
			if _, _, err := store.UpsertObservedPullRequests("repo", []pr.PullRequest{incoming}, token); err != nil {
				t.Fatal(err)
			}
			current, _ := store.GetPullRequest(p.ID)
			if current.Status != transition.to || !current.HasCurrentComparison() || current.ComparisonState != pr.ComparisonReady || current.BaseCommit != "base" || current.HeadCommit != "head" || current.DiffBaseCommit != "ancestor" {
				t.Fatalf("lifecycle-only change must retain a revalidated comparison: %+v", current)
			}
			var projectionState string
			if err := db.QueryRow(`select comparison_state from pull_requests where id=?`, p.ID).Scan(&projectionState); err != nil || projectionState != string(pr.ComparisonReady) {
				t.Fatalf("relational comparison state = %q, %v", projectionState, err)
			}
			if err := store.AcceptComparison(ctx, p.ID, original, "ancestor"); err == nil {
				t.Fatal("accepted comparison prepared before lifecycle change")
			}
			fresh, err := store.CaptureComparison(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !pr.SameComparisonVersion(current.Comparison.Inputs, fresh) {
				t.Fatalf("lifecycle change left a ready comparison with an obsolete version: snapshot=%+v current=%+v", current.Comparison.Inputs, fresh)
			}
			var storedLifecycle int64
			if err := db.QueryRow(`select json_extract(comparison_snapshot,'$.inputs.lifecycle_generation') from pull_requests where id=?`, p.ID).Scan(&storedLifecycle); err != nil || storedLifecycle != fresh.LifecycleGeneration {
				t.Fatalf("relational snapshot lifecycle = %d, %v; want %d", storedLifecycle, err, fresh.LifecycleGeneration)
			}
			if fresh.Base.Generation != original.Base.Generation || fresh.Head.Generation != original.Head.Generation {
				t.Fatal("lifecycle change changed branch generations")
			}
			if err := store.AcceptComparison(ctx, p.ID, fresh, "ancestor"); err != nil {
				t.Fatal(err)
			}
			current, _ = store.GetPullRequest(p.ID)
			if !current.HasCurrentComparison() || !pr.SameComparisonVersion(current.Comparison.Inputs, fresh) {
				t.Fatalf("fresh comparison was not accepted: %+v", current)
			}
			observe()
			current, _ = store.GetPullRequest(p.ID)
			if !current.HasCurrentComparison() || !pr.SameComparisonVersion(current.Comparison.Inputs, fresh) {
				t.Fatal("unchanged observation invalidated completed comparison")
			}
		})
	}
}
