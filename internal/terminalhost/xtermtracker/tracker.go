// Package xtermtracker adapts @xterm/headless to the terminal domain's
// bounded ScreenTracker boundary.
package xtermtracker

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/holark-ai/holark/internal/terminals"
)

const workerScript = "worker.mjs"
const defaultOperationTimeout = 2 * time.Second

type Factory struct {
	WorkerDir          string
	ScrollbackLines    int
	MaximumReplayBytes int
}

func (factory Factory) New(id terminals.TerminalID, dimensions terminals.Dimensions) (terminals.ScreenTracker, error) {
	lines := factory.ScrollbackLines
	if lines == 0 {
		lines = terminals.DefaultScrollbackLines
	}
	limit := factory.MaximumReplayBytes
	if limit == 0 {
		limit = terminals.DefaultCheckpointReplayBytes
	}
	return New(id, dimensions, Options{
		WorkerDir: factory.WorkerDir, ScrollbackLines: lines, MaximumReplayBytes: limit,
	})
}

type Options struct {
	WorkerDir          string
	ScrollbackLines    int
	MaximumReplayBytes int
	OperationTimeout   time.Duration
}

type Tracker struct {
	id                 terminals.TerminalID
	dimensions         terminals.Dimensions
	maximumReplayBytes int
	operationTimeout   time.Duration
	lastSequence       uint64
	resetSequence      uint64

	mu     sync.Mutex
	nextID uint64
	closed bool
	failed bool
	waited bool
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *lockedBuffer
}

func New(id terminals.TerminalID, dimensions terminals.Dimensions, options Options) (*Tracker, error) {
	if !id.Valid() || dimensions.Validate() != nil {
		return nil, errors.New("invalid xterm screen tracker configuration")
	}
	if options.ScrollbackLines == 0 {
		options.ScrollbackLines = terminals.DefaultScrollbackLines
	}
	if options.MaximumReplayBytes == 0 {
		options.MaximumReplayBytes = terminals.DefaultCheckpointReplayBytes
	}
	if options.OperationTimeout == 0 {
		options.OperationTimeout = defaultOperationTimeout
	}
	if options.ScrollbackLines < 1 || options.ScrollbackLines > 100_000 || options.MaximumReplayBytes < 1 {
		return nil, errors.New("invalid xterm screen tracker limits")
	}
	workerDirectory, err := findWorkerDir(options.WorkerDir)
	if err != nil {
		return nil, err
	}
	command := exec.Command("node", filepath.Join(workerDirectory, workerScript))
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, err
	}
	tracker := &Tracker{
		id: id, dimensions: dimensions,
		maximumReplayBytes: options.MaximumReplayBytes, operationTimeout: options.OperationTimeout, cmd: command, stdin: stdin,
		stdout: bufio.NewReader(stdout), stderr: &lockedBuffer{},
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start xterm worker: %w", err)
	}
	go func() { _, _ = io.Copy(tracker.stderr, stderr) }()
	if _, err := tracker.call(map[string]any{
		"command": "init", "columns": dimensions.Columns, "rows": dimensions.Rows,
		"scrollback": options.ScrollbackLines, "maximum_replay_bytes": options.MaximumReplayBytes,
	}); err != nil {
		_ = tracker.Close()
		return nil, fmt.Errorf("initialize xterm worker: %w", err)
	}
	return tracker, nil
}

func (tracker *Tracker) Write(sequence uint64, data []byte) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if sequence <= tracker.lastSequence || len(data) == 0 {
		return errors.New("invalid xterm screen write sequence")
	}
	response, err := tracker.callLocked(map[string]any{
		"command": "write", "sequence": sequence, "data": base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		return err
	}
	tracker.captureResetSequence(response)
	tracker.lastSequence = sequence
	return nil
}

func (tracker *Tracker) Resize(sequence uint64, dimensions terminals.Dimensions) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if sequence <= tracker.lastSequence || dimensions.Validate() != nil {
		return errors.New("invalid xterm screen resize sequence")
	}
	response, err := tracker.callLocked(map[string]any{
		"command": "resize", "sequence": sequence, "columns": dimensions.Columns, "rows": dimensions.Rows,
	})
	if err != nil {
		return err
	}
	tracker.captureResetSequence(response)
	tracker.dimensions = dimensions
	tracker.lastSequence = sequence
	return nil
}

func (tracker *Tracker) Snapshot(sequence uint64) (terminals.TerminalCheckpoint, error) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if sequence != tracker.lastSequence {
		return terminals.TerminalCheckpoint{}, errors.New("xterm snapshot sequence is not at the write barrier")
	}
	response, err := tracker.callLocked(map[string]any{"command": "snapshot"})
	if err != nil {
		return terminals.TerminalCheckpoint{}, err
	}
	tracker.captureResetSequence(response)
	encoded, ok := response["data"].(string)
	if !ok {
		return terminals.TerminalCheckpoint{}, errors.New("xterm worker returned an invalid snapshot")
	}
	payload, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return terminals.TerminalCheckpoint{}, fmt.Errorf("decode xterm snapshot: %w", err)
	}
	if len(payload) > tracker.maximumReplayBytes {
		return terminals.TerminalCheckpoint{}, errors.New("xterm snapshot exceeds the bounded replay payload")
	}
	return terminals.NewCheckpoint(tracker.id, sequence, tracker.dimensions, payload), nil
}

