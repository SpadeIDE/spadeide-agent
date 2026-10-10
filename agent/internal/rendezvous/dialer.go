package rendezvous

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spadeide/spade-agent/internal/identity"
)

var errEpochRefresh = errors.New("epoch refresh")

// Dialer keeps a registration WebSocket to the rendezvous while remote access is on.
type Dialer struct {
	Identity *identity.Agent
	// RvSecret returns the durable blinded-handle key (32 bytes), or nil/empty
	// to register aid-only (legacy pairings).
	RvSecret func() []byte
	// OnOffer is called with the agent relay ticket when a client looks us up.
	// Offer.Punch is an optional ephemeral UDP socket already mapped via reflex
	// (caller ServePunchQUIC); may be nil if reflex failed.
	// Offer.Mode is "full" (punch+relay) or "punch" (upgrade only — no new relay).
	OnOffer func(ctx context.Context, o Offer)
	// MaxOffersPerMinute bounds how many offers are accepted from rendezvous
	// (0 = default 12). Excess offers are dropped.
	MaxOffersPerMinute int

	mu         sync.Mutex
	cancel     context.CancelFunc
	enabled    bool
	server     string
	registered bool
	gen        uint64 // Apply generation; session defer clears registered only if current
	offerTimes []time.Time
}

// retryAfterError is returned when the rendezvous answers 429 with Retry-After.
type retryAfterError struct {
	after time.Duration
	err   error
}

func (e *retryAfterError) Error() string {
	if e.after > 0 {
		return fmt.Sprintf("%v (retry-after %s)", e.err, e.after)
	}
	return e.err.Error()
}

func (e *retryAfterError) Unwrap() error { return e.err }

type envelope map[string]any

