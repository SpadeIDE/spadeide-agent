package rendezvous

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spadeide/spade-agent/internal/identity"
)

var errEpochRefresh = errors.New("epoch refresh")

// DefaultURL is the product rendezvous when RemoteServer is empty.
// SPADE_RENDEZVOUS_URL overrides for smoke / staging.
var DefaultURL = func() string {
	if v := strings.TrimSpace(os.Getenv("SPADE_RENDEZVOUS_URL")); v != "" {
		return v
	}
	return ProductURL
}()

// Dialer keeps a registration WebSocket to the rendezvous while remote access is on.
type Dialer struct {
	Identity *identity.Agent
	// RvSecret returns the durable blinded-handle key (32 bytes), or nil/empty
	// to register aid-only (legacy pairings).
	RvSecret func() []byte
	// OnOffer is called with the agent relay ticket when a client looks us up.
	// punch is an optional ephemeral UDP socket already mapped via reflex
	// (caller ServePunchQUIC); may be nil if reflex failed.
	// mode is "full" (punch+relay) or "punch" (upgrade only — no new relay).
	OnOffer func(ctx context.Context, sid, ticket, baseURL, mode string, punch net.PacketConn)
	// MaxOffersPerMinute bounds how many offers are accepted from rendezvous
	// (0 = default 12). Excess offers are dropped.
	MaxOffersPerMinute int

	mu         sync.Mutex
	cancel     context.CancelFunc
	enabled    bool
	server     string
	registered bool
	offerTimes []time.Time
}

type envelope map[string]any

// State reports toggle + whether the registration socket is live.
func (d *Dialer) State() (enabled bool, server string, registered bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	srv := d.server
	if srv == "" {
		srv = DefaultURL
	}
	return d.enabled, srv, d.registered
}

// Apply starts or stops the dialer to match the persisted remote-access setting.
func (d *Dialer) Apply(enabled bool, server string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	d.enabled = enabled
	d.registered = false
	d.server = strings.TrimSpace(server)
	if !enabled {
		log.Printf("rendezvous: remote access off")
		return
	}
	base := d.server
	if base == "" {
		base = DefaultURL
	}
	if base == "" {
		log.Printf("rendezvous: remote on but no server URL")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	go d.loop(ctx, base)
}

func (d *Dialer) loop(ctx context.Context, base string) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := d.session(ctx, base)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errEpochRefresh) {
			backoff = time.Second
			continue
		}
		log.Printf("rendezvous: disconnected (%v); retry in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (d *Dialer) session(ctx context.Context, base string) error {
	defer func() {
		d.mu.Lock()
		d.registered = false
		d.mu.Unlock()
	}()

	agentURL, err := joinPath(base, "/v1/agent")
	if err != nil {
		return err
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}
	if allowsInsecure(base) {
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // loopback smoke only
	}
	conn, _, err := dialer.DialContext(ctx, agentURL, http.Header{})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	hello := envelope{"t": "hello", "aid": d.Identity.ID, "proto": 1}
	var secret []byte
	if d.RvSecret != nil {
		secret = d.RvSecret()
	}
	if len(secret) == 32 {
		rids := CurrentRIDs(secret, time.Now())
		hello["rids"] = rids
		hello["proto"] = 2
	}
	if err := conn.WriteJSON(hello); err != nil {
		return err
	}
	var msg envelope
	if err := conn.ReadJSON(&msg); err != nil {
		return err
	}
	if msg["t"] != "challenge" {
		return fmt.Errorf("expected challenge, got %v", msg["t"])
	}
	nonceB64, _ := msg["nonce"].(string)
	nonce, err := base64.StdEncoding.DecodeString(nonceB64)
	if err != nil {
		return fmt.Errorf("bad nonce: %w", err)
	}
	prove := envelope{
		"t":   "prove",
		"pub": d.Identity.PublicBase64(),
		"sig": base64.StdEncoding.EncodeToString(d.Identity.Sign(nonce)),
	}
	if len(secret) == 32 {
		prove["mac"] = base64.StdEncoding.EncodeToString(ProveMAC(secret, nonce))
	}
	if err := conn.WriteJSON(prove); err != nil {
		return err
	}
	if err := conn.ReadJSON(&msg); err != nil {
		return err
	}
	if msg["t"] != "ready" {
		if msg["t"] == "error" {
			return fmt.Errorf("rendezvous: %v", msg["msg"])
		}
		return fmt.Errorf("expected ready, got %v", msg["t"])
	}
	d.mu.Lock()
	d.registered = true
	d.mu.Unlock()
	if len(secret) == 32 {
		log.Printf("rendezvous: registered at %s (aid+rid)", base)
	} else {
		log.Printf("rendezvous: registered at %s (aid)", base)
	}

	pingEvery := 30 * time.Second
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()

	// Re-handshake before the next epoch so epoch+1 stays covered.
	refreshAt := epochRefreshAt(time.Now())
	refreshTimer := time.NewTimer(time.Until(refreshAt))
	defer refreshTimer.Stop()

	var writeMu sync.Mutex
	writeJSON := func(v envelope) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(v)
	}

	readErr := make(chan error, 1)
	go func() {
		for {
			_ = conn.SetReadDeadline(time.Now().Add(6 * time.Minute))
			var m envelope
			if err := conn.ReadJSON(&m); err != nil {
				readErr <- err
				return
			}
			switch m["t"] {
			case "pong":
				// ok
			case "offer":
				sid, _ := m["sid"].(string)
				ticket, _ := m["ticket"].(string)
				mode, _ := m["mode"].(string)
				if mode != "punch" {
					mode = "full"
				}
				if sid == "" || ticket == "" || d.OnOffer == nil {
					continue
				}
				if !d.allowOffer() {
					log.Printf("rendezvous: dropping offer sid=%s (rate limit)", sid)
					continue
				}
				go d.handleOffer(ctx, base, sid, ticket, mode, writeJSON)
			case "error":
				readErr <- fmt.Errorf("server: %v", m["msg"])
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return err
		case <-refreshTimer.C:
			return errEpochRefresh
		case <-ping.C:
			if err := writeJSON(envelope{"t": "ping"}); err != nil {
				return err
			}
		}
	}
}

