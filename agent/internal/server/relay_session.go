package server

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

// ServeRelaySession runs nested agent TLS over a relay byte pipe, then a
// three-stream mux (control / rpc / pty) matching QUIC layout — same
// length-prefixed JSON per stream, with an extra u8 stream_id on the wire.
func (s *Server) ServeRelaySession(raw net.Conn) {
	defer raw.Close()
	// Match iPad nested-TLS budget; a hung peer must not pin dial slots long.
	_ = raw.SetDeadline(time.Now().Add(20 * time.Second))

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
	mux.UnlockFrames()

	s.runSession(dev, sessionWires{
		control:    control,
		rpc:        rpc,
		ptyOut:     ptyStream,
		forceClose: func() { _ = mux.Close() },
		label:      "relay",
	}, func(c *client) error {
		errCh := make(chan error, 3)
		go readStreamLoop(c, control, control, errCh)
		go readStreamLoop(c, rpc, rpc, errCh)
		go readStreamLoop(c, ptyStream, ptyStream, errCh)

		select {
		case err := <-errCh:
			return err
		case <-c.closed:
			return nil
		}
	})
}

// serveFramed runs auth + runSession on a single length-prefixed pipe (no
// stream mux). Used by tests as the simplest runSession entry point; also
// handy for single-pipe debugging.
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

	var writeMu sync.Mutex
	fw := &lockedWriter{w: conn, mu: &writeMu}
	s.runSession(dev, sessionWires{
		control:    fw,
		rpc:        fw,
		ptyOut:     fw,
		forceClose: func() { _ = conn.Close() },
		label:      via,
	}, func(c *client) error {
		for {
			env, err := readFrame(conn)
			if err != nil {
				return err
			}
			c.dispatchInbound(env, fw)
		}
	})
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
