package localshell

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run the production lifecycle in a separate process, including its environment
// and OS-owned lease. No agent sessions are started by this test.
func TestEphemeralProcess(t *testing.T) {
	repository := os.Getenv("HOLARK_EPHEMERAL_TEST_REPOSITORY")
	if repository == "" {
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := Run(ctx, Options{RepositoryPath: repository, Ephemeral: true, Stdout: os.Stdout}); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type ephemeralProcess struct {
	command   *exec.Cmd
	url       string
	directory string
	stopped   bool
}

func startEphemeralProcess(t *testing.T, repository, temporary string) *ephemeralProcess {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestEphemeralProcess$")
	command.Env = append(os.Environ(), "HOLARK_EPHEMERAL_TEST_REPOSITORY="+repository, "TMPDIR="+temporary, "HOLARK_LISTEN_ADDR=127.0.0.1:0")
	command.Stderr = os.Stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	process := &ephemeralProcess{command: command}
	t.Cleanup(func() {
		if !process.stopped {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "http://") {
				ready <- strings.Split(scanner.Text(), "/?launch=")[0]
				return
			}
		}
		ready <- ""
	}()
	select {
	case process.url = <-ready:
		if process.url == "" {
			t.Fatal("ephemeral process exited before publishing its URL")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("ephemeral process did not start")
	}
	paths, err := filepath.Glob(filepath.Join(temporary, "holark-*", "ephemeral", "*", "run-*", "runtime", "connections", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), process.url) {
			process.directory = filepath.Dir(filepath.Dir(filepath.Dir(path)))
		}
	}
	if process.directory == "" {
		t.Fatal("instance connection not found")
	}
	return process
}

func stopEphemeralProcess(t *testing.T, process *ephemeralProcess, crash bool) {
	t.Helper()
	var err error
	if crash {
		err = process.command.Process.Kill()
	} else {
		err = process.command.Process.Signal(os.Interrupt)
	}
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- process.command.Wait() }()
	select {
	case err := <-done:
		process.stopped = true
		if !crash && err != nil {
			t.Fatal(err)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("ephemeral process did not stop")
	}
}

func TestEphemeralInstancesPreserveLiveStateAndCollectAbandonedState(t *testing.T) {
	temporary := t.TempDir()
	repository := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repository}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return string(output)
	}
	git("init", "-b", "main")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	first := startEphemeralProcess(t, repository, temporary)
	createWorktree := func(process *ephemeralProcess, id string) string {
		t.Helper()
		path := filepath.Join(process.directory, "home", "worktrees", id, "repo")
		git("worktree", "add", "-b", "holark/"+id, path, "HEAD")
		if err := os.WriteFile(filepath.Join(path, "dirty.txt"), []byte("keep me"), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	firstID := "holon_111111111111111111111111"
	firstWorktree := createWorktree(first, firstID)
	second := startEphemeralProcess(t, repository, temporary)
	secondID := "holon_222222222222222222222222"
	secondWorktree := createWorktree(second, secondID)
	assertLive := func(process *ephemeralProcess, worktree string) {
		t.Helper()
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Get(process.url)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("server status: %d", response.StatusCode)
		}
		if data, err := os.ReadFile(filepath.Join(worktree, "dirty.txt")); err != nil || string(data) != "keep me" {
			t.Fatalf("live worktree lost: %q, %v", data, err)
		}
	}
	assertGone := func(process *ephemeralProcess, id string) {
		t.Helper()
		if _, err := os.Stat(process.directory); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("instance directory remains: %v", err)
		}
		if strings.Contains(git("worktree", "list", "--porcelain"), process.directory) {
			t.Fatal("worktree registration remains")
		}
		if strings.TrimSpace(git("branch", "--list", "holark/"+id)) != "" {
			t.Fatal("ephemeral branch remains")
		}
	}
	assertLive(first, firstWorktree)
	assertLive(second, secondWorktree)
	stopEphemeralProcess(t, second, false)
	assertGone(second, secondID)
	assertLive(first, firstWorktree)
	abandoned := startEphemeralProcess(t, repository, temporary)
	abandonedID := "holon_333333333333333333333333"
	abandonedWorktree := createWorktree(abandoned, abandonedID)
	stopEphemeralProcess(t, abandoned, true)
	if _, err := os.Stat(abandonedWorktree); err != nil {
		t.Fatalf("crash did not leave worktree: %v", err)
	}
	replacement := startEphemeralProcess(t, repository, temporary)
	assertGone(abandoned, abandonedID)
	assertLive(first, firstWorktree)
	stopEphemeralProcess(t, replacement, false)
	stopEphemeralProcess(t, first, false)
	assertGone(first, firstID)
}
