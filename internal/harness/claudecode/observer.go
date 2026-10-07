package claudecode

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Observer owns a launch's socket. Binding is synchronous and precedes PTY
// launch; accepting starts with monitoring so acknowledgements imply receipt.
type Observer struct {
	mu       sync.Mutex
	listener *net.UnixListener
	metadata runtimeMetadata
	closed   bool
}

type hookDelivery struct {
	event NormalizedEvent
	ack   chan bool
	err   error
}

func PrepareObserver(runtimeDir string) (*Observer, error) {
	metadata, err := readRuntimeMetadata(runtimeDir)
	if err != nil {
		return nil, err
	}
	listener, err := listenObserver(metadata.SocketPath)
	if err != nil {
		return nil, err
	}
	return &Observer{listener: listener, metadata: metadata}, nil
}

func listenObserver(path string) (*net.UnixListener, error) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

func (o *Observer) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	if o.listener == nil {
		return nil
	}
	err := o.listener.Close()
	o.listener = nil
	return err
}

// rebind replaces a failed launch listener without changing launch identity.
// The caller stops the receiver using the old listener before calling rebind.
func (o *Observer) rebind() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return os.ErrClosed
	}
	if o.listener != nil {
		_ = o.listener.Close()
		o.listener = nil
	}
	listener, err := listenObserver(o.metadata.SocketPath)
	if err != nil {
		return err
	}
	o.listener = listener
	return nil
}

func (o *Observer) currentListener() (*net.UnixListener, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.listener == nil {
		return nil, os.ErrClosed
	}
	return o.listener, nil
}

func (o *Observer) receive(ctx context.Context, deliveries chan<- hookDelivery) {
	listener, err := o.currentListener()
	if err != nil {
		defer close(deliveries)
		if ctx.Err() == nil {
			select {
			case deliveries <- hookDelivery{err: err}:
			case <-ctx.Done():
			}
		}
		return
	}
	o.receiveFrom(ctx, listener, deliveries)
}

func (o *Observer) receiveFrom(ctx context.Context, listener *net.UnixListener, deliveries chan<- hookDelivery) {
	defer close(deliveries)
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() == nil {
				select {
				case deliveries <- hookDelivery{err: err}:
				case <-ctx.Done():
				}
			}
			return
		}
		o.receiveOne(ctx, conn, deliveries)
	}
}

func (o *Observer) receiveOne(ctx context.Context, conn *net.UnixConn, deliveries chan<- hookDelivery) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	reader := bufio.NewReader(io.LimitReader(conn, MaxRecordBytes+1))
	record, err := reader.ReadBytes('\n')
	if err == nil && len(record) > MaxRecordBytes {
		err = errors.New("Claude hook record exceeds size limit")
	}
	var event NormalizedEvent
	if err == nil {
		event, err = decodeNormalizedEvent(record)
	}
	if err == nil && (event.HarnessSessionID != o.metadata.HarnessSessionID || subtle.ConstantTimeCompare([]byte(event.LaunchToken), []byte(o.metadata.LaunchToken)) != 1) {
		// A different launch is not evidence of loss in this launch.
		_, _ = conn.Write([]byte("no\n"))
		return
	}
	delivery := hookDelivery{event: event, err: err, ack: make(chan bool, 1)}
	select {
	case deliveries <- delivery:
	case <-ctx.Done():
		return
	}
	select {
	case ok := <-delivery.ack:
		if ok {
			_, _ = conn.Write([]byte("ok\n"))
		} else {
			_, _ = conn.Write([]byte("no\n"))
		}
	case <-ctx.Done():
	}
}
