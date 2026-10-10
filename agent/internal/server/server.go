package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spadeide/spade-agent/internal/auth"
	"github.com/spadeide/spade-agent/internal/config"
	fsaccess "github.com/spadeide/spade-agent/internal/fs"
	gitpkg "github.com/spadeide/spade-agent/internal/git"
	"github.com/spadeide/spade-agent/internal/httpx"
	"github.com/spadeide/spade-agent/internal/identity"
	"github.com/spadeide/spade-agent/internal/protocol"
	ptypkg "github.com/spadeide/spade-agent/internal/pty"
	"github.com/spadeide/spade-agent/internal/ratelimit"
)

var upgrader = websocket.Upgrader{
	// Native SpadeIDE clients do not send Origin. Reject browser Origins so a
	// malicious page on the LAN cannot open a WebSocket to a paired agent.
	CheckOrigin: func(r *http.Request) bool {
		return r.Header.Get("Origin") == ""
	},
}

type Server struct {
	Port     int
	Store    *config.Store
	Auth     *auth.Manager
	Identity *identity.Agent
	Host     string
	LocalIP  string

	pairLimit  *ratelimit.Limiter
	loginLimit *ratelimit.Limiter

	mu      sync.Mutex
	clients map[*client]struct{}
}

// streamWriter is a QUIC (or other) stream we can write frames to.
type streamWriter interface {
	io.Writer
}

type client struct {
	// Exactly one of conn (WebSocket) or the QUIC stream trio is set.
	conn *websocket.Conn

	control   streamWriter
	rpc       streamWriter
	ptyOut    streamWriter
	controlMu sync.Mutex
	rpcMu     sync.Mutex
	ptyMu     sync.Mutex

	// replyStreams maps in-flight request IDs to the stream that should carry
	// the response (QUIC only). Safe for concurrent fs.* handlers.
	replyStreams sync.Map // string → streamWriter

	// sessMu covers unixUser, fs, pty (swapped on host.login) and device field
	// mutations (Unix bind). The device *pointer* is set once at connect and
	// not reassigned; ID/Name reads during Disconnect/ClientInfos are stable.
	sessMu     sync.RWMutex
	device     *config.Device
	unixUser   string
	srv        *Server
	fs         *fsaccess.Service
	git        *gitpkg.Service
	pty        *ptypkg.Manager
	writeMu    sync.Mutex // WebSocket only (one writer)
	forceClose func()

	wsMu      sync.RWMutex
	workspace string // guarded by wsMu (read from the concurrent fs.* path)

	closed    chan struct{}
	closeOnce sync.Once
}

func (c *client) shutdown() {
	c.closeOnce.Do(func() { close(c.closed) })
}

// installIO serializes ping/pong with JSON writes. Gorilla allows one writer;
// iOS URLSession sends pings, and a racy pong during WriteJSON corrupts the socket.
func (c *client) installIO() {
	c.conn.SetPingHandler(func(appData string) error {
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
		err := c.conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
		if err == websocket.ErrCloseSent {
			return nil
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil
		}
		return err
	})
}

