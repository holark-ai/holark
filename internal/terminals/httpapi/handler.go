package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/holark-ai/holark/internal/sessionterminals"
	"github.com/holark-ai/holark/internal/terminals"
)

type terminalCommandEnvelope struct {
	Version   int             `json:"version"`
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

type terminalCommand struct {
	Type      string
	RequestID string
	Payload   any
}

type terminalDeliveryFrame struct {
	Version   int                             `json:"version"`
	Type      string                          `json:"type"`
	RequestID string                          `json:"request_id,omitempty"`
	Restore   *terminals.TerminalRestore      `json:"restore,omitempty"`
	Update    *terminals.TerminalUpdateStream `json:"update,omitempty"`
	Code      string                          `json:"code,omitempty"`
	Message   string                          `json:"message,omitempty"`
}

const (
	terminalConnectionUnavailable = "connection_unavailable"
	terminalLost                  = "terminal_lost"
)

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

// New exposes the protocol-v12 terminal boundary without
// constructing the legacy hosted HTTP server. Authentication is supplied by
// the enclosing local application shell.
type Coordinator interface {
	Resolve(terminals.TerminalID) (sessionterminals.Binding, error)
	Attach(context.Context, terminals.TerminalID) (*sessionterminals.Attachment, error)
	Detach(context.Context, terminals.TerminalID, *sessionterminals.Attachment) error
	Input(context.Context, terminals.TerminalID, *sessionterminals.Attachment, []byte) error
	Resize(context.Context, terminals.TerminalID, *sessionterminals.Attachment, uint64, terminals.Dimensions) error
	CloseAttached(context.Context, terminals.TerminalID, *sessionterminals.Attachment) error
}

func NewHandler(coordinator Coordinator, diagnostic Diagnostic) *Handler {
	return &Handler{coordinator: coordinator, diagnostic: diagnostic}
}
func (handler *Handler) Probe(w http.ResponseWriter, r *http.Request)  { handler.probeTerminal(w, r) }
func (handler *Handler) Attach(w http.ResponseWriter, r *http.Request) { handler.attachTerminal(w, r) }

type Diagnostic interface {
	Record(string, string, map[string]any)
}
type Handler struct {
	coordinator Coordinator
	diagnostic  Diagnostic
}

func New(coordinator Coordinator, diagnostic Diagnostic) http.Handler {
	handler := &Handler{coordinator: coordinator, diagnostic: diagnostic}
	mux := http.NewServeMux()
	owned := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			binding, err := coordinator.Resolve(terminals.TerminalID(r.PathValue("terminalID")))
			if err != nil || binding.SessionID != r.PathValue("holonID") {
				http.NotFound(w, r)
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("HEAD /api/v1/holons/{holonID}/terminals/{terminalID}/attach", owned(handler.probeTerminal))
	mux.HandleFunc("GET /api/v1/holons/{holonID}/terminals/{terminalID}/attach", owned(handler.attachTerminal))
	return mux
}

func (handler *Handler) probeTerminal(response http.ResponseWriter, request *http.Request) {
	if handler.coordinator == nil {
		writeError(response, http.StatusServiceUnavailable, "terminal_unavailable", "Terminal service is unavailable.")
		return
	}
	id := terminals.TerminalID(request.PathValue("terminalID"))
	if !id.Valid() {
		writeError(response, http.StatusBadRequest, "invalid_request", "Terminal ID is invalid.")
		return
	}
	handler.recordTerminalDiagnostic("terminal_availability_requested", id, nil)
	if _, err := handler.coordinator.Resolve(id); err != nil {
		handler.recordTerminalDiagnostic("terminal_availability_failed", id, nil)
		if errors.Is(err, terminals.ErrNotFound) {
			writeError(response, http.StatusNotFound, "terminal_not_found", "Terminal not found.")
		} else {
			writeError(response, http.StatusInternalServerError, "terminal_failed", "Terminal binding is unavailable.")
		}
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) attachTerminal(response http.ResponseWriter, request *http.Request) {
	if handler.coordinator == nil {
		writeError(response, http.StatusServiceUnavailable, "terminal_unavailable", "Terminal service is unavailable.")
		return
	}
	id := terminals.TerminalID(request.PathValue("terminalID"))
	if !id.Valid() {
		writeError(response, http.StatusBadRequest, "invalid_request", "Terminal ID is invalid.")
		return
	}
	handler.recordTerminalDiagnostic("terminal_attachment_requested", id, nil)
	connection, err := websocket.Accept(response, request, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	closeReason := "connection_closed"
	defer func() {
		handler.recordTerminalDiagnostic("terminal_attachment_closed", id, map[string]any{"reason": closeReason})
	}()
	handler.recordTerminalDiagnostic("terminal_websocket_opened", id, nil)
	connection.SetReadLimit(128 * 1024)
	helloContext, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	_, data, err := connection.Read(helloContext)
	cancel()
	if err != nil {
		closeReason = "browser_read_closed"
		return
	}
	hello, err := decodeTerminalCommand(data)
	attach, attached := hello.Payload.(*terminals.AttachCommand)
	if err != nil || hello.Type != "attach" || !attached || attach.TerminalID != id {
		closeReason = "invalid_attach_command"
		_ = connection.Close(websocket.StatusPolicyViolation, "invalid terminal attachment")
		return
	}
	attachment, err := handler.coordinator.Attach(request.Context(), id)
	if err != nil {
		closeReason = "attach_failed"
		handler.recordTerminalDiagnostic("terminal_attachment_failed", id, map[string]any{"reason": "terminal_service"})
		_ = writeTerminalV5(connection, terminalErrorFrame("", err))
		return
	}
	handler.recordTerminalDiagnostic("terminal_attachment_opened", id, map[string]any{
		"checkpoint_sequence": attachment.Restore.Checkpoint.Sequence, "last_sequence": attachment.Restore.LastSequence,
		"notification_count": len(attachment.Restore.Tail), "checkpoint_bytes": len(attachment.Restore.Checkpoint.ReplayPayload),
	})
	defer func() {
		detachContext, stopDetach := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopDetach()
		_ = handler.coordinator.Detach(detachContext, id, attachment)
	}()
	restore := attachment.Restore
	if err := writeTerminalV5(connection, terminalDeliveryFrame{
		Type: "attached", Restore: &restore,
	}); err != nil {
		closeReason = "attached_frame_failed"
		return
	}
	handler.recordTerminalDiagnostic("terminal_frame_forwarded", id, map[string]any{
		"delivery": "attached", "checkpoint_sequence": restore.Checkpoint.Sequence, "last_sequence": restore.LastSequence,
		"notification_count": len(restore.Tail), "bytes": terminalRestoreBytes(restore),
	})
	commands := make(chan terminalCommand, 32)
	readError := make(chan error, 1)
	go readTerminalV5(request.Context(), connection, commands, readError)
	for {
		select {
		case <-request.Context().Done():
			closeReason = "request_cancelled"
			return
		case <-readError:
			closeReason = "browser_read_closed"
			return
		case <-attachment.Retired:
			closeReason = "attachment_transferred"
			_ = writeTerminalV5(connection, terminalDeliveryFrame{
				Type: "retired", Code: "attachment_transferred",
				Message: "This terminal was attached in another view.",
			})
			return
		case command := <-commands:
			if !attachmentFrameMatches(command, id, attachment.Restore.Attachment) {
				_ = writeTerminalV5(connection, terminalErrorFrame(command.RequestID, terminals.ErrStaleAttachment))
				return
			}
			switch command.Type {
			case "input":
				input := command.Payload.(*terminals.InputCommand)
				err = handler.coordinator.Input(request.Context(), id, attachment, input.Data)
				if err != nil && writeTerminalV5(connection, terminalErrorFrame("", err)) != nil {
					return
				}
			case "resize":
				resize := command.Payload.(*terminals.ResizeCommand)
				err := handler.coordinator.Resize(request.Context(), id, attachment, resize.Revision, resize.Dimensions)
				if err != nil && writeTerminalV5(connection, terminalErrorFrame("", err)) != nil {
					return
				}
			case "close":
				closeErr := handler.coordinator.CloseAttached(request.Context(), id, attachment)
				if writeTerminalV5(connection, terminalResultFrame(command.RequestID, closeErr)) != nil {
					return
				}
			default:
				if writeTerminalV5(connection, terminalErrorFrame(command.RequestID, errors.New("unknown terminal command"))) != nil {
					return
				}
			}
		case update, ok := <-attachment.Updates:
			if !ok {
				// Node loss is temporary. Closing the browser socket invokes the
				// existing reconnect loop without publishing permanent loss.
				closeReason = "node_attachment_closed"
				return
			}
			if err := writeTerminalV5(connection, terminalDeliveryFrame{
				Type: "updates", Update: &update,
			}); err != nil {
				closeReason = "update_frame_failed"
				return
			}
			handler.recordTerminalDiagnostic("terminal_frame_forwarded", id, map[string]any{
				"delivery": "updates", "checkpoint_sequence": terminalCheckpointSequence(update),
				"last_sequence": update.LastSequence, "notification_count": len(update.Notifications), "bytes": terminalUpdateBytes(update),
			})
		}
	}
}

func (handler *Handler) recordTerminalDiagnostic(event string, id terminals.TerminalID, fields map[string]any) {
	if handler.diagnostic == nil {
		return
	}
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["terminal_id"] = string(id)
	handler.diagnostic.Record("http-terminal", event, fields)
}

func terminalRestoreBytes(restore terminals.TerminalRestore) int {
	return len(restore.Checkpoint.ReplayPayload) + terminalNotificationBytes(restore.Tail)
}

func terminalUpdateBytes(update terminals.TerminalUpdateStream) int {
	total := terminalNotificationBytes(update.Notifications)
	if update.Checkpoint != nil {
		total += len(update.Checkpoint.ReplayPayload)
	}
	return total
}

func terminalNotificationBytes(notifications []terminals.ProcessNotification) int {
	total := 0
	for _, notification := range notifications {
		total += len(notification.Data)
	}
	return total
}

func terminalCheckpointSequence(update terminals.TerminalUpdateStream) uint64 {
	if update.Checkpoint == nil {
		return 0
	}
	return update.Checkpoint.Sequence
}

func attachmentFrameMatches(command terminalCommand, id terminals.TerminalID, attachment terminals.TerminalAttachment) bool {
	switch payload := command.Payload.(type) {
	case *terminals.InputCommand:
		return payload.TerminalID == id && payload.Attachment == attachment
	case *terminals.ResizeCommand:
		return payload.TerminalID == id && payload.Attachment == attachment
	case *terminals.CloseCommand:
		return payload.TerminalID == id && payload.Attachment == attachment
	default:
		return false
	}
}

func readTerminalV5(ctx context.Context, connection *websocket.Conn, commands chan<- terminalCommand, failed chan<- error) {
	for {
		messageType, data, err := connection.Read(ctx)
		if err != nil {
			failed <- err
			return
		}
		if messageType != websocket.MessageText {
			failed <- errors.New("terminal messages must be text")
			return
		}
		command, err := decodeTerminalCommand(data)
		if err != nil {
			failed <- errors.New("invalid terminal command")
			return
		}
		select {
		case commands <- command:
		case <-ctx.Done():
			return
		}
	}
}

func decodeTerminalCommand(data []byte) (terminalCommand, error) {
	var envelope terminalCommandEnvelope
	if err := decodeTerminalFrame(data, &envelope); err != nil || envelope.Version != terminals.ProtocolVersion {
		return terminalCommand{}, errors.New("invalid terminal command envelope")
	}
	command := terminalCommand{Type: envelope.Type, RequestID: envelope.RequestID}
	switch envelope.Type {
	case "attach":
		payload := &terminals.AttachCommand{}
		if err := decodeTerminalFrame(envelope.Payload, payload); err != nil || !payload.TerminalID.Valid() {
			return terminalCommand{}, errors.New("invalid terminal attachment")
		}
		command.Payload = payload
	case "input":
		payload := &terminals.InputCommand{}
		if err := decodeTerminalFrame(envelope.Payload, payload); err != nil || !payload.TerminalID.Valid() ||
			!payload.Attachment.Valid() || len(payload.Data) == 0 || len(payload.Data) > 64*1024 {
			return terminalCommand{}, errors.New("invalid terminal input")
		}
		command.Payload = payload
	case "resize":
		payload := &terminals.ResizeCommand{}
		if err := decodeTerminalFrame(envelope.Payload, payload); err != nil || !payload.TerminalID.Valid() ||
			!payload.Attachment.Valid() || payload.Revision == 0 || payload.Dimensions.Validate() != nil {
			return terminalCommand{}, errors.New("invalid terminal resize")
		}
		command.Payload = payload
	case "close":
		payload := &terminals.CloseCommand{}
		if err := decodeTerminalFrame(envelope.Payload, payload); err != nil || !validRequestID(envelope.RequestID) ||
			!payload.TerminalID.Valid() || !payload.Attachment.Valid() {
			return terminalCommand{}, errors.New("invalid terminal close")
		}
		command.Payload = payload
	default:
		return terminalCommand{}, errors.New("unknown terminal command")
	}
	return command, nil
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character != '-' && character != '_' && (character < '0' || character > '9') && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') {
			return false
		}
	}
	return true
}

func decodeTerminalFrame(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing terminal JSON")
	}
	return nil
}

func terminalResultFrame(requestID string, err error) terminalDeliveryFrame {
	if err != nil {
		return terminalErrorFrame(requestID, err)
	}
	return terminalDeliveryFrame{Type: "result", RequestID: requestID}
}

func terminalErrorFrame(requestID string, err error) terminalDeliveryFrame {
	code := "terminal_failed"
	message := "Terminal operation failed."
	switch {
	case temporaryTerminalConnectionError(err):
		code, message = terminalConnectionUnavailable, "Terminal connection is temporarily unavailable."
	case errors.Is(err, terminals.ErrNotFound):
		code, message = "terminal_not_found", "Terminal not found."
	case errors.Is(err, terminals.ErrProcessLost):
		code, message = terminalLost, "Terminal process is no longer available."
	case errors.Is(err, terminals.ErrProcessEnded):
		code, message = "terminal_ended", "Terminal process has ended."
	case errors.Is(err, terminals.ErrNotRunning):
		code, message = "terminal_not_running", "Terminal is not accepting input."
	case errors.Is(err, terminals.ErrStaleResize):
		code, message = "stale_resize", "Terminal resize revision is stale."
	case errors.Is(err, terminals.ErrStaleAttachment), errors.Is(err, terminals.ErrAttachmentRequired):
		code, message = "stale_attachment", "This terminal attachment is no longer current."
	default:
		if text := strings.TrimSpace(err.Error()); text != "" && len(text) <= 256 {
			message = text
		}
	}
	return terminalDeliveryFrame{Type: "error", RequestID: requestID, Code: code, Message: message}
}

func temporaryTerminalConnectionError(err error) bool {
	return errors.Is(err, terminals.ErrConnectionUnavailable)
}

func writeTerminalV5(connection *websocket.Conn, message terminalDeliveryFrame) error {
	message.Version = terminals.ProtocolVersion
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return connection.Write(ctx, websocket.MessageText, data)
}
