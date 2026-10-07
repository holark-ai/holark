//go:build integration

package terminalhost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/terminalhost/xtermtracker"
	"github.com/holark-ai/holark/internal/terminals"
)

// Preloading the queue makes pending output deterministic while exercising the
// host's tracking loop and real xterm worker. Each pair of chunks reaches the
// checkpoint threshold; queued bytes must neither delay nor accelerate it.
func TestCheckpointAdvancesWithPendingOutput(t *testing.T) {
	checkpoints := make(checkpointSequenceRecorder, 8)
	manager, err := NewManager(Options{DetachedTailBytes: 1024, Diagnostic: checkpoints})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	id := terminals.TerminalID("checkpoint-pending-output")
	dimensions := terminals.Dimensions{Columns: 80, Rows: 24}
	running := &process{
		id: id, dimensions: dimensions,
		checkpoint:  terminals.NewCheckpoint(id, 0, dimensions, nil),
		trackerWake: make(chan struct{}, 1), trackerStop: make(chan struct{}), trackerDone: make(chan struct{}),
	}
	manager.processes[id] = running
	t.Cleanup(func() { delete(manager.processes, id) })
	for sequence := uint64(1); sequence <= 6; sequence++ {
		_, err := manager.appendNotification(running, terminals.ProcessNotification{
			Kind: terminals.ProcessOutput, Data: bytes.Repeat([]byte("x"), 384), Characters: 384,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	go manager.trackScreen(running)
	t.Cleanup(func() { manager.stopTracking(running) })
	for _, want := range []uint64{2, 4, 6} {
		select {
		case got := <-checkpoints:
			if got != want {
				t.Fatalf("checkpoint sequence = %d, want %d (six chunks queued)", got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for checkpoint at sequence %d", want)
		}
	}
}

type checkpointSequenceRecorder chan uint64

func (recorder checkpointSequenceRecorder) Record(_, event string, fields map[string]any) {
	if event == "host_checkpoint_committed" {
		recorder <- fields["checkpoint_sequence"].(uint64)
	}
}

// Four real terminals share two background workers that prepare terminal
// snapshots for browser reloads. If one worker is killed, the host replaces it
// and rebuilds the affected snapshots from the last saved screen plus subsequent
// output. The other worker and every terminal process must keep running.
//
// Exact reconstruction is possible only while the retained tail still contains
// every output and resize after the saved screen. If a retention limit creates a
// gap, the host switches to degraded restoration instead. The browser behavior
// for that fallback is covered by "degraded restore is disclosed and a parsed
// full reset restores trust" in web/e2e/terminal-tracking.spec.ts.
func TestBoundedWorkerPoolRecoversOnlyModelsFromKilledWorker(t *testing.T) {
	pool, err := xtermtracker.NewPool(xtermtracker.PoolOptions{MaximumWorkers: 2})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{MaximumTerminals: 4, ScreenTrackerFactory: pool})
	if err != nil {
		_ = pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	dimensions := terminals.Dimensions{Columns: 80, Rows: 24}
	ids := []terminals.TerminalID{"pool-crash-one", "pool-crash-two", "pool-crash-three", "pool-crash-four"}
	for _, id := range ids {
		if _, err := manager.Launch(t.Context(), terminals.LaunchSpec{
			TerminalID: id, Kind: terminals.LaunchCommand,
			Command: "/bin/sh", Arguments: []string{"-c", "i=0; while :; do printf 'POOL_CRASH_%06d\\n' \"$i\"; i=$((i+1)); sleep 0.02; done"},
			Environment: os.Environ(), CWD: t.TempDir(), Dimensions: dimensions,
		}); err != nil {
			t.Fatal(err)
		}
	}

	initial := waitForPoolState(t, pool, 2, len(ids), nil)
	before := terminalSequences(manager, ids)
	killedPID := initial.PIDs[0]
	survivingPID := initial.PIDs[1]
	worker, err := os.FindProcess(killedPID)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Kill(); err != nil {
		t.Fatalf("kill tracking worker %d: %v", killedPID, err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		stats := pool.Stats()
		if stats.Workers == 2 && stats.Models == len(ids) && !slices.Contains(stats.PIDs, killedPID) &&
			slices.Contains(stats.PIDs, survivingPID) && allSequencesAdvanced(manager, ids, before) && allPTYsRunning(manager, ids) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("worker models did not recover without harming PTYs: before=%v after=%v stats=%+v inventory=%+v",
		before, terminalSequences(manager, ids), pool.Stats(), manager.Inventory())
}

func waitForPoolState(t *testing.T, pool *xtermtracker.Pool, workers, models int, excludedPIDs []int) xtermtracker.PoolStats {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stats := pool.Stats()
		if stats.Workers == workers && stats.Models == models {
			excluded := false
			for _, pid := range excludedPIDs {
				excluded = excluded || slices.Contains(stats.PIDs, pid)
			}
			if !excluded {
				return stats
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pool state did not reach workers=%d models=%d: %+v", workers, models, pool.Stats())
	return xtermtracker.PoolStats{}
}

func terminalSequences(manager *Manager, ids []terminals.TerminalID) map[terminals.TerminalID]uint64 {
	sequences := make(map[terminals.TerminalID]uint64, len(ids))
	for _, id := range ids {
		running, err := manager.find(id)
		if err != nil {
			continue
		}
		running.stateMu.Lock()
		sequences[id] = running.sequence
		running.stateMu.Unlock()
	}
	return sequences
}

func allSequencesAdvanced(manager *Manager, ids []terminals.TerminalID, before map[terminals.TerminalID]uint64) bool {
	after := terminalSequences(manager, ids)
	for _, id := range ids {
		if after[id] <= before[id] {
			return false
		}
	}
	return true
}

// When screen tracking is unavailable, snapshots cannot advance and terminal
// output accumulates in the Go-owned retained tails. Per-terminal limits alone
// are not enough: total memory would still grow with the number of terminals.
//
// Three real terminals cover attached, detached, and never-attached states and
// each produces more output than the deliberately small test limits. The host
// may discard older restoration history and switch a terminal to degraded
// restoration, but the combined retained output must stay within the aggregate
// limit and every PTY must remain alive and continue producing output. The test
// intentionally does not prescribe which terminal's retained history is
// degraded first.
func TestAggregateTailLimitPreservesRunningAttachedAndDetachedPTYs(t *testing.T) {
	const (
		checkpointBytes = 4 * 1024
		terminalBytes   = 16 * 1024
		aggregateBytes  = 24 * 1024
	)
	manager, err := NewManager(Options{
		MaximumTerminals:          3,
		DetachedTailBytes:         checkpointBytes,
		MaximumTailBytes:          terminalBytes,
		MaximumAggregateTailBytes: aggregateBytes,
		ScreenTrackerFactory:      unavailableTrackerFactory{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	dimensions := terminals.Dimensions{Columns: 80, Rows: 24}
	ids := []terminals.TerminalID{"aggregate-attached", "aggregate-detached", "aggregate-never-attached"}
	for _, id := range ids {
		if _, err := manager.Launch(t.Context(), terminals.LaunchSpec{
			TerminalID: id, Kind: terminals.LaunchCommand,
			Command: "/bin/sh", Arguments: []string{"-c", "IFS= read -r _; head -c 131072 /dev/zero | tr '\\000' x; printf '\\nTAIL_LIMIT_DONE\\n'; sleep 30"},
			Environment: os.Environ(), CWD: t.TempDir(), Dimensions: dimensions,
		}); err != nil {
			t.Fatal(err)
		}
	}

	attached, err := manager.Attach(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	detached, err := manager.Attach(ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Detach(ids[1], detached.Attachment); err != nil {
		t.Fatal(err)
	}
	if !attached.Attachment.Valid() {
		t.Fatal("attached terminal did not retain its attachment")
	}
	for _, id := range ids {
		if err := manager.Input(t.Context(), id, []byte("\n")); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if allOutputObserved(manager, ids) && retainedTailBytes(manager) <= aggregateBytes && allTerminalTailsBounded(manager, terminalBytes) &&
			allPTYsRunning(manager, ids) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("retention exceeded limits or harmed a PTY: aggregate=%d inventory=%+v", retainedTailBytes(manager), manager.Inventory())
}

type unavailableTrackerFactory struct{}

func (unavailableTrackerFactory) New(terminals.TerminalID, terminals.Dimensions) (terminals.ScreenTracker, error) {
	return nil, errors.New("injected tracking outage")
}

func retainedTailBytes(manager *Manager) int {
	manager.mu.Lock()
	processes := make([]*process, 0, len(manager.processes))
	for _, running := range manager.processes {
		processes = append(processes, running)
	}
	manager.mu.Unlock()
	total := 0
	for _, running := range processes {
		running.stateMu.Lock()
		total += running.tailBytes
		running.stateMu.Unlock()
	}
	return total
}

func allTerminalTailsBounded(manager *Manager, maximum int) bool {
	manager.mu.Lock()
	processes := make([]*process, 0, len(manager.processes))
	for _, running := range manager.processes {
		processes = append(processes, running)
	}
	manager.mu.Unlock()
	for _, running := range processes {
		running.stateMu.Lock()
		bounded := running.tailBytes <= maximum
		running.stateMu.Unlock()
		if !bounded {
			return false
		}
	}
	return true
}

func allOutputObserved(manager *Manager, ids []terminals.TerminalID) bool {
	for _, id := range ids {
		running, err := manager.find(id)
		if err != nil {
			return false
		}
		running.stateMu.Lock()
		observed := running.sequence >= 4
		running.stateMu.Unlock()
		if !observed {
			return false
		}
	}
	return true
}

func allPTYsRunning(manager *Manager, ids []terminals.TerminalID) bool {
	for _, id := range ids {
		if err := manager.Signal(context.Background(), id, syscall.Signal(0)); err != nil {
			return false
		}
	}
	return true
}
