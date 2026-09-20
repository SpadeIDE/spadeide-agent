package rendezvous

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"strings"
	"time"
)

const (
	ridDomain   = "spade-rv"
	proveDomain = "spade-prove"
	ridBytes    = 16
)

// Epoch is the 1-hour bucket used for blinded rendezvous handles.
func Epoch(t time.Time) uint64 {
	return uint64(t.Unix() / 3600)
}

// RID derives an opaque rendezvous handle for one epoch.
// rid = base32lower(HMAC-SHA256(secret, "spade-rv" || be64(epoch))[:16])
func RID(secret []byte, epoch uint64) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(ridDomain))
	var eb [8]byte
	binary.BigEndian.PutUint64(eb[:], epoch)
	_, _ = mac.Write(eb[:])
	sum := mac.Sum(nil)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:ridBytes]))
}

// CurrentRIDs returns handles for epoch and epoch+1 (clock-skew tolerance).
func CurrentRIDs(secret []byte, now time.Time) []string {
	e := Epoch(now)
	return []string{RID(secret, e), RID(secret, e+1)}
}

// ProveMAC = HMAC-SHA256(secret, "spade-prove" || nonce).
func ProveMAC(secret, nonce []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(proveDomain))
	_, _ = mac.Write(nonce)
	return mac.Sum(nil)
}

// VerifyProveMAC checks a prove MAC in constant time.
func VerifyProveMAC(secret, nonce, got []byte) bool {
	want := ProveMAC(secret, nonce)
	return hmac.Equal(want, got)
}
