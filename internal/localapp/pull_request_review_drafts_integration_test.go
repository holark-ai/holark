package localapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/holons"
	holonssqlite "github.com/holark-ai/holark/internal/holons/sqliteadapter"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	commentssqlite "github.com/holark-ai/holark/internal/pullrequestcomments/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	metadatagit "github.com/holark-ai/holark/internal/pullrequestmetadata/gitadapter"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	worksqlite "github.com/holark-ai/holark/internal/pullrequestwork/sqliteadapter"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
	"github.com/holark-ai/holark/internal/testfixture/branchfixture"
)

// draftsTarget reports the fixture's pull request as open at its real head.
type draftsTarget struct{ head string }

func (d draftsTarget) GetCommentTarget(context.Context, string) (pullrequestcomments.PullRequestTarget, error) {
	return pullrequestcomments.PullRequestTarget{ID: "pr", HeadCommit: d.head, ComparisonCurrent: true, Status: pullrequestcomments.PullRequestOpen, SyncProvider: "github", RepositoryURL: "https://github.com/fixture/repo", ProviderPullRequest: 1}, nil
}

type reviewDraftsFixture struct {
	db                       *sql.DB
	dbPath, root, base, head string
	prs                      *prsqlite.Store
	store                    *worksqlite.Store
	work                     *pullrequestwork.Service
	holons                   *holons.Service
	comments                 *pullrequestcomments.Service
	runtime                  *terminalHolonService
}

