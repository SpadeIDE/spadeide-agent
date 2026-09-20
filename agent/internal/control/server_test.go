package control

import (
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
