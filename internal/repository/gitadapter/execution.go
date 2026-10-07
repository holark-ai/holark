package gitadapter

import (
	"context"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/holark-ai/holark/internal/gitexec"
)

const operationTimeout = gitexec.OperationTimeout

func gitCommand(ctx context.Context, args ...string) (*exec.Cmd, func()) {
	return gitexec.Command(ctx, args...)
}

// Zero-value usable, cancellable serialization for repository operations.
// The deadline includes time waiting for the current operation.
type operationLock struct {
	once     sync.Once
	token    chan struct{}
	ownerMu  sync.Mutex
	owner    string
	acquired time.Time
}

func (m *operationLock) lock(ctx context.Context, operation string) error {
	m.once.Do(func() { m.token = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return err
	}
	slog.DebugContext(ctx, "repository waiting for access", "operation", operation)
	slow := time.AfterFunc(5*time.Second, func() {
		m.ownerMu.Lock()
		defer m.ownerMu.Unlock()
		slog.WarnContext(ctx, "repository still waiting for access", "operation", operation, "holder", m.owner, "held_for", time.Since(m.acquired))
	})
	defer slow.Stop()
	select {
	case m.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		m.ownerMu.Lock()
		m.owner, m.acquired = operation, time.Now()
		m.ownerMu.Unlock()
		slog.DebugContext(ctx, "repository acquired access", "operation", operation)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *operationLock) Unlock() {
	m.ownerMu.Lock()
	slog.Debug("repository released access", "operation", m.owner, "held_for", time.Since(m.acquired))
	m.owner = ""
	m.ownerMu.Unlock()
	<-m.token
}
