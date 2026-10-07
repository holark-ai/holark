package localapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/holons"
	holonhttp "github.com/holark-ai/holark/internal/holons/httpapi"
	"github.com/holark-ai/holark/internal/sessionterminals"
	"github.com/holark-ai/holark/internal/terminals"
)

func closeTabRequest(handler http.Handler, holonID, kind, childID string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/holons/"+holonID+"/"+kind+"/"+childID+"/close", nil))
	return w
}

func TestTabCloseHTTPRepeatAndConcurrent(t *testing.T) {
	for _, runtime := range []bool{false, true} {
		for _, kind := range []string{"agent-sessions", "terminals"} {
			name := "domain/"
			if runtime {
				name = "runtime/"
			}
			t.Run(name+kind, func(t *testing.T) {
				service, store := terminalTestService(t)
				now := time.Now().UTC()
				exit := 7
				one, _ := terminals.NewID()
				two, _ := terminals.NewID()
				h := holons.Holon{ID: "tabs", Title: "Tabs", Kind: holons.KindNormal, Status: holons.StatusFailed, CreatedAt: now,
					AgentSessions: []holons.AgentSession{
						{ID: "agent", HolonID: "tabs", AgentType: "codex", Status: "failed", Reason: "retained outcome", ExitCode: &exit, CreatedAt: now, UpdatedAt: now, FinishedAt: &now},
						{ID: "sibling-agent", HolonID: "tabs", AgentType: "codex", Status: "completed", CreatedAt: now, UpdatedAt: now},
					},
					ManualTerminals: []holons.ManualTerminal{
						{ID: "shell", HolonID: "tabs", TerminalID: string(one), CreatedAt: now, UpdatedAt: now},
						{ID: "sibling-shell", HolonID: "tabs", TerminalID: string(two), CreatedAt: now, UpdatedAt: now},
					},
				}
				if err := store.Create(t.Context(), h); err != nil {
					t.Fatal(err)
				}
				for _, shell := range h.ManualTerminals {
					if _, err := store.AddManualTerminal(t.Context(), h.ID, shell); err != nil {
						t.Fatal(err)
					}
				}
				before, err := service.Get(t.Context(), h.ID)
				if err != nil {
					t.Fatal(err)
				}
				gateway := &launchGateway{}
				handler := holonhttp.New(service)
				if runtime {
					handler = holonhttp.New(terminalRuntime(t, service, gateway))
				}
				childID := "agent"
				if kind == "terminals" {
					childID = "shell"
				}
				// Start together so the first close races with another request.
				responses := make([]*httptest.ResponseRecorder, 2)
				start := make(chan struct{})
				var wg sync.WaitGroup
				for i := range responses {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						responses[i] = closeTabRequest(handler, h.ID, kind, childID)
					}()
				}
				close(start)
				wg.Wait()
				responses = append(responses, closeTabRequest(handler, h.ID, kind, childID))
				for _, response := range responses {
					if response.Code != http.StatusOK {
						t.Fatalf("close: %d %s", response.Code, response.Body)
					}
					if response.Body.String() != responses[0].Body.String() {
						t.Fatalf("repeat changed record: %s != %s", response.Body, responses[0].Body)
					}
				}
				var record struct {
					ID       string
					ClosedAt *time.Time `json:"closed_at"`
				}
				if err := json.Unmarshal(responses[0].Body.Bytes(), &record); err != nil || record.ID != childID || record.ClosedAt == nil {
					t.Fatalf("closed record=%+v err=%v", record, err)
				}
				after, err := service.Get(t.Context(), h.ID)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before.AgentSessions[1], after.AgentSessions[1]) || !reflect.DeepEqual(before.ManualTerminals[1], after.ManualTerminals[1]) {
					t.Fatal("close changed sibling tabs")
				}
				if a := after.AgentSessions[0]; a.Status != "failed" || a.Reason != "retained outcome" || a.ExitCode == nil || *a.ExitCode != exit || !a.FinishedAt.Equal(now) {
					t.Fatalf("close changed agent outcome: %+v", a)
				}
				if runtime && kind == "terminals" && (len(gateway.closed) != 1 || gateway.closed[0] != one) {
					t.Fatalf("shutdown repeated or affected sibling: %v", gateway.closed)
				}
				missingCode, missingMessage := "agent_session_not_found", "Agent session not found."
				if kind == "terminals" {
					missingCode, missingMessage = "manual_terminal_not_found", "Shell tab not found."
				}
				for _, missing := range []struct{ holonID, childID, code, message string }{
					{"missing", childID, "holon_not_found", "Holon not found."},
					{h.ID, "unknown", missingCode, missingMessage},
					{"h", childID, missingCode, missingMessage},
				} {
					response := closeTabRequest(handler, missing.holonID, kind, missing.childID)
					var got struct{ Code, Message string }
					if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || response.Code != 404 || got.Code != missing.code || got.Message != missing.message {
						t.Fatalf("missing %s/%s: %d %s (%v)", missing.holonID, missing.childID, response.Code, response.Body, err)
					}
				}
			})
		}
	}
}

