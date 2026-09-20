package rendezvous

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	reflexPing = "SPADE-REFLEX"
	reflexPong = "SPADE-REFLEX-OK"
)

// OpenPunchSocket binds an ephemeral UDP socket and learns its reflexive
// address via the rendezvous echo. Caller owns the socket (QUIC listen + close).
func OpenPunchSocket(ctx context.Context, rvBase string) (pc net.PacketConn, direct string, err error) {
	host, port, err := UDPEndpointFromWSS(rvBase)
	if err != nil {
		return nil, "", err
	}
	pc, err = net.ListenPacket("udp", ":0")
	if err != nil {
		return nil, "", err
	}
	direct, err = QueryReflex(ctx, pc, host, port)
	if err != nil {
		_ = pc.Close()
		return nil, "", err
	}
	return pc, direct, nil
}

// QueryReflex learns the public mapping for packetConn by asking the rendezvous
// UDP echo.
func QueryReflex(ctx context.Context, packetConn net.PacketConn, rvHost string, rvPort int) (string, error) {
	ips, err := net.LookupIP(rvHost)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("lookup %s: %w", rvHost, err)
	}
	var ip net.IP
	for _, c := range ips {
		if v4 := c.To4(); v4 != nil {
			ip = v4
			break
		}
	}
	if ip == nil {
		ip = ips[0]
	}
	rv := &net.UDPAddr{IP: ip, Port: rvPort}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(3 * time.Second)
	}
	_ = packetConn.SetDeadline(deadline)
	defer func() { _ = packetConn.SetDeadline(time.Time{}) }()

	if _, err := packetConn.WriteTo([]byte(reflexPing+"\n"), rv); err != nil {
		return "", err
	}

	buf := make([]byte, 256)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, from, err := packetConn.ReadFrom(buf)
		if err != nil {
			return "", err
		}
		if ua, ok := from.(*net.UDPAddr); ok {
			if !ua.IP.Equal(rv.IP) || ua.Port != rv.Port {
				continue
			}
		}
		msg := strings.TrimSpace(string(buf[:n]))
		if !strings.HasPrefix(msg, reflexPong) {
			continue
		}
		parts := strings.Fields(msg)
		if len(parts) < 3 {
			continue
		}
		if net.ParseIP(parts[1]) == nil {
			continue
		}
		return net.JoinHostPort(parts[1], parts[2]), nil
	}
}

// UDPEndpointFromWSS maps wss://host[:port] → host + UDP port (default 443).
func UDPEndpointFromWSS(base string) (host string, port int, err error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", 0, err
	}
	host = u.Hostname()
	if host == "" {
		return "", 0, fmt.Errorf("no host in %q", base)
	}
	port = 443
	if p := u.Port(); p != "" {
		if _, scanErr := fmt.Sscanf(p, "%d", &port); scanErr != nil || port <= 0 {
			return "", 0, fmt.Errorf("bad port in %q", base)
		}
	}
	return host, port, nil
}
