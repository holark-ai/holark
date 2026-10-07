package terminalhost

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holark-ai/holark/internal/terminals"
)

type Client struct {
	socketPath     string
	token          string
	expectedHostID string
	controllerID   string
	next           atomic.Uint64
	epoch          atomic.Uint64
	connectMu      sync.Mutex
	mu             sync.Mutex
	hostID         string
	session        *clientSession
	closed         bool
}

type callResult struct {
	response Response
	err      error
}

type clientSession struct {
	owner       *Client
	epoch       uint64
	connection  net.Conn
	reader      *bufio.Reader
	writer      *bufio.Writer
	writePermit chan struct{}
	mu          sync.Mutex
	pending     map[string]chan callResult
	abandoned   map[string]struct{}
	closed      bool
}

var errClientClosed = errors.New("terminal host client is closed")

const terminalHostIOTimeout = 12 * time.Second

func NewClient(socketPath, token string) (*Client, error) {
	return NewControllerClient(socketPath, token, "", "")
}

func NewControllerClient(socketPath, token, expectedHostID, controllerID string) (*Client, error) {
	if err := validateClientEndpoint(socketPath, token, expectedHostID); err != nil {
		return nil, err
	}
	if controllerID == "" {
		var err error
		controllerID, err = randomCredential(24)
		if err != nil {
			return nil, err
		}
	}
	if len(controllerID) > 128 || len(expectedHostID) > 128 {
		return nil, errors.New("terminal host controller identity is invalid")
	}
	return &Client{socketPath: socketPath, token: token, expectedHostID: expectedHostID, controllerID: controllerID}, nil
}

// ReconfigureEndpoint rotates a controller client onto a clean replacement
// host while preserving the object held by node services. In-flight calls are
// failed as ambiguous and are never retried by this method.
func (client *Client) ReconfigureEndpoint(socketPath, token, expectedHostID string) error {
	if err := validateClientEndpoint(socketPath, token, expectedHostID); err != nil {
		return err
	}
	client.connectMu.Lock()
	defer client.connectMu.Unlock()
	client.mu.Lock()
	if client.closed {
		client.mu.Unlock()
		return errClientClosed
	}
	session := client.session
	client.socketPath = socketPath
	client.token = token
	client.expectedHostID = expectedHostID
	client.hostID = ""
	client.session = nil
	client.epoch.Store(0)
	client.mu.Unlock()
	if session != nil {
		session.fail(errors.New("terminal host endpoint was replaced"))
	}
	return nil
}

func validateClientEndpoint(socketPath, token, expectedHostID string) error {
	if socketPath == "" || len(token) < 32 || len(token) > 512 || len(expectedHostID) > 128 {
		return errors.New("terminal host socket and token are required")
	}
	return nil
}

func (client *Client) Launch(ctx context.Context, spec terminals.LaunchSpec) (int, error) {
	response, err := client.call(ctx, Request{Type: RequestLaunch, Launch: &spec})
	return response.PID, err
}

func (client *Client) Input(ctx context.Context, id terminals.TerminalID, data []byte) error {
	_, err := client.call(ctx, Request{Type: RequestInput, Input: &terminals.InputCommand{
		TerminalID: id, Data: data,
	}})
	return err
}

func (client *Client) InputAttached(ctx context.Context, id terminals.TerminalID, attachment terminals.TerminalAttachment, data []byte) error {
	_, err := client.call(ctx, Request{Type: RequestInput, Input: &terminals.InputCommand{
		TerminalID: id, Attachment: attachment, Data: data,
	}})
	return err
}

func (client *Client) Resize(ctx context.Context, id terminals.TerminalID, revision uint64, dimensions terminals.Dimensions) error {
	_, err := client.call(ctx, Request{Type: RequestResize, Resize: &terminals.ResizeCommand{
		TerminalID: id, Revision: revision, Dimensions: dimensions,
	}})
	return err
}

func (client *Client) ResizeAttached(ctx context.Context, id terminals.TerminalID, attachment terminals.TerminalAttachment, revision uint64, dimensions terminals.Dimensions) error {
	_, err := client.call(ctx, Request{Type: RequestResize, Resize: &terminals.ResizeCommand{
		TerminalID: id, Attachment: attachment,
		Revision: revision, Dimensions: dimensions,
	}})
	return err
}

func (client *Client) Signal(ctx context.Context, id terminals.TerminalID, signal string) error {
	_, err := client.call(ctx, Request{Type: RequestSignal, Signal: &terminals.SignalCommand{
		TerminalID: id, Signal: signal,
	}})
	return err
}

