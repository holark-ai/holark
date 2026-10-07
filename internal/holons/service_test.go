package holons

import (
	"context"
	"errors"
	"testing"
	"time"
)

type repositorySpy struct {
	createdCommit, publishedExpected string
	renamedExpected, renamedSlug     string
	archivedBranch                   string
	reopenedBranch                   string
	removed                          bool
	renameErr, archiveErr            error
	inspection                       WorkspaceInspection
	inspectOptions                   InspectOptions
}

func (r *repositorySpy) CreateWorkspace(_ context.Context, id, commit string) (Workspace, error) {
	r.createdCommit = commit
	return Workspace{Path: "/worktrees/" + id, Branch: "holark/" + id, BaseCommit: commit}, nil
}
func (r *repositorySpy) InspectWorkspace(context.Context, string) (WorkspaceInspection, error) {
	return r.inspection, nil
}
func (r *repositorySpy) InspectWorkspaceWithOptions(_ context.Context, _ string, options InspectOptions) (WorkspaceInspection, error) {
	r.inspectOptions = options
	return r.inspection, nil
}

func TestInspectionPreservesRichWorkspaceProjection(t *testing.T) {
	store := &compileStore{}
	store.h = Holon{ID: "h", BaseBranch: "main", BaseCommit: "base", WorktreeBranch: "holark/h"}
	repo := &repositorySpy{inspection: WorkspaceInspection{Branch: "holark/h", BaseCommit: "base", HeadCommit: "head", Clean: false, Changes: []WorkspaceChange{{Path: "file.go", Status: "M", Patch: "@@", Binary: true, Truncated: true}}}}
	got, e := NewServiceWithRepository(store, repo).InspectWorkspace(context.Background(), "h")
	if e != nil {
		t.Fatal(e)
	}
	if got.Branch != "holark/h" || got.BaseBranch != "main" || got.BaseCommit != "base" || got.HeadCommit != "head" || !got.HasChanges || !got.Dirty || got.Clean || len(got.Changes) != 1 || got.Changes[0].Patch != "@@" || !got.Changes[0].Binary || !got.Changes[0].Truncated {
		t.Fatalf("inspection=%+v", got)
	}
	if repo.inspectOptions.BaseBranch != "main" || repo.inspectOptions.BranchBaseCommit != "base" {
		t.Fatalf("inspection boundaries=%+v", repo.inspectOptions)
	}
}
func (r *repositorySpy) PublishWorkspace(_ context.Context, _ string, p Publish) (PublishedWorkspace, error) {
	r.publishedExpected = p.ExpectedRemoteHead
	return PublishedWorkspace{UpstreamBranch: p.UpstreamBranch, HeadCommit: "new-head"}, nil
}
func (r *repositorySpy) RemoveWorkspace(context.Context, string) error { r.removed = true; return nil }
func (r *repositorySpy) ArchiveWorkspace(_ context.Context, _ string, branch string) error {
	r.archivedBranch = branch
	return r.archiveErr
}
func (r *repositorySpy) ReopenWorkspace(_ context.Context, _ string, branch string) error {
	r.reopenedBranch = branch
	return nil
}
func (r *repositorySpy) RenameWorkspace(_ context.Context, _ string, expected, slug string) (string, error) {
	r.renamedExpected, r.renamedSlug = expected, slug
	if r.renameErr != nil {
		return "", r.renameErr
	}
	return "holark/" + slug, nil
}

type memoryStore struct {
	h         Holon
	updateErr error
}

func (m *memoryStore) Create(_ context.Context, h Holon) error    { m.h = h; return nil }
func (m *memoryStore) Get(context.Context, string) (Holon, error) { return m.h, nil }
func (m *memoryStore) List(context.Context) ([]Holon, error)      { return []Holon{m.h}, nil }
func (m *memoryStore) Update(_ context.Context, h Holon) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.h = h
	return nil
}

