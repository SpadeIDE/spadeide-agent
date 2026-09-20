package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Agent holds the long-lived Ed25519 identity used for agentId and (later)
// rendezvous slot ownership proofs.
type Agent struct {
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
	ID      string // base32(sha256(pub)), no padding, lower-case
}

// LoadOrCreate reads ~/.spade-agent/identity or mints a new keypair (0600).
func LoadOrCreate(dir string) (*Agent, error) {
	path := filepath.Join(dir, "identity")
	if data, err := os.ReadFile(path); err == nil {
		return parse(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block := &pem.Block{
		Type:  "SPADEIDE AGENT PRIVATE KEY",
		Bytes: priv.Seed(),
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	return &Agent{
		Private: priv,
		Public:  pub,
		ID:      DeriveID(pub),
	}, nil
}

func parse(data []byte) (*Agent, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("identity: not PEM")
	}
	if len(block.Bytes) != ed25519.SeedSize {
		return nil, fmt.Errorf("identity: bad seed length %d", len(block.Bytes))
	}
	priv := ed25519.NewKeyFromSeed(block.Bytes)
	pub := priv.Public().(ed25519.PublicKey)
	return &Agent{Private: priv, Public: pub, ID: DeriveID(pub)}, nil
}

// DeriveID is agentId = base32(sha256(pubkey)) without padding.
func DeriveID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	return strings.ToLower(enc)
}

// PublicBase64 returns the raw 32-byte public key as standard base64.
func (a *Agent) PublicBase64() string {
	return base64.StdEncoding.EncodeToString(a.Public)
}

// Sign signs a message with the identity key.
func (a *Agent) Sign(msg []byte) []byte {
	return ed25519.Sign(a.Private, msg)
}