func (client *Client) CloseTerminal(ctx context.Context, id terminals.TerminalID) error {
	_, err := client.call(ctx, Request{Type: RequestClose, Close: &terminals.CloseCommand{
		TerminalID: id,
	}})
	return err
}

func (client *Client) CloseAttached(ctx context.Context, id terminals.TerminalID, attachment terminals.TerminalAttachment) error {
	_, err := client.call(ctx, Request{Type: RequestClose, Close: &terminals.CloseCommand{
		TerminalID: id, Attachment: attachment,
	}})
	return err
}

func (client *Client) Inventory(ctx context.Context) ([]terminals.TerminalID, error) {
	response, err := client.call(ctx, Request{Type: RequestInventory})
	return response.Inventory, err
}

func (client *Client) Attach(ctx context.Context, id terminals.TerminalID) (terminals.TerminalRestore, error) {
	response, err := client.call(ctx, Request{
		Type: RequestTerminalAttach, Attach: &terminals.AttachCommand{
			TerminalID: id,
		},
	})
	if err != nil {
		return terminals.TerminalRestore{}, err
	}
	if response.Restore == nil {
		return terminals.TerminalRestore{}, errors.New("terminal host omitted attachment restore")
	}
	return *response.Restore, nil
}

func (client *Client) Detach(ctx context.Context, id terminals.TerminalID, attachment terminals.TerminalAttachment) error {
	_, err := client.call(ctx, Request{
		Type: RequestTerminalDetach, Detach: &terminals.DetachCommand{
			TerminalID: id, Attachment: attachment,
		},
	})
	return err
}

func (client *Client) AttachmentUpdates(ctx context.Context, id terminals.TerminalID, attachment terminals.TerminalAttachment, cursor Cursor, maximumBytes int) (terminals.TerminalUpdateStream, error) {
	response, err := client.call(ctx, Request{
		Type: RequestAttachmentUpdates, Detach: &terminals.DetachCommand{
			TerminalID: id, Attachment: attachment,
		},
		Cursor: cursor, MaximumBytes: maximumBytes,
	})
	if err != nil {
		return terminals.TerminalUpdateStream{}, err
	}
	if len(response.Updates) != 1 {
		return terminals.TerminalUpdateStream{}, errors.New("terminal host omitted attachment update")
	}
	return response.Updates[0], nil
}

func (client *Client) Completions(ctx context.Context, acknowledged []terminals.TerminalID, wait time.Duration) ([]terminals.ProcessCompletion, error) {
	response, err := client.call(ctx, Request{
		Type: RequestCompletions, Acknowledged: acknowledged,
		WaitMillis: int(min(wait, time.Second) / time.Millisecond),
	})
	return response.Completions, err
}

func (client *Client) call(ctx context.Context, request Request) (Response, error) {
	request.Version = ProtocolVersion
	request.RequestID = client.requestID()
	var session *clientSession
	var pending chan callResult
	for attempts := 0; attempts < 2; attempts++ {
		var err error
		session, err = client.connect(ctx)
		if err != nil {
			return Response{}, err
		}
		request.ControllerEpoch = session.epoch
		if pending, err = session.register(request.RequestID); err == nil {
			break
		}
		client.drop(session)
	}
	if pending == nil {
		return Response{}, errors.New("terminal host connection closed before request")
	}
	if err := session.write(ctx, request); err != nil {
		session.remove(request.RequestID, false)
		return Response{}, err
	}
	var result callResult
	select {
	case result = <-pending:
	case <-ctx.Done():
		if session.remove(request.RequestID, true) {
			return Response{}, ctx.Err()
		}
		result = <-pending
	}
	if result.err != nil {
		return Response{}, result.err
	}
	if !result.response.OK {
		return Response{}, remoteError(result.response)
	}
	return result.response, nil
}

