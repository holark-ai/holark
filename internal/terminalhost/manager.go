// Package terminalhost owns local PTYs and process groups independently from
// control-plane and browser transport lifetimes.
package terminalhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"github.com/holark-ai/holark/internal/terminalhost/xtermtracker"
	"github.com/holark-ai/holark/internal/terminals"
)

var (
	ErrCapacity        = errors.New("terminal host capacity exceeded")
	ErrDuplicate       = errors.New("terminal process already exists")
	ErrNotFound        = errors.New("terminal process not found")
	ErrWaitTimeout     = errors.New("timed out waiting for terminal process exit")
	ErrClosed          = errors.New("terminal host is closed")
	ErrStaleController = errors.New("terminal host controller was fenced")
	ErrStaleAttachment = errors.New("terminal attachment was fenced")
	ErrOrphanExpired   = errors.New("terminal host orphan grace expired")
)

const (
	DefaultMaximumTerminals = 1024
	maxPTYChunkBytes        = 64 * 1024
	processExitGrace        = time.Second
	processPollInterval     = 10 * time.Millisecond
)

type Options struct {
	ProcessRegistry           *ProcessRegistry
	MaximumTerminals          int
	DetachedTailBytes         int
	MaximumTailBytes          int
	MaximumAggregateTailBytes int64
	ScreenTrackerFactory      terminals.ScreenTrackerFactory
	Diagnostic                DiagnosticRecorder
	TerminalIOCapture         TerminalIOCapture
}

type DiagnosticRecorder interface {
	Record(layer, event string, fields map[string]any)
}

// TerminalIOCapture is an optional observer at the terminal-host PTY boundary.
// Implementations must return promptly and must not affect terminal behavior.
type TerminalIOCapture interface {
	CaptureOutput(id terminals.TerminalID, data []byte, readCompletedAt, occurredAt time.Time, sequence uint64, result string)
	CaptureInput(id terminals.TerminalID, data []byte, startedAt, completedAt time.Time, writtenBytes int, result string)
	CaptureCheckpoint(checkpoint terminals.TerminalCheckpoint)
}

// TerminalProgress is a read-only snapshot of host-owned process progress.
type TerminalProgress struct {
	TerminalID         terminals.TerminalID `json:"terminal_id"`
	Dimensions         terminals.Dimensions `json:"dimensions"`
	ResizeRevision     uint64               `json:"resize_revision"`
	LastSequence       uint64               `json:"last_sequence"`
	CheckpointSequence uint64               `json:"checkpoint_sequence"`
}

type process struct {
	id          terminals.TerminalID
	command     *exec.Cmd
	pty         *os.File
	tracker     terminals.ScreenTracker
	trackerWake chan struct{}
	trackerStop chan struct{}
	trackerDone chan struct{}
	trackerOnce sync.Once

	stateMu                sync.Mutex
	dimensions             terminals.Dimensions
	resizeRevision         uint64
	sequence               uint64
	screenSequence         uint64
	checkpoint             terminals.TerminalCheckpoint
	tail                   []terminals.ProcessNotification
	tailBytes              int
	attachmentSequence     uint64
	completionAcknowledged bool
	trackingStopped        bool
	attachment             terminals.TerminalAttachment
	attachmentWake         chan struct{}
	attachmentDone         chan struct{}
	closing                bool
	exited                 bool
	exitCode               *int

	done     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
	readDone chan struct{}
	writeMu  sync.Mutex
	resizeMu sync.Mutex
}

type Manager struct {
	processRegistry           *ProcessRegistry
	mu                        sync.Mutex
	maximum                   int
	detachedTailBytes         int
	maximumTailBytes          int
	maximumAggregateTailBytes int64
	retainedTailBytes         atomic.Int64
	retentionMu               sync.Mutex
	trackerFactory            terminals.ScreenTrackerFactory
	processes                 map[terminals.TerminalID]*process
	changed                   chan struct{}
	streamStart               int
	closed                    bool
	scanProcessGroups         processGroupScanner
	resizePTY                 func(*os.File, *pty.Winsize) error
	diagnostic                DiagnosticRecorder
	terminalIOCapture         TerminalIOCapture
}

type processGroupScanner func(int) ([]int, error)

func NewManager(options Options) (*Manager, error) {
	if options.MaximumTerminals == 0 {
		options.MaximumTerminals = DefaultMaximumTerminals
	}
	if options.DetachedTailBytes == 0 {
		options.DetachedTailBytes = terminals.DefaultDetachedTailBytes
	}
	if options.MaximumTailBytes == 0 {
		options.MaximumTailBytes = terminals.DefaultMaximumTailBytes
	}
	if options.MaximumAggregateTailBytes == 0 {
		options.MaximumAggregateTailBytes = terminals.DefaultAggregateTailBytes
	}
	if options.ScreenTrackerFactory == nil {
		pool, err := xtermtracker.NewPool(xtermtracker.PoolOptions{ScrollbackLines: terminals.DefaultScrollbackLines})
		if err != nil {
			return nil, err
		}
		options.ScreenTrackerFactory = pool
	}
	if options.MaximumTerminals < 1 || options.DetachedTailBytes < 1024 ||
		options.MaximumTailBytes < options.DetachedTailBytes || options.MaximumAggregateTailBytes < int64(options.MaximumTailBytes) {
		return nil, errors.New("invalid terminal host limits")
	}
	return &Manager{
		processRegistry: options.ProcessRegistry,
		maximum:         options.MaximumTerminals, detachedTailBytes: options.DetachedTailBytes,
		maximumTailBytes: options.MaximumTailBytes, maximumAggregateTailBytes: options.MaximumAggregateTailBytes,
		trackerFactory: options.ScreenTrackerFactory,
		processes:      make(map[terminals.TerminalID]*process), changed: make(chan struct{}, 1),
		resizePTY: pty.Setsize, scanProcessGroups: terminalProcessGroupIDs, diagnostic: options.Diagnostic,
		terminalIOCapture: options.TerminalIOCapture,
	}, nil
}

