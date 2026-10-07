package localapp_test

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
	"github.com/holark-ai/holark/internal/repository"
)

func metadataGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", path, "-c", "user.name=Metadata Test", "-c", "user.email=metadata@invalid"}, args...)...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func metadataWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func (fixture *creationFixture) changeRange(t *testing.T, id, base, head string) {
	t.Helper()
	catalog, err := prsqlite.New(t.Context(), fixture.scenario.Application.Database)
	if err != nil {
		t.Fatal(err)
	}
	current := fixture.pullRequest(t, id)
	path := fixture.scenario.State.WorktreePath
	metadataGit(t, path, "push", "--force", "origin", base+":refs/heads/"+current.BaseBranch, head+":refs/heads/"+current.HeadBranch)
	read, err := catalog.BeginBranchObservation(t.Context(), []repository.BranchIdentity{current.BaseRef, current.HeadRef})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.AcceptBranchObservation(t.Context(), read, map[string]repository.BranchObservation{current.BaseRef.Ref: {Commit: base, Exists: true}, current.HeadRef.Ref: {Commit: head, Exists: true}}); err != nil {
		t.Fatal(err)
	}
	inputs, err := catalog.CaptureComparison(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	mergeBase := metadataGit(t, path, "merge-base", base, head)
	if err := catalog.AcceptComparison(t.Context(), id, inputs, mergeBase); err != nil {
		t.Fatal(err)
	}
	fixture.scenario.GitHub.SetCommits(base, head)

}

func TestMetadataChangesDuringGenerationStillSaveAndPublish(t *testing.T) {
	fixture := newCreationFixture(t)
	created := fixture.create(t, pullrequestlifecycle.StatusOpen)
	pending := fixture.metadata(t, created.ID)
	path := fixture.scenario.State.WorktreePath
	metadataWrite(t, filepath.Join(path, "scenario.txt"), "Updated behavior\n")
	metadataGit(t, path, "add", "scenario.txt")
	metadataGit(t, path, "commit", "-m", "Change behavior")
	head := metadataGit(t, path, "rev-parse", "HEAD")
	fixture.changeRange(t, created.ID, created.BaseCommit, head)
	metadataGit(t, path, "push", "origin", head+":refs/heads/"+created.HeadBranch)
	fixture.scenario.GitHub.SetCommits(created.BaseCommit, head)
	fixture.complete(t, pending)
	fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusOpen)
	// Publication success precedes comparison preparation. Wait for the existing
	// synchronization path before requiring a current freshness assessment.
	fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/sync", `{}`, http.StatusNoContent, nil)

	saved := fixture.metadata(t, created.ID)
	if saved.Freshness == nil || !saved.Freshness.Outdated || saved.Freshness.Generated.DiffBaseCommit != created.BaseCommit || saved.Freshness.Generated.HeadCommit != created.HeadCommit || saved.Freshness.Current.HeadCommit != head {
		t.Fatalf("generation/current ranges = %+v", saved.Freshness)
	}
	if saved.ApplicationError != nil || saved.AgentSession.Error != "" {
		t.Fatalf("valid output reported failure: %+v", saved)
	}
	requests := fixture.scenario.GitHub.CreateRequests()
	if len(requests) != 1 || requests[0].Body != generatedDescription {
		t.Fatalf("publication = %+v", requests)
	}
}

func TestMetadataFreshnessUsesPatchAndFullOrderedMessages(t *testing.T) {
	for _, test := range []struct {
		name, content, message string
		outdated               bool
	}{
		{"patch matches despite changed message", "pull request creation browser scenario\n", "Rewrite explanation", false},
		{"messages match despite changed patch", "Substantive amendment\n", "Add pull request creation scenario", false},
		{"both differ", "Different behavior\n", "Describe different behavior", true},
		{"full message body matters", "Different behavior\n", "Add pull request creation scenario\n\nAdditional rationale", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCreationFixture(t)
			created := fixture.create(t, pullrequestlifecycle.StatusWIP)
			fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/metadata/agent", `{"action":"regenerate"}`, http.StatusAccepted, nil)
			fixture.complete(t, fixture.metadata(t, created.ID))
			fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusWIP)
			path := fixture.scenario.State.WorktreePath
			metadataWrite(t, filepath.Join(path, "scenario.txt"), test.content)
			metadataGit(t, path, "add", "scenario.txt")
			metadataGit(t, path, "commit", "--amend", "--allow-empty", "-m", test.message)
			fixture.changeRange(t, created.ID, created.BaseCommit, metadataGit(t, path, "rev-parse", "HEAD"))
			metadataGit(t, path, "push", "--force", "origin", "HEAD:refs/heads/"+created.HeadBranch)
			fixture.reopen(t)
			saved := fixture.metadata(t, created.ID)
			if saved.Freshness == nil || saved.Freshness.Outdated != test.outdated {
				t.Fatalf("reloaded freshness = %+v", saved.Freshness)
			}
			if test.outdated {
				current := fixture.pullRequest(t, created.ID)
				fixture.scenario.GitHub.SetCommits(current.BaseCommit, current.HeadCommit)
				fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/transition", `{"status":"open"}`, http.StatusOK, nil)
				fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusOpen)
				if opened := fixture.metadata(t, created.ID); opened.AgentSession.ID != saved.AgentSession.ID || !opened.Freshness.Outdated || opened.PreparationTarget != "" {
					t.Fatalf("outdated metadata was regenerated: %+v", opened)
				}
			}
		})
	}
}

