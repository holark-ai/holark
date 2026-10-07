package localapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/holons"
	holonshttp "github.com/holark-ai/holark/internal/holons/httpapi"
	"github.com/holark-ai/holark/internal/repository"
)

func TestManualAsyncPreparationIsVisibleAndOwnedBeyondRequest(t *testing.T) {
	for _, outcome := range []string{"shell", "cancel", "failure", "shell failure", "agent unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			f := newTerminalCreationFixture(t)
			if outcome == "shell failure" {
				f.manager.Close()
			}
			ctx, cancel := context.WithCancel(t.Context())
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			f.service.startups = &manualStartupJobs{ctx: ctx, prepare: func(ctx context.Context, branch string) (repository.Preparation, error) {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return repository.Preparation{}, ctx.Err()
				}
				if outcome == "failure" {
					return repository.Preparation{}, errors.New("fetch unavailable")
				}
				return f.repositories.PrepareBranch(ctx, branch)
			}}
			t.Cleanup(func() { unblock(); cancel(); f.service.startups.Close() })
			mode := "terminal"
			if outcome == "agent unavailable" {
				mode = "agent"
			}
			requestCtx, cancelRequest := context.WithCancel(t.Context())
			handler := holonshttp.New(f.service)
			request := httptest.NewRequest("POST", "/api/v1/holons", strings.NewReader(`{"kind":"normal","startup_mode":"`+mode+`","base_branch":"feature/terminal"}`)).WithContext(requestCtx)
			request.Header.Set("Prefer", "respond-async")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			cancelRequest()
			var h holons.Holon
			if err := json.Unmarshal(response.Body.Bytes(), &h); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusAccepted || h.Status != holons.StatusPreparing || h.WorktreePath != "" || len(h.AgentSessions) != 0 || len(h.ManualTerminals) != 0 || response.Header().Get("Location") != "/api/v1/holons/"+h.ID {
				t.Fatalf("acceptance=%d %s headers=%v", response.Code, response.Body, response.Header())
			}
			waitReservedWork(t, entered)
			for _, path := range []string{"/api/v1/holons", "/api/v1/holons/" + h.ID} {
				read := httptest.NewRecorder()
				handler.ServeHTTP(read, httptest.NewRequest("GET", path, nil))
				if read.Code != 200 || !strings.Contains(read.Body.String(), `"status":"preparing"`) {
					t.Fatalf("preparation read: %d %s", read.Code, read.Body)
				}
			}
			if outcome == "cancel" {
				ended, err := f.service.End(t.Context(), h.ID)
				if err != nil || ended.Status != holons.StatusCancelled {
					t.Fatalf("end=%+v %v", ended, err)
				}
			}
			unblock()
			f.service.startups.Close()
			latest, err := f.service.Get(t.Context(), h.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch outcome {
			case "shell":
				if latest.Status != holons.StatusRunning || latest.BaseCommit != f.preparation.Commit || len(latest.ManualTerminals) != 1 {
					t.Fatalf("started=%+v", latest)
				}
				assertTerminalShellUsable(t, f.manager, latest)
			case "cancel":
				if latest.Status != holons.StatusCancelled || latest.WorktreePath != "" || len(latest.ManualTerminals) != 0 {
					t.Fatalf("reactivated=%+v", latest)
				}
			default:
				if latest.Status != holons.StatusFailed || latest.Reason == "" || latest.FinishedAt == nil || latest.ID != h.ID {
					t.Fatalf("failure=%+v", latest)
				}
			}
		})
	}
}

