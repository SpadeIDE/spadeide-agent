package server

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spadeide/spade-agent/internal/protocol"
)

// MaxFrameBytes bounds a single length-prefixed QUIC frame (matches WSS limit).
const MaxFrameBytes = 64 << 20

// ALPN negotiated for SpadeIDE QUIC sessions.
const QUICALPN = "spadeide"

// MethodAuth is the first control-stream message from the client after the
// agent has sent MethodAuthChallenge.
const MethodAuth = "auth"

// MethodAuthChallenge is pushed by the agent immediately after TLS so the
// client can bind its device-key proof to a single-use nonce.
const MethodAuthChallenge = "auth.challenge"

// authParams carries optional device-key proof used when TLS client certificates
// are unavailable (notably nested TLS over the loopback↔WebSocket relay bridge).
type authParams struct {
	DeviceID string `json:"deviceId,omitempty"`
	TS       int64  `json:"ts,omitempty"`
	Nonce    string `json:"nonce,omitempty"`
	Sig      string `json:"sig,omitempty"` // base64 ASN.1 ECDSA-SHA256 over AuthMessage
}

// writeFrame writes one length-prefixed JSON envelope to w.
func writeFrame(w io.Writer, env protocol.Envelope) error {
	payload, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if len(payload) > MaxFrameBytes {
		return fmt.Errorf("frame too large: %d", len(payload))
	}
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(payload)))
	copy(buf[4:], payload)
	_, err = w.Write(buf)
	return err
}

// readFrame reads one length-prefixed JSON envelope from r.
func readFrame(r io.Reader) (protocol.Envelope, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return protocol.Envelope{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > MaxFrameBytes {
		return protocol.Envelope{}, fmt.Errorf("invalid frame length %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return protocol.Envelope{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(buf, &env); err != nil {
		return protocol.Envelope{}, err
	}
	return env, nil
}