func (c *client) pingLoop() {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.closed:
			return
		case <-t.C:
			c.writeMu.Lock()
			err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			c.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (c *client) setWorkspace(p string) {
	c.wsMu.Lock()
	c.workspace = p
	c.wsMu.Unlock()
}

func (c *client) getWorkspace() string {
	c.wsMu.RLock()
	defer c.wsMu.RUnlock()
	return c.workspace
}

func New(port int, store *config.Store, authMgr *auth.Manager, id *identity.Agent) *Server {
	host, _ := os.Hostname()
	return &Server{
		Port:     port,
		Store:    store,
		Auth:     authMgr,
		Identity: id,
		Host:     host,
		LocalIP:  primaryIPv4(),
		clients:  make(map[*client]struct{}),
		pairLimit: ratelimit.New(ratelimit.Config{
			Rate:       1,
			Burst:      5,
			FailLimit:  5,
			LockoutMin: 2 * time.Second,
			LockoutMax: 10 * time.Minute,
		}),
		loginLimit: ratelimit.New(ratelimit.Config{
			Rate:       1,
			Burst:      5,
			FailLimit:  5,
			LockoutMin: 2 * time.Second,
			LockoutMax: 10 * time.Minute,
		}),
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/pair/info", s.handlePairInfo)
	mux.HandleFunc("/pair", httpx.Method(http.MethodPost, s.handlePair))
	mux.HandleFunc("/v1", s.handleWS)

	tlsCfg := s.Auth.TLSConfig()
	ln, err := tls.Listen("tcp", fmt.Sprintf(":%d", s.Port), tlsCfg)
	if err != nil {
		return err
	}

	httpServer := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = httpServer.Close()
	}()

	// QUIC on the same port (UDP) — clients prefer it; WSS remains the fallback.
	go func() {
		if err := s.listenQUIC(ctx); err != nil && ctx.Err() == nil {
			log.Printf("quic listener stopped: %v", err)
		}
	}()

	log.Printf("listening on https://%s:%d (wss /v1) and quic://%s:%d (alpn %s)",
		s.displayHost(), s.Port, s.displayHost(), s.Port, QUICALPN)
	err = httpServer.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// DisplayHost returns the current LAN IPv4 for QR / status (refreshed each call).
func (s *Server) DisplayHost() string {
	return s.displayHost()
}

func (s *Server) displayHost() string {
	// Re-resolve every time — DHCP / Wi‑Fi changes must show up in the QR.
	if ip := primaryIPv4(); ip != "" {
		s.LocalIP = ip
		return ip
	}
	if s.LocalIP != "" {
		return s.LocalIP
	}
	return "127.0.0.1"
}

func (s *Server) PairURL() string {
	secret, _ := s.Auth.PairSecret()
	q := url.Values{}
	q.Set("host", s.displayHost())
	q.Set("port", fmt.Sprintf("%d", s.Port))
	q.Set("name", s.Host)
	q.Set("fp", s.Auth.Fingerprint())
	q.Set("secret", secret)
	if s.Identity != nil {
		q.Set("aid", s.Identity.ID)
	}
	return "spadeide://pair?" + q.Encode()
}

// DisconnectDevice force-closes any live session for the given device id.
func (s *Server) DisconnectDevice(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		if c.device != nil && c.device.ID == id {
			c.shutdown()
			if c.forceClose != nil {
				c.forceClose()
			}
		}
	}
}

// takeoverOtherSessions closes every live session for deviceID except keep —
// used when Direct upgrades past an existing Relay (or vice versa).
func (s *Server) takeoverOtherSessions(deviceID string, keep *client) {
	s.mu.Lock()
	var victims []*client
	for c := range s.clients {
		if c == keep || c.device == nil || c.device.ID != deviceID {
			continue
		}
		victims = append(victims, c)
	}
	s.mu.Unlock()
	for _, c := range victims {
		log.Printf("session takeover: closing prior session for %s", deviceID)
		c.shutdown()
		if c.forceClose != nil {
			c.forceClose()
		}
	}
}

// DisconnectAll force-closes every live client session.
func (s *Server) DisconnectAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		c.shutdown()
		if c.forceClose != nil {
			c.forceClose()
		}
	}
}

// ClientInfo is a connected iPad/client summary for the control UI.
type ClientInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) ClientInfos() []ClientInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ClientInfo, 0, len(s.clients))
	for c := range s.clients {
		if c.device == nil {
			continue
		}
		out = append(out, ClientInfo{ID: c.device.ID, Name: c.device.Name})
	}
	return out
}

func (s *Server) handlePairInfo(w http.ResponseWriter, r *http.Request) {
	st, _ := s.Store.LoadState()
	info := protocol.PairInfo{
		HostName:    s.Host,
		Port:        s.Port,
		Fingerprint: s.Auth.Fingerprint(),
		Version:     config.Version,
		Paired:      st != nil && len(st.Devices) > 0,
	}
	if s.Identity != nil {
		info.AgentID = s.Identity.ID
	}
	httpx.WriteJSON(w, info)
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	key := httpx.ClientIP(r)
	if !s.pairLimit.Allow(key) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	var req protocol.PairRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, protocol.CodeInvalidParams, "invalid json")
		return
	}
	if req.DeviceID == "" || req.DeviceName == "" {
		httpx.WriteError(w, http.StatusBadRequest, protocol.CodeInvalidParams, "deviceId and deviceName required")
		return
	}
	if req.DevicePub == "" {
		httpx.WriteError(w, http.StatusBadRequest, protocol.CodeInvalidParams, "devicePub required")
		return
	}
	if _, err := s.Auth.ValidateAndPair(req.Secret, req.DeviceID, req.DeviceName, req.DevicePub); err != nil {
		s.pairLimit.Failure(key)
		httpx.WriteError(w, http.StatusUnauthorized, protocol.CodeBadSecret, err.Error())
		return
	}
	s.pairLimit.Success(key)
	resp := protocol.PairResponse{
		HostName:    s.Host,
		Fingerprint: s.Auth.Fingerprint(),
	}
	if s.Identity != nil {
		resp.AgentID = s.Identity.ID
		resp.AgentPub = s.Identity.PublicBase64()
	}
	if s.Store != nil {
		if _, err := s.Store.EnsureRvSecret(); err == nil {
			if b64, err := s.Store.RvSecretBase64(); err == nil {
				resp.RvSecret = b64
			}
		}
	}
	httpx.WriteJSON(w, resp)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	var peerCerts []*x509.Certificate
	if r.TLS != nil {
		peerCerts = r.TLS.PeerCertificates
	}
	dev, err := s.Auth.AuthenticatePeer(peerCerts)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	// Bound a single frame so a malicious/broken client can't force unbounded
	// allocation. base64 of a MaxFileBytes write (~32 MiB → ~43 MiB) plus JSON
	// overhead fits comfortably under this.
	conn.SetReadLimit(64 << 20)

	s.runSession(dev, sessionWires{
		ws:         conn,
		forceClose: func() { _ = conn.Close() },
		label:      "ws",
	}, func(c *client) error {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return err
			}
			var env protocol.Envelope
			if err := json.Unmarshal(data, &env); err != nil {
				c.send(protocol.NewError("", protocol.CodeInvalidParams, "invalid json"))
				continue
			}
			c.dispatchInbound(env, nil)
		}
	})
}

