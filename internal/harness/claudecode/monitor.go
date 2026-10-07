package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/protocol"
)

type MonitorEventType string

const (
	MonitorMetadata      MonitorEventType = "metadata"
	MonitorActivity      MonitorEventType = "activity"
	MonitorContextUsage  MonitorEventType = "context_usage"
	MonitorInputState    MonitorEventType = "input_state"
	MonitorObservability MonitorEventType = "observability"
	MonitorDiagnostic    MonitorEventType = "diagnostic"
)

type MonitorEvent struct {
	Type                 MonitorEventType
	ResumeTarget         string
	RolloutPath          string
	Activity             protocol.AgentActivity
	ContextTokens        *int64
	InputState           protocol.InputState
	ObservabilityStatus  protocol.ObservabilityStatus
	ObservabilityMessage string
}

type MonitorOptions struct {
	StartupTimeout    time.Duration
	Observer          *Observer
	ProcessID         int
	LaunchedAt        time.Time
	InitialInputState protocol.InputState
	AttentionActions  <-chan protocol.TerminalAttentionAction
}

func MonitorWithOptions(ctx context.Context, runtimeDir, worktree, harnessSessionID string, options MonitorOptions, send func(MonitorEvent)) {
	if send == nil {
		return
	}
	if options.StartupTimeout <= 0 {
		options.StartupTimeout = 15 * time.Second
	}
	send(MonitorEvent{Type: MonitorObservability, ObservabilityStatus: protocol.ObservabilityStarting, Activity: protocol.ActivityStarting})
	metadata, err := readRuntimeMetadata(runtimeDir)
	if err != nil || metadata.HarnessSessionID != harnessSessionID {
		degrade(send, "Claude monitoring runtime is unavailable.")
		return
	}
	observer := options.Observer
	if observer == nil {
		observer, err = PrepareObserver(runtimeDir)
		if err != nil {
			degrade(send, "Claude monitoring socket is unavailable.")
			return
		}
	}
	defer observer.Close()
	if observer.metadata.LaunchToken != metadata.LaunchToken {
		degrade(send, "Claude monitoring launch identity changed.")
		return
	}
	files, err := newObservedFiles(options.ProcessID, options.LaunchedAt)
	if err != nil {
		degrade(send, err.Error())
		return
	}
	defer files.watcher.Close()
	if err := files.refreshWatches(runtimeDir); err != nil {
		degrade(send, "Claude file notifications are unavailable.")
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var deliveries <-chan hookDelivery
	var stopReceiver context.CancelFunc
	startReceiver := func() error {
		listener, err := observer.currentListener()
		if err != nil {
			return err
		}
		receiveContext, stop := context.WithCancel(ctx)
		channel := make(chan hookDelivery)
		stopReceiver, deliveries = stop, channel
		go observer.receiveFrom(receiveContext, listener, channel)
		return nil
	}
	stopReceiving := func() {
		if stopReceiver != nil {
			stopReceiver()
			stopReceiver = nil
		}
		deliveries = nil
	}
	defer stopReceiving()
	reducer := NewReducer(options.InitialInputState)
	healthy, discovered := false, false
	lastProblem := ""
	startup := time.NewTimer(options.StartupTimeout)
	defer startup.Stop()
	startupChannel := startup.C
	actions := options.AttentionActions
	// A buffered wakeup drains large appends in bounded chunks without polling.
	drain := make(chan struct{}, 1)
	scheduleDrain := func() {
		select {
		case drain <- struct{}{}:
		default:
		}
	}
	published, draining := false, false
	var publishedActivity protocol.AgentActivity
	var publishedInput protocol.InputState
	var pendingContextTokens *int64
	emit := func() {
		if !healthy || draining {
			return
		}
		if published && reducer.activity == publishedActivity && reducer.inputState == publishedInput {
			return
		}
		kind := MonitorActivity
		if !published || reducer.inputState != publishedInput {
			kind = MonitorInputState
		}
		published, publishedActivity, publishedInput = true, reducer.activity, reducer.inputState
		send(MonitorEvent{Type: kind, Activity: reducer.activity, InputState: reducer.inputState})
	}
	problem := func(message string) {
		healthy, published = false, false
		reducer.ApplyPresence("")
		if message != lastProblem {
			lastProblem = message
			degrade(send, message)
			send(MonitorEvent{Type: MonitorInputState, InputState: protocol.InputNone, Activity: protocol.ActivityUnknown})
		}
	}
	checkLoss := func() bool {
		present, err := lossMarkersPresent(runtimeDir)
		return present || err != nil
	}
	recovering := false
	clearLoss := func() error {
		return clearLossMarkers(runtimeDir, metadata.LaunchToken)
	}
	refresh := func() {
		if !discovered {
			return
		}
		if err := files.refreshWatches(runtimeDir); err != nil {
			problem("Claude file notifications are unavailable.")
			return
		}
		status, err := files.readPresence()
		if err != nil {
			if !files.presenceSeen && (errors.Is(err, os.ErrNotExist) || errors.Is(err, errPresencePending)) && startupChannel != nil {
				return
			}
			problem("Claude presence observation is unavailable: " + err.Error())
			return
		}
		reducer.ApplyPresence(status)
		more, err := files.readTranscript(func(record TranscriptObservation) {
			projection := reducer.ApplyTranscript(record)
			if projection.ContextChanged {
				pendingContextTokens = projection.ContextTokens
			}
		})
		if err != nil {
			problem("Claude transcript observation is unavailable: " + err.Error())
			return
		}
		// Do not publish settlement until the complete append is consumed: an
		// interruption or blocking outcome may be in a later chunk.
		draining = more || len(files.partial) != 0
		if more {
			scheduleDrain()
			return
		}
		if !healthy {
			// File notifications and hook deliveries can arrive while the socket
			// is being rebound. Reconcile them, but do not advertise health until
			// the launch listener and its loss marker have both recovered.
			if recovering {
				return
			}
			healthy, lastProblem = true, ""
			startup.Stop()
			startupChannel = nil
			send(MonitorEvent{Type: MonitorObservability, ObservabilityStatus: protocol.ObservabilityHealthy})
		}
		emit()
		if healthy && pendingContextTokens != nil {
			send(MonitorEvent{Type: MonitorContextUsage, ContextTokens: pendingContextTokens})
			pendingContextTokens = nil
		}
	}
	var recoveryTimer *time.Timer
	var recoveryChannel <-chan time.Time
	recoveryDelay := 100 * time.Millisecond
	scheduleRecovery := func(delay time.Duration) {
		if recoveryTimer == nil {
			recoveryTimer = time.NewTimer(delay)
		} else {
			if !recoveryTimer.Stop() {
				select {
				case <-recoveryTimer.C:
				default:
				}
			}
			recoveryTimer.Reset(delay)
		}
		recoveryChannel = recoveryTimer.C
	}
	defer func() {
		if recoveryTimer != nil {
			recoveryTimer.Stop()
		}
	}()
	beginRecovery := func(message string) {
		problem(message)
		stopReceiving()
		recovering = true
		recoveryDelay = 100 * time.Millisecond
		scheduleRecovery(0)
	}
	attemptRecovery := func() {
		recoveryChannel = nil
		if deliveries == nil {
			if err := observer.rebind(); err != nil {
				scheduleRecovery(recoveryDelay)
				recoveryDelay = min(recoveryDelay*2, 2*time.Second)
				return
			}
			if err := startReceiver(); err != nil {
				scheduleRecovery(recoveryDelay)
				recoveryDelay = min(recoveryDelay*2, 2*time.Second)
				return
			}
		}
		if err := clearLoss(); err != nil {
			scheduleRecovery(recoveryDelay)
			recoveryDelay = min(recoveryDelay*2, 2*time.Second)
			return
		}
		if checkLoss() {
			scheduleRecovery(recoveryDelay)
			recoveryDelay = min(recoveryDelay*2, 2*time.Second)
			return
		}
		recovering = false
		refresh()
	}
	if err := startReceiver(); err != nil {
		problem("Claude monitoring socket is unavailable.")
		return
	}
	if checkLoss() {
		beginRecovery("Claude monitoring lost a hook event and is recovering.")
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-startupChannel:
			startupChannel = nil
			problem("Claude monitoring did not observe session startup and presence before its deadline.")
		case delivery, ok := <-deliveries:
			if !ok {
				beginRecovery("Claude monitoring socket stopped and is recovering.")
				continue
			}
			if delivery.err != nil {
				if delivery.ack != nil {
					delivery.ack <- false
				}
				beginRecovery("Claude monitoring received a malformed hook event and is recovering.")
				continue
			}
			event := delivery.event
			if event.Diagnostic != "" {
				if delivery.ack != nil {
					delivery.ack <- false
				}
				beginRecovery("Claude monitoring lost a hook event and is recovering.")
				continue
			}
			if event.Event == "SessionStart" && event.AgentID == "" {
				if err := files.discoverProcesses(event.ProcessID); err != nil {
					delivery.ack <- false
					problem(err.Error())
					return
				}
				if err := files.setTranscript(event.TranscriptPath, event.SessionID); err != nil {
					if delivery.ack != nil {
						delivery.ack <- false
					}
					problem(err.Error())
					return
				}
				files.presenceDir = filepath.Join(event.ConfigDir, "sessions")
				discovered = true
			}
			projection := reducer.Apply(event)
			if projection.Metadata {
				send(MonitorEvent{Type: MonitorMetadata, ResumeTarget: projection.ResumeTarget, RolloutPath: projection.RolloutPath})
			}
			emit()
			// Release the hook before reading files: Stop settlement is produced
			// only after all hook processes, including ours, have returned.
			delivery.ack <- true
			if event.Event == "CwdChanged" && outsideWorktree(worktree, event.CWD) {
				send(MonitorEvent{Type: MonitorDiagnostic, ObservabilityMessage: "Claude changed to a directory outside the managed worktree."})
			}
			refresh()
		case event, ok := <-files.watcher.Events:
			if !ok {
				problem("Claude file notifications stopped.")
				return
			}
			if relevantLossMarkerPath(event.Name, runtimeDir) && checkLoss() {
				beginRecovery("Claude monitoring lost a hook event and is recovering.")
				continue
			}
			if files.relevantPresencePath(event.Name) || relevantPath(event.Name, files.transcriptPath) {
				refresh()
			}
		case <-files.watcher.Errors:
			problem("Claude file notifications lost events.")
			return
		case <-drain:
			refresh()
		case <-recoveryChannel:
			if recovering {
				attemptRecovery()
			}
		case action, ok := <-actions:
			if !ok {
				actions = nil
				continue
			}
			reducer.ApplyAttentionAction(action)
			emit()
		}
	}
}

