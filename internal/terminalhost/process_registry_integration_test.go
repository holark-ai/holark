//go:build darwin || linux

package terminalhost

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProcessRecoveryKillsOwnedProcessesAndPreservesOtherLaunches(t *testing.T) {
	for _, mode := range []string{"recorded", "before-pid-persistence", "without-marker", "mismatched-identity"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			registry, err := OpenProcessRegistry(directory)
			if err != nil {
				t.Fatal(err)
			}
			// Ignored SIGTERM survives exec, so cleanup must use its force-kill fallback.
			command := exec.Command("/bin/sh", "-c", "trap '' TERM; echo ready; exec sleep 120")
			if mode == "before-pid-persistence" {
				// macOS hides environments of protected system executables such
				// as /bin/sleep. Node is already a Holark test prerequisite.
				command = exec.Command("node", "-e", "process.on('SIGTERM', () => {}); console.log('ready'); setInterval(() => {}, 1000)")
			}
			owner, err := registry.Prepare(command)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "without-marker" || mode == "mismatched-identity" {
				command.Env = []string{"PATH=/bin:/usr/bin"}
			}
			startRecoveryProcess(t, command)
			if mode != "before-pid-persistence" {
				if err := registry.Started(owner, command.Process.Pid); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "mismatched-identity" {
				// Model a stale PID record without relying on the OS to reuse a PID.
				data, err := json.Marshal(processRecord{Owner: owner, PID: command.Process.Pid, Started: "different-creation"})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, owner+".json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			unrelated := exec.Command("/bin/sh", "-c", "echo ready; exec sleep 120")
			unrelated.Env = []string{"PATH=/bin:/usr/bin"}
			startRecoveryProcess(t, unrelated)
			restarted, err := OpenProcessRegistry(directory)
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.Recover(); err != nil {
				t.Fatal(err)
			}
			if got := recoveryProcessAlive(command.Process.Pid); got != (mode == "mismatched-identity") {
				t.Fatalf("old process alive=%t after recovery (%s)", got, mode)
			}
			if !recoveryProcessAlive(unrelated.Process.Pid) {
				t.Fatal("recovery killed an unrelated process")
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatalf("old journal entries=%v, error=%v", entries, err)
			}
			current := exec.Command("/bin/sh", "-c", "echo ready; exec sleep 120")
			currentOwner, err := restarted.Prepare(current)
			if err != nil {
				t.Fatal(err)
			}
			startRecoveryProcess(t, current)
			if err := restarted.Started(currentOwner, current.Process.Pid); err != nil {
				t.Fatal(err)
			}
			if err := restarted.Recover(); err != nil {
				t.Fatal(err)
			}
			if !recoveryProcessAlive(current.Process.Pid) {
				t.Fatal("repeated recovery killed a current launch")
			}
		})
	}
}

func TestProcessRecoveryFindsMarkedOrphanAfterParentExit(t *testing.T) {
	directory := t.TempDir()
	registry, err := OpenProcessRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	parent := exec.Command("/bin/sh", "-c", `"$1" -e 'setInterval(() => {}, 1000)' >/dev/null 2>&1 & echo $!`, "orphan-parent", node)
	if _, err := registry.Prepare(parent); err != nil {
		t.Fatal(err)
	}
	// Leave only the pre-launch journal, then let the original parent exit.
	output, err := parent.Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatal(err)
	}
	identity := processOwnerIdentity(pid)
	t.Cleanup(func() {
		if identity != "" && processOwnerIdentity(pid) == identity {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	if !recoveryProcessAlive(pid) {
		t.Fatal("child did not survive its parent")
	}
	restarted, err := OpenProcessRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(); err != nil {
		t.Fatal(err)
	}
	if recoveryProcessAlive(pid) {
		t.Fatal("marked orphan survived recovery")
	}
}

func startRecoveryProcess(t *testing.T, command *exec.Cmd) {
	t.Helper()
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	t.Cleanup(func() { _ = command.Process.Kill(); <-done })
	ready := make(chan bool, 1)
	go func() { scanner := bufio.NewScanner(stdout); ready <- scanner.Scan() && scanner.Text() == "ready" }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("process exited before readiness")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("process readiness timed out")
	}
}

func TestProcessRecoveryKeepsTrackOfMarkerlessChildrenDuringCleanup(t *testing.T) {
	directory := t.TempDir()
	registry, err := OpenProcessRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(t.TempDir(), "child-pid")
	parent := exec.Command("node", "-e", `
const child = require('child_process').spawn('/bin/sh', ['-c', "trap '' TERM; echo ready; exec sleep 120"], {
  env: {PATH: '/bin:/usr/bin'}, stdio: ['ignore', 'pipe', 'ignore']
});
child.stdout.once('data', () => {
  require('fs').writeFileSync(process.argv[1], String(child.pid));
  console.log('ready');
});
process.on('SIGTERM', () => process.exit(0));
setInterval(() => {}, 1000);
`, pidPath)
	owner, err := registry.Prepare(parent)
	if err != nil {
		t.Fatal(err)
	}
	startRecoveryProcess(t, parent)
	if err := registry.Started(owner, parent.Process.Pid); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	identity := processOwnerIdentity(pid)
	t.Cleanup(func() {
		if identity != "" && processOwnerIdentity(pid) == identity {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	restarted, err := OpenProcessRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(); err != nil {
		t.Fatal(err)
	}
	if recoveryProcessAlive(pid) {
		t.Fatal("markerless child survived after its parent exited during cleanup")
	}
}

func recoveryProcessAlive(pid int) bool {
	output, err := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	state := strings.TrimSpace(string(output))
	return state != "" && !strings.HasPrefix(state, "Z")
}
