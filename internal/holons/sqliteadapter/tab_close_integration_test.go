package sqliteadapter

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/holons"
)

type tabCloseRepository struct {
	holons.Repository
	archiveErr error
	archived   bool
}

func (r *tabCloseRepository) ArchiveWorkspace(context.Context, string, string) error {
	if r.archiveErr != nil {
		return r.archiveErr
	}
	r.archived = true
	return nil
}

func (*tabCloseRepository) ReopenWorkspace(context.Context, string, string) error { return nil }

func TestTabCloseArchivesOnlyAfterWholeHolonEnd(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        holons.Status
		agentTab, end bool
	}{
		{name: "last running agent", status: holons.StatusRunning, agentTab: true},
		{name: "shell after cancelled agent", status: holons.StatusCancelled},
		{name: "shell after cancelling agent", status: holons.StatusCancelling},
		{name: "whole shutdown shell", status: holons.StatusCancelling, end: true},
		{name: "terminal whole shutdown shell", status: holons.StatusCancelled, end: true},
		{name: "whole shutdown closed agent", status: holons.StatusCancelled, agentTab: true, end: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tabs.sqlite")
			db, store := openStore(t, path)
			t.Cleanup(func() { _ = db.Close() })
			now := time.Now().UTC()
			h := holons.Holon{ID: "h", Title: "Workspace", Kind: holons.KindNormal, Status: test.status,
				BaseBranch: "main", BaseCommit: "base", WorktreePath: t.TempDir(), WorktreeBranch: "holark/h", CreatedAt: now,
				AgentSessions: []holons.AgentSession{{ID: "agent", HolonID: "h", AgentType: "codex", Status: string(test.status), CreatedAt: now, UpdatedAt: now}},
			}
			if test.status == holons.StatusCancelling || test.end {
				h.AgentSessions[0].Status = "cancelled"
				if test.agentTab && test.end {
					h.AgentSessions[0].ClosedAt = &now
				}
			}
			if err := store.Create(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			repository := &tabCloseRepository{}
			service := holons.NewServiceWithRepository(store, repository)
			if !test.agentTab {
				if _, err := store.AddManualTerminal(t.Context(), h.ID, holons.ManualTerminal{ID: "shell", HolonID: h.ID, CreatedAt: now, UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			if test.end {
				if _, err := service.End(t.Context(), h.ID); err != nil {
					t.Fatal(err)
				}
				repository.archiveErr = errors.New("archive failed")
			}
			closeTab := func() (holons.Holon, error) {
				if test.agentTab {
					return service.CloseAgentSession(t.Context(), h.ID, "agent")
				}
				return service.CloseManualTerminal(t.Context(), h.ID, "shell")
			}
			for attempt := 0; attempt < 2; attempt++ {
				_, err := closeTab()
				if !errors.Is(err, repository.archiveErr) {
					t.Fatalf("close %d: %v", attempt, err)
				}
				after, err := service.Get(t.Context(), h.ID)
				if err != nil || after.ArchivedAt != nil || repository.archived {
					t.Fatalf("workspace archived: %+v err=%v", after, err)
				}
			}
			// Retrying after a database reopen must recover shutdown intent.
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, store = openStore(t, path)
			service = holons.NewServiceWithRepository(store, repository)
			repository.archiveErr = nil
			after, err := closeTab()
			if err != nil || (after.ArchivedAt != nil) != test.end || repository.archived != test.end {
				t.Fatalf("retry: %+v archived=%v err=%v", after, repository.archived, err)
			}
			if test.end {
				if _, err := service.Reopen(t.Context(), h.ID); err != nil {
					t.Fatal(err)
				}
				repository.archived = false
				after, err = closeTab()
				if err != nil || after.ArchivedAt != nil || repository.archived || after.EndRequested {
					t.Fatalf("reopened workspace archived: %+v err=%v", after, err)
				}
			}
		})
	}
}
