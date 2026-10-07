package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/protocol"
)

func TestCommandBuildsNativeInteractiveLaunch(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "sonnet")
	repository := filepath.Join(t.TempDir(), "worktree")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeDir := privateRuntimeDir(t)
	t.Setenv("HOLARK_STALE", "remove-me")
	command, err := Command("/opt/bin/claude", "/opt/bin/holark-node", runtimeDir, repository, "session-one", "harness-one", "fix the tests")
	if err != nil {
		t.Fatal(err)
	}
	prepared := command.Args[2]
	if !validUUID(prepared) {
		t.Fatalf("prepared resume target = %q", prepared)
	}
	want := []string{"/opt/bin/claude", "--session-id", prepared, "--settings", filepath.Join(runtimeDir, SettingsFileName), "--", "fix the tests"}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("arguments = %#v, want %#v", command.Args, want)
	}
	if command.Dir != repository {
		t.Fatalf("working directory = %q", command.Dir)
	}
	if hasEnvironment(command.Env, "HOLARK_STALE") {
		t.Fatalf("inherited Holark environment was not sanitized: %#v", command.Env)
	}
	for name, want := range map[string]string{
		"ANTHROPIC_MODEL":         "sonnet",
		"HOLARK_HOLON_ID":         "session-one",
		"HOLARK_AGENT_SESSION_ID": "harness-one",
		"HOLARK_REPO_PATH":        repository,
	} {
		if got := environmentValue(command.Env, name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	joined := strings.Join(command.Args, " ")
	for _, forbidden := range []string{"--bare", "--safe-mode", "--add-dir", "bypassPermissions", "--dangerously-skip-permissions"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("launch contains forbidden permission/configuration flag %q: %s", forbidden, joined)
		}
	}
	assertPrivateRuntime(t, runtimeDir)
}

func TestFreshCommandModelArgument(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "sonnet")
	command, err := Command("claude", "/opt/bin/holark", privateRuntimeDir(t), t.TempDir(), "session-model", "agent-model", "prompt", "claude-exact-version")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--model", "claude-exact-version", "--", "prompt"}
	if !reflect.DeepEqual(command.Args[len(command.Args)-4:], want) {
		t.Fatalf("args = %q", command.Args)
	}
}

func TestCommandTreatsFlagLikePromptAsData(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "worktree")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"--version", "--dangerously-skip-permissions", "-unknown"} {
		t.Run(prompt, func(t *testing.T) {
			command, err := Command("claude", "/opt/holark-node", privateRuntimeDir(t), repository, "session-one", "harness-one", prompt)
			if err != nil {
				t.Fatal(err)
			}
			if got := command.Args[len(command.Args)-2:]; !reflect.DeepEqual(got, []string{"--", prompt}) {
				t.Fatalf("prompt arguments = %#v, want end-of-options marker followed by %q", got, prompt)
			}
		})
	}
}

func TestCommandOmitsEmptyPromptAndResumeUsesStoredUUID(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "sonnet")
	t.Setenv("ANTHROPIC_DEFAULT_OPUS_MODEL", "custom-opus")
	repository := filepath.Join(t.TempDir(), "worktree")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	startRuntime := privateRuntimeDir(t)
	start, err := Command("claude", "/opt/holark-node", startRuntime, repository, "session-one", "harness-one", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(start.Args) != 5 || start.Args[1] != "--session-id" || start.Args[3] != "--settings" {
		t.Fatalf("empty-prompt arguments = %#v", start.Args)
	}

	resumeRuntime := privateRuntimeDir(t)
	resumeID := "123e4567-e89b-12d3-a456-426614174000"
	resumed, err := ResumeCommand("claude", "/opt/holark-node", resumeRuntime, repository, "session-one", "harness-one", resumeID, "continue the work")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "--resume", resumeID, "--settings", filepath.Join(resumeRuntime, SettingsFileName), "--", "continue the work"}
	if !reflect.DeepEqual(resumed.Args, want) {
		t.Fatalf("resume arguments = %#v, want %#v", resumed.Args, want)
	}
	if resumed.Dir != repository {
		t.Fatalf("resume directory = %q", resumed.Dir)
	}
	if hasEnvironment(resumed.Env, "ANTHROPIC_MODEL") {
		t.Fatal("resume inherited a model override")
	}
	if got := environmentValue(resumed.Env, "ANTHROPIC_DEFAULT_OPUS_MODEL"); got != "custom-opus" {
		t.Fatalf("resume model alias mapping = %q", got)
	}
	if _, err := ResumeCommand("claude", "/opt/holark-node", privateRuntimeDir(t), repository, "session-one", "harness-one", "not-a-uuid", ""); err == nil {
		t.Fatal("invalid resume UUID was accepted")
	}
}

