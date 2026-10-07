package codex

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/holark-ai/holark/internal/commandprefix"
	"github.com/holark-ai/holark/internal/harness/cliprobe"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/terminalenv"
	"github.com/holark-ai/holark/internal/terminalhost"
)

// Manager owns one server for an immutable command prefix. Credentials are never placed in
// command arguments, diagnostics, or the environment inherited by tool threads.
type Manager struct {
	prefix                      commandprefix.Prefix
	discovery                   cliprobe.Discovery
	processRegistry             *terminalhost.ProcessRegistry
	terminalContext             terminalenv.Context
	shutdown                    chan struct{}
	closeOnce                   sync.Once
	closeErr                    error
	mu                          sync.Mutex
	ctx                         context.Context
	cancel                      context.CancelFunc
	root, url, token, bridgeURL string
	processOwner                string
	server                      *http.Server
	process                     *exec.Cmd
	done                        chan struct{}
	processCleanup              *error
	cleanupErr                  error
	launches                    map[string]*launchBinding
	closed                      bool
	generation                  uint64
}
type Observation struct {
	Failed        bool
	Acknowledge   func(error)
	Identity      string
	TurnID        string
	Activity      protocol.AgentActivity
	ContextTokens *int64
	Input         protocol.InputState
	Status        protocol.ObservabilityStatus
	Message       string
}
type launchBinding struct {
	// Reduction remains ordered while persistence callbacks release mu. Stop
	// takes only mu, so a callback may synchronously stop its own agent.
	reductionMu            sync.Mutex
	profile                string
	mu                     sync.Mutex
	manager                *Manager
	ctx                    context.Context
	cancel                 context.CancelFunc
	agent, cwd, token      string
	generation             uint64
	env                    map[string]string
	reducer                *Reducer
	observer               *rpcConn
	native                 *rpcConn
	sink                   func(Observation)
	ready                  chan struct{}
	serverDone             <-chan struct{}
	serverURL, serverToken string
	stopping               bool
	connected              bool
	bound                  bool
	connection             uint64
}

func NewManager() *Manager {
	return NewManagerWithContextAndProcessRegistry(terminalenv.Context{}, nil)
}

// NewManagerWithContext applies the composition discovery context to the
// app-server and its tools, independently of the remote terminal environment.
func NewManagerWithContext(terminalContext terminalenv.Context) *Manager {
	return NewManagerWithContextAndProcessRegistry(terminalContext, nil)
}

func NewManagerWithProcessRegistry(registry *terminalhost.ProcessRegistry) *Manager {
	return NewManagerWithContextAndProcessRegistry(terminalenv.Context{}, registry)
}

func NewManagerWithContextAndProcessRegistry(terminalContext terminalenv.Context, registry *terminalhost.ProcessRegistry) *Manager {
	return NewManagerWithOptions(ManagerOptions{TerminalContext: terminalContext, ProcessRegistry: registry})
}

type ManagerOptions struct {
	Prefix          commandprefix.Prefix
	TerminalContext terminalenv.Context
	ProcessRegistry *terminalhost.ProcessRegistry
	Discovery       cliprobe.Discovery
}

