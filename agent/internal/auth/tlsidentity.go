package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spadeide/spade-agent/internal/config"
)

type tlsIdentity struct {
	mu          sync.Mutex
	tlsCert     tls.Certificate
	fingerprint string
}

func (t *tlsIdentity) ensure(store *config.Store) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	certDir := store.CertDir()
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
		t.tlsCert = cert
		t.fingerprint = fingerprintOf(cert)
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
		// iPad pins SHA-256(DER); SAN/hostname are never validated on the
		// client (QUICTransport / RelayTransport). Do not enumerate LAN/VPN/
		// Docker IPs — that would publish a free network map pre-auth (A12).
		DNSNames: []string{"localhost", "spade-agent.local"},
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
	t.tlsCert = cert
	t.fingerprint = fingerprintOf(cert)
	return nil
}

func (t *tlsIdentity) fingerprintLocked() string {
	return t.fingerprint
}

func (t *tlsIdentity) certificate() tls.Certificate {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tlsCert
}

func fingerprintOf(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(sum[:])
}
