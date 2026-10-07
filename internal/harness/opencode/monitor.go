package opencode

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
)

type MonitorEvent struct {
	Type                 MonitorEventType
	ResumeTarget         string
	Activity             protocol.AgentActivity
	ContextTokens        *int64
	InputState           protocol.InputState
	ObservabilityStatus  protocol.ObservabilityStatus
	ObservabilityMessage string
}

type MonitorOptions struct {
	StartupTimeout    time.Duration
	PollInterval      time.Duration
	InitialInputState protocol.InputState
}

func Monitor(ctx context.Context, runtimeDir, resumeTarget string, inputState protocol.InputState, send func(MonitorEvent)) {
	MonitorWithOptions(ctx, runtimeDir, resumeTarget, MonitorOptions{
		StartupTimeout:    15 * time.Second,
		PollInterval:      100 * time.Millisecond,
		InitialInputState: inputState,
	}, send)
}

func MonitorWithOptions(ctx context.Context, runtimeDir, resumeTarget string, options MonitorOptions, send func(MonitorEvent)) {
	if send == nil {
		return
	}
	if options.StartupTimeout <= 0 {
		options.StartupTimeout = 15 * time.Second
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 100 * time.Millisecond
	}
	send(MonitorEvent{Type: MonitorObservability, ObservabilityStatus: protocol.ObservabilityStarting, Activity: protocol.ActivityStarting})

	spoolPath := filepath.Join(runtimeDir, SpoolFileName)
	reducer := NewReducer(resumeTarget, options.InitialInputState)
	offset := 0
	initialized := false
	ticker := time.NewTicker(options.PollInterval)
	defer ticker.Stop()
	startup := time.NewTimer(options.StartupTimeout)
	defer startup.Stop()
	startupChannel := startup.C

	emitProjection := func(projection Projection) {
		if projection.Metadata {
			send(MonitorEvent{Type: MonitorMetadata, ResumeTarget: projection.ResumeTarget})
		}
		if projection.InputChanged || projection.ActivityChanged {
			kind := MonitorActivity
			if projection.InputChanged {
				kind = MonitorInputState
			}
			send(MonitorEvent{Type: kind, InputState: projection.InputState, Activity: projection.Activity})
		}
		if projection.ContextChanged {
			send(MonitorEvent{Type: MonitorContextUsage, ContextTokens: projection.ContextTokens})
		}
	}
	poll := func() error {
		info, err := os.Lstat(spoolPath)
		if err != nil || !info.Mode().IsRegular() {
			if err != nil {
				return err
			}
			return errors.New("OpenCode event spool is not a regular file")
		}
		if info.Size() > MaxSpoolBytes || info.Size() < int64(offset) {
			return errors.New("OpenCode event spool changed unexpectedly")
		}
		data, err := os.ReadFile(spoolPath)
		if err != nil {
			return err
		}
		if len(data) > MaxSpoolBytes || len(data) < offset {
			return errors.New("OpenCode event spool changed unexpectedly")
		}
		for offset < len(data) {
			relativeEnd := bytes.IndexByte(data[offset:], '\n')
			if relativeEnd < 0 {
				if len(data)-offset > MaxRecordBytes {
					return errors.New("OpenCode event record exceeds size limit")
				}
				break
			}
			lineEnd := offset + relativeEnd
			if lineEnd == offset || lineEnd-offset > MaxRecordBytes {
				return errors.New("malformed OpenCode event record")
			}
			fact, known, err := DecodeFact(data[offset:lineEnd])
			if err != nil {
				return err
			}
			offset = lineEnd + 1
			if !known {
				continue
			}
			if fact.Type == "observer_failed" {
				return errors.New("OpenCode observer failed")
			}
			if fact.Type == "observer_initialized" {
				reducer.Apply(fact)
				if !initialized {
					initialized = true
					if startupChannel != nil && !startup.Stop() {
						select {
						case <-startup.C:
						default:
						}
					}
					startupChannel = nil
					send(MonitorEvent{Type: MonitorObservability, ObservabilityStatus: protocol.ObservabilityHealthy, Activity: protocol.ActivityIdle})
				}
				continue
			}
			emitProjection(reducer.Apply(fact))
		}
		return nil
	}

	for {
		if err := poll(); err != nil {
			send(MonitorEvent{
				Type: MonitorObservability, ObservabilityStatus: protocol.ObservabilityDegraded, Activity: protocol.ActivityUnknown,
				ObservabilityMessage: "OpenCode monitoring stopped because its event spool is unreadable or malformed.",
			})
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-startupChannel:
			startupChannel = nil
			send(MonitorEvent{
				Type: MonitorObservability, ObservabilityStatus: protocol.ObservabilityDegraded, Activity: protocol.ActivityUnknown,
				ObservabilityMessage: "OpenCode monitoring did not observe observer initialization within 15 seconds.",
			})
		case <-ticker.C:
		}
	}
}
