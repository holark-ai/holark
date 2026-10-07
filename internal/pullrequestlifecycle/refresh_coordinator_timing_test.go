package pullrequestlifecycle

import (
	"context"
	"testing"
	"time"
)

type timedRetryError struct{ delay time.Duration }

func (err timedRetryError) Error() string             { return "retry later" }
func (err timedRetryError) RetryAfter() time.Duration { return err.delay }

func TestRunBasicTimingDoesNotDelayExplicitRefreshes(t *testing.T) {
	coordinator := NewRefreshCoordinator()
	defer coordinator.Close()
	delays := make(chan time.Duration, 4)
	release := make(chan struct{}, 4)
	coordinator.wait = func(ctx context.Context, delay time.Duration) bool {
		delays <- delay
		select {
		case <-ctx.Done():
			return false
		case <-release:
			return true
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := make(chan int, 2)
	callCount := 0
	go coordinator.RunBasic(ctx, func(context.Context) error {
		callCount++
		call := callCount
		calls <- call
		if call == 2 {
			return timedRetryError{delay: 2 * time.Minute}
		}
		return nil
	})

	if delay := <-delays; delay != time.Minute {
		t.Fatalf("initial delay = %v", delay)
	}
	explicit := make(chan struct{})
	coordinator.Trigger(t.Context(), RefreshKey{RepositoryID: "repo", Section: "explicit"}, func(context.Context) error {
		close(explicit)
		return nil
	})
	select {
	case <-explicit:
	case <-time.After(time.Second):
		t.Fatal("explicit refresh waited for background timer")
	}

	release <- struct{}{}
	<-calls
	if delay := <-delays; delay != time.Minute {
		t.Fatalf("successful delay = %v", delay)
	}
	release <- struct{}{}
	<-calls
	if delay := <-delays; delay != 2*time.Minute {
		t.Fatalf("provider retry delay = %v", delay)
	}
	cancel()
}
