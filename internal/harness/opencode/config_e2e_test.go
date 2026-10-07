//go:build opencode_e2e

package opencode

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
)

// A real v2 launch must load the user's global model and relative server/UI
// plugins alongside the observer, without rewriting the user's configuration.
// Exercise real route changes and resume hydration without model requests.
func TestOpenCodeE2EGlobalConfiguration(t *testing.T) {
	if os.Getenv("HOLARK_OPENCODE_E2E") != "1" {
		t.Skip("set HOLARK_OPENCODE_E2E=1 to run the real OpenCode smoke test")
	}
	probe := Probe(t.Context())
	if !probe.Available || !IsV2Version(probe.Version) {
		t.Skip("OpenCode v2 is required")
	}
	t.Setenv(ConfigEnvironment, "")
	if err := os.Unsetenv(ConfigEnvironment); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_CLI_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_TEST_HOME", t.TempDir())
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, t.TempDir())
	}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		logs, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_DATA_HOME"), "opencode", "log", "*.log"))
		for _, log := range logs {
			data, _ := os.ReadFile(log)
			for _, line := range strings.Split(string(data), "\n") {
				if strings.Contains(line, "plugin") || strings.Contains(line, "error") {
					t.Log(line)
				}
			}
		}
	})
	config := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode")
	files := map[string]string{
		"opencode.jsonc": `{
			// Retain JSONC and resolve this plugin beside its original config.
			"model": "holark-config/holark-config-model",
			"providers": {"holark-config": {
				"package": "@opencode/ai/providers/openai/chat",
				"options": {"apiKey": "unused-no-model-request"},
				"models": {"holark-config-model": {"name": "holark-config-model"}}
			}},
			"plugins": ["./user-server",],
		}`,
		"cli.json": `{"plugins":["./user-cli"],"leader":{"timeout":321}}`,
		"user-server/server.js": `import { writeFileSync } from "node:fs";
			export default { id: "holark.test-global", setup() {
				writeFileSync(new URL("../server-loaded", import.meta.url), "loaded");
			}};`,
		"user-cli/tui.js": `import { writeFileSync, readFileSync, existsSync } from "node:fs";
			export default { id: "holark.test-cli", setup(ctx) {
				const marker = new URL("../cli-loaded", import.meta.url);
				if (existsSync(marker)) return; // Let the next launch exercise --session.
				let stopped = false;
				void (async () => {
					while (!existsSync(new URL("../navigate", import.meta.url))) {
						if (stopped) return;
						await new Promise(resolve => setTimeout(resolve, 50));
					}
					const location = ctx.location ?? ctx.data.location.default();
					const first = await ctx.client.session.create({location});
					const second = await ctx.client.session.create({location});
					for (const session of [first, second, first]) {
						ctx.ui.router.navigate({type:"session", sessionID:session.id});
						const deadline = Date.now() + 15000;
						while (!stopped) {
							const facts = readFileSync(process.env.HOLARK_OPENCODE_EVENTS_PATH, "utf8")
								.trim().split("\n").filter(Boolean).map(JSON.parse);
							const selected = facts.filter(f => f.type === "session_selected");
							if (selected.at(-1)?.session_id === session.id) break;
							if (Date.now() > deadline) throw new Error("Session selection timed out");
							await new Promise(resolve => setTimeout(resolve, 50));
						}
						if (stopped) return;
						ctx.ui.router.navigate({type:"home"});
						await new Promise(resolve => setTimeout(resolve, 150));
					}
					writeFileSync(marker, JSON.stringify([first.id, second.id, first.id]));
				})().catch(error => writeFileSync(new URL("../cli-error", import.meta.url), String(error)));
				return () => { stopped = true; };
			}};`,
	}
	for name, content := range files {
		path := filepath.Join(config, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runtimeDir := openCodeE2ERuntime(t)
	command, err := CommandWithVersion("opencode", probe.Version, runtimeDir, t.TempDir(), "config-test", "config-agent", "")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := terminalhost.NewManager(terminalhost.Options{MaximumTerminals: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	terminalID := terminals.TerminalID("terminal-opencode-config-e2e")
	launchOpenCodeE2ETerminal(t, manager, terminalID, command.Path, command.Args[1:], command.Env, command.Dir)
	waitOpenCodeE2EScreenText(t, manager, terminalID, "holark-config-model", false, 45*time.Second)
	if err := os.WriteFile(filepath.Join(config, "navigate"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, serverErr := os.Stat(filepath.Join(config, "server-loaded"))
		_, cliErr := os.Stat(filepath.Join(config, "cli-loaded"))
		if failure, err := os.ReadFile(filepath.Join(config, "cli-error")); err == nil {
			t.Fatalf("session navigation: %s", failure)
		}
		spool, err := os.ReadFile(filepath.Join(runtimeDir, SpoolFileName))
		if serverErr == nil && cliErr == nil && err == nil && bytes.Contains(spool, []byte(`"observer_initialized"`)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("global plugins or observer did not load: server=%v cli=%v spool=%s err=%v", serverErr, cliErr, spool, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	closeOpenCodeE2ETerminal(t, manager, terminalID)
	var selected []string
	hydrated := map[string]bool{}
	reducer := NewReducer("", protocol.InputNone)
	for _, fact := range readOpenCodeE2EFacts(t, runtimeDir) {
		if fact.Type == "observer_failed" {
			t.Fatal("terminal observer failed")
		}
		projection := reducer.Apply(fact)
		if fact.Type == "session_status" && fact.Status == "idle" {
			hydrated[fact.SessionID] = true
		}
		if fact.Type == "session_selected" {
			if !hydrated[fact.SessionID] {
				t.Fatalf("session selected before hydration: %s", fact.SessionID)
			}
			selected = append(selected, fact.SessionID)
			if !projection.Metadata || projection.ResumeTarget != fact.SessionID || projection.Activity != protocol.ActivityIdle || projection.InputState != protocol.InputNone {
				t.Fatalf("fresh session projection = %+v", projection)
			}
		}
	}
	var wantSelected []string
	marker, err := os.ReadFile(filepath.Join(config, "cli-loaded"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(marker, &wantSelected); err != nil {
		t.Fatal(err)
	}
	// Location changes can reload the user plugin during startup. Check the
	// completed navigation sequence after those initial routes settle.
	if len(selected) < len(wantSelected) || !slices.Equal(selected[len(selected)-len(wantSelected):], wantSelected) {
		t.Fatalf("root navigation selections = %v, want suffix %v", selected, wantSelected)
	}
	selected = wantSelected
	resumeRuntime := openCodeE2ERuntime(t)
	resume, err := ResumeCommandWithVersion("opencode", probe.Version, resumeRuntime, command.Dir, "config-test", "config-agent", selected[0], "")
	if err != nil {
		t.Fatal(err)
	}
	events, stopMonitor, monitorDone := startOpenCodeE2EMonitor(resumeRuntime, selected[0], protocol.InputNone)
	defer stopMonitor()
	resumeTerminalID := terminals.TerminalID("terminal-opencode-config-resume-e2e")
	launchOpenCodeE2ETerminal(t, manager, resumeTerminalID, resume.Path, resume.Args[1:], resume.Env, resume.Dir)
	metadata := waitOpenCodeE2EEvent(t, events, func(event MonitorEvent) bool { return event.Type == MonitorMetadata })
	if metadata.ResumeTarget != selected[0] {
		t.Fatalf("resumed target = %q, want %q", metadata.ResumeTarget, selected[0])
	}
	closeOpenCodeE2ETerminal(t, manager, resumeTerminalID)
	stopMonitor()
	waitOpenCodeE2EDone(t, monitorDone)
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(config, name))
		if err != nil || string(got) != want {
			t.Fatalf("user file %s changed: %v", name, err)
		}
	}
}
