package localapp

import (
	"context"
	"errors"
	"fmt"
	holonshttp "github.com/holark-ai/holark/internal/holons/httpapi"
	holonssqlite "github.com/holark-ai/holark/internal/holons/sqliteadapter"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/prompt_templates"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	worksqlite "github.com/holark-ai/holark/internal/pullrequestwork/sqliteadapter"
)

type workLauncherRepository struct {
	path          string
	createdCommit string
	reopened      string
}

func (r *workLauncherRepository) CreateWorkspace(_ context.Context, id, commit string) (holons.Workspace, error) {
	r.createdCommit = commit
	return holons.Workspace{Path: r.path, Branch: "holark/" + id, BaseCommit: commit}, nil
}

func (*workLauncherRepository) InspectWorkspace(context.Context, string) (holons.WorkspaceInspection, error) {
	return holons.WorkspaceInspection{}, nil
}

func (*workLauncherRepository) PublishWorkspace(context.Context, string, holons.Publish) (holons.PublishedWorkspace, error) {
	return holons.PublishedWorkspace{}, nil
}

func (*workLauncherRepository) RemoveWorkspace(context.Context, string) error { return nil }
func (*workLauncherRepository) ArchiveWorkspace(context.Context, string, string) error {
	return nil
}
func (r *workLauncherRepository) ReopenWorkspace(_ context.Context, _ string, branch string) error {
	r.reopened = branch
	return nil
}

type workLauncherPromptReader struct {
	keys []string
}

func (r *workLauncherPromptReader) Read(_ context.Context, key string) string {
	r.keys = append(r.keys, key)
	return "{{pull_request_title}}\n{{comment_body}}\n{{mode_instruction}}\n{{artifact_path}}"
}

type workLauncherCommentReader struct {
	ids     []string
	comment pullrequestcomments.Comment
}

func (r *workLauncherCommentReader) Get(_ context.Context, id string) (pullrequestcomments.Comment, error) {
	r.ids = append(r.ids, id)
	if r.comment.Body != "" {
		return r.comment, nil
	}
	return pullrequestcomments.Comment{Body: "  Fix the bug. \n"}, nil
}

func (*workLauncherCommentReader) ListByPullRequest(context.Context, string) ([]pullrequestcomments.Comment, error) {
	return nil, nil
}

