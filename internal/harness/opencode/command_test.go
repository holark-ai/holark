package opencode

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/protocol"
)

func TestFreshCommandModelArgument(t *testing.T) {
	t.Setenv(ConfigEnvironment, "")
	if err := os.Unsetenv(ConfigEnvironment); err != nil {
		t.Fatal(err)
	}
	command, err := Command("opencode", openCodeCommandRuntime(t), t.TempDir(), "session-model", "agent-model", "prompt", "provider/custom-model")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--model", "provider/custom-model", "--prompt=prompt"}
	if !reflect.DeepEqual(command.Args[len(command.Args)-3:], want) {
		t.Fatalf("args = %q", command.Args)
	}
}

func TestSharedConfigDirectoryDiffersByVersion(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	v1 := SharedConfigDirectory(runtimeDir)
	legacy := SharedConfigDirectory(runtimeDir, "opencode v1.18.25")
	v2 := SharedConfigDirectory(runtimeDir, "opencode v2.0.8")
	if v1 != legacy {
		t.Fatalf("legacy version changed the shared directory: %q vs %q", v1, legacy)
	}
	if v1 == v2 {
		t.Fatalf("v1 and v2 share a configuration directory: %q", v1)
	}
}

func TestForkCommandBuildsNativeForkWithDestinationRuntime(t *testing.T) {
	t.Setenv(ConfigEnvironment, "")
	if err := os.Unsetenv(ConfigEnvironment); err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(t.TempDir(), "worktree")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeDir := openCodeCommandRuntime(t)
	command, err := ForkCommand(
		"/opt/bin/opencode", runtimeDir, repository, "session-one", "destination-harness",
		"source-session", "discuss the commit",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/opt/bin/opencode", repository, "--mini", "--session", "source-session",
		"--fork", "--prompt=discuss the commit",
	}
	if !reflect.DeepEqual(command.Args, want) || command.Dir != repository {
		t.Fatalf("fork command = %#v in %q, want %#v in %q", command.Args, command.Dir, want, repository)
	}
	for name, want := range map[string]string{
		ConfigEnvironment:         SharedConfigDirectory(runtimeDir),
		EventsEnvironment:         filepath.Join(runtimeDir, SpoolFileName),
		"HOLARK_HOLON_ID":         "session-one",
		"HOLARK_AGENT_SESSION_ID": "destination-harness",
	} {
		if got := openCodeCommandEnvironment(command.Env, name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	for _, path := range []string{
		filepath.Join(SharedConfigDirectory(runtimeDir), PluginsDirectoryName, PluginFileName),
		filepath.Join(runtimeDir, SpoolFileName),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("destination runtime is missing %s: %v", path, err)
		}
	}

	promptless := openCodeCommandRuntime(t)
	command, err = ForkCommand(
		"/opt/bin/opencode", promptless, repository, "session-one", "promptless-destination",
		"source-session", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{
		"/opt/bin/opencode", repository, "--session", "source-session", "--fork",
	}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("promptless fork command = %#v, want %#v", command.Args, want)
	}
}

func TestForkCommandRejectsInvalidSourceAndPrompt(t *testing.T) {
	t.Setenv(ConfigEnvironment, "")
	if err := os.Unsetenv(ConfigEnvironment); err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(t.TempDir(), "worktree")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		source string
		prompt string
	}{
		{name: "missing source", prompt: "discuss the commit"},
		{name: "invalid source", source: "source/session", prompt: "discuss the commit"},
		{name: "oversized prompt", source: "source-session", prompt: strings.Repeat("x", protocol.MaxPromptBytes+1)},
		{name: "invalid prompt", source: "source-session", prompt: string([]byte{0xff})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ForkCommand(
				"opencode", openCodeCommandRuntime(t), repository, "session-one", "destination-harness",
				test.source, test.prompt,
			); err == nil {
				t.Fatal("expected invalid fork command to fail")
			}
		})
	}
}