func newReviewDraftsFixture(t *testing.T) *reviewDraftsFixture {
	t.Helper()
	f := &reviewDraftsFixture{root: t.TempDir(), dbPath: filepath.Join(t.TempDir(), "state.sqlite")}
	gitPlumbingCommand(t, f.root, "init", "-b", "main")
	gitPlumbingCommand(t, f.root, "config", "user.name", "Review Test")
	gitPlumbingCommand(t, f.root, "config", "user.email", "review@invalid")
	gitPlumbingWrite(t, f.root, "file.txt", "original\n")
	gitPlumbingCommand(t, f.root, "add", ".")
	gitPlumbingCommand(t, f.root, "commit", "-m", "Initial")
	f.base = gitPlumbingOutput(t, f.root, "rev-parse", "HEAD")
	gitPlumbingWrite(t, f.root, "file.txt", "changed\n")
	gitPlumbingCommand(t, f.root, "commit", "-am", "Feature")
	f.head = gitPlumbingOutput(t, f.root, "rev-parse", "HEAD")
	f.open(t)
	_, err := branchfixture.Create(t.Context(), f.prs, pullrequestlifecycle.PullRequest{ID: "pr", RepositoryID: "repo", Title: "Review", Status: pullrequestlifecycle.StatusOpen, BaseBranch: "main", BaseCommit: f.base, HeadBranch: "topic", HeadCommit: f.head})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Close() })
	return f
}
func (f *reviewDraftsFixture) open(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	var err error
	f.db, err = database.Open(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ExecContext(ctx, "create table if not exists repositories(id text primary key); insert or ignore into repositories(id) values('repo')"); err != nil {
		t.Fatal(err)
	}
	f.prs, err = prsqlite.New(ctx, f.db)
	if err != nil {
		t.Fatal(err)
	}
	f.store, err = worksqlite.New(ctx, f.db)
	if err != nil {
		t.Fatal(err)
	}
	hs, err := holonssqlite.New(ctx, f.db)
	if err != nil {
		t.Fatal(err)
	}
	git, err := gitadapter.OpenWithWorktrees(ctx, f.root, filepath.Join(f.root, "worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	f.holons = holons.NewServiceWithRepository(hs, holonRepositoryCoordinator{repositories: repository.NewService(git)})
	cs, err := commentssqlite.New(ctx, f.db)
	if err != nil {
		t.Fatal(err)
	}
	f.comments = pullrequestcomments.NewService(cs, draftsTarget{head: f.head}, pullrequestcomments.WithProviderGateway(blockedDeliveryProvider{}, nil))
	runtime := &terminalHolonService{Service: f.holons}
	f.runtime = runtime
	f.work = pullrequestwork.New(f.store, localWorkCatalog{store: f.prs}, localWorkLauncher{holons: runtime}, localReviewComments{comments: f.comments})
	f.work.SetReviewChanges(localReviewChanges{changes: metadatagit.Changes{Path: f.root}})
	f.work.SetWorkerRuntime(localWorkRuntime{holons: runtime})
	runtime.work = f.work
	runtime.workCompletion = &localPullRequestWorkCompletionCoordinator{reviewMu: &runtime.recoveryMu, holons: f.holons, work: f.work}
}
func (f *reviewDraftsFixture) start(t *testing.T) (pullrequestwork.Work, holons.Holon) {
	t.Helper()
	works, err := f.work.Start(t.Context(), pullrequestwork.Start{PullRequestID: "pr", Kind: pullrequestwork.KindReview})
	if err != nil {
		t.Fatal(err)
	}
	w := works[0]
	if w.Mode != pullrequestwork.ModeAssisted {
		t.Fatalf("default mode: %s", w.Mode)
	}
	h, err := f.holons.Get(t.Context(), w.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	h, err = f.holons.SetAgentSessionStatus(t.Context(), h.ID, h.AgentSessions[0].ID, holons.StatusRunning, "", "conversation")
	if err != nil {
		t.Fatal(err)
	}
	h, err = f.holons.UpdateAgentObservation(t.Context(), h.ID, h.AgentSessions[0].ID, "", "", "", string(protocol.InputTaskComplete), string(protocol.ObservabilityHealthy), "", protocol.ActivityIdle, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.WorktreePath, ".holark"), 0700); err != nil {
		t.Fatal(err)
	}
	return w, h
}

func TestAssistedReviewRecoveryImportsSavedCompletedTurn(t *testing.T) {
	for _, test := range []struct {
		name          string
		artifact      string
		unfinished    string
		wantStatus    pullrequestwork.Status
		completedPeer bool
	}{
		{name: "completed peer", completedPeer: true, artifact: `{"comments":[{"body":"saved finding"}]}`, wantStatus: pullrequestwork.StatusCompleted},
		{name: "completed", artifact: `{"comments":[{"body":"saved finding"}]}`, wantStatus: pullrequestwork.StatusCompleted},
		{name: "missing", wantStatus: pullrequestwork.StatusRunning},
		{name: "invalid", artifact: `{"comments":[{"body":"saved finding","scope":"typo"}]}`, wantStatus: pullrequestwork.StatusWaiting},
		{name: "unfinished turn", artifact: `{"comments":[{"body":"partial finding"}]}`, unfinished: "source", wantStatus: pullrequestwork.StatusRunning},
		{name: "unfinished peer", artifact: `{"comments":[{"body":"partial finding"}]}`, unfinished: "peer", wantStatus: pullrequestwork.StatusRunning},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newReviewDraftsFixture(t)
			_, h := f.start(t)
			agentID := h.AgentSessions[0].ID
			if test.completedPeer {
				peer, err := f.runtime.AddForkedAgent(t.Context(), h.ID, agentID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.holons.SetAgentSessionStatus(t.Context(), h.ID, peer.ID, holons.StatusRunning, "", "peer"); err != nil {
					t.Fatal(err)
				}
				if _, err = f.holons.UpdateAgentObservation(t.Context(), h.ID, peer.ID, "", "", "", string(protocol.InputTaskComplete), string(protocol.ObservabilityHealthy), "", protocol.ActivityCompleted, nil); err != nil {
					t.Fatal(err)
				}
			}
			if test.unfinished != "" {
				writerID := agentID
				if test.unfinished == "peer" {
					peer, err := f.runtime.AddForkedAgent(t.Context(), h.ID, agentID)
					if err != nil {
						t.Fatal(err)
					}
					writerID = peer.ID
				}
				if _, err := f.holons.SetAgentSessionStatus(t.Context(), h.ID, writerID, holons.StatusRunning, "", "conversation"); err != nil {
					t.Fatal(err)
				}
				if _, err := f.holons.UpdateAgentObservation(t.Context(), h.ID, writerID, "", "", "", string(protocol.InputNone), string(protocol.ObservabilityHealthy), "", protocol.ActivityWorking, nil); err != nil {
					t.Fatal(err)
				}
			}
			if test.artifact != "" {
				gitPlumbingWrite(t, h.WorktreePath, ".holark/review.json", test.artifact)
			}

			// Reopen durable state at the crash boundary: task_complete was saved,
			// but its artifact has not been imported. No CLI or new event is needed.
			for attempt := 0; attempt < 2; attempt++ {
				if err := f.db.Close(); err != nil {
					t.Fatal(err)
				}
				f.open(t)
				if err := clearStaleTerminalBindings(t.Context(), f.holons); err != nil {
					t.Fatal(err)
				}
				if err := f.work.Recover(t.Context(), h.ID); err != nil {
					t.Fatal(err)
				}
				f.runtime.restoreAgents(t.Context())
				work, err := f.work.WorkForSession(t.Context(), h.ID)
				if err != nil || work.Status != test.wantStatus {
					t.Fatalf("recovered work: %+v err=%v", work, err)
				}
				comments, err := f.comments.ListByPullRequest(t.Context(), "pr")
				if err != nil {
					t.Fatal(err)
				}
				recovered, err := f.holons.Get(t.Context(), h.ID)
				if err != nil {
					t.Fatal(err)
				}
				if test.wantStatus == pullrequestwork.StatusCompleted {
					if len(comments) != 1 || comments[0].Body != "saved finding" || comments[0].PublicationState != pullrequestcomments.PublicationDraft {
						t.Fatalf("recovered drafts: %+v", comments)
					}
					for _, agent := range recovered.AgentSessions {
						if agent.Status != string(holons.StatusExpired) {
							t.Fatalf("completed review relaunched: %+v", recovered)
						}
					}
					if recovered.ArchivedAt == nil {
						t.Fatalf("completed review not archived: %+v", recovered)
					}
				} else {
					if len(comments) != 0 || recovered.AgentSession(agentID).Status != string(holons.StatusRestoring) {
						t.Fatalf("unfinished review was consumed: comments=%+v holon=%+v", comments, recovered)
					}
					if test.unfinished == "" && recovered.AgentSession(agentID).InputState != string(protocol.InputUserRequired) {
						t.Fatalf("review not available for correction: %+v", recovered)
					}
					if test.artifact != "" {
						data, err := os.ReadFile(filepath.Join(h.WorktreePath, ".holark/review.json"))
						if err != nil || string(data) != test.artifact {
							t.Fatalf("unfinished artifact changed: %q err=%v", data, err)
						}
					}
				}
			}
		})
	}
}

func TestAssistedReviewImportsFindingsAsPublishableDrafts(t *testing.T) {
	f := newReviewDraftsFixture(t)
	_, h := f.start(t)
	agentID := h.AgentSessions[0].ID
	coordinator := localPullRequestWorkCompletionCoordinator{holons: f.holons, work: f.work}

	// Without an artifact the conversation continues and nothing is imported.
	if _, err := coordinator.CompleteFromAgent(t.Context(), h, agentID); err != nil {
		t.Fatal(err)
	}
	current, err := f.work.WorkForSession(t.Context(), h.ID)
	if err != nil || current.Status != pullrequestwork.StatusRunning {
		t.Fatalf("work without artifact: %+v err=%v", current, err)
	}
	if comments, err := f.comments.ListByPullRequest(t.Context(), "pr"); err != nil || len(comments) != 0 {
		t.Fatalf("comments without artifact: %+v err=%v", comments, err)
	}

	gitPlumbingWrite(t, h.WorktreePath, ".holark/review.json", `{"comments":[{"body":"first finding"},{"body":"second finding"}]}`)
	if _, err := coordinator.CompleteFromAgent(t.Context(), h, agentID); err != nil {
		t.Fatal(err)
	}
	current, err = f.work.WorkForSession(t.Context(), h.ID)
	if err != nil || current.Status != pullrequestwork.StatusCompleted {
		t.Fatalf("work with artifact: %+v err=%v", current, err)
	}
	comments, err := f.comments.ListByPullRequest(t.Context(), "pr")
	if err != nil || len(comments) != 2 {
		t.Fatalf("comments=%+v err=%v", comments, err)
	}
	for _, comment := range comments {
		if comment.PublicationState != pullrequestcomments.PublicationDraft || comment.AuthorType != pullrequestcomments.AuthorAgent {
			t.Fatalf("imported comment: %+v", comment)
		}
	}

	after, err := f.comments.PublishDrafts(t.Context(), "pr", f.head, []string{comments[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, comment := range after {
		published := comment.PublicationState != pullrequestcomments.PublicationDraft
		if published != (comment.ID == comments[0].ID) {
			t.Fatalf("only the selected draft should be published: %+v", comment)
		}
	}
}

func TestAssistedReviewWaitsForAllTabsBeforeImportingSharedArtifact(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        holons.Status
		activity      protocol.AgentActivity
		observability protocol.ObservabilityStatus
	}{
		{"working", holons.StatusRunning, protocol.ActivityWorking, protocol.ObservabilityHealthy},
		{"queued", holons.StatusQueued, protocol.ActivityIdle, protocol.ObservabilityHealthy},
		{"restoring", holons.StatusRestoring, protocol.ActivityIdle, protocol.ObservabilityHealthy},
		{"unobserved", holons.StatusRunning, protocol.ActivityIdle, protocol.ObservabilityDegraded},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newReviewDraftsFixture(t)
			_, h := f.start(t)
			writerID := h.AgentSessions[0].ID
			fork, err := f.runtime.AddForkedAgent(t.Context(), h.ID, writerID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.holons.SetAgentSessionStatus(t.Context(), h.ID, fork.ID, holons.StatusRunning, "", "fork"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.holons.UpdateAgentObservation(t.Context(), h.ID, fork.ID, "", "", "", string(protocol.InputTaskComplete), string(protocol.ObservabilityHealthy), "", protocol.ActivityCompleted, nil); err != nil {
				t.Fatal(err)
			}
			// Keep the earlier idle snapshot: completion must reload the writer's state.
			if _, err := f.holons.UpdateAgentObservation(t.Context(), h.ID, writerID, "", "", "", string(protocol.InputNone), string(test.observability), "", test.activity, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := f.holons.SetAgentSessionStatus(t.Context(), h.ID, writerID, test.status, "", "conversation"); err != nil {
				t.Fatal(err)
			}
			const partial = `{"comments":[{"body":"early finding"}]}`
			gitPlumbingWrite(t, h.WorktreePath, ".holark/review.json", partial)
			coordinator := localPullRequestWorkCompletionCoordinator{reviewMu: &f.runtime.recoveryMu, holons: f.holons, work: f.work}
			disposition, err := coordinator.CompleteFromAgent(t.Context(), h, fork.ID)
			if err != nil || disposition.Terminal || disposition.AlreadyHandled {
				t.Fatalf("fork completion: %+v err=%v", disposition, err)
			}
			current, err := f.work.WorkForSession(t.Context(), h.ID)
			if err != nil || current.Status != pullrequestwork.StatusRunning || current.ArtifactImported {
				t.Fatalf("unfinished review: %+v err=%v", current, err)
			}
			if comments, err := f.comments.ListByPullRequest(t.Context(), "pr"); err != nil || len(comments) != 0 {
				t.Fatalf("premature drafts: %+v err=%v", comments, err)
			}
			if data, err := os.ReadFile(filepath.Join(h.WorktreePath, ".holark/review.json")); err != nil || string(data) != partial {
				t.Fatalf("unfinished artifact changed: %q err=%v", data, err)
			}

			gitPlumbingWrite(t, h.WorktreePath, ".holark/review.json", `{"comments":[{"body":"early finding"},{"body":"later finding"}]}`)
			if _, err := f.holons.SetAgentSessionStatus(t.Context(), h.ID, writerID, holons.StatusRunning, "", "conversation"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.holons.UpdateAgentObservation(t.Context(), h.ID, writerID, "", "", "", string(protocol.InputTaskComplete), string(protocol.ObservabilityHealthy), "", protocol.ActivityCompleted, nil); err != nil {
				t.Fatal(err)
			}
			disposition, err = coordinator.CompleteFromAgent(t.Context(), h, writerID)
			if err != nil || !disposition.Terminal {
				t.Fatalf("writer completion: %+v err=%v", disposition, err)
			}
			current, err = f.work.WorkForSession(t.Context(), h.ID)
			if err != nil || current.Status != pullrequestwork.StatusCompleted {
				t.Fatalf("finished review: %+v err=%v", current, err)
			}
			comments, err := f.comments.ListByPullRequest(t.Context(), "pr")
			if err != nil || len(comments) != 2 {
				t.Fatalf("final drafts: %+v err=%v", comments, err)
			}
			for _, comment := range comments {
				if comment.PublicationState != pullrequestcomments.PublicationDraft || (comment.Body != "early finding" && comment.Body != "later finding") {
					t.Fatalf("incorrect final draft: %+v", comment)
				}
			}
			if _, err := os.Stat(filepath.Join(h.WorktreePath, ".holark/review.json")); !os.IsNotExist(err) {
				t.Fatalf("finished artifact not consumed: %v", err)
			}
		})
	}
}

func TestAssistedReviewKeepsFindingsRecoverableWhenHeadAdvances(t *testing.T) {
	f := newReviewDraftsFixture(t)
	_, h := f.start(t)
	agentID := h.AgentSessions[0].ID
	coordinator := localPullRequestWorkCompletionCoordinator{holons: f.holons, work: f.work}
	artifact := `{"comments":[{"body":"finding on the reviewed head"}]}`
	gitPlumbingWrite(t, h.WorktreePath, ".holark/review.json", artifact)
	gitPlumbingWrite(t, f.root, "file.txt", "advanced\n")
	gitPlumbingCommand(t, f.root, "commit", "-am", "Advance during review")
	advanced := gitPlumbingOutput(t, f.root, "rev-parse", "HEAD")
	if _, err := branchfixture.Accept(t.Context(), f.prs, "pr", f.base, advanced, f.base); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		disposition, err := coordinator.CompleteFromAgent(t.Context(), h, agentID)
		if err != nil || disposition.Terminal || disposition.AlreadyHandled {
			t.Fatalf("stale completion: %+v err=%v", disposition, err)
		}
		current, err := f.work.WorkForSession(t.Context(), h.ID)
		if err != nil || current.Status != pullrequestwork.StatusWaiting || current.ArtifactImported || current.CompletedAt != nil || !strings.Contains(current.Error, pullrequestwork.ErrStaleHead.Error()) {
			t.Fatalf("stale work: %+v err=%v", current, err)
		}
		if comments, err := f.comments.ListByPullRequest(t.Context(), "pr"); err != nil || len(comments) != 0 {
			t.Fatalf("stale findings imported: %+v err=%v", comments, err)
		}
		data, err := os.ReadFile(filepath.Join(h.WorktreePath, ".holark/review.json"))
		if err != nil || string(data) != artifact {
			t.Fatalf("artifact not preserved: %q err=%v", data, err)
		}
		currentHolon, err := f.holons.Get(t.Context(), h.ID)
		if err != nil {
			t.Fatal(err)
		}
		agent := currentHolon.AgentSession(agentID)
		if agent.Status != string(holons.StatusRunning) || agent.InputState != string(protocol.InputUserRequired) || agent.Activity != protocol.ActivityNeedsInput {
			t.Fatalf("conversation not recoverable: %+v", agent)
		}
	}

	// Restoring the reviewed head allows the preserved artifact to be retried.
	if _, err := branchfixture.Accept(t.Context(), f.prs, "pr", f.base, f.head, f.base); err != nil {
		t.Fatal(err)
	}
	disposition, err := coordinator.CompleteFromAgent(t.Context(), h, agentID)
	if err != nil || !disposition.Terminal || disposition.AlreadyHandled {
		t.Fatalf("retry: %+v err=%v", disposition, err)
	}
	current, err := f.work.WorkForSession(t.Context(), h.ID)
	if err != nil || current.Status != pullrequestwork.StatusCompleted || current.Error != "" {
		t.Fatalf("retried work: %+v err=%v", current, err)
	}
	comments, err := f.comments.ListByPullRequest(t.Context(), "pr")
	if err != nil || len(comments) != 1 || comments[0].PublicationState != pullrequestcomments.PublicationDraft || comments[0].OriginalHeadCommit != f.head {
		t.Fatalf("retried findings: %+v err=%v", comments, err)
	}
}

func TestAssistedReviewRejectsInvalidArtifactsAndAllowsCorrection(t *testing.T) {
	const validComment = `{"body":"valid finding"}`
	withInvalidComment := func(comment string) string {
		return `{"comments":[` + validComment + `,` + comment + `]}`
	}
	for _, test := range []struct {
		name, artifact, reason string
	}{
		{"scope", withInvalidComment(`{"body":"bad scope","scope":"lines"}`), "comments[1]"},
		{"side", withInvalidComment(`{"body":"bad side","scope":"line","path":"file.txt","side":"RIGTH","line":1}`), "comments[1]"},
		{"missing line", withInvalidComment(`{"body":"missing line","scope":"line","path":"file.txt","side":"RIGHT"}`), "comments[1]"},
		{"nonpositive line", withInvalidComment(`{"body":"bad line","scope":"line","path":"file.txt","side":"RIGHT","line":0}`), "comments[1]"},
		{"line type", withInvalidComment(`{"body":"bad line","scope":"line","path":"file.txt","side":"RIGHT","line":"one"}`), "invalid JSON"},
		{"blank body", withInvalidComment(`{"body":" "}`), "comments[1].body"},
		{"oversized body", withInvalidComment(fmt.Sprintf(`{"body":%q}`, strings.Repeat("é", maxPullRequestWorkTextCharacters+1))), "comments[1].body"},
		{"multibyte body", withInvalidComment(fmt.Sprintf(`{"body":%q}`, strings.Repeat("é", pullrequestcomments.MaximumBodyLength/2+1))), "UTF-8 bytes"},
		{"blank finding", `{"findings":["valid finding"," "]}`, "findings[1]"},
		{"oversized finding", fmt.Sprintf(`{"findings":["valid finding",%q]}`, strings.Repeat("é", maxPullRequestWorkTextCharacters+1)), "findings[1]"},
		{"multibyte finding", fmt.Sprintf(`{"findings":["valid finding",%q]}`, strings.Repeat("é", pullrequestcomments.MaximumBodyLength/2+1)), "UTF-8 bytes"},
		{"invalid location after 30 comments", `{"comments":[` + strings.Repeat(validComment+",", 30) + `{"body":"bad location","scope":"line","path":"file.txt","side":"RIGHT"}]}`, "comments[30]"},
		{"malformed JSON", `{"comments":[`, "invalid JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newReviewDraftsFixture(t)
			_, h := f.start(t)
			agentID := h.AgentSessions[0].ID
			coordinator := localPullRequestWorkCompletionCoordinator{holons: f.holons, work: f.work}
			gitPlumbingWrite(t, h.WorktreePath, ".holark/review.json", test.artifact)
			for range 2 {
				disposition, err := coordinator.CompleteFromAgent(t.Context(), h, agentID)
				if err != nil || disposition.Terminal || disposition.AlreadyHandled {
					t.Fatalf("invalid completion: %+v err=%v", disposition, err)
				}
				current, err := f.work.WorkForSession(t.Context(), h.ID)
				if err != nil || current.Status != pullrequestwork.StatusWaiting || current.CompletedAt != nil || !strings.Contains(current.Error, test.reason) {
					t.Fatalf("invalid work: %+v err=%v", current, err)
				}
				if comments, err := f.comments.ListByPullRequest(t.Context(), "pr"); err != nil || len(comments) != 0 {
					t.Fatalf("partially imported artifact: %+v err=%v", comments, err)
				}
				data, err := os.ReadFile(filepath.Join(h.WorktreePath, ".holark/review.json"))
				if err != nil || string(data) != test.artifact {
					t.Fatalf("artifact not preserved: %q err=%v", data, err)
				}
				currentHolon, err := f.holons.Get(t.Context(), h.ID)
				if err != nil {
					t.Fatal(err)
				}
				agent := currentHolon.AgentSession(agentID)
				if agent.Status != string(holons.StatusRunning) || agent.InputState != string(protocol.InputUserRequired) || agent.Activity != protocol.ActivityNeedsInput {
					t.Fatalf("conversation not recoverable: %+v", agent)
				}
			}

			// Correcting the artifact imports every finding, including values at the limits.
			body := strings.Repeat("é", pullrequestcomments.MaximumBodyLength/2)
			corrected := fmt.Sprintf(`{"findings":[%q],"comments":[`, body) + strings.Repeat(validComment+",", maxPullRequestReviewComments-2) + validComment + `]}`
			gitPlumbingWrite(t, h.WorktreePath, ".holark/review.json", corrected)
			disposition, err := coordinator.CompleteFromAgent(t.Context(), h, agentID)
			if err != nil || !disposition.Terminal {
				t.Fatalf("corrected completion: %+v err=%v", disposition, err)
			}
			current, err := f.work.WorkForSession(t.Context(), h.ID)
			if err != nil || current.Status != pullrequestwork.StatusCompleted || current.Error != "" {
				t.Fatalf("corrected work: %+v err=%v", current, err)
			}
			comments, err := f.comments.ListByPullRequest(t.Context(), "pr")
			if err != nil || len(comments) != maxPullRequestReviewComments {
				t.Fatalf("corrected drafts=%d err=%v", len(comments), err)
			}
			for _, comment := range comments {
				wantBody := "valid finding"
				if comment.SourceReviewIndex == 0 {
					wantBody = body
				}
				if comment.Body != wantBody || comment.PublicationState != pullrequestcomments.PublicationDraft {
					t.Fatalf("incorrect draft: %+v", comment)
				}
			}
			if _, err := os.Stat(filepath.Join(h.WorktreePath, ".holark/review.json")); !os.IsNotExist(err) {
				t.Fatalf("corrected artifact not consumed: %v", err)
			}
		})
	}
}

func TestAssistedReviewImportsEveryFindingBeyondThirty(t *testing.T) {
	f := newReviewDraftsFixture(t)
	_, h := f.start(t)
	coordinator := localPullRequestWorkCompletionCoordinator{holons: f.holons, work: f.work}
	var findings, comments []string
	for index := range 31 {
		findings = append(findings, fmt.Sprintf(`"finding %d"`, index))
		comments = append(comments, fmt.Sprintf(`{"body":"comment %d","scope":"line","path":"file.txt","side":"RIGHT","line":1}`, index))
	}
	artifact := `{"findings":[` + strings.Join(findings, ",") + `],"comments":[` + strings.Join(comments, ",") + `]}`
	gitPlumbingWrite(t, h.WorktreePath, ".holark/review.json", artifact)

	disposition, err := coordinator.CompleteFromAgent(t.Context(), h, h.AgentSessions[0].ID)
	if err != nil || !disposition.Terminal || disposition.AlreadyHandled {
		t.Fatalf("completion: %+v err=%v", disposition, err)
	}
	current, err := f.work.WorkForSession(t.Context(), h.ID)
	if err != nil || current.Status != pullrequestwork.StatusCompleted || current.Error != "" {
		t.Fatalf("completed work: %+v err=%v", current, err)
	}
	drafts, err := f.comments.ListByPullRequest(t.Context(), "pr")
	if err != nil || len(drafts) != 62 {
		t.Fatalf("drafts=%d, want 62, err=%v", len(drafts), err)
	}
	seen := make(map[int]bool)
	for _, draft := range drafts {
		index := draft.SourceReviewIndex
		wantBody := fmt.Sprintf("finding %d", index)
		wantScope := pullrequestcomments.ScopePullRequest
		if index >= 31 {
			wantBody = fmt.Sprintf("comment %d", index-31)
			wantScope = pullrequestcomments.ScopeLine
			if draft.Path != "file.txt" || draft.Side != "RIGHT" || draft.Line == nil || *draft.Line != 1 {
				t.Fatalf("incorrect location: %+v", draft)
			}
		}
		if index < 0 || index >= 62 || seen[index] || draft.Body != wantBody || draft.Scope != wantScope || draft.PublicationState != pullrequestcomments.PublicationDraft {
			t.Fatalf("incorrect draft: %+v", draft)
		}
		seen[index] = true
	}
	if _, err := os.Stat(filepath.Join(h.WorktreePath, ".holark/review.json")); !os.IsNotExist(err) {
		t.Fatalf("imported artifact not consumed: %v", err)
	}
}

func TestAssistedReviewRetriesFailedDraftImport(t *testing.T) {
	f := newReviewDraftsFixture(t)
	_, h := f.start(t)
	agentID := h.AgentSessions[0].ID
	coordinator := localPullRequestWorkCompletionCoordinator{holons: f.holons, work: f.work}
	const artifact = `{"comments":[{"body":"first finding"},{"body":"second finding"}]}`
	gitPlumbingWrite(t, h.WorktreePath, ".holark/review.json", artifact)
	if _, err := f.db.ExecContext(t.Context(), `create trigger reject_second_draft before insert on pull_request_comments
		when new.source_review_index = 1 begin select raise(abort, 'draft import unavailable'); end`); err != nil {
		t.Fatal(err)
	}
	var firstID string
	for range 2 {
		disposition, err := coordinator.CompleteFromAgent(t.Context(), h, agentID)
		if err != nil || disposition.Terminal || disposition.AlreadyHandled {
			t.Fatalf("failed import: %+v err=%v", disposition, err)
		}
		current, err := f.work.WorkForSession(t.Context(), h.ID)
		if err != nil || current.Status != pullrequestwork.StatusWaiting || current.CompletedAt != nil || !strings.Contains(current.Error, "draft import unavailable") {
			t.Fatalf("failed import work: %+v err=%v", current, err)
		}
		data, err := os.ReadFile(filepath.Join(h.WorktreePath, ".holark/review.json"))
		if err != nil || string(data) != artifact {
			t.Fatalf("artifact not preserved: %q err=%v", data, err)
		}
		currentHolon, err := f.holons.Get(t.Context(), h.ID)
		if err != nil {
			t.Fatal(err)
		}
		agent := currentHolon.AgentSession(agentID)
		if agent.Status != string(holons.StatusRunning) || agent.InputState != string(protocol.InputUserRequired) || agent.Activity != protocol.ActivityNeedsInput {
			t.Fatalf("conversation not recoverable: %+v", agent)
		}
		comments, err := f.comments.ListByPullRequest(t.Context(), "pr")
		if err != nil || len(comments) != 1 {
			t.Fatalf("partial import: %+v err=%v", comments, err)
		}
		if firstID == "" {
			firstID = comments[0].ID
		}
		if comments[0].ID != firstID || comments[0].PublicationState != pullrequestcomments.PublicationDraft {
			t.Fatalf("partial draft changed: %+v", comments[0])
		}
	}
	if _, err := f.db.ExecContext(t.Context(), `drop trigger reject_second_draft`); err != nil {
		t.Fatal(err)
	}
	disposition, err := coordinator.CompleteFromAgent(t.Context(), h, agentID)
	if err != nil || !disposition.Terminal || disposition.AlreadyHandled {
		t.Fatalf("retried import: %+v err=%v", disposition, err)
	}
	current, err := f.work.WorkForSession(t.Context(), h.ID)
	if err != nil || current.Status != pullrequestwork.StatusCompleted || current.CompletedAt == nil || current.Error != "" {
		t.Fatalf("retried work: %+v err=%v", current, err)
	}
	if _, err := os.Stat(filepath.Join(h.WorktreePath, ".holark/review.json")); !os.IsNotExist(err) {
		t.Fatalf("imported artifact not consumed: %v", err)
	}
	comments, err := f.comments.ListByPullRequest(t.Context(), "pr")
	if err != nil || len(comments) != 2 {
		t.Fatalf("retried drafts: %+v err=%v", comments, err)
	}
	for _, comment := range comments {
		if comment.PublicationState != pullrequestcomments.PublicationDraft || (comment.SourceReviewIndex == 0 && comment.ID != firstID) {
			t.Fatalf("incorrect retried draft: %+v", comment)
		}
	}
}

// Pause at the runtime boundary after the first tab expires, while another tab
// still keeps the workspace open. No agent CLI is launched by this test.
type reviewRetirementRuntime struct {
	localAgentRuntime
	started   chan struct{}
	release   chan struct{}
	cancelled []string
}

func (r *reviewRetirementRuntime) Cancel(ctx context.Context, _, agentID string) error {
	r.cancelled = append(r.cancelled, agentID)
	if len(r.cancelled) == 1 {
		close(r.started)
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func TestAssistedReviewRetiresAllTabsBeforeAdmittingAnother(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	f := newReviewDraftsFixture(t)
	_, h := f.start(t)
	source := h.AgentSessions[0].ID
	peer, err := f.runtime.AddForkedAgent(ctx, h.ID, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.holons.SetAgentSessionStatus(ctx, h.ID, peer.ID, holons.StatusRunning, "", "peer"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.holons.UpdateAgentObservation(ctx, h.ID, peer.ID, "", "", "", string(protocol.InputTaskComplete), string(protocol.ObservabilityHealthy), "", protocol.ActivityCompleted, nil); err != nil {
		t.Fatal(err)
	}
	gitPlumbingWrite(t, h.WorktreePath, ".holark/review.json", `{"comments":[{"body":"finding"}]}`)
	runtime := &reviewRetirementRuntime{started: make(chan struct{}), release: make(chan struct{})}
	f.runtime.workCompletion.agents = runtime
	state := localAgentState{holons: f.holons, workCompletion: f.runtime.workCompletion, agents: runtime}
	completed := make(chan error, 1)
	go func() { completed <- state.completePullRequestWork(ctx, h, source) }()
	select {
	case <-runtime.started:
	case <-ctx.Done():
		t.Fatal("review cleanup did not start")
	}
	admitted := make(chan error, 1)
	go func() {
		_, err := f.runtime.AddAgentSession(ctx, h.ID, "codex", "another review tab")
		admitted <- err
	}()
	select {
	case err := <-admitted:
		t.Fatalf("admission escaped unfinished cleanup: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(runtime.release)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	if err := <-admitted; !errors.Is(err, holons.ErrInvalid) {
		t.Fatalf("admission after cleanup: %v", err)
	}
	if len(runtime.cancelled) != 2 || runtime.cancelled[0] != source || runtime.cancelled[1] != peer.ID {
		t.Fatalf("retired agents: %v", runtime.cancelled)
	}
	retired, err := f.holons.Get(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range retired.AgentSessions {
		if agent.Status != string(holons.StatusExpired) {
			t.Fatalf("agent still active: %+v", agent)
		}
	}
	if retired.ArchivedAt == nil {
		t.Fatalf("review workspace not archived: %+v", retired)
	}
	if err := state.completePullRequestWork(ctx, h, peer.ID); err != nil {
		t.Fatal(err)
	}
	if len(runtime.cancelled) != 2 {
		t.Fatalf("duplicate completion repeated cleanup: %v", runtime.cancelled)
	}
}