// epochRefreshAt is 2 minutes before the next hour-bucket boundary.
func epochRefreshAt(now time.Time) time.Time {
	e := Epoch(now)
	next := time.Unix(int64((e+1)*3600), 0)
	at := next.Add(-2 * time.Minute)
	if !at.After(now) {
		return now.Add(time.Minute)
	}
	return at
}

func (d *Dialer) handleOffer(
	ctx context.Context,
	base, sid, ticket, mode string,
	writeJSON func(envelope) error,
) {
	var punch net.PacketConn
	var direct string
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	pc, addr, err := OpenPunchSocket(pctx, base)
	cancel()
	if err != nil {
		log.Printf("rendezvous: punch/reflex failed sid=%s: %v", sid, err)
	} else {
		punch, direct = pc, addr
		log.Printf("rendezvous: direct candidate sid=%s %s mode=%s", sid, direct, mode)
	}
	if err := writeJSON(envelope{"t": "offer-ack", "sid": sid, "direct": direct}); err != nil {
		log.Printf("rendezvous: offer-ack failed: %v", err)
		if punch != nil {
			_ = punch.Close()
		}
		return
	}
	d.OnOffer(ctx, sid, ticket, base, mode, punch)
}

func (d *Dialer) allowOffer() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	limit := d.MaxOffersPerMinute
	if limit <= 0 {
		limit = 12
	}
	now := time.Now()
	cut := now.Add(-time.Minute)
	kept := d.offerTimes[:0]
	for _, t := range d.offerTimes {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	d.offerTimes = kept
	if len(d.offerTimes) >= limit {
		return false
	}
	d.offerTimes = append(d.offerTimes, now)
	return true
}

// DialRelay opens the relay WebSocket for an offer ticket.
func DialRelay(ctx context.Context, base, ticket string) (*websocket.Conn, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	u.Path = "/v1/relay"
	q := u.Query()
	q.Set("ticket", ticket)
	u.RawQuery = q.Encode()
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	if allowsInsecure(base) {
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // loopback smoke only
	}
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	return conn, err
}

// WrapRelay returns a stream-oriented net.Conn over the relay WebSocket.
func WrapRelay(ws *websocket.Conn) net.Conn {
	return newWSConn(ws)
}

func allowsInsecure(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func joinPath(base, path string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", err
	}
	if u.Scheme != "wss" && u.Scheme != "ws" {
		return "", fmt.Errorf("rendezvous URL must be wss:// (got %q)", base)
	}
	u.Path = path
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
