package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminalenv"
	"github.com/holark-ai/holark/internal/terminalhost"
)

func TestAppServerToolDiscoveryContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if probe := Probe(ctx); !probe.Available {
		t.Skip(probe.Reason)
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("HOLARK_RUNTIME_DIR", "/stale-runtime")
	t.Setenv("HOLARK_SERVER_URL", "http://stale")
	t.Setenv("HOLARK_CLI_AUTH_TOKEN", "stale-token")
	t.Setenv("HOLARK_CODEX_BRIDGE_TOKEN", "stale-bridge-token")
	discovery := terminalenv.Context{
		ExecutablePath: filepath.Join(t.TempDir(), "bin", "holark"), RuntimeDirectory: t.TempDir(),
	}
	manager := NewManagerWithContext(discovery)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	command := exec.Command("codex")
	command.Dir, command.Env = t.TempDir(), os.Environ()
	session := protocol.HarnessSession{ID: "agent-one", SessionID: "holon-one", TerminalID: "terminal-one"}
	if err := manager.Prepare(ctx, command, session); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Dir(discovery.ExecutablePath) + string(filepath.ListSeparator)
	for _, entry := range []string{
		"HOLARK_RUNTIME_DIR=" + discovery.RuntimeDirectory,
		"PATH=" + prefix + os.Getenv("PATH"),
	} {
		if !slices.Contains(manager.process.Env, entry) {
			t.Errorf("app-server environment missing %q", entry)
		}
	}
	for _, entry := range manager.process.Env {
		name, _, _ := strings.Cut(entry, "=")
		if name == "HOLARK_SERVER_URL" || name == "HOLARK_CLI_AUTH_TOKEN" || name == "HOLARK_CODEX_BRIDGE_TOKEN" {
			t.Errorf("app-server inherited %s", name)
		}
	}
	observer, err := connectRPC(ctx, manager.url, manager.token)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	if err := observer.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	// Prepare and configure the real app-server binding without ever launching
	// the remote TUI or applying localAgentLauncher's terminal transformation.
	binding := manager.launches[session.TerminalID]
	for _, configuredPath := range []bool{false, true} {
		name := "inherited PATH"
		if configuredPath {
			name = "configured PATH"
		}
		t.Run(name, func(t *testing.T) {
			shell := map[string]any{"CUSTOM": "shell-value", "HOLARK_RUNTIME_DIR": "/stale-shell"}
			mcp := map[string]any{"CUSTOM": "mcp-value", "HOLARK_RUNTIME_DIR": "/stale-mcp"}
			shellPath, mcpPath := os.Getenv("PATH"), os.Getenv("PATH")
			if configuredPath {
				shellPath, mcpPath = "/shell/bin", "/mcp/bin"
				shell["PATH"], mcp["PATH"] = shellPath, mcpPath
			}
			httpServer := map[string]any{"url": "https://example.invalid/mcp"}
			params := map[string]any{"config": map[string]any{
				"shell_environment_policy": map[string]any{"inherit": "none", "set": shell},
				"mcp_servers": map[string]any{
					"stdio": map[string]any{"command": "/bin/sh", "env": mcp},
					"http":  httpServer,
				},
			}}
			if err := binding.configure(ctx, observer, params, false); err != nil {
				t.Fatal(err)
			}
			config := params["config"].(map[string]any)
			shell = config["shell_environment_policy"].(map[string]any)["set"].(map[string]any)
			servers := config["mcp_servers"].(map[string]any)
			mcp = servers["stdio"].(map[string]any)["env"].(map[string]any)
			for name, env := range map[string]map[string]any{"shell": shell, "mcp": mcp} {
				path := shellPath
				if name == "mcp" {
					path = mcpPath
				}
				for key, want := range map[string]string{
					"PATH": prefix + path, "HOLARK_RUNTIME_DIR": discovery.RuntimeDirectory,
					"HOLARK_HOLON_ID": session.SessionID, "HOLARK_AGENT_SESSION_ID": session.ID,
					"HOLARK_REPO_PATH": command.Dir, "CUSTOM": name + "-value",
					terminalhost.ProcessOwnerEnvironment: manager.processOwner,
				} {
					if env[key] != want {
						t.Errorf("%s %s = %v, want %q", name, key, env[key], want)
					}
				}
				for _, key := range []string{"HOLARK_SERVER_URL", "HOLARK_CLI_AUTH_TOKEN", "HOLARK_CODEX_BRIDGE_TOKEN", "CODEX_HOME"} {
					if _, ok := env[key]; ok {
						t.Errorf("%s overrides include %s", name, key)
					}
				}
			}
			if _, ok := servers["http"].(map[string]any)["env"]; ok {
				t.Error("HTTP MCP server received process environment settings")
			}
		})
	}
}
