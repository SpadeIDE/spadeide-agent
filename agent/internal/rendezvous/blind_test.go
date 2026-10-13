package rendezvous

import (
	"crypto/rand"
	"testing"
	"time"
)

func TestRIDStableWithinEpoch(t *testing.T) {
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	e := Epoch(time.Unix(1_700_000_000, 0))
	a := RID(secret, e)
	b := RID(secret, e)
	if a != b || a == "" {
		t.Fatalf("unstable rid %q vs %q", a, b)
	}
	if RID(secret, e+1) == a {
		t.Fatal("adjacent epochs must differ")
	}
}

func TestProveMACRoundTrip(t *testing.T) {
	secret := []byte("test-secret-32-bytes-long!!!!!!")
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	mac := ProveMAC(secret, nonce)
	if !VerifyProveMAC(secret, nonce, mac) {
		t.Fatal("mac verify failed")
	}
	mac[0] ^= 0xff
	if VerifyProveMAC(secret, nonce, mac) {
		t.Fatal("tampered mac accepted")
	}
}

func TestCurrentRIDsLen(t *testing.T) {
	secret := make([]byte, 32)
	rids := CurrentRIDs(secret, time.Now())
	if len(rids) != 2 || rids[0] == rids[1] {
		t.Fatalf("%v", rids)
	}
}

func TestMergeRIDsTwoDevices(t *testing.T) {
	a := make([]byte, 32)
	b := make([]byte, 32)
	for i := range a {
		a[i] = byte(i + 1)
		b[i] = byte(100 + i)
	}
	now := time.Unix(1_700_000_000, 0)
	rids := mergeRIDs([][]byte{a, b}, now)
	if len(rids) != 4 {
		t.Fatalf("want 4 rids, got %v", rids)
	}
	seen := map[string]bool{}
	for _, r := range rids {
		if seen[r] {
			t.Fatalf("dup %s", r)
		}
		seen[r] = true
	}
}

func TestRIDGoldenVector(t *testing.T) {
	// Fixed vector shared with iOS BlindRendezvous (do not change).
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	const epoch uint64 = 472222
	got := RID(secret, epoch)
	const want = "22crh5d3gi3ugxyv3lbdmbovjm"
	if got != want {
		t.Fatalf("rid=%q want %q", got, want)
	}
}
