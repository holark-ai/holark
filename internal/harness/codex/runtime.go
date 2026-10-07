package codex

import (
	"context"
	"errors"
	"os/exec"
	"sync"

	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/protocol"
)

// Runtime retains a server for each command still owning sessions. Terminal IDs
// route observation and cleanup to the server that actually launched the client.
type Runtime struct {
	operations   sync.WaitGroup
	mu           sync.Mutex
	options      ManagerOptions
	commands     commandprefix.Resolver
	current      string
	managers     map[string]*Manager
	owners       map[string]*runtimeLaunch
	modelReaders map[*Manager]int
	closed       bool
	cleanupErr   error
	closeOnce    sync.Once
}

type runtimeLaunch struct {
	manager  *Manager
	ready    chan struct{}
	stopped  chan struct{}
	stopping bool
	stopErr  error
}

func NewRuntime(options ManagerOptions, commands commandprefix.Resolver) *Runtime {
	prefix := options.Prefix
	if prefix.Executable() == "" {
		prefix = commandprefix.New("codex")
	}
	options.Prefix = prefix
	return &Runtime{options: options, commands: commands, current: prefix.Key(), managers: map[string]*Manager{prefix.Key(): NewManagerWithOptions(options)}, owners: map[string]*runtimeLaunch{}, modelReaders: map[*Manager]int{}}
}

func (r *Runtime) Prepare(ctx context.Context, cmd *exec.Cmd, session protocol.HarnessSession) error {
	return r.PrepareWithPrefix(ctx, cmd, session, commandprefix.New("codex"))
}

func (r *Runtime) PrepareWithPrefix(ctx context.Context, cmd *exec.Cmd, session protocol.HarnessSession, prefix commandprefix.Prefix) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("Codex runtime is closed")
	}
	if r.owners[session.TerminalID] != nil {
		r.mu.Unlock()
		return errors.New("Codex terminal is already owned")
	}
	r.operations.Add(1)
	defer r.operations.Done()
	manager := r.managerLocked(prefix)
	// Reserve ownership during preparation, so another launch cannot retire it.
	launch := &runtimeLaunch{manager: manager, ready: make(chan struct{}), stopped: make(chan struct{})}
	r.owners[session.TerminalID] = launch
	retired := r.retireLocked(ctx, nil)
	r.mu.Unlock()
	r.closeManagers(retired)
	err := manager.Prepare(ctx, cmd, session)
	close(launch.ready)
	if err != nil {
		return errors.Join(err, r.stop(ctx, session.TerminalID, true))
	}
	return nil
}

// managerLocked selects the server for new work without disturbing existing sessions.
func (r *Runtime) managerLocked(prefix commandprefix.Prefix) *Manager {
	key := prefix.Key()
	manager := r.managers[key]
	if manager == nil {
		options := r.options
		options.Prefix = prefix
		manager = NewManagerWithOptions(options)
		r.managers[key] = manager
	}
	r.current = key
	return manager
}

// ModelsWithPrefix shares the configured app-server with launches. An in-flight
// discovery keeps its server alive if the configured command changes meanwhile.
func (r *Runtime) ModelsWithPrefix(ctx context.Context, prefix commandprefix.Prefix) ([]protocol.ModelChoice, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errors.New("Codex runtime is closed")
	}
	r.operations.Add(1)
	defer r.operations.Done()
	manager := r.managerLocked(prefix)
	r.modelReaders[manager]++
	retired := r.retireLocked(ctx, nil)
	r.mu.Unlock()
	r.closeManagers(retired)
	defer func() {
		r.mu.Lock()
		r.modelReaders[manager]--
		if r.modelReaders[manager] == 0 {
			delete(r.modelReaders, manager)
		}
		retired := r.retireLocked(ctx, nil)
		r.mu.Unlock()
		r.closeManagers(retired)
	}()
	return manager.Models(ctx)
}

func (r *Runtime) Monitor(ctx context.Context, terminalID string, sink func(Observation)) {
	r.mu.Lock()
	launch := r.owners[terminalID]
	r.mu.Unlock()
	if launch == nil {
		sink(Observation{Status: protocol.ObservabilityDegraded, Message: "Codex launch is not owned by this runtime."})
		return
	}
	launch.manager.Monitor(ctx, terminalID, sink)
}

func (r *Runtime) Stop(ctx context.Context, terminalID string) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.operations.Add(1)
	r.mu.Unlock()
	defer r.operations.Done()
	return r.stop(ctx, terminalID, false)
}

func (r *Runtime) stop(ctx context.Context, terminalID string, failed bool) error {
	r.mu.Lock()
	launch := r.owners[terminalID]
	if launch == nil {
		r.mu.Unlock()
		return nil
	}
	if launch.stopping {
		r.mu.Unlock()
		select {
		case <-launch.stopped:
			return launch.stopErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	launch.stopping = true
	r.mu.Unlock()
	// Preparation owns the reservation until the server has bound the terminal.
	<-launch.ready
	err := launch.manager.Stop(ctx, terminalID)
	r.mu.Lock()
	delete(r.owners, terminalID)
	// Keep the current server warm; retire older servers after their final session.
	var failedManager *Manager
	if failed {
		failedManager = launch.manager
	}
	retired := r.retireLocked(ctx, failedManager)
	r.mu.Unlock()
	launch.stopErr = errors.Join(err, r.closeManagers(retired))
	close(launch.stopped)
	return launch.stopErr
}

func (r *Runtime) retireLocked(ctx context.Context, failedManager *Manager) []*Manager {
	current := r.current
	if r.commands != nil {
		// Settings may have changed in another repository since the last launch.
		// If they cannot be read, only servers still in use need to stay alive.
		current = ""
		if prefix, err := r.commands.ResolveCommand(ctx, protocol.HarnessCodex); err == nil {
			current = prefix.Key()
		}
	}
	used := make(map[*Manager]bool)
	for _, launch := range r.owners {
		used[launch.manager] = true
	}
	var retired []*Manager
	for key, manager := range r.managers {
		if !used[manager] && r.modelReaders[manager] == 0 && (manager == failedManager || key != current) {
			delete(r.managers, key)
			retired = append(retired, manager)
		}
	}
	return retired
}

func (r *Runtime) closeManagers(managers []*Manager) error {
	var err error
	for _, manager := range managers {
		err = errors.Join(err, manager.Close())
	}
	r.mu.Lock()
	r.cleanupErr = errors.Join(r.cleanupErr, err)
	r.mu.Unlock()
	return err
}

func (r *Runtime) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		var managers []*Manager
		for _, manager := range r.managers {
			managers = append(managers, manager)
		}
		r.mu.Unlock()
		r.closeManagers(managers)
		r.operations.Wait()
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cleanupErr
}