func decodeNormalizedEvent(data []byte) (NormalizedEvent, error) {
	var event NormalizedEvent
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return NormalizedEvent{}, fmt.Errorf("decode normalized Claude event: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return NormalizedEvent{}, errors.New("normalized Claude event has trailing data")
	}
	if event.Version != 1 || !protocol.ValidIdentifier(event.HarnessSessionID) || len(event.LaunchToken) != 64 {
		return NormalizedEvent{}, errors.New("invalid normalized Claude event")
	}
	if event.Diagnostic != "" {
		if event.Diagnostic != droppedHookDiagnostic || event.Event != "" || event.SessionID != "" {
			return NormalizedEvent{}, errors.New("invalid normalized Claude diagnostic")
		}
		return event, nil
	}
	if event.Event == "SessionStart" && event.AgentID == "" &&
		(!filepath.IsAbs(event.ConfigDir) || filepath.Clean(event.ConfigDir) != event.ConfigDir ||
			len(event.ConfigDir) > 4096 || strings.Contains(event.ConfigDir, "\x00")) {
		return NormalizedEvent{}, errors.New("invalid Claude configuration directory")
	}
	if event.Event == "" || event.SessionID == "" || len(event.SessionID) > 128 || strings.Contains(event.SessionID, "\x00") ||
		len(event.PromptID) > 256 || strings.Contains(event.PromptID, "\x00") ||
		event.BackgroundTaskCount < 0 || event.BackgroundTaskCount > 1024 || event.SessionCronCount < 0 || event.SessionCronCount > 1024 {
		return NormalizedEvent{}, errors.New("invalid normalized Claude event")
	}
	return event, nil
}

func degrade(send func(MonitorEvent), message string) {
	send(MonitorEvent{Type: MonitorObservability, ObservabilityStatus: protocol.ObservabilityDegraded, Activity: protocol.ActivityUnknown, ObservabilityMessage: message})
}

func outsideWorktree(worktree, cwd string) bool {
	if worktree == "" || cwd == "" || !filepath.IsAbs(worktree) || !filepath.IsAbs(cwd) {
		return false
	}
	relative, err := filepath.Rel(worktree, cwd)
	return err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