// compileStore supplies intentionally unused methods for this coordinator test.
type compileStore struct {
	memoryStore
}

func (m *compileStore) SetStatus(_ context.Context, _ string, status Status, _ time.Time) (Holon, error) {
	m.h.Status = status
	return m.h, nil
}
func (m *compileStore) AddAgentSession(context.Context, string, AgentSession) (Holon, error) {
	return m.h, nil
}
func (m *compileStore) UpdateAgentSession(context.Context, string, AgentSession, bool) (Holon, error) {
	return m.h, nil
}
func (m *compileStore) AddManualTerminal(context.Context, string, ManualTerminal) (Holon, error) {
	return m.h, nil
}
func (m *compileStore) UpdateManualTerminal(context.Context, string, ManualTerminal) (Holon, error) {
	return m.h, nil
}
func (m *compileStore) ReorderTabs(context.Context, string, []TabRef) (Holon, error) { return m.h, nil }
func (m *compileStore) ListByPullRequest(context.Context, string) ([]Holon, error)   { return nil, nil }

type endStore struct{ compileStore }

func (m *endStore) SetStatus(_ context.Context, _ string, status Status, _ time.Time) (Holon, error) {
	m.h.Status = status
	return m.h, nil
}

type lifecycleStore struct{ compileStore }

func (m *lifecycleStore) UpdateAgentSession(_ context.Context, _ string, agent AgentSession, aggregate bool) (Holon, error) {
	for i := range m.h.AgentSessions {
		if m.h.AgentSessions[i].ID == agent.ID {
			m.h.AgentSessions[i] = agent
		}
	}
	if aggregate {
		allTerminal := len(m.h.AgentSessions) > 0
		for _, candidate := range m.h.AgentSessions {
			if candidate.ClosedAt == nil && !IsTerminal(Status(candidate.Status)) {
				allTerminal = false
			}
		}
		if allTerminal {
			m.h.Status = StatusCompleted
		}
	}
	return m.h, nil
}

func (m *lifecycleStore) SetStatus(_ context.Context, _ string, status Status, now time.Time) (Holon, error) {
	m.h.Status = status
	if status == StatusQueued {
		m.h.FinishedAt = nil
		for i := range m.h.AgentSessions {
			if m.h.AgentSessions[i].ClosedAt == nil && IsTerminal(Status(m.h.AgentSessions[i].Status)) {
				m.h.AgentSessions[i].Status = string(StatusQueued)
				m.h.AgentSessions[i].TerminalID = ""
				m.h.AgentSessions[i].StartedAt = nil
				m.h.AgentSessions[i].FinishedAt = nil
				m.h.AgentSessions[i].UpdatedAt = now
			}
		}
	}
	return m.h, nil
}

func (m *lifecycleStore) UpdateManualTerminal(_ context.Context, _ string, terminal ManualTerminal) (Holon, error) {
	for i := range m.h.ManualTerminals {
		if m.h.ManualTerminals[i].ID == terminal.ID {
			m.h.ManualTerminals[i] = terminal
		}
	}
	return m.h, nil
}

func TestEndArchivesWorkspaceAfterShutdown(t *testing.T) {
	store := &endStore{}
	store.h = Holon{ID: "h", Status: StatusRunning, WorktreeBranch: "holark/h", WorktreePath: "/worktrees/h"}
	repository := &repositorySpy{}
	service := NewServiceWithRepository(store, repository)

	ending, err := service.End(t.Context(), "h")
	if err != nil || ending.Status != StatusCancelling || store.h.ArchivedAt != nil {
		t.Fatalf("end intent=%+v err=%v", ending, err)
	}
	want := errors.New("archive failed")
	repository.archiveErr = want
	if _, err = service.FinalizeCancellation(t.Context(), "h"); !errors.Is(err, want) {
		t.Fatalf("finalize error=%v, want %v", err, want)
	}
	if store.h.ArchivedAt != nil {
		t.Fatal("holon archived after archive failure")
	}
	repository.archiveErr = nil
	ended, err := service.FinalizeCancellation(t.Context(), "h")
	if err != nil || ended.Status != StatusCancelled || ended.ArchivedAt == nil {
		t.Fatalf("ended holon=%+v err=%v", ended, err)
	}
	if repository.archivedBranch != "holark/h" {
		t.Fatalf("archived branch=%q", repository.archivedBranch)
	}
}

