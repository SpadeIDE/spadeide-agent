package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentUpsertDevice(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("dev-%02d", i)
			errs <- store.UpsertDevice(Device{ID: id, Name: id, CreatedAt: int64(i)})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	st, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Devices) != n {
		t.Fatalf("got %d devices, want %d", len(st.Devices), n)
	}
	seen := map[string]bool{}
	for _, d := range st.Devices {
		seen[d.ID] = true
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("dev-%02d", i)
		if !seen[id] {
			t.Fatalf("missing %s", id)
		}
	}

	store2, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	st2, err := store2.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(st2.Devices) != n {
		t.Fatalf("reloaded %d devices, want %d", len(st2.Devices), n)
	}
}

func TestEnsureRvSecretIdempotent(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.EnsureRvSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 32 {
		t.Fatalf("len=%d", len(a))
	}
	b, err := store.EnsureRvSecret()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("EnsureRvSecret minted a second secret")
	}
}

func TestPerDeviceRvSecretUnpairIndependent(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDevice(Device{ID: "a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDevice(Device{ID: "b", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	sa, err := store.EnsureDeviceRvSecret("a")
	if err != nil {
		t.Fatal(err)
	}
	sb, err := store.EnsureDeviceRvSecret("b")
	if err != nil {
		t.Fatal(err)
	}
	if sa == "" || sb == "" || sa == sb {
		t.Fatalf("secrets should differ: %q %q", sa, sb)
	}
	again, err := store.EnsureDeviceRvSecret("a")
	if err != nil || again != sa {
		t.Fatalf("re-ensure rotated secret: %q vs %q (%v)", again, sa, err)
	}
	if _, err := store.RemoveDevice("a"); err != nil {
		t.Fatal(err)
	}
	secrets, err := store.AllDeviceRvSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 1 {
		t.Fatalf("after unpair A: secrets=%d", len(secrets))
	}
	devB, err := store.FindDeviceByID("b")
	if err != nil || devB.RvSecret != sb {
		t.Fatalf("B secret lost: %+v %v", devB, err)
	}
}

func TestMigrateLegacyRvSecrets(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := store.EnsureRvSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDevice(Device{ID: "old", Name: "Old"}); err != nil {
		t.Fatal(err)
	}
	n, err := store.MigrateLegacyRvSecrets()
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	dev, err := store.FindDeviceByID("old")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := decodeRvSecret(dev.RvSecret)
	if err != nil || string(raw) != string(legacy) {
		t.Fatalf("migrated=%q legacy=%q err=%v", dev.RvSecret, legacy, err)
	}
	n, err = store.MigrateLegacyRvSecrets()
	if err != nil || n != 0 {
		t.Fatalf("second migrate n=%d err=%v", n, err)
	}
	// Next pair upgrades the shared legacy copy to a unique secret.
	upgraded, err := store.EnsureDeviceRvSecret("old")
	if err != nil {
		t.Fatal(err)
	}
	if upgraded == "" || upgraded == base64.StdEncoding.EncodeToString(legacy) {
		t.Fatalf("expected unique upgrade, got %q", upgraded)
	}
}

func TestSaveStateLeavesNoTmp(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDevice(Device{ID: "a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("tmp left behind: %v", err)
	}
}

func TestDefaultDirHonoursSPADE_AGENT_HOME(t *testing.T) {
	t.Setenv("SPADE_AGENT_HOME", "/tmp/spade-test-home")
	t.Setenv("SPADE_AGENT_MODE", "")
	t.Setenv("INVOCATION_ID", "")
	got, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/spade-test-home" {
		t.Fatalf("got %q", got)
	}
}

func TestDefaultDirSystemMode(t *testing.T) {
	t.Setenv("SPADE_AGENT_HOME", "")
	t.Setenv("SPADE_AGENT_MODE", "system")
	t.Setenv("INVOCATION_ID", "")
	got, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != SystemStateDir {
		t.Fatalf("got %q want %q", got, SystemStateDir)
	}
}

func TestDefaultDirUserHome(t *testing.T) {
	t.Setenv("SPADE_AGENT_HOME", "")
	t.Setenv("SPADE_AGENT_MODE", "")
	t.Setenv("INVOCATION_ID", "")
	got, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if got == SystemStateDir {
		t.Fatal("user mode must not use system state dir")
	}
	if filepath.Base(got) != ".spade-agent" {
		t.Fatalf("got %q, want …/.spade-agent", got)
	}
}

func TestUpdateRemoteAccessMerge(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetRemoteAccess(true, "wss://a.example"); err != nil {
		t.Fatal(err)
	}
	srv := "wss://b.example"
	if err := store.UpdateRemoteAccess(nil, &srv); err != nil {
		t.Fatal(err)
	}
	on, got, err := store.RemoteAccess()
	if err != nil {
		t.Fatal(err)
	}
	if !on || got != "wss://b.example" {
		t.Fatalf("on=%v srv=%q", on, got)
	}
	off := false
	if err := store.UpdateRemoteAccess(&off, nil); err != nil {
		t.Fatal(err)
	}
	on, got, err = store.RemoteAccess()
	if err != nil {
		t.Fatal(err)
	}
	if on || got != "wss://b.example" {
		t.Fatalf("on=%v srv=%q after disable", on, got)
	}
}

func TestSaveStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	st := &State{
		Devices:      []Device{{ID: "x", Name: "X"}},
		RemoteAccess: true,
		RemoteServer: "wss://rv.example",
	}
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	other, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := other.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Devices) != 1 || got.Devices[0].ID != "x" {
		t.Fatalf("devices=%+v", got.Devices)
	}
	if !got.RemoteAccess || got.RemoteServer != "wss://rv.example" {
		t.Fatalf("remote=%v %q", got.RemoteAccess, got.RemoteServer)
	}
}

