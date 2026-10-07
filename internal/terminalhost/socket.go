package terminalhost

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/holark-ai/holark/internal/terminals"
)

type Server struct {
	manager     *Manager
	token       string
	hostID      string
	orphanGrace time.Duration
	onExpired   func()

	controllerMu     sync.Mutex
	controller       *hostController
	lastControllerID string
	fenced           map[string]struct{}
	nextEpoch        uint64
	orphanTimer      *time.Timer
	listener         net.Listener
	expired          chan struct{}
	expireOnce       sync.Once
}

type ServerOptions struct {
	HostID      string
	OrphanGrace time.Duration
	OnExpired   func()
}

type hostController struct {
	id         string
	epoch      uint64
	connection net.Conn
	cancel     context.CancelFunc
}

const maximumInFlightHostRequests = 64

type connectionResponseWriter struct {
	connection net.Conn
	writer     *bufio.Writer
	cancel     context.CancelFunc
	mu         sync.Mutex
}

func NewServer(manager *Manager, token string) (*Server, error) {
	return NewServerWithOptions(manager, token, ServerOptions{})
}

func NewServerWithOptions(manager *Manager, token string, options ServerOptions) (*Server, error) {
	if manager == nil || len(token) < 32 || len(token) > 512 {
		return nil, errors.New("terminal host manager and handshake token are required")
	}
	if options.HostID == "" {
		options.HostID = "embedded-terminal-host"
	}
	if len(options.HostID) > 128 {
		return nil, errors.New("terminal host identity is invalid")
	}
	if options.OrphanGrace == 0 {
		options.OrphanGrace = 60 * time.Second
	}
	if options.OrphanGrace < 10*time.Millisecond || options.OrphanGrace > 10*time.Minute {
		return nil, errors.New("terminal host orphan grace is invalid")
	}
	return &Server{
		manager: manager, token: token, hostID: options.HostID, orphanGrace: options.OrphanGrace,
		onExpired: options.OnExpired, fenced: make(map[string]struct{}), expired: make(chan struct{}),
	}, nil
}

// Listen creates a private Unix socket. The parent runtime directory must
// already be mode 0700; the socket itself is forced to mode 0600.
func Listen(socketPath string) (net.Listener, error) {
	if socketPath == "" || len(socketPath) > 100 {
		return nil, errors.New("invalid terminal host socket path")
	}
	directory := filepath.Dir(socketPath)
	info, err := os.Stat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, errors.New("terminal host runtime directory must be mode 0700")
	}
	if existing, statErr := os.Lstat(socketPath); statErr == nil {
		if existing.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("refusing to replace non-socket terminal host path")
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

func (server *Server) Serve(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		return errors.New("terminal host listener is required")
	}
	server.controllerMu.Lock()
	server.listener = listener
	server.startOrphanTimerLocked()
	server.controllerMu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
		case <-server.expired:
		}
		_ = listener.Close()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return ErrOrphanExpired
			}
			return err
		}
		go server.serveConnection(ctx, connection)
	}
}