func NewManagerWithOptions(options ManagerOptions) *Manager {
	if options.Prefix.Executable() == "" {
		options.Prefix = commandprefix.New("codex")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{prefix: options.Prefix, discovery: options.Discovery, processRegistry: options.ProcessRegistry, terminalContext: options.TerminalContext, shutdown: make(chan struct{}), ctx: ctx, cancel: cancel, launches: map[string]*launchBinding{}}
}
func credential() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func (m *Manager) startLocked(ctx context.Context) (result error) {
	if m.closed {
		return errors.New("Codex manager is closed")
	}
	if m.process != nil {
		select {
		case <-m.done:
			m.cleanupErr = errors.Join(m.cleanupErr, *m.processCleanup)
			m.process = nil
		default:
			return nil
		}
	}
	if probe := ProbeWithOptions(ctx, m.prefix.Executable(), cliprobe.Options{Discovery: m.discovery, Prefix: &m.prefix}); !probe.Available {
		return errors.New(probe.Reason)
	}
	root, err := os.MkdirTemp("", "holark-codex-")
	if err != nil {
		return err
	}
	token, err := credential()
	if err != nil {
		os.RemoveAll(root)
		return err
	}
	owner, err := credential()
	if err != nil {
		os.RemoveAll(root)
		return err
	}
	tokenFile := filepath.Join(root, "credential")
	if err = os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		os.RemoveAll(root)
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.RemoveAll(root)
		return err
	}
	url := "ws://" + listener.Addr().String()
	listener.Close()
	cmd := m.prefix.Command("app-server", "--listen", url, "--ws-auth", "capability-token", "--ws-token-file", tokenFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Nil output streams connect directly to the null device. io.Discard would
	// create pipes that a wrapper's descendants can hold open, blocking Wait
	// before the descendant cleanup below gets a chance to run.
	cmd.Env = m.terminalContext.Environment(terminalenv.HarnessEnvironment(os.Environ(), "", "", ""))
	cmd.Env = append(cmd.Env, terminalhost.ProcessOwnerEnvironment+"="+owner)
	if m.processRegistry != nil {
		owner, err = m.processRegistry.Prepare(cmd)
		if err != nil {
			os.RemoveAll(root)
			return err
		}
	}
	if err = cmd.Start(); err != nil {
		os.RemoveAll(root)
		return errors.New("Codex app-server startup failed")
	}
	if err = m.processRegistry.Started(owner, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		os.RemoveAll(root)
		return err
	}
	done := make(chan struct{})
	cleanupErr := new(error)
	go func() {
		_ = cmd.Wait()
		*cleanupErr = errors.Join(terminalhost.ReapProcessSession(cmd.Process.Pid), terminalhost.ReapOwnedProcesses(owner))
		if *cleanupErr != nil {
			slog.Error("Codex owned-process cleanup failed", "error", *cleanupErr)
		}
		close(done)
	}()
	success := false
	defer func() {
		if !success {
			_ = cmd.Process.Kill()
			<-done
			result = errors.Join(result, *cleanupErr, os.RemoveAll(root))
		}
	}()
	deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		c, e := connectRPC(deadline, url, token)
		if e == nil {
			e = c.initialize(deadline)
			c.close()
			if e == nil {
				break
			}
		}
		select {
		case <-done:
			return errors.New("Codex app-server exited during startup")
		case <-deadline.Done():
			return errors.New("Codex app-server readiness timed out")
		case <-m.shutdown:
			return errors.New("Codex application is shutting down")
		case <-time.After(50 * time.Millisecond):
		}
	}
	if m.server == nil {
		bridge, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			return e
		}
		m.bridgeURL = "ws://" + bridge.Addr().String()
		m.server = &http.Server{Handler: http.HandlerFunc(m.serve), ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = m.server.Serve(bridge) }()
	}
	if m.root != "" {
		_ = os.RemoveAll(m.root)
	}
	m.root = root
	m.url = url
	m.token = token
	m.processOwner = owner
	m.process = cmd
	m.done = done
	m.processCleanup = cleanupErr
	success = true
	return nil
}

// Prepare binds the credentials to the persisted destination and terminal ID.
// That terminal ID is also the launch generation used by the persistence adapter.
func (m *Manager) Prepare(ctx context.Context, cmd *exec.Cmd, session protocol.HarnessSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.startLocked(ctx); err != nil {
		return err
	}
	if session.ResumeTarget != "" {
		observer, err := connectRPC(ctx, m.url, m.token)
		if err != nil {
			return err
		}
		err = observer.initialize(ctx)
		if err == nil {
			err = observer.call(ctx, "thread/read", map[string]any{"threadId": session.ResumeTarget, "includeTurns": false}, nil)
		}
		observer.close()
		if err != nil {
			return errors.New("Stored Codex thread is unavailable; its identity has been retained")
		}
	}
	token, err := credential()
	if err != nil {
		return err
	}
	m.generation++
	lifetime, cancel := context.WithCancel(m.ctx)
	b := &launchBinding{manager: m, ctx: lifetime, cancel: cancel, agent: session.ID, cwd: cmd.Dir, token: token, generation: m.generation, env: map[string]string{}, ready: make(chan struct{})}
	for i, arg := range cmd.Args {
		if arg == "--profile" && i+1 < len(cmd.Args) {
			b.profile = cmd.Args[i+1]
		}
	}
	b.reducer = NewReducer(session.ResumeTarget, b.generation, session.InputState)
	b.serverDone = m.done
	b.serverURL, b.serverToken = m.url, m.token
	cmd.Env = terminalenv.HarnessEnvironment(cmd.Env, session.SessionID, session.ID, cmd.Dir)
	for _, entry := range cmd.Env {
		name, value, ok := cutEnv(entry)
		if ok {
			b.env[name] = value
		}
	}
	// Only launch-specific values override tool environment. Account/config and
	// PATH remain those resolved by Codex's effective thread configuration,
	// with discovery context applied when configuring shell and MCP tools.
	for name := range b.env {
		if !isHolarkEnvironment(name) {
			delete(b.env, name)
		}
	}
	b.env[terminalhost.ProcessOwnerEnvironment] = m.processOwner
	cmd.Env = append(cmd.Env, "HOLARK_CODEX_BRIDGE_TOKEN="+token)
	offset := 1 + len(m.prefix.Arguments())
	args := append([]string(nil), cmd.Args[:offset]...)
	args = append(args, "--remote", m.bridgeURL, "--remote-auth-token-env", "HOLARK_CODEX_BRIDGE_TOKEN")
	cmd.Args = append(args, cmd.Args[offset:]...)
	m.launches[session.TerminalID] = b
	return nil
}
func (m *Manager) Monitor(ctx context.Context, generationID string, sink func(Observation)) {
	m.mu.Lock()
	b := m.launches[generationID]
	m.mu.Unlock()
	if b == nil {
		sink(Observation{Status: protocol.ObservabilityDegraded, Message: "Stop and resume this Codex agent to use app-server monitoring."})
		return
	}
	b.mu.Lock()
	b.sink = sink
	close(b.ready)
	b.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-b.ctx.Done():
	case <-b.serverDone:
		b.cancel()
		b.reductionMu.Lock()
		sink(Observation{Failed: true, Input: protocol.InputNone, Status: protocol.ObservabilityDegraded, Message: "Codex app-server exited; resume this agent to continue its stored thread."})
		b.reductionMu.Unlock()
	}
}
func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		close(m.shutdown)
		m.mu.Lock()
		m.closed = true
		ids := make([]string, 0, len(m.launches))
		for id := range m.launches {
			ids = append(ids, id)
		}
		server, process, done, root := m.server, m.process, m.done, m.root
		cleanup := m.processCleanup
		m.closeErr = m.cleanupErr
		m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, id := range ids {
			_ = m.Stop(ctx, id)
		}
		m.cancel()
		if server != nil {
			_ = server.Close()
		}
		if process != nil {
			_ = process.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				_ = process.Process.Kill()
				<-done
			}
			m.closeErr = errors.Join(m.closeErr, *cleanup)
		}
		if root != "" {
			m.closeErr = errors.Join(m.closeErr, os.RemoveAll(root))
		}
	})
	return m.closeErr
}

