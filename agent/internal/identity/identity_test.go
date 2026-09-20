package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateStable(t *testing.T) {
	dir := t.TempDir()
	a1, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a1.ID != a2.ID {
		t.Fatalf("id changed: %s vs %s", a1.ID, a2.ID)
	}
	if a1.PublicBase64() != base64.StdEncoding.EncodeToString(a2.Public) {
		t.Fatal("pub mismatch")
	}
	info, err := os.Stat(filepath.Join(dir, "identity"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("identity perms too open: %o", info.Mode().Perm())
	}
}

func TestDeriveIDLength(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	id := DeriveID(pub)
	if len(id) < 40 {
		t.Fatalf("short id %q", id)
	}
}
