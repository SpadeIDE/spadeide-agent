package server

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spadeide/spade-agent/internal/auth"
	"github.com/spadeide/spade-agent/internal/config"
	"github.com/spadeide/spade-agent/internal/protocol"
)

// authenticateControl runs the challenge→proof handshake on a control stream.
//
//	client → auth.challenge
//	server ← {nonce}
//	client → auth {deviceId,ts,nonce,sig}   (sig optional when TLS client cert works)
//	server ← ok
//
// Returns the paired device. peerCertCount is for logging.
func (s *Server) authenticateControl(control io.ReadWriter, peerCerts []*x509.Certificate) (*config.Device, int, error) {
	peerN := len(peerCerts)

	chalEnv, err := readFrame(control)
	if err != nil {
		return nil, peerN, fmt.Errorf("read challenge request: %w", err)
	}
	if chalEnv.Method != MethodAuthChallenge && chalEnv.Method != MethodAuth {
		_ = writeFrame(control, protocol.NewError(chalEnv.ID, protocol.CodeUnauthorized, "expected auth.challenge"))
		return nil, peerN, fmt.Errorf("expected auth.challenge, got %q", chalEnv.Method)
	}

	// Legacy: client sent a full auth proof in the first message (pre-nonce).
	// Reject — nonce is required for the proof path; cert-only can short-circuit.
	if chalEnv.Method == MethodAuth {
		var ap authParams
		_ = json.Unmarshal(chalEnv.Params, &ap)
		if ap.Sig != "" && ap.Nonce == "" {
			_ = writeFrame(control, protocol.NewError(chalEnv.ID, protocol.CodeUnauthorized, "auth nonce required"))
			return nil, peerN, fmt.Errorf("auth without nonce")
		}
		if ap.Nonce != "" || ap.Sig != "" {
			dev, err := s.Auth.AuthenticateSession(peerCerts, auth.AuthProof{
				DeviceID: ap.DeviceID,
				TS:       ap.TS,
				Nonce:    ap.Nonce,
				SigB64:   ap.Sig,
			})
			if err != nil {
				_ = writeFrame(control, protocol.NewError(chalEnv.ID, protocol.CodeUnauthorized, "unauthorized"))
				return nil, peerN, err
			}
			ok := true
			_ = writeFrame(control, protocol.Envelope{ID: chalEnv.ID, OK: &ok, Result: json.RawMessage(`{}`)})
			return dev, peerN, nil
		}
		// Bare auth with only deviceId / empty → treat as challenge request.
	}

	nonce := s.Auth.IssueAuthNonce()
	chalBody, _ := json.Marshal(map[string]string{"nonce": nonce})
	ok := true
	if err := writeFrame(control, protocol.Envelope{ID: chalEnv.ID, OK: &ok, Result: chalBody}); err != nil {
		return nil, peerN, err
	}

	authEnv, err := readFrame(control)
	if err != nil {
		return nil, peerN, fmt.Errorf("read auth: %w", err)
	}
	if authEnv.Method != MethodAuth {
		_ = writeFrame(control, protocol.NewError(authEnv.ID, protocol.CodeUnauthorized, "expected auth"))
		return nil, peerN, fmt.Errorf("expected auth, got %q", authEnv.Method)
	}
	var ap authParams
	if err := json.Unmarshal(authEnv.Params, &ap); err != nil {
		_ = writeFrame(control, protocol.NewError(authEnv.ID, protocol.CodeInvalidParams, "bad auth params"))
		return nil, peerN, err
	}
	if ap.Nonce == "" {
		ap.Nonce = nonce
	}
	dev, err := s.Auth.AuthenticateSession(peerCerts, auth.AuthProof{
		DeviceID: ap.DeviceID,
		TS:       ap.TS,
		Nonce:    ap.Nonce,
		SigB64:   ap.Sig,
	})
	if err != nil {
		_ = writeFrame(control, protocol.NewError(authEnv.ID, protocol.CodeUnauthorized, "unauthorized"))
		return nil, peerN, err
	}
	_ = writeFrame(control, protocol.Envelope{ID: authEnv.ID, OK: &ok, Result: json.RawMessage(`{}`)})
	return dev, peerN, nil
}
