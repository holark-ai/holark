package sqliteadapter

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/ide"
	idesqlite "github.com/holark-ai/holark/internal/ide/sqliteadapter"
)

func openStore(t *testing.T, path string) (*sql.DB, *Store) {
	t.Helper()
	db, e := database.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(context.Background(), db)
	if e != nil {
		t.Fatal(e)
	}
	return db, s
}
func TestFreshSchemaRetainsKindsRelationshipsAndArchivedHistoryAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, store := openStore(t, path)
	service := holons.NewService(store)
	now := time.Now().UTC()
	exit := 1
	started := now.Add(-time.Minute)
	finished := now
	h := holons.Holon{ID: "h", Title: "Review", Prompt: "review", Kind: holons.KindPullReview, Status: holons.StatusFailed, BaseBranch: "main", BaseCommit: "abc", WorkSessionStartCommit: "def", WorktreeBranch: "holark/h", WorktreePath: "/worktrees/h", IssueID: "issue-7", PullRequestID: "pr-9", Reason: "failed", ExitCode: &exit, CreatedAt: started, FinishedAt: &finished, ArchivedAt: &finished, AgentSessions: []holons.AgentSession{{ID: "old", HolonID: "h", TerminalID: "old-pty", AgentType: "codex", Title: "Old", Prompt: "review", Status: string(holons.StatusCompleted), CreatedAt: started, UpdatedAt: now, FinishedAt: &finished, ClosedAt: &finished}, {ID: "a", HolonID: "h", AgentType: "codex", Title: "Agent", Prompt: "review", Status: string(holons.StatusFailed), Reason: "failed", ExitCode: &exit, TabOrder: 1, CreatedAt: started, UpdatedAt: now, StartedAt: &started, FinishedAt: &finished}}}
	if e := store.Create(context.Background(), h); e != nil {
		t.Fatal(e)
	}
	_ = service
	db.Close()
	db, store = openStore(t, path)
	defer db.Close()
	got, e := store.Get(context.Background(), h.ID)
	if e != nil {
		t.Fatal(e)
	}
	if got.Kind != holons.KindPullReview || got.WorkSessionStartCommit != "def" || got.IssueID != "issue-7" || got.PullRequestID != "pr-9" || got.ArchivedAt == nil || len(got.AgentSessions) != 2 || got.AgentSessions[0].ClosedAt == nil || got.AgentSessions[1].ClosedAt != nil {
		t.Fatalf("recovered holon = %+v", got)
	}
}

func TestAgentContextUsagePersistsAndSurvivesUnrelatedObservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.sqlite")
	db, store := openStore(t, path)
	now := time.Now().UTC()
	h := holons.Holon{
		ID: "context", Title: "Context", Kind: holons.KindNormal, Status: holons.StatusRunning, BaseCommit: "abc", CreatedAt: now,
		AgentSessions: []holons.AgentSession{{ID: "agent", HolonID: "context", AgentType: "codex", Title: "Agent", Status: string(holons.StatusRunning), CreatedAt: now, UpdatedAt: now}},
	}
	if err := store.Create(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	service := holons.NewService(store)
	contextTokens := int64(14470)
	observed, err := service.UpdateAgentObservation(t.Context(), h.ID, "agent", "", "", "", "", "", "", "", &contextTokens)
	if err != nil || observed.AgentSessions[0].ContextTokens == nil || *observed.AgentSessions[0].ContextTokens != contextTokens {
		t.Fatalf("context observation = %+v, err=%v", observed.AgentSessions, err)
	}
	updatedAt := observed.AgentSessions[0].UpdatedAt
	duplicate, err := service.UpdateAgentObservation(t.Context(), h.ID, "agent", "", "", "", "", "", "", "", &contextTokens)
	if err != nil || !duplicate.AgentSessions[0].UpdatedAt.Equal(updatedAt) {
		t.Fatalf("duplicate context observation updated session: %+v, err=%v", duplicate.AgentSessions, err)
	}
	if _, err = service.UpdateAgentObservation(t.Context(), h.ID, "agent", "", "", "", "task_complete", "", "", "", nil); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, store = openStore(t, path)
	defer db.Close()
	restored, err := store.Get(t.Context(), h.ID)
	if err != nil || restored.AgentSessions[0].ContextTokens == nil || *restored.AgentSessions[0].ContextTokens != contextTokens {
		t.Fatalf("restored context = %+v, err=%v", restored.AgentSessions, err)
	}
}