func (manager *Manager) Launch(ctx context.Context, spec terminals.LaunchSpec) (int, error) {
	if !spec.TerminalID.Valid() || strings.TrimSpace(spec.Command) == "" {
		return 0, errors.New("invalid terminal launch")
	}
	if err := spec.Dimensions.Validate(); err != nil {
		return 0, err
	}
	if len(spec.Arguments) > 4096 || len(spec.Environment) > 8192 || len(spec.CWD) > 4096 {
		return 0, errors.New("terminal launch exceeds limits")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	command := exec.Command(spec.Command, spec.Arguments...)
	configureChildProcess(command)
	command.Dir = spec.CWD
	command.Env = append([]string(nil), spec.Environment...)
	running := &process{
		id: spec.TerminalID, command: command,
		dimensions: spec.Dimensions, done: make(chan struct{}), stop: make(chan struct{}),
		readDone: make(chan struct{}), trackerWake: make(chan struct{}, 1), trackerStop: make(chan struct{}),
		trackerDone: make(chan struct{}),
	}
	running.checkpoint = terminals.NewCheckpoint(spec.TerminalID, 0, spec.Dimensions, nil)

	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return 0, ErrClosed
	}
	if manager.processes[spec.TerminalID] != nil {
		manager.mu.Unlock()
		return 0, ErrDuplicate
	}
	active := 0
	for _, existing := range manager.processes {
		if !processDone(existing.done) {
			active++
		}
	}
	if active >= manager.maximum {
		manager.mu.Unlock()
		return 0, ErrCapacity
	}
	owner, err := manager.processRegistry.Prepare(command)
	if err != nil {
		manager.mu.Unlock()
		return 0, err
	}
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: uint16(spec.Dimensions.Columns), Rows: uint16(spec.Dimensions.Rows)})
	if err != nil {
		manager.mu.Unlock()
		return 0, fmt.Errorf("start terminal PTY: %w", err)
	}
	if err = manager.processRegistry.Started(owner, command.Process.Pid); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = terminal.Close()
		manager.mu.Unlock()
		return 0, err
	}
	running.pty = terminal
	manager.processes[spec.TerminalID] = running
	manager.mu.Unlock()

	manager.record("host_process_started", running, map[string]any{"columns": spec.Dimensions.Columns, "rows": spec.Dimensions.Rows})
	manager.captureCheckpoint(running.checkpoint)
	manager.notifyChanged()
	go manager.trackScreen(running)
	go manager.read(running)
	go manager.wait(running)
	return command.Process.Pid, nil
}

func (manager *Manager) Input(ctx context.Context, id terminals.TerminalID, data []byte) error {
	return manager.input(ctx, id, terminals.TerminalAttachment{}, data)
}

func (manager *Manager) InputAttached(ctx context.Context, id terminals.TerminalID, attachment terminals.TerminalAttachment, data []byte) error {
	if !attachment.Valid() {
		return ErrStaleAttachment
	}
	return manager.input(ctx, id, attachment, data)
}

func (manager *Manager) input(ctx context.Context, id terminals.TerminalID, attachment terminals.TerminalAttachment, data []byte) error {
	if len(data) == 0 || len(data) > maxPTYChunkBytes {
		return errors.New("invalid terminal input")
	}
	running, err := manager.find(id)
	if err != nil {
		return err
	}
	startedAt := time.Now().UTC()
	if err := ctx.Err(); err != nil {
		manager.captureInput(id, data, startedAt, time.Now().UTC(), 0, "cancelled")
		return err
	}
	running.stateMu.Lock()
	if attachment.Valid() && running.attachment != attachment {
		running.stateMu.Unlock()
		manager.captureInput(id, data, startedAt, time.Now().UTC(), 0, "stale_attachment")
		return ErrStaleAttachment
	}
	if running.exited {
		running.stateMu.Unlock()
		manager.captureInput(id, data, startedAt, time.Now().UTC(), 0, "exited")
		return ErrNotFound
	}
	running.writeMu.Lock()
	written, err := running.pty.Write(data)
	running.writeMu.Unlock()
	running.stateMu.Unlock()
	result := "written"
	if err != nil {
		result = "write_failed"
	} else if written != len(data) {
		result = "partial"
	}
	manager.captureInput(id, data, startedAt, time.Now().UTC(), written, result)
	return err
}

func (manager *Manager) Resize(ctx context.Context, id terminals.TerminalID, revision uint64, dimensions terminals.Dimensions) error {
	return manager.resize(ctx, id, terminals.TerminalAttachment{}, revision, dimensions)
}

func (manager *Manager) ResizeAttached(ctx context.Context, id terminals.TerminalID, attachment terminals.TerminalAttachment, revision uint64, dimensions terminals.Dimensions) error {
	if !attachment.Valid() {
		return ErrStaleAttachment
	}
	return manager.resize(ctx, id, attachment, revision, dimensions)
}

