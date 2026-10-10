package auth

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/spadeide/spade-agent/internal/config"
)

type sessionAuth struct {
	mu sync.Mutex

	// Single-use auth nonces for device-key proofs (≈2 min TTL).
	pendingNonces map[string]time.Time // nonce → expiry
}

// maxPendingNonces caps pre-auth challenge storage (LAN DoS hygiene).
const maxPendingNonces = 64

func newSessionAuth() *sessionAuth {
	return &sessionAuth{
		pendingNonces: make(map[string]time.Time),
	}
}

// IssueAuthNonce mints a single-use nonce for the next auth proof (≈2 min TTL).
func (s *sessionAuth) issueAuthNonce() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgePendingNoncesLocked(time.Now())
	if len(s.pendingNonces) >= maxPendingNonces {
		s.evictOldestNonceLocked()
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Same policy as mintSecretLocked: never return a weak nonce.
		panic("crypto/rand: " + err.Error())
	}
	nonce := hex.EncodeToString(b)
	s.pendingNonces[nonce] = time.Now().Add(2 * time.Minute)
	return nonce
}

func (s *sessionAuth) purgePendingNoncesLocked(now time.Time) {
	for n, exp := range s.pendingNonces {
		if now.After(exp) {
			delete(s.pendingNonces, n)
		}
	}
}

func (s *sessionAuth) evictOldestNonceLocked() {
	var oldest string
	var oldestExp time.Time
	first := true
	for n, exp := range s.pendingNonces {
		if first || exp.Before(oldestExp) {
			oldest, oldestExp, first = n, exp, false
		}
	}
	if oldest != "" {
		delete(s.pendingNonces, oldest)
	}
}

func (s *sessionAuth) authenticateProof(store *config.Store, agentFingerprint string, proof AuthProof) (*config.Device, error) {
	if proof.DeviceID == "" || proof.SigB64 == "" || proof.TS == 0 || proof.Nonce == "" {
		return nil, errors.New("client certificate or device proof required")
	}
	now := time.Now().Unix()
	// Tight window — nonce already bounds replay; keep clock skew small.
	if proof.TS < now-90 || proof.TS > now+30 {
		return nil, errors.New("auth proof expired")
	}

	s.mu.Lock()
	s.purgePendingNoncesLocked(time.Now())
	exp, ok := s.pendingNonces[proof.Nonce]
	if !ok || time.Now().After(exp) {
		s.mu.Unlock()
		return nil, errors.New("auth nonce invalid")
	}
	delete(s.pendingNonces, proof.Nonce)
	s.mu.Unlock()

	dev, err := store.FindDeviceByID(proof.DeviceID)
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
	msg := AuthMessage(agentFingerprint, proof.TS, proof.Nonce)
	sum := sha256.Sum256([]byte(msg))
	if !ecdsa.VerifyASN1(pub, sum[:], sig) {
		return nil, fmt.Errorf("unauthorized")
	}

	return dev, nil
}
