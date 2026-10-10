package rendezvous

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsConn adapts a WebSocket so TLS / framed I/O can treat it as a byte stream.
// Each Write becomes one binary WS message; Reads reassemble across messages.
type wsConn struct {
	ws            *websocket.Conn
	mu            sync.Mutex
	buf           []byte
	closed        bool
	readDeadline  time.Time
	writeDeadline time.Time
}

func newWSConn(ws *websocket.Conn) *wsConn {
	return &wsConn{ws: ws}
}

func (c *wsConn) Read(p []byte) (int, error) {
	for len(c.buf) == 0 {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return 0, io.EOF
		}
		dl := c.readDeadline
		c.mu.Unlock()
		if dl.IsZero() {
			_ = c.ws.SetReadDeadline(time.Now().Add(5 * time.Minute))
		} else {
			_ = c.ws.SetReadDeadline(dl)
		}
		typ, data, err := c.ws.ReadMessage()
		if err != nil {
			return 0, err
		}
		// Hub may emit text control frames (e.g. legacy {"t":"paired"}). Nested
		// TLS is binary-only — skip text so older rendezvous builds still work.
		if typ == websocket.TextMessage {
			continue
		}
		c.buf = data
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

func (c *wsConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	if c.writeDeadline.IsZero() {
		_ = c.ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
	} else {
		_ = c.ws.SetWriteDeadline(c.writeDeadline)
	}
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return c.ws.Close()
}

func (c *wsConn) LocalAddr() net.Addr  { return wsAddr("local") }
func (c *wsConn) RemoteAddr() net.Addr { return wsAddr("remote") }

func (c *wsConn) SetDeadline(t time.Time) error {
	_ = c.SetReadDeadline(t)
	_ = c.SetWriteDeadline(t)
	return nil
}

func (c *wsConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.mu.Unlock()
	return c.ws.SetReadDeadline(t)
}

func (c *wsConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.writeDeadline = t
	c.mu.Unlock()
	return c.ws.SetWriteDeadline(t)
}

type wsAddr string

func (a wsAddr) Network() string { return "ws" }
func (a wsAddr) String() string  { return string(a) }
