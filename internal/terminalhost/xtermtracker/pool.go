package xtermtracker

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/holark-ai/holark/internal/terminals"
)

const (
	defaultIdleTimeout = 30 * time.Second
	maximumWriteBytes  = 64 * 1024
)

type PoolOptions struct {
	WorkerDir          string
	ScrollbackLines    int
	MaximumReplayBytes int
	MaximumWorkers     int
	OperationTimeout   time.Duration
	IdleTimeout        time.Duration
}

// Pool is a bounded owner of Node.js workers. Each terminal model keeps a
// stable slot assignment while that worker is healthy, and slot queues are
// serviced round-robin by model identity.
type Pool struct {
	mu      sync.Mutex
	options PoolOptions
	slots   []*workerSlot
	nextID  uint64
	closed  bool
}

type workerSlot struct {
	pool *Pool
	id   uint64

	mu       sync.Mutex
	process  *workerProcess
	queues   map[string][]*poolRequest
	order    []string
	next     int
	models   int
	dead     bool
	stopping bool
	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	idle     *time.Timer
}

type workerProcess struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	stderr  *lockedBuffer
	nextID  uint64
}

type poolRequest struct {
	model  string
	fields map[string]any
	result chan poolResult
}

type poolResult struct {
	fields map[string]any
	err    error
}

type pooledModel struct {
	pool               *Pool
	slot               *workerSlot
	identity           string
	id                 terminals.TerminalID
	dimensions         terminals.Dimensions
	maximumReplayBytes int
	lastSequence       uint64
	resetSequence      uint64
	mu                 sync.Mutex
	closed             bool
}

type PoolStats struct {
	Workers int
	Models  int
	PIDs    []int
}

func NewPool(options PoolOptions) (*Pool, error) {
	if options.ScrollbackLines == 0 {
		options.ScrollbackLines = terminals.DefaultScrollbackLines
	}
	if options.MaximumReplayBytes == 0 {
		options.MaximumReplayBytes = terminals.DefaultCheckpointReplayBytes
	}
	if options.MaximumWorkers == 0 {
		options.MaximumWorkers = min(4, runtime.NumCPU())
	}
	if options.OperationTimeout == 0 {
		options.OperationTimeout = defaultOperationTimeout
	}
	if options.IdleTimeout == 0 {
		options.IdleTimeout = defaultIdleTimeout
	}
	if options.ScrollbackLines < 1 || options.ScrollbackLines > 100_000 || options.MaximumReplayBytes < 1 ||
		options.MaximumWorkers < 1 || options.OperationTimeout <= 0 || options.IdleTimeout <= 0 {
		return nil, errors.New("invalid xterm worker pool configuration")
	}
	return &Pool{options: options}, nil
}

func (pool *Pool) New(id terminals.TerminalID, dimensions terminals.Dimensions) (terminals.ScreenTracker, error) {
	if !id.Valid() || dimensions.Validate() != nil {
		return nil, errors.New("invalid xterm screen model configuration")
	}
	slot, err := pool.assignSlot()
	if err != nil {
		return nil, err
	}
	identity := string(id)
	model := &pooledModel{
		pool: pool, slot: slot, identity: identity, id: id,
		dimensions: dimensions, maximumReplayBytes: pool.options.MaximumReplayBytes,
	}
	if _, err := slot.call(identity, map[string]any{
		"command": "create", "columns": dimensions.Columns, "rows": dimensions.Rows,
		"scrollback": pool.options.ScrollbackLines, "maximum_replay_bytes": pool.options.MaximumReplayBytes,
	}); err != nil {
		pool.releaseSlotModel(slot)
		return nil, workerSlotError{slot: slot.id, err: fmt.Errorf("create xterm model: %w", err)}
	}
	return model, nil
}

