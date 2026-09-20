package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"testing"
	"time"

	"github.com/spadeide/spade-agent/internal/config"
)

func TestPreviousSecretAcceptedAfterRotate(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := m.PairSecret()
	m.RotatePairSecret()
	dev, err := m.ValidateAndPair(old, "dev-1", "iPad", "pub-a")
	if err != nil {
		t.Fatalf("prev secret should work: %v", err)
	}
	if dev.ID != "dev-1" {
		t.Fatal(dev)
	}
}

func TestExpiredSecretRejected(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := m.PairSecret()
	m.mu.Lock()
	m.pairExpires = time.Now().Add(-time.Second)
	m.prevSecret = ""
	m.mu.Unlock()
	_, err = m.ValidateAndPair(secret, "dev-1", "iPad", "pub-a")
	if err == nil {
		t.Fatal("expected expiry error")
	}
}

func TestBurnAfterSuccess(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := m.PairSecret()
	if _, err := m.ValidateAndPair(secret, "dev-1", "iPad", "pub-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ValidateAndPair(secret, "dev-2", "iPad 2", "pub-b"); err == nil {
		t.Fatal("secret should be one-time")
	}
}

func TestKnownDeviceSameKeyRePairsWithoutSecret(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := m.PairSecret()
	first, err := m.ValidateAndPair(secret, "dev-1", "iPad", "pub-a")
	if err != nil {
		t.Fatal(err)
	}
	again, err := m.ValidateAndPair("totally-wrong", "dev-1", "iPad Pro", "pub-a")
	if err != nil {
		t.Fatalf("same key should re-pair without secret: %v", err)
	}
	if again.Name != "iPad Pro" || again.DevicePub != "pub-a" {
		t.Fatalf("expected updated name, same pub: %+v", again)
	}
	if again.CreatedAt != first.CreatedAt {
		t.Fatal("createdAt should be preserved")
	}
}

func TestKnownDeviceKeyChangeRequiresSecret(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := m.PairSecret()
	if _, err := m.ValidateAndPair(secret, "dev-1", "iPad", "pub-a"); err != nil {
		t.Fatal(err)
	}
	_, err = m.ValidateAndPair("", "dev-1", "attacker", "pub-evil")
	if err == nil {
		t.Fatal("key change without secret must be rejected")
	}
	got, err := store.FindDeviceByID("dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.DevicePub != "pub-a" {
		t.Fatalf("stored key changed: %+v", got)
	}
}

func TestKnownDeviceKeyChangeWithSecret(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := m.PairSecret()
	first, err := m.ValidateAndPair(secret, "dev-1", "iPad", "pub-a")
	if err != nil {
		t.Fatal(err)
	}
	secret2, _ := m.PairSecret()
	again, err := m.ValidateAndPair(secret2, "dev-1", "iPad", "pub-b")
	if err != nil {
		t.Fatalf("key change with secret should work: %v", err)
	}
	if again.DevicePub != "pub-b" {
		t.Fatalf("expected new pub: %+v", again)
	}
	if again.CreatedAt != first.CreatedAt {
		t.Fatal("createdAt should be preserved")
	}
}

func TestPairRequiresDevicePub(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := m.PairSecret()
	if _, err := m.ValidateAndPair(secret, "dev-1", "iPad", ""); err == nil {
		t.Fatal("expected devicePub required")
	}
}

func TestTLSConfigRequestsClientCert(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.TLSConfig()
	if cfg.ClientAuth != tls.RequestClientCert {
		t.Fatalf("ClientAuth=%v", cfg.ClientAuth)
	}
	sess := m.SessionTLSConfig()
	if sess.ClientAuth != tls.RequestClientCert {
		t.Fatalf("Session ClientAuth=%v", sess.ClientAuth)
	}
}

func TestAuthenticatePeerByCert(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	cert, spkiB64 := mustClientCert(t)
	secret, _ := m.PairSecret()
	dev, err := m.ValidateAndPair(secret, "dev-mtls", "iPad", spkiB64)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.AuthenticatePeer([]*x509.Certificate{cert})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != dev.ID {
		t.Fatalf("got %+v", got)
	}
}

func TestAuthenticatePeerRejectsTokenOnly(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := m.PairSecret()
	if _, err := m.ValidateAndPair(secret, "dev-tok", "iPad", "pub-x"); err != nil {
		t.Fatal(err)
	}
	_, err = m.AuthenticatePeer(nil)
	if err == nil {
		t.Fatal("expected client certificate required")
	}
}

func TestAuthenticatePeerMiss(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := mustClientCert(t)
	_, err = m.AuthenticatePeer([]*x509.Certificate{cert})
	if err == nil {
		t.Fatal("expected unauthorized")
	}
}

func TestAuthenticateProof(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	spkiB64 := base64.StdEncoding.EncodeToString(spki)
	secret, _ := m.PairSecret()
	if _, err := m.ValidateAndPair(secret, "dev-proof", "iPad", spkiB64); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Unix()
	nonce := m.IssueAuthNonce()
	sum := sha256.Sum256([]byte(AuthMessage(m.Fingerprint(), ts, nonce)))
	sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	dev, err := m.AuthenticateSession(nil, AuthProof{
		DeviceID: "dev-proof",
		TS:       ts,
		Nonce:    nonce,
		SigB64:   base64.StdEncoding.EncodeToString(sig),
	})
	if err != nil {
		t.Fatal(err)
	}
	if dev.ID != "dev-proof" {
		t.Fatalf("got %+v", dev)
	}
	// Replay of the same proof must fail (nonce already consumed).
	_, err = m.AuthenticateSession(nil, AuthProof{
		DeviceID: "dev-proof",
		TS:       ts,
		Nonce:    nonce,
		SigB64:   base64.StdEncoding.EncodeToString(sig),
	})
	if err == nil {
		t.Fatal("expected replay rejection")
	}
	_, err = m.AuthenticateSession(nil, AuthProof{})
	if err == nil {
		t.Fatal("expected proof required")
	}
}

func mustClientCert(t *testing.T) (*x509.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "SpadeIDE Device"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, base64.StdEncoding.EncodeToString(cert.RawSubjectPublicKeyInfo)
}