func TestContinueWorkLauncherOpensInteractiveHolonAtPullRequestHead(t *testing.T) {
	for _, tt := range []struct {
		name, title, prompt, wantTitle, wantPrompt string
		history                                    []pullrequestwork.Work
	}{
		{name: "blank", prompt: " \n\t ", wantTitle: "Follow Up 1"},
		{name: "existing follow ups", wantTitle: "Follow Up 3", history: []pullrequestwork.Work{
			{ID: "first", PullRequestID: "pr-1", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeContinue, SessionID: "holon-1", Status: pullrequestwork.StatusCompleted},
			{ID: "second", PullRequestID: "pr-1", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeContinue, SessionID: "holon-2", Status: pullrequestwork.StatusRunning},
		}},
		{name: "unrelated work", wantTitle: "Follow Up 1", history: []pullrequestwork.Work{
			{ID: "other-pr", PullRequestID: "pr-2", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeContinue, SessionID: "holon-other"},
			{ID: "comment", PullRequestID: "pr-1", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, SessionID: "holon-comment"},
			{ID: "review", PullRequestID: "pr-1", Kind: pullrequestwork.KindReview, SessionID: "holon-review"},
			{ID: "current", PullRequestID: "pr-1", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeContinue, Status: pullrequestwork.StatusQueued},
		}},
		{name: "supplied", prompt: " \n\t Continue the implementation \nInclude regression coverage. \n", wantTitle: "Follow Up 1", wantPrompt: "Continue the implementation \nInclude regression coverage."},
		{name: "custom title", title: "Finish the implementation", prompt: "Continue the implementation", wantTitle: "Finish the implementation", wantPrompt: "Continue the implementation"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, err := database.Open(filepath.Join(t.TempDir(), "work.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			works, err := worksqlite.New(t.Context(), db)
			if err != nil {
				t.Fatal(err)
			}
			if err := works.CreateBatch(t.Context(), tt.history); err != nil {
				t.Fatal(err)
			}
			_, store := terminalTestService(t)
			repository := &workLauncherRepository{path: t.TempDir()}
			service := holons.NewServiceWithRepository(store, repository)
			templates := &workLauncherPromptReader{}
			comments := &workLauncherCommentReader{}
			agents := &recordingAgentLauncher{}
			launcher := localWorkLauncher{
				holons:    &terminalHolonService{Service: service, agents: agents},
				harnesses: &harnessPreferencesStub{resolved: protocol.HarnessCodex, model: "workflow-model"},
				templates: templates,
				comments:  comments,
				works:     works,
			}
			pullRequest := pullrequestwork.PullRequest{
				ID: "pr-1", Title: "Pull request title", BaseBranch: "main",
				BaseCommit: "base-commit", DiffBaseCommit: "diff-base-commit",
				HeadBranch: "feature", HeadCommit: "head-commit",
			}
			work := pullrequestwork.Work{Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeContinue, CommentID: "unused-comment"}

			holonID, err := launcher.StartContinue(t.Context(), pullRequest, work, tt.prompt, pullrequestwork.ContinueOptions{Title: tt.title})
			if err != nil {
				t.Fatal(err)
			}
			created, err := service.Get(t.Context(), holonID)
			if err != nil {
				t.Fatal(err)
			}
			if len(agents.options) != 1 || agents.options[0] != (agentsessions.LaunchOptions{}) {
				t.Fatalf("continue launch options=%+v, want identity generation disabled", agents.options)
			}
			if created.Title != tt.wantTitle {
				t.Fatalf("continue holon title=%q, want %q", created.Title, tt.wantTitle)
			}
			if created.Prompt != tt.wantPrompt || len(created.AgentSessions) != 1 || created.AgentSessions[0].Prompt != tt.wantPrompt {
				t.Fatalf("continue holon prompt=%q agent_sessions=%+v, want prompt %q", created.Prompt, created.AgentSessions, tt.wantPrompt)
			}
			if created.AgentSessions[0].Model != "workflow-model" {
				t.Fatalf("PR workflow lost model: %+v", created.AgentSessions)
			}
			if len(templates.keys) != 0 || len(comments.ids) != 0 {
				t.Fatalf("continue loaded templates=%v comments=%v", templates.keys, comments.ids)
			}
			if created.Kind != holons.KindPullWorker || created.PullRequestID != pullRequest.ID || created.BaseBranch != pullRequest.BaseBranch || created.BaseCommit != pullRequest.DiffBaseCommit || created.UpstreamBranch != pullRequest.HeadBranch || created.UpstreamHeadCommit != pullRequest.HeadCommit || repository.createdCommit != pullRequest.HeadCommit {
				t.Fatalf("continue holon=%+v workspace commit=%q", created, repository.createdCommit)
			}
		})
	}
}

func TestCommentWorkLauncherPreservesTitleAndPrompt(t *testing.T) {
	for _, mode := range []pullrequestwork.Mode{pullrequestwork.ModeAuto, pullrequestwork.ModeAssisted} {
		for _, custom := range []bool{false, true} {
			name := string(mode) + "/template"
			if custom {
				name = string(mode) + "/custom"
			}
			t.Run(name, func(t *testing.T) {
				_, store := terminalTestService(t)
				service := holons.NewServiceWithRepository(store, &workLauncherRepository{path: t.TempDir()})
				templates := &workLauncherPromptReader{}
				comments := &workLauncherCommentReader{}
				agents := &recordingAgentLauncher{}
				launcher := localWorkLauncher{holons: &terminalHolonService{Service: service, agents: agents}, templates: templates, comments: comments}
				pullRequest := pullrequestwork.PullRequest{ID: "pr-1", Title: "Pull request title", BaseBranch: "main", BaseCommit: "base", HeadBranch: "feature", HeadCommit: "head"}
				work := pullrequestwork.Work{Kind: pullrequestwork.KindWorker, Mode: mode, CommentID: "comment-1"}
				prompt := ""
				modeInstruction := "Run independently without waiting for user input. Commit the fix before finishing; only ask for user input if you are truly blocked."
				if mode == pullrequestwork.ModeAssisted {
					modeInstruction = "You may ask the user for clarification if needed. Do not commit changes unless the user explicitly approves a proposed commit message; if the fix is ready with uncommitted changes, finish so Holark can request commit approval."
				}
				wantPrompt := "Pull request title\nFix the bug.\n" + modeInstruction + "\n.holark/comment-reply.json"
				if custom {
					prompt = " \nCustom instructions\nMore details. \n"
					wantPrompt = "Custom instructions\nMore details."
				}
				holonID, err := launcher.Start(t.Context(), pullRequest, work, prompt)
				if err != nil {
					t.Fatal(err)
				}
				created, err := service.Get(t.Context(), holonID)
				if err != nil {
					t.Fatal(err)
				}
				if len(agents.options) != 1 || agents.options[0] != (agentsessions.LaunchOptions{GenerateIdentity: true}) {
					t.Fatalf("comment launch options=%+v, want branch naming only", agents.options)
				}
				if created.Title != "Address pull request comments" || created.Kind != holons.KindPullWorker {
					t.Fatalf("comment holon title=%q kind=%q", created.Title, created.Kind)
				}
				if created.Prompt != wantPrompt || len(created.AgentSessions) != 1 || created.AgentSessions[0].Prompt != wantPrompt {
					t.Fatalf("comment holon prompt=%q agent_sessions=%+v, want prompt %q", created.Prompt, created.AgentSessions, wantPrompt)
				}
				if len(comments.ids) != 1 || comments.ids[0] != work.CommentID {
					t.Fatalf("comment lookups=%v, want [%s]", comments.ids, work.CommentID)
				}
				if custom {
					if len(templates.keys) != 0 {
						t.Fatalf("custom prompt loaded templates=%v", templates.keys)
					}
				} else if len(templates.keys) != 1 || templates.keys[0] != prompttemplates.PullRequestWorkerKey {
					t.Fatalf("template reads=%v, want [%s]", templates.keys, prompttemplates.PullRequestWorkerKey)
				}
			})
		}
	}
}

func TestCommentWorkLauncherIncludesReviewLocation(t *testing.T) {
	line := 42
	_, store := terminalTestService(t)
	service := holons.NewServiceWithRepository(store, &workLauncherRepository{path: t.TempDir()})
	comments := &workLauncherCommentReader{comment: pullrequestcomments.Comment{
		Body: "Fix the bug.", Scope: pullrequestcomments.ScopeLine, Path: "internal/example.go",
		Side: "RIGHT", Line: &line, OriginalHeadCommit: "reviewed-head",
	}}
	launcher := localWorkLauncher{
		holons:    &terminalHolonService{Service: service, agents: &recordingAgentLauncher{}},
		templates: &workLauncherPromptReader{}, comments: comments,
	}
	pullRequest := pullrequestwork.PullRequest{ID: "pr-1", Title: "Pull request title", BaseBranch: "main", BaseCommit: "base", HeadBranch: "feature", HeadCommit: "current-head"}
	holonID, err := launcher.Start(t.Context(), pullRequest, pullrequestwork.Work{Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, CommentID: "comment-1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Get(t.Context(), holonID)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"Fix the bug.", "Comment location: internal/example.go:42 (RIGHT)", "Reviewed commit: reviewed-head"} {
		if !strings.Contains(created.Prompt, value) {
			t.Fatalf("prompt does not contain %q: %s", value, created.Prompt)
		}
	}
}

type continueWorkControllerStub struct {
	work         pullrequestwork.Work
	publishCalls int
	failCalls    int
}

func (controller *continueWorkControllerStub) WorkForSession(_ context.Context, sessionID string) (pullrequestwork.Work, error) {
	if controller.work.SessionID != sessionID {
		return pullrequestwork.Work{}, pullrequestwork.ErrNotFound
	}
	return controller.work, nil
}

func (controller *continueWorkControllerStub) PublishContinueSession(_ context.Context, sessionID string, options pullrequestwork.PublicationOptions) (pullrequestwork.Work, error) {
	controller.publishCalls++
	if controller.work.SessionID != sessionID {
		return pullrequestwork.Work{}, pullrequestwork.ErrNotFound
	}
	controller.work.HeadCommit = "published"
	controller.work.BaseHeadCommit = "published"
	controller.work.ResultHeadCommit = "published"
	return controller.work, nil
}

func (controller *continueWorkControllerStub) Fail(_ context.Context, id, _ string) error {
	if controller.work.ID != id {
		return pullrequestwork.ErrNotFound
	}
	controller.failCalls++
	controller.work.Status = pullrequestwork.StatusFailed
	return nil
}

func TestTerminalHolonPublicationDelegatesExactContinueSession(t *testing.T) {
	service, _ := terminalTestService(t)
	controller := &continueWorkControllerStub{work: pullrequestwork.Work{
		ID: "worker", SessionID: "h", PullRequestID: "pr-1", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeContinue, Status: pullrequestwork.StatusRunning,
	}}
	runtime := &terminalHolonService{Service: service, work: controller}
	if _, err := runtime.Publish(t.Context(), "h", holons.Publish{}); err != nil {
		t.Fatal(err)
	}
	if controller.publishCalls != 1 || controller.work.ResultHeadCommit != "published" {
		t.Fatalf("calls=%d work=%+v", controller.publishCalls, controller.work)
	}
}

func TestCancellingContinueHolonSettlesWorker(t *testing.T) {
	service, store := terminalTestService(t)
	h, err := service.Get(t.Context(), "h")
	if err != nil {
		t.Fatal(err)
	}
	h.ID = "continue-holon"
	h.AgentSessions = []holons.AgentSession{{ID: "agent", HolonID: h.ID, AgentType: "codex", Status: string(holons.StatusRunning), CreatedAt: h.CreatedAt, UpdatedAt: h.CreatedAt}}
	if err = store.Create(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	controller := &continueWorkControllerStub{work: pullrequestwork.Work{
		ID: "worker", SessionID: h.ID, PullRequestID: "pr-1", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeContinue, Status: pullrequestwork.StatusRunning,
	}}
	runtime := &terminalHolonService{Service: service, work: controller}
	holon, err := runtime.End(t.Context(), h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if holon.Status != holons.StatusCancelled || controller.failCalls != 1 || controller.work.Status != pullrequestwork.StatusFailed {
		t.Fatalf("holon=%+v calls=%d work=%+v", holon, controller.failCalls, controller.work)
	}
}

// These tests exercise the queue, local launcher, holon service, and SQLite
// together. The launch port is observed without invoking or faking an agent CLI.
type reservedWorkCatalog struct {
	pr       pullrequestwork.PullRequest
	prepare  func(context.Context) error
	resolved bool
}

func (c *reservedWorkCatalog) PullRequest(string) (pullrequestwork.PullRequest, bool) {
	return c.pr, true
}
func (c *reservedWorkCatalog) SyncPullRequest(context.Context, string) error { return nil }
func (c *reservedWorkCatalog) PrepareWork(ctx context.Context, _ string) error {
	if c.prepare != nil {
		if err := c.prepare(ctx); err != nil {
			return err
		}
	}
	c.pr.BaseCommit, c.pr.DiffBaseCommit, c.pr.HeadCommit = "new-base", "new-diff", "new-head"
	return nil
}
func (c *reservedWorkCatalog) AddressComment(context.Context, string, string) (bool, error) {
	return c.resolved, nil
}

type reservedWorkRepository struct {
	workLauncherRepository
	prepare func(context.Context, string) error
}

func (r *reservedWorkRepository) CreateWorkspace(ctx context.Context, id, commit string) (holons.Workspace, error) {
	if r.prepare != nil {
		if err := r.prepare(ctx, id); err != nil {
			return holons.Workspace{}, err
		}
	}
	return r.workLauncherRepository.CreateWorkspace(ctx, id, commit)
}

type reservedWorkFixture struct {
	works      *worksqlite.Store
	holons     *holonssqlite.Store
	repository *reservedWorkRepository
	catalog    *reservedWorkCatalog
	runtime    *terminalHolonService
	launcher   localWorkLauncher
	launches   int
	launchErr  error
}

func newReservedWorkFixture(t *testing.T) *reservedWorkFixture {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "reserved.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	hs, err := holonssqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := worksqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	f := &reservedWorkFixture{works: ws, holons: hs, repository: &reservedWorkRepository{workLauncherRepository: workLauncherRepository{path: t.TempDir()}}, catalog: &reservedWorkCatalog{pr: pullrequestwork.PullRequest{ID: "pr", Active: true, BaseBranch: "main", BaseCommit: "base", DiffBaseCommit: "diff", HeadBranch: "feature", HeadCommit: "head"}}}
	f.runtime = &terminalHolonService{Service: holons.NewServiceWithRepository(hs, f.repository)}
	f.runtime.agents = reservationLauncher{holonAgentLauncher: &recordingAgentLauncher{}, launch: func(ctx context.Context, hid, aid string) error {
		f.launches++
		if f.launchErr != nil {
			return f.launchErr
		}
		return (localAgentState{holons: f.runtime.Service}).Running(ctx, hid, aid)
	}}
	f.launcher = localWorkLauncher{holons: f.runtime, works: ws, comments: &workLauncherCommentReader{}}
	return f
}
func (f *reservedWorkFixture) service() *pullrequestwork.Service {
	s := pullrequestwork.New(f.works, f.catalog, f.launcher, nil)
	s.SetComments(f.catalog)
	s.SetWorkerRuntime(localWorkRuntime{holons: f.runtime})
	return s
}
func (f *reservedWorkFixture) admit(t *testing.T, s *pullrequestwork.Service, comments ...string) []pullrequestwork.Work {
	t.Helper()
	w, err := s.Start(t.Context(), pullrequestwork.Start{PullRequestID: "pr", RequestID: "incoming", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, CommentIDs: comments})
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func (f *reservedWorkFixture) linked(t *testing.T, id string) (pullrequestwork.Work, holons.Holon) {
	t.Helper()
	w, err := f.works.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.holons.Get(t.Context(), w.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if h.PullRequestID != w.PullRequestID {
		t.Fatalf("unlinked holon: %+v %+v", w, h)
	}
	return w, h
}
func waitReservedWork[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("queued work did not reach expected boundary")
		var zero T
		return zero
	}
}

func TestQueuedWorkReservesBeforeSynchronizationAndWorkspace(t *testing.T) {
	f := newReservedWorkFixture(t)
	syncEntered, workspaceEntered := make(chan struct{}), make(chan struct{})
	syncRelease, workspaceRelease := make(chan struct{}), make(chan struct{})
	f.catalog.prepare = func(ctx context.Context) error {
		close(syncEntered)
		select {
		case <-syncRelease:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.repository.prepare = func(ctx context.Context, _ string) error {
		close(workspaceEntered)
		select {
		case <-workspaceRelease:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s := f.service()
	jobs := f.admit(t, s, "comment")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Dispatch(ctx) }()
	waitReservedWork(t, syncEntered)
	w, h := f.linked(t, jobs[0].ID)
	if w.Status != pullrequestwork.StatusQueued || h.Status != holons.StatusPreparing || h.WorktreePath != "" || len(h.AgentSessions) != 0 || len(h.ManualTerminals) != 0 {
		t.Fatalf("synchronization started without empty reservation: %+v %+v", w, h)
	}
	handler := holonshttp.New(f.runtime)
	for _, path := range []string{"/api/v1/holons", "/api/v1/holons/" + h.ID} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"status":"preparing"`) {
			t.Fatalf("first reservation not readable: %d %s", response.Code, response.Body)
		}
	}
	id := h.ID
	close(syncRelease)
	waitReservedWork(t, workspaceEntered)
	w, h = f.linked(t, jobs[0].ID)
	if w.Status != pullrequestwork.StatusRunning || h.ID != id || h.Status != holons.StatusPreparing || h.WorktreePath != "" || len(h.AgentSessions) != 0 {
		t.Fatalf("workspace setup lost reservation: %+v %+v", w, h)
	}
	close(workspaceRelease)
	if err := waitReservedWork(t, done); err != nil {
		t.Fatal(err)
	}
	w, h = f.linked(t, jobs[0].ID)
	if f.launches != 1 || h.ID != id || h.BaseCommit != "new-diff" || h.WorkSessionStartCommit != "new-head" || f.repository.createdCommit != "new-head" || h.WorktreePath != f.repository.path || h.WorktreeBranch == "" || h.Prompt == "" || len(h.AgentSessions) != 1 || h.AgentSessions[0].Prompt != h.Prompt {
		t.Fatalf("final setup not persisted: %+v %+v", w, h)
	}
}

func TestQueuedSynchronizationRetryAndRestartReuseReservation(t *testing.T) {
	f := newReservedWorkFixture(t)
	s := f.service()
	jobs := f.admit(t, s, "first", "second")
	failure := errors.New("sync unavailable")
	f.catalog.prepare = func(context.Context) error { return failure }
	var id string
	for range 2 {
		if err := s.Dispatch(t.Context()); !errors.Is(err, failure) {
			t.Fatal(err)
		}
		w, h := f.linked(t, jobs[0].ID)
		if id != "" && id != h.ID {
			t.Fatal("retry changed reservation")
		}
		id = h.ID
		second, err := f.works.Get(t.Context(), jobs[1].ID)
		if err != nil || w.Error != "" || h.Reason != "" || w.Status != pullrequestwork.StatusQueued || w.StartedAt != nil || second.SessionID != "" || f.launches != 0 {
			t.Fatalf("retry changed queue: %+v %+v %+v %v", w, h, second, err)
		}
	}
	s = f.service()
	if err := s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, h := f.linked(t, jobs[0].ID)
	if h.Status != holons.StatusPreparing {
		t.Fatalf("restart settled synchronization reservation: %+v", h)
	}
	f.catalog.prepare = nil
	for range 2 {
		if err := s.Dispatch(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	w, h := f.linked(t, jobs[0].ID)
	all, err := f.holons.List(t.Context())
	if err != nil || len(all) != 1 || h.ID != id || w.Status != pullrequestwork.StatusRunning || f.launches != 1 {
		t.Fatalf("recovery duplicated launch: %+v %+v %v", w, all, err)
	}
}

func TestQueuedSetupFailuresBelongToIncomingWork(t *testing.T) {
	for _, phase := range []string{"workspace", "prompt", "launch"} {
		t.Run(phase, func(t *testing.T) {
			f := newReservedWorkFixture(t)
			s := f.service()
			now := time.Now().UTC()
			predecessor := pullrequestwork.Work{ID: "predecessor", PullRequestID: "pr", SessionID: "predecessor-holon", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusCompleted, CreatedAt: now.Add(-time.Hour), CompletedAt: &now}
			if err := f.works.Create(t.Context(), predecessor); err != nil {
				t.Fatal(err)
			}
			if err := f.holons.Create(t.Context(), holons.Holon{ID: predecessor.SessionID, Kind: holons.KindPullWorker, PullRequestID: "pr", Status: holons.StatusCompleted, CreatedAt: predecessor.CreatedAt, FinishedAt: &now}); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("incoming " + phase + " failed")
			switch phase {
			case "workspace":
				f.repository.prepare = func(context.Context, string) error { return failure }
			case "prompt":
				f.launcher.comments = nil
				s = f.service()
			case "launch":
				f.launchErr = failure
			}
			jobs := f.admit(t, s, "comment")
			if err := s.Dispatch(t.Context()); err != nil {
				t.Fatal(err)
			}
			w, h := f.linked(t, jobs[0].ID)
			if w.Status != pullrequestwork.StatusFailed || h.Status != holons.StatusFailed || w.Error == "" || h.Reason != w.Error || w.CompletedAt == nil || h.FinishedAt == nil {
				t.Fatalf("failure lost incoming ownership: %+v %+v", w, h)
			}
			before, previous := f.linked(t, predecessor.ID)
			if before.Status != pullrequestwork.StatusCompleted || previous.Status != holons.StatusCompleted || before.Error != "" || previous.Reason != "" {
				t.Fatalf("predecessor changed: %+v %+v", before, previous)
			}
			f.repository.prepare = nil
			f.launchErr = nil
			f.launcher.comments = &workLauncherCommentReader{}
			s = f.service()
			next := f.admit(t, s, "next")
			if err := s.Dispatch(t.Context()); err != nil {
				t.Fatal(err)
			}
			running, _ := f.linked(t, next[0].ID)
			if running.Status != pullrequestwork.StatusRunning {
				t.Fatalf("failure stranded queue: %+v", running)
			}
		})
	}
}

func TestQueuedReservationCancellationAndInterruptedSetup(t *testing.T) {
	for _, scenario := range []string{"queue cancel", "end holon", "end during sync", "restart setup", "restart agent reservation"} {
		t.Run(scenario, func(t *testing.T) {
			f := newReservedWorkFixture(t)
			s := f.service()
			jobs := f.admit(t, s, "first", "second")
			f.catalog.prepare = func(context.Context) error { return errors.New("offline") }
			if err := s.Dispatch(t.Context()); err == nil {
				t.Fatal("expected synchronization failure")
			}
			w, h := f.linked(t, jobs[0].ID)
			switch scenario {
			case "queue cancel":
				if _, err := s.Cancel(t.Context(), "pr", w.ID); err != nil {
					t.Fatal(err)
				}
			case "end holon":
				if _, err := f.runtime.End(t.Context(), h.ID); err != nil {
					t.Fatal(err)
				}
			case "restart setup", "restart agent reservation":
				w.Status = pullrequestwork.StatusRunning
				if err := f.works.Update(t.Context(), w); err != nil {
					t.Fatal(err)
				}
				if scenario == "restart agent reservation" {
					_, err := f.runtime.Service.PrepareReserved(t.Context(), h.ID, holons.Create{Kind: holons.KindPullWorker, PullRequestID: "pr", BaseCommit: "base", AgentType: "codex"})
					if err != nil {
						t.Fatal(err)
					}
				}
				s = f.service()
				if err := s.Recover(t.Context()); err != nil {
					t.Fatal(err)
				}
			case "end during sync":
				f.catalog.prepare = func(ctx context.Context) error { _, err := f.runtime.End(ctx, h.ID); return err }

			}
			if scenario != "end during sync" {
				f.catalog.prepare = nil
			}
			if err := s.Dispatch(t.Context()); err != nil {
				t.Fatal(err)
			}
			first, firstHolon := f.linked(t, jobs[0].ID)
			want := pullrequestwork.StatusCancelled
			if strings.HasPrefix(scenario, "restart") {
				want = pullrequestwork.StatusFailed
			}
			if first.Status != want || !holons.IsTerminal(firstHolon.Status) {
				t.Fatalf("reservation stranded: %+v %+v", first, firstHolon)
			}
			next, _ := f.linked(t, jobs[1].ID)
			if next.Status != pullrequestwork.StatusRunning {
				t.Fatalf("queue stranded: %+v", next)
			}

			if f.launches != 1 {
				t.Fatalf("launches=%d", f.launches)
			}
		})
	}
}

func TestResolvedQueuedCommentSkipsReservationOrSettlesIt(t *testing.T) {
	for _, duringSync := range []bool{false, true} {
		t.Run(fmt.Sprint(duringSync), func(t *testing.T) {
			f := newReservedWorkFixture(t)
			s := f.service()
			jobs := f.admit(t, s, "comment")
			if duringSync {
				f.catalog.prepare = func(context.Context) error { f.catalog.resolved = true; return nil }
			} else {
				f.catalog.resolved = true
			}
			if err := s.Dispatch(t.Context()); err != nil {
				t.Fatal(err)
			}
			w, err := f.works.Get(t.Context(), jobs[0].ID)
			if err != nil || w.Status != pullrequestwork.StatusSkipped || f.launches != 0 {
				t.Fatalf("resolved comment launched: %+v %v", w, err)
			}
			all, err := f.holons.List(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if duringSync {
				if len(all) != 1 || all[0].Status != holons.StatusCompleted || all[0].WorktreePath != "" || len(all[0].AgentSessions) != 0 {
					t.Fatalf("reservation not settled: %+v", all)
				}
			} else if len(all) != 0 || w.SessionID != "" {
				t.Fatalf("resolved comment reserved: %+v %+v", w, all)
			}
		})
	}
}

type reservationWriteFailure struct {
	*holonssqlite.Store
	stage string
	after bool
}

var errReservationWrite = errors.New("reservation write unavailable")

func (s *reservationWriteFailure) Create(ctx context.Context, h holons.Holon) error {
	if s.stage != "reserve" {
		return s.Store.Create(ctx, h)
	}
	if s.after {
		if err := s.Store.Create(ctx, h); err != nil {
			return err
		}
	}
	return errReservationWrite
}
func (s *reservationWriteFailure) CompleteReservation(ctx context.Context, h holons.Holon) error {
	if s.stage != "setup" {
		return s.Store.CompleteReservation(ctx, h)
	}
	if s.after {
		if err := s.Store.CompleteReservation(ctx, h); err != nil {
			return err
		}
	}
	return errReservationWrite
}
func (s *reservationWriteFailure) Update(ctx context.Context, h holons.Holon) error {
	if s.stage != "outcome" || h.Status != holons.StatusFailed {
		return s.Store.Update(ctx, h)
	}
	if s.after {
		if err := s.Store.Update(ctx, h); err != nil {
			return err
		}
	}
	return errReservationWrite
}

type reservedWorkWriteFailure struct {
	*worksqlite.Store
	stage string
	after bool
}

func (s *reservedWorkWriteFailure) Update(ctx context.Context, w pullrequestwork.Work) error {
	hit := (s.stage == "link" && w.Status == pullrequestwork.StatusQueued && w.SessionID != "") || (s.stage == "execution" && w.Status == pullrequestwork.StatusRunning) || (s.stage == "work outcome" && w.Status == pullrequestwork.StatusFailed)
	if !hit {
		return s.Store.Update(ctx, w)
	}
	if s.after {
		if err := s.Store.Update(ctx, w); err != nil {
			return err
		}
	}
	return errReservationWrite
}

func TestQueuedReservationPersistenceStopsSideEffects(t *testing.T) {
	for _, stage := range []string{"link", "reserve", "execution", "setup", "outcome", "work outcome"} {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/committed=%t", stage, after), func(t *testing.T) {
				f := newReservedWorkFixture(t)
				hs := &reservationWriteFailure{Store: f.holons, stage: stage, after: after}
				ws := &reservedWorkWriteFailure{Store: f.works, stage: stage, after: after}
				f.runtime.Service = holons.NewServiceWithRepository(hs, f.repository)
				s := pullrequestwork.New(ws, f.catalog, f.launcher, nil)
				s.SetWorkerRuntime(localWorkRuntime{holons: f.runtime})
				syncs, workspaces := 0, 0
				f.catalog.prepare = func(context.Context) error { syncs++; return nil }
				f.repository.prepare = func(context.Context, string) error {
					workspaces++
					if stage == "outcome" || stage == "work outcome" {
						return errors.New("workspace failed")
					}
					return nil
				}
				jobs := f.admit(t, s, "first", "second")
				if err := s.Dispatch(t.Context()); !errors.Is(err, errReservationWrite) {
					t.Fatalf("dispatch=%v", err)
				}
				second, err := f.works.Get(t.Context(), jobs[1].ID)
				if err != nil || second.SessionID != "" || f.launches != 0 {
					t.Fatalf("uncertain persistence advanced queue: %+v launches=%d error=%v", second, f.launches, err)
				}
				if (stage == "link" || stage == "reserve") && (syncs != 0 || workspaces != 0) {
					t.Fatalf("preparation crossed failed reservation: sync=%d workspace=%d", syncs, workspaces)
				}
				if stage == "execution" && workspaces != 0 {
					t.Fatal("workspace crossed failed execution checkpoint")
				}
				if stage == "setup" {
					_, h := f.linked(t, jobs[0].ID)
					if len(h.AgentSessions) != 0 {
						t.Fatalf("agent crossed failed setup checkpoint: %+v", h)
					}
				}
				hs.stage, ws.stage = "", ""
				f.repository.prepare = nil
				// Recovery settles interrupted execution; queued reservations reuse the ID.
				before, _ := f.works.Get(t.Context(), jobs[0].ID)
				if err := s.Recover(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := s.Dispatch(t.Context()); err != nil {
					t.Fatal(err)
				}
				first, _ := f.works.Get(t.Context(), jobs[0].ID)
				if before.SessionID != "" && first.SessionID != before.SessionID {
					t.Fatal("recovery replaced durable reservation ID")
				}
				if f.launches != 1 {
					t.Fatalf("queue recovery launches=%d", f.launches)
				}
			})
		}
	}
}

func TestReservedHolonEndedAfterSetupCannotLaunch(t *testing.T) {
	f := newReservedWorkFixture(t)
	w := pullrequestwork.Work{SessionID: "reserved", PullRequestID: "pr", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto}
	if err := f.launcher.Reserve(t.Context(), f.catalog.pr, w); err != nil {
		t.Fatal(err)
	}
	_, err := f.runtime.prepareAndStartReserved(t.Context(), w.SessionID, holons.Create{Kind: holons.KindPullWorker, PullRequestID: "pr", BaseCommit: "base", AgentType: "codex"}, agentsessions.LaunchOptions{}, func(h holons.Holon) error {
		_, err := f.runtime.End(t.Context(), h.ID)
		return err
	})
	if !errors.Is(err, holons.ErrReservationEnded) || f.launches != 0 {
		t.Fatalf("ended reservation launched: %v launches=%d", err, f.launches)
	}
	if err := f.launcher.Reserve(t.Context(), f.catalog.pr, w); !errors.Is(err, pullrequestwork.ErrReservationEnded) {
		t.Fatalf("ended reservation reactivated: %v", err)
	}
	h, err := f.holons.Get(t.Context(), w.SessionID)
	if err != nil || !h.EndRequested || h.Status != holons.StatusCancelled || h.AgentSessions[0].TerminalID != "" {
		t.Fatalf("ended reservation changed: %+v %v", h, err)
	}
}

type notifyingContinueFailure struct {
	*pullrequestwork.Service
	entered chan struct{}
}

func (s notifyingContinueFailure) Fail(ctx context.Context, id, reason string) error {
	close(s.entered)
	return s.Service.Fail(ctx, id, reason)
}

func TestQueuedLaunchDoesNotWaitForUnrelatedContinueCancellation(t *testing.T) {
	f := newReservedWorkFixture(t)
	s := f.service()
	now := time.Now().UTC()
	if err := f.holons.Create(t.Context(), holons.Holon{ID: "continue", PullRequestID: "pr", Kind: holons.KindPullWorker, Status: holons.StatusRunning, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := f.works.Create(t.Context(), pullrequestwork.Work{ID: "continue-work", SessionID: "continue", PullRequestID: "pr", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeContinue, Status: pullrequestwork.StatusRunning, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	ending := make(chan struct{})
	f.runtime.work = notifyingContinueFailure{Service: s, entered: ending}
	entered, release := make(chan struct{}), make(chan struct{})
	f.repository.prepare = func(ctx context.Context, _ string) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.admit(t, s, "comment")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dispatched, ended := make(chan error, 1), make(chan error, 1)
	go func() { dispatched <- s.Dispatch(ctx) }()
	waitReservedWork(t, entered)
	go func() { _, err := f.runtime.End(ctx, "continue"); ended <- err }()
	// End holds its runtime recovery lock and is about to wait for the queue
	// lock. Reserved launch must not wait for that unrelated runtime lock.
	waitReservedWork(t, ending)
	close(release)
	if err := waitReservedWork(t, dispatched); err != nil {
		t.Fatal(err)
	}
	if err := waitReservedWork(t, ended); err != nil {
		t.Fatal(err)
	}
	if f.launches != 1 {
		t.Fatalf("launches=%d", f.launches)
	}
}
