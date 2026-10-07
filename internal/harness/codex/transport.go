package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// rpcConn has exactly one reader. Requests are bounded and notifications never
// block that reader: an overflowing observer reconnects and reconciles history.
type rpcConn struct {
	socket  *websocket.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	pending map[string]chan rpcMessage
	events  chan rpcMessage
	next    atomic.Uint64
}
type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func connectRPC(ctx context.Context, url, token string) (*rpcConn, error) {
	dialCtx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	socket, _, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		return nil, errors.New("Codex app-server connection failed")
	}
	socket.SetReadLimit(32 << 20)
	lifetime, cancel := context.WithCancel(ctx)
	c := &rpcConn{socket: socket, ctx: lifetime, cancel: cancel, pending: make(map[string]chan rpcMessage), events: make(chan rpcMessage, 512)}
	go c.read()
	return c, nil
}
func (c *rpcConn) read() {
	defer c.cancel()
	defer close(c.events)
	for {
		_, data, err := c.socket.Read(c.ctx)
		if err != nil {
			return
		}
		var m rpcMessage
		if json.Unmarshal(data, &m) != nil {
			return
		}
		if m.Method == "" && len(m.ID) > 0 {
			c.mu.Lock()
			target := c.pending[string(m.ID)]
			c.mu.Unlock()
			if target != nil {
				select {
				case target <- m:
				default:
				}
				continue
			}
		}
		select {
		case c.events <- m:
		default:
			return
		}
	}
}
func (c *rpcConn) write(ctx context.Context, m rpcMessage) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.socket.Write(bounded, websocket.MessageText, data)
}
func (c *rpcConn) call(ctx context.Context, method string, params any, result any) error {
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	id := fmt.Sprintf(`"holark-%d"`, c.next.Add(1))
	ch := make(chan rpcMessage, 1)
	c.mu.Lock()
	if len(c.pending) >= 64 {
		c.mu.Unlock()
		return errors.New("Codex request queue is full")
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err = c.write(bounded, rpcMessage{ID: json.RawMessage(id), Method: method, Params: data}); err != nil {
		return err
	}
	select {
	case m := <-ch:
		if len(m.Error) > 0 {
			return fmt.Errorf("Codex %s request failed", method)
		}
		if result != nil {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	case <-bounded.Done():
		return bounded.Err()
	case <-c.ctx.Done():
		return errors.New("Codex app-server disconnected")
	}
}
func (c *rpcConn) initialize(ctx context.Context) error {
	if err := c.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "holark", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}, nil); err != nil {
		return err
	}
	return c.write(ctx, rpcMessage{Method: "initialized", Params: json.RawMessage(`{}`)})
}
func (c *rpcConn) close() { c.cancel(); _ = c.socket.CloseNow() }
