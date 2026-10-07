package localapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/holons"
	holonshttp "github.com/holark-ai/holark/internal/holons/httpapi"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
)

func TestChangeHolonBaseBranch(t *testing.T) {
	root := t.TempDir()
	gitPlumbingCommand(t, root, "init", "-b", "main")
	gitPlumbingCommand(t, root, "config", "user.name", "Base Test")
	gitPlumbingCommand(t, root, "config", "user.email", "base@example.test")
	gitPlumbingWrite(t, root, "base.txt", "base\n")
	gitPlumbingCommand(t, root, "add", ".")
	gitPlumbingCommand(t, root, "commit", "-m", "base")
	base := gitPlumbingOutput(t, root, "rev-parse", "HEAD")
	gitPlumbingCommand(t, root, "switch", "-c", "develop")
	gitPlumbingWrite(t, root, "develop.txt", "develop\n")
	gitPlumbingCommand(t, root, "add", ".")
	gitPlumbingCommand(t, root, "commit", "-m", "develop")
	develop := gitPlumbingOutput(t, root, "rev-parse", "HEAD")
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitPlumbingCommand(t, root, "clone", "--bare", root, remote)
	gitPlumbingCommand(t, root, "remote", "add", "origin", remote)
	adapter, err := gitadapter.OpenWithWorktrees(t.Context(), root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repositories := repository.NewService(adapter)
	if _, err = repositories.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, store := terminalTestService(t)
	service := holons.NewServiceWithRepository(store, holonRepositoryCoordinator{repositories})
	h, err := service.Create(t.Context(), holons.Create{Title: "Change base", BaseBranch: "main", BaseCommit: base, WorkSessionStartCommit: develop})
	if err != nil {
		t.Fatal(err)
	}
	gitPlumbingWrite(t, h.WorktreePath, "session.txt", "session\n")
	gitPlumbingCommand(t, h.WorktreePath, "add", ".")
	gitPlumbingCommand(t, h.WorktreePath, "commit", "-m", "session")
	head := gitPlumbingOutput(t, h.WorktreePath, "rev-parse", "HEAD")
	gitPlumbingWrite(t, h.WorktreePath, "dirty.txt", "keep this edit\n")
	handler := holonshttp.New(service)
	endpoint := "/api/v1/holons/" + h.ID
	change := func(branch string, want int) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"base_branch": branch})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPut, endpoint+"/base-branch", strings.NewReader(string(body))))
		if w.Code != want {
			t.Fatalf("change base %q: %d %s", branch, w.Code, w.Body.String())
		}
	}
	change("develop", http.StatusOK)
	// Read through a fresh service to check durable state, not just the response.
	service = holons.NewServiceWithRepository(store, holonRepositoryCoordinator{repositories})
	saved, err := service.Get(t.Context(), h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.BaseBranch != "develop" || saved.BaseCommit != develop || saved.WorkSessionStartCommit != develop {
		t.Fatalf("saved base/session boundaries: %+v", saved)
	}
	inspection, err := service.InspectWorkspaceWithOptions(t.Context(), h.ID, holons.InspectOptions{BaseRef: "main", TargetRef: "worktree", SummaryOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.BaseBranch != "develop" || inspection.BranchBaseCommit != develop || len(inspection.Commits) != 1 || len(inspection.Files) != 2 {
		t.Fatalf("new comparison: %+v", inspection)
	}
	if gitPlumbingOutput(t, h.WorktreePath, "rev-parse", "HEAD") != head || !inspection.Dirty {
		t.Fatal("changing base changed the workspace")
	}
	source, err := (localPullRequestHolons{service}).PullRequestSource(t.Context(), h.ID)
	if err != nil || source.BaseBranch != "develop" || source.BaseCommit != develop {
		t.Fatalf("future PR source: %+v, %v", source, err)
	}
	change("missing", http.StatusNotFound)
	change("", http.StatusBadRequest)
	still, _ := service.Get(t.Context(), h.ID)
	if still.BaseBranch != "develop" || still.BaseCommit != develop {
		t.Fatal("failed change replaced the base")
	}
	saved.RebaseAttempt = &holons.RebaseAttempt{State: "running"}
	if err = store.Update(t.Context(), saved); err != nil {
		t.Fatal(err)
	}
	change("main", http.StatusConflict)
}
