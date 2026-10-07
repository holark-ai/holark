package terminals

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

const (
	CheckpointFormatANSI         = "holark.ansi.v1"
	DefaultScrollbackLines       = 10_000
	DefaultCheckpointReplayBytes = 10 * 1024 * 1024
	DefaultDetachedTailBytes     = 128 * 1024
	DefaultMaximumTailBytes      = 2 * 1024 * 1024
	DefaultAggregateTailBytes    = 1024 * 1024 * 1024
)

var ErrCheckpointUnavailable = errors.New("terminal checkpoint unavailable")

type RestorationQuality string

const (
	RestorationTrusted  RestorationQuality = "trusted"
	RestorationDegraded RestorationQuality = "degraded"
)

func (quality RestorationQuality) Valid() bool {
	return quality == RestorationTrusted || quality == RestorationDegraded
}

// TerminalCheckpoint is a bounded, replayable rendering checkpoint. Sequence
// is the host-owned process notification sequence at the snapshot barrier.
// ReplayPayload is terminal output, not an application transcript.
type TerminalCheckpoint struct {
	TerminalID    TerminalID         `json:"terminal_id"`
	Sequence      uint64             `json:"sequence"`
	Dimensions    Dimensions         `json:"dimensions"`
	FormatVersion string             `json:"format_version"`
	ReplayPayload []byte             `json:"replay_payload"`
	Checksum      string             `json:"checksum"`
	Quality       RestorationQuality `json:"quality"`
}

func NewCheckpoint(id TerminalID, sequence uint64, dimensions Dimensions, payload []byte) TerminalCheckpoint {
	return NewCheckpointWithQuality(id, sequence, dimensions, payload, RestorationTrusted)
}

func NewCheckpointWithQuality(id TerminalID, sequence uint64, dimensions Dimensions, payload []byte, quality RestorationQuality) TerminalCheckpoint {
	digest := sha256.Sum256(payload)
	return TerminalCheckpoint{
		TerminalID: id, Sequence: sequence, Dimensions: dimensions,
		// Keep an empty payload non-nil so encoding/json emits an empty Base64 string instead of null.
		FormatVersion: CheckpointFormatANSI, ReplayPayload: append([]byte{}, payload...),
		Checksum: hex.EncodeToString(digest[:]), Quality: quality,
	}
}

func (checkpoint TerminalCheckpoint) Validate() error {
	if !checkpoint.TerminalID.Valid() || checkpoint.Dimensions.Validate() != nil ||
		checkpoint.FormatVersion != CheckpointFormatANSI || checkpoint.Checksum == "" ||
		!checkpoint.Quality.Valid() ||
		len(checkpoint.ReplayPayload) > DefaultCheckpointReplayBytes {
		return errors.New("invalid terminal checkpoint")
	}
	digest := sha256.Sum256(checkpoint.ReplayPayload)
	if checkpoint.Checksum != hex.EncodeToString(digest[:]) {
		return errors.New("terminal checkpoint checksum does not match")
	}
	return nil
}

// TerminalAttachment contains the one opaque host-issued token which fences
// the sole interactive renderer for a terminal.
type TerminalAttachment struct {
	Token string `json:"token"`
}

func (attachment TerminalAttachment) Valid() bool {
	return attachment.Token != "" && len(attachment.Token) <= 128
}

// ScreenTracker is the narrow domain-owned boundary implemented by the
// terminal-host VT adapter. Implementations must serialize Write, Resize and
// Snapshot calls for an individual terminal.
type ScreenTracker interface {
	Write(sequence uint64, data []byte) error
	Resize(sequence uint64, dimensions Dimensions) error
	Snapshot(sequence uint64) (TerminalCheckpoint, error)
}

type ScreenTrackerFactory interface {
	New(TerminalID, Dimensions) (ScreenTracker, error)
}

type ProcessNotificationKind string

const (
	ProcessOutput ProcessNotificationKind = "output"
	ProcessResize ProcessNotificationKind = "resize"
	ProcessExit   ProcessNotificationKind = "exit"
)