func TestForkCommandBuildsNativeForkWithDestinationRuntime(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "sonnet")
	t.Setenv("ANTHROPIC_DEFAULT_OPUS_MODEL", "custom-opus")
	repository := filepath.Join(t.TempDir(), "worktree")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeDir := privateRuntimeDir(t)
	sourceID := "123e4567-e89b-12d3-a456-426614174000"
	command, err := ForkCommand(
		"/opt/bin/claude", "/opt/bin/holark-node", runtimeDir, repository,
		"session-one", "destination-harness", sourceID, "discuss the commit",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/opt/bin/claude", "--resume", sourceID, "--fork-session",
		"--settings", filepath.Join(runtimeDir, SettingsFileName), "--", "discuss the commit",
	}
	if !reflect.DeepEqual(command.Args, want) || command.Dir != repository {
		t.Fatalf("fork command = %#v in %q, want %#v in %q", command.Args, command.Dir, want, repository)
	}
	if got := environmentValue(command.Env, "HOLARK_AGENT_SESSION_ID"); got != "destination-harness" {
		t.Fatalf("destination harness ID = %q", got)
	}
	if hasEnvironment(command.Env, "ANTHROPIC_MODEL") {
		t.Fatal("fork inherited a model override")
	}
	if got := environmentValue(command.Env, "ANTHROPIC_DEFAULT_OPUS_MODEL"); got != "custom-opus" {
		t.Fatalf("fork model alias mapping = %q", got)
	}
	assertPrivateRuntime(t, runtimeDir)

	promptlessRuntime := privateRuntimeDir(t)
	promptless, err := ForkCommand(
		"/opt/bin/claude", "/opt/bin/holark-node", promptlessRuntime, repository,
		"session-one", "promptless-destination", sourceID, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{
		"/opt/bin/claude", "--resume", sourceID, "--fork-session",
		"--settings", filepath.Join(promptlessRuntime, SettingsFileName),
	}
	if !reflect.DeepEqual(promptless.Args, want) {
		t.Fatalf("promptless fork command = %#v, want %#v", promptless.Args, want)
	}
}

func TestForkCommandRejectsInvalidSourceAndPrompt(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "worktree")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	validSource := "123e4567-e89b-12d3-a456-426614174000"
	tests := []struct {
		name   string
		source string
		prompt string
	}{
		{name: "missing source", prompt: "discuss the commit"},
		{name: "invalid source", source: "not-a-uuid", prompt: "discuss the commit"},
		{name: "oversized prompt", source: validSource, prompt: strings.Repeat("x", protocol.MaxPromptBytes+1)},
		{name: "invalid prompt", source: validSource, prompt: string([]byte{0xff})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ForkCommand(
				"claude", "/opt/holark-node", privateRuntimeDir(t), repository,
				"session-one", "destination-harness", test.source, test.prompt,
			); err == nil {
				t.Fatal("expected invalid fork command to fail")
			}
		})
	}
}

func TestPrepareRuntimeCreatesDocumentedContentFreeHooks(t *testing.T) {
	runtimeDir := privateRuntimeDir(t)
	settingsPath, err := PrepareRuntime(runtimeDir, "/opt/Holark Node/bin/holark-node", "harness-one")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings hookSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	for _, event := range hookEvents {
		matchers := settings.Hooks[event]
		if len(matchers) != 1 || len(matchers[0].Hooks) != 1 {
			t.Fatalf("%s hooks = %#v", event, matchers)
		}
		hook := matchers[0].Hooks[0]
		if hook.Type != "command" || hook.Timeout != 5 || !strings.Contains(hook.Command, "'/opt/Holark Node/bin/holark-node' claude-hook") {
			t.Fatalf("%s hook = %#v", event, hook)
		}
	}
	assertPrivateRuntime(t, runtimeDir)
	metadata, err := readRuntimeMetadata(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.LaunchToken) != 64 || metadata.HarnessSessionID != "harness-one" {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func privateRuntimeDir(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func assertPrivateRuntime(t *testing.T, runtimeDir string) {
	t.Helper()
	info, err := os.Stat(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("runtime permissions = %o", info.Mode().Perm())
	}
	for _, name := range []string{SettingsFileName, MetadataFileName} {
		info, err := os.Stat(filepath.Join(runtimeDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s permissions = %o", name, info.Mode().Perm())
		}
	}
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, value := range environment {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return ""
}

func hasEnvironment(environment []string, name string) bool {
	prefix := name + "="
	for _, value := range environment {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func TestShellQuoteHandlesApostrophes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Claude hook commands use the platform shell supported by Claude Code")
	}
	if got := shellQuote("/tmp/it's/here"); got != "'/tmp/it'\"'\"'s/here'" {
		t.Fatalf("quoted path = %q", got)
	}
}
