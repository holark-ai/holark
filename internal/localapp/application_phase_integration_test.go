package localapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/holons"
	holonshttp "github.com/holark-ai/holark/internal/holons/httpapi"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

// Hold application ports, without substituting any agent CLI or protocol.
type completionGate struct{ entered chan chan struct{} }

func (g completionGate) wait(ctx context.Context) error {
	release := make(chan struct{})
	select {
	case g.entered <- release:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (g completionGate) ImportArtifact(ctx context.Context, _ string) error { return g.wait(ctx) }
func (g completionGate) Complete(ctx context.Context, _ holons.Holon) (pullRequestWorkCompletionDisposition, error) {
	return pullRequestWorkCompletionDisposition{Terminal: true}, g.wait(ctx)
}

type retirementGate struct {
	localAgentRuntime
	completionGate
}

func (g retirementGate) Cancel(ctx context.Context, _, _ string) error { return g.wait(ctx) }

func assertAPIPhase(t *testing.T, handler http.Handler, id, phase string) {
	t.Helper()
	for _, method := range []string{"GET", "LIST", "PATCH"} {
		path, verb, body := "/api/v1/holons/"+id, method, ""
		if method == "LIST" {
			path, verb = "/api/v1/holons", "GET"
		}
		if method == "PATCH" {
			body = `{"title":"Still available"}`
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(verb, path, strings.NewReader(body)))
		if response.Code != 200 {
			t.Fatalf("%s response: %d %s", method, response.Code, response.Body)
		}
		var h holons.Holon
		if method == "LIST" {
			var list struct {
				Holons []holons.Holon `json:"holons"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
			for _, candidate := range list.Holons {
				if candidate.ID == id {
					h = candidate
				}
			}
		} else if err := json.Unmarshal(response.Body.Bytes(), &h); err != nil {
			t.Fatal(err)
		}
		if h.ID != id || h.ApplicationPhase != phase {
			t.Fatalf("%s phase=%q want=%q: %s", method, h.ApplicationPhase, phase, response.Body)
		}
	}
}

func TestFinalizingCoversObservationApplicationAndRetirement(t *testing.T) {
	for _, kind := range []holons.Kind{holons.KindPRMetadata, holons.KindPullReview, holons.KindRebase, holons.KindPullWorker} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			state, service, _, _ := specializedAgentState(t, kind, pullrequestwork.ModeAuto, holons.WorkspaceInspection{Clean: true, HeadCommit: "result"})
			if kind == holons.KindRebase {
				h, _ := service.Get(ctx, "specialized")
				if err := os.MkdirAll(filepath.Join(h.WorktreePath, ".holark"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(h.WorktreePath, ".holark/rebase_complete"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			apply := completionGate{entered: make(chan chan struct{}, 1)}
			retire := retirementGate{completionGate: completionGate{entered: make(chan chan struct{}, 1)}}
			state.metadata, state.workCompletion, state.agents = apply, apply, retire
			handler := holonshttp.New(&terminalHolonService{Service: service})
			done := make(chan error, 1)
			go func() {
				done <- state.Observed(ctx, "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete, Activity: protocol.ActivityCompleted})
			}()
			release := waitReservedWork(t, apply.entered)
			assertAPIPhase(t, handler, "specialized", "finalizing")
			h, err := service.Get(ctx, "specialized")
			if err != nil || h.AgentSession("agent").InputState != string(protocol.InputTaskComplete) || h.Status != holons.StatusRunning {
				t.Fatalf("observation=%+v %v", h, err)
			}
			close(release)
			release = waitReservedWork(t, retire.entered)
			assertAPIPhase(t, handler, "specialized", "finalizing")
			h, _ = service.Get(ctx, "specialized")
			if h.Status != holons.StatusExpired {
				t.Fatalf("settlement changed: %+v", h)
			}
			close(release)
			if err := waitReservedWork(t, done); err != nil {
				t.Fatal(err)
			}
			assertAPIPhase(t, handler, "specialized", "")
		})
	}
}

func TestOverlappingCompletionOwnersAndCancellation(t *testing.T) {
	for _, cancelHolon := range []bool{false, true} {
		t.Run(map[bool]string{false: "overlap", true: "cancel"}[cancelHolon], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			state, service, _, _ := specializedAgentState(t, holons.KindPRMetadata, pullrequestwork.ModeAuto, holons.WorkspaceInspection{})
			gate := completionGate{entered: make(chan chan struct{}, 2)}
			state.metadata, state.agents = gate, nil
			handler := holonshttp.New(&terminalHolonService{Service: service})
			first, second := make(chan error, 1), make(chan error, 1)
			go func() {
				first <- state.Observed(ctx, "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete})
			}()
			releaseFirst := waitReservedWork(t, gate.entered)
			go func() {
				second <- state.Observed(ctx, "specialized", "agent", harness.Event{InputState: protocol.InputTaskComplete})
			}()
			releaseSecond := waitReservedWork(t, gate.entered)
			close(releaseFirst)
			if err := waitReservedWork(t, first); err != nil {
				t.Fatal(err)
			}
			assertAPIPhase(t, handler, "specialized", "finalizing")
			if cancelHolon {
				if _, err := service.End(ctx, "specialized"); err != nil {
					t.Fatal(err)
				}
				assertAPIPhase(t, handler, "specialized", "")
			}
			close(releaseSecond)
			if err := waitReservedWork(t, second); err != nil {
				t.Fatal(err)
			}
			assertAPIPhase(t, handler, "specialized", "")
		})
	}
}
