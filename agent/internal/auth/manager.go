package auth

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
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

	mu          sync.Mutex
	pairSecret  string
	pairExpires time.Time
	// Previous secret stays valid until prevExpires so a QR scanned during
	// rotate / "New pair code" still works.
	prevSecret  string
	prevExpires time.Time
	tlsCert     tls.Certificate
	fingerprint string

	// Single-use auth nonces and a short-lived replay cache of consumed proofs.
	pendingNonces map[string]time.Time // nonce → expiry
	usedProofs    map[string]time.Time // proof digest → expiry
}

func NewManager(store *config.Store) (*Manager, error) {
	m := &Manager{
		store:         store,
		pendingNonces: make(map[string]time.Time),
		usedProofs:    make(map[string]time.Time),
	}
	if err := m.ensureTLS(); err != nil {
		return nil, err
	}
	m.RotatePairSecret()
	return m, nil
}

func (m *Manager) Fingerprint() string {
	return m.fingerprint
}

func (m *Manager) TLSConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{m.tlsCert},
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pairSecret == "" || time.Now().After(m.pairExpires) {
		m.rotatePairSecretLocked()
	}
	return m.pairSecret, m.pairExpires
}

func (m *Manager) RotatePairSecret() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rotatePairSecretLocked()
}

// rotatePairSecretLocked mints a fresh secret; the caller must hold m.mu.
func (m *Manager) rotatePairSecretLocked() string {
	// Keep the outgoing code valid until its original deadline (or a short floor)
	// so an in-flight scan isn't racing an explicit "New pair code".
	if m.pairSecret != "" {
		m.prevSecret = m.pairSecret
		m.prevExpires = m.pairExpires
		if m.prevExpires.Before(time.Now().Add(2 * time.Minute)) {
			m.prevExpires = time.Now().Add(2 * time.Minute)
		}
	}
	return m.mintSecretLocked()
}

func (m *Manager) mintSecretLocked() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	m.pairSecret = hex.EncodeToString(b)
	m.pairExpires = time.Now().Add(10 * time.Minute)
	return m.pairSecret
}