func (client *Client) connect(ctx context.Context) (*clientSession, error) {
	client.connectMu.Lock()
	defer client.connectMu.Unlock()
	client.mu.Lock()
	if client.closed {
		client.mu.Unlock()
		return nil, errClientClosed
	}
	current := client.session
	client.mu.Unlock()
	if current != nil && current.alive() {
		return current, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", client.socketPath)
	if err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(connection, MaxFrameBytes)
	writer := bufio.NewWriterSize(connection, 64*1024)
	_ = connection.SetDeadline(contextDeadline(ctx, terminalHostIOTimeout))
	helloID := client.requestID()
	if err := writeRequest(writer, Request{
		Version: ProtocolVersion, Type: RequestHello, RequestID: helloID, Token: client.token,
		HostID: client.expectedHostID, ControllerID: client.controllerID,
	}); err != nil {
		_ = connection.Close()
		return nil, err
	}
	hello, err := readResponse(reader)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	if hello.RequestID != helloID {
		_ = connection.Close()
		return nil, errors.New("terminal host handshake response request ID does not match")
	}
	if !hello.OK {
		_ = connection.Close()
		return nil, remoteError(hello)
	}
	if hello.HostID == "" || hello.ControllerEpoch == 0 ||
		(client.expectedHostID != "" && hello.HostID != client.expectedHostID) {
		_ = connection.Close()
		return nil, errors.New("terminal host handshake identity does not match")
	}
	_ = connection.SetDeadline(time.Time{})
	session := &clientSession{
		owner: client, epoch: hello.ControllerEpoch, connection: connection, reader: reader, writer: writer,
		writePermit: make(chan struct{}, 1), pending: make(map[string]chan callResult), abandoned: make(map[string]struct{}),
	}
	session.writePermit <- struct{}{}
	client.mu.Lock()
	if client.closed {
		client.mu.Unlock()
		_ = connection.Close()
		return nil, errClientClosed
	}
	client.hostID = hello.HostID
	client.epoch.Store(hello.ControllerEpoch)
	client.session = session
	client.mu.Unlock()
	go session.read()
	return session, nil
}

func (client *Client) drop(session *clientSession) {
	client.mu.Lock()
	if client.session == session {
		client.session = nil
	}
	client.mu.Unlock()
}

func (client *Client) Close() error {
	client.mu.Lock()
	if client.closed {
		client.mu.Unlock()
		return nil
	}
	client.closed = true
	session := client.session
	client.session = nil
	client.mu.Unlock()
	if session != nil {
		session.fail(errClientClosed)
	}
	return nil
}

func (session *clientSession) alive() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return !session.closed
}

func (session *clientSession) register(requestID string) (chan callResult, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil, net.ErrClosed
	}
	pending := make(chan callResult, 1)
	session.pending[requestID] = pending
	return pending, nil
}

func (session *clientSession) remove(requestID string, abandoned bool) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if _, exists := session.pending[requestID]; !exists {
		return false
	}
	delete(session.pending, requestID)
	if abandoned && !session.closed {
		session.abandoned[requestID] = struct{}{}
	}
	return true
}

func (session *clientSession) write(ctx context.Context, request Request) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-session.writePermit:
	}
	defer func() { session.writePermit <- struct{}{} }()
	if !session.alive() {
		return net.ErrClosed
	}
	_ = session.connection.SetWriteDeadline(contextDeadline(ctx, terminalHostIOTimeout))
	err := writeRequest(session.writer, request)
	_ = session.connection.SetWriteDeadline(time.Time{})
	if err != nil {
		session.fail(err)
	}
	return err
}

func (session *clientSession) read() {
	for {
		response, err := readResponse(session.reader)
		if err != nil {
			session.fail(err)
			return
		}
		session.mu.Lock()
		pending := session.pending[response.RequestID]
		if pending != nil {
			delete(session.pending, response.RequestID)
			session.mu.Unlock()
			pending <- callResult{response: response}
			continue
		}
		if _, abandoned := session.abandoned[response.RequestID]; abandoned {
			delete(session.abandoned, response.RequestID)
			session.mu.Unlock()
			continue
		}
		session.mu.Unlock()
		session.fail(errors.New("terminal host response request ID does not match"))
		return
	}
}

func (session *clientSession) fail(err error) {
	if err == nil {
		err = net.ErrClosed
	}
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return
	}
	session.closed = true
	pending := session.pending
	session.pending = make(map[string]chan callResult)
	session.abandoned = make(map[string]struct{})
	session.mu.Unlock()
	_ = session.connection.Close()
	session.owner.drop(session)
	for _, response := range pending {
		response <- callResult{err: err}
	}
}

func contextDeadline(ctx context.Context, maximum time.Duration) time.Time {
	deadline := time.Now().Add(maximum)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		return value
	}
	return deadline
}

func (client *Client) requestID() string {
	return "local-" + formatUint(client.next.Add(1))
}

func writeRequest(writer *bufio.Writer, request Request) error {
	if err := jsonEncoder(writer).Encode(request); err != nil {
		return err
	}
	return writer.Flush()
}

func remoteError(response Response) error {
	return &RemoteError{Code: response.Code, Message: response.Message}
}
