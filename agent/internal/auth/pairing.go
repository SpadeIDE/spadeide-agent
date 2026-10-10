package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/spadeide/spade-agent/internal/config"
)

type pairingState struct {
	mu sync.Mutex

	pairSecret  string
	pairExpires time.Time
	// Previous secret stays valid until prevExpires so a QR scanned during
	// rotate / "New pair code" still works.
	prevSecret  string
	prevExpires time.Time
}

func (p *pairingState) currentPairSecret() (secret string, expires time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pairSecret == "" || time.Now().After(p.pairExpires) {
		p.rotatePairSecretLocked()
	}
	return p.pairSecret, p.pairExpires
}

func (p *pairingState) rotatePairSecret() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rotatePairSecretLocked()
}

// rotatePairSecretLocked mints a fresh secret; the caller must hold p.mu.
func (p *pairingState) rotatePairSecretLocked() string {
	// Keep the outgoing code valid until its original deadline (or a short floor)
	// so an in-flight scan isn't racing an explicit "New pair code".
	if p.pairSecret != "" {
		p.prevSecret = p.pairSecret
		p.prevExpires = p.pairExpires
		if p.prevExpires.Before(time.Now().Add(2 * time.Minute)) {
			p.prevExpires = time.Now().Add(2 * time.Minute)
		}
	}
	return p.mintSecretLocked()
}

func (p *pairingState) mintSecretLocked() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Pairing secret must never be all-zero; crypto/rand failure is fatal.
		panic("crypto/rand: " + err.Error())
	}
	p.pairSecret = hex.EncodeToString(b)
	p.pairExpires = time.Now().Add(10 * time.Minute)
	return p.pairSecret
}

func (p *pairingState) validateAndPair(store *config.Store, secret, deviceID, deviceName, devicePub string) (*config.Device, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if devicePub == "" {
		return nil, errors.New("devicePub required")
	}

	var existing *config.Device
	if deviceID != "" {
		if ex, err := store.FindDeviceByID(deviceID); err == nil && ex != nil {
			existing = ex
			if ex.DevicePub == devicePub {
				// Idempotent re-pair: keep Name and Unix bind. /pair is not a
				// rename API (A11); UpsertDevice replaces the whole record and
				// would wipe UnixUser on a system install.
				out := *ex
				return &out, nil
			}
			// Fall through to the secret check — key change is a new pairing.
		}
	}

	now := time.Now()
	matchCurrent := secretEqual(secret, p.pairSecret)
	matchPrev := secretEqual(secret, p.prevSecret)
	okCurrent := matchCurrent && now.Before(p.pairExpires)
	okPrev := matchPrev && now.Before(p.prevExpires)
	if !okCurrent && !okPrev {
		if matchCurrent || matchPrev {
			return nil, errors.New("pairing secret expired")
		}
		return nil, errors.New("bad pairing secret")
	}
	createdAt := time.Now().Unix()
	if existing != nil {
		createdAt = existing.CreatedAt
	}
	dev := config.Device{
		ID:        deviceID,
		Name:      deviceName,
		DevicePub: devicePub,
		CreatedAt: createdAt,
	}
	if err := store.UpsertDevice(dev); err != nil {
		return nil, err
	}
	// One-time: burn both slots, then mint a fresh unused code (do not park the
	// consumed secret in prev — that would allow a second pair with the same QR).
	p.prevSecret = ""
	p.prevExpires = time.Time{}
	p.mintSecretLocked()
	return &dev, nil
}

// secretEqual is a constant-time compare for pairing secrets (empty never matches).
func secretEqual(got, want string) bool {
	if got == "" || want == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
