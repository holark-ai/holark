package terminalenv

import (
	"reflect"
	"testing"
)

func TestSanitizeForBrowserPTY(t *testing.T) {
	got := SanitizeForBrowserPTY([]string{
		"PATH=/bin",
		"TERM=screen-256color",
		"COLORTERM=old-color",
		"TERM_PROGRAM=tmux",
		"TMUX=/private/tmp/tmux-501/default,123,0",
		"TMUX_PANE=%1",
		"STY=screen",
		"WINDOW=2",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
	})
	want := []string{
		"PATH=/bin",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"TERM_PROGRAM=holark",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %#v, want %#v", got, want)
	}
}

func TestHarnessEnvironmentStripsInheritedHolarkConfiguration(t *testing.T) {
	got := HarnessEnvironment([]string{
		"PATH=/bin",
		"HOLARK_NODE_TOKEN=secret",
		"HOLARK_CLI_AUTH_TOKEN=cli-token",
		"HOLARK_SERVER_URL=http://127.0.0.1:1234",
		"HOLARK_OTHER=private",
	}, "session-1", "harness-1", "/repo")
	want := []string{
		"PATH=/bin",
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"TERM_PROGRAM=holark",
		"HOLARK_HOLON_ID=session-1",
		"HOLARK_AGENT_SESSION_ID=harness-1",
		"HOLARK_REPO_PATH=/repo",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %#v, want %#v", got, want)
	}
}

func TestContextOwnsRuntimeAndBinaryWhilePreservingAssignedIdentities(t *testing.T) {
	context := Context{ExecutablePath: "/opt/holark/bin/holark", RuntimeDirectory: "/run/user/1000/holark"}
	got := context.Environment([]string{
		"PATH=/usr/bin:/bin",
		"HOLARK_SERVER_URL=http://127.0.0.1:8080",
		"HOLARK_CLI_AUTH_TOKEN=stale-token",
		"HOLARK_RUNTIME_DIR=/tmp/stale",
		"HOLARK_OTHER=stale",
		"HOLARK_HOLON_ID=holon-one",
		"HOLARK_AGENT_SESSION_ID=agent-one",
		"HOLARK_REPO_PATH=/work/repository",
	})
	want := []string{
		"PATH=/opt/holark/bin:/usr/bin:/bin",
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"TERM_PROGRAM=holark",
		"HOLARK_HOLON_ID=holon-one",
		"HOLARK_AGENT_SESSION_ID=agent-one",
		"HOLARK_REPO_PATH=/work/repository",
		"HOLARK_RUNTIME_DIR=/run/user/1000/holark",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %#v, want %#v", got, want)
	}
}