func (server *Server) serveConnection(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	reader := bufio.NewReaderSize(connection, MaxFrameBytes)
	writer := bufio.NewWriterSize(connection, 64*1024)
	_ = connection.SetDeadline(contextDeadline(ctx, terminalHostIOTimeout))
	hello, err := readRequest(reader)
	if err != nil || validateRequest(hello, true) != nil ||
		subtle.ConstantTimeCompare([]byte(hello.Token), []byte(server.token)) != 1 ||
		(hello.HostID != "" && hello.HostID != server.hostID) {
		_ = writeResponse(writer, Response{Version: ProtocolVersion, RequestID: hello.RequestID, Code: "unauthorized", Message: "terminal host handshake failed"})
		return
	}
	connectionContext, cancel := context.WithCancel(ctx)
	controller, attachErr := server.attachController(hello.ControllerID, connection, cancel)
	if attachErr != nil {
		cancel()
		_ = writeResponse(writer, Response{
			Version: ProtocolVersion, RequestID: hello.RequestID,
			Code: "stale_controller", Message: "terminal host controller was fenced",
		})
		return
	}
	defer server.detachController(controller)
	if err := writeResponse(writer, Response{
		Version: ProtocolVersion, RequestID: hello.RequestID, OK: true,
		HostID: server.hostID, ControllerEpoch: controller.epoch,
	}); err != nil {
		return
	}
	_ = connection.SetDeadline(time.Time{})
	responses := &connectionResponseWriter{connection: connection, writer: writer, cancel: cancel}
	requestSlots := make(chan struct{}, maximumInFlightHostRequests)
	var requests sync.WaitGroup
	connectionClosed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-connectionClosed:
		}
	}()
	defer func() {
		close(connectionClosed)
		cancel()
		_ = connection.Close()
		requests.Wait()
	}()
	for {
		request, readErr := readRequest(reader)
		if readErr != nil {
			return
		}
		if validateRequest(request, false) != nil {
			_ = responses.write(Response{
				Version: ProtocolVersion, RequestID: request.RequestID,
				Code: "invalid_request", Message: "invalid terminal host request",
			})
			return
		}
		if request.ControllerEpoch != controller.epoch || !server.controllerCurrent(controller) {
			_ = responses.write(Response{
				Version: ProtocolVersion, RequestID: request.RequestID,
				Code: "stale_controller", Message: "terminal host controller was fenced",
			})
			return
		}
		select {
		case requestSlots <- struct{}{}:
		case <-connectionContext.Done():
			return
		}
		requests.Add(1)
		go func(request Request) {
			defer requests.Done()
			defer func() { <-requestSlots }()
			response := server.dispatch(connectionContext, request)
			response.Version = ProtocolVersion
			response.RequestID = request.RequestID
			_ = responses.write(response)
		}(request)
	}
}

func (responses *connectionResponseWriter) write(response Response) error {
	responses.mu.Lock()
	defer responses.mu.Unlock()
	_ = responses.connection.SetWriteDeadline(time.Now().Add(terminalHostIOTimeout))
	err := writeResponse(responses.writer, response)
	_ = responses.connection.SetWriteDeadline(time.Time{})
	if err != nil {
		responses.cancel()
		_ = responses.connection.Close()
	}
	return err
}

func (server *Server) dispatch(ctx context.Context, request Request) Response {
	response := Response{OK: true}
	var err error
	switch request.Type {
	case RequestLaunch:
		response.PID, err = server.manager.Launch(ctx, *request.Launch)
	case RequestInput:
		if request.Input.Attachment.Valid() {
			err = server.manager.InputAttached(ctx, request.Input.TerminalID, request.Input.Attachment, request.Input.Data)
		} else {
			err = server.manager.Input(ctx, request.Input.TerminalID, request.Input.Data)
		}
	case RequestResize:
		if request.Resize.Attachment.Valid() {
			err = server.manager.ResizeAttached(ctx, request.Resize.TerminalID, request.Resize.Attachment, request.Resize.Revision, request.Resize.Dimensions)
		} else {
			err = server.manager.Resize(ctx, request.Resize.TerminalID, request.Resize.Revision, request.Resize.Dimensions)
		}
	case RequestSignal:
		var signal syscall.Signal
		signal, err = parseSignal(request.Signal.Signal)
		if err == nil {
			err = server.manager.Signal(ctx, request.Signal.TerminalID, signal)
		}
	case RequestClose:
		if request.Close.Attachment.Valid() {
			err = server.manager.CloseAttached(request.Close.TerminalID, request.Close.Attachment, 2*time.Second)
		} else {
			err = server.manager.CloseTerminal(request.Close.TerminalID, 2*time.Second)
		}
	case RequestInventory:
		response.Inventory = server.manager.Inventory()
	case RequestTerminalAttach:
		var restore terminals.TerminalRestore
		restore, err = server.manager.Attach(request.Attach.TerminalID)
		if err == nil {
			response.Restore = &restore
		}
	case RequestTerminalDetach:
		err = server.manager.Detach(request.Detach.TerminalID, request.Detach.Attachment)
	case RequestAttachmentUpdates:
		var update terminals.TerminalUpdateStream
		update, err = server.manager.AttachmentUpdates(ctx, request.Detach.TerminalID, request.Detach.Attachment, request.Cursor, request.MaximumBytes)
		if err == nil {
			response.Updates = []terminals.TerminalUpdateStream{update}
		}
	case RequestCompletions:
		wait := time.Duration(request.WaitMillis) * time.Millisecond
		if wait < -time.Millisecond || wait > time.Second {
			err = errors.New("invalid completion wait")
		} else {
			response.Completions, err = server.manager.Completions(ctx, request.Acknowledged, wait)
		}
	default:
		err = errors.New("unknown terminal host request")
	}
	if err != nil {
		response.OK = false
		response.Code = hostErrorCode(err)
		response.Message = boundedMessage(err.Error())
	}
	return response
}