func TestTabCloseHTTPShutdownFailureAndRetry(t *testing.T) {
	for _, kind := range []string{"agent-sessions", "terminals"} {
		for _, failure := range []error{errors.New("shutdown failed"), context.Canceled} {
			t.Run(kind+"/"+failure.Error(), func(t *testing.T) {
				service, store := terminalTestService(t)
				now := time.Now().UTC()
				id, _ := terminals.NewID()
				var err error
				if kind == "agent-sessions" {
					_, err = store.AddAgentSession(t.Context(), "h", holons.AgentSession{ID: "tab", HolonID: "h", TerminalID: string(id), AgentType: "codex", Status: "running", CreatedAt: now, UpdatedAt: now})
				} else {
					_, err = store.AddManualTerminal(t.Context(), "h", holons.ManualTerminal{ID: "tab", HolonID: "h", TerminalID: string(id), CreatedAt: now, UpdatedAt: now})
				}
				if err != nil {
					t.Fatal(err)
				}
				gateway := &launchGateway{closeErr: map[terminals.TerminalID]error{id: failure}, closeCheck: func(terminals.TerminalID) {
					h, err := service.Get(t.Context(), "h")
					if err != nil {
						t.Fatal(err)
					}
					if kind == "agent-sessions" && h.AgentSessions[0].ClosedAt != nil || kind == "terminals" && h.ManualTerminals[0].ClosedAt != nil {
						t.Fatal("tab closed before shutdown succeeded")
					}
				}}
				handler := holonhttp.New(terminalRuntime(t, service, gateway))
				wantStatus := http.StatusInternalServerError
				if errors.Is(failure, context.Canceled) {
					wantStatus = http.StatusRequestTimeout
				}
				if response := closeTabRequest(handler, "h", kind, "tab"); response.Code != wantStatus {
					t.Fatalf("shutdown failure: %d %s", response.Code, response.Body)
				}
				h, err := service.Get(t.Context(), "h")
				if err != nil {
					t.Fatal(err)
				}
				if kind == "agent-sessions" && (h.AgentSessions[0].ClosedAt != nil || h.AgentSessions[0].TerminalID != string(id)) || kind == "terminals" && (h.ManualTerminals[0].ClosedAt != nil || h.ManualTerminals[0].TerminalID != string(id)) {
					t.Fatalf("failed shutdown hid tab: %+v", h)
				}
				delete(gateway.closeErr, id)
				first := closeTabRequest(handler, "h", kind, "tab")
				repeat := closeTabRequest(handler, "h", kind, "tab")
				if first.Code != 200 || repeat.Code != 200 || first.Body.String() != repeat.Body.String() || len(gateway.closed) != 2 {
					t.Fatalf("retry=%d %s repeated=%d %s shutdowns=%v", first.Code, first.Body, repeat.Code, repeat.Body, gateway.closed)
				}
			})
		}
	}
}