func TestRemoveDeviceMissing(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ok, err := store.RemoveDevice("nope")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestRemoteAccessDefaultsOn(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	on, _, err := store.RemoteAccess()
	if err != nil {
		t.Fatal(err)
	}
	if !on {
		t.Fatal("fresh install should default remote access on")
	}
}

func TestMigrateRemoteAccessDefaultOn(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-migration install that still has the old default (off).
	if err := store.SaveState(&State{RemoteAccess: false}); err != nil {
		t.Fatal(err)
	}
	other, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	flipped, err := other.MigrateRemoteAccessDefaultOn()
	if err != nil {
		t.Fatal(err)
	}
	if !flipped {
		t.Fatal("expected flip from off → on")
	}
	on, _, err := other.RemoteAccess()
	if err != nil || !on {
		t.Fatalf("on=%v err=%v", on, err)
	}
	flipped, err = other.MigrateRemoteAccessDefaultOn()
	if err != nil || flipped {
		t.Fatalf("second migrate should no-op: flipped=%v err=%v", flipped, err)
	}
	off := false
	if err := other.UpdateRemoteAccess(&off, nil); err != nil {
		t.Fatal(err)
	}
	flipped, err = other.MigrateRemoteAccessDefaultOn()
	if err != nil || flipped {
		t.Fatalf("explicit off must stick: flipped=%v err=%v", flipped, err)
	}
	on, _, err = other.RemoteAccess()
	if err != nil || on {
		t.Fatalf("want off after explicit disable, on=%v err=%v", on, err)
	}
}

func TestCacheReloadsWhenDiskChanges(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDevice(Device{ID: "a", Name: "A"}); err != nil {
		t.Fatal(err)
	}

	// Simulate CLI writing state.json while the daemon holds a cache.
	other, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.UpsertDevice(Device{ID: "b", Name: "B"}); err != nil {
		t.Fatal(err)
	}

	st, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Devices) != 2 {
		t.Fatalf("daemon cache stale: got %d devices, want 2", len(st.Devices))
	}
	seen := map[string]bool{}
	for _, d := range st.Devices {
		seen[d.ID] = true
	}
	if !seen["a"] || !seen["b"] {
		t.Fatalf("devices=%v", seen)
	}
}