// State reports toggle + whether the registration socket is live.
func (d *Dialer) State() (enabled bool, server string, registered bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	srv := d.server
	if srv == "" {
		srv = DefaultURL()
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
	d.gen++
	gen := d.gen
	d.server = strings.TrimSpace(server)
	if !enabled {
		log.Printf("rendezvous: remote access off")
		return
	}
	base := d.server
	if base == "" {
		base = DefaultURL()
	}
	srv, err := ParseServerURL(base)
	if err != nil {
		log.Printf("rendezvous: bad server URL: %v", err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	go d.loop(ctx, srv, gen)
}

// maxRetryAfter caps rendezvous Retry-After. The server ban ladder tops out at
// 24h; honouring that verbatim would park the agent for a day (and, if assigned
// into backoff, forever). 15m is enough to ride out a real ban without locking
// up after a false-positive CGNAT strike.
const maxRetryAfter = 15 * time.Minute

func (d *Dialer) loop(ctx context.Context, srv ServerURL, gen uint64) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		ready, err := d.session(ctx, srv, gen)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errEpochRefresh) {
			backoff = time.Second
			continue
		}
		// Retry-After applies to this wait only — never stick it into backoff.
		wait := backoff
		var ra *retryAfterError
		if errors.As(err, &ra) && ra.after > 0 {
			wait = ra.after
		}
		log.Printf("rendezvous: disconnected (%v); retry in %s", err, wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if ready {
			backoff = time.Second
		} else if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (d *Dialer) session(ctx context.Context, srv ServerURL, gen uint64) (ready bool, err error) {
	defer func() {
		d.mu.Lock()
		if d.gen == gen {
			d.registered = false
		}
		d.mu.Unlock()
	}()

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		TLSClientConfig:  srv.TLSConfig(),
	}
	conn, resp, err := dialer.DialContext(ctx, srv.AgentURL(), http.Header{})
	if err != nil {
		return false, dialWSError("dial", resp, err)
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
		return false, err
	}
	var msg envelope
	if err := conn.ReadJSON(&msg); err != nil {
		return false, err
	}
	if msg["t"] != "challenge" {
		return false, fmt.Errorf("expected challenge, got %v", msg["t"])
	}
	nonceB64, _ := msg["nonce"].(string)
	nonce, err := base64.StdEncoding.DecodeString(nonceB64)
	if err != nil {
		return false, fmt.Errorf("bad nonce: %w", err)
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
		return false, err
	}
	if err := conn.ReadJSON(&msg); err != nil {
		return false, err
	}
	if msg["t"] != "ready" {
		if msg["t"] == "error" {
			return false, fmt.Errorf("rendezvous: %v", msg["msg"])
		}
		return false, fmt.Errorf("expected ready, got %v", msg["t"])
	}
	d.mu.Lock()
	if d.gen == gen {
		d.registered = true
	}
	d.mu.Unlock()
	ready = true
	if len(secret) == 32 {
		log.Printf("rendezvous: registered at %s (aid+rid)", srv)
	} else {
		log.Printf("rendezvous: registered at %s (aid)", srv)
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
				go d.handleOffer(ctx, srv, sid, ticket, mode, writeJSON)
			case "error":
				readErr <- fmt.Errorf("server: %v", m["msg"])
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ready, ctx.Err()
		case err := <-readErr:
			return ready, err
		case <-refreshTimer.C:
			return ready, errEpochRefresh
		case <-ping.C:
			if err := writeJSON(envelope{"t": "ping"}); err != nil {
				return ready, err
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
	srv ServerURL,
	sid, ticket, mode string,
	writeJSON func(envelope) error,
) {
	emit := func(punch net.PacketConn, offerMode string) {
		if d.OnOffer == nil {
			return
		}
		o := Offer{
			SID: sid, Ticket: ticket, Mode: offerMode, Server: srv, Punch: punch,
		}
		d.OnOffer(ctx, o)
	}
	asyncEmit := func(punch net.PacketConn, offerMode string) {
		if d.OnOffer == nil {
			return
		}
		o := Offer{
			SID: sid, Ticket: ticket, Mode: offerMode, Server: srv, Punch: punch,
		}
		go d.OnOffer(ctx, o)
	}
	// Punch-only (Relay→Direct upgrade): client already has a session; it needs
	// the reflexive address before it dials. Reflex first, then ack+punch.
	if mode == "punch" {
		var punch net.PacketConn
		var direct string
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		pc, addr, err := OpenPunchSocket(pctx, srv.String())
		cancel()
		if err != nil {
			log.Printf("rendezvous: punch/reflex failed sid=%s: %v", sid, err)
		} else {
			punch, direct = pc, addr
			log.Printf("rendezvous: direct candidate sid=%s %s mode=punch", sid, direct)
		}
		if err := writeJSON(envelope{"t": "offer-ack", "sid": sid, "direct": direct}); err != nil {
			log.Printf("rendezvous: offer-ack failed: %v", err)
			if punch != nil {
				_ = punch.Close()
			}
			return
		}
		emit(punch, mode)
		return
	}

	// Full connect: ack immediately so the rendezvous can return peer / ticket
	// without waiting on UDP reflex (that used to add up to ~3s on every remote
	// connect). Start the relay dial right away; reflex runs in parallel and a
	// second offer-ack may carry "direct" for the client's optional punch race.
	if err := writeJSON(envelope{"t": "offer-ack", "sid": sid, "direct": ""}); err != nil {
		log.Printf("rendezvous: offer-ack failed: %v", err)
		return
	}
	asyncEmit(nil, mode)

	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	pc, addr, err := OpenPunchSocket(pctx, srv.String())
	cancel()
	if err != nil {
		log.Printf("rendezvous: punch/reflex failed sid=%s: %v", sid, err)
		return
	}
	log.Printf("rendezvous: direct candidate sid=%s %s (late)", sid, addr)
	if err := writeJSON(envelope{"t": "offer-ack", "sid": sid, "direct": addr}); err != nil {
		log.Printf("rendezvous: late offer-ack failed: %v", err)
		_ = pc.Close()
		return
	}
	// Punch listener only — relay already dialed above. Always async so we
	// never block the signal read loop on ServePunchQUIC setup.
	asyncEmit(pc, "punch")
}

// allowOffer is a hand-rolled sliding minute window (B9). Converge with
// internal/ratelimit / rendezvous Banbook once the owner picks third-module
// vs vendored copies — do not create a shared repo without that answer.
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
func DialRelay(ctx context.Context, server ServerURL, ticket string) (*websocket.Conn, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		TLSClientConfig:  server.TLSConfig(),
	}
	conn, resp, err := dialer.DialContext(ctx, server.RelayURL(ticket), nil)
	if err != nil {
		return nil, dialWSError("relay dial", resp, err)
	}
	return conn, nil
}

func dialWSError(op string, resp *http.Response, err error) error {
	if resp == nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()
	log.Printf("rendezvous: %s HTTP %d", op, resp.StatusCode)
	if resp.StatusCode == http.StatusTooManyRequests {
		return &retryAfterError{after: parseRetryAfter(resp.Header.Get("Retry-After")), err: fmt.Errorf("%s: %w", op, err)}
	}
	return fmt.Errorf("%s: HTTP %d: %w", op, resp.StatusCode, err)
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		if until := time.Until(t); until > 0 {
			d = until
		}
	}
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// ValidateServerURL checks a rendezvous URL at the control-API boundary (A15).
// Empty is allowed (means product default).
func ValidateServerURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	_, err := ParseServerURL(raw)
	return err
}

// WrapRelay returns a stream-oriented net.Conn over the relay WebSocket.
func WrapRelay(ws *websocket.Conn) net.Conn {
	return newWSConn(ws)
}
