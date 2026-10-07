package sqliteadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
	"github.com/holark-ai/holark/internal/repositorybrowser"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	githubprovider "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	"github.com/holark-ai/holark/internal/database"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

type observationBarrier struct {
	started, release chan struct{}
	snapshot         pr.GitHubPullRequest
}
type actionBarrierTransport struct {
	pr.GitHubTransport
	observations    chan observationBarrier
	opening, finish chan struct{}
}

func (transport *actionBarrierTransport) ListActive(ctx context.Context, _ string) ([]pr.GitHubPullRequest, error) {
	barrier := <-transport.observations
	close(barrier.started)
	select {
	case <-barrier.release:
		return []pr.GitHubPullRequest{barrier.snapshot}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (transport *actionBarrierTransport) MarkReadyForReview(ctx context.Context, _ pr.GitHubPullRequestTarget) (pr.GitHubPullRequest, error) {
	close(transport.opening)
	select {
	case <-transport.finish:
		return pr.GitHubPullRequest{Status: pr.StatusOpen}, nil
	case <-ctx.Done():
		return pr.GitHubPullRequest{}, ctx.Err()
	}
}

func TestOpenProtectsDurableLifecycleAgainstOverlappingSync(t *testing.T) {
	ctx := context.Background()
	catalog, db := operationCatalog(t)
	data := json.RawMessage(`{"github":{"number":7,"node_id":"PR_7","draft":true,"state":"open"}}`)
	current, err := catalog.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Title: "A draft", Status: pr.StatusDraft, SyncProvider: "github", SyncExternalID: "PR_7", SyncData: data, HeadCommit: "head", BaseCommit: "base"})
	if err != nil {
		t.Fatal(err)
	}
	transport := &actionBarrierTransport{observations: make(chan observationBarrier, 3), opening: make(chan struct{}), finish: make(chan struct{})}
	coordinator := pr.New(catalog, pr.Options{Projects: pr.ProjectLookupFunc(func(string) (pr.Project, bool) {
		return pr.Project{ID: "repo", RepositoryURL: "https://github.com/org/repo", GitHubBacked: true}, true
	}), GitHubTransport: transport, GitHubCodec: githubprovider.New(nil)})
	snapshot := pr.GitHubPullRequest{Title: "Remote metadata", Status: pr.StatusClosed, ExternalID: "PR_7", SyncData: data, HeadCommit: "head", BaseCommit: "base"}
	startObservation := func() (observationBarrier, chan error) {
		barrier := observationBarrier{started: make(chan struct{}), release: make(chan struct{}), snapshot: snapshot}
		transport.observations <- barrier
		done := make(chan error, 1)
		go func() { _, err := coordinator.SyncActive(ctx, "repo"); done <- err }()
		<-barrier.started
		return barrier, done
	}
	old, oldDone := startObservation()
	actionDone := make(chan error, 1)
	go func() {
		_, err := coordinator.RequestTransition(pr.WithRequestID(ctx, "open-7"), current, pr.StatusOpen)
		actionDone <- err
	}()
	<-transport.opening
	pending, ok := catalog.GetPullRequest(current.ID)
	if !ok || pending.Status != pr.StatusDraft || len(pending.Operations) != 1 || !pending.Operations[0].Active() {
		t.Fatalf("Open must remain durably pending while provider is blocked: %#v", pending)
	}
	close(old.release)
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
	pending, _ = catalog.GetPullRequest(current.ID)
	if pending.Status != pr.StatusDraft || pending.Title != "Remote metadata" || !pending.Operations[0].Active() {
		t.Fatalf("old sync changed lifecycle or discarded valid metadata: %#v", pending)
	}
	overlapping, overlappingDone := startObservation()
	close(transport.finish)
	if err := <-actionDone; err != nil {
		t.Fatal(err)
	}
	opened, _ := catalog.GetPullRequest(current.ID)
	if opened.Status != pr.StatusOpen || len(opened.Operations) != 0 {
		t.Fatalf("confirmed Open not saved: %#v", opened)
	}
	completed, found, err := catalog.GetOperation(ctx, "open-7")
	if err != nil || !found || completed.Status != "succeeded" {
		t.Fatalf("durable Open completion: %#v %v", completed, err)
	}
	duplicate, err := coordinator.RequestTransition(pr.WithRequestID(ctx, "open-7"), current, pr.StatusOpen)
	if err != nil || duplicate.Status != pr.StatusOpen || duplicate.ViewRevision != opened.ViewRevision {
		t.Fatalf("successful duplicate must return confirmed result without another mutation: %#v %v", duplicate, err)
	}
	if _, err := coordinator.RequestTransition(pr.WithRequestID(ctx, "open-7"), opened, pr.StatusDraft); err == nil {
		t.Fatal("same request ID accepted a different transition target")
	}

	close(overlapping.release)
	if err := <-overlappingDone; err != nil {
		t.Fatal(err)
	}
	after, _ := catalog.GetPullRequest(current.ID)
	if after.Status != pr.StatusOpen || after.ViewRevision < opened.ViewRevision {
		t.Fatalf("overlapping sync rolled back Open: %#v", after)
	}
	snapshot.Status = pr.StatusDraft
	lagged, laggedDone := startObservation()
	close(lagged.release)
	if err := <-laggedDone; err != nil {
		t.Fatal(err)
	}
	after, _ = catalog.GetPullRequest(current.ID)
	if after.Status != pr.StatusDraft {
		t.Fatalf("post-action observation was not accepted: %#v", after)
	}
	snapshot.Status = pr.StatusOpen
	fresh, freshDone := startObservation()
	close(fresh.release)
	if err := <-freshDone; err != nil {
		t.Fatal(err)
	}
	after, _ = catalog.GetPullRequest(current.ID)
	if after.Status != pr.StatusOpen {
		t.Fatalf("later provider correction was not accepted: %#v", after)
	}
	var relationalStatus string
	if err := db.QueryRow(`select status from pull_requests where id=?`, current.ID).Scan(&relationalStatus); err != nil {
		t.Fatal(err)
	}
	if relationalStatus != "open" {
		t.Fatalf("relational lifecycle diverged: %s", relationalStatus)
	}
	operation, found, err := catalog.GetOperation(ctx, "open-7")
	if err != nil || !found || operation.Status != "succeeded" {
		t.Fatalf("durable operation: %#v, %v", operation, err)
	}
}

