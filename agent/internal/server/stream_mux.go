package server

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
)

// Logical stream IDs on a nested-TLS relay (and any future byte-pipe mux).
const (
	StreamControl byte = 0
	StreamRPC     byte = 1
	StreamPTY     byte = 2
	streamCount        = 3
)

// streamMux multiplexes three bidirectional logical streams over one net.Conn.
// Wire frame: u8 stream_id ‖ u32be length ‖ payload.
type streamMux struct {
	conn   net.Conn
	writeMu sync.Mutex
	streams [streamCount]*muxStream
	closed  chan struct{}
	closeOnce sync.Once
	readErr error
}

type muxStream struct {
	id   byte
	mux  *streamMux
	mu   sync.Mutex
	buf  []byte
	wait chan struct{}
}

func newStreamMux(conn net.Conn) *streamMux {
	m := &streamMux{
		conn:   conn,
		closed: make(chan struct{}),
	}
	for i := byte(0); i < streamCount; i++ {
		m.streams[i] = &muxStream{
			id:   i,
			mux:  m,
			wait: make(chan struct{}, 1),
		}
	}
	return m
}

func (m *streamMux) Stream(id byte) *muxStream {
	if int(id) >= streamCount {
		return nil
	}
	return m.streams[id]
}

func (m *streamMux) Close() error {
	m.closeOnce.Do(func() {
		close(m.closed)
		_ = m.conn.Close()
		for _, s := range m.streams {
			s.signal()
		}
	})
	return nil
}

// Serve demuxes inbound frames until the connection fails or Close.
func (m *streamMux) Serve() {
	defer m.Close()
	for {
		var hdr [5]byte
		if _, err := io.ReadFull(m.conn, hdr[:]); err != nil {
			m.readErr = err
			return
		}
		sid := hdr[0]
		n := binary.BigEndian.Uint32(hdr[1:5])
		if sid >= streamCount || n == 0 || n > MaxFrameBytes {
			m.readErr = fmt.Errorf("bad mux frame sid=%d n=%d", sid, n)
			return
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(m.conn, buf); err != nil {
			m.readErr = err
			return
		}
		s := m.streams[sid]
		s.mu.Lock()
		s.buf = append(s.buf, buf...)
		s.mu.Unlock()
		s.signal()
	}
}

func (s *muxStream) signal() {
	select {
	case s.wait <- struct{}{}:
	default:
	}
}

func (s *muxStream) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if len(s.buf) > 0 {
			n := copy(p, s.buf)
			s.buf = s.buf[n:]
			s.mu.Unlock()
			return n, nil
		}
		s.mu.Unlock()

		select {
		case <-s.mux.closed:
			if s.mux.readErr != nil {
				return 0, s.mux.readErr
			}
			return 0, io.EOF
		case <-s.wait:
		}
	}
}

func (s *muxStream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(p) > MaxFrameBytes {
		return 0, fmt.Errorf("mux frame too large: %d", len(p))
	}
	hdr := [5]byte{s.id}
	binary.BigEndian.PutUint32(hdr[1:5], uint32(len(p)))

	s.mux.writeMu.Lock()
	defer s.mux.writeMu.Unlock()
	select {
	case <-s.mux.closed:
		return 0, net.ErrClosed
	default:
	}
	if _, err := s.mux.conn.Write(hdr[:]); err != nil {
		return 0, err
	}
	if _, err := s.mux.conn.Write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}
