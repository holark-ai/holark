package sqliteadapter

import (
	"errors"
	"testing"

	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

func TestCreationIdentityAndIntentCommitTogether(t *testing.T) {
	ctx := t.Context()
	catalog, db := operationCatalog(t)
	if _, _, err := catalog.BeginOperation(ctx, pr.Operation{RequestID: "create", HolonID: "holon", Kind: "create", Groups: []pr.FieldGroup{pr.LifecycleGroup, pr.TopologyGroup}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create trigger reject_binding before update on pull_request_operations when new.pull_request_id != '' begin select raise(abort,'binding failed'); end`); err != nil {
		t.Fatal(err)
	}
	input := pr.PullRequest{RepositoryID: "repo", Status: pr.StatusWIP, LinkedHolonIDs: []string{"holon"}}
	if _, err := catalog.CreateOperationPullRequest(ctx, "create", input); err == nil {
		t.Fatal("binding failure succeeded")
	}
	if items := catalog.ListPullRequests("repo"); len(items) != 0 {
		t.Fatalf("unowned creation became visible: %#v", items)
	}
	if _, err := db.Exec(`drop trigger reject_binding`); err != nil {
		t.Fatal(err)
	}
	current, err := catalog.CreateOperationPullRequest(ctx, "create", input)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Operations) != 1 || current.Operations[0].RequestID != "create" {
		t.Fatalf("creation lacks durable ownership: %#v", current)
	}
	if _, _, err := catalog.BeginOperation(ctx, pr.Operation{RequestID: "open", PullRequestID: current.ID, Kind: "transition", RequestedStatus: pr.StatusOpen, Groups: []pr.FieldGroup{pr.LifecycleGroup}}); !errors.Is(err, pr.ErrOperationInProgress) {
		t.Fatalf("overlapping Open bypassed creation: %v", err)
	}
}
