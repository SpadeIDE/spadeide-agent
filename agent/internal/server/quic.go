package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/quic-go/quic-go"
)

// listenQUIC serves SpadeIDE sessions over QUIC on UDP :Port (same number as WSS).
// Client (Network.framework, shared NWParameters):
//  1. control — auth.challenge then auth (creates stream 0)
//  2. rpc     — ping kick opens stream, then fs.*/git.*
//  3. pty     — ping kick opens stream, then pty.*
//
// NWConnectionGroup does not handshake with quic-go; use shared NWParameters instead.
func (s *Server) listenQUIC(ctx context.Context) error {
	tlsCfg := s.Auth.SessionTLSConfig()
	tlsCfg.NextProtos = []string{QUICALPN}

	ln, err := quic.ListenAddr(
		fmt.Sprintf(":%d", s.Port),
		tlsCfg,
		&quic.Config{
			MaxIdleTimeout:  5 * time.Minute,
			KeepAlivePeriod: 15 * time.Second,
			Allow0RTT:       false,
		},
	)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("quic accept error: %v", err)
			continue
		}
		log.Printf("quic handshake ok from %s", conn.RemoteAddr())
		go s.serveQUIC(conn)
	}
}

func (s *Server) serveQUIC(conn *quic.Conn) {
	defer conn.CloseWithError(0, "bye")

	ctx := conn.Context()

	// Network.framework opens a QUIC STREAM only on the first send. Client writes
	// auth.challenge on control first (stream 0), then opens rpc/pty with a ping kick.
	control, err := conn.AcceptStream(ctx)
	if err != nil {
		log.Printf("quic control accept failed from %s: %v", conn.RemoteAddr(), err)
		return
	}

	peerCerts := conn.ConnectionState().TLS.PeerCertificates
	dev, peerN, err := s.authenticateControl(control, peerCerts)
	if err != nil {
		log.Printf("quic auth failed from %s: %v (peerCerts=%d)", conn.RemoteAddr(), err, peerN)
		return
	}

	rpc, err := acceptStreamWithTimeout(ctx, conn, 3*time.Second, "rpc")
	if err != nil {
		log.Printf("quic rpc accept failed from %s: %v", conn.RemoteAddr(), err)
		return
	}
	ptyStream, err := acceptStreamWithTimeout(ctx, conn, 3*time.Second, "pty")
	if err != nil {
		log.Printf("quic pty accept failed from %s: %v", conn.RemoteAddr(), err)
		return
	}
	log.Printf("quic streams ready control=%d rpc=%d pty=%d from %s",
		control.StreamID(), rpc.StreamID(), ptyStream.StreamID(), conn.RemoteAddr())

	s.runSession(dev, sessionWires{
		control:    control,
		rpc:        rpc,
		ptyOut:     ptyStream,
		forceClose: func() { _ = conn.CloseWithError(0, "unpaired") },
		label:      "quic",
	}, func(c *client) error {
		errCh := make(chan error, 3)
		go readStreamLoop(c, control, control, errCh)
		go readStreamLoop(c, rpc, rpc, errCh)
		go readStreamLoop(c, ptyStream, ptyStream, errCh)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			return err
		case <-c.closed:
			return nil
		}
	})
}

// ServePunchQUIC listens for one QUIC client on an ephemeral punched UDP socket
// (hole-punch path). Closes the packet conn when the accept window or session ends.
func (s *Server) ServePunchQUIC(ctx context.Context, pc net.PacketConn) {
	defer pc.Close()

	tlsCfg := s.Auth.SessionTLSConfig()
	tlsCfg.NextProtos = []string{QUICALPN}
	tr := &quic.Transport{Conn: pc}
	defer tr.Close()

	ln, err := tr.Listen(tlsCfg, &quic.Config{
		MaxIdleTimeout:  5 * time.Minute,
		KeepAlivePeriod: 15 * time.Second,
		Allow0RTT:       false,
	})
	if err != nil {
		log.Printf("punch quic listen: %v", err)
		return
	}
	defer ln.Close()

	acceptCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	conn, err := ln.Accept(acceptCtx)
	if err != nil {
		log.Printf("punch quic accept: %v", err)
		return
	}
	log.Printf("punch quic handshake ok from %s", conn.RemoteAddr())
	s.serveQUIC(conn)
}

func acceptStreamWithTimeout(ctx context.Context, conn *quic.Conn, d time.Duration, label string) (*quic.Stream, error) {
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	st, err := conn.AcceptStream(cctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	return st, nil
}

func readStreamLoop(c *client, r io.Reader, reply streamWriter, errCh chan<- error) {
	for {
		env, err := readFrame(r)
		if err != nil {
			select {
			case errCh <- err:
			default:
			}
			return
		}
		c.dispatchInbound(env, reply)
	}
}
