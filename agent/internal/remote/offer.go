package remote

import (
	"context"
	"log"
	"net"
	"sync"
	"time"

	"github.com/spadeide/spade-agent/internal/rendezvous"
	"github.com/spadeide/spade-agent/internal/server"
)

// OfferHandler dials relay sessions and optional punch QUIC listeners for rendezvous offers.
type OfferHandler struct {
	Agent       *server.Server
	MaxDials    int
	DialTimeout time.Duration
	PunchWindow time.Duration

	// Optional test hooks; nil uses production paths.
	DialRelay    func(ctx context.Context, server rendezvous.ServerURL, ticket string) (net.Conn, error)
	ServeSession func(conn net.Conn)
	ServePunch   func(ctx context.Context, pc net.PacketConn)

	once sync.Once
	sem  chan struct{}
}

func (h *OfferHandler) initSem() {
	h.once.Do(func() {
		n := h.MaxDials
		if n <= 0 {
			n = 4
		}
		h.sem = make(chan struct{}, n)
	})
}

// inFlight reports how many dial slots are currently held (tests / diagnostics).
func (h *OfferHandler) inFlight() int {
	h.initSem()
	return len(h.sem)
}

// Handle processes a rendezvous offer (relay dial and/or punch listener).
func (h *OfferHandler) Handle(ctx context.Context, o rendezvous.Offer) {
	_ = ctx
	punchWindow := h.PunchWindow
	if punchWindow <= 0 {
		punchWindow = 15 * time.Second
	}
	dialTimeout := h.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 60 * time.Second
	}

	// Punch must never take offerSem — a late punch callback races the
	// in-flight relay dial and used to drop it ("too many in flight"),
	// leaving the iPad alone on the relay until nested TLS timed out.
	if o.Punch != nil {
		pctx, pcancel := context.WithTimeout(context.Background(), punchWindow)
		go func(pc net.PacketConn) {
			defer pcancel()
			if h.ServePunch != nil {
				h.ServePunch(pctx, pc)
				return
			}
			if h.Agent != nil {
				h.Agent.ServePunchQUIC(pctx, pc)
			}
		}(o.Punch)
	}
	if o.Mode == "punch" {
		log.Printf("rendezvous: punch-only offer sid=%s (no relay dial)", o.SID)
		return
	}

	h.initSem()
	select {
	case h.sem <- struct{}{}:
	default:
		log.Printf("rendezvous: dropping offer sid=%s (too many in flight)", o.SID)
		return
	}
	released := false
	release := func() {
		if !released {
			released = true
			<-h.sem
		}
	}
	defer release()

	offerCtx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	log.Printf("rendezvous: offer sid=%s — dialing relay", o.SID)

	conn, err := h.dial(offerCtx, o.Server, o.Ticket)
	if err != nil {
		log.Printf("rendezvous: relay dial failed: %v", err)
		return
	}
	// Only bound concurrent dials — the session itself can outlive the slot.
	release()
	h.serve(conn)
}

func (h *OfferHandler) dial(ctx context.Context, server rendezvous.ServerURL, ticket string) (net.Conn, error) {
	if h.DialRelay != nil {
		return h.DialRelay(ctx, server, ticket)
	}
	ws, err := rendezvous.DialRelay(ctx, server, ticket)
	if err != nil {
		return nil, err
	}
	return rendezvous.WrapRelay(ws), nil
}

func (h *OfferHandler) serve(conn net.Conn) {
	if h.ServeSession != nil {
		h.ServeSession(conn)
		return
	}
	if h.Agent != nil {
		h.Agent.ServeRelaySession(conn)
	}
}
