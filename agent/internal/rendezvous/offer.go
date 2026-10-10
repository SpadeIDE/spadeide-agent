package rendezvous

import "net"

// Offer is delivered when a client looks up this agent on the rendezvous.
type Offer struct {
	SID, Ticket, Mode string
	Server            ServerURL
	Punch             net.PacketConn
}
