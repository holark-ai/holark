package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/holark-ai/holark/internal/harness/claudecode"
	"github.com/holark-ai/holark/internal/harness/cliprobe"
	"github.com/holark-ai/holark/internal/harness/codex"
	"github.com/holark-ai/holark/internal/harness/opencode"
)

// Only discovery inputs are synthetic. The application owns classification,
// capability responses, settings persistence, and all execution paths.
type harnessScenario struct {
	mu     sync.RWMutex
	inputs map[string]harnessDiscoveryInput
}

func newHarnessScenario() *harnessScenario {
	return &harnessScenario{inputs: map[string]harnessDiscoveryInput{
		"codex":    {Mode: "output", Output: "codex-cli " + codex.LatestSupportedVersion},
		"claude":   {Mode: "output", Output: claudecode.LatestSupportedVersion + " (Claude Code)"},
		"opencode": {Mode: "output", Output: opencode.LatestSupportedVersion},
	}}
}

type harnessDiscoveryInput struct {
	Mode   string `json:"mode"`
	Output string `json:"output"`
}

func (s *harnessScenario) Discover(ctx context.Context, executable string) cliprobe.Observation {
	s.mu.RLock()
	input, ok := s.inputs[filepath.Base(executable)]
	s.mu.RUnlock()
	if !ok || input.Mode == "installed" {
		return (cliprobe.Installed{}).Discover(ctx, executable)
	}
	if input.Mode == "missing" {
		return cliprobe.Observation{ResolutionError: exec.ErrNotFound}
	}
	path, err := exec.LookPath(executable)
	if err != nil {
		path = filepath.Join(string(filepath.Separator), "__holark_discovery_scenario__", executable)
	}
	path, _ = filepath.Abs(path)
	observation := cliprobe.Observation{Path: path, Output: input.Output}
	if input.Mode == "failed" {
		observation.VersionError = errors.New("scenario version command failed")
	}
	return observation
}

func (s *harnessScenario) inputsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodPut {
		var input struct {
			Executable string `json:"executable"`
			Mode       string `json:"mode"`
			Output     string `json:"output"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			http.Error(w, "Invalid discovery input", http.StatusBadRequest)
			return
		}
		if input.Executable != "codex" && input.Executable != "claude" && input.Executable != "opencode" {
			http.Error(w, "Unknown executable", http.StatusBadRequest)
			return
		}
		if input.Mode != "installed" && input.Mode != "missing" && input.Mode != "failed" && input.Mode != "output" {
			http.Error(w, "Unknown discovery mode", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.inputs[input.Executable] = harnessDiscoveryInput{Mode: input.Mode, Output: input.Output}
		s.mu.Unlock()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_ = json.NewEncoder(w).Encode(s.inputs)
}

//go:embed harness_scenario.html
var harnessScenarioHTML string

func harnessPlaygroundHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, harnessScenarioHTML)
}
