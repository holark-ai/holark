package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/protocol"
)

func TestCommand(t *testing.T) {
	t.Setenv("HOLARK_SERVER_URL", "http://127.0.0.1:4242")
	codexHome := useTemporaryCodexHome(t)
	repository := filepath.Join(string(filepath.Separator), "work", "repo")
	command, err := Command("/usr/local/bin/codex", repository, "session-1", "Fix the bug")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/local/bin/codex", "--cd", repository, "--profile", "holark-holon-session-1", "--sandbox", "workspace-write", "--ask-for-approval", "on-request", "--", "Fix the bug"}
	if !reflect.DeepEqual(command.Args, want) || command.Dir != repository {
		t.Fatalf("unexpected command: %#v in %q", command.Args, command.Dir)
	}
	profilePath := filepath.Join(codexHome, "holark-holon-session-1.config.toml")
	profile, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	wantProfile := "# Managed by Holark.\n[projects." + strconv.Quote(repository) + "]\ntrust_level = \"trusted\"\n"
	if string(profile) != wantProfile {
		t.Fatalf("unexpected profile contents: %q", profile)
	}
	info, err := os.Stat(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("profile permissions = %o, want 600", info.Mode().Perm())
	}
	for _, value := range command.Env {
		if strings.HasPrefix(value, "HOLARK_") && value != "HOLARK_HOLON_ID=session-1" && value != "HOLARK_REPO_PATH="+repository {
			t.Fatalf("unexpected Holark variable: %q", value)
		}
	}
}

func TestFreshCommandModelArgument(t *testing.T) {
	useTemporaryCodexHome(t)
	command, err := Command("codex", t.TempDir(), "session-model", "prompt", "custom-model")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--model", "custom-model", "--", "prompt"}
	if !reflect.DeepEqual(command.Args[len(command.Args)-4:], want) {
		t.Fatalf("args = %q", command.Args)
	}
}

func TestCommandAllowsEmptyPrompt(t *testing.T) {
	useTemporaryCodexHome(t)
	repository := filepath.Join(string(filepath.Separator), "work", "repo")
	command, err := Command("/usr/local/bin/codex", repository, "session-1", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/local/bin/codex", "--cd", repository, "--profile", "holark-holon-session-1", "--sandbox", "workspace-write", "--ask-for-approval", "on-request"}
	if !reflect.DeepEqual(command.Args, want) || command.Dir != repository {
		t.Fatalf("unexpected command: %#v in %q", command.Args, command.Dir)
	}
}

func TestCommandRejectsInvalidPrompt(t *testing.T) {
	repository := filepath.Join(string(filepath.Separator), "work", "repo")
	if _, err := Command("codex", repository, "session-1", strings.Repeat("x", protocol.MaxPromptBytes+1)); err == nil {
		t.Fatal("expected oversized prompt to fail")
	}
}

func TestEnvironmentRemovesHostedValues(t *testing.T) {
	got := environment([]string{"PATH=/bin", "HOLARK_NODE_TOKEN=secret", "HOLARK_OTHER=private"}, "session-1", "/repo")
	want := []string{"PATH=/bin", "TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=holark", "HOLARK_HOLON_ID=session-1", "HOLARK_REPO_PATH=/repo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected environment: %#v", got)
	}
}

func TestEnvironmentRemovesParentTerminalValues(t *testing.T) {
	got := environment([]string{"PATH=/bin", "TERM=screen-256color", "TMUX=/tmp/tmux", "TMUX_PANE=%1", "STY=screen", "WINDOW=1"}, "session-1", "/repo")
	want := []string{"PATH=/bin", "TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=holark", "HOLARK_HOLON_ID=session-1", "HOLARK_REPO_PATH=/repo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected environment: %#v", got)
	}
}

func TestResumeCommand(t *testing.T) {
	useTemporaryCodexHome(t)
	repository := filepath.Join(string(filepath.Separator), "work", "repo")
	command, err := ResumeCommand("/usr/local/bin/codex", repository, "session-1", "codex-session-1", "continue the work")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/local/bin/codex", "resume", "--cd", repository, "--profile", "holark-holon-session-1", "codex-session-1", "--", "continue the work"}
	if !reflect.DeepEqual(command.Args, want) || command.Dir != repository {
		t.Fatalf("unexpected resume command: %#v in %q", command.Args, command.Dir)
	}
}

func TestForkCommand(t *testing.T) {
	useTemporaryCodexHome(t)
	repository := filepath.Join(string(filepath.Separator), "work", "repo")
	for _, test := range []struct {
		name, prompt string
		want         []string
	}{
		{"prompted", "discuss the commit", []string{"/usr/local/bin/codex", "fork", "--cd", repository, "--profile", "holark-holon-session-1", "codex-source-1", "--", "discuss the commit"}},
		{"promptless", "", []string{"/usr/local/bin/codex", "fork", "--cd", repository, "--profile", "holark-holon-session-1", "codex-source-1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			command, err := ForkCommand("/usr/local/bin/codex", repository, "session-1", "codex-source-1", test.prompt)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(command.Args, test.want) || command.Dir != repository {
				t.Fatalf("unexpected fork command: %#v in %q", command.Args, command.Dir)
			}
		})
	}
}

func TestForkCommandRejectsInvalidSourceAndPrompt(t *testing.T) {
	repository := filepath.Join(string(filepath.Separator), "work", "repo")
	tests := []struct {
		name   string
		source string
		prompt string
	}{
		{name: "missing source", prompt: "discuss the commit"},
		{name: "invalid source", source: "source/session", prompt: "discuss the commit"},
		{name: "oversized prompt", source: "codex-source-1", prompt: strings.Repeat("x", protocol.MaxPromptBytes+1)},
		{name: "invalid prompt", source: "codex-source-1", prompt: string([]byte{0xff})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ForkCommand("codex", repository, "session-1", test.source, test.prompt); err == nil {
				t.Fatal("expected invalid fork command to fail")
			}
		})
	}
}

func useTemporaryCodexHome(t *testing.T) string {
	t.Helper()
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	return codexHome
}