func (manager *Manager) resize(ctx context.Context, id terminals.TerminalID, attachment terminals.TerminalAttachment, revision uint64, dimensions terminals.Dimensions) error {
	if revision == 0 {
		return errors.New("terminal resize revision is required")
	}
	if err := dimensions.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	running, err := manager.find(id)
	if err != nil {
		return err
	}
	running.resizeMu.Lock()
	defer running.resizeMu.Unlock()
	manager.retentionMu.Lock()
	defer manager.retentionMu.Unlock()
	running.stateMu.Lock()
	if attachment.Valid() && running.attachment != attachment {
		running.stateMu.Unlock()
		return ErrStaleAttachment
	}
	if running.exited {
		running.stateMu.Unlock()
		return ErrNotFound
	}
	if revision <= running.resizeRevision {
		running.stateMu.Unlock()
		return nil
	}
	if dimensions == running.dimensions {
		if _, err := manager.appendNotificationLocked(running, terminals.ProcessNotification{
			Kind: terminals.ProcessResize, Dimensions: dimensions, ResizeRevision: revision,
		}); err != nil {
			running.stateMu.Unlock()
			return err
		}
		running.resizeRevision = revision
		running.stateMu.Unlock()
		manager.enforceAggregateTailLimitLocked()
		return nil
	}
	if err := manager.resizePTY(running.pty, &pty.Winsize{Cols: uint16(dimensions.Columns), Rows: uint16(dimensions.Rows)}); err != nil {
		running.stateMu.Unlock()
		return err
	}
	running.dimensions = dimensions
	if _, err := manager.appendNotificationLocked(running, terminals.ProcessNotification{
		Kind: terminals.ProcessResize, Dimensions: dimensions, ResizeRevision: revision,
	}); err != nil {
		running.stateMu.Unlock()
		return err
	}
	running.resizeRevision = revision
	sequence := running.sequence
	running.stateMu.Unlock()
	manager.record("host_terminal_resized", running, map[string]any{
		"revision": revision, "columns": dimensions.Columns, "rows": dimensions.Rows, "sequence": sequence,
	})
	manager.enforceAggregateTailLimitLocked()
	return nil
}

func (manager *Manager) Signal(_ context.Context, id terminals.TerminalID, signal syscall.Signal) error {
	running, err := manager.find(id)
	if err != nil {
		return err
	}
	return signalProcessGroupIfPresent(running.command.Process.Pid, signal)
}

func (manager *Manager) CloseTerminal(id terminals.TerminalID, grace time.Duration) error {
	return manager.closeTerminal(id, terminals.TerminalAttachment{}, grace)
}

func (manager *Manager) CloseAttached(id terminals.TerminalID, attachment terminals.TerminalAttachment, grace time.Duration) error {
	if !attachment.Valid() {
		return ErrStaleAttachment
	}
	return manager.closeTerminal(id, attachment, grace)
}

func (manager *Manager) closeTerminal(id terminals.TerminalID, attachment terminals.TerminalAttachment, grace time.Duration) error {
	running, err := manager.find(id)
	if err != nil {
		return err
	}
	running.stateMu.Lock()
	if attachment.Valid() && running.attachment != attachment {
		running.stateMu.Unlock()
		return ErrStaleAttachment
	}
	running.closing = true
	exited := running.exited
	running.stateMu.Unlock()
	if exited {
		return nil
	}
	return terminatePTYSession(manager.scanProcessGroups, running.command.Process.Pid, running.done, grace)
}