func TestIndependentObservationCannotRestorePublishedHead(t *testing.T) {
	ctx := context.Background()
	catalog, db := operationCatalog(t)
	current, err := catalog.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusOpen, Title: "Before", HeadCommit: "old", BaseCommit: "base", SyncProvider: "github", SyncExternalID: "7", SyncData: json.RawMessage(`{"github":{"state":"open"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	current = seedComparison(t, catalog, current)
	token, err := catalog.BeginObservation(ctx, "repo", []pr.FieldGroup{pr.LifecycleGroup, pr.TopologyGroup, pr.MetadataGroup})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = catalog.BeginOperation(ctx, pr.Operation{RequestID: "publish-head", PullRequestID: current.ID, Kind: "publish", Groups: []pr.FieldGroup{pr.TopologyGroup}}); err != nil {
		t.Fatal(err)
	}
	branchRead, err := catalog.BeginBranchObservation(ctx, []repository.BranchIdentity{current.HeadRef})
	if err != nil {
		t.Fatal(err)
	}
	if err = catalog.AcceptBranchObservation(ctx, branchRead, map[string]repository.BranchObservation{current.HeadRef.Ref: {Commit: "other", Exists: true}}); err == nil {
		t.Fatal("branch observation bypassed active publication")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := catalog.CompleteHeadInTransaction(ctx, tx, current.ID, "old", "new", nil, nil)
	if err != nil || !matched {
		t.Fatalf("publish: %t %v", matched, err)
	}
	if _, err = catalog.CompleteOperationInTransaction(ctx, tx, "publish-head", "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	accepted, err := catalog.CaptureComparison(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = catalog.AcceptComparison(ctx, current.ID, accepted, "base"); err != nil {
		t.Fatal(err)
	}
	incoming := current
	incoming.Title = "Remote metadata"
	if _, _, err := catalog.UpsertObservedPullRequests("repo", []pr.PullRequest{incoming}, token); err != nil {
		t.Fatal(err)
	}
	after, _ := catalog.GetPullRequest(current.ID)
	if after.HeadCommit != "new" || after.Title != "Remote metadata" {
		t.Fatalf("independent metadata/head acceptance failed: %#v", after)
	}
	if _, err := catalog.UpdatePullRequestReadiness(current.ID, current.TopologyGeneration, "old", json.RawMessage(`{"github":{"state":"closed","readiness":{"head_commit":"old"}}}`), time.Now()); err != nil {
		t.Fatal(err)
	}
	after, _ = catalog.GetPullRequest(current.ID)
	if after.HeadCommit != "new" || after.Status != pr.StatusOpen {
		t.Fatalf("stale readiness changed publication: %#v", after)
	}
	// Backward force pushes are accepted only after verifying the real remote ref.
	token, err = catalog.BeginObservation(ctx, "repo", []pr.FieldGroup{pr.TopologyGroup})
	if err != nil {
		t.Fatal(err)
	}
	incoming.VerifiedHead = true
	if _, _, err := catalog.UpsertObservedPullRequests("repo", []pr.PullRequest{incoming}, token); err != nil {
		t.Fatal(err)
	}
	read, err := catalog.BeginBranchObservation(ctx, []repository.BranchIdentity{current.HeadRef})
	if err != nil {
		t.Fatal(err)
	}
	if err = catalog.AcceptBranchObservation(ctx, read, map[string]repository.BranchObservation{current.HeadRef.Ref: {Commit: "old", Exists: true}}); err != nil {
		t.Fatal(err)
	}
	accepted, err = catalog.CaptureComparison(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = catalog.AcceptComparison(ctx, current.ID, accepted, "base"); err != nil {
		t.Fatal(err)
	}
	after, _ = catalog.GetPullRequest(current.ID)
	if after.HeadCommit != "old" {
		t.Fatalf("verified external force push not accepted: %#v", after)
	}
}

func TestLegacyConfirmationWithoutEventIDAcceptsMerge(t *testing.T) {
	ctx := t.Context()
	catalog, _ := operationCatalog(t)
	current, err := catalog.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusOpen, SyncProvider: "github", SyncExternalID: "7", LifecycleConfirmation: &pr.Confirmation{Status: pr.StatusOpen}, SyncData: json.RawMessage(`{"github":{"state":"open","lifecycle_event_id":"legacy"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	mergedAt := time.Now().UTC()
	remote := pr.GitHubPullRequest{ExternalID: "7", Status: pr.StatusMerged, MergedAt: &mergedAt, MergedCommit: "merge-sha", SyncData: json.RawMessage(`{"github":{"state":"closed","merged":true}}`)}
	coordinator := pr.New(catalog, pr.Options{Projects: pr.ProjectLookupFunc(func(string) (pr.Project, bool) {
		return pr.Project{ID: "repo", RepositoryURL: "https://github.com/org/repo", GitHubBacked: true}, true
	}), GitHubTransport: &sourceIdentityTransport{snapshots: []pr.GitHubPullRequest{remote}}})
	if _, err := coordinator.Sync(ctx, "repo"); err != nil {
		t.Fatal(err)
	}
	after, _ := catalog.GetPullRequest(current.ID)
	if after.Status != pr.StatusMerged || after.MergedAt == nil || after.MergedCommit != "merge-sha" {
		t.Fatalf("legacy confirmation blocked merge: %+v", after)
	}
}

func TestObservedMergeSurvivesActiveActionAndLateCompletion(t *testing.T) {
	ctx := t.Context()
	catalog, _ := operationCatalog(t)
	current, err := catalog.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusDraft, SyncProvider: "github", SyncExternalID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := catalog.BeginObservation(ctx, "repo", []pr.FieldGroup{pr.LifecycleGroup})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.BeginOperation(ctx, pr.Operation{RequestID: "open-7", PullRequestID: current.ID, Kind: "transition", RequestedStatus: pr.StatusOpen, Groups: []pr.FieldGroup{pr.LifecycleGroup}}); err != nil {
		t.Fatal(err)
	}
	incoming := current
	mergedAt := time.Now().UTC()
	incoming.Status, incoming.MergedAt, incoming.MergedCommit = pr.StatusMerged, &mergedAt, "merge-sha"
	if _, _, err := catalog.UpsertObservedPullRequests("repo", []pr.PullRequest{incoming}, token); err != nil {
		t.Fatal(err)
	}
	after, _ := catalog.GetPullRequest(current.ID)
	if after.Status != pr.StatusMerged || after.MergedCommit != "merge-sha" {
		t.Fatalf("merge blocked by active action: %+v", after)
	}
	confirmed := current
	confirmed.Status = pr.StatusOpen
	if _, err := catalog.CompleteTransition(ctx, "open-7", current, confirmed); err != nil {
		t.Fatal(err)
	}
	after, _ = catalog.GetPullRequest(current.ID)
	if after.Status != pr.StatusMerged || after.MergedCommit != "merge-sha" || after.MergedAt == nil {
		t.Fatalf("late completion undid merge: %+v", after)
	}
	stale := current
	stale.Status = pr.StatusOpen
	if _, _, err := catalog.UpsertObservedPullRequests("repo", []pr.PullRequest{stale}, token); err != nil {
		t.Fatal(err)
	}
	freshToken, err := catalog.BeginObservation(ctx, "repo", []pr.FieldGroup{pr.LifecycleGroup})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.UpsertObservedPullRequests("repo", []pr.PullRequest{stale}, freshToken); err != nil {
		t.Fatal(err)
	}
	after, _ = catalog.GetPullRequest(current.ID)
	if after.Status != pr.StatusMerged {
		t.Fatalf("open observation undid merge: %+v", after)
	}
}

func TestOperationSurvivesRestartAndRejectsDuplicateMutation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog.sqlite")
	db, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`create table repositories(id text primary key);insert into repositories values('repo')`); err != nil {
		t.Fatal(err)
	}
	catalog, err := New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	current, err := catalog.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusDraft})
	if err != nil {
		t.Fatal(err)
	}
	request := pr.Operation{RequestID: "durable-open", PullRequestID: current.ID, Kind: "transition", RequestedStatus: pr.StatusOpen, Groups: []pr.FieldGroup{pr.LifecycleGroup}}
	if _, created, err := catalog.BeginOperation(ctx, request); err != nil || !created {
		t.Fatalf("register: %t %v", created, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	catalog, err = New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	operation, created, err := catalog.BeginOperation(ctx, request)
	if err != nil || created || !operation.Active() {
		t.Fatalf("duplicate registration: %#v %t %v", operation, created, err)
	}
	if unfinished, err := catalog.ListUnfinishedOperations(ctx); err != nil || len(unfinished) != 1 {
		t.Fatalf("recoverable operations: %#v %v", unfinished, err)
	}
	if err := catalog.RecordOperationStep(ctx, request.RequestID, current.ID, "mutation", pr.OperationStep{Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if cancelled, err := catalog.CancelOperationBeforeStep(ctx, request.RequestID, "mutation"); err != nil || cancelled {
		t.Fatalf("must not cancel an executing remote Ready: %t %v", cancelled, err)
	}
	if _, err := catalog.CompleteOperation(ctx, request.RequestID, "failed", "definite provider rejection"); err != nil {
		t.Fatal(err)
	}
	after, _ := catalog.GetPullRequest(current.ID)
	if len(after.Operations) != 0 || after.LifecycleGeneration <= current.LifecycleGeneration {
		t.Fatalf("failure must release protection and advance generations: %#v", after)
	}
	coordinator := pr.New(catalog, pr.Options{})
	if _, err := coordinator.RequestTransition(pr.WithRequestID(ctx, request.RequestID), current, pr.StatusOpen); err == nil || err.Error() != "definite provider rejection" {
		t.Fatalf("failed duplicate must replay stored failure: %v", err)
	}
	after, _ = catalog.GetPullRequest(current.ID)
	if after.Status != pr.StatusDraft || len(after.Operations) != 0 {
		t.Fatalf("failed duplicate repeated action: %#v", after)
	}

}

func TestCreationRecoveryUsesVerifiedPublishedInputsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	remote := filepath.Join(root, "remote.git")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, output, err)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-b", "main", source)
	git("-C", source, "-c", "user.name=Recovery", "-c", "user.email=recovery@invalid", "commit", "--allow-empty", "-m", "base")
	base := git("-C", source, "rev-parse", "HEAD")
	git("-C", source, "checkout", "-b", "feature")
	git("-C", source, "-c", "user.name=Recovery", "-c", "user.email=recovery@invalid", "commit", "--allow-empty", "-m", "feature")
	head := git("-C", source, "rev-parse", "HEAD")
	git("init", "--bare", remote)
	git("-C", source, "remote", "add", "origin", remote)
	git("-C", source, "push", "origin", "main", "feature")
	gitRepository, err := gitadapter.Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	repositories := repository.NewService(gitRepository)
	db, err := database.Open(filepath.Join(root, "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`create table repositories(id text primary key);insert into repositories values('repo')`); err != nil {
		t.Fatal(err)
	}
	catalog, err := New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	operation, _, err := catalog.BeginOperation(ctx, pr.Operation{RequestID: "recover-creation", HolonID: "holon", Kind: "create", RequestedStatus: pr.StatusWIP, Groups: []pr.FieldGroup{pr.LifecycleGroup, pr.TopologyGroup}})
	if err != nil {
		t.Fatal(err)
	}
	prepared := pr.PullRequest{RepositoryID: "repo", Title: "Recovered title", Summary: "Recovered summary", BaseBranch: "main", BaseCommit: base, HeadBranch: "feature", HeadCommit: head, DiffBaseCommit: base, Status: pr.StatusWIP, LinkedHolonIDs: []string{"holon"}}
	if err := catalog.RecordOperationStep(ctx, operation.RequestID, "", "preparation", pr.OperationStep{Status: "succeeded", Creation: &pr.CreationInputs{RepositoryID: "repo", Title: prepared.Title, Summary: prepared.Summary, BaseBranch: prepared.BaseBranch, BaseCommit: prepared.BaseCommit, HeadBranch: prepared.HeadBranch, DiffBaseCommit: prepared.DiffBaseCommit}, HeadCommit: head}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.CompleteOperation(ctx, operation.RequestID, "uncertain", "publication response interrupted"); err != nil {
		t.Fatal(err)
	}
	coordinator := pr.New(catalog, pr.Options{Repository: recoveryRepository{Service: repositories}, Projects: pr.ProjectLookupFunc(func(string) (pr.Project, bool) {
		return pr.Project{ID: "repo", RepositoryURL: remote, DefaultBranch: "main"}, true
	})})
	if err := coordinator.RecoverOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.RecoverOperations(ctx); err != nil {
		t.Fatal(err)
	}
	items := catalog.ListPullRequests("repo")
	if len(items) != 1 || items[0].HeadCommit != head || items[0].Title != prepared.Title || items[0].Summary != prepared.Summary || len(items[0].LinkedHolonIDs) != 1 {
		t.Fatalf("recovered local identity: %#v", items)
	}
	operation, found, err := catalog.GetOperation(ctx, operation.RequestID)
	if err != nil || !found || operation.Status != "succeeded" || operation.PullRequestID != items[0].ID {
		t.Fatalf("recovered operation: %#v %v", operation, err)
	}
	creation := pr.NewCreationCoordinator(catalog, duplicateCreationSource{}, duplicateCreationPublisher{}, coordinator, repositories)
	request := pr.CreatePullRequest{HolonID: "holon", RepositoryID: "repo", Title: prepared.Title, Summary: prepared.Summary, Target: pr.StatusWIP}
	duplicate, err := creation.Create(pr.WithRequestID(ctx, operation.RequestID), request)
	if err != nil || duplicate.ID != items[0].ID {
		t.Fatalf("successful creation retry did not return original identity: %#v %v", duplicate, err)
	}
	request.Target = pr.StatusDraft
	if _, err := creation.Create(pr.WithRequestID(ctx, operation.RequestID), request); err == nil {
		t.Fatal("creation request ID accepted a different target")
	}
	request.Target = pr.StatusWIP
	request.HolonID = "another-holon"
	if _, err := creation.Create(pr.WithRequestID(ctx, operation.RequestID), request); err == nil {
		t.Fatal("creation request ID accepted a different Holon")
	}
	if _, _, err := catalog.BeginOperation(ctx, pr.Operation{RequestID: operation.RequestID, Kind: "publish", HolonID: "holon", PullRequestID: items[0].ID}); err == nil {
		t.Fatal("request ID accepted a different action kind")
	}

}

func TestMetadataConfirmationRejectsPostActionProviderLag(t *testing.T) {
	ctx := context.Background()
	catalog, db := operationCatalog(t)
	current, err := catalog.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusOpen, Title: "Old title", Summary: "Old body", SyncProvider: "github", SyncExternalID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	providerAt := time.Now().UTC()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = catalog.UpdateMetadataInTransaction(ctx, tx, current.ID, "Saved title", "Saved body", providerAt, providerAt); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	token, err := catalog.BeginObservation(ctx, "repo", []pr.FieldGroup{pr.MetadataGroup})
	if err != nil {
		t.Fatal(err)
	}
	current.UpdatedAt = providerAt.Add(-time.Second)
	if _, _, err := catalog.UpsertObservedPullRequests("repo", []pr.PullRequest{current}, token); err != nil {
		t.Fatal(err)
	}
	after, _ := catalog.GetPullRequest(current.ID)
	if after.Title != "Saved title" || after.Summary != "Saved body" {
		t.Fatalf("post-action provider lag restored old metadata: %#v", after)
	}
	token, err = catalog.BeginObservation(ctx, "repo", []pr.FieldGroup{pr.MetadataGroup})
	if err != nil {
		t.Fatal(err)
	}
	current.Title = "Later external title"
	current.UpdatedAt = providerAt.Add(time.Second)
	if _, _, err := catalog.UpsertObservedPullRequests("repo", []pr.PullRequest{current}, token); err != nil {
		t.Fatal(err)
	}
	after, _ = catalog.GetPullRequest(current.ID)
	if after.Title != "Later external title" {
		t.Fatalf("newer confirmed metadata revision rejected: %#v", after)
	}
}

func TestCreationAttachmentPreservesConcurrentPublicationAndMetadata(t *testing.T) {
	ctx := context.Background()
	catalog, db := operationCatalog(t)
	before, err := catalog.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusWIP, Title: "Original title", HeadCommit: "old", BaseCommit: "base", HeadBranch: "feature", BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	before = seedComparison(t, catalog, before)
	if err := catalog.UpdatePullRequestMetadata(before.ID, "Edited while creating", "New summary", time.Now()); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if matched, err := catalog.CompleteHeadInTransaction(ctx, tx, before.ID, "old", "published", nil, nil); err != nil || !matched {
		t.Fatalf("publication: %t %v", matched, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	inputs, err := catalog.CaptureComparison(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = catalog.AcceptComparison(ctx, before.ID, inputs, "base"); err != nil {
		t.Fatal(err)
	}
	response := before
	response.Status = pr.StatusDraft
	response.SyncProvider = "github"
	response.SyncExternalID = "created"
	if _, _, err = catalog.BeginOperation(ctx, pr.Operation{RequestID: "attach", PullRequestID: before.ID, Kind: "transition", RequestedStatus: pr.StatusDraft, Groups: []pr.FieldGroup{pr.LifecycleGroup}}); err != nil {
		t.Fatal(err)
	}
	attached, err := catalog.CompleteTransition(ctx, "attach", before, response)
	if err != nil {
		t.Fatal(err)
	}
	if attached.HeadCommit != "published" || attached.Title != "Edited while creating" || attached.Summary != "New summary" || attached.Status != pr.StatusDraft || attached.SyncExternalID != "created" {
		t.Fatalf("creation response restored independent writes: %#v", attached)
	}
	var head, title string
	if err := db.QueryRow(`select head_commit,title from pull_requests where id=?`, before.ID).Scan(&head, &title); err != nil {
		t.Fatal(err)
	}
	if head != attached.HeadCommit || title != attached.Title {
		t.Fatalf("representations disagree: %s, %s", head, title)
	}
}

type recoveryRepository struct{ *repository.Service }

func (value recoveryRepository) Refresh(ctx context.Context, _ repositorybrowser.Repository) error {
	_, err := value.Service.Refresh(ctx)
	return err
}
func (value recoveryRepository) ResolveRef(ctx context.Context, _ repositorybrowser.Repository, ref string) (string, error) {
	return value.Service.Resolve(ctx, ref)
}

type duplicateCreationSource struct{}

func (duplicateCreationSource) PullRequestSource(context.Context, string) (pr.PullRequestSource, error) {
	return pr.PullRequestSource{}, errors.New("duplicate creation unexpectedly inspected workspace")
}

type duplicateCreationPublisher struct{}

func (duplicateCreationPublisher) Publish(context.Context, string, string) error {
	return errors.New("duplicate creation unexpectedly published branch")
}

type sourceIdentityTransport struct {
	pr.GitHubTransport
	snapshots []pr.GitHubPullRequest
}

func (transport *sourceIdentityTransport) ListActive(context.Context, string) ([]pr.GitHubPullRequest, error) {
	return append([]pr.GitHubPullRequest(nil), transport.snapshots...), nil
}
func (transport *sourceIdentityTransport) List(context.Context, string) ([]pr.GitHubPullRequest, error) {
	return append([]pr.GitHubPullRequest(nil), transport.snapshots...), nil
}

func TestForkBranchCannotAttachLocalPublication(t *testing.T) {
	for _, mode := range []string{"basic synchronization", "uncertain creation recovery"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			catalog, _ := operationCatalog(t)
			local, err := catalog.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusWIP, Title: "Local work", HeadBranch: "feature", HeadCommit: "same-head", BaseBranch: "main", BaseCommit: "same-base", LinkedHolonIDs: []string{"holon"}})
			if err != nil {
				t.Fatal(err)
			}
			fork := pr.GitHubPullRequest{Status: pr.StatusDraft, ExternalID: "fork-pr", HeadBranch: "feature", HeadCommit: "same-head", BaseBranch: "main", BaseCommit: "same-base", SyncData: json.RawMessage(`{"github":{"head_repository_url":"https://github.com/fork/repo.git","base_repository_url":"https://github.com/owner/repo.git"}}`)}
			transport := &sourceIdentityTransport{snapshots: []pr.GitHubPullRequest{fork}}
			coordinator := pr.New(catalog, pr.Options{GitHubTransport: transport, Projects: pr.ProjectLookupFunc(func(string) (pr.Project, bool) {
				return pr.Project{ID: "repo", RepositoryURL: "git@github.com:owner/repo.git", GitHubBacked: true}, true
			})})
			refresh := func() {
				t.Helper()
				if mode == "basic synchronization" {
					if _, err := coordinator.SyncActive(ctx, "repo"); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := coordinator.RecoverOperations(ctx); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "uncertain creation recovery" {
				if _, _, err := catalog.BeginOperation(ctx, pr.Operation{RequestID: "uncertain-create", PullRequestID: local.ID, Kind: "transition", RequestedStatus: pr.StatusDraft, Groups: []pr.FieldGroup{pr.LifecycleGroup}}); err != nil {
					t.Fatal(err)
				}
				if _, err := catalog.CompleteOperation(ctx, "uncertain-create", "uncertain", "response interrupted"); err != nil {
					t.Fatal(err)
				}
			}
			refresh()
			after, _ := catalog.GetPullRequest(local.ID)
			if after.SyncExternalID != "" || after.Status != pr.StatusWIP {
				t.Fatalf("fork with identical branches and SHA attached local WIP: %#v", after)
			}
			origin := fork
			origin.ExternalID = "origin-pr"
			origin.SyncData = json.RawMessage(`{"github":{"head_repository_url":"https://github.com/OWNER/repo.git","base_repository_url":"https://github.com/owner/repo.git"}}`)
			transport.snapshots = append(transport.snapshots, origin)
			refresh()
			after, _ = catalog.GetPullRequest(local.ID)
			if after.SyncExternalID != "origin-pr" || after.Status != pr.StatusDraft {
				t.Fatalf("same origin in HTTPS/SSH forms failed to attach: %#v", after)
			}
		})
	}
}

func TestCatalogViewsExcludeHistoricalOperationPayloads(t *testing.T) {
	ctx := t.Context()
	catalog, db := operationCatalog(t)
	for _, id := range []string{"first", "second"} {
		current, err := catalog.CreatePullRequest(pr.PullRequest{ID: id, RepositoryID: "repo", Status: pr.StatusDraft})
		if err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"history", "active"} {
			requestID := id + suffix
			if _, _, err = catalog.BeginOperation(ctx, pr.Operation{RequestID: requestID, PullRequestID: current.ID, Kind: "metadata", Groups: []pr.FieldGroup{pr.MetadataGroup}}); err != nil {
				t.Fatal(err)
			}
			if err = catalog.RecordOperationStep(ctx, requestID, current.ID, "metadata", pr.OperationStep{Status: "running", Summary: strings.Repeat("large recovery input", 1000)}); err != nil {
				t.Fatal(err)
			}
			if suffix == "history" {
				if _, err = catalog.CompleteOperation(ctx, requestID, "failed", "provider rejected"); err != nil {
					t.Fatal(err)
				}
			}
		}
		var document string
		if err = db.QueryRow(`select document from pull_request_catalog where id=?`, current.ID).Scan(&document); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(document, "operations") || strings.Contains(document, "large recovery input") {
			t.Fatal("PR document duplicates operation storage")
		}
	}
	items := catalog.ListPullRequests("repo")
	if len(items) != 2 {
		t.Fatalf("list: %#v", items)
	}
	for _, current := range items {
		if len(current.Operations) != 1 || current.Operations[0].RequestID != current.ID+"active" {
			t.Fatalf("active summary: %#v", current.Operations)
		}
		detail, found := catalog.GetPullRequest(current.ID)
		if !found || len(detail.Operations) != 1 {
			t.Fatalf("detail summary: %#v", detail)
		}
		raw, err := json.Marshal(detail)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "large recovery input") || strings.Contains(string(raw), "history") {
			t.Fatal("historical operations or recovery payloads leaked into view")
		}
		historical, found, err := catalog.GetOperation(ctx, current.ID+"history")
		if err != nil || !found || len(historical.Steps["metadata"].Summary) == 0 {
			t.Fatal("historical recovery evidence was lost")
		}
	}
}

func TestObservedOpeningOnlyCompletesItsOwnIntent(t *testing.T) {
	ctx := t.Context()
	catalog, _ := operationCatalog(t)
	current, err := catalog.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusDraft, Title: "Before", SyncProvider: "github", SyncExternalID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []pr.Operation{
		{RequestID: "opening", PullRequestID: current.ID, Kind: "transition", RequestedStatus: pr.StatusOpen, Groups: []pr.FieldGroup{pr.LifecycleGroup}},
		{RequestID: "metadata", PullRequestID: current.ID, Kind: "metadata", Groups: []pr.FieldGroup{pr.MetadataGroup}},
	} {
		if _, _, err = catalog.BeginOperation(ctx, operation); err != nil {
			t.Fatal(err)
		}
	}
	token, err := catalog.BeginObservation(ctx, "repo", []pr.FieldGroup{pr.LifecycleGroup, pr.MetadataGroup})
	if err != nil {
		t.Fatal(err)
	}
	incoming := current
	incoming.Status, incoming.Title = pr.StatusOpen, "Provider lag"
	if _, _, err = catalog.UpsertObservedPullRequests("repo", []pr.PullRequest{incoming}, token); err != nil {
		t.Fatal(err)
	}
	current, _ = catalog.GetPullRequest(current.ID)
	if current.Status != pr.StatusOpen || current.Title != "Before" || len(current.Operations) != 1 || current.Operations[0].RequestID != "metadata" {
		t.Fatalf("independent intent lost: %#v", current)
	}
	opening, _, err := catalog.GetOperation(ctx, "opening")
	if err != nil || opening.Status != "succeeded" {
		t.Fatalf("external opening not confirmed: %#v %v", opening, err)
	}
	metadata, _, err := catalog.GetOperation(ctx, "metadata")
	if err != nil || metadata.Status != "running" {
		t.Fatalf("metadata completed before its effect: %#v %v", metadata, err)
	}
}

func (transport *sourceIdentityTransport) Get(context.Context, pr.GitHubPullRequestTarget) (pr.GitHubPullRequest, error) {
	return transport.snapshots[0], nil
}

func TestInterruptedWithdrawalRecoversWIPAndAcceptsVerifiedReopening(t *testing.T) {
	ctx := t.Context()
	catalog, _ := operationCatalog(t)
	current, err := catalog.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusDraft, SyncProvider: "github", SyncExternalID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = catalog.BeginOperation(ctx, pr.Operation{RequestID: "withdraw", PullRequestID: current.ID, Kind: "transition", RequestedStatus: pr.StatusWIP, Groups: []pr.FieldGroup{pr.LifecycleGroup}}); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.CompleteOperation(ctx, "withdraw", "uncertain", "lost close response"); err != nil {
		t.Fatal(err)
	}
	remote := pr.GitHubPullRequest{ExternalID: "7", Status: pr.StatusClosed, SyncData: json.RawMessage(`{"github":{"state":"closed"}}`)}
	coordinator := pr.New(catalog, pr.Options{GitHubTransport: &sourceIdentityTransport{snapshots: []pr.GitHubPullRequest{remote}}})
	if err = coordinator.RecoverOperations(ctx); err != nil {
		t.Fatal(err)
	}
	current, _ = catalog.GetPullRequest(current.ID)
	if current.Status != pr.StatusWIP || current.ClosedAt != nil || len(current.Operations) != 0 || current.LifecycleConfirmation == nil {
		t.Fatalf("withdrawal recovery: %#v", current)
	}
	observe := func(status pr.Status, data json.RawMessage) {
		t.Helper()
		token, err := catalog.BeginObservation(ctx, "repo", []pr.FieldGroup{pr.LifecycleGroup})
		if err != nil {
			t.Fatal(err)
		}
		incoming := current
		incoming.Status, incoming.SyncData = status, data
		if _, _, err = catalog.UpsertObservedPullRequests("repo", []pr.PullRequest{incoming}, token); err != nil {
			t.Fatal(err)
		}
	}
	observe(pr.StatusClosed, remote.SyncData)
	current, _ = catalog.GetPullRequest(current.ID)
	if current.Status != pr.StatusWIP {
		t.Fatalf("matching remote close replaced WIP: %#v", current)
	}
	observe(pr.StatusDraft, json.RawMessage(`{"github":{"state":"open","draft":true}}`))
	current, _ = catalog.GetPullRequest(current.ID)
	if current.Status != pr.StatusDraft {
		t.Fatalf("verified reopening rejected: %#v", current)
	}
}

// Each integration scenario gets the same real SQLite catalog and repository.
func operationCatalog(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec(`create table repositories(id text primary key);insert into repositories values('repo')`); err != nil {
		t.Fatal(err)
	}
	catalog, err := New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	return catalog, db
}