func TestEndArchivesAlreadyTerminalWorkspaceWithoutChangingOutcome(t *testing.T) {
	now := time.Now().UTC()
	store := &endStore{}
	store.h = Holon{ID: "h", Status: StatusFailed, Reason: "agent failed", FinishedAt: &now, WorktreeBranch: "holark/h", WorktreePath: "/worktrees/h", AgentSessions: []AgentSession{{ID: "agent", Status: string(StatusFailed), CreatedAt: now, UpdatedAt: now}}}
	repository := &repositorySpy{}
	service := NewServiceWithRepository(store, repository)

	ending, err := service.End(t.Context(), "h")
	if err != nil || ending.Status != StatusFailed {
		t.Fatalf("end terminal holon=%+v err=%v", ending, err)
	}
	ended, err := service.FinalizeCancellation(t.Context(), "h")
	if err != nil || ended.Status != StatusFailed || ended.Reason != "agent failed" || ended.ArchivedAt == nil || repository.archivedBranch != "holark/h" {
		t.Fatalf("archived terminal holon=%+v repository=%+v err=%v", ended, repository, err)
	}
}

func TestWorkspaceArchivalPolicyAndReopenPreparation(t *testing.T) {
	now := time.Now().UTC()
	terminalAgent := AgentSession{ID: "agent", HolonID: "h", AgentType: "codex", Status: string(StatusRunning), ResumeTarget: "conversation", CreatedAt: now, UpdatedAt: now}
	for _, test := range []struct {
		name        string
		kind        Kind
		terminal    *ManualTerminal
		wantArchive bool
	}{
		{name: "regular completion keeps workspace", kind: KindNormal},
		{name: "finite completion archives workspace", kind: KindPullReview, wantArchive: true},
		{name: "metadata completion archives workspace", kind: KindPRMetadata, wantArchive: true},
		{name: "finite completion waits for live consumer", kind: KindPullWorker, terminal: &ManualTerminal{ID: "shell", HolonID: "h", TerminalID: "pty", CreatedAt: now, UpdatedAt: now}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &lifecycleStore{}
			store.h = Holon{ID: "h", Kind: test.kind, Status: StatusRunning, BaseBranch: "main", BaseCommit: "base", WorktreeBranch: "holark/h", WorktreePath: "/worktrees/h", CreatedAt: now, AgentSessions: []AgentSession{terminalAgent}}
			if test.terminal != nil {
				store.h.ManualTerminals = []ManualTerminal{*test.terminal}
			}
			repository := &repositorySpy{}
			got, err := NewServiceWithRepository(store, repository).SetAgentSessionStatus(t.Context(), "h", "agent", StatusCompleted, "", "conversation")
			if err != nil {
				t.Fatal(err)
			}
			if (got.ArchivedAt != nil) != test.wantArchive || (repository.archivedBranch != "") != test.wantArchive {
				t.Fatalf("holon=%+v archived branch=%q", got, repository.archivedBranch)
			}
			if got.AgentSessions[0].ClosedAt != nil {
				t.Fatal("archival closed retained agent")
			}
			if test.terminal != nil {
				got, err = NewServiceWithRepository(store, repository).CloseManualTerminal(t.Context(), "h", test.terminal.ID)
				if err != nil || got.ArchivedAt == nil || repository.archivedBranch != "holark/h" {
					t.Fatalf("archive after final consumer closed: holon=%+v repository=%+v err=%v", got, repository, err)
				}
			}
		})
	}

	archived := now
	store := &lifecycleStore{}
	store.h = Holon{ID: "h", Kind: KindPullReview, Status: StatusCompleted, BaseBranch: "main", BaseCommit: "base", WorktreeBranch: "holark/h", WorktreePath: "/worktrees/h", CreatedAt: now, FinishedAt: &now, ArchivedAt: &archived, AgentSessions: []AgentSession{{ID: "agent", HolonID: "h", AgentType: "codex", Status: string(StatusCompleted), ResumeTarget: "conversation", CreatedAt: now, UpdatedAt: now, FinishedAt: &now}}}
	repository := &repositorySpy{}
	reopened, err := NewServiceWithRepository(store, repository).Reopen(t.Context(), "h")
	if err != nil || repository.reopenedBranch != "holark/h" || reopened.ArchivedAt != nil || reopened.Status != StatusQueued || reopened.AgentSessions[0].Status != string(StatusQueued) || reopened.AgentSessions[0].ResumeTarget != "conversation" {
		t.Fatalf("reopened=%+v repository=%+v err=%v", reopened, repository, err)
	}

	store.h = Holon{ID: "h", Kind: KindPullReview, Status: StatusCompleted, BaseBranch: "main", BaseCommit: "base", WorktreeBranch: "holark/h", WorktreePath: "/worktrees/h", CreatedAt: now, FinishedAt: &now, ArchivedAt: &archived}
	reopened, err = NewServiceWithRepository(store, repository).Reopen(t.Context(), "h")
	if err != nil || reopened.ArchivedAt != nil || reopened.Status != StatusCompleted {
		t.Fatalf("reopened workspace without retained agents=%+v err=%v", reopened, err)
	}
}

