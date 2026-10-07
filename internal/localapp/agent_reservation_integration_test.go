package localapp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/terminals"
)

// Cancel after the actual SQLite commit, optionally before the final store read.
type cancelledReservationStore struct {
	holons.Store
	cancel          context.CancelFunc
	readAfterCancel bool
}

func (s *cancelledReservationStore) AddAgentSession(ctx context.Context, id string, a holons.AgentSession) (holons.Holon, error) {
	h, err := s.Store.AddAgentSession(ctx, id, a)
	if err == nil && s.cancel != nil {
		s.cancel()
		s.cancel = nil
		if s.readAfterCancel {
			return s.Store.Get(ctx, id)
		}
	}
	return h, err
}

type reservationLauncher struct {
	holonAgentLauncher
	launch func(context.Context, string, string) error
}

func (l reservationLauncher) Launch(ctx context.Context, hid, aid string, _ terminals.Dimensions, _ string, _ agentsessions.LaunchOptions) error {
	return l.launch(ctx, hid, aid)
}

func TestAgentReservationRetryAfterCancellation(t *testing.T) {
	for _, finalRead := range []bool{true, false} {
		name := "launch session read"
		if finalRead {
			name = "reservation final read"
		}
		t.Run(name, func(t *testing.T) {
			service, store := terminalTestService(t)
			h, err := service.Get(t.Context(), "h")
			if err != nil {
				t.Fatal(err)
			}
			h.WorktreeBranch = "holark/h"
			if err := store.Update(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			service = holons.NewService(&cancelledReservationStore{Store: store, cancel: cancel, readAfterCancel: finalRead})
			state := localAgentState{holons: service}
			launches := 0
			runtime := &terminalHolonService{Service: service, agents: reservationLauncher{launch: func(ctx context.Context, hid, aid string) error {
				if _, err := state.Session(ctx, hid, aid); err != nil {
					return err
				}
				launches++
				return state.Running(ctx, hid, aid)
			}}}
			if _, err := runtime.AddAgentSessionOnce(ctx, "h", "codex", "Fix it", "request"); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled request: %v", err)
			}
			for retry := 0; retry < 2; retry++ {
				a, err := runtime.AddAgentSessionOnce(t.Context(), "h", "codex", "Fix it", "request")
				if err != nil || a.ID != holons.AgentRequestID("h", "request") || a.Status != string(holons.StatusRunning) {
					t.Fatalf("retry returned %+v, %v", a, err)
				}
			}
			if launches != 1 {
				t.Fatalf("launches = %d, want 1", launches)
			}
			if _, err := runtime.AddAgentSessionOnce(t.Context(), "h", "codex", "Different prompt", "request"); !errors.Is(err, holons.ErrInvalid) {
				t.Fatalf("conflicting retry: %v", err)
			}
		})
	}
}

func TestAgentReservationConcurrentRetriesLaunchOnce(t *testing.T) {
	service, store := terminalTestService(t)
	now := time.Now().UTC()
	if _, err := store.AddAgentSession(t.Context(), "h", holons.AgentSession{
		ID: holons.AgentRequestID("h", "request"), HolonID: "h", AgentType: "codex", Prompt: "Fix it",
		Status: string(holons.StatusQueued), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	state := localAgentState{holons: service}
	var launches atomic.Int32
	started := make(chan struct{}, 16)
	release := make(chan struct{})
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(release) }) })
	runtime := &terminalHolonService{Service: service, agents: reservationLauncher{launch: func(ctx context.Context, hid, aid string) error {
		launches.Add(1)
		started <- struct{}{}
		<-release
		return state.Running(ctx, hid, aid)
	}}}
	results := make(chan error, 16)
	for i := 0; i < cap(results); i++ {
		go func() {
			a, err := runtime.AddAgentSessionOnce(t.Context(), "h", "codex", "Fix it", "request")
			if err == nil && a.Status != string(holons.StatusRunning) {
				err = errors.New("retry returned an unstarted reservation")
			}
			results <- err
		}()
	}
	select {
	case <-started:
	case err := <-results:
		t.Fatalf("request returned before launching: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runtime.AddAgentSessionOnce(ctx, "h", "codex", "Fix it", "request"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled concurrent retry: %v", err)
	}
	unblock.Do(func() { close(release) })
	for i := 0; i < cap(results); i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := launches.Load(); got != 1 {
		t.Fatalf("launches = %d, want 1", got)
	}
}

func TestAgentReservationRetryDoesNotLaunchOwnedConversation(t *testing.T) {
	for _, field := range []string{"terminal", "resume target", "rollout"} {
		t.Run(field, func(t *testing.T) {
			service, store := terminalTestService(t)
			now := time.Now().UTC()
			a := holons.AgentSession{
				ID: holons.AgentRequestID("h", "request"), HolonID: "h", AgentType: "codex", Prompt: "Fix it",
				Status: string(holons.StatusQueued), CreatedAt: now, UpdatedAt: now,
			}
			switch field {
			case "terminal":
				a.TerminalID = "existing-terminal"
			case "resume target":
				a.ResumeTarget = "existing-conversation"
			case "rollout":
				a.RolloutPath = "existing-rollout"
			}
			if _, err := store.AddAgentSession(t.Context(), "h", a); err != nil {
				t.Fatal(err)
			}
			launcher := &recordingAgentLauncher{}
			runtime := &terminalHolonService{Service: service, agents: launcher}
			got, err := runtime.AddAgentSessionOnce(t.Context(), "h", "codex", "Fix it", "request")
			if err != nil || got.ID != a.ID || len(launcher.launches) != 0 {
				t.Fatalf("owned reservation retried: %+v, %v; launches: %v", got, err, launcher.launches)
			}
		})
	}
}
