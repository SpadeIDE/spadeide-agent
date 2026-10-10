package remote

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spadeide/spade-agent/internal/rendezvous"
)

func TestPunchDoesNotTakeDialSlot(t *testing.T) {
	h := &OfferHandler{MaxDials: 1}
	h.initSem()
	h.sem <- struct{}{} // occupy the only dial slot

	punched := make(chan struct{})
	h.ServePunch = func(ctx context.Context, pc net.PacketConn) {
		_ = ctx
		_ = pc.Close()
		close(punched)
	}

	pc := &nopPacketConn{}
	h.Handle(context.Background(), rendezvous.Offer{
		SID:   "punch-1",
		Mode:  "punch",
		Punch: pc,
	})

	select {
	case <-punched:
	case <-time.After(2 * time.Second):
		t.Fatal("ServePunch not called")
	}
	if h.inFlight() != 1 {
		t.Fatalf("punch took or released dial slot: inFlight=%d want 1", h.inFlight())
	}
}

func TestDialSlotReleasedBeforeServeSession(t *testing.T) {
	srv, err := rendezvous.ParseServerURL("wss://rv.example")
	if err != nil {
		t.Fatal(err)
	}

	h := &OfferHandler{MaxDials: 1, DialTimeout: 5 * time.Second}

	dialGate := make(chan struct{})
	var duringDial atomic.Int32
	h.DialRelay = func(ctx context.Context, server rendezvous.ServerURL, ticket string) (net.Conn, error) {
		duringDial.Store(int32(h.inFlight()))
		<-dialGate
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}

	serveSem := make(chan int, 1)
	h.ServeSession = func(conn net.Conn) {
		serveSem <- h.inFlight()
		_ = conn.Close()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Handle(context.Background(), rendezvous.Offer{
			SID:    "relay-1",
			Mode:   "full",
			Ticket: "t",
			Server: srv,
		})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for duringDial.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if duringDial.Load() != 1 {
		t.Fatalf("during dial inFlight=%d want 1", duringDial.Load())
	}
	close(dialGate)

	select {
	case n := <-serveSem:
		if n != 0 {
			t.Fatalf("ServeSession saw inFlight=%d want 0 (slot must release before session)", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeSession not called")
	}
	<-done
}

func TestFullSemDropsOffer(t *testing.T) {
	srv, err := rendezvous.ParseServerURL("wss://rv.example")
	if err != nil {
		t.Fatal(err)
	}
	h := &OfferHandler{MaxDials: 1}
	h.initSem()
	h.sem <- struct{}{}

	var dialed atomic.Bool
	h.DialRelay = func(ctx context.Context, server rendezvous.ServerURL, ticket string) (net.Conn, error) {
		dialed.Store(true)
		return nil, context.Canceled
	}
	h.Handle(context.Background(), rendezvous.Offer{
		SID:    "drop-1",
		Mode:   "full",
		Ticket: "t",
		Server: srv,
	})
	if dialed.Load() {
		t.Fatal("dial ran despite full semaphore")
	}
}

type nopPacketConn struct{}

func (n *nopPacketConn) ReadFrom([]byte) (int, net.Addr, error) { return 0, nil, net.ErrClosed }
func (n *nopPacketConn) WriteTo([]byte, net.Addr) (int, error)  { return 0, net.ErrClosed }
func (n *nopPacketConn) Close() error                           { return nil }
func (n *nopPacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}
func (n *nopPacketConn) SetDeadline(time.Time) error      { return nil }
func (n *nopPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (n *nopPacketConn) SetWriteDeadline(time.Time) error { return nil }