type addedAgentStore struct {
	compileStore
	added AgentSession
}

func (s *addedAgentStore) AddAgentSession(_ context.Context, _ string, agent AgentSession) (Holon, error) {
	s.added = agent
	s.h.AgentSessions = append(s.h.AgentSessions, agent)
	return s.h, nil
}

func TestAddAgentSessionNormalizesPromptAndInitialInputState(t *testing.T) {
	tests := []struct {
		name           string
		prompt         string
		wantPrompt     string
		wantInputState string
	}{
		{name: "empty prompt", prompt: "", wantPrompt: "", wantInputState: "task_complete"},
		{name: "whitespace prompt", prompt: " \n\t ", wantPrompt: "", wantInputState: "task_complete"},
		{name: "prompted", prompt: " \n continue the work \t", wantPrompt: "continue the work", wantInputState: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &addedAgentStore{}
			store.h = Holon{ID: "h", BaseCommit: "base", WorktreeBranch: "holark/h", WorktreePath: "/worktrees/h"}
			if _, err := NewService(store).AddAgentSession(t.Context(), "h", "codex", test.prompt); err != nil {
				t.Fatal(err)
			}
			if store.added.Prompt != test.wantPrompt || store.added.InputState != test.wantInputState {
				t.Fatalf("agent prompt=%q input_state=%q, want %q and %q", store.added.Prompt, store.added.InputState, test.wantPrompt, test.wantInputState)
			}
		})
	}
}

func TestCreatePinsExactCommitAndPersistsWorkspaceThenPublishesThroughPort(t *testing.T) {
	store := &compileStore{}
	repo := &repositorySpy{}
	service := NewServiceWithRepository(store, repo)
	h, e := service.Create(context.Background(), Create{Prompt: "Fix race", BaseBranch: "main", BaseCommit: "branch-base", WorkSessionStartCommit: "selected-sha"})
	if e != nil {
		t.Fatal(e)
	}
	if repo.createdCommit != "selected-sha" || h.BaseCommit != "branch-base" || h.WorkSessionStartCommit != "selected-sha" || h.WorktreePath == "" || h.WorktreeBranch == "" {
		t.Fatalf("create=%+v commit=%q", h, repo.createdCommit)
	}
	if h.Title != "Fix race" || h.WorktreeBranch != "holark/"+h.ID || h.UpstreamBranch != "" || h.Published {
		t.Fatalf("fallback identity was not immediately usable: %+v", h)
	}
	h, e = service.Publish(context.Background(), h.ID, Publish{Remote: "origin", UpstreamBranch: "topic", ExpectedRemoteHead: "lease"})
	if e != nil {
		t.Fatal(e)
	}
	if repo.publishedExpected != "lease" || !h.Published || h.UpstreamHeadCommit != "new-head" {
		t.Fatalf("publish=%+v lease=%q", h, repo.publishedExpected)
	}
}

