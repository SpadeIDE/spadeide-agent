package rendezvous

import (
	"crypto/tls"
	"fmt"
	"net/url"
	"strings"

	"github.com/spadeide/spade-agent/internal/config"
)

// ProductURL is the hosted introducer baked into agent builds.
// Override at runtime with SPADE_RENDEZVOUS_URL or `spadeide remote on --server`.
const ProductURL = "wss://rendezvous.spadeide.com"

// DefaultURL is the product rendezvous when RemoteServer is empty.
// SPADE_RENDEZVOUS_URL overrides for smoke / staging.
func DefaultURL() string {
	if v := config.EnvRendezvous(); v != "" {
		return v
	}
	return ProductURL
}

// ServerURL is a validated ws:// or wss:// rendezvous base URL.
type ServerURL struct {
	u *url.URL
}

// ParseServerURL parses and validates a rendezvous WebSocket URL.
func ParseServerURL(raw string) (ServerURL, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "/")
	if raw == "" {
		return ServerURL{}, fmt.Errorf("empty rendezvous URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ServerURL{}, err
	}
	if u.Scheme != "wss" && u.Scheme != "ws" {
		return ServerURL{}, fmt.Errorf("rendezvous URL must be wss:// (got %q)", raw)
	}
	if u.Hostname() == "" {
		return ServerURL{}, fmt.Errorf("no host in %q", raw)
	}
	return ServerURL{u: u}, nil
}

func (s ServerURL) clone() url.URL {
	u := *s.u
	return u
}

// AgentURL is the registration WebSocket path.
func (s ServerURL) AgentURL() string {
	u := s.clone()
	u.Path = "/v1/agent"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// RelayURL is the relay WebSocket path for a ticket.
func (s ServerURL) RelayURL(ticket string) string {
	u := s.clone()
	u.Path = "/v1/relay"
	q := u.Query()
	q.Set("ticket", ticket)
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String()
}

// TLSConfig returns a client TLS config with InsecureSkipVerify only for loopback hosts.
func (s ServerURL) TLSConfig() *tls.Config {
	host := strings.ToLower(s.u.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return &tls.Config{InsecureSkipVerify: true} //nolint:gosec // loopback smoke only
	}
	return nil
}

func (s ServerURL) String() string {
	return s.u.String()
}
