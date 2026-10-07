package terminalenv_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/harness/codex"
	"github.com/holark-ai/holark/internal/harness/opencode"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminalenv"
)

func TestContextPreservesHarnessLaunchConfiguration(t *testing.T) {
	for _, harnessType := range []protocol.HarnessType{protocol.HarnessCodex, protocol.HarnessOpenCode} {
		t.Run(string(harnessType), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			if harnessType == protocol.HarnessCodex {
				if probe := codex.Probe(ctx); !probe.Available {
					t.Skip(probe.Reason)
				}
				t.Setenv("CODEX_HOME", t.TempDir())
			} else {
				t.Setenv(opencode.ConfigEnvironment, "")
				if err := os.Unsetenv(opencode.ConfigEnvironment); err != nil {
					t.Fatal(err)
				}
			}
			manager := codex.NewManager()
			t.Cleanup(func() {
				if err := manager.Close(); err != nil {
					t.Error(err)
				}
			})
			driver, _ := harness.RegistryWithCodex(manager).Driver(harnessType)
			repository, runtimeDir := t.TempDir(), t.TempDir()
			command, err := driver.Command(harness.StartSpec{
				RepositoryPath: repository, RuntimeDir: runtimeDir, SessionID: "holon-one",
				HarnessSession: protocol.HarnessSession{
					ID: "agent-one", SessionID: "holon-one", TerminalID: "terminal-one", HarnessType: harnessType,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{
				"HOLARK_HOLON_ID": "holon-one", "HOLARK_AGENT_SESSION_ID": "agent-one", "HOLARK_REPO_PATH": repository,
			}
			if harnessType == protocol.HarnessCodex {
				index := slices.Index(command.Args, "--remote-auth-token-env")
				if index < 0 || index+1 >= len(command.Args) {
					t.Fatal("prepared Codex command has no bridge authentication environment reference")
				}
				name := command.Args[index+1]
				for _, entry := range command.Env {
					if value, ok := strings.CutPrefix(entry, name+"="); ok {
						want[name] = value
					}
				}
				if want[name] == "" {
					t.Fatal("Codex manager did not assign a bridge token")
				}
			} else {
				want[opencode.EventsEnvironment] = filepath.Join(runtimeDir, opencode.SpoolFileName)
				want[opencode.ConfigEnvironment] = opencode.SharedConfigDirectory(runtimeDir)
			}
			// Apply the same final transformation as localAgentLauncher, including
			// stale CLI configuration that must never reach the launched process.
			command.Env = append(command.Env, "HOLARK_CLI_AUTH_TOKEN=stale-token", "HOLARK_SERVER_URL=http://stale")
			launchContext := terminalenv.Context{ExecutablePath: "/opt/holark/bin/holark", RuntimeDirectory: t.TempDir()}
			environment := launchContext.Environment(command.Env)
			for name, value := range want {
				if !slices.Contains(environment, name+"="+value) {
					t.Errorf("final launch environment lost harness-assigned %s", name)
				}
			}
			for _, entry := range environment {
				name, _, _ := strings.Cut(entry, "=")
				if name == "HOLARK_CLI_AUTH_TOKEN" || name == "HOLARK_SERVER_URL" {
					t.Errorf("final launch environment retained %s", name)
				}
			}
		})
	}
}
