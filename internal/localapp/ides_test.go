package localapp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/holons"
	holonssqlite "github.com/holark-ai/holark/internal/holons/sqliteadapter"
	"github.com/holark-ai/holark/internal/ide"
	idesqlite "github.com/holark-ai/holark/internal/ide/sqliteadapter"
)

type closingIDERuntime struct {
	stopped []string
}

func (*closingIDERuntime) Start(context.Context, ide.Start) error { return nil }
func (r *closingIDERuntime) Stop(_ context.Context, id string) error {
	r.stopped = append(r.stopped, id)
	return nil
}
func (*closingIDERuntime) Target(string) (string, bool) { return "", false }
func (*closingIDERuntime) LogPath(string) string        { return "" }
func (*closingIDERuntime) Close() error                 { return nil }

type archivalRepository struct {
	archivedID     string
	archivedBranch string
}

func (*archivalRepository) CreateWorkspace(context.Context, string, string) (holons.Workspace, error) {
	return holons.Workspace{}, nil
}
func (*archivalRepository) InspectWorkspace(context.Context, string) (holons.WorkspaceInspection, error) {
	return holons.WorkspaceInspection{}, nil
}
func (*archivalRepository) PublishWorkspace(context.Context, string, holons.Publish) (holons.PublishedWorkspace, error) {
	return holons.PublishedWorkspace{}, nil
}
func (*archivalRepository) RemoveWorkspace(context.Context, string) error { return nil }
func (r *archivalRepository) ArchiveWorkspace(_ context.Context, id, branch string) error {
	r.archivedID = id
	r.archivedBranch = branch
	return nil
}
func (*archivalRepository) ReopenWorkspace(context.Context, string, string) error { return nil }

func TestClosingFinalIDEArchivesCompletedFiniteHolon(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "ides.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	holonsStore, err := holonssqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	idesStore, err := idesqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	holon := holons.Holon{
		ID: "finite", Title: "Finite", Kind: holons.KindPullWorker, Status: holons.StatusCompleted,
		BaseCommit: "base", WorktreeBranch: "holark/finite", WorktreePath: t.TempDir(),
		CreatedAt: now, FinishedAt: &now,
	}
	if err = holonsStore.Create(t.Context(), holon); err != nil {
		t.Fatal(err)
	}
	if _, err = idesStore.Create(t.Context(), ide.IDE{
		ID: "editor", HolonID: holon.ID, Provider: "vscode", State: ide.Ready,
		DesiredOpen: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	repository := &archivalRepository{}
	holonsService := holons.NewServiceWithRepository(holonsStore, repository)
	runtime := &closingIDERuntime{}
	service := &localIDEService{
		Service: ide.New(idesStore, nil, runtime),
		holons:  holonsService,
	}

	closed, err := service.Close(t.Context(), holon.ID, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if closed.State != ide.Closed || closed.DesiredOpen || len(runtime.stopped) != 1 || runtime.stopped[0] != "editor" {
		t.Fatalf("closed IDE=%+v stopped=%v", closed, runtime.stopped)
	}
	if repository.archivedID != holon.ID || repository.archivedBranch != holon.WorktreeBranch {
		t.Fatalf("archive call=%q %q", repository.archivedID, repository.archivedBranch)
	}
	got, err := holonsService.Get(t.Context(), holon.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArchivedAt == nil {
		t.Fatal("completed finite Holon was not marked archived")
	}
}