func TestManualPreparationRestartFailsOnlyUnlaunchedReservations(t *testing.T) {
	f := newTerminalCreationFixture(t)
	ctx := t.Context()
	pending, err := f.service.ReserveManual(ctx, holons.Create{Kind: holons.KindNormal, BaseBranch: "feature/terminal"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := f.service.ReserveManual(ctx, holons.Create{Kind: holons.KindNormal})
	if err != nil {
		t.Fatal(err)
	}
	agent, err = f.service.PrepareReserved(ctx, agent.ID, holons.Create{Kind: holons.KindNormal, AgentType: "codex", BaseBranch: f.preparation.Branch, BaseCommit: f.preparation.Commit})
	if err != nil {
		t.Fatal(err)
	}
	running := f.create(t, "")
	queued, err := f.service.Reserve(ctx, "queued-pr", holons.Create{Kind: holons.KindPullWorker, PullRequestID: "pr"})
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct the service over the same SQLite records, discarding all local jobs.
	recovered := holons.NewServiceWithRepository(f.store, holonRepositoryCoordinator{repositories: f.repositories})
	if err = settleInterruptedManualPreparations(ctx, recovered); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{pending.ID, agent.ID} {
		h, err := recovered.Get(ctx, id)
		if err != nil || h.Status != holons.StatusFailed || !strings.Contains(h.Reason, "stopped before preparation") {
			t.Fatalf("interrupted=%+v %v", h, err)
		}
	}
	for id, want := range map[string]holons.Status{running.ID: holons.StatusRunning, queued.ID: holons.StatusPreparing} {
		h, err := recovered.Get(ctx, id)
		if err != nil || h.Status != want {
			t.Fatalf("existing recovery changed=%+v %v", h, err)
		}
	}
	assertTerminalShellUsable(t, f.manager, running)
}

func TestManualPreparationRestartSettlesUnfinishedShellStartup(t *testing.T) {
	for _, launched := range []bool{false, true} {
		name := "before launch"
		if launched {
			name = "before finalization"
		}
		t.Run(name, func(t *testing.T) {
			f := newTerminalCreationFixture(t)
			ctx := t.Context()
			in := holons.Create{Kind: holons.KindNormal, StartupMode: "terminal", BaseBranch: f.preparation.Branch, BaseCommit: f.preparation.Commit}
			h, err := f.service.ReserveManual(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			h, err = f.service.PrepareReserved(ctx, h.ID, in)
			if err != nil {
				t.Fatal(err)
			}
			if launched {
				h, err = f.service.AddManualTerminal(ctx, h.ID, "", "", "")
			} else {
				// Persist the tab as AddManualTerminal does before launching its process.
				h, err = f.service.Service.AddManualTerminal(ctx, h.ID, "Shell", h.WorktreePath, "interrupted-terminal")
			}
			if err != nil {
				t.Fatal(err)
			}
			if launched {
				assertTerminalShellUsable(t, f.manager, h)
			}
			f.manager.Close()
			recovered := holons.NewServiceWithRepository(f.store, holonRepositoryCoordinator{repositories: f.repositories})
			// Repeat startup recovery to verify that the settled outcome stays durable.
			for range 2 {
				if err := settleInterruptedManualPreparations(ctx, recovered); err != nil {
					t.Fatal(err)
				}
				if err := clearStaleTerminalBindings(ctx, recovered); err != nil {
					t.Fatal(err)
				}
				latest, err := recovered.Get(ctx, h.ID)
				if err != nil || latest.Status != holons.StatusFailed || latest.FinishedAt == nil || latest.StartedAt != nil || !strings.Contains(latest.Reason, "stopped before preparation") {
					t.Fatalf("interrupted shell=%+v %v", latest, err)
				}
				if latest.WorktreePath != h.WorktreePath || len(latest.ManualTerminals) != 1 || latest.ManualTerminals[0].ID != h.ManualTerminals[0].ID || latest.ManualTerminals[0].TerminalID != "" {
					t.Fatalf("recovered workspace or tab=%+v", latest)
				}
			}
		})
	}
}

func TestManualStartupJobsJoinBeforeShutdown(t *testing.T) {
	f := newTerminalCreationFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancelled, release := make(chan struct{}), make(chan struct{})
	f.service.startups = &manualStartupJobs{ctx: ctx, prepare: func(ctx context.Context, _ string) (repository.Preparation, error) {
		<-ctx.Done()
		close(cancelled)
		<-release
		return repository.Preparation{}, ctx.Err()
	}}
	h, err := f.service.CreateAsync(t.Context(), holons.Create{Kind: holons.KindNormal, StartupMode: "terminal", BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	waitReservedWork(t, cancelled)
	joined := make(chan struct{})
	go func() { f.service.startups.Close(); close(joined) }()
	select {
	case <-joined:
		t.Fatal("startup owner returned before preparation stopped")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	waitReservedWork(t, joined)
	h, err = f.service.Get(t.Context(), h.ID)
	if err != nil || h.Status != holons.StatusFailed {
		t.Fatalf("shutdown preparation=%+v %v", h, err)
	}
}
