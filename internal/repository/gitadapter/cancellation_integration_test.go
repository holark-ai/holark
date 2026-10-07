package gitadapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/repository"
)

// A real Git transport provides deterministic failure injection without
// replacing Git or invoking any agent CLI.
func gatedTransport(t *testing.T, root, remote string, inheritedPipe bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
	script := filepath.Join(dir, "transport.sh")
	body := fmt.Sprintf("#!/bin/sh\necho $$ >> '%s'\nwhile [ ! -f '%s' ]; do sleep 0.02; done\nexec git-upload-pack '%s'\n", started, release, remote)
	if inheritedPipe {
		body = fmt.Sprintf("#!/bin/sh\nsleep 300 >&2 &\necho $! > '%s'\nwait\n", started)
	}
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	git(t, root, "config", "protocol.ext.allow", "always")
	git(t, root, "remote", "set-url", "origin", "ext::sh "+script)
	return started, release
}
func awaitFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return b
		}
		select {
		case <-deadline.C:
			t.Fatal("transport did not start")
		case <-ticker.C:
		}
	}
}
func awaitError(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("operation remained blocked")
		return nil
	}
}
func TestGitCancellationReleasesInheritedPipesAndRepositoryAccess(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint("deadline=", deadline), func(t *testing.T) {
			root, remote := fixture(t)
			adapter, err := Open(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			started, _ := gatedTransport(t, root, remote, true)
			ctx, cancel := context.WithCancel(t.Context())
			if deadline {
				ctx, cancel = context.WithTimeout(t.Context(), 500*time.Millisecond)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := adapter.Refresh(ctx); done <- err }()
			pid, _ := strconv.Atoi(strings.TrimSpace(string(awaitFile(t, started))))
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			if !deadline {
				cancel()
			}
			err = awaitError(t, done)
			want := context.Canceled
			if deadline {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Fatalf("error=%v, want %v", err, want)
			}
			until := time.Now().Add(3 * time.Second)
			for syscall.Kill(pid, 0) == nil && time.Now().Before(until) {
				time.Sleep(10 * time.Millisecond)
			}
			if syscall.Kill(pid, 0) == nil {
				t.Fatal("transport descendant survived cancellation")
			}
			git(t, root, "remote", "set-url", "origin", remote)
			if _, err := adapter.Refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestRepositoryWaitCancellationDoesNotRunAfterHolderFinishes(t *testing.T) {
	root, remote := fixture(t)
	adapter, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	started, release := gatedTransport(t, root, remote, false)
	holder := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _, err := adapter.Refresh(ctx); holder <- err }()
	awaitFile(t, started)
	waiterCtx, stop := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer stop()
	waiter := make(chan error, 1)
	go func() { _, err := adapter.PrepareBranch(waiterCtx, "main"); waiter <- err }()
	if err := awaitError(t, waiter); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error=%v", err)
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := awaitError(t, holder); err != nil {
		t.Fatal(err)
	}
	calls := strings.Fields(string(awaitFile(t, started)))
	if len(calls) != 1 {
		t.Fatalf("canceled waiter fetched: %v", calls)
	}
}
func TestSharedRefreshSurvivesInitiatorCancellationAndFetchesAgain(t *testing.T) {
	root, remote := fixture(t)
	adapter, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	started, release := gatedTransport(t, root, remote, false)
	service := repository.NewServiceWithContext(t.Context(), adapter)
	firstCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { _, err := service.Refresh(firstCtx); first <- err }()
	awaitFile(t, started)
	// Start the second wait synchronously, then cancel the initiator while the
	// real transport remains gated. The timer is only the cancellation trigger.
	stop := time.AfterFunc(100*time.Millisecond, cancel)
	defer stop.Stop()
	go func() {
		if err := awaitError(t, first); !errors.Is(err, context.Canceled) {
			t.Errorf("initiator=%v", err)
		}
		_ = os.WriteFile(release, []byte("release"), 0600)
	}()
	_, err = service.Refresh(t.Context())
	second <- err
	if err := awaitError(t, second); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Fields(string(awaitFile(t, started))); len(lines) != 1 {
		t.Fatalf("duplicate fetches=%v", lines)
	}
	if _, err := service.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Fields(string(awaitFile(t, started))); len(lines) != 2 {
		t.Fatalf("completed refresh was reused: %v", lines)
	}
}

func TestWorkspaceInspectionReleasesInheritedPipesAndRepositoryAccess(t *testing.T) {
	for _, mode := range []string{"canceled", "deadline", "parent-exited"} {
		t.Run(mode, func(t *testing.T) {
			root, base := workspaceRepository(t)
			adapter := openWorkspaceAdapter(t, root)
			workspace, err := adapter.CreateWorkspace(t.Context(), "inspection", base)
			if err != nil {
				t.Fatal(err)
			}

			dir := t.TempDir()
			started := filepath.Join(dir, "started")
			hook := filepath.Join(dir, "fsmonitor.sh")
			// Keep Git's stderr pipe open even if Git or the hook exits.
			body := fmt.Sprintf("#!/bin/sh\nsleep 300 >&2 &\necho $! > '%s'\n", started)
			if mode == "parent-exited" {
				body += "printf 'token\\0/\\0'\n"
			} else {
				body += "wait\n"
			}
			if err := os.WriteFile(hook, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			git(t, root, "config", "core.fsmonitor", hook)
			git(t, root, "config", "core.fsmonitorHookVersion", "2")

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, time.Second)
				defer stop()
			}
			done := make(chan error, 1)
			go func() {
				_, err := adapter.InspectWorkspaceWithOptions(ctx, workspace.ID, repository.InspectOptions{SummaryOnly: true})
				done <- err
			}()
			pid, err := strconv.Atoi(strings.TrimSpace(string(awaitFile(t, started))))
			if err != nil || pid <= 0 {
				t.Fatalf("invalid fsmonitor descendant PID: %d, %v", pid, err)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			if mode == "canceled" {
				cancel()
			}
			if err := awaitError(t, done); err == nil {
				t.Fatal("inspection with an inherited pipe succeeded")
			}
			until := time.Now().Add(3 * time.Second)
			for syscall.Kill(pid, 0) == nil && time.Now().Before(until) {
				time.Sleep(10 * time.Millisecond)
			}
			if syscall.Kill(pid, 0) == nil {
				t.Fatal("fsmonitor descendant survived inspection")
			}

			git(t, root, "config", "--unset", "core.fsmonitor")
			nextCtx, stop := context.WithTimeout(t.Context(), time.Second)
			defer stop()
			if _, err := adapter.InspectWorkspace(nextCtx, workspace.ID); err != nil {
				t.Fatalf("inspect after releasing repository access: %v", err)
			}
		})
	}
}
