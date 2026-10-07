package pullrequestfixture

import (
	"context"
	"fmt"
	"github.com/holark-ai/holark/internal/testfixture/branchfixture"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"

	pullrequestssqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
)

// outdatedReviewHandler reproduces a provider head advancing while Holark's
// local PR catalog is stale. The first review pins the old catalog revision;
// synchronization immediately afterward makes its stale range visible.
func (scenario *Scenario) outdatedReviewHandler(next http.Handler) http.Handler {
	var mu sync.Mutex
	advanced := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pullRequestID, reviewStart := reviewStartPullRequestID(r)
		if !reviewStart {
			next.ServeHTTP(w, r)
			return
		}

		mu.Lock()
		advance := !advanced
		var revision reviewAdvance
		if advance {
			var err error
			revision, err = scenario.advanceRemotePullRequest(r.Context(), pullRequestID)
			if err != nil {
				mu.Unlock()
				http.Error(w, fmt.Sprintf("advance simulated pull request: %v", err), http.StatusInternalServerError)
				return
			}
			advanced = true
		}
		mu.Unlock()

		response := httptest.NewRecorder()
		next.ServeHTTP(response, r)
		if advance && response.Code == http.StatusCreated {
			if err := scenario.synchronizeAdvancedPullRequest(r.Context(), pullRequestID, revision); err != nil {
				http.Error(w, fmt.Sprintf("synchronize simulated pull request: %v", err), http.StatusInternalServerError)
				return
			}
		}
		for key, values := range response.Header() {
			w.Header()[key] = append([]string(nil), values...)
		}
		w.WriteHeader(response.Code)
		_, _ = io.Copy(w, response.Body)
	})
}

func reviewStartPullRequestID(r *http.Request) (string, bool) {
	if r.Method != http.MethodPost {
		return "", false
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "pull-requests" || parts[4] != "reviews" || parts[3] == "" {
		return "", false
	}
	return parts[3], true
}

type reviewAdvance struct {
	previousHead string
	currentHead  string
}

func (scenario *Scenario) advanceRemotePullRequest(ctx context.Context, pullRequestID string) (reviewAdvance, error) {
	var headBranch, catalogHead string
	if err := scenario.Application.Database.QueryRowContext(ctx, `select head_branch, head_commit from pull_requests where id = ?`, pullRequestID).Scan(&headBranch, &catalogHead); err != nil {
		return reviewAdvance{}, err
	}
	workspaceHead, err := gitOutputContext(ctx, scenario.State.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return reviewAdvance{}, err
	}
	if strings.TrimSpace(workspaceHead) != catalogHead {
		return reviewAdvance{}, fmt.Errorf("source workspace is at %s, catalog is at %s", strings.TrimSpace(workspaceHead), catalogHead)
	}
	path := filepath.Join(scenario.State.WorktreePath, "outdated-review.txt")
	if err = os.WriteFile(path, []byte("The simulated provider advanced after Holark last synchronized this pull request.\n"), 0o600); err != nil {
		return reviewAdvance{}, err
	}
	for _, args := range [][]string{
		{"add", "outdated-review.txt"},
		{"-c", "user.name=Holark Browser Scenario", "-c", "user.email=browser-scenario@invalid", "commit", "-m", "Advance pull request outside Holark"},
		{"push", "origin", "HEAD:refs/heads/" + headBranch},
	} {
		if _, err = gitOutputContext(ctx, scenario.State.WorktreePath, args...); err != nil {
			return reviewAdvance{}, err
		}
	}
	currentHead, err := gitOutputContext(ctx, scenario.State.WorktreePath, "rev-parse", "HEAD")
	return reviewAdvance{previousHead: catalogHead, currentHead: strings.TrimSpace(currentHead)}, err
}

func (scenario *Scenario) synchronizeAdvancedPullRequest(ctx context.Context, pullRequestID string, revision reviewAdvance) error {
	store, err := pullrequestssqlite.New(ctx, scenario.Application.Database)
	if err != nil {
		return err
	}
	p, ok := store.GetPullRequest(pullRequestID)
	if !ok || p.HeadCommit != revision.previousHead {
		return fmt.Errorf("catalog no longer contains expected head %s", revision.previousHead)
	}
	mergeBase, err := gitOutputContext(ctx, scenario.State.WorktreePath, "merge-base", p.BaseCommit, revision.currentHead)
	if err != nil {
		return err
	}
	_, err = branchfixture.Accept(ctx, store, p.ID, p.BaseCommit, revision.currentHead, strings.TrimSpace(mergeBase))
	return err
}
