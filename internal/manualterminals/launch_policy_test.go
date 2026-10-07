package manualterminals

import (
	"reflect"
	"testing"

	"github.com/holark-ai/holark/internal/terminalenv"
)

func TestShellLaunchPolicyAppliesTerminalContext(t *testing.T) {
	policy := ShellLaunchPolicy{TerminalContext: terminalenv.Context{
		ExecutablePath:   "/opt/holark/bin/holark",
		RuntimeDirectory: "/run/holark-custom",
	}}
	command, environment := policy.Resolve([]string{
		"PATH=/usr/bin",
		"HOLARK_SERVER_URL=http://127.0.0.1:8080",
		"HOLARK_CLI_AUTH_TOKEN=stale-token",
		"HOLARK_HOLON_ID=holon-one",
		"HOLARK_REPO_PATH=/work/repository",
	}, "/not/an/executable")
	want := []string{
		"PATH=/opt/holark/bin:/usr/bin",
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"TERM_PROGRAM=holark",
		"HOLARK_HOLON_ID=holon-one",
		"HOLARK_REPO_PATH=/work/repository",
		"HOLARK_RUNTIME_DIR=/run/holark-custom",
	}
	if command != "/bin/sh" || !reflect.DeepEqual(environment, want) {
		t.Fatalf("command = %q, environment = %#v, want %#v", command, environment, want)
	}
}
