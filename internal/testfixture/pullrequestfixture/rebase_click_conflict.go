package pullrequestfixture

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
)

// rebaseClickConflictHandler creates the ref race at the production HTTP
// boundary. Browser launch plumbing releases the click one second after a real
// clean preview. This handler then advances main with an overlapping change
// immediately before the production endpoint takes its click-time ref snapshot.
func (scenario *Scenario) rebaseClickConflictHandler(next http.Handler) http.Handler {
	var mu sync.Mutex
	advanced := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pullRequestID, rebaseStart := rebaseStartPullRequestID(r)
		if !rebaseStart {
			next.ServeHTTP(w, r)
			return
		}

		mu.Lock()
		defer mu.Unlock()
		if advanced {
			next.ServeHTTP(w, r)
			return
		}
		if err := advanceConflictingBase(scenario.repositoryPath); err != nil {
			http.Error(w, fmt.Sprintf("advance main at rebase click: %v", err), http.StatusInternalServerError)
			return
		}
		advanced = true
		log.Printf("Rebase click race: clean preview for %s was one second old when conflicting main commit landed", pullRequestID)
		next.ServeHTTP(w, r)
	})
}

func rebaseStartPullRequestID(r *http.Request) (string, bool) {
	if r.Method != http.MethodPost {
		return "", false
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "pull-requests" || parts[4] != "rebase" || parts[3] == "" {
		return "", false
	}
	return parts[3], true
}