type closeRaceProducts struct {
	sessionterminals.Products
	beforeResolve func()
}

func (p *closeRaceProducts) ResolveTerminalBinding(id terminals.TerminalID) (sessionterminals.Binding, bool) {
	p.beforeResolve()
	return p.Products.ResolveTerminalBinding(id)
}

func TestShellCloseCompletionRace(t *testing.T) {
	for _, when := range []string{"before request", "before resolve", "during shutdown", "binding lost while open"} {
		t.Run(when, func(t *testing.T) {
			service, store := terminalTestService(t)
			id, _ := terminals.NewID()
			now := time.Now().UTC()
			if _, err := store.AddManualTerminal(t.Context(), "h", holons.ManualTerminal{ID: "shell", HolonID: "h", TerminalID: string(id), CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			products := &localTerminalProducts{holons: service}
			complete := func() {
				if err := products.ApplyTerminalCompletion(localTerminalHost, terminals.ProcessCompletion{TerminalID: id}); err != nil {
					t.Fatal(err)
				}
			}
			var port sessionterminals.Products = products
			gateway := &launchGateway{}
			switch when {
			case "before request":
				complete()
			case "before resolve":
				port = &closeRaceProducts{Products: products, beforeResolve: complete}
			case "during shutdown":
				gateway.closeCheck = func(terminals.TerminalID) { complete() }
			case "binding lost while open":
				port = &closeRaceProducts{Products: products, beforeResolve: func() {
					if _, err := service.SetManualTerminalBinding(t.Context(), "h", "shell", ""); err != nil {
						t.Fatal(err)
					}
				}}
			}
			coordinator, err := sessionterminals.New(gateway, port)
			if err != nil {
				t.Fatal(err)
			}
			handler := holonhttp.New(&terminalHolonService{Service: service, terminals: coordinator})
			response := closeTabRequest(handler, "h", "terminals", "shell")
			if when == "binding lost while open" {
				h, err := service.Get(t.Context(), "h")
				if err != nil || response.Code != 500 || h.ManualTerminals[0].ClosedAt != nil {
					t.Fatalf("unconfirmed close: %d %s holon=%+v err=%v", response.Code, response.Body, h, err)
				}
				return
			}
			repeat := closeTabRequest(handler, "h", "terminals", "shell")
			if response.Code != 200 || repeat.Code != 200 || response.Body.String() != repeat.Body.String() {
				t.Fatalf("completion close=%d %s repeat=%d %s", response.Code, response.Body, repeat.Code, repeat.Body)
			}
		})
	}
}

func TestShellCloseAcceptsHostConfirmedGone(t *testing.T) {
	for _, gone := range []error{terminals.ErrNotFound, terminals.ErrProcessLost, terminals.ErrProcessEnded, terminals.ErrNotRunning} {
		t.Run(gone.Error(), func(t *testing.T) {
			service, store := terminalTestService(t)
			id, _ := terminals.NewID()
			now := time.Now().UTC()
			if _, err := store.AddManualTerminal(t.Context(), "h", holons.ManualTerminal{ID: "shell", HolonID: "h", TerminalID: string(id), CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			handler := holonhttp.New(terminalRuntime(t, service, &launchGateway{closeErr: map[terminals.TerminalID]error{id: gone}}))
			response := closeTabRequest(handler, "h", "terminals", "shell")
			h, err := service.Get(t.Context(), "h")
			if err != nil || response.Code != 200 || h.ManualTerminals[0].ClosedAt == nil {
				t.Fatalf("confirmed gone: %d %s holon=%+v err=%v", response.Code, response.Body, h, err)
			}
		})
	}
}

type retryCloseRepository struct {
	archivalRepository
	failure error
}

func (r *retryCloseRepository) ArchiveWorkspace(ctx context.Context, id, branch string) error {
	if r.failure != nil {
		return r.failure
	}
	return r.archivalRepository.ArchiveWorkspace(ctx, id, branch)
}

func TestTabCloseRetriesCleanupAfterDurableClosure(t *testing.T) {
	for _, mode := range []string{"agent", "shell", "cancelling shell", "closed cancelling agent"} {
		t.Run(mode, func(t *testing.T) {
			_, store := terminalTestService(t)
			now := time.Now().UTC()
			id, _ := terminals.NewID()
			h := holons.Holon{ID: "cleanup", Title: "Cleanup", Kind: holons.KindPullReview, Status: holons.StatusCompleted, WorktreePath: t.TempDir(), WorktreeBranch: "holark/cleanup", CreatedAt: now}
			kind := "terminals"
			if mode == "agent" || mode == "closed cancelling agent" {
				kind = "agent-sessions"
				h.AgentSessions = []holons.AgentSession{{ID: "tab", HolonID: h.ID, AgentType: "codex", Status: "completed", CreatedAt: now, UpdatedAt: now, FinishedAt: &now}}
			} else {
				h.ManualTerminals = []holons.ManualTerminal{{ID: "tab", HolonID: h.ID, TerminalID: string(id), CreatedAt: now, UpdatedAt: now}}
			}
			if mode == "cancelling shell" {
				h.Kind, h.Status = holons.KindNormal, holons.StatusCancelling
			}
			if mode == "closed cancelling agent" {
				h.Kind, h.Status = holons.KindNormal, holons.StatusCancelling
				h.AgentSessions[0].Status = string(holons.StatusCancelled)
				h.AgentSessions[0].ClosedAt = &now
			}
			if err := store.Create(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			for _, shell := range h.ManualTerminals {
				if _, err := store.AddManualTerminal(t.Context(), h.ID, shell); err != nil {
					t.Fatal(err)
				}
			}
			repository := &retryCloseRepository{failure: errors.New("archive failed")}
			service := holons.NewServiceWithRepository(store, repository)
			if h.Kind == holons.KindNormal {
				if _, err := service.End(t.Context(), h.ID); err != nil {
					t.Fatal(err)
				}
			}
			gateway := &launchGateway{}
			handler := holonhttp.New(terminalRuntime(t, service, gateway))
			var retained holons.Holon
			for attempt := 0; attempt < 2; attempt++ {
				response := closeTabRequest(handler, h.ID, kind, "tab")
				if response.Code != 500 {
					t.Fatalf("cleanup failure: %d %s", response.Code, response.Body)
				}
				var err error
				retained, err = service.Get(t.Context(), h.ID)
				if err != nil || retained.ArchivedAt != nil {
					t.Fatalf("retained=%+v err=%v", retained, err)
				}
				if kind == "agent-sessions" && retained.AgentSessions[0].ClosedAt == nil || kind == "terminals" && retained.ManualTerminals[0].ClosedAt == nil {
					t.Fatal("closure was not durable")
				}
			}
			repository.failure = nil
			response := closeTabRequest(handler, h.ID, kind, "tab")
			after, err := service.Get(t.Context(), h.ID)
			if err != nil || response.Code != 200 || after.ArchivedAt == nil {
				t.Fatalf("cleanup retry=%d %s holon=%+v err=%v", response.Code, response.Body, after, err)
			}
			if !reflect.DeepEqual(retained.AgentSessions, after.AgentSessions) || !reflect.DeepEqual(retained.ManualTerminals, after.ManualTerminals) {
				t.Fatal("cleanup retry changed retained tabs")
			}
			if kind == "terminals" && len(gateway.closed) != 1 {
				t.Fatalf("cleanup repeated shutdown: %v", gateway.closed)
			}
		})
	}
}