func (manager *Manager) Inventory() []terminals.TerminalID {
	manager.mu.Lock()
	processes := make([]*process, 0, len(manager.processes))
	for _, running := range manager.processes {
		processes = append(processes, running)
	}
	manager.mu.Unlock()
	result := make([]terminals.TerminalID, 0, len(processes))
	for _, running := range processes {
		result = append(result, running.id)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func (manager *Manager) Progress(id terminals.TerminalID) (TerminalProgress, bool) {
	manager.mu.Lock()
	running := manager.processes[id]
	manager.mu.Unlock()
	if running == nil {
		return TerminalProgress{}, false
	}
	running.stateMu.Lock()
	defer running.stateMu.Unlock()
	return TerminalProgress{
		TerminalID: running.id,
		Dimensions: running.dimensions, ResizeRevision: running.resizeRevision,
		LastSequence: running.sequence, CheckpointSequence: running.checkpoint.Sequence,
	}, true
}

func (manager *Manager) Attach(id terminals.TerminalID) (terminals.TerminalRestore, error) {
	running, err := manager.find(id)
	if err != nil {
		return terminals.TerminalRestore{}, err
	}
	token, err := newAttachmentToken()
	if err != nil {
		return terminals.TerminalRestore{}, err
	}
	running.stateMu.Lock()
	defer running.stateMu.Unlock()
	if running.exited {
		return terminals.TerminalRestore{}, terminals.ErrProcessEnded
	}
	checkpoint := running.checkpoint
	if running.attachmentDone != nil {
		close(running.attachmentDone)
	}
	running.attachment = terminals.TerminalAttachment{Token: token}
	running.attachmentSequence = 0
	running.attachmentWake = make(chan struct{}, 1)
	running.attachmentDone = make(chan struct{})
	manager.record("host_attachment_opened", running, map[string]any{
		"checkpoint_sequence": checkpoint.Sequence, "last_sequence": running.sequence,
		"notification_count": len(notificationsAfter(running.tail, checkpoint.Sequence)),
		"checkpoint_bytes":   len(checkpoint.ReplayPayload),
	})
	return terminals.TerminalRestore{
		Attachment: running.attachment, Checkpoint: cloneCheckpoint(checkpoint),
		Tail:         cloneNotifications(notificationsAfter(running.tail, checkpoint.Sequence)),
		LastSequence: running.sequence,
	}, nil
}

func newAttachmentToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "terminal-attachment-" + hex.EncodeToString(value), nil
}

func (manager *Manager) Detach(id terminals.TerminalID, attachment terminals.TerminalAttachment) error {
	running, err := manager.find(id)
	if err != nil {
		return err
	}
	manager.retentionMu.Lock()
	running.stateMu.Lock()
	if !attachment.Valid() || running.attachment != attachment {
		running.stateMu.Unlock()
		manager.retentionMu.Unlock()
		return ErrStaleAttachment
	}
	if running.attachmentDone != nil {
		close(running.attachmentDone)
	}
	running.attachment = terminals.TerminalAttachment{}
	running.attachmentSequence = 0
	running.attachmentWake = nil
	running.attachmentDone = nil
	manager.compactDeliveredCheckpointTailLocked(running)
	manager.record("host_attachment_detached", running, map[string]any{"last_sequence": running.sequence})
	running.stateMu.Unlock()
	manager.retentionMu.Unlock()
	manager.retireIfReady(running)
	return nil
}

// AttachmentUpdates waits for the next host-owned update for one current
// interactive attachment. The restore barrier returned by Attach and this
// method share the same process lock, so output cannot be lost between them.
func (manager *Manager) AttachmentUpdates(ctx context.Context, id terminals.TerminalID, attachment terminals.TerminalAttachment, cursor Cursor, maximumBytes int) (terminals.TerminalUpdateStream, error) {
	if !attachment.Valid() || !cursor.Valid() || cursor.TerminalID != id {
		return terminals.TerminalUpdateStream{}, errors.New("invalid terminal attachment cursor")
	}
	if maximumBytes <= 0 || maximumBytes > MaxFrameBytes/2 {
		maximumBytes = 64 * 1024
	}
	for {
		running, err := manager.find(id)
		if err != nil {
			return terminals.TerminalUpdateStream{}, err
		}
		manager.retentionMu.Lock()
		running.stateMu.Lock()
		if running.attachment != attachment || running.attachmentWake == nil || running.attachmentDone == nil {
			running.stateMu.Unlock()
			manager.retentionMu.Unlock()
			return terminals.TerminalUpdateStream{}, ErrStaleAttachment
		}
		running.attachmentSequence = max(running.attachmentSequence, cursor.Sequence)
		manager.compactDeliveredCheckpointTailLocked(running)
		update := attachmentUpdateLocked(running, cursor, maximumBytes)
		wake := running.attachmentWake
		done := running.attachmentDone
		running.stateMu.Unlock()
		manager.retentionMu.Unlock()
		manager.retireIfReady(running)
		if update.Checkpoint != nil || len(update.Notifications) > 0 {
			manager.record("host_attachment_update_ready", running, map[string]any{
				"checkpoint_sequence": terminalHostCheckpointSequence(update), "last_sequence": update.LastSequence,
				"notification_count": len(update.Notifications), "bytes": terminalHostUpdateBytes(update),
			})
			return update, nil
		}
		select {
		case <-ctx.Done():
			return terminals.TerminalUpdateStream{}, ctx.Err()
		case <-done:
			return terminals.TerminalUpdateStream{}, ErrStaleAttachment
		case <-wake:
		}
	}
}

func attachmentUpdateLocked(running *process, cursor Cursor, maximumBytes int) terminals.TerminalUpdateStream {
	update := terminals.TerminalUpdateStream{
		TerminalID: running.id, LastSequence: running.sequence,
	}
	effective := cursor.Sequence
	checkpointRequired := cursor.CheckpointSequence < running.checkpoint.Sequence &&
		(cursor.Sequence >= running.checkpoint.Sequence || !notificationsContinueAfter(running.tail, cursor.Sequence))
	if checkpointRequired {
		checkpoint := cloneCheckpoint(running.checkpoint)
		update.Checkpoint = &checkpoint
		effective = max(effective, checkpoint.Sequence)
	}
	used := 0
	for _, notification := range running.tail {
		if notification.Sequence <= effective {
			continue
		}
		cost := notificationCost(notification)
		if len(update.Notifications) > 0 && used+cost > maximumBytes {
			break
		}
		update.Notifications = append(update.Notifications, cloneNotification(notification))
		used += cost
	}
	return update
}

func (manager *Manager) Completions(ctx context.Context, acknowledged []terminals.TerminalID, wait time.Duration) ([]terminals.ProcessCompletion, error) {
	for _, id := range acknowledged {
		running, err := manager.find(id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		running.stateMu.Lock()
		running.completionAcknowledged = true
		running.stateMu.Unlock()
		manager.retireIfReady(running)
	}
	var deadline <-chan time.Time
	var timer *time.Timer
	if wait >= 0 {
		timer = time.NewTimer(wait)
		deadline = timer.C
		defer timer.Stop()
	}
	for {
		manager.mu.Lock()
		processes := make([]*process, 0, len(manager.processes))
		for _, running := range manager.processes {
			processes = append(processes, running)
		}
		closed := manager.closed
		manager.mu.Unlock()
		sort.Slice(processes, func(left, right int) bool { return processes[left].id < processes[right].id })
		completions := make([]terminals.ProcessCompletion, 0)
		for _, running := range processes {
			running.stateMu.Lock()
			if running.exited && !running.completionAcknowledged && running.exitCode != nil {
				completions = append(completions, terminals.ProcessCompletion{TerminalID: running.id, ExitCode: *running.exitCode})
			}
			running.stateMu.Unlock()
		}
		if len(completions) > 0 || wait == 0 || closed {
			return completions, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, nil
		case <-manager.changed:
		}
	}
}

func (manager *Manager) retireIfReady(running *process) {
	manager.retentionMu.Lock()
	running.stateMu.Lock()
	ready := running.exited && running.completionAcknowledged &&
		(!running.attachment.Valid() || running.attachmentSequence >= running.sequence)
	if !ready {
		running.stateMu.Unlock()
		manager.retentionMu.Unlock()
		return
	}
	manager.mu.Lock()
	if manager.processes[running.id] != running {
		manager.mu.Unlock()
		running.stateMu.Unlock()
		manager.retentionMu.Unlock()
		return
	}
	delete(manager.processes, running.id)
	if running.attachmentDone != nil {
		close(running.attachmentDone)
	}
	running.attachment = terminals.TerminalAttachment{}
	running.attachmentWake = nil
	running.attachmentDone = nil
	manager.retainedTailBytes.Add(-int64(running.tailBytes))
	running.tail = nil
	running.tailBytes = 0
	manager.mu.Unlock()
	running.stateMu.Unlock()
	manager.retentionMu.Unlock()
	manager.stopTracking(running)
	manager.notifyChanged()
}

func (manager *Manager) Close() {
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return
	}
	manager.closed = true
	active := make([]*process, 0, len(manager.processes))
	for _, running := range manager.processes {
		active = append(active, running)
	}
	manager.mu.Unlock()
	manager.notifyChanged()
	for _, running := range active {
		running.stateMu.Lock()
		exited := running.exited
		running.stateMu.Unlock()
		if !exited {
			_ = forcePTYSessionExit(manager.scanProcessGroups, running.command.Process.Pid, time.Second)
		}
	}
	for _, running := range active {
		running.stateMu.Lock()
		exited := running.exited
		running.stateMu.Unlock()
		if !exited {
			_ = waitForDone(running.done, time.Second)
		}
		manager.stopTracking(running)
	}
	if closer, ok := manager.trackerFactory.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

type screenTrackerCloser interface {
	Close() error
}

func closeScreenTracker(tracker terminals.ScreenTracker) {
	if closer, ok := tracker.(screenTrackerCloser); ok {
		_ = closer.Close()
	}
}

func (manager *Manager) find(id terminals.TerminalID) (*process, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	running := manager.processes[id]
	if running == nil {
		return nil, ErrNotFound
	}
	if manager.closed {
		return nil, ErrClosed
	}
	return running, nil
}

func (manager *Manager) read(running *process) {
	defer close(running.readDone)
	buffer := make([]byte, maxPTYChunkBytes)
	for {
		manager.mu.Lock()
		closed := manager.closed
		manager.mu.Unlock()
		if closed {
			return
		}
		count, err := running.pty.Read(buffer)
		readCompletedAt := time.Now().UTC()
		if count > 0 {
			data := append([]byte(nil), buffer[:count]...)
			notification, appendErr := manager.appendNotification(running, terminals.ProcessNotification{
				Kind: terminals.ProcessOutput, Data: data,
				Characters: uint64(utf8.RuneCount(buffer[:count])),
			})
			result := "accepted"
			if appendErr != nil {
				result = "append_failed"
				if errors.Is(appendErr, ErrClosed) {
					result = "host_closed"
				} else if errors.Is(appendErr, ErrNotFound) {
					result = "terminal_missing"
				}
			}
			manager.captureOutput(running.id, data, readCompletedAt, notification.OccurredAt, notification.Sequence, result)
			if appendErr != nil {
				if !errors.Is(appendErr, ErrClosed) && !errors.Is(appendErr, ErrNotFound) {
					log.Printf("retain terminal output terminal_id=%s: %v", running.id, appendErr)
				}
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (manager *Manager) wait(running *process) {
	err := running.command.Wait()
	drainPTYSession(manager.scanProcessGroups, running.command.Process.Pid)
	_ = running.pty.Close()
	running.stopOnce.Do(func() { close(running.stop) })
	<-running.readDone
	exitCode := 0
	if err != nil {
		exitCode = running.command.ProcessState.ExitCode()
	}
	manager.retentionMu.Lock()
	running.stateMu.Lock()
	_, _ = manager.appendNotificationLocked(running, terminals.ProcessNotification{
		Kind: terminals.ProcessExit, ExitCode: &exitCode,
	})
	running.exited = true
	running.exitCode = &exitCode
	close(running.done)
	lastSequence := running.sequence
	running.stateMu.Unlock()
	manager.record("host_process_stopped", running, map[string]any{"code": exitCode, "last_sequence": lastSequence})
	manager.enforceAggregateTailLimitLocked()
	manager.retentionMu.Unlock()
	manager.notifyChanged()
}

func (manager *Manager) appendNotification(running *process, notification terminals.ProcessNotification) (terminals.ProcessNotification, error) {
	manager.retentionMu.Lock()
	defer manager.retentionMu.Unlock()
	running.stateMu.Lock()
	normalized, err := manager.appendNotificationLocked(running, notification)
	running.stateMu.Unlock()
	if err == nil {
		manager.enforceAggregateTailLimitLocked()
	}
	return normalized, err
}

func (manager *Manager) appendNotificationLocked(running *process, notification terminals.ProcessNotification) (terminals.ProcessNotification, error) {
	manager.mu.Lock()
	closed := manager.closed
	current := manager.processes[running.id] == running
	manager.mu.Unlock()
	if closed {
		return terminals.ProcessNotification{}, ErrClosed
	}
	if !current {
		return terminals.ProcessNotification{}, ErrNotFound
	}
	nextSequence := running.sequence + 1
	notification.TerminalID = running.id
	notification.Sequence = nextSequence
	notification.OccurredAt = time.Now().UTC()
	if err := notification.Validate(); err != nil {
		return terminals.ProcessNotification{}, err
	}
	switch notification.Kind {
	case terminals.ProcessOutput:
		running.screenSequence = notification.Sequence
	case terminals.ProcessResize:
		if notification.ResizeRevision != 0 {
			running.screenSequence = notification.Sequence
		}
	}
	running.sequence = nextSequence
	running.tail = append(running.tail, cloneNotification(notification))
	cost := notificationCost(notification)
	running.tailBytes += cost
	manager.retainedTailBytes.Add(int64(cost))
	if running.tailBytes > manager.maximumTailBytes {
		manager.degradeProcessLocked(running, "terminal_tail_limit")
	}
	manager.wakeTracker(running)
	manager.notifyAttachmentChangedLocked(running)
	manager.notifyChanged()
	return notification, nil
}

func (manager *Manager) trackScreen(running *process) {
	defer close(running.trackerDone)
	var tracker terminals.ScreenTracker
	var trackedSequence uint64
	modelQuality := terminals.RestorationTrusted
	backoff := 100 * time.Millisecond
	recoveryAttempts := 0
	recovering := false
	for {
		if tracker != nil {
			running.stateMu.Lock()
			checkpointSequence := running.checkpoint.Sequence
			running.stateMu.Unlock()
			if checkpointSequence > trackedSequence {
				closeScreenTracker(tracker)
				tracker = nil
				continue
			}
		}
		if tracker == nil {
			running.stateMu.Lock()
			checkpoint := cloneCheckpoint(running.checkpoint)
			running.stateMu.Unlock()
			created, err := manager.trackerFactory.New(running.id, checkpoint.Dimensions)
			if err != nil {
				manager.logTrackingFailure(running, nil, checkpoint.Sequence, "start", recoveryAttempts+1, err)
				if !waitForTracker(running, backoff) {
					return
				}
				backoff = min(backoff*2, 30*time.Second)
				continue
			}
			tracker = created
			running.stateMu.Lock()
			running.tracker = tracker
			running.stateMu.Unlock()
			trackedSequence = checkpoint.Sequence
			modelQuality = checkpoint.Quality
			if len(checkpoint.ReplayPayload) > 0 {
				if err := tracker.Write(checkpoint.Sequence, checkpoint.ReplayPayload); err != nil {
					recoveryAttempts++
					manager.logTrackingFailure(running, tracker, checkpoint.Sequence, "reconstruct", recoveryAttempts, err)
					closeScreenTracker(tracker)
					tracker = nil
					if recoveryAttempts >= 3 {
						manager.stopTrackingPermanently(running, checkpoint.Sequence, recoveryAttempts)
						return
					}
					recovering = true
					continue
				}
			}
			backoff = 100 * time.Millisecond
		}

		resetSequence := trackerResetSequence(tracker)
		notification, hasNotification, snapshotSequence, shouldSnapshot := manager.nextTrackingWork(running, trackedSequence, resetSequence)
		if hasNotification {
			var err error
			switch notification.Kind {
			case terminals.ProcessOutput:
				err = tracker.Write(notification.Sequence, notification.Data)
			case terminals.ProcessResize:
				if notification.ResizeRevision != 0 {
					err = tracker.Resize(notification.Sequence, notification.Dimensions)
				}
			}
			if err != nil {
				recoveryAttempts++
				manager.logTrackingFailure(running, tracker, trackedSequence, "advance", recoveryAttempts, err)
				closeScreenTracker(tracker)
				tracker = nil
				if recoveryAttempts >= 3 {
					manager.stopTrackingPermanently(running, trackedSequence, recoveryAttempts)
					return
				}
				recovering = true
				continue
			}
			trackedSequence = notification.Sequence
			continue
		}
		if shouldSnapshot {
			manager.record("host_checkpoint_candidate_started", running, map[string]any{"sequence": snapshotSequence})
			checkpoint, err := tracker.Snapshot(snapshotSequence)
			if err != nil {
				if errors.Is(err, terminals.ErrCheckpointUnavailable) {
					manager.degradeRestoration(running, "snapshot_minimum_too_large")
					recoveryAttempts = 0
					recovering = false
					continue
				}
				recoveryAttempts++
				manager.logTrackingFailure(running, tracker, snapshotSequence, "snapshot", recoveryAttempts, err)
				closeScreenTracker(tracker)
				tracker = nil
				if recoveryAttempts >= 3 {
					manager.stopTrackingPermanently(running, snapshotSequence, recoveryAttempts)
					return
				}
				recovering = true
				continue
			}
			modelQuality = manager.completeCheckpoint(running, checkpoint, trackerResetSequence(tracker), modelQuality)
			recoveryAttempts = 0
			recovering = false
			continue
		}
		if recovering {
			recoveryAttempts = 0
			recovering = false
		}
		if !waitForTracker(running, 0) {
			closeScreenTracker(tracker)
			return
		}
	}
}

func (manager *Manager) nextTrackingWork(running *process, trackedSequence, resetSequence uint64) (terminals.ProcessNotification, bool, uint64, bool) {
	running.stateMu.Lock()
	defer running.stateMu.Unlock()
	// Checkpoint processed work before taking more output, so a continuously
	// nonempty queue cannot starve snapshots. Exclude unprocessed bytes: a large
	// backlog must not cause a new snapshot after every chunk.
	processedBytes := 0
	for _, notification := range running.tail {
		if notification.Sequence > trackedSequence {
			break
		}
		if notification.Sequence > running.checkpoint.Sequence {
			processedBytes += notificationCost(notification)
		}
	}
	shouldSnapshot := trackedSequence > running.checkpoint.Sequence &&
		(processedBytes >= manager.detachedTailBytes ||
			running.checkpoint.Quality == terminals.RestorationDegraded && resetSequence > running.checkpoint.Sequence)
	if shouldSnapshot {
		return terminals.ProcessNotification{}, false, trackedSequence, true
	}
	for _, notification := range running.tail {
		if notification.Sequence <= trackedSequence {
			continue
		}
		if notification.Kind == terminals.ProcessOutput || notification.Kind == terminals.ProcessResize {
			return cloneNotification(notification), true, 0, false
		}
	}
	return terminals.ProcessNotification{}, false, trackedSequence, false
}

func (manager *Manager) completeCheckpoint(
	running *process,
	checkpoint terminals.TerminalCheckpoint,
	resetSequence uint64,
	modelQuality terminals.RestorationQuality,
) terminals.RestorationQuality {
	manager.retentionMu.Lock()
	running.stateMu.Lock()
	committed := false
	lastSequence := uint64(0)
	if checkpoint.Sequence > running.checkpoint.Sequence {
		checkpoint.Quality = modelQuality
		if modelQuality == terminals.RestorationDegraded && resetSequence > running.checkpoint.Sequence &&
			checkpoint.Sequence >= resetSequence {
			checkpoint.Quality = terminals.RestorationTrusted
			modelQuality = terminals.RestorationTrusted
			log.Printf("terminal screen restoration trusted terminal_id=%s sequence=%d category=parsed_ris",
				running.id, checkpoint.Sequence)
		}
		running.checkpoint = checkpoint
		manager.compactDeliveredCheckpointTailLocked(running)
		committed = true
		lastSequence = running.sequence
	}
	running.stateMu.Unlock()
	manager.retentionMu.Unlock()
	if committed {
		manager.record("host_checkpoint_committed", running, map[string]any{
			"checkpoint_sequence": checkpoint.Sequence, "checkpoint_bytes": len(checkpoint.ReplayPayload),
			"restoration_quality": string(checkpoint.Quality), "last_sequence": lastSequence,
		})
		manager.captureCheckpoint(checkpoint)
	}
	manager.notifyAttachmentChanged(running)
	manager.notifyChanged()
	return modelQuality
}

type screenTrackerResetProgress interface {
	ResetSequence() uint64
}

func trackerResetSequence(tracker terminals.ScreenTracker) uint64 {
	progress, ok := tracker.(screenTrackerResetProgress)
	if !ok {
		return 0
	}
	return progress.ResetSequence()
}

func (manager *Manager) wakeTracker(running *process) {
	select {
	case running.trackerWake <- struct{}{}:
	default:
	}
}

func (manager *Manager) stopTracking(running *process) {
	running.trackerOnce.Do(func() { close(running.trackerStop) })
	<-running.trackerDone
}

func waitForTracker(running *process, delay time.Duration) bool {
	if delay <= 0 {
		select {
		case <-running.trackerStop:
			return false
		case <-running.trackerWake:
			return true
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-running.trackerStop:
		return false
	case <-timer.C:
		return true
	}
}

func (manager *Manager) logTrackingFailure(
	running *process,
	tracker terminals.ScreenTracker,
	sequence uint64,
	category string,
	attempt int,
	err error,
) {
	transition := "reconstruct_model"
	if category == "start" {
		transition = "retry_start"
	} else if attempt >= 3 {
		transition = "stop_tracking"
	}
	log.Printf("terminal screen tracking failure terminal_id=%s worker_slot=%s sequence=%d category=%s attempt=%d transition=%s: %v",
		running.id, trackingWorkerSlot(tracker, err), sequence, category, attempt, transition, err)
}

type screenTrackerWorkerSlot interface {
	WorkerSlot() string
}

func trackingWorkerSlot(tracker terminals.ScreenTracker, err error) string {
	if diagnostic, ok := tracker.(screenTrackerWorkerSlot); ok {
		return diagnostic.WorkerSlot()
	}
	if diagnostic, ok := err.(screenTrackerWorkerSlot); ok {
		return diagnostic.WorkerSlot()
	}
	return "unassigned"
}

func (manager *Manager) notifyChanged() {
	select {
	case manager.changed <- struct{}{}:
	default:
	}
}

func (manager *Manager) notifyAttachmentChanged(running *process) {
	running.stateMu.Lock()
	manager.notifyAttachmentChangedLocked(running)
	running.stateMu.Unlock()
}

func (manager *Manager) notifyAttachmentChangedLocked(running *process) {
	if running.attachmentWake == nil {
		return
	}
	select {
	case running.attachmentWake <- struct{}{}:
	default:
	}
}

func notificationsAfter(notifications []terminals.ProcessNotification, sequence uint64) []terminals.ProcessNotification {
	for index, notification := range notifications {
		if notification.Sequence > sequence {
			return notifications[index:]
		}
	}
	return nil
}

func compactNotificationsAfter(notifications []terminals.ProcessNotification, sequence uint64) []terminals.ProcessNotification {
	retained := notificationsAfter(notifications, sequence)
	if len(retained) == 0 {
		return nil
	}
	return append([]terminals.ProcessNotification(nil), retained...)
}

func processDone(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func notificationCost(notification terminals.ProcessNotification) int {
	return len(notification.Data) + 128
}

func notificationBytesAfter(notifications []terminals.ProcessNotification, sequence uint64) int {
	total := 0
	for _, notification := range notifications {
		if notification.Sequence > sequence {
			total += notificationCost(notification)
		}
	}
	return total
}

func notificationsContinueAfter(notifications []terminals.ProcessNotification, sequence uint64) bool {
	for _, notification := range notifications {
		if notification.Sequence > sequence {
			return notification.Sequence == sequence+1
		}
	}
	return false
}

func (manager *Manager) compactDeliveredCheckpointTailLocked(running *process) {
	through := running.checkpoint.Sequence
	if running.attachment.Valid() {
		through = min(through, running.attachmentSequence)
	}
	if through == 0 {
		return
	}
	oldBytes := running.tailBytes
	running.tail = compactNotificationsAfter(running.tail, through)
	running.tailBytes = notificationBytesAfter(running.tail, 0)
	manager.retainedTailBytes.Add(int64(running.tailBytes - oldBytes))
	if running.tailBytes != oldBytes {
		manager.record("host_checkpoint_tail_compacted", running, map[string]any{
			"checkpoint_sequence": running.checkpoint.Sequence, "sequence": through,
			"bytes": oldBytes - running.tailBytes, "last_sequence": running.sequence,
		})
	}
}

func (manager *Manager) enforceAggregateTailLimitLocked() {
	for manager.retainedTailBytes.Load() > manager.maximumAggregateTailBytes {
		manager.mu.Lock()
		processes := make([]*process, 0, len(manager.processes))
		for _, running := range manager.processes {
			processes = append(processes, running)
		}
		manager.mu.Unlock()
		type candidate struct {
			running  *process
			attached bool
			bytes    int
		}
		candidates := make([]candidate, 0, len(processes))
		for _, running := range processes {
			running.stateMu.Lock()
			candidates = append(candidates, candidate{
				running: running, attached: running.attachment.Valid(), bytes: running.tailBytes,
			})
			running.stateMu.Unlock()
		}
		sort.Slice(candidates, func(left, right int) bool {
			if candidates[left].attached != candidates[right].attached {
				return candidates[left].attached
			}
			return candidates[left].bytes > candidates[right].bytes
		})
		degraded := false
		for _, candidate := range candidates {
			if manager.retainedTailBytes.Load() <= manager.maximumAggregateTailBytes {
				break
			}
			candidate.running.stateMu.Lock()
			if candidate.running.tailBytes > 0 {
				manager.degradeProcessLocked(candidate.running, "aggregate_tail_limit")
				degraded = true
			}
			candidate.running.stateMu.Unlock()
		}
		if !degraded {
			return
		}
	}
}

func (manager *Manager) degradeProcessLocked(running *process, category string) {
	oldBytes := running.tailBytes
	if running.attachmentDone != nil {
		close(running.attachmentDone)
	}
	running.attachment = terminals.TerminalAttachment{}
	running.attachmentSequence = 0
	running.attachmentWake = nil
	running.attachmentDone = nil
	running.checkpoint = terminals.NewCheckpointWithQuality(
		running.id, running.sequence, running.dimensions, nil, terminals.RestorationDegraded,
	)
	running.tail = nil
	running.tailBytes = 0
	manager.retainedTailBytes.Add(-int64(oldBytes))
	manager.record("host_retention_pressure", running, map[string]any{
		"reason": category, "bytes": oldBytes, "last_sequence": running.sequence,
	})
	manager.wakeTracker(running)
	log.Printf("terminal screen restoration degraded terminal_id=%s sequence=%d category=%s",
		running.id, running.sequence, category)
}

func (manager *Manager) degradeRestoration(running *process, category string) {
	manager.retentionMu.Lock()
	running.stateMu.Lock()
	manager.degradeProcessLocked(running, category)
	running.stateMu.Unlock()
	manager.retentionMu.Unlock()
	manager.notifyChanged()
}

func (manager *Manager) stopTrackingPermanently(running *process, sequence uint64, attempts int) {
	manager.retentionMu.Lock()
	running.stateMu.Lock()
	running.tracker = nil
	running.trackingStopped = true
	running.stateMu.Unlock()
	manager.retentionMu.Unlock()
	log.Printf("terminal screen tracking stopped terminal_id=%s sequence=%d category=recovery_limit attempt=%d",
		running.id, sequence, attempts)
	manager.notifyChanged()
}

func cloneNotification(notification terminals.ProcessNotification) terminals.ProcessNotification {
	notification.Data = append([]byte(nil), notification.Data...)
	notification.ExitCode = cloneInt(notification.ExitCode)
	return notification
}

func cloneNotifications(notifications []terminals.ProcessNotification) []terminals.ProcessNotification {
	result := make([]terminals.ProcessNotification, len(notifications))
	for index, notification := range notifications {
		result[index] = cloneNotification(notification)
	}
	return result
}

func cloneCheckpoint(checkpoint terminals.TerminalCheckpoint) terminals.TerminalCheckpoint {
	// Preserve the non-nil empty slice required by the checkpoint JSON contract.
	checkpoint.ReplayPayload = append([]byte{}, checkpoint.ReplayPayload...)
	return checkpoint
}

func (manager *Manager) record(event string, running *process, fields map[string]any) {
	if manager.diagnostic == nil || running == nil {
		return
	}
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["terminal_id"] = string(running.id)
	manager.diagnostic.Record("terminal-host", event, fields)
}

func (manager *Manager) captureOutput(
	id terminals.TerminalID,
	data []byte,
	readCompletedAt, occurredAt time.Time,
	sequence uint64,
	result string,
) {
	if manager.terminalIOCapture != nil {
		manager.terminalIOCapture.CaptureOutput(id, data, readCompletedAt, occurredAt, sequence, result)
	}
}

func (manager *Manager) captureInput(
	id terminals.TerminalID,
	data []byte,
	startedAt, completedAt time.Time,
	writtenBytes int,
	result string,
) {
	if manager.terminalIOCapture != nil {
		manager.terminalIOCapture.CaptureInput(id, data, startedAt, completedAt, writtenBytes, result)
	}
}

func (manager *Manager) captureCheckpoint(checkpoint terminals.TerminalCheckpoint) {
	if manager.terminalIOCapture != nil {
		manager.terminalIOCapture.CaptureCheckpoint(checkpoint)
	}
}

func terminalHostCheckpointSequence(update terminals.TerminalUpdateStream) uint64 {
	if update.Checkpoint == nil {
		return 0
	}
	return update.Checkpoint.Sequence
}

func terminalHostUpdateBytes(update terminals.TerminalUpdateStream) int {
	total := 0
	if update.Checkpoint != nil {
		total += len(update.Checkpoint.ReplayPayload)
	}
	for _, notification := range update.Notifications {
		total += len(notification.Data)
	}
	return total
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
