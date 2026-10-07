package pullrequestlifecycle

import (
	"context"
	"testing"
)

func TestRefreshFollowupRetainsCompletedResultForEveryJoinedCaller(t *testing.T) {
	coordinator := NewRefreshCoordinator()
	defer coordinator.Close()
	key := RefreshKey{RepositoryID: "repository", Section: "basic"}
	started, release := make(chan struct{}), make(chan struct{})
	coordinator.Trigger(t.Context(), key, func(context.Context) error { close(started); <-release; return nil })
	<-started
	// Register both callers before releasing the old pass. The last request
	// supplies the work, while both callers retain the same completed result.
	first := coordinator.enqueue(t.Context(), key, func(context.Context) (any, error) { t.Error("superseded work ran"); return nil, nil })
	second := coordinator.enqueue(t.Context(), key, func(context.Context) (any, error) { return SyncResult{Imported: 7, Updated: 3}, nil })
	close(release)
	for _, caller := range []*refreshPass{first, second} {
		<-caller.done
		result, ok := caller.result.(SyncResult)
		if caller.err != nil || !ok || result.Imported != 7 || result.Updated != 3 {
			t.Fatalf("joined result=%+v err=%v", caller.result, caller.err)
		}
	}
	// A later repository pass cannot overwrite either completed caller's result.
	later, err := coordinator.DoResult(t.Context(), key, func(context.Context) (any, error) { return SyncResult{Imported: 1}, nil })
	if err != nil || later.(SyncResult).Imported != 1 || first.result.(SyncResult).Imported != 7 {
		t.Fatalf("later=%+v retained=%+v err=%v", later, first.result, err)
	}
}