func TestCreateNormalizesImmediateFallbackTitles(t *testing.T) {
	for _, test := range []struct {
		name, title, prompt, want string
	}{
		{name: "explicit", title: "  Explicit title  ", prompt: "Ignored", want: "Explicit title"},
		{name: "first prompt line", prompt: "\n  Fix the race  \nMore detail", want: "Fix the race"},
		{name: "generic fallback", prompt: " \n\t", want: "Holon"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &compileStore{}
			h, err := NewServiceWithRepository(store, &repositorySpy{}).Create(t.Context(), Create{Title: test.title, Prompt: test.prompt, BaseCommit: "base"})
			if err != nil {
				t.Fatal(err)
			}
			if h.Title != test.want || store.h.Title != test.want || h.WorktreeBranch == "" || store.h.WorktreeBranch != h.WorktreeBranch || h.UpstreamBranch != "" {
				t.Fatalf("created=%+v persisted=%+v", h, store.h)
			}
		})
	}
}

func TestApplyGeneratedIdentityRenamesBranchAndRespectsCurrentTitle(t *testing.T) {
	store := &compileStore{}
	repository := &repositorySpy{}
	service := NewServiceWithRepository(store, repository)
	store.h = Holon{ID: "h", Title: "Fallback", Prompt: "Fix race", WorktreeBranch: "holark/h"}

	generated, err := service.ApplyGeneratedIdentity(t.Context(), "h", "Fallback", "Repair race", "repair-race")
	if err != nil {
		t.Fatal(err)
	}
	if generated.Title != "Repair race" || generated.WorktreeBranch != "holark/repair-race" || repository.renamedExpected != "holark/h" || repository.renamedSlug != "repair-race" {
		t.Fatalf("generated=%+v repository=%+v", generated, repository)
	}

	store.h.Title = "Manual rename"
	preserved, err := service.ApplyGeneratedIdentity(t.Context(), "h", "Fallback", "Overwrite attempt", "better-branch")
	if err != nil {
		t.Fatal(err)
	}
	if preserved.Title != "Manual rename" || preserved.WorktreeBranch != "holark/better-branch" {
		t.Fatalf("concurrent manual title was not preserved: %+v", preserved)
	}
}

func TestApplyGeneratedIdentityFailureRetainsPersistedFallback(t *testing.T) {
	for _, test := range []struct {
		name       string
		repository *repositorySpy
		updateErr  error
	}{
		{name: "workspace rename", repository: &repositorySpy{renameErr: errors.New("rename unavailable")}},
		{name: "identity persistence", repository: &repositorySpy{}, updateErr: errors.New("store unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &compileStore{}
			store.h = Holon{ID: "h", Title: "Fallback", WorktreeBranch: "holark/h"}
			store.updateErr = test.updateErr
			_, err := NewServiceWithRepository(store, test.repository).ApplyGeneratedIdentity(t.Context(), "h", "Fallback", "Generated", "generated-branch")
			if err == nil {
				t.Fatal("expected identity application error")
			}
			if store.h.Title != "Fallback" || store.h.WorktreeBranch != "holark/h" {
				t.Fatalf("fallback identity changed after failed application: %+v", store.h)
			}
		})
	}
}

func (m *compileStore) SetSelectedTab(context.Context, string, string) error { return nil }