func (m *Manager) ValidateAndPair(secret, deviceID, deviceName, devicePub string) (*config.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if devicePub == "" {
		return nil, errors.New("devicePub required")
	}

	// Known deviceId is an identifier, never an authenticator. Skip the secret
	// only for a genuinely idempotent re-registration (same public key).
	// Changing DevicePub requires a valid pairing secret (or would require
	// proving possession of the old key — not implemented here).
	if deviceID != "" {
		if existing, err := m.store.FindDeviceByID(deviceID); err == nil && existing != nil {
			if existing.DevicePub == devicePub {
				dev := config.Device{
					ID:        deviceID,
					Name:      deviceName,
					DevicePub: devicePub,
					CreatedAt: existing.CreatedAt,
				}
				if dev.Name == "" {
					dev.Name = existing.Name
				}
				if err := m.store.UpsertDevice(dev); err != nil {
					return nil, err
				}
				return &dev, nil
			}
			// Fall through to the secret check — key change is a new pairing.
		}
	}

	now := time.Now()
	matchCurrent := secretEqual(secret, m.pairSecret)
	matchPrev := secretEqual(secret, m.prevSecret)
	okCurrent := matchCurrent && now.Before(m.pairExpires)
	okPrev := matchPrev && now.Before(m.prevExpires)
	if !okCurrent && !okPrev {
		if matchCurrent || matchPrev {
			return nil, errors.New("pairing secret expired")
		}
		return nil, errors.New("bad pairing secret")
	}
	createdAt := time.Now().Unix()
	if deviceID != "" {
		if existing, err := m.store.FindDeviceByID(deviceID); err == nil && existing != nil {
			createdAt = existing.CreatedAt
		}
	}
	dev := config.Device{
		ID:        deviceID,
		Name:      deviceName,
		DevicePub: devicePub,
		CreatedAt: createdAt,
	}
	if err := m.store.UpsertDevice(dev); err != nil {
		return nil, err
	}
	// One-time: burn both slots, then mint a fresh unused code (do not park the
	// consumed secret in prev — that would allow a second pair with the same QR).
	m.prevSecret = ""
	m.prevExpires = time.Time{}
	m.mintSecretLocked()
	return &dev, nil
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

// maxPendingNonces caps pre-auth challenge storage (LAN DoS hygiene).
const maxPendingNonces = 64

// IssueAuthNonce mints a single-use nonce for the next auth proof (≈2 min TTL).
func (m *Manager) IssueAuthNonce() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeAuthCachesLocked(time.Now())
	if len(m.pendingNonces) >= maxPendingNonces {
		m.evictOldestNonceLocked()
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	nonce := hex.EncodeToString(b)
	m.pendingNonces[nonce] = time.Now().Add(2 * time.Minute)
	return nonce
}

func (m *Manager) purgeAuthCachesLocked(now time.Time) {
	for n, exp := range m.pendingNonces {
		if now.After(exp) {
			delete(m.pendingNonces, n)
		}
	}
	for d, exp := range m.usedProofs {
		if now.After(exp) {
			delete(m.usedProofs, d)
		}
	}
}

func (m *Manager) evictOldestNonceLocked() {
	var oldest string
	var oldestExp time.Time
	first := true
	for n, exp := range m.pendingNonces {
		if first || exp.Before(oldestExp) {
			oldest, oldestExp, first = n, exp, false
		}
	}
	if oldest != "" {
		delete(m.pendingNonces, oldest)
	}
}

// secretEqual is a constant-time compare for pairing secrets (empty never matches).
func secretEqual(got, want string) bool {
	if got == "" || want == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
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
	if proof.DeviceID == "" || proof.SigB64 == "" || proof.TS == 0 || proof.Nonce == "" {
		return nil, errors.New("client certificate or device proof required")
	}
	now := time.Now().Unix()
	// Tight window — nonce already bounds replay; keep clock skew small.
	if proof.TS < now-90 || proof.TS > now+30 {
		return nil, errors.New("auth proof expired")
	}

	m.mu.Lock()
	m.purgeAuthCachesLocked(time.Now())
	exp, ok := m.pendingNonces[proof.Nonce]
	if !ok || time.Now().After(exp) {
		m.mu.Unlock()
		return nil, errors.New("auth nonce invalid")
	}
	delete(m.pendingNonces, proof.Nonce)
	digest := proofDigest(proof)
	if _, used := m.usedProofs[digest]; used {
		m.mu.Unlock()
		return nil, errors.New("auth proof replay")
	}
	fp := m.fingerprint
	m.mu.Unlock()

	dev, err := m.store.FindDeviceByID(proof.DeviceID)
	if err != nil || dev == nil || dev.DevicePub == "" {
		return nil, errors.New("unauthorized")
	}
	pubDER, err := base64.StdEncoding.DecodeString(dev.DevicePub)
	if err != nil {
		return nil, errors.New("unauthorized")
	}
	pubAny, err := x509.ParsePKIXPublicKey(pubDER)
	if err != nil {
		return nil, errors.New("unauthorized")
	}
	pub, ok := pubAny.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("unauthorized")
	}
	sig, err := base64.StdEncoding.DecodeString(proof.SigB64)
	if err != nil || len(sig) == 0 {
		return nil, errors.New("unauthorized")
	}
	msg := AuthMessage(fp, proof.TS, proof.Nonce)
	sum := sha256.Sum256([]byte(msg))
	if !ecdsa.VerifyASN1(pub, sum[:], sig) {
		return nil, fmt.Errorf("unauthorized")
	}

	m.mu.Lock()
	m.usedProofs[digest] = time.Now().Add(3 * time.Minute)
	m.mu.Unlock()
	return dev, nil
}

func proofDigest(p AuthProof) string {
	h := sha256.New()
	_, _ = h.Write([]byte(p.DeviceID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.FormatInt(p.TS, 10)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(p.Nonce))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(p.SigB64))
	return hex.EncodeToString(h.Sum(nil))
}

func (m *Manager) ensureTLS() error {
	certDir := m.store.CertDir()
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		return err
	}
	certPath := filepath.Join(certDir, "server.crt")
	keyPath := filepath.Join(certDir, "server.key")

	if _, err := os.Stat(certPath); err == nil {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return err
		}
		m.tlsCert = cert
		m.fingerprint = fingerprintOf(cert)
		return nil
	}

	key, err := generateKey()
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "SpadeIDE Agent", Organization: []string{"SpadeIDE"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost", "spade-agent.local"},
	}

	// Add local interface IPs so iPad TLS hostname/IP can match if needed.
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			addrs, _ := iface.Addrs()
			for _, a := range addrs {
				var ip net.IP
				switch v := a.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip == nil || ip.IsLoopback() {
					continue
				}
				if v4 := ip.To4(); v4 != nil {
					tmpl.IPAddresses = append(tmpl.IPAddresses, v4)
				}
			}
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, publicKey(key), key)
	if err != nil {
		return err
	}
	certOut, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		_ = certOut.Close()
		return err
	}
	_ = certOut.Close()

	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := writeKey(keyOut, key); err != nil {
		_ = keyOut.Close()
		return err
	}
	_ = keyOut.Close()

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return err
	}
	m.tlsCert = cert
	m.fingerprint = fingerprintOf(cert)
	return nil
}

func fingerprintOf(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(sum[:])
}