func (pool *Pool) assignSlot() (*workerSlot, error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.closed {
		return nil, errors.New("xterm worker pool is closed")
	}
	var live []*workerSlot
	for _, slot := range pool.slots {
		slot.mu.Lock()
		if !slot.dead && !slot.stopping {
			live = append(live, slot)
		}
		slot.mu.Unlock()
	}
	if len(live) < pool.options.MaximumWorkers && (len(live) == 0 || pool.liveModelCountLocked(live) >= len(live)) {
		slot, err := pool.startSlotLocked()
		if err == nil {
			live = append(live, slot)
		} else if len(live) == 0 {
			return nil, err
		}
	}
	if len(live) == 0 {
		return nil, errors.New("no healthy xterm worker is available")
	}
	selected := live[0]
	for _, slot := range live[1:] {
		if slotModelCount(slot) < slotModelCount(selected) {
			selected = slot
		}
	}
	selected.mu.Lock()
	selected.models++
	if selected.idle != nil {
		selected.idle.Stop()
		selected.idle = nil
	}
	selected.mu.Unlock()
	return selected, nil
}

func (pool *Pool) liveModelCountLocked(slots []*workerSlot) int {
	total := 0
	for _, slot := range slots {
		total += slotModelCount(slot)
	}
	return total
}

func slotModelCount(slot *workerSlot) int {
	slot.mu.Lock()
	defer slot.mu.Unlock()
	return slot.models
}

func (pool *Pool) startSlotLocked() (*workerSlot, error) {
	workerDirectory, err := findWorkerDir(pool.options.WorkerDir)
	if err != nil {
		return nil, err
	}
	process, err := startWorkerProcess(filepath.Join(workerDirectory, workerScript))
	if err != nil {
		return nil, err
	}
	pool.nextID++
	slot := &workerSlot{
		pool: pool, id: pool.nextID, process: process, queues: make(map[string][]*poolRequest),
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	pool.slots = append(pool.slots, slot)
	go slot.run()
	return slot, nil
}

func startWorkerProcess(script string) (*workerProcess, error) {
	command := exec.Command("node", script)
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
	process := &workerProcess{command: command, stdin: stdin, stdout: bufio.NewReader(stdout), stderr: &lockedBuffer{}}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start xterm worker: %w", err)
	}
	go func() { _, _ = io.Copy(process.stderr, stderr) }()
	return process, nil
}

func (slot *workerSlot) call(model string, fields map[string]any) (map[string]any, error) {
	request := &poolRequest{model: model, fields: fields, result: make(chan poolResult, 1)}
	slot.mu.Lock()
	if slot.dead || slot.stopping {
		slot.mu.Unlock()
		return nil, errors.New("xterm worker slot is unhealthy")
	}
	if len(slot.queues[model]) == 0 {
		slot.order = append(slot.order, model)
	}
	slot.queues[model] = append(slot.queues[model], request)
	slot.mu.Unlock()
	select {
	case slot.wake <- struct{}{}:
	default:
	}
	result := <-request.result
	return result.fields, result.err
}

func (slot *workerSlot) run() {
	defer close(slot.done)
	for {
		request := slot.nextRequest()
		if request == nil {
			select {
			case <-slot.stop:
				slot.shutdown(nil)
				return
			case <-slot.wake:
				continue
			}
		}
		fields, fatal, err := slot.process.exchange(request.model, request.fields, slot.pool.options.OperationTimeout)
		request.result <- poolResult{fields: fields, err: err}
		if fatal {
			slot.shutdown(err)
			return
		}
	}
}

func (slot *workerSlot) nextRequest() *poolRequest {
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if len(slot.order) == 0 {
		return nil
	}
	if slot.next >= len(slot.order) {
		slot.next = 0
	}
	identity := slot.order[slot.next]
	queue := slot.queues[identity]
	request := queue[0]
	queue = queue[1:]
	if len(queue) == 0 {
		delete(slot.queues, identity)
		slot.order = append(slot.order[:slot.next], slot.order[slot.next+1:]...)
		if slot.next >= len(slot.order) {
			slot.next = 0
		}
	} else {
		slot.queues[identity] = queue
		slot.next = (slot.next + 1) % len(slot.order)
	}
	return request
}

