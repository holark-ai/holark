package terminalhost

import (
	"errors"

	"github.com/holark-ai/holark/internal/terminals"
)

const (
	ProtocolVersion = terminals.ProtocolVersion
	MaxFrameBytes   = 16 * 1024 * 1024
)

type RequestType string

const (
	RequestHello             RequestType = "hello"
	RequestLaunch            RequestType = "launch"
	RequestInput             RequestType = "input"
	RequestResize            RequestType = "resize"
	RequestSignal            RequestType = "signal"
	RequestClose             RequestType = "close"
	RequestInventory         RequestType = "inventory"
	RequestTerminalAttach    RequestType = "terminal_attach"
	RequestTerminalDetach    RequestType = "terminal_detach"
	RequestAttachmentUpdates RequestType = "attachment_updates"
	RequestCompletions       RequestType = "completions"
)

type Cursor = terminals.ProcessCursor

type Request struct {
	Version         int                      `json:"version"`
	Type            RequestType              `json:"type"`
	RequestID       string                   `json:"request_id"`
	Token           string                   `json:"token,omitempty"`
	HostID          string                   `json:"host_id,omitempty"`
	ControllerID    string                   `json:"controller_id,omitempty"`
	ControllerEpoch uint64                   `json:"controller_epoch,omitempty"`
	Launch          *terminals.LaunchSpec    `json:"launch,omitempty"`
	Input           *terminals.InputCommand  `json:"input,omitempty"`
	Resize          *terminals.ResizeCommand `json:"resize,omitempty"`
	Signal          *terminals.SignalCommand `json:"signal,omitempty"`
	Close           *terminals.CloseCommand  `json:"close,omitempty"`
	Attach          *terminals.AttachCommand `json:"attach,omitempty"`
	Detach          *terminals.DetachCommand `json:"detach,omitempty"`
	Cursor          Cursor                   `json:"cursor,omitempty"`
	Acknowledged    []terminals.TerminalID   `json:"acknowledged,omitempty"`
	MaximumBytes    int                      `json:"maximum_bytes,omitempty"`
	WaitMillis      int                      `json:"wait_millis,omitempty"`
}

type Response struct {
	Version         int                              `json:"version"`
	RequestID       string                           `json:"request_id"`
	OK              bool                             `json:"ok"`
	HostID          string                           `json:"host_id,omitempty"`
	ControllerEpoch uint64                           `json:"controller_epoch,omitempty"`
	Code            string                           `json:"code,omitempty"`
	Message         string                           `json:"message,omitempty"`
	PID             int                              `json:"pid,omitempty"`
	Inventory       []terminals.TerminalID           `json:"inventory,omitempty"`
	Restore         *terminals.TerminalRestore       `json:"restore,omitempty"`
	Updates         []terminals.TerminalUpdateStream `json:"updates,omitempty"`
	Completions     []terminals.ProcessCompletion    `json:"completions,omitempty"`
}

func validateRequest(request Request, hello bool) error {
	if request.Version != ProtocolVersion || request.RequestID == "" {
		return errors.New("invalid terminal host request envelope")
	}
	if hello {
		if request.Type != RequestHello || request.Token == "" || request.ControllerID == "" ||
			len(request.ControllerID) > 128 || request.ControllerEpoch != 0 {
			return errors.New("terminal host handshake required")
		}
		return nil
	}
	if request.Token != "" || request.HostID != "" || request.ControllerID != "" ||
		request.Type == RequestHello || request.ControllerEpoch == 0 {
		return errors.New("invalid terminal host request")
	}
	payloads := 0
	for _, present := range []bool{
		request.Launch != nil, request.Input != nil, request.Resize != nil, request.Signal != nil,
		request.Close != nil, request.Attach != nil, request.Detach != nil,
	} {
		if present {
			payloads++
		}
	}
	valid := false
	switch request.Type {
	case RequestLaunch:
		valid = payloads == 1 && request.Launch != nil
	case RequestInput:
		valid = payloads == 1 && request.Input != nil
	case RequestResize:
		valid = payloads == 1 && request.Resize != nil
	case RequestSignal:
		valid = payloads == 1 && request.Signal != nil
	case RequestClose:
		valid = payloads == 1 && request.Close != nil
	case RequestTerminalAttach:
		valid = payloads == 1 && request.Attach != nil
	case RequestTerminalDetach:
		valid = payloads == 1 && request.Detach != nil
	case RequestAttachmentUpdates:
		valid = payloads == 1 && request.Detach != nil && request.Cursor.Valid()
	case RequestInventory, RequestCompletions:
		valid = payloads == 0
	}
	if !valid {
		return errors.New("invalid terminal host request payload")
	}
	return nil
}
