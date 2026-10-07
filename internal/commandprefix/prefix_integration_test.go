//go:build darwin || linux

package commandprefix

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

// Use ordinary subprocesses, not emulated agent CLIs, to exercise inherited pipes.
func TestCommandContextDeadlineStopsWrapperChildren(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	cmd := New("/bin/sh", "-c", "sleep 1.2 & echo ready; wait").CommandContext(ctx)
	start := time.Now()
	output, err := cmd.CombinedOutput()
	if cmd.Process != nil {
		defer cmd.Cancel()
	}
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("command error = %v, context error = %v", err, ctx.Err())
	}
	if string(output) != "ready\n" {
		t.Fatalf("wrapper did not start its child: %q", output)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("cancellation waited for the wrapper's child: %v", elapsed)
	}
}

func TestCommandContextEarlyCancelStopsWrapperChildren(t *testing.T) {
	cmd := New("/bin/sh", "-c", "sleep 1.2 & echo ready; wait").CommandContext(t.Context())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	// The child inherits stderr as well as stdout. Wait must not hang draining it.
	cmd.Stderr = new(bytes.Buffer)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Cancel()
	reader := bufio.NewReader(stdout)
	if line, err := reader.ReadString('\n'); err != nil || line != "ready\n" {
		_ = cmd.Cancel()
		_ = cmd.Wait()
		t.Fatalf("wrapper readiness = %q, %v", line, err)
	}
	start := time.Now()
	if err := cmd.Cancel(); err != nil {
		_ = cmd.Wait()
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("expected the cancelled command to fail")
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("early cancellation left a child holding stderr: %v", elapsed)
	}
}

func TestCommandContextBoundsPipeWaitAfterWrapperExit(t *testing.T) {
	cmd := New("/bin/sh", "-c", "sleep 5 & echo ready").CommandContext(t.Context())
	start := time.Now()
	output, err := cmd.CombinedOutput()
	if cmd.Process != nil {
		defer cmd.Cancel()
	}
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("inherited pipe wait error = %v, want %v", err, exec.ErrWaitDelay)
	}
	if string(output) != "ready\n" {
		t.Fatalf("wrapper output = %q", output)
	}
	if elapsed := time.Since(start); elapsed >= 3*time.Second {
		t.Fatalf("pipe wait was not bounded: %v", elapsed)
	}
}
