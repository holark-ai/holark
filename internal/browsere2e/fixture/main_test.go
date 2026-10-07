package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
)

func TestFixtureRestartClearsDurableTerminalBinding(t *testing.T) {
	staticDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("<main>Holark</main>"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture, err := newFixtureApplication(staticDir)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.close()
	before := fixture.app
	holon, err := before.Holons.Get(context.Background(), fixture.state.HolonID)
	if err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/holons/"+holon.ID+"/terminals", bytes.NewBufferString(`{"title":"durable"}`))
	request.Header.Set("Content-Type", "application/json")
	before.Handler.ServeHTTP(create, request)
	if create.Code != http.StatusCreated {
		t.Fatalf("create terminal status=%d body=%q", create.Code, create.Body.String())
	}
	var record struct {
		ID         string `json:"id"`
		TerminalID string `json:"terminal_id"`
	}
	if err = json.Unmarshal(create.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.TerminalID == "" {
		t.Fatal("local terminal did not receive a PTY binding")
	}
	if _, ok := before.Manager.Progress(terminals.TerminalID(record.TerminalID)); !ok {
		t.Fatal("local PTY is not hosted by application manager")
	}
	response := httptest.NewRecorder()
	fixture.restartHandler(response, httptest.NewRequest(http.MethodPost, "/__e2e/restart-holark", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("restart status=%d body=%q", response.Code, response.Body.String())
	}
	if fixture.app == before {
		t.Fatal("restart retained old application")
	}
	if _, err := before.Manager.Attach(terminals.TerminalID(record.TerminalID)); !errors.Is(err, terminalhost.ErrClosed) {
		t.Fatalf("attach to closed application error = %v, want terminal host closed", err)
	}
	restarted, err := fixture.app.Holons.Get(context.Background(), holon.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.ManualTerminals) != 1 || restarted.ManualTerminals[0].ID != record.ID || restarted.ManualTerminals[0].TerminalID != "" {
		t.Fatalf("restarted terminals = %+v", restarted.ManualTerminals)
	}
	relaunch := httptest.NewRecorder()
	fixture.app.Handler.ServeHTTP(relaunch, httptest.NewRequest(http.MethodPost, "/api/v1/holons/"+holon.ID+"/terminals/"+record.ID+"/relaunch", nil))
	if relaunch.Code != http.StatusOK {
		t.Fatalf("relaunch status=%d body=%q", relaunch.Code, relaunch.Body.String())
	}
	var replacement struct {
		TerminalID string `json:"terminal_id"`
	}
	if err = json.Unmarshal(relaunch.Body.Bytes(), &replacement); err != nil {
		t.Fatal(err)
	}
	if replacement.TerminalID == "" || replacement.TerminalID == record.TerminalID {
		t.Fatalf("replacement terminal ID = %q, old = %q", replacement.TerminalID, record.TerminalID)
	}
}

func TestValidatePullRequestFlow(t *testing.T) {
	tests := []struct {
		name     string
		scenario fixtureScenario
		flow     pullRequestFlow
		wantErr  bool
	}{
		{name: "happy path", scenario: fixtureScenarioPullRequestCreation, flow: pullRequestFlowHappyPath},
		{name: "save failure", scenario: fixtureScenarioPullRequestCreation, flow: pullRequestFlowDescriptionSaveFailure},
		{name: "missing flow", scenario: fixtureScenarioPullRequestCreation, wantErr: true},
		{name: "unknown flow", scenario: fixtureScenarioPullRequestCreation, flow: "unknown", wantErr: true},
		{name: "flow without scenario", flow: pullRequestFlowHappyPath, wantErr: true},
		{name: "default fixture"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePullRequestFlow(test.scenario, test.flow)
			if (err != nil) != test.wantErr {
				t.Fatalf("validatePullRequestFlow(%q, %q) error = %v, want error %v", test.scenario, test.flow, err, test.wantErr)
			}
		})
	}
}