func (c *client) send(env protocol.Envelope) {
	if c.conn != nil {
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
		_ = c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		err := c.conn.WriteJSON(env)
		_ = c.conn.SetWriteDeadline(time.Time{})
		if err != nil {
			log.Printf("ws write error: %v", err)
		}
		return
	}

	w, mu := c.pickQUICWriter(env)
	if w == nil {
		log.Printf("quic write: no stream for event=%q id=%q", env.Event, env.ID)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if err := writeFrame(w, env); err != nil {
		log.Printf("quic write error: %v", err)
	}
}

func (c *client) pickQUICWriter(env protocol.Envelope) (streamWriter, *sync.Mutex) {
	switch env.Event {
	case protocol.EventPTYData, protocol.EventPTYExit:
		return c.ptyOut, &c.ptyMu
	case "":
		if env.ID != "" {
			if v, ok := c.replyStreams.Load(env.ID); ok {
				if w, ok := v.(streamWriter); ok {
					return w, c.mutexFor(w)
				}
			}
		}
		if c.control != nil {
			return c.control, &c.controlMu
		}
		return c.rpc, &c.rpcMu
	default:
		if c.control != nil {
			return c.control, &c.controlMu
		}
		return c.rpc, &c.rpcMu
	}
}

func (c *client) mutexFor(w streamWriter) *sync.Mutex {
	switch w {
	case c.ptyOut:
		return &c.ptyMu
	case c.rpc:
		return &c.rpcMu
	default:
		return &c.controlMu
	}
}

// dispatchInbound runs handle for one envelope, binding responses to reply
// when non-nil (QUIC stream affinity).
//
// Always asynchronous: the WebSocket / QUIC read loop must keep draining.
// git.status used to run inline, so a scan of a large home directory (or a
// hanging stat) stalled every fs.read / fs.write on the same stream. The iPad
// stayed "Connected" because ping lives on the control stream.
func (c *client) dispatchInbound(env protocol.Envelope, reply streamWriter) {
	go func() {
		if reply != nil && env.ID != "" {
			c.replyStreams.Store(env.ID, reply)
			defer c.replyStreams.Delete(env.ID)
		}
		defer func() {
			if r := recover(); r != nil {
				log.Printf("rpc panic method=%s id=%s: %v\n%s", env.Method, env.ID, r, stackTrace())
				if env.ID != "" {
					c.send(protocol.NewError(env.ID, protocol.CodeInternal, "internal error"))
				}
			}
		}()
		c.handle(env)
	}()
}

func stackTrace() string {
	buf := make([]byte, 4096)
	n := runtime.Stack(buf, false)
	return string(buf[:n])
}

// primaryIPv4 picks the LAN address the iPad should dial. It prefers physical
// Ethernet/Wi-Fi interfaces (en*) with a private (RFC1918) address, so VPN
// tunnels (utun*), Docker bridges, and public addresses don't win and leave the
// pair URL unreachable from the iPad on the same Wi-Fi.
func primaryIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}
	var privateFallback, anyFallback string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		preferredIface := strings.HasPrefix(iface.Name, "en")
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			v4 := ip.To4()
			if v4 == nil {
				continue
			}
			s := v4.String()
			if preferredIface && v4.IsPrivate() {
				return s // best: Wi-Fi/Ethernet with a LAN address
			}
			if v4.IsPrivate() && privateFallback == "" {
				privateFallback = s
			}
			if anyFallback == "" {
				anyFallback = s
			}
		}
	}
	if privateFallback != "" {
		return privateFallback
	}
	if anyFallback != "" {
		return anyFallback
	}
	return "127.0.0.1"
}

func mustHostname() string {
	h, _ := os.Hostname()
	return h
}