// ProcessNotification is ephemeral host state used to advance a checkpoint.
// It is intentionally not an application transcript or persistence journal.
type ProcessNotification struct {
	TerminalID     TerminalID              `json:"terminal_id"`
	Sequence       uint64                  `json:"sequence"`
	Kind           ProcessNotificationKind `json:"kind"`
	Data           []byte                  `json:"data,omitempty"`
	Characters     uint64                  `json:"characters,omitempty"`
	Dimensions     Dimensions              `json:"dimensions,omitempty"`
	ResizeRevision uint64                  `json:"resize_revision,omitempty"`
	ExitCode       *int                    `json:"exit_code,omitempty"`
	OccurredAt     time.Time               `json:"occurred_at"`
}

func (notification ProcessNotification) Validate() error {
	if !notification.TerminalID.Valid() || notification.Sequence == 0 ||
		notification.OccurredAt.IsZero() {
		return errors.New("invalid terminal process notification")
	}
	switch notification.Kind {
	case ProcessOutput:
		if len(notification.Data) == 0 || notification.Characters == 0 {
			return errors.New("invalid terminal output notification")
		}
	case ProcessResize:
		if notification.Dimensions.Validate() != nil || notification.ResizeRevision == 0 {
			return errors.New("invalid terminal resize notification")
		}
	case ProcessExit:
		if notification.ExitCode == nil {
			return errors.New("invalid terminal exit notification")
		}
	default:
		return errors.New("invalid terminal process notification kind")
	}
	return nil
}

// TerminalRestore is captured under one host-side barrier. Output produced
// after Checkpoint.Sequence appears once, in order, in Tail or a later update.
type TerminalRestore struct {
	Attachment   TerminalAttachment    `json:"attachment"`
	Checkpoint   TerminalCheckpoint    `json:"checkpoint"`
	Tail         []ProcessNotification `json:"tail,omitempty"`
	LastSequence uint64                `json:"last_sequence"`
}

func (restore TerminalRestore) Validate() error {
	if !restore.Attachment.Valid() || restore.Checkpoint.Validate() != nil ||
		restore.LastSequence < restore.Checkpoint.Sequence {
		return errors.New("invalid terminal restore")
	}
	previous := restore.Checkpoint.Sequence
	for _, notification := range restore.Tail {
		if notification.Validate() != nil || notification.TerminalID != restore.Checkpoint.TerminalID ||
			notification.Sequence != previous+1 {
			return errors.New("invalid terminal restore tail")
		}
		previous = notification.Sequence
	}
	if previous != restore.LastSequence {
		return errors.New("terminal restore tail does not reach its barrier")
	}
	return nil
}

// TerminalUpdateStream carries bounded live state from one host process.
// Checkpoint is present when the caller's cursor predates a compaction barrier.
type TerminalUpdateStream struct {
	TerminalID    TerminalID            `json:"terminal_id"`
	Checkpoint    *TerminalCheckpoint   `json:"checkpoint,omitempty"`
	Notifications []ProcessNotification `json:"notifications,omitempty"`
	LastSequence  uint64                `json:"last_sequence"`
}

// ProcessCursor identifies the last host-owned state sent to one attachment.
type ProcessCursor struct {
	TerminalID         TerminalID `json:"terminal_id"`
	Sequence           uint64     `json:"sequence"`
	CheckpointSequence uint64     `json:"checkpoint_sequence,omitempty"`
}

func (cursor ProcessCursor) Valid() bool {
	return cursor.TerminalID.Valid() && cursor.CheckpointSequence <= cursor.Sequence
}

func (stream TerminalUpdateStream) Validate() error {
	if !stream.TerminalID.Valid() ||
		stream.Checkpoint == nil && len(stream.Notifications) == 0 {
		return errors.New("invalid terminal process update")
	}
	previous := uint64(0)
	if stream.Checkpoint != nil {
		if stream.Checkpoint.Validate() != nil || stream.Checkpoint.TerminalID != stream.TerminalID {
			return errors.New("invalid terminal process checkpoint")
		}
		previous = stream.Checkpoint.Sequence
	}
	for index, notification := range stream.Notifications {
		if notification.Validate() != nil || notification.TerminalID != stream.TerminalID ||
			(previous != 0 || stream.Checkpoint != nil || index > 0) && notification.Sequence != previous+1 {
			return errors.New("invalid terminal process notification sequence")
		}
		previous = notification.Sequence
	}
	if stream.LastSequence < previous {
		return errors.New("terminal process update exceeds its barrier")
	}
	return nil
}
