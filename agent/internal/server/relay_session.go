package server

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	fsaccess "github.com/spadeide/spade-agent/internal/fs"
	"github.com/spadeide/spade-agent/internal/platform"
	"github.com/spadeide/spade-agent/internal/protocol"
	ptypkg "github.com/spadeide/spade-agent/internal/pty"
)

// ServeRelaySession runs nested agent TLS over a relay byte pipe, then a
// three-stream mux (control / rpc / pty) matching QUIC layout — same
// length-prefixed JSON per stream, with an extra u8 stream_id on the wire.
func (s *Server) ServeRelaySession(raw net.Conn) {
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(45 * time.Second))

	tlsCfg := s.Auth.SessionTLSConfig()
	tlsConn := tls.Server(raw, tlsCfg)
	if err := tlsConn.Handshake(); err != nil {
		log.Printf("relay tls handshake failed: %v", err)
		return
	}
	peerN := len(tlsConn.ConnectionState().PeerCertificates)
	log.Printf("relay tls handshake ok (peerCerts=%d)", peerN)
	_ = tlsConn.SetDeadline(time.Time{})

	mux := newStreamMux(tlsConn)
	defer mux.Close()
	go mux.Serve()

	control := mux.Stream(StreamControl)
	rpc := mux.Stream(StreamRPC)
	ptyStream := mux.Stream(StreamPTY)

	dev, peerN, err := s.authenticateControl(control, tlsConn.ConnectionState().PeerCertificates)
	if err != nil {
		log.Printf("relay auth failed: %v (peerCerts=%d)", err, peerN)
		return
	}

	home, _ := os.UserHomeDir()
	c := &client{
		control: control,
		rpc:     rpc,
		ptyOut:  ptyStream,
		device:  dev,
		closed:  make(chan struct{}),
	}
	c.forceClose = func() { _ = mux.Close() }
	c.fs = fsaccess.New(home, func(env protocol.Envelope) { c.send(env) })
	c.pty = ptypkg.NewManager(platform.DefaultShell(), func(env protocol.Envelope) { c.send(env) })

	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()

	defer func() {
		c.shutdown()
		c.pty.CloseAll()
		c.fs.Close()
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
		log.Printf("relay client disconnected: %s (%s)", dev.Name, dev.ID)
	}()

	log.Printf("relay client connected (mux): %s (%s)", dev.Name, dev.ID)
	s.takeoverOtherSessions(dev.ID, c)

	errCh := make(chan error, 3)
	go readStreamLoop(c, control, control, errCh)
	go readStreamLoop(c, rpc, rpc, errCh)
	go readStreamLoop(c, ptyStream, ptyStream, errCh)

	select {
	case <-errCh:
	case <-c.closed:
	}
}

// serveFramed is retained for tests / single-pipe debugging only.
func (s *Server) serveFramed(conn net.Conn, via string) {
	defer conn.Close()

	var peerCerts []*x509.Certificate
	if tc, ok := conn.(*tls.Conn); ok {
		peerCerts = tc.ConnectionState().PeerCertificates
	}
	dev, _, err := s.authenticateControl(conn, peerCerts)
	if err != nil {
		log.Printf("%s auth failed: %v", via, err)
		return
	}

	home, _ := os.UserHomeDir()
	var writeMu sync.Mutex
	fw := &lockedWriter{w: conn, mu: &writeMu}
	c := &client{
		control: fw,
		rpc:     fw,
		ptyOut:  fw,
		device:  dev,
		closed:  make(chan struct{}),
	}
	c.forceClose = func() { _ = conn.Close() }
	c.fs = fsaccess.New(home, func(env protocol.Envelope) { c.send(env) })
	c.pty = ptypkg.NewManager(platform.DefaultShell(), func(env protocol.Envelope) { c.send(env) })

	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()

	defer func() {
		c.shutdown()
		c.pty.CloseAll()
		c.fs.Close()
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
		log.Printf("%s client disconnected: %s (%s)", via, dev.Name, dev.ID)
	}()

	log.Printf("%s client connected: %s (%s)", via, dev.Name, dev.ID)

	for {
		env, err := readFrame(conn)
		if err != nil {
			return
		}
		c.dispatchInbound(env, fw)
	}
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