func TestMetadataRebaseIgnoresLinePositionsAndKeepsMessageFallback(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "shifted patch", true: "conflict resolution with unchanged messages"}[conflict], func(t *testing.T) {
			fixture := newCreationFixture(t)
			path := fixture.scenario.State.WorktreePath
			// Add a tracked-file edit to the source commit before generation.
			metadataWrite(t, filepath.Join(path, "README.md"), "Pull request scenario\nDescription detail\n")
			metadataGit(t, path, "add", "README.md")
			metadataGit(t, path, "commit", "--amend", "-m", "Explain description")
			created := fixture.create(t, pullrequestlifecycle.StatusWIP)
			fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/metadata/agent", `{"action":"regenerate"}`, http.StatusAccepted, nil)
			fixture.complete(t, fixture.metadata(t, created.ID))
			fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusWIP)
			metadataGit(t, path, "checkout", "--detach", created.BaseCommit)
			metadataWrite(t, filepath.Join(path, "README.md"), "Upstream prefix\nPull request scenario\n")
			metadataGit(t, path, "add", "README.md")
			metadataGit(t, path, "commit", "-m", "Upstream change")
			base := metadataGit(t, path, "rev-parse", "HEAD")
			metadataGit(t, path, "commit", "--allow-empty", "-m", "Add pull request creation scenario")
			detail, message := "Description detail", "Reword explanation"
			if conflict {
				detail, message = "Resolved description detail", "Explain description"
			}
			metadataWrite(t, filepath.Join(path, "README.md"), "Upstream prefix\nPull request scenario\n"+detail+"\n")
			metadataWrite(t, filepath.Join(path, "scenario.txt"), "pull request creation browser scenario\n")
			metadataGit(t, path, "add", ".")
			metadataGit(t, path, "commit", "-m", message)
			fixture.changeRange(t, created.ID, base, metadataGit(t, path, "rev-parse", "HEAD"))
			saved := fixture.metadata(t, created.ID)
			if saved.Freshness == nil || saved.Freshness.Outdated {
				t.Fatalf("rebase warning = %+v", saved.Freshness)
			}
		})
	}
}

func TestMetadataProvenanceSurvivesFailedRegenerationAndClearsOnManualEdit(t *testing.T) {
	fixture := newCreationFixture(t)
	created := fixture.create(t, pullrequestlifecycle.StatusWIP)
	fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/metadata/agent", `{"action":"regenerate"}`, http.StatusAccepted, nil)
	fixture.complete(t, fixture.metadata(t, created.ID))
	fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusWIP)
	path := fixture.scenario.State.WorktreePath
	metadataWrite(t, filepath.Join(path, "scenario.txt"), "New behavior\n")
	metadataGit(t, path, "add", "scenario.txt")
	metadataGit(t, path, "commit", "-m", "New behavior")
	head := metadataGit(t, path, "rev-parse", "HEAD")
	fixture.changeRange(t, created.ID, created.BaseCommit, head)
	var pending pullrequestmetadata.Metadata
	fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/metadata/agent", `{"action":"regenerate"}`, http.StatusAccepted, &pending)
	if pending.Description != generatedDescription || pending.Freshness == nil || !pending.Freshness.Outdated || pending.Freshness.Generated.HeadCommit != created.HeadCommit {
		t.Fatalf("regeneration lost previous provenance: %+v", pending)
	}
	if err := fixture.scenario.Agents.SetTurnError(t.Context(), pending.AgentSession.ID, "Agent crashed"); err != nil {
		t.Fatal(err)
	}
	saved := fixture.metadata(t, created.ID)
	if saved.Description != generatedDescription || saved.Freshness == nil || !saved.Freshness.Outdated {
		t.Fatalf("failure lost previous provenance: %+v", saved)
	}
	fixture.assertPublicationBlocked(t, created.ID, false)
	fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/metadata/agent", `{"action":"regenerate"}`, http.StatusAccepted, &pending)
	fixture.complete(t, pending)
	fixture.await(t, func() bool { return fixture.metadata(t, created.ID).GenerationComplete })
	saved = fixture.metadata(t, created.ID)
	if saved.Freshness == nil || saved.Freshness.Outdated || saved.Freshness.Generated.HeadCommit != head {
		t.Fatalf("regeneration did not replace provenance: %+v", saved)
	}
	fixture.request(t, "PATCH", "/api/v1/pull-requests/"+created.ID+"/metadata", `{"description":"User maintained description"}`, http.StatusOK, nil)
	fixture.reopen(t)
	saved = fixture.metadata(t, created.ID)
	if saved.Freshness != nil || saved.Description != "User maintained description" {
		t.Fatalf("reloaded manual description: %+v", saved)
	}
}

