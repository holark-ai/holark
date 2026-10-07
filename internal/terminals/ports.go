package terminals

import (
	"context"
	"errors"
)

var (
	ErrNotFound              = errors.New("terminal not found")
	ErrAttachmentRequired    = errors.New("terminal attachment is required")
	ErrStaleAttachment       = errors.New("terminal attachment is stale")
	ErrNotRunning            = errors.New("terminal is not accepting input")
	ErrStaleResize           = errors.New("terminal resize revision is stale")
	ErrConnectionUnavailable = errors.New("terminal connection is temporarily unavailable")
	ErrProcessLost           = errors.New("terminal process is no longer available")
	ErrProcessEnded          = errors.New("terminal process has ended")
)

type LaunchKind string

const (
	LaunchCommand LaunchKind = "command"
	LaunchShell   LaunchKind = "shell"
)

type LaunchSpec struct {
	TerminalID  TerminalID `json:"terminal_id"`
	Kind        LaunchKind `json:"kind"`
	Command     string     `json:"command,omitempty"`
	Arguments   []string   `json:"arguments,omitempty"`
	Environment []string   `json:"environment,omitempty"`
	CWD         string     `json:"cwd,omitempty"`
	Dimensions  Dimensions `json:"dimensions"`
}

// The command payloads below are shared by browser, server, node, and host
// envelopes. Request correlation and transport routing stay outside them.
type AttachCommand struct {
	TerminalID TerminalID `json:"terminal_id"`
}

type DetachCommand struct {
	TerminalID TerminalID         `json:"terminal_id"`
	Attachment TerminalAttachment `json:"attachment"`
}

type InputCommand struct {
	TerminalID TerminalID         `json:"terminal_id"`
	Attachment TerminalAttachment `json:"attachment,omitempty"`
	Data       []byte             `json:"data_base64"`
}

type ResizeCommand struct {
	TerminalID TerminalID         `json:"terminal_id"`
	Attachment TerminalAttachment `json:"attachment,omitempty"`
	Revision   uint64             `json:"revision"`
	Dimensions Dimensions         `json:"dimensions"`
}

type SignalCommand struct {
	TerminalID TerminalID `json:"terminal_id"`
	Signal     string     `json:"signal"`
}

type CloseCommand struct {
	TerminalID TerminalID         `json:"terminal_id"`
	Attachment TerminalAttachment `json:"attachment,omitempty"`
}

type ProcessCompletion struct {
	TerminalID TerminalID `json:"terminal_id"`
	ExitCode   int        `json:"exit_code"`
}

// LiveAttachment is a host-owned restore barrier followed by ordered live
// frames. Closing Updates means the upstream node/host proxy was interrupted;
// callers establish a fresh attachment rather than replaying server state.
type LiveAttachment struct {
	Restore TerminalRestore
	Updates <-chan TerminalUpdateStream
}

// LocalGateway is the replaceable transport boundary owned by the terminal
// domain. Input and resize are sent once and are never retried after an
// ambiguous transport interruption.
type LocalGateway interface {
	Launch(context.Context, string, LaunchSpec) error
	Attach(context.Context, string, TerminalID) (LiveAttachment, error)
	Detach(context.Context, string, TerminalID, TerminalAttachment) error
	Input(context.Context, string, TerminalID, TerminalAttachment, []byte) error
	Resize(context.Context, string, TerminalID, TerminalAttachment, uint64, Dimensions) error
	Signal(context.Context, string, TerminalID, string) error
	Close(context.Context, string, TerminalID, TerminalAttachment) error
}
