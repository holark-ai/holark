// Package localadapter runs one private VS Code serve-web process per IDE.
package localadapter

import (
	"context"
	"errors"
	"fmt"
	"github.com/holark-ai/holark/internal/ide"
	"github.com/holark-ai/holark/internal/ideinstall"
	"github.com/holark-ai/holark/internal/ideworkbench"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxDiagnostics = 8192
const maxLogBytes = 1 << 20

type Options struct {
	ConfiguredExecutable, DataDirectory string
	ReadinessTimeout, ShutdownTimeout   time.Duration
	Resolver                            func(context.Context, ideinstall.Options) (ideinstall.Result, error)
}
type process struct {
	cmd         *exec.Cmd
	target      string
	done        chan struct{}
	log         *os.File
	diagnostics *buffer
}
type Runtime struct {
	options   Options
	mu        sync.Mutex
	processes map[string]*process
	closed    bool
}

func New(o Options) *Runtime {
	if o.ReadinessTimeout <= 0 {
		o.ReadinessTimeout = 30 * time.Second
	}
	if o.ShutdownTimeout <= 0 {
		o.ShutdownTimeout = 5 * time.Second
	}
	if o.Resolver == nil {
		o.Resolver = ideinstall.Resolve
	}
	return &Runtime{options: o, processes: map[string]*process{}}
}

func (r *Runtime) Start(ctx context.Context, s ide.Start) (startErr error) {
	if s.IDEID == "" || s.WorktreePath == "" || s.BasePath == "" {
		return ide.ErrInvalid
	}
	instance := filepath.Dir(r.LogPath(s.IDEID))
	if e := os.MkdirAll(instance, 0700); e != nil {
		return e
	}
	logFile, e := os.OpenFile(r.LogPath(s.IDEID), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	logWriter := &limitedFile{file: logFile, maximum: maxLogBytes}
	logEvent := func(format string, args ...any) {
		fmt.Fprintf(logWriter, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
	}
	startDone := make(chan struct{})
	processOwnsLog := false
	defer func() {
		if startErr != nil {
			logEvent("IDE startup failed: %v", startErr)
		}
		close(startDone)
		if !processOwnsLog {
			_ = logFile.Close()
		}
	}()
	logEvent("Starting IDE %s for worktree %s", s.IDEID, s.WorktreePath)
	info, e := os.Stat(s.WorktreePath)
	if e != nil || !info.IsDir() {
		return errors.New("requested worktree is unavailable")
	}
	// Resolution (including managed download) happens only in this explicit Open path.
	resolved, e := r.options.Resolver(ctx, ideinstall.Options{ConfiguredPath: r.options.ConfiguredExecutable, DataDir: r.options.DataDirectory, Log: logWriter})
	if e != nil {
		return fmt.Errorf("VS Code could not be provisioned: %w", e)
	}
	logEvent("Using VS Code %s (%s): %s", resolved.Version, resolved.Source, resolved.Executable)
	port, e := privatePort()
	if e != nil {
		return e
	}
	args := []string{"serve-web", "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--server-base-path", s.BasePath, "--default-folder", s.WorktreePath, "--server-data-dir", filepath.Join(r.options.DataDirectory, "profile"), "--without-connection-token", "--accept-server-license-terms", "--disable-telemetry"}
	cmd := exec.Command(resolved.Executable, args...)
	cmd.Dir = s.WorktreePath
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(), "DO_NOT_TRACK=1", "VSCODE_TELEMETRY_LEVEL=off")
	diagnostics := &buffer{max: maxDiagnostics}
	cmd.Stdout = io.MultiWriter(logWriter, diagnostics)
	cmd.Stderr = cmd.Stdout
	p := &process{cmd: cmd, target: "http://127.0.0.1:" + strconv.Itoa(port), done: make(chan struct{}), log: logFile, diagnostics: diagnostics}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("IDE runtime is closed")
	}
	if _, ok := r.processes[s.IDEID]; ok {
		r.mu.Unlock()
		return errors.New("IDE already running")
	}
	if e = cmd.Start(); e != nil {
		r.mu.Unlock()
		return fmt.Errorf("start VS Code: %w: %s", e, diagnostics.String())
	}
	processOwnsLog = true
	r.processes[s.IDEID] = p
	r.mu.Unlock()
	go func() {
		err := cmd.Wait()
		logEvent("VS Code process exited: %v", err)
		close(p.done)
		r.forget(s.IDEID, p)
		// Startup can still be recording a failure after the process exits.
		<-startDone
		_ = logFile.Close()
	}()
	logEvent("VS Code process started (PID %d)", cmd.Process.Pid)
	endpoint := p.target + strings.TrimRight(s.BasePath, "/") + "/"
	deadline, cancel := context.WithTimeout(ctx, r.options.ReadinessTimeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, _ := http.NewRequestWithContext(deadline, http.MethodGet, endpoint, nil)
		response, requestErr := (&http.Client{Timeout: time.Second}).Do(request)
		if requestErr == nil && ready(response) {
			logEvent("VS Code workbench ready")
			return nil
		}
		select {
		case <-p.done:
			r.forget(s.IDEID, p)
			return fmt.Errorf("VS Code exited before readiness: %s", diagnostics.String())
		case <-deadline.Done():
			_ = r.stop(s.IDEID, p)
			return fmt.Errorf("VS Code did not become ready in time: %s", diagnostics.String())
		case <-ticker.C:
		}
	}
}
func (r *Runtime) LogPath(id string) string {
	return filepath.Join(r.options.DataDirectory, "runtimes", id, "serve-web.log")
}

func ready(response *http.Response) bool {
	if response == nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 || !ideworkbench.IsHTMLContentType(response.Header.Get("Content-Type")) {
		return false
	}
	body, e := io.ReadAll(io.LimitReader(response.Body, ideworkbench.MaxHTMLBytes+1))
	return e == nil && len(body) <= ideworkbench.MaxHTMLBytes && ideworkbench.Validate(body) == nil
}
func (r *Runtime) Target(id string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.processes[id]
	if !ok {
		return "", false
	}
	select {
	case <-p.done:
		return "", false
	default:
		return p.target, true
	}
}
func (r *Runtime) Stop(ctx context.Context, id string) error {
	r.mu.Lock()
	p := r.processes[id]
	r.mu.Unlock()
	if p == nil {
		return nil
	}
	return r.stop(id, p)
}
func (r *Runtime) stop(id string, p *process) error {
	select {
	case <-p.done:
		r.forget(id, p)
		return nil
	default:
	}
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	}
	timer := time.NewTimer(r.options.ShutdownTimeout)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		if p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
		<-p.done
	}
	r.forget(id, p)
	return nil
}
func (r *Runtime) forget(id string, p *process) {
	r.mu.Lock()
	if r.processes[id] == p {
		delete(r.processes, id)
	}
	r.mu.Unlock()
}
func (r *Runtime) Close() error {
	r.mu.Lock()
	r.closed = true
	all := make(map[string]*process, len(r.processes))
	for id, p := range r.processes {
		all[id] = p
	}
	r.mu.Unlock()
	for id, p := range all {
		_ = r.stop(id, p)
	}
	return nil
}
func privatePort() (int, error) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return 0, e
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

type buffer struct {
	mu   sync.Mutex
	data []byte
	max  int
}
type limitedFile struct {
	mu      sync.Mutex
	file    *os.File
	written int
	maximum int
}

func (w *limitedFile) Write(v []byte) (int, error) {
	original := len(v)
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.maximum - w.written
	if remaining <= 0 {
		return original, nil
	}
	if len(v) > remaining {
		v = v[:remaining]
	}
	n, e := w.file.Write(v)
	w.written += n
	if e != nil {
		return n, e
	}
	return original, nil
}

func (b *buffer) Write(v []byte) (int, error) {
	n := len(v)
	b.mu.Lock()
	defer b.mu.Unlock()
	left := b.max - len(b.data)
	if left > 0 {
		if len(v) > left {
			v = v[:left]
		}
		b.data = append(b.data, v...)
	}
	return n, nil
}
func (b *buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.data))
}