func readRequest(reader *bufio.Reader) (Request, error) {
	data, err := reader.ReadBytes('\n')
	if err != nil {
		return Request{}, err
	}
	if len(data) == 0 || len(data) > MaxFrameBytes {
		return Request{}, errors.New("terminal host frame exceeds limit")
	}
	var request Request
	if err := strictJSON(data, &request); err != nil {
		return Request{}, err
	}
	return request, nil
}

func readResponse(reader *bufio.Reader) (Response, error) {
	data, err := reader.ReadBytes('\n')
	if err != nil {
		return Response{}, err
	}
	if len(data) == 0 || len(data) > MaxFrameBytes {
		return Response{}, errors.New("terminal host frame exceeds limit")
	}
	var response Response
	if err := strictJSON(data, &response); err != nil {
		return Response{}, err
	}
	if response.Version != ProtocolVersion || response.RequestID == "" {
		return Response{}, errors.New("invalid terminal host response")
	}
	return response, nil
}

func strictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing terminal host JSON")
	}
	return nil
}

func writeResponse(writer *bufio.Writer, response Response) error {
	if err := json.NewEncoder(writer).Encode(response); err != nil {
		return err
	}
	return writer.Flush()
}

func parseSignal(value string) (syscall.Signal, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "INT", "SIGINT":
		return syscall.SIGINT, nil
	case "TERM", "SIGTERM":
		return syscall.SIGTERM, nil
	case "HUP", "SIGHUP":
		return syscall.SIGHUP, nil
	case "KILL", "SIGKILL":
		return syscall.SIGKILL, nil
	default:
		return 0, errors.New("unsupported terminal signal")
	}
}

func hostErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, terminals.ErrProcessEnded):
		return "ended"
	case errors.Is(err, ErrDuplicate):
		return "duplicate"
	case errors.Is(err, ErrCapacity):
		return "capacity"
	case errors.Is(err, ErrStaleAttachment):
		return "stale_attachment"
	case errors.Is(err, ErrStaleController):
		return "stale_controller"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	default:
		return "host_error"
	}
}

func boundedMessage(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}

type RemoteError struct {
	Code    string
	Message string
}

func (err *RemoteError) Error() string {
	return fmt.Sprintf("terminal host %s: %s", err.Code, err.Message)
}

func (err *RemoteError) Is(target error) bool {
	switch err.Code {
	case "not_found":
		return target == ErrNotFound
	case "ended":
		return target == terminals.ErrProcessEnded
	case "duplicate":
		return target == ErrDuplicate
	case "capacity":
		return target == ErrCapacity
	case "stale_attachment":
		return target == ErrStaleAttachment
	case "stale_controller":
		return target == ErrStaleController
	default:
		return false
	}
}