func (tracker *Tracker) ResetSequence() uint64 {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.resetSequence
}

func (tracker *Tracker) WorkerSlot() string {
	if tracker.cmd.Process == nil {
		return "legacy"
	}
	return fmt.Sprintf("legacy-pid-%d", tracker.cmd.Process.Pid)
}

func (tracker *Tracker) captureResetSequence(response map[string]any) {
	value, ok := response["reset_sequence"].(float64)
	if ok && value >= 0 && value <= float64(^uint64(0)) {
		tracker.resetSequence = max(tracker.resetSequence, uint64(value))
	}
}

func (tracker *Tracker) Close() error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.closed {
		return nil
	}
	var callErr error
	if !tracker.failed {
		_, callErr = tracker.callLocked(map[string]any{"command": "close"})
	}
	tracker.closed = true
	_ = tracker.stdin.Close()
	if tracker.failed && tracker.cmd.Process != nil {
		_ = tracker.cmd.Process.Kill()
	}
	var waitErr error
	if !tracker.waited {
		waitErr = tracker.cmd.Wait()
		tracker.waited = true
	}
	if callErr != nil {
		return callErr
	}
	return waitErr
}

func (tracker *Tracker) call(request map[string]any) (map[string]any, error) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.callLocked(request)
}

func (tracker *Tracker) callLocked(request map[string]any) (map[string]any, error) {
	if tracker.closed || tracker.failed {
		return nil, errors.New("xterm tracker is closed")
	}
	tracker.nextID++
	request["id"] = tracker.nextID
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')
	type exchangeResult struct {
		line []byte
		err  error
	}
	completed := make(chan exchangeResult, 1)
	go func() {
		if _, err := tracker.stdin.Write(payload); err != nil {
			completed <- exchangeResult{err: fmt.Errorf("write xterm worker request: %w%s", err, tracker.stderrSuffix())}
			return
		}
		line, err := tracker.stdout.ReadBytes('\n')
		if err != nil {
			completed <- exchangeResult{err: fmt.Errorf("read xterm worker response: %w%s", err, tracker.stderrSuffix())}
			return
		}
		completed <- exchangeResult{line: line}
	}()
	var exchange exchangeResult
	timer := time.NewTimer(tracker.operationTimeout)
	select {
	case exchange = <-completed:
		timer.Stop()
	case <-timer.C:
		tracker.failed = true
		if tracker.cmd.Process != nil {
			_ = tracker.cmd.Process.Kill()
		}
		return nil, errors.New("xterm worker operation timed out")
	}
	if exchange.err != nil {
		tracker.failLocked()
		return nil, exchange.err
	}
	line := exchange.line
	var response struct {
		ID            uint64 `json:"id"`
		OK            bool   `json:"ok"`
		Error         string `json:"error"`
		ErrorCategory string `json:"error_category"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		tracker.failLocked()
		return nil, fmt.Errorf("decode xterm worker response: %w", err)
	}
	if response.ID != tracker.nextID {
		tracker.failLocked()
		return nil, errors.New("xterm worker response id mismatch")
	}
	if !response.OK {
		if response.Error == "" {
			response.Error = "xterm worker command failed"
		}
		if response.ErrorCategory == "snapshot_unavailable" {
			return nil, fmt.Errorf("%w: %s", terminals.ErrCheckpointUnavailable, response.Error)
		}
		return nil, errors.New(response.Error)
	}
	var fields map[string]any
	if err := json.Unmarshal(line, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func (tracker *Tracker) failLocked() {
	tracker.failed = true
	if tracker.cmd.Process != nil {
		_ = tracker.cmd.Process.Kill()
	}
}

func (tracker *Tracker) stderrSuffix() string {
	output := tracker.stderr.String()
	if output == "" {
		return ""
	}
	return ": " + output
}

func findWorkerDir(configured string) (string, error) {
	candidates := make([]string, 0)
	if value := os.Getenv("HOLARK_XTERM_WORKER_DIR"); value != "" {
		candidates = append(candidates, value)
	}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "..", "terminal-worker"))
	}
	if configured != "" {
		candidates = append(candidates, configured)
	}
	if workingDirectory, err := os.Getwd(); err == nil {
		for directory := workingDirectory; ; directory = filepath.Dir(directory) {
			candidates = append(candidates, filepath.Join(directory, "terminal-worker"))
			parent := filepath.Dir(directory)
			if parent == directory {
				break
			}
		}
	}
	_, source, _, ok := runtime.Caller(0)
	if ok {
		candidates = append(candidates, filepath.Join(filepath.Dir(source), "..", "..", "..", "terminal-worker"))
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		workerPath := filepath.Join(candidate, workerScript)
		if info, err := os.Stat(workerPath); err == nil && !info.IsDir() {
			return filepath.Clean(candidate), nil
		}
	}
	return "", errors.New("xterm worker directory not found")
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.buffer.Len() > 8192 {
		buffer.buffer.Reset()
	}
	return buffer.buffer.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}
