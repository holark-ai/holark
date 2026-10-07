package sqliteadapter

import (
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/repository"
	branches "github.com/holark-ai/holark/internal/repository/sqliteadapter"
	"testing"
)

func TestSharedBranchesInvalidateDependentsAndAcceptUnchangedComparison(t *testing.T) {
	store, db := operationCatalog(t)
	ctx := t.Context()
	base := repository.PublishedBranchIdentity("https://github.com/owner/repo", "main")
	head := repository.PublishedBranchIdentity("git@github.com:fork/repo.git", "topic")
	observe := func(id repository.BranchIdentity, commit string) {
		t.Helper()
		read, err := store.BeginBranchObservation(ctx, []repository.BranchIdentity{id})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AcceptBranchObservation(ctx, read, map[string]repository.BranchObservation{id.Ref: {Commit: commit, Exists: commit != ""}}); err != nil {
			t.Fatal(err)
		}
	}
	observe(base, "base")
	observe(head, "head")
	var ids []string
	for range 2 {
		p, err := store.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusOpen, BaseRef: base, HeadRef: head, BaseBranch: "main", HeadBranch: "topic"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
		input, err := store.CaptureComparison(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		observe(base, "base")
		observe(head, "head")
		if err := store.UpdatePullRequestMetadata(p.ID, "new title", "", p.UpdatedAt); err != nil {
			t.Fatal(err)
		}
		if err := store.AcceptComparison(ctx, p.ID, input, "ancestor"); err != nil {
			t.Fatalf("identical observation starved comparison: %v", err)
		}
	}
	before, err := store.CaptureComparison(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	observe(base, "new-base")
	for _, id := range ids {
		p, _ := store.GetPullRequest(id)
		if p.ComparisonState != pr.ComparisonStale || p.BaseCommit != "base" || p.HeadCommit != "head" || p.DiffBaseCommit != "ancestor" {
			t.Fatalf("lost stale display: %+v", p)
		}
		var relationalBase string
		if err := db.QueryRow(`select base_commit from pull_requests where id=?`, id).Scan(&relationalBase); err != nil || relationalBase != p.BaseCommit {
			t.Fatalf("projection: %s %v", relationalBase, err)
		}
	}
	if err := store.AcceptComparison(ctx, ids[0], before, "ancestor"); err == nil {
		t.Fatal("accepted changed generation")
	}
	// Deletion and recreation at the same SHA must not resurrect old inputs.
	next, err := store.CaptureComparison(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	observe(head, "")
	observe(head, "head")
	if err := store.AcceptComparison(ctx, ids[0], next, "ancestor"); err == nil {
		t.Fatal("accepted recreated ref")
	}
	// Claims survive transactions and fence earlier reads on any PR sharing a ref.
	read, err := store.BeginBranchObservation(ctx, []repository.BranchIdentity{head})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := branches.ClaimBranch(ctx, tx, read.Branches[0], "first-operation"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	batch, err := store.BeginBranchObservation(ctx, []repository.BranchIdentity{base, head})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AcceptBranchObservation(ctx, batch, map[string]repository.BranchObservation{base.Ref: {Commit: "independent-base", Exists: true}, head.Ref: {Commit: "head", Exists: true}}); err == nil {
		t.Fatal("protected head accepted")
	}
	check, err := store.BeginBranchObservation(ctx, []repository.BranchIdentity{base})
	if err != nil {
		t.Fatal(err)
	}
	if check.Branches[0].Commit != "independent-base" {
		t.Fatal("protected head discarded independent base observation")
	}
	if _, err := store.CaptureComparison(ctx, ids[1]); err == nil {
		t.Fatal("shared head bypassed claim")
	}
	if err := store.AcceptBranchObservation(ctx, read, map[string]repository.BranchObservation{head.Ref: {Commit: "head", Exists: true}}); err == nil {
		t.Fatal("read overlapped protected mutation")
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := branches.ClaimBranch(ctx, tx, read.Branches[0], "second-operation"); err == nil {
		t.Fatal("cross-PR claim accepted")
	}
	tx.Rollback()
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := branches.ReleaseBranches(ctx, tx, "first-operation"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptBranchObservation(ctx, read, map[string]repository.BranchObservation{head.Ref: {Commit: "head", Exists: true}}); err == nil {
		t.Fatal("released claim lost mutation fence")
	}
}

func TestDiscoveryDoesNotReplaceSharedBranchComparison(t *testing.T) {
	store, _ := operationCatalog(t)
	ctx := t.Context()
	p, err := store.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", SyncProvider: "github", SyncExternalID: "github:owner/repo#1", Status: pr.StatusOpen, BaseBranch: "main", HeadBranch: "topic", SyncData: []byte(`{"github":{"base_repository_url":"https://github.com/owner/repo.git","head_repository_url":"https://github.com/fork/repo.git"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []struct {
		id     repository.BranchIdentity
		commit string
	}{{p.BaseRef, "actual-base"}, {p.HeadRef, "actual-head"}} {
		read, err := store.BeginBranchObservation(ctx, []repository.BranchIdentity{b.id})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AcceptBranchObservation(ctx, read, map[string]repository.BranchObservation{b.id.Ref: {Commit: b.commit, Exists: true}}); err != nil {
			t.Fatal(err)
		}
	}
	input, err := store.CaptureComparison(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		token, err := store.BeginObservation(ctx, "repo", []pr.FieldGroup{pr.LifecycleGroup, pr.TopologyGroup, pr.MetadataGroup})
		if err != nil {
			t.Fatal(err)
		}
		remote := p
		remote.BaseCommit = "stale-provider-base"
		remote.HeadCommit = "stale-provider-head"
		remote.Title = "Latest title"
		if _, _, err := store.UpsertObservedPullRequests("repo", []pr.PullRequest{remote}, token); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AcceptComparison(ctx, p.ID, input, "ancestor"); err != nil {
		t.Fatal(err)
	}
	current, _ := store.GetPullRequest(p.ID)
	if current.BaseCommit != "actual-base" || current.HeadCommit != "actual-head" || current.Title != "Latest title" || !current.HasCurrentComparison() {
		t.Fatalf("comparison: %+v", current)
	}
}

func TestRebaseCompletionKeepsAdvancedBaseAndRollsBackSharedHead(t *testing.T) {
	store, db := operationCatalog(t)
	ctx := t.Context()
	base := repository.PublishedBranchIdentity("https://github.com/owner/repo", "main")
	head := repository.PublishedBranchIdentity("https://github.com/owner/repo", "topic")
	p, err := store.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusOpen, BaseRef: base, HeadRef: head, BaseBranch: "main", HeadBranch: "topic"})
	if err != nil {
		t.Fatal(err)
	}
	observe := func(id repository.BranchIdentity, sha string) {
		t.Helper()
		r, e := store.BeginBranchObservation(ctx, []repository.BranchIdentity{id})
		if e != nil {
			t.Fatal(e)
		}
		if e = store.AcceptBranchObservation(ctx, r, map[string]repository.BranchObservation{id.Ref: {Commit: sha, Exists: true}}); e != nil {
			t.Fatal(e)
		}
	}
	observe(base, "base")
	observe(head, "head")
	input, err := store.CaptureComparison(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptComparison(ctx, p.ID, input, "ancestor"); err != nil {
		t.Fatal(err)
	}
	observe(base, "advanced-base")
	pinned, diff := "base", "base"
	for _, commit := range []bool{false, true} {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		ok, err := store.CompleteHeadInTransaction(ctx, tx, p.ID, "head", "rebased", &pinned, &diff)
		if err != nil || !ok {
			t.Fatalf("completion: %t %v", ok, err)
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		current, err := store.CaptureComparison(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		wantHead := "head"
		if commit {
			wantHead = "rebased"
		}
		if current.Base.Commit != "advanced-base" || current.Head.Commit != wantHead {
			t.Fatalf("branch state: %+v", current)
		}
	}
	p, _ = store.GetPullRequest(p.ID)
	if p.ComparisonState != pr.ComparisonStale || p.BaseCommit != "base" || p.HeadCommit != "head" {
		t.Fatalf("historical comparison: %+v", p)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if matched, err := store.CompleteHeadInTransaction(ctx, tx, p.ID, "unrelated", "older", nil, nil); err == nil && matched {
		t.Fatal("recovery overwrote unrelated head")
	}
	tx.Rollback()
}

func TestWorkerOwnershipConflictsAcrossPullRequestsSharingHead(t *testing.T) {
	store, _ := operationCatalog(t)
	ctx := t.Context()
	base := repository.PublishedBranchIdentity("https://github.com/owner/repo", "main")
	head := repository.PublishedBranchIdentity("https://github.com/fork/repo", "topic")
	for _, b := range []struct {
		id  repository.BranchIdentity
		sha string
	}{{base, "base"}, {head, "head"}} {
		read, err := store.BeginBranchObservation(ctx, []repository.BranchIdentity{b.id})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AcceptBranchObservation(ctx, read, map[string]repository.BranchObservation{b.id.Ref: {Commit: b.sha, Exists: true}}); err != nil {
			t.Fatal(err)
		}
	}
	var ids []string
	for range 2 {
		p, err := store.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusOpen, BaseRef: base, HeadRef: head, BaseBranch: "main", HeadBranch: "topic"})
		if err != nil {
			t.Fatal(err)
		}
		inputs, err := store.CaptureComparison(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AcceptComparison(ctx, p.ID, inputs, "base"); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	operation, created, err := store.BeginOperation(ctx, pr.Operation{RequestID: "worker", PullRequestID: ids[0], Kind: "work_publish", Groups: []pr.FieldGroup{pr.TopologyGroup}})
	if err != nil || !created || operation.BranchInputs == nil {
		t.Fatalf("claim: %+v %t %v", operation, created, err)
	}
	for _, kind := range []string{"publish", "rebase", "merge"} {
		if _, _, err := store.BeginOperation(ctx, pr.Operation{RequestID: kind, PullRequestID: ids[1], Kind: kind, Groups: []pr.FieldGroup{pr.TopologyGroup}}); err == nil {
			t.Fatalf("%s bypassed shared ownership", kind)
		}
	}
	if _, err := store.CompleteOperation(ctx, operation.RequestID, "uncertain", "interrupted push"); err != nil {
		t.Fatal(err)
	}
	heldRead, err := store.BeginBranchObservation(ctx, []repository.BranchIdentity{head})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptBranchObservation(ctx, heldRead, map[string]repository.BranchObservation{head.Ref: {Commit: "head", Exists: true}}); err == nil {
		t.Fatal("uncertain push released head")
	}
	if _, err := store.CompleteOperation(ctx, operation.RequestID, "failed", "confirmed no push"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginBranchObservation(ctx, []repository.BranchIdentity{head}); err != nil {
		t.Fatal(err)
	}
}

func TestOpeningKeepsBranchRelationshipsDuringBackgroundInvalidation(t *testing.T) {
	store, _ := operationCatalog(t)
	ctx := t.Context()
	base := repository.PublishedBranchIdentity("git@github.com:owner/repo.git", "main")
	head := repository.PublishedBranchIdentity("git@github.com:owner/repo.git", "topic")
	p, err := store.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusWIP, BaseBranch: "main", HeadBranch: "topic", BaseRef: base, HeadRef: head})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.BeginOperation(ctx, pr.Operation{RequestID: "opening", PullRequestID: p.ID, Kind: "transition", RequestedStatus: pr.StatusDraft, Groups: []pr.FieldGroup{pr.LifecycleGroup}})
	if err != nil {
		t.Fatal(err)
	}
	// Display state may advance while GitHub creates the pull request.
	if _, err = store.mutate(p.ID, func(current *pr.PullRequest) error { current.TopologyGeneration++; return nil }); err != nil {
		t.Fatal(err)
	}
	confirmed := p
	confirmed.Status = pr.StatusDraft
	confirmed.SyncProvider = "github"
	confirmed.SyncExternalID = "github:owner/repo#1"
	confirmed.SyncData = []byte(`{"github":{"base_repository_url":"https://github.com/owner/repo.git","head_repository_url":"https://github.com/owner/repo.git"}}`)
	result, err := store.CompleteTransition(ctx, "opening", p, confirmed)
	if err != nil {
		t.Fatal(err)
	}
	if result.BaseRef != base || result.HeadRef != head {
		t.Fatalf("opening lost relationship: %+v", result)
	}
}

// seedComparison supplies accepted branch evidence to operation integration tests.
func seedComparison(t *testing.T, store *Store, p pr.PullRequest) pr.PullRequest {
	t.Helper()
	if p.BaseBranch == "" {
		p.BaseBranch = "main"
	}
	if p.HeadBranch == "" {
		p.HeadBranch = "topic"
	}
	if p.BaseCommit == "" {
		p.BaseCommit = "base"
	}
	if p.HeadCommit == "" {
		p.HeadCommit = "head"
	}
	base, head := p.BaseCommit, p.HeadCommit
	p.BaseRef = repository.PublishedBranchIdentity("https://github.com/owner/repo.git", p.BaseBranch)
	p.HeadRef = repository.PublishedBranchIdentity("https://github.com/owner/repo.git", p.HeadBranch)
	p.SyncData = mergeTopologyData(p.SyncData, []byte(`{"github":{"base_repository_url":"https://github.com/owner/repo.git","head_repository_url":"https://github.com/owner/repo.git"}}`))
	var err error
	p, err = store.mutate(p.ID, func(current *pr.PullRequest) error { *current = p; return nil })
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.BeginBranchObservation(t.Context(), []repository.BranchIdentity{p.BaseRef, p.HeadRef})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AcceptBranchObservation(t.Context(), read, map[string]repository.BranchObservation{p.BaseRef.Ref: {Commit: base, Exists: true}, p.HeadRef.Ref: {Commit: head, Exists: true}}); err != nil {
		t.Fatal(err)
	}
	inputs, err := store.CaptureComparison(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AcceptComparison(t.Context(), p.ID, inputs, base); err != nil {
		t.Fatal(err)
	}
	p, _ = store.GetPullRequest(p.ID)
	return p
}

func TestRebaseCompletionRetainsComparisonUntilPrepared(t *testing.T) {
	store, db := operationCatalog(t)
	ctx := t.Context()
	base := repository.PublishedBranchIdentity("https://github.com/owner/repo", "main")
	head := repository.PublishedBranchIdentity("https://github.com/owner/repo", "topic")
	p, err := store.CreatePullRequest(pr.PullRequest{RepositoryID: "repo", Status: pr.StatusOpen, BaseRef: base, HeadRef: head, BaseBranch: "main", HeadBranch: "topic"})
	if err != nil {
		t.Fatal(err)
	}
	observe := func(headCommit string) {
		t.Helper()
		read, err := store.BeginBranchObservation(ctx, []repository.BranchIdentity{base, head})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AcceptBranchObservation(ctx, read, map[string]repository.BranchObservation{base.Ref: {Commit: "base", Exists: true}, head.Ref: {Commit: headCommit, Exists: true}}); err != nil {
			t.Fatal(err)
		}
	}
	observe("head")
	inputs, err := store.CaptureComparison(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptComparison(ctx, p.ID, inputs, "ancestor"); err != nil {
		t.Fatal(err)
	}
	p, _ = store.GetPullRequest(p.ID)
	operation, created, err := store.BeginOperation(ctx, pr.Operation{RequestID: "rebase-version", PullRequestID: p.ID, Kind: "rebase", ExpectedHead: p.HeadCommit, ExpectedInputs: pr.CaptureMutationInputs(p), Groups: []pr.FieldGroup{pr.TopologyGroup, pr.LifecycleGroup}})
	if err != nil || !created {
		t.Fatalf("claim: %t %v", created, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	pinned := "base"
	if matched, err := store.CompleteHeadInTransaction(ctx, tx, p.ID, "head", "rebased", &pinned, &pinned); err != nil || !matched {
		t.Fatalf("completion: %t %v", matched, err)
	}
	if _, err := store.CompleteOperationInTransaction(ctx, tx, operation.RequestID, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	p, _ = store.GetPullRequest(p.ID)
	if p.HasCurrentComparison() || p.HeadCommit != "head" || p.DiffBaseCommit != "ancestor" || p.TopologyConfirmation == nil || p.TopologyConfirmation.HeadCommit != "rebased" {
		t.Fatalf("publication must retain stale display and confirm published head: %+v", p)
	}
	observe("rebased")
	current, err := store.CaptureComparison(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptComparison(ctx, p.ID, inputs, "ancestor"); err == nil {
		t.Fatal("pre-publication preparation accepted")
	}
	if err := store.AcceptComparison(ctx, p.ID, current, "base"); err != nil {
		t.Fatal(err)
	}
	p, _ = store.GetPullRequest(p.ID)
	if !p.HasCurrentComparison() || p.HeadCommit != "rebased" || !pr.SameComparisonVersion(p.Comparison.Inputs, current) {
		t.Fatalf("prepared comparison is not current: %+v; inputs=%+v", p, current)
	}
}
