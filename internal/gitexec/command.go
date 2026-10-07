// Package gitexec bounds Git execution and cleans up descendant processes.
package gitexec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

var gitSequence atomic.Uint64

// OperationTimeout bounds a Git command and repository operations that use it.
const OperationTimeout = 60 * time.Second
const outputDrainTimeout = 2 * time.Second

// Command creates a bounded Git command with its own process group.
// Callers must defer the returned cleanup function to release descendants and timers.
func Command(ctx context.Context, args ...string) (*exec.Cmd, func()) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	command := exec.CommandContext(ctx, "git", args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = outputDrainTimeout
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	start := time.Now()
	operation := args[0]
	operationID := fmt.Sprintf("git-%d-%d", os.Getpid(), gitSequence.Add(1))
	logger := slog.Default().With("operation", operation, "operation_id", operationID)
	logger.DebugContext(ctx, "git started")
	slow := time.AfterFunc(5*time.Second, func() {
		logger.WarnContext(ctx, "git still running", "elapsed", time.Since(start))
	})
	return command, func() {
		slow.Stop()
		if command.Process != nil {
			// Also collect descendants left behind by a parent that exited normally.
			err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			if err != nil && !errors.Is(err, syscall.ESRCH) {
				logger.WarnContext(ctx, "git descendant cleanup failed", "pid", command.Process.Pid, "error", err)
			} else {
				logger.DebugContext(ctx, "git descendant cleanup completed", "pid", command.Process.Pid, "process_group", command.Process.Pid, "signaled", err == nil)
			}
			logger.DebugContext(ctx, "git completed", "pid", command.Process.Pid, "elapsed", time.Since(start), "cancellation", ctx.Err(), "state", command.ProcessState)
		}
		cancel()
	}
}
