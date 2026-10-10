package server

import (
	"log"

	"github.com/gorilla/websocket"
	"github.com/spadeide/spade-agent/internal/config"
)

// sessionWires describes the transport-specific half of a client session.
// Exactly one of ws (WebSocket) or the control/rpc/ptyOut trio is set.
type sessionWires struct {
	control, rpc, ptyOut streamWriter
	ws                   *websocket.Conn // nil for QUIC/relay
	forceClose           func()
	label                string // "ws" | "quic" | "relay" | …
}

// runSession owns the whole lifecycle: register, attach, takeover, read, clean up.
func (s *Server) runSession(dev *config.Device, w sessionWires, read func(*client) error) {
	c := &client{
		conn:    w.ws,
		control: w.control,
		rpc:     w.rpc,
		ptyOut:  w.ptyOut,
		device:  dev,
		closed:  make(chan struct{}),
	}
	c.forceClose = w.forceClose
	if w.ws != nil {
		c.installIO()
	}
	s.attachClient(c)

	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()

	defer func() {
		c.shutdown()
		c.closeSessionIO()
		if w.ws != nil {
			_ = w.ws.Close()
		}
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
		log.Printf("%s client disconnected: %s (%s)", w.label, dev.Name, dev.ID)
	}()

	log.Printf("%s client connected: %s (%s)", w.label, dev.Name, dev.ID)
	s.takeoverOtherSessions(dev.ID, c)
	if w.ws != nil {
		go c.pingLoop()
	}
	_ = read(c)
}