func (process *workerProcess) exchange(model string, fields map[string]any, timeout time.Duration) (map[string]any, bool, error) {
	process.nextID++
	requestID := process.nextID
	request := make(map[string]any, len(fields)+2)
	for key, value := range fields {
		request[key] = value
	}
	request["id"] = requestID
	request["model"] = model
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, false, err
	}
	payload = append(payload, '\n')
	type exchangeResult struct {
		line []byte
		err  error
	}
	completed := make(chan exchangeResult, 1)
	go func() {
		if _, err := process.stdin.Write(payload); err != nil {
			completed <- exchangeResult{err: err}
			return
		}
		line, err := process.stdout.ReadBytes('\n')
		completed <- exchangeResult{line: line, err: err}
	}()
	select {
	case result := <-completed:
		if result.err != nil {
			return nil, true, fmt.Errorf("xterm worker I/O: %w%s", result.err, stderrSuffix(process.stderr))
		}
		var response struct {
			ID            uint64 `json:"id"`
			OK            bool   `json:"ok"`
			Error         string `json:"error"`
			ErrorCategory string `json:"error_category"`
		}
		if err := json.Unmarshal(result.line, &response); err != nil {
			return nil, true, fmt.Errorf("decode xterm worker response: %w", err)
		}
		if response.ID != requestID {
			return nil, true, errors.New("xterm worker response id mismatch")
		}
		if !response.OK {
			if response.Error == "" {
				response.Error = "xterm worker command failed"
			}
			if response.ErrorCategory == "snapshot_unavailable" {
				return nil, false, fmt.Errorf("%w: %s", terminals.ErrCheckpointUnavailable, response.Error)
			}
			return nil, false, errors.New(response.Error)
		}
		var decoded map[string]any
		if err := json.Unmarshal(result.line, &decoded); err != nil {
			return nil, true, err
		}
		return decoded, false, nil
	case <-time.After(timeout):
		return nil, true, errors.New("xterm worker operation timed out")
	}
}

func (slot *workerSlot) shutdown(cause error) {
	slot.mu.Lock()
	if slot.dead {
		slot.mu.Unlock()
		return
	}
	slot.dead = true
	pending := make([]*poolRequest, 0)
	for _, queue := range slot.queues {
		pending = append(pending, queue...)
	}
	slot.queues = make(map[string][]*poolRequest)
	slot.order = nil
	slot.mu.Unlock()
	_ = slot.process.stdin.Close()
	if slot.process.command.Process != nil {
		_ = slot.process.command.Process.Kill()
	}
	_ = slot.process.command.Wait()
	if cause == nil {
		cause = errors.New("xterm worker slot stopped")
	}
	for _, request := range pending {
		request.result <- poolResult{err: cause}
	}
	slot.pool.removeSlot(slot)
}

func (pool *Pool) removeSlot(target *workerSlot) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	for index, slot := range pool.slots {
		if slot == target {
			pool.slots = append(pool.slots[:index], pool.slots[index+1:]...)
			return
		}
	}
}

func (pool *Pool) releaseSlotModel(slot *workerSlot) {
	slot.mu.Lock()
	if slot.models > 0 {
		slot.models--
	}
	if slot.models == 0 && !slot.dead && !slot.stopping && slot.idle == nil {
		slot.idle = time.AfterFunc(pool.options.IdleTimeout, func() { pool.stopIdleSlot(slot) })
	}
	slot.mu.Unlock()
}

func (pool *Pool) stopIdleSlot(slot *workerSlot) {
	slot.mu.Lock()
	if slot.models != 0 || slot.dead || slot.stopping {
		slot.idle = nil
		slot.mu.Unlock()
		return
	}
	slot.stopping = true
	close(slot.stop)
	slot.mu.Unlock()
	<-slot.done
}

