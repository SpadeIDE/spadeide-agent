package control

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spadeide/spade-agent/internal/auth"
	"github.com/spadeide/spade-agent/internal/config"
	"github.com/spadeide/spade-agent/internal/identity"
	"github.com/spadeide/spade-agent/internal/server"
)

func TestGuardRejectsMissingToken(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authMgr, err := auth.NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.LoadOrCreate(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	agent := server.New(0, store, authMgr, id)
	s := New(0, store, authMgr, agent)
	s.token = "secret-token"

	h := s.guard(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	h(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
}

func TestHandleQRReturnsPNG(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authMgr, err := auth.NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.LoadOrCreate(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	agent := server.New(7422, store, authMgr, id)
	s := New(0, store, authMgr, agent)
	s.token = "secret-token"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/pair/qr.png", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	s.guard(s.handleQR)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type=%q", ct)
	}
	body := rec.Body.Bytes()
	if len(body) < 8 || string(body[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("not a PNG (%d bytes)", len(body))
	}
}

func TestEnsureTokenMintsAndPersists(t *testing.T) {
	t.Setenv("SPADE_CONTROL_TOKEN", "")
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authMgr, err := auth.NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.LoadOrCreate(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(0, store, authMgr, server.New(0, store, authMgr, id))
	tok, err := s.EnsureToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" || s.token == "" {
		t.Fatal("expected minted token")
	}
	if got := store.ReadControlToken(); got != tok {
		t.Fatalf("disk=%q want %q", got, tok)
	}
}

func TestGuardFailsClosedWhenTokenEmpty(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authMgr, err := auth.NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.LoadOrCreate(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(0, store, authMgr, server.New(0, store, authMgr, id))
	// Intentionally leave s.token empty (bypass EnsureToken).
	h := s.guard(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	h(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("empty token must 401, got %d", rec.Code)
	}
}

func TestHandleRemoteSetsServerWithoutClearingEnabled(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetRemoteAccess(true, "wss://old.example"); err != nil {
		t.Fatal(err)
	}
	authMgr, err := auth.NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.LoadOrCreate(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(0, store, authMgr, server.New(0, store, authMgr, id))
	s.token = "secret-token"

	post := func(body any) *httptest.ResponseRecorder {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/remote", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer secret-token")
		req.Header.Set("Content-Type", "application/json")
		s.guard(s.handleRemote)(rec, req)
		return rec
	}

	rec := post(map[string]any{"server": "wss://rv.example"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	on, srv, err := store.RemoteAccess()
	if err != nil {
		t.Fatal(err)
	}
	if !on {
		t.Fatal("enabled should stay true")
	}
	if srv != "wss://rv.example" {
		t.Fatalf("server=%q", srv)
	}

	rec = post(map[string]any{"server": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", rec.Code, rec.Body.String())
	}
	on, srv, err = store.RemoteAccess()
	if err != nil {
		t.Fatal(err)
	}
	if !on || srv != "" {
		t.Fatalf("want enabled + empty server, got on=%v srv=%q", on, srv)
	}

	rec = post(map[string]any{"enabled": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle status=%d body=%s", rec.Code, rec.Body.String())
	}
	on, srv, err = store.RemoteAccess()
	if err != nil {
		t.Fatal(err)
	}
	if on {
		t.Fatal("expected remote off")
	}
	if srv != "" {
		t.Fatalf("server should stay empty, got %q", srv)
	}
}

func TestHandleRemoteRejectsInvalidServerURL(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetRemoteAccess(true, "wss://good.example"); err != nil {
		t.Fatal(err)
	}
	authMgr, err := auth.NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.LoadOrCreate(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(0, store, authMgr, server.New(0, store, authMgr, id))
	s.token = "secret-token"

	raw, err := json.Marshal(map[string]any{"server": "https://not-ws.example"})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/remote", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Content-Type", "application/json")
	s.guard(s.handleRemote)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}

	on, srv, err := store.RemoteAccess()
	if err != nil {
		t.Fatal(err)
	}
	if !on || srv != "wss://good.example" {
		t.Fatalf("store mutated on bad URL: on=%v srv=%q", on, srv)
	}
}

type applyProbe struct {
	n       int
	enabled bool
	server  string
}

func (p *applyProbe) Apply(enabled bool, server string) {
	p.n++
	p.enabled = enabled
	p.server = server
}

func (p *applyProbe) State() (bool, string, bool) {
	return p.enabled, p.server, false
}

func TestUnpairAndResetRefreshRendezvous(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetRemoteAccess(true, "wss://rv.example"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDevice(config.Device{ID: "dev-a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDevice(config.Device{ID: "dev-b", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	authMgr, err := auth.NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.LoadOrCreate(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	agent := server.New(0, store, authMgr, id)
	probe := &applyProbe{}
	// Same wiring as serve.go: hook → Remote.Apply.
	agent.OnDevicesChanged = func() {
		on, srv, err := store.RemoteAccess()
		if err != nil || !on {
			return
		}
		probe.Apply(on, srv)
	}
	s := New(0, store, authMgr, agent)
	s.Remote = probe
	s.token = "secret-token"

	del := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v1/pair/devices/dev-a", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	s.guard(s.handleDevice)(del, req)
	if del.Code != http.StatusOK {
		t.Fatalf("unpair status=%d body=%s", del.Code, del.Body.String())
	}
	if probe.n != 1 || !probe.enabled || probe.server != "wss://rv.example" {
		t.Fatalf("unpair refresh: %+v", probe)
	}

	reset := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/pair/reset", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	s.guard(s.handleReset)(reset, req)
	if reset.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", reset.Code, reset.Body.String())
	}
	if probe.n != 2 {
		t.Fatalf("reset should refresh again: n=%d", probe.n)
	}
}