func (m *Manager) Stop(ctx context.Context, generationID string) error {
	ctx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	m.mu.Lock()
	b := m.launches[generationID]
	delete(m.launches, generationID)
	m.mu.Unlock()
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.stopping = true
	observer := b.observer
	native := b.native
	thread := b.reducer.ThreadID
	b.mu.Unlock()
	if b.ctx.Err() != nil {
		if native != nil {
			native.close()
		}
		if observer != nil {
			observer.close()
		}
		return nil
	}
	// Unsubscribe only removes event delivery; Codex keeps unloaded candidates
	// alive for a grace period. Stop execution explicitly before releasing them.
	var err error
	// A dedicated connection also works while the native client has quit or the
	// observer is reconnecting. Use this launch's server, never a replacement.
	if thread != "" {
		control, connectErr := connectRPC(ctx, b.serverURL, b.serverToken)
		err = connectErr
		if connectErr == nil {
			defer control.close()
			err = control.initialize(ctx)
		}
		if err == nil {
			var snapshot threadResponse
			readErr := control.call(ctx, "thread/read", map[string]any{"threadId": thread, "includeTurns": true}, &snapshot)
			err = errors.Join(err, readErr)
			if readErr == nil {
				for _, turn := range snapshot.Thread.Turns {
					if turn.Status == "inProgress" {
						err = errors.Join(err, control.call(ctx, "turn/interrupt", map[string]string{"threadId": thread, "turnId": turn.ID}, nil))
					}
				}
			}
			err = errors.Join(err, control.call(ctx, "thread/backgroundTerminals/clean", map[string]string{"threadId": thread}, nil))
		}
	}
	if native != nil {
		if thread != "" {
			_ = native.call(ctx, "thread/unsubscribe", map[string]string{"threadId": thread}, nil)
		}
		native.close()
	}
	if observer != nil && thread != "" {
		err = errors.Join(err, observer.call(ctx, "thread/unsubscribe", map[string]string{"threadId": thread}, nil))
	}
	b.cancel()
	if observer != nil {
		observer.close()
	}
	return err
}
func (m *Manager) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	var b *launchBinding
	provided := r.Header.Get("Authorization")
	for _, candidate := range m.launches {
		if subtle.ConstantTimeCompare([]byte(provided), []byte("Bearer "+candidate.token)) == 1 {
			b = candidate
			break
		}
	}
	url, token := m.url, m.token
	m.mu.Unlock()
	if b == nil || b.ctx.Err() != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	b.mu.Lock()
	if b.connected || b.stopping {
		b.mu.Unlock()
		http.Error(w, "Connection already active", http.StatusConflict)
		return
	}
	b.connected = true
	b.mu.Unlock()
	defer func() { b.mu.Lock(); b.connected = false; b.mu.Unlock() }()
	down, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer down.CloseNow()
	down.SetReadLimit(32 << 20)
	up, err := connectRPC(b.ctx, url, token)
	if err != nil {
		return
	}
	defer up.close()
	b.mu.Lock()
	b.native = up
	b.mu.Unlock()
	b.bridge(down, up, url, token)
}