func TestLegacyOriginBaseBranchIsCanonicalizedOnRead(t *testing.T) {
	db, store := openStore(t, filepath.Join(t.TempDir(), "legacy.sqlite"))
	defer db.Close()
	now := time.Now().UTC()
	h := holons.Holon{ID: "legacy", Title: "Legacy", Kind: holons.KindNormal, Status: holons.StatusQueued, BaseBranch: "main", BaseCommit: "abc", CreatedAt: now}
	if err := store.Create(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update holons set base_branch='origin/main' where id='legacy'`); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(t.Context(), h.ID)
	if err != nil || got.BaseBranch != "main" {
		t.Fatalf("holon=%+v err=%v", got, err)
	}
	if err := store.Update(t.Context(), got); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRow(`select base_branch from holons where id='legacy'`).Scan(&stored); err != nil || stored != "main" {
		t.Fatalf("stored branch=%q err=%v", stored, err)
	}
}

func TestUpdatePersistsGeneratedIdentityAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generated-identity.sqlite")
	db, store := openStore(t, path)
	h := holons.Holon{
		ID:             "holon-generated",
		Title:          "New holon",
		Kind:           holons.KindNormal,
		Status:         holons.StatusQueued,
		BaseCommit:     "abc",
		WorktreeBranch: "holark/holon-generated",
		CreatedAt:      time.Now().UTC(),
	}
	if err := store.Create(t.Context(), h); err != nil {
		t.Fatal(err)
	}

	h.Title = "Persist generated branch names"
	h.WorktreeBranch = "holark/persist-generated-branch-names"
	if err := store.Update(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, store = openStore(t, path)
	defer db.Close()
	got, err := store.Get(t.Context(), h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != h.Title || got.WorktreeBranch != h.WorktreeBranch {
		t.Fatalf("generated identity = (%q, %q), want (%q, %q)", got.Title, got.WorktreeBranch, h.Title, h.WorktreeBranch)
	}
}

func TestReorderTabsIsAtomic(t *testing.T) {
	db, store := openStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	defer db.Close()
	now := time.Now().UTC()
	h := holons.Holon{ID: "h", Title: "H", Prompt: "p", Kind: holons.KindNormal, Status: holons.StatusRunning, BaseCommit: "abc", CreatedAt: now, AgentSessions: []holons.AgentSession{{ID: "a", HolonID: "h", AgentType: "codex", Title: "Agent", Status: "running", CreatedAt: now, UpdatedAt: now}}}
	if e := store.Create(context.Background(), h); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`insert into holon_manual_terminals(id,holon_id,terminal_id,title,cwd,tab_order,created_at,updated_at) values('t','h','pty','Shell','/tmp',1,?,?)`, ts(now), ts(now)); e != nil {
		t.Fatal(e)
	}
	if _, e := store.ReorderTabs(context.Background(), "h", []holons.TabRef{{Type: "agent", ID: "a"}, {Type: "terminal", ID: "missing"}}); e == nil {
		t.Fatal("expected rollback")
	}
	got, e := store.Get(context.Background(), "h")
	if e != nil {
		t.Fatal(e)
	}
	if got.AgentSessions[0].TabOrder != 0 || got.ManualTerminals[0].TabOrder != 1 {
		t.Fatalf("partial reorder persisted: %+v", got)
	}
	if _, e = store.ReorderTabs(context.Background(), "h", []holons.TabRef{{Type: "terminal", ID: "t"}}); !errors.Is(e, holons.ErrInvalid) {
		t.Fatalf("incomplete order error = %v", e)
	}
	got, e = store.Get(context.Background(), "h")
	if e != nil {
		t.Fatal(e)
	}
	if got.AgentSessions[0].TabOrder != 0 || got.ManualTerminals[0].TabOrder != 1 {
		t.Fatalf("incomplete order changed tabs: %+v", got)
	}
}

func TestIDELifecyclePersistsAndParticipatesInTabOrdering(t *testing.T) {
	db, store := openStore(t, filepath.Join(t.TempDir(), "ide.sqlite"))
	defer db.Close()
	var err error
	now := time.Now().UTC()
	if err = store.Create(t.Context(), holons.Holon{ID: "h-ide", Title: "IDE", Kind: holons.KindNormal, Status: holons.StatusQueued, BaseCommit: "abc", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	ideStore, err := idesqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ideStore.Create(t.Context(), ide.IDE{ID: "ide-one", HolonID: "h-ide", Provider: "vscode", State: ide.Starting, DesiredOpen: true, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	h, err := store.Get(t.Context(), "h-ide")
	if len(h.IDEs) != 1 || h.IDEs[0].State != "starting" {
		t.Fatalf("ides=%+v", h.IDEs)
	}
	ready := now.Add(time.Second)
	v := h.IDEs[0]
	v.State = "ready"
	v.ReadyAt = &ready
	v.UpdatedAt = ready
	_, err = ideStore.Update(t.Context(), ide.IDE{ID: v.ID, HolonID: v.HolonID, Provider: v.Provider, State: ide.Ready, TabOrder: v.TabOrder, DesiredOpen: v.DesiredOpen, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, ReadyAt: v.ReadyAt})
	if err != nil {
		t.Fatal(err)
	}
	h, err = store.Get(t.Context(), "h-ide")
	if err != nil {
		t.Fatal(err)
	}
	if h.IDEs[0].ReadyAt == nil || h.IDEs[0].State != "ready" {
		t.Fatalf("ready=%+v", h.IDEs[0])
	}
	h, err = store.ReorderTabs(t.Context(), "h-ide", []holons.TabRef{{Type: "ide", ID: "ide-one"}})
	if err != nil {
		t.Fatal(err)
	}
	if h.IDEs[0].TabOrder != 0 {
		t.Fatalf("tab order=%d", h.IDEs[0].TabOrder)
	}
}

func TestNewTabsUseVisibleCrossKindOrderAndManualTerminalDefaults(t *testing.T) {
	db, store := openStore(t, filepath.Join(t.TempDir(), "tabs.sqlite"))
	defer db.Close()
	now := time.Now().UTC()
	if err := store.Create(t.Context(), holons.Holon{ID: "h", Title: "H", Prompt: "p", Kind: holons.KindNormal, Status: holons.StatusRunning, BaseCommit: "abc", WorktreeBranch: "holark/h", WorktreePath: "/w", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	service := holons.NewService(store)
	h, err := service.AddManualTerminal(t.Context(), "h", "", "/w", "pty-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.ManualTerminals) != 1 || h.ManualTerminals[0].Title != "Shell 1" || h.ManualTerminals[0].TabOrder != 0 {
		t.Fatalf("first terminal = %+v", h.ManualTerminals)
	}
	first := h.ManualTerminals[0]
	h, err = service.UpdateManualTerminal(t.Context(), "h", first.ID, "   ")
	if err != nil || h.ManualTerminals[0].Title != "Shell 1" {
		t.Fatalf("blank rename = %+v, %v", h.ManualTerminals, err)
	}
	ideStore, err := idesqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	createdIDE, err := ideStore.Create(t.Context(), ide.IDE{ID: "ide", HolonID: "h", Provider: "vscode", State: ide.Starting, DesiredOpen: true, CreatedAt: now, UpdatedAt: now})
	if err != nil || createdIDE.TabOrder != 1 {
		t.Fatalf("ide = %+v, %v", createdIDE, err)
	}
	h, err = service.AddManualTerminal(t.Context(), "h", "", "/w", "pty-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.ManualTerminals) != 2 || h.ManualTerminals[1].Title != "Shell 2" || h.ManualTerminals[1].TabOrder != 2 {
		t.Fatalf("second terminal = %+v", h.ManualTerminals)
	}
	if _, err = service.CloseManualTerminal(t.Context(), "h", first.ID); err != nil {
		t.Fatal(err)
	}
	closedAt := now.Add(time.Second)
	if _, err = db.Exec(`update holon_ides set desired_open=0,closed_at=?,tab_order=99 where id="ide"`, ts(closedAt)); err != nil {
		t.Fatal(err)
	}
	h, err = service.AddAgentSession(t.Context(), "h", "codex", "continue")
	if err != nil {
		t.Fatal(err)
	}
	if got := h.AgentSessions[0].TabOrder; got != 3 {
		t.Fatalf("agent tab order = %d, want 3", got)
	}
}

func TestEndTransitionsLiveAgentsToCancellingAndIsIdempotent(t *testing.T) {
	db, store := openStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	defer db.Close()
	now := time.Now().UTC()
	h := holons.Holon{ID: "h", Title: "H", Prompt: "p", Kind: holons.KindNormal, Status: holons.StatusRunning, BaseCommit: "abc", CreatedAt: now, AgentSessions: []holons.AgentSession{{ID: "a", HolonID: "h", AgentType: "codex", Title: "Agent", Status: "running", CreatedAt: now, UpdatedAt: now}}}
	if e := store.Create(context.Background(), h); e != nil {
		t.Fatal(e)
	}
	service := holons.NewService(store)
	cancelled, e := service.End(context.Background(), "h")
	if e != nil {
		t.Fatal(e)
	}
	if cancelled.Status != holons.StatusCancelling || cancelled.AgentSessions[0].Status != string(holons.StatusCancelling) {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	if _, e = service.End(context.Background(), "h"); e != nil {
		t.Fatalf("second end = %v", e)
	}
}

func TestAgentProcessCompletionUsesCancellationPrecedenceAndPreservesTerminalOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		status     holons.Status
		exit       int
		want       holons.Status
		wantReason string
	}{
		{name: "cancel zero", status: holons.StatusCancelling, exit: 0, want: holons.StatusCancelled},
		{name: "cancel nonzero", status: holons.StatusCancelling, exit: 17, want: holons.StatusCancelled},
		{name: "complete", status: holons.StatusRunning, exit: 0, want: holons.StatusCompleted},
		{name: "fail", status: holons.StatusRunning, exit: 17, want: holons.StatusFailed, wantReason: "Process exited with an error."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, store := openStore(t, filepath.Join(t.TempDir(), "completion.sqlite"))
			defer db.Close()
			now := time.Now().UTC()
			h := holons.Holon{ID: "h", Title: "H", Kind: holons.KindNormal, Status: test.status, BaseCommit: "abc", CreatedAt: now, AgentSessions: []holons.AgentSession{{ID: "a", HolonID: "h", TerminalID: "terminal", AgentType: "codex", Status: string(test.status), CreatedAt: now, UpdatedAt: now}}}
			if err := store.Create(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			got, err := holons.NewService(store).CompleteAgentSession(t.Context(), "h", "a", "terminal", test.exit)
			if err != nil || got.Status != test.want || got.AgentSessions[0].Status != string(test.want) || got.AgentSessions[0].Reason != test.wantReason || got.AgentSessions[0].TerminalID != "" || got.AgentSessions[0].ExitCode == nil || *got.AgentSessions[0].ExitCode != test.exit {
				t.Fatalf("completion=%+v err=%v", got, err)
			}
		})
	}

	for _, terminal := range []holons.Status{holons.StatusCancelled, holons.StatusExpired} {
		t.Run("duplicate "+string(terminal), func(t *testing.T) {
			db, store := openStore(t, filepath.Join(t.TempDir(), "duplicate.sqlite"))
			defer db.Close()
			now := time.Now().UTC()
			originalExit := 4
			h := holons.Holon{ID: "h", Title: "H", Kind: holons.KindNormal, Status: terminal, BaseCommit: "abc", CreatedAt: now, FinishedAt: &now, AgentSessions: []holons.AgentSession{{ID: "a", HolonID: "h", TerminalID: "terminal", AgentType: "codex", Status: string(terminal), Reason: "preserved", ExitCode: &originalExit, CreatedAt: now, UpdatedAt: now, FinishedAt: &now}}}
			if err := store.Create(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			got, err := holons.NewService(store).CompleteAgentSession(t.Context(), "h", "a", "terminal", 0)
			if err != nil || got.Status != terminal || got.AgentSessions[0].Status != string(terminal) || got.AgentSessions[0].Reason != "preserved" || got.AgentSessions[0].TerminalID != "" || got.AgentSessions[0].ExitCode == nil || *got.AgentSessions[0].ExitCode != originalExit {
				t.Fatalf("duplicate completion=%+v err=%v", got, err)
			}
		})
	}

	t.Run("terminal parent with late live-agent exit", func(t *testing.T) {
		db, store := openStore(t, filepath.Join(t.TempDir(), "late.sqlite"))
		defer db.Close()
		now := time.Now().UTC()
		h := holons.Holon{ID: "h", Title: "H", Kind: holons.KindNormal, Status: holons.StatusExpired, BaseCommit: "abc", CreatedAt: now, FinishedAt: &now, AgentSessions: []holons.AgentSession{{ID: "a", HolonID: "h", TerminalID: "terminal", AgentType: "codex", Status: string(holons.StatusCancelling), CreatedAt: now, UpdatedAt: now}}}
		if err := store.Create(t.Context(), h); err != nil {
			t.Fatal(err)
		}
		got, err := holons.NewService(store).CompleteAgentSession(t.Context(), "h", "a", "terminal", 0)
		if err != nil || got.Status != holons.StatusExpired || got.AgentSessions[0].Status != string(holons.StatusCancelled) || got.AgentSessions[0].TerminalID != "" {
			t.Fatalf("late completion=%+v err=%v", got, err)
		}
	})
}

func TestStaleAgentCompletionCannotSettleReplacementRuntime(t *testing.T) {
	db, store := openStore(t, filepath.Join(t.TempDir(), "stale-completion.sqlite"))
	defer db.Close()
	now := time.Now().UTC()
	h := holons.Holon{ID: "h", Title: "H", Kind: holons.KindNormal, Status: holons.StatusRunning, BaseCommit: "abc", CreatedAt: now, AgentSessions: []holons.AgentSession{{ID: "a", HolonID: "h", TerminalID: "new-terminal", AgentType: "codex", Status: string(holons.StatusRunning), CreatedAt: now, UpdatedAt: now}}}
	if err := store.Create(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	got, err := holons.NewService(store).CompleteAgentSession(t.Context(), "h", "a", "old-terminal", 9)
	if err != nil || got.Status != holons.StatusRunning || got.AgentSessions[0].Status != string(holons.StatusRunning) || got.AgentSessions[0].TerminalID != "new-terminal" || got.AgentSessions[0].ExitCode != nil {
		t.Fatalf("stale completion changed replacement runtime: holon=%+v err=%v", got, err)
	}
}

func TestCancellationFinalizationIncludesManualTerminalsAndIDEsAndEndsCompletedHolon(t *testing.T) {
	db, store := openStore(t, filepath.Join(t.TempDir(), "resources.sqlite"))
	defer db.Close()
	now := time.Now().UTC()
	service := holons.NewService(store)
	if err := store.Create(t.Context(), holons.Holon{ID: "manual", Title: "Manual", Kind: holons.KindNormal, Status: holons.StatusRunning, BaseCommit: "abc", WorktreePath: "/w", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	h, err := service.AddManualTerminal(t.Context(), "manual", "Shell", "/w", "")
	if err != nil {
		t.Fatal(err)
	}
	h, err = service.End(t.Context(), "manual")
	if err != nil || h.Status != holons.StatusCancelling {
		t.Fatalf("manual intent=%+v err=%v", h, err)
	}
	if h, err = service.FinalizeCancellation(t.Context(), "manual"); err != nil || h.Status != holons.StatusCancelling {
		t.Fatalf("manual finalized while live=%+v err=%v", h, err)
	}
	if _, err = service.CloseManualTerminal(t.Context(), "manual", h.ManualTerminals[0].ID); err != nil {
		t.Fatal(err)
	}
	if h, err = service.FinalizeCancellation(t.Context(), "manual"); err != nil || h.Status != holons.StatusCancelled || h.FinishedAt == nil {
		t.Fatalf("manual final=%+v err=%v", h, err)
	}

	if err = store.Create(t.Context(), holons.Holon{ID: "ide-only", Title: "IDE", Kind: holons.KindNormal, Status: holons.StatusRunning, BaseCommit: "abc", WorktreePath: "/w", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	ideStore, err := idesqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ideStore.Create(t.Context(), ide.IDE{ID: "open-editor", HolonID: "ide-only", Provider: "vscode", State: ide.Suspended, DesiredOpen: true, CreatedAt: now, UpdatedAt: now, ClosedAt: &now}); err != nil {
		t.Fatal(err)
	}
	h, err = service.End(t.Context(), "ide-only")
	if err != nil || h.Status != holons.StatusCancelling {
		t.Fatalf("IDE intent=%+v err=%v", h, err)
	}
	if h, err = service.FinalizeCancellation(t.Context(), "ide-only"); err != nil || h.Status != holons.StatusCancelled {
		t.Fatalf("IDE final=%+v err=%v", h, err)
	}

	if err = store.Create(t.Context(), holons.Holon{ID: "terminal", Title: "Terminal", Kind: holons.KindNormal, Status: holons.StatusCompleted, BaseCommit: "abc", WorktreePath: "/w", CreatedAt: now, FinishedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if _, err = ideStore.Create(t.Context(), ide.IDE{ID: "editor", HolonID: "terminal", Provider: "vscode", State: ide.Suspended, DesiredOpen: true, CreatedAt: now, UpdatedAt: now, ClosedAt: &now}); err != nil {
		t.Fatal(err)
	}
	h, err = service.End(t.Context(), "terminal")
	if err != nil || h.Status != holons.StatusCompleted {
		t.Fatalf("terminal resource intent=%+v err=%v", h, err)
	}
	h, err = service.FinalizeCancellation(t.Context(), "terminal")
	if err != nil || h.Status != holons.StatusCompleted {
		t.Fatalf("terminal outcome changed=%+v err=%v", h, err)
	}
}

func TestMultipleAgentsKeepHolonCancellingUntilAllCancellationCompletionsArrive(t *testing.T) {
	db, store := openStore(t, filepath.Join(t.TempDir(), "multiple.sqlite"))
	defer db.Close()
	now := time.Now().UTC()
	h := holons.Holon{ID: "h", Title: "H", Kind: holons.KindNormal, Status: holons.StatusCancelling, BaseCommit: "abc", CreatedAt: now, AgentSessions: []holons.AgentSession{
		{ID: "one", HolonID: "h", TerminalID: "terminal-one", AgentType: "codex", Status: string(holons.StatusCancelling), CreatedAt: now, UpdatedAt: now},
		{ID: "two", HolonID: "h", TerminalID: "terminal-two", AgentType: "codex", Status: string(holons.StatusCancelling), CreatedAt: now, UpdatedAt: now},
	}}
	if err := store.Create(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	service := holons.NewService(store)
	got, err := service.CompleteAgentSession(t.Context(), "h", "one", "terminal-one", 0)
	if err != nil || got.Status != holons.StatusCancelling {
		t.Fatalf("first completion=%+v err=%v", got, err)
	}
	got, err = service.CompleteAgentSession(t.Context(), "h", "two", "terminal-two", 1)
	if err != nil || got.Status != holons.StatusCancelled {
		t.Fatalf("second completion=%+v err=%v", got, err)
	}
}

func TestResumeRejectsReadOnlyMissingWorkspaceAndNonterminalAgent(t *testing.T) {
	db, store := openStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	defer db.Close()
	now := time.Now().UTC()
	service := holons.NewService(store)
	cases := []holons.Holon{{ID: "readonly", ReadOnly: true, WorktreeBranch: "b", WorktreePath: "/w"}, {ID: "workspace"}, {ID: "live", WorktreeBranch: "b", WorktreePath: "/w", AgentSessions: []holons.AgentSession{{ID: "live-a", HolonID: "live", AgentType: "codex", Title: "Agent", Status: "running", CreatedAt: now, UpdatedAt: now}}}}
	for _, h := range cases {
		h.Title = "H"
		h.Prompt = "p"
		h.Kind = holons.KindNormal
		h.Status = holons.StatusFailed
		h.BaseCommit = "abc"
		h.CreatedAt = now
		if len(h.AgentSessions) == 0 {
			h.AgentSessions = []holons.AgentSession{{ID: h.ID + "-a", HolonID: h.ID, AgentType: "codex", Title: "Agent", Status: "failed", CreatedAt: now, UpdatedAt: now}}
		}
		if e := store.Create(context.Background(), h); e != nil {
			t.Fatal(e)
		}
		if _, e := service.Resume(context.Background(), h.ID); !errors.Is(e, holons.ErrNotResumable) {
			t.Fatalf("resume %s = %v", h.ID, e)
		}
	}
}

func TestNestedLifecycleManualTerminalHistoryAggregationAndActivityOrder(t *testing.T) {
	db, store := openStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	defer db.Close()
	now := time.Now().UTC()
	for _, h := range []holons.Holon{{ID: "worker", Title: "Worker", Prompt: "p", Kind: holons.KindPullWorker, Status: holons.StatusRunning, BaseCommit: "abc", WorktreeBranch: "holark/worker", WorktreePath: "/w", PullRequestID: "pr", CreatedAt: now}, {ID: "metadata", Title: "Metadata", Prompt: "p", Kind: holons.KindPRMetadata, Status: holons.StatusQueued, BaseCommit: "abc", PullRequestID: "pr", CreatedAt: now.Add(time.Second)}} {
		if e := store.Create(context.Background(), h); e != nil {
			t.Fatal(e)
		}
	}
	service := holons.NewService(store)
	h, e := service.AddAgentSession(context.Background(), "worker", "codex", "work")
	if e != nil {
		t.Fatal(e)
	}
	agent := h.AgentSessions[0]
	h, e = service.SetAgentSessionStatus(context.Background(), "worker", agent.ID, holons.StatusCompleted, "", "resume-1")
	if e != nil {
		t.Fatal(e)
	}
	if h.Status != holons.StatusCompleted || h.AgentSessions[0].FinishedAt == nil || h.AgentSessions[0].ResumeTarget != "resume-1" {
		t.Fatalf("aggregated=%+v", h)
	}
	h, e = service.AddManualTerminal(context.Background(), "worker", "Shell", "/w", "pty")
	if e != nil {
		t.Fatal(e)
	}
	terminal := h.ManualTerminals[0]
	h, e = service.UpdateManualTerminal(context.Background(), "worker", terminal.ID, "Logs")
	if e != nil {
		t.Fatal(e)
	}
	h, e = service.CloseManualTerminal(context.Background(), "worker", terminal.ID)
	if e != nil {
		t.Fatal(e)
	}
	if h.ManualTerminals[0].Title != "Logs" || h.ManualTerminals[0].ClosedAt == nil {
		t.Fatalf("terminal history=%+v", h.ManualTerminals)
	}
	h, e = service.CloseAgentSession(context.Background(), "worker", agent.ID)
	if e != nil {
		t.Fatal(e)
	}
	if h.AgentSessions[0].ClosedAt == nil {
		t.Fatalf("agent history=%+v", h.AgentSessions)
	}
	activity, e := service.PullRequestActivity(context.Background(), "pr")
	if e != nil {
		t.Fatal(e)
	}
	if len(activity) != 2 || activity[0].Kind != holons.KindPRMetadata || activity[1].Kind != holons.KindPullWorker {
		t.Fatalf("activity=%+v", activity)
	}
}

func TestNestedAgentActionsRejectInvalidStateAndResumeInPlace(t *testing.T) {
	db, store := openStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	defer db.Close()
	now := time.Now().UTC()
	exit := 7
	started := now.Add(-time.Minute)
	finished := now.Add(-time.Second)
	h := holons.Holon{ID: "h", Title: "H", Prompt: "p", Kind: holons.KindNormal, Status: holons.StatusFailed, BaseBranch: "main", BaseCommit: "abc", WorktreeBranch: "holark/h", WorktreePath: "/w", CreatedAt: now, AgentSessions: []holons.AgentSession{{ID: "done", HolonID: "h", TerminalID: "pty", AgentType: "codex", Title: "Done", Status: "failed", Reason: "boom", ExitCode: &exit, CreatedAt: now, UpdatedAt: now, StartedAt: &started, FinishedAt: &finished}, {ID: "live", HolonID: "h", AgentType: "codex", Title: "Live", Status: "running", CreatedAt: now, UpdatedAt: now}}}
	if e := store.Create(context.Background(), h); e != nil {
		t.Fatal(e)
	}
	service := holons.NewService(store)
	if _, e := service.CancelAgentSession(context.Background(), "h", "done"); !errors.Is(e, holons.ErrNotCancellable) {
		t.Fatalf("cancel terminal=%v", e)
	}
	if _, e := service.ResumeAgentSession(context.Background(), "h", "live", "x"); !errors.Is(e, holons.ErrNotResumable) {
		t.Fatalf("resume live=%v", e)
	}
	got, e := service.ResumeAgentSession(context.Background(), "h", "done", "resume-7")
	if e != nil {
		t.Fatal(e)
	}
	var resumed holons.AgentSession
	for _, a := range got.AgentSessions {
		if a.ID == "done" {
			resumed = a
		}
	}
	if resumed.ID != "done" || resumed.Status != "queued" || resumed.TerminalID != "" || resumed.Reason != "" || resumed.ExitCode != nil || resumed.StartedAt != nil || resumed.FinishedAt != nil || resumed.ResumeTarget != "resume-7" {
		t.Fatalf("resumed=%+v", resumed)
	}
	got, e = service.CancelAgentSession(context.Background(), "h", "live")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = service.CancelAgentSession(context.Background(), "h", "live"); !errors.Is(e, holons.ErrNotCancellable) {
		t.Fatalf("cancel twice=%v", e)
	}
	got, e = service.CloseAgentSession(context.Background(), "h", "live")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = service.CloseAgentSession(context.Background(), "h", "live"); e != nil {
		t.Fatalf("close twice=%v", e)
	}
}

func TestAggregatePrecedenceDoesNotDependOnInsertionOrderAndCopiesSelectedOutcome(t *testing.T) {
	db, store := openStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	defer db.Close()
	now := time.Now().UTC()
	older := now.Add(-time.Minute)
	start := now.Add(-2 * time.Minute)
	finish := now.Add(-time.Second)
	exit := 3
	h := holons.Holon{ID: "h", Title: "H", Prompt: "p", Kind: holons.KindNormal, Status: holons.StatusQueued, BaseCommit: "abc", CreatedAt: now, AgentSessions: []holons.AgentSession{{ID: "failed", HolonID: "h", AgentType: "codex", Title: "F", Status: "failed", Reason: "failed reason", ExitCode: &exit, TabOrder: 0, CreatedAt: now, UpdatedAt: now, StartedAt: &start, FinishedAt: &finish}, {ID: "running", HolonID: "h", AgentType: "codex", Title: "R", Status: "running", Reason: "running reason", TabOrder: 1, CreatedAt: now, UpdatedAt: older, StartedAt: &start}}}
	if e := store.Create(context.Background(), h); e != nil {
		t.Fatal(e)
	}
	got, e := store.UpdateAgentSession(context.Background(), "h", h.AgentSessions[0], true)
	if e != nil {
		t.Fatal(e)
	}
	if got.Status != holons.StatusRunning || got.Reason != "running reason" || got.ExitCode != nil || got.StartedAt == nil || got.FinishedAt != nil {
		t.Fatalf("running precedence=%+v", got)
	}
	cancel := got.AgentSessions[1]
	cancel.Status = "cancelling"
	cancel.Reason = "stopping"
	cancel.UpdatedAt = older
	got, e = store.UpdateAgentSession(context.Background(), "h", cancel, true)
	if e != nil {
		t.Fatal(e)
	}
	if got.Status != holons.StatusCancelling || got.Reason != "stopping" {
		t.Fatalf("cancelling precedence=%+v", got)
	}
}

func TestAggregateUsesEarliestVisibleStartAndLatestFinishOnlyWhenAllTerminal(t *testing.T) {
	db, store := openStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	defer db.Close()
	now := time.Now().UTC()
	early := now.Add(-3 * time.Minute)
	late := now.Add(-2 * time.Minute)
	firstFinish := now.Add(-time.Minute)
	lastFinish := now.Add(-30 * time.Second)
	h := holons.Holon{ID: "h", Title: "H", Prompt: "p", Kind: holons.KindNormal, Status: holons.StatusQueued, BaseCommit: "abc", CreatedAt: now, AgentSessions: []holons.AgentSession{{ID: "one", HolonID: "h", AgentType: "codex", Title: "One", Status: "completed", CreatedAt: now, UpdatedAt: now, StartedAt: &late, FinishedAt: &firstFinish}, {ID: "two", HolonID: "h", AgentType: "codex", Title: "Two", Status: "running", CreatedAt: now, UpdatedAt: now.Add(time.Second), StartedAt: &early}}}
	if e := store.Create(context.Background(), h); e != nil {
		t.Fatal(e)
	}
	got, e := store.UpdateAgentSession(context.Background(), "h", h.AgentSessions[1], true)
	if e != nil {
		t.Fatal(e)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(early) || got.FinishedAt != nil {
		t.Fatalf("active timestamps start=%v finish=%v", got.StartedAt, got.FinishedAt)
	}
	two := got.AgentSessions[1]
	two.Status = "failed"
	two.FinishedAt = &lastFinish
	two.UpdatedAt = now.Add(2 * time.Second)
	got, e = store.UpdateAgentSession(context.Background(), "h", two, true)
	if e != nil {
		t.Fatal(e)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(early) || got.FinishedAt == nil || !got.FinishedAt.Equal(lastFinish) {
		t.Fatalf("terminal timestamps start=%v finish=%v", got.StartedAt, got.FinishedAt)
	}
}

func TestClosingLastVisibleAgentCancelsHolonAndRetainsHistory(t *testing.T) {
	db, store := openStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	defer db.Close()
	now := time.Now().UTC()
	h := holons.Holon{ID: "h", Title: "H", Prompt: "p", Kind: holons.KindNormal, Status: holons.StatusRunning, BaseCommit: "abc", WorktreeBranch: "holark/h", WorktreePath: "/w", CreatedAt: now, AgentSessions: []holons.AgentSession{{ID: "a", HolonID: "h", AgentType: "codex", Title: "A", Status: "running", CreatedAt: now, UpdatedAt: now}}}
	if e := store.Create(context.Background(), h); e != nil {
		t.Fatal(e)
	}
	got, e := holons.NewService(store).CloseAgentSession(context.Background(), "h", "a")
	if e != nil {
		t.Fatal(e)
	}
	if got.Status != holons.StatusCancelled || got.FinishedAt == nil || len(got.AgentSessions) != 1 || got.AgentSessions[0].ClosedAt == nil || got.AgentSessions[0].Status != "cancelling" {
		t.Fatalf("closed=%+v", got)
	}
}

func TestAddAgentRejectsClosedReadOnlyAndIncompleteWorkspaceHolons(t *testing.T) {
	db, store := openStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	defer db.Close()
	now := time.Now().UTC()
	closed := now
	for _, h := range []holons.Holon{{ID: "archived", ArchivedAt: &closed, WorktreeBranch: "b", WorktreePath: "/w"}, {ID: "readonly", ReadOnly: true, WorktreeBranch: "b", WorktreePath: "/w"}, {ID: "incomplete"}} {
		h.Title = "H"
		h.Prompt = "p"
		h.Kind = holons.KindNormal
		h.Status = holons.StatusCompleted
		h.BaseCommit = "abc"
		h.CreatedAt = now
		if e := store.Create(context.Background(), h); e != nil {
			t.Fatal(e)
		}
		if _, e := holons.NewService(store).AddAgentSession(context.Background(), h.ID, "codex", "p"); !errors.Is(e, holons.ErrInvalid) {
			t.Fatalf("add %s=%v", h.ID, e)
		}
	}
}
