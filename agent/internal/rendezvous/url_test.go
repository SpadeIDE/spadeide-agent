package rendezvous

import (
	"os"
	"strings"
	"testing"
)

func TestParseServerURL(t *testing.T) {
	s, err := ParseServerURL("wss://rv.example/path")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.AgentURL(); got != "wss://rv.example/v1/agent" {
		t.Fatalf("AgentURL=%q", got)
	}
	if got := s.RelayURL("tk"); !strings.Contains(got, "ticket=tk") || !strings.Contains(got, "/v1/relay") {
		t.Fatalf("RelayURL=%q", got)
	}

	if _, err := ParseServerURL("https://rv.example"); err == nil {
		t.Fatal("https should fail")
	}
	if _, err := ParseServerURL("http://rv.example"); err == nil {
		t.Fatal("http should fail")
	}
}

func TestServerURLTLSConfigLoopback(t *testing.T) {
	s, err := ParseServerURL("ws://127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.TLSConfig()
	if cfg == nil || !cfg.InsecureSkipVerify {
		t.Fatalf("want loopback InsecureSkipVerify, got %#v", cfg)
	}

	s2, err := ParseServerURL("wss://rv.example")
	if err != nil {
		t.Fatal(err)
	}
	if cfg := s2.TLSConfig(); cfg != nil {
		t.Fatalf("want nil TLS for remote, got %#v", cfg)
	}
}

func TestDefaultURLReadsEnv(t *testing.T) {
	const key = "SPADE_RENDEZVOUS_URL"
	prev, had := os.LookupEnv(key)
	t.Setenv(key, "ws://127.0.0.1:1")
	got := DefaultURL()
	if got != "ws://127.0.0.1:1" {
		t.Fatalf("got %q", got)
	}
	if had {
		t.Setenv(key, prev)
	} else {
		os.Unsetenv(key)
	}
}
