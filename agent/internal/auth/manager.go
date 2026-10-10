package auth

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strconv"
	"time"

	"github.com/spadeide/spade-agent/internal/config"
)

// AuthMessage is signed by the iPad device key when TLS client certs are absent
// (relay nested TLS over Network.framework loopback often cannot present them).
// Nonce is a single-use value issued by the agent (auth.challenge).
func AuthMessage(fingerprint string, ts int64, nonce string) string {
	return "spade-auth-v2\n" + fingerprint + "\n" + strconv.FormatInt(ts, 10) + "\n" + nonce
}

type Manager struct {
	store *config.Store

	tls  tlsIdentity
	pair pairingState
	sess *sessionAuth
}

func NewManager(store *config.Store) (*Manager, error) {
	m := &Manager{
		store: store,
		sess:  newSessionAuth(),
	}
	if err := m.tls.ensure(store); err != nil {
		return nil, err
	}
	m.RotatePairSecret()
	return m, nil
}

func (m *Manager) Fingerprint() string {
	m.tls.mu.Lock()
	defer m.tls.mu.Unlock()
	return m.tls.fingerprintLocked()
}

func (m *Manager) TLSConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{m.tls.certificate()},
		MinVersion:   tls.VersionTLS12,
		// Shared HTTPS listener also serves POST /pair (no client cert yet).
		// Session auth still requires a matching client cert via AuthenticatePeer.
		ClientAuth: tls.RequestClientCert,
	}
}

// SessionTLSConfig is for QUIC and nested relay TLS.
// Request (not Require) so a missing client identity fails in AuthenticatePeer
// with a clear error instead of aborting the handshake as "cancelled".
func (m *Manager) SessionTLSConfig() *tls.Config {
	cfg := m.TLSConfig().Clone()
	cfg.ClientAuth = tls.RequestClientCert
	cfg.MinVersion = tls.VersionTLS13
	return cfg
}

// PairSecret returns the current secret, lazily minting a new one if expired.
func (m *Manager) PairSecret() (secret string, expires time.Time) {
	return m.pair.currentPairSecret()
}

func (m *Manager) RotatePairSecret() string {
	return m.pair.rotatePairSecret()
}

func (m *Manager) ValidateAndPair(secret, deviceID, deviceName, devicePub string) (*config.Device, error) {
	return m.pair.validateAndPair(m.store, secret, deviceID, deviceName, devicePub)
}

// AuthenticatePeer resolves the paired device from the TLS client certificate
// leaf SPKI (must match a stored DevicePub). Bearer tokens are not accepted.
func (m *Manager) AuthenticatePeer(peerCerts []*x509.Certificate) (*config.Device, error) {
	if len(peerCerts) == 0 || peerCerts[0] == nil {
		return nil, errors.New("client certificate required")
	}
	dev, err := m.store.FindDeviceByPubSPKI(peerCerts[0].RawSubjectPublicKeyInfo)
	if err != nil {
		return nil, errors.New("unauthorized")
	}
	return dev, nil
}

// AuthProof is an ECDSA-P256 signature of AuthMessage under the paired DevicePub.
type AuthProof struct {
	DeviceID string
	TS       int64
	Nonce    string
	SigB64   string
}

// IssueAuthNonce mints a single-use nonce for the next auth proof (≈2 min TTL).
func (m *Manager) IssueAuthNonce() string {
	return m.sess.issueAuthNonce()
}

// AuthenticateSession prefers a TLS client certificate; otherwise verifies a
// short-lived device-key proof bound to this agent's fingerprint and a nonce.
func (m *Manager) AuthenticateSession(peerCerts []*x509.Certificate, proof AuthProof) (*config.Device, error) {
	if len(peerCerts) > 0 && peerCerts[0] != nil {
		if dev, err := m.AuthenticatePeer(peerCerts); err == nil {
			return dev, nil
		}
	}
	return m.AuthenticateProof(proof)
}

func (m *Manager) AuthenticateProof(proof AuthProof) (*config.Device, error) {
	return m.sess.authenticateProof(m.store, m.Fingerprint(), proof)
}