func TestMetadataSaveFailureCanRetryDurableOutputAfterReload(t *testing.T) {
	fixture := newCreationFixture(t)
	created := fixture.create(t, pullrequestlifecycle.StatusOpen)
	pending := fixture.metadata(t, created.ID)
	_, err := fixture.scenario.Application.Database.ExecContext(t.Context(), `create temp trigger reject_metadata before update of description on pull_requests begin select raise(fail, 'save unavailable'); end`)
	if err != nil {
		t.Fatal(err)
	}
	fixture.complete(t, pending)
	fixture.await(t, func() bool { return fixture.metadata(t, created.ID).ApplicationError != nil })
	saved := fixture.metadata(t, created.ID)
	if saved.ApplicationError.Operation != "save" || saved.AgentSession.Error != "" || saved.Description != created.Summary {
		t.Fatalf("save failure: %+v", saved)
	}
	fixture.reopen(t) // The temporary failure disappears; pending output remains in SQLite.
	fixture.scenario.GitHub.SetCommits(created.BaseCommit, created.HeadCommit)
	saved = pullrequestmetadata.Metadata{}
	fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/metadata/retry", "", http.StatusOK, &saved)
	fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusOpen)
	if saved.ApplicationError != nil || saved.Freshness == nil || saved.Freshness.Outdated {
		t.Fatalf("retry did not save output and provenance: %+v", saved)
	}
}

func TestMetadataFreshnessKeepsCommitMessageOrder(t *testing.T) {
	fixture := newCreationFixture(t)
	path := fixture.scenario.State.WorktreePath
	metadataWrite(t, filepath.Join(path, "scenario.txt"), "Second change\n")
	metadataGit(t, path, "commit", "-am", "Second change")
	created := fixture.create(t, pullrequestlifecycle.StatusWIP)
	fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/metadata/agent", `{"action":"regenerate"}`, http.StatusAccepted, nil)
	fixture.complete(t, fixture.metadata(t, created.ID))
	fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusWIP)
	// Recreate the same messages in reverse order, with a different overall patch.
	metadataGit(t, path, "checkout", "--detach", created.BaseCommit)
	metadataWrite(t, filepath.Join(path, "scenario.txt"), "Second change\n")
	metadataGit(t, path, "add", ".")
	metadataGit(t, path, "commit", "-m", "Second change")
	metadataWrite(t, filepath.Join(path, "scenario.txt"), "Reordered behavior\n")
	metadataGit(t, path, "commit", "-am", "Add pull request creation scenario")
	metadataGit(t, path, "commit", "--allow-empty", "-m", "Add pull request creation scenario")
	fixture.changeRange(t, created.ID, created.BaseCommit, metadataGit(t, path, "rev-parse", "HEAD"))
	saved := fixture.metadata(t, created.ID)
	if saved.Freshness == nil || !saved.Freshness.Outdated {
		t.Fatalf("reordered messages hid changed patch: %+v", saved.Freshness)
	}
}

func TestMetadataFreshnessFailurePreservesLastDescriptionWarning(t *testing.T) {
	fixture := newCreationFixture(t)
	created := fixture.create(t, pullrequestlifecycle.StatusWIP)
	fixture.request(t, "POST", "/api/v1/pull-requests/"+created.ID+"/metadata/agent", `{"action":"regenerate"}`, http.StatusAccepted, nil)
	fixture.complete(t, fixture.metadata(t, created.ID))
	fixture.awaitTarget(t, created.ID, pullrequestlifecycle.StatusWIP)
	path := fixture.scenario.State.WorktreePath
	metadataWrite(t, filepath.Join(path, "scenario.txt"), "A substantively different implementation\n")
	metadataGit(t, path, "add", "scenario.txt")
	metadataGit(t, path, "commit", "--amend", "-m", "Describe changed implementation")
	fixture.changeRange(t, created.ID, created.BaseCommit, metadataGit(t, path, "rev-parse", "HEAD"))
	outdated := fixture.metadata(t, created.ID)
	if outdated.Freshness == nil || !outdated.Freshness.Outdated {
		t.Fatalf("expected outdated description: %+v", outdated.Freshness)
	}
	catalog, err := prsqlite.New(t.Context(), fixture.scenario.Application.Database)
	if err != nil {
		t.Fatal(err)
	}
	// A required comparison failure retains the last complete display and warning.
	if err := catalog.InvalidateComparison(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	unknown := fixture.metadata(t, created.ID)
	if unknown.Freshness == nil || unknown.Freshness.Status != "unknown" || !unknown.Freshness.Outdated || unknown.Description != outdated.Description {
		t.Fatalf("failed comparison erased warning: %+v", unknown)
	}
}
