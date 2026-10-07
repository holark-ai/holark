package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/harness/claudecode"
	"github.com/holark-ai/holark/internal/holarkcli"
)

func TestClaudeHookCommandDeliversSessionStart(t *testing.T) {
	runtimeDir, launchToken := prepareClaudeRuntime(t)
	metadata, err := os.ReadFile(filepath.Join(runtimeDir, claudecode.MetadataFileName))
	if err != nil {
		t.Fatal(err)
	}
	var runtime struct {
		SocketPath string `json:"socket_path"`
	}
	if err := json.Unmarshal(metadata, &runtime); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", runtime.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	received := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		data, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			return
		}
		received <- data
		_, _ = conn.Write([]byte("ok\n"))
	}()
	payload := `{"hook_event_name":"SessionStart","session_id":"123e4567-e89b-12d3-a456-426614174000"}`
	var stderr bytes.Buffer

	code := runCLI(context.Background(), holarkcli.Options{
		Args: []string{
			"claude-hook",
			"--runtime-dir", runtimeDir,
			"--launch-token", launchToken,
			"--harness-session-id", "harness-one",
		},
		Stdin:  strings.NewReader(payload),
		Stdout: io.Discard,
		Stderr: &stderr,
		Env:    func(string) string { return "" },
	})
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}

	var record []byte
	select {
	case record = <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("hook was not delivered")
	}
	var event claudecode.NormalizedEvent
	if err := json.Unmarshal(bytes.TrimSpace(record), &event); err != nil {
		t.Fatal(err)
	}
	if event.Event != "SessionStart" || event.SessionID != "123e4567-e89b-12d3-a456-426614174000" || event.HarnessSessionID != "harness-one" {
		t.Fatalf("event = %#v", event)
	}
}

func TestClaudeHookCommandRejectsInvalidArgumentsWithoutExposingInput(t *testing.T) {
	_, launchToken := prepareClaudeRuntime(t)
	const payload = `{"hook_event_name":"SessionStart","session_id":"PAYLOAD_SECRET"}`
	tests := []struct {
		name string
		args []string
	}{
		{name: "malformed flag", args: []string{"claude-hook", "--runtime-dir"}},
		{name: "unexpected argument", args: []string{"claude-hook", "--launch-token", launchToken, "UNEXPECTED_SECRET"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			claimed, code := dispatchInternalCommand(test.args, strings.NewReader(payload), &stderr)
			if !claimed || code != 1 {
				t.Fatalf("claimed = %v, code = %d", claimed, code)
			}
			if stderr.String() != "holark: Claude hook ingestion failed\n" {
				t.Fatalf("stderr = %q", stderr.String())
			}
			for _, secret := range []string{launchToken, "UNEXPECTED_SECRET", "PAYLOAD_SECRET"} {
				if strings.Contains(stderr.String(), secret) {
					t.Fatalf("stderr exposed sensitive input %q", secret)
				}
			}
		})
	}
}

func TestUnrelatedCommandContinuesToPublicCLI(t *testing.T) {
	var stdout, stderr bytes.Buffer
	options := holarkcli.Options{
		Args:   []string{"help"},
		Stdin:  strings.NewReader("PUBLIC_CLI_STDIN"),
		Stdout: &stdout,
		Stderr: &stderr,
		Env:    func(string) string { return "" },
	}
	claimed, code := dispatchInternalCommand(options.Args, options.Stdin, options.Stderr)
	if claimed || code != 0 {
		t.Fatalf("claimed = %v, code = %d", claimed, code)
	}
	if code := runCLI(context.Background(), options); code != 0 {
		t.Fatalf("public CLI code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("public CLI output = %q", stderr.String())
	}
}

func prepareClaudeRuntime(t *testing.T) (string, string) {
	t.Helper()
	runtimeDir := t.TempDir()
	if _, err := claudecode.PrepareRuntime(runtimeDir, "/opt/holark", "harness-one"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(runtimeDir, claudecode.MetadataFileName))
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		LaunchToken string `json:"launch_token"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	return runtimeDir, metadata.LaunchToken
}