func (model *pooledModel) Write(sequence uint64, data []byte) error {
	model.mu.Lock()
	defer model.mu.Unlock()
	if model.closed || sequence <= model.lastSequence || len(data) == 0 || len(data) > maximumWriteBytes {
		return errors.New("invalid xterm screen write sequence")
	}
	response, err := model.slot.call(model.identity, map[string]any{
		"command": "write", "sequence": sequence, "data": base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		return err
	}
	model.captureResetSequence(response)
	model.lastSequence = sequence
	return nil
}

func (model *pooledModel) Resize(sequence uint64, dimensions terminals.Dimensions) error {
	model.mu.Lock()
	defer model.mu.Unlock()
	if model.closed || sequence <= model.lastSequence || dimensions.Validate() != nil {
		return errors.New("invalid xterm screen resize sequence")
	}
	response, err := model.slot.call(model.identity, map[string]any{
		"command": "resize", "sequence": sequence, "columns": dimensions.Columns, "rows": dimensions.Rows,
	})
	if err != nil {
		return err
	}
	model.captureResetSequence(response)
	model.dimensions = dimensions
	model.lastSequence = sequence
	return nil
}

func (model *pooledModel) Snapshot(sequence uint64) (terminals.TerminalCheckpoint, error) {
	model.mu.Lock()
	defer model.mu.Unlock()
	if model.closed || sequence != model.lastSequence {
		return terminals.TerminalCheckpoint{}, errors.New("xterm snapshot sequence is not at the write barrier")
	}
	response, err := model.slot.call(model.identity, map[string]any{"command": "snapshot"})
	if err != nil {
		return terminals.TerminalCheckpoint{}, err
	}
	model.captureResetSequence(response)
	encoded, ok := response["data"].(string)
	if !ok {
		return terminals.TerminalCheckpoint{}, errors.New("xterm worker returned an invalid snapshot")
	}
	payload, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return terminals.TerminalCheckpoint{}, fmt.Errorf("decode xterm snapshot: %w", err)
	}
	if len(payload) > model.maximumReplayBytes {
		return terminals.TerminalCheckpoint{}, errors.New("xterm snapshot exceeds the bounded replay payload")
	}
	return terminals.NewCheckpoint(model.id, sequence, model.dimensions, payload), nil
}

func (model *pooledModel) ResetSequence() uint64 {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.resetSequence
}

func (model *pooledModel) WorkerSlot() string {
	return fmt.Sprintf("%d", model.slot.id)
}

func (model *pooledModel) captureResetSequence(response map[string]any) {
	value, ok := response["reset_sequence"].(float64)
	if ok && value >= 0 && value <= float64(^uint64(0)) {
		model.resetSequence = max(model.resetSequence, uint64(value))
	}
}

func (model *pooledModel) Close() error {
	model.mu.Lock()
	if model.closed {
		model.mu.Unlock()
		return nil
	}
	model.closed = true
	_, err := model.slot.call(model.identity, map[string]any{"command": "destroy"})
	model.mu.Unlock()
	model.pool.releaseSlotModel(model.slot)
	return err
}

func (pool *Pool) Stats() PoolStats {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	stats := PoolStats{}
	for _, slot := range pool.slots {
		slot.mu.Lock()
		if !slot.dead && !slot.stopping {
			stats.Workers++
			stats.Models += slot.models
			if slot.process.command.Process != nil {
				stats.PIDs = append(stats.PIDs, slot.process.command.Process.Pid)
			}
		}
		slot.mu.Unlock()
	}
	return stats
}

func (pool *Pool) Close() error {
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return nil
	}
	pool.closed = true
	slots := append([]*workerSlot(nil), pool.slots...)
	pool.mu.Unlock()
	for _, slot := range slots {
		slot.mu.Lock()
		if !slot.dead && !slot.stopping {
			slot.stopping = true
			close(slot.stop)
		}
		done := slot.done
		slot.mu.Unlock()
		<-done
	}
	return nil
}

func stderrSuffix(buffer *lockedBuffer) string {
	output := buffer.String()
	if output == "" {
		return ""
	}
	return ": " + output
}

type workerSlotError struct {
	slot uint64
	err  error
}

func (failure workerSlotError) Error() string      { return failure.err.Error() }
func (failure workerSlotError) Unwrap() error      { return failure.err }
func (failure workerSlotError) WorkerSlot() string { return fmt.Sprintf("%d", failure.slot) }

var _ terminals.ScreenTrackerFactory = (*Pool)(nil)
var _ terminals.ScreenTracker = (*pooledModel)(nil)