func TestV2ForkCommandUsesMiniWithAndWithoutPrompt(t *testing.T) {
	t.Setenv(ConfigEnvironment, "")
	if err := os.Unsetenv(ConfigEnvironment); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PWD", t.TempDir())
	repository := t.TempDir()
	for _, prompt := range []string{"", "discuss the commit"} {
		t.Run("prompt="+prompt, func(t *testing.T) {
			runtimeDir := openCodeCommandRuntime(t)
			command, err := ForkCommandWithVersion(
				"/opt/bin/launcher", "opencode v2.0.8", runtimeDir, repository,
				"session-one", "destination-harness", "source-session", prompt, "opencode",
			)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"/opt/bin/launcher", "opencode", "mini", "--standalone", "--session", "source-session", "--fork"}
			if prompt != "" {
				want = append(want, "--prompt="+prompt)
			}
			if !reflect.DeepEqual(command.Args, want) || command.Dir != repository {
				t.Fatalf("fork command = %#v in %q, want %#v in %q", command.Args, command.Dir, want, repository)
			}
			for name, want := range map[string]string{
				"PWD":                      repository,
				"HOLARK_OPENCODE_OBSERVER": "server",
				ConfigEnvironment:          "",
				EventsEnvironment:          filepath.Join(runtimeDir, SpoolFileName),
			} {
				if got := openCodeCommandEnvironment(command.Env, name); got != want {
					t.Fatalf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

func TestConversationsReuseConfigAndKeepSeparateEvents(t *testing.T) {
	t.Setenv(ConfigEnvironment, "")
	if err := os.Unsetenv(ConfigEnvironment); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"", "unknown", "opencode v1.18.25", "opencode v2.0.8"} {
		t.Run("version="+version, func(t *testing.T) {
			root, repository := t.TempDir(), t.TempDir()
			t.Setenv("OPENCODE_CONFIG_CONTENT", `{// inherited JSONC
				"model":"provider/model", "plugins":["./user-plugin",],
			}`)
			if IsV2Version(version) {
				t.Setenv(ConfigEnvironment, t.TempDir())
			}
			var config string
			for _, operation := range []string{"start", "start-model", "fork", "resume"} {
				runtimeDir := filepath.Join(root, operation)
				if err := os.Mkdir(runtimeDir, 0700); err != nil {
					t.Fatal(err)
				}
				var cmd *exec.Cmd
				var err error
				switch operation {
				case "start":
					cmd, err = CommandWithVersion("opencode", version, runtimeDir, repository, "holon", operation, "hello")
				case "start-model":
					cmd, err = CommandWithVersion("opencode", version, runtimeDir, repository, "holon", operation, "hello", "provider/model")
				case "fork":
					cmd, err = ForkCommandWithVersion("opencode", version, runtimeDir, repository, "holon", operation, "source", "continue")
				case "resume":
					cmd, err = ResumeCommandWithVersion("opencode", version, runtimeDir, repository, "holon", operation, "source", "")
				}
				if err != nil {
					t.Fatal(err)
				}
				standalone := 0
				for _, arg := range cmd.Args {
					if arg == "--standalone" {
						standalone++
					}
				}
				wantStandalone := 0
				if version == "opencode v2.0.8" {
					wantStandalone = 1
				}
				if standalone != wantStandalone {
					t.Fatalf("%s args = %q, want %d --standalone flags", operation, cmd.Args, wantStandalone)
				}
				env := cmd.Env
				got := openCodeCommandEnvironment(env, ConfigEnvironment)
				if IsV2Version(version) {
					if got != os.Getenv(ConfigEnvironment) {
						t.Fatalf("%s replaced the user's global config root: %s", operation, got)
					}
					var content struct {
						Model   string   `json:"model"`
						Plugins []string `json:"plugins"`
					}
					if err := json.Unmarshal([]byte(openCodeCommandEnvironment(env, "OPENCODE_CONFIG_CONTENT")), &content); err != nil {
						t.Fatal(err)
					}
					got = SharedConfigDirectory(runtimeDir, version)
					if content.Model != "provider/model" || !reflect.DeepEqual(content.Plugins, []string{"./user-plugin", got}) {
						t.Fatalf("%s lost inherited config or observer: %+v", operation, content)
					}
				}
				if config == "" {
					config = got
					// Stand in for already installed dependencies; later conversations must
					// leave the directory intact so OpenCode can reuse them.
					if err := os.Mkdir(filepath.Join(config, "node_modules"), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(config, "node_modules", "installed"), []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
				} else if got != config {
					t.Fatalf("%s used a new configuration directory: %s", operation, got)
				}
				if data, err := os.ReadFile(filepath.Join(config, "node_modules", "installed")); err != nil || string(data) != "keep" {
					t.Fatalf("%s lost installed dependencies: %q, %v", operation, data, err)
				}
				spool := openCodeCommandEnvironment(env, EventsEnvironment)
				if spool != filepath.Join(runtimeDir, SpoolFileName) {
					t.Fatalf("%s spool = %s", operation, spool)
				}
				if data, err := os.ReadFile(spool); err != nil || len(data) != 0 {
					t.Fatalf("%s inherited events: %q, %v", operation, data, err)
				}
				if err := os.WriteFile(spool, []byte(operation), 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func openCodeCommandRuntime(t *testing.T) string {
	t.Helper()
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return runtimeDir
}

func openCodeCommandEnvironment(environment []string, name string) string {
	prefix := name + "="
	for _, value := range environment {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return ""
}
