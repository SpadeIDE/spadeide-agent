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
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spadeide/spade-agent/internal/ai"
	"github.com/spadeide/spade-agent/internal/auth"
	"github.com/spadeide/spade-agent/internal/config"
	fsaccess "github.com/spadeide/spade-agent/internal/fs"
	gitpkg "github.com/spadeide/spade-agent/internal/git"
	"github.com/spadeide/spade-agent/internal/identity"
	"github.com/spadeide/spade-agent/internal/platform"
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

	pairLimit *ratelimit.Limiter

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

	control streamWriter
	rpc     streamWriter
	ptyOut  streamWriter
	controlMu sync.Mutex
	rpcMu     sync.Mutex
	ptyMu     sync.Mutex

	// replyStreams maps in-flight request IDs to the stream that should carry
	// the response (QUIC only). Safe for concurrent fs.* handlers.
	replyStreams sync.Map // string → streamWriter

	device     *config.Device
	fs         *fsaccess.Service
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
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/pair/info", s.handlePairInfo)
	mux.HandleFunc("/pair", s.handlePair)
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
	writeJSON(w, info)
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := pairClientKey(r)
	if !s.pairLimit.Allow(key) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	var req protocol.PairRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, protocol.CodeInvalidParams, "invalid json")
		return
	}
	if req.DeviceID == "" || req.DeviceName == "" {
		writeErr(w, http.StatusBadRequest, protocol.CodeInvalidParams, "deviceId and deviceName required")
		return
	}
	if req.DevicePub == "" {
		writeErr(w, http.StatusBadRequest, protocol.CodeInvalidParams, "devicePub required")
		return
	}
	if _, err := s.Auth.ValidateAndPair(req.Secret, req.DeviceID, req.DeviceName, req.DevicePub); err != nil {
		s.pairLimit.Failure(key)
		writeErr(w, http.StatusUnauthorized, protocol.CodeBadSecret, err.Error())
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
	writeJSON(w, resp)
}

func pairClientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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

	home, _ := os.UserHomeDir()
	c := &client{conn: conn, device: dev, closed: make(chan struct{})}
	c.forceClose = func() { _ = conn.Close() }
	c.installIO()
	c.fs = fsaccess.New(home, func(env protocol.Envelope) { c.send(env) })
	c.pty = ptypkg.NewManager(platform.DefaultShell(), func(env protocol.Envelope) { c.send(env) })

	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()

	defer func() {
		c.shutdown()
		c.pty.CloseAll()
		c.fs.Close()
		_ = conn.Close()
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
	}()

	log.Printf("client connected: %s (%s)", dev.Name, dev.ID)
	go c.pingLoop()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("client disconnected: %s (%s): %v", dev.Name, dev.ID, err)
			return
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			c.send(protocol.NewError("", protocol.CodeInvalidParams, "invalid json"))
			continue
		}
		c.dispatchInbound(env, nil)
	}
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
// when non-nil (QUIC stream affinity). Filesystem calls run concurrently.
func (c *client) dispatchInbound(env protocol.Envelope, reply streamWriter) {
	run := func() {
		if reply != nil && env.ID != "" {
			c.replyStreams.Store(env.ID, reply)
			defer c.replyStreams.Delete(env.ID)
		}
		c.handle(env)
	}
	if strings.HasPrefix(env.Method, "fs.") {
		go run()
	} else {
		run()
	}
}

// decodeParams unmarshals env.Params into T, answering invalid_params and
// reporting false when the payload doesn't fit.
func decodeParams[T any](c *client, env protocol.Envelope) (T, bool) {
	var p T
	if err := json.Unmarshal(env.Params, &p); err != nil {
		c.send(protocol.NewError(env.ID, protocol.CodeInvalidParams, err.Error()))
		return p, false
	}
	return p, true
}

// errorCode maps a filesystem error to its protocol code. Uses errors.Is on the
// fsaccess sentinels, so the mapping never depends on message text.
func errorCode(err error) string {
	switch {
	case errors.Is(err, fsaccess.ErrOutsideRoots),
		errors.Is(err, fsaccess.ErrSymlinkEscape),
		errors.Is(err, fsaccess.ErrBrowseOnly):
		return protocol.CodeForbidden
	case errors.Is(err, fsaccess.ErrInvalidPattern):
		return protocol.CodeInvalidParams
	case errors.Is(err, os.ErrNotExist):
		return protocol.CodeNotFound
	default:
		return protocol.CodeIOError
	}
}

// reply answers with result, or with the mapped error when err is non-nil.
func (c *client) reply(id string, result any, err error) {
	if err != nil {
		c.send(protocol.NewError(id, errorCode(err), err.Error()))
		return
	}
	c.send(protocol.NewResponse(id, result))
}

// ack answers with an empty result, or with err under the given code.
func (c *client) ack(id string, err error, code string) {
	if err != nil {
		c.send(protocol.NewError(id, code, err.Error()))
		return
	}
	c.send(protocol.NewResponse(id, map[string]any{}))
}

func (c *client) handle(env protocol.Envelope) {
	if env.Method == "" {
		c.send(protocol.NewError(env.ID, protocol.CodeInvalidParams, "missing method"))
		return
	}
	switch env.Method {
	case protocol.MethodHello:
		c.send(protocol.NewResponse(env.ID, protocol.HelloResult{
			Agent:   config.AppName,
			Version: config.Version,
			OS:      runtime.GOOS,
			Arch:    runtime.GOARCH,
			Home:    mustHome(),
			Shell:   platform.DefaultShell(),
			Host:    mustHostname(),
		}))
	case protocol.MethodPing:
		c.send(protocol.NewResponse(env.ID, map[string]bool{"pong": true}))
	case protocol.MethodWorkspaceRoots:
		c.send(protocol.NewResponse(env.ID, protocol.WorkspaceRootsResult{Roots: c.fs.Roots()}))
	case protocol.MethodWorkspaceOpen:
		p, ok := decodeParams[protocol.PathParams](c, env)
		if !ok {
			return
		}
		if err := c.fs.AddRoot(p.Path); err != nil {
			c.reply(env.ID, nil, err)
			return
		}
		// Report the path as the agent resolved it, not as the client spelled it.
		opened := p.Path
		if st, err := c.fs.Stat(p.Path); err == nil {
			opened = st.Path
		}
		c.setWorkspace(opened)
		c.send(protocol.NewResponse(env.ID, protocol.WorkspaceOpenResult{
			Path: opened,
			Name: filepath.Base(opened),
		}))
	case protocol.MethodFSList:
		c.handlePath(env, func(path string) (any, error) { return c.fs.List(path) })
	case protocol.MethodFSStat:
		c.handlePath(env, func(path string) (any, error) { return c.fs.Stat(path) })
	case protocol.MethodFSRead:
		c.handlePath(env, func(path string) (any, error) { return c.fs.Read(path) })
	case protocol.MethodFSMkdir:
		c.handlePath(env, func(path string) (any, error) {
			return map[string]string{"path": path}, c.fs.Mkdir(path)
		})
	case protocol.MethodFSDelete:
		c.handlePath(env, func(path string) (any, error) {
			return map[string]string{"path": path}, c.fs.Delete(path)
		})
	case protocol.MethodFSWatch:
		c.handlePath(env, func(path string) (any, error) {
			id, err := c.fs.Watch(path)
			return protocol.WatchResult{WatchID: id}, err
		})
	case protocol.MethodFSWrite:
		p, ok := decodeParams[protocol.WriteParams](c, env)
		if !ok {
			return
		}
		res, err := c.fs.Write(p.Path, p.Content)
		c.reply(env.ID, res, err)
	case protocol.MethodFSRename:
		p, ok := decodeParams[protocol.RenameParams](c, env)
		if !ok {
			return
		}
		path, err := c.fs.Rename(p.From, p.To)
		c.reply(env.ID, map[string]string{"path": path}, err)
	case protocol.MethodFSCopy, protocol.MethodFSMove:
		p, ok := decodeParams[protocol.CopyParams](c, env)
		if !ok {
			return
		}
		var path string
		var err error
		if env.Method == protocol.MethodFSMove {
			path, err = c.fs.Move(p.From, p.ToDir)
		} else {
			path, err = c.fs.Copy(p.From, p.ToDir)
		}
		c.reply(env.ID, map[string]string{"path": path}, err)
	case protocol.MethodFSSearch:
		p, ok := decodeParams[protocol.SearchParams](c, env)
		if !ok {
			return
		}
		root := p.Root
		if root == "" {
			root = c.getWorkspace()
		}
		res, err := c.fs.Search(root, p.Limit)
		c.reply(env.ID, res, err)
	case protocol.MethodFSGrep:
		p, ok := decodeParams[protocol.GrepParams](c, env)
		if !ok {
			return
		}
		if p.Root == "" {
			p.Root = c.getWorkspace()
		}
		start := time.Now()
		res, err := c.fs.Grep(p)
		if err != nil {
			log.Printf("fs.grep failed q=%q root=%s dur=%s: %v", p.Query, p.Root, time.Since(start).Round(time.Millisecond), err)
			c.reply(env.ID, nil, err)
			return
		}
		log.Printf("fs.grep q=%q root=%s matches=%d truncated=%v dur=%s",
			p.Query, p.Root, len(res.Matches), res.Truncated, time.Since(start).Round(time.Millisecond))
		c.send(protocol.NewResponse(env.ID, res))
	case protocol.MethodFSReplaceInFiles:
		p, ok := decodeParams[protocol.ReplaceInFilesParams](c, env)
		if !ok {
			return
		}
		if p.Root == "" {
			p.Root = c.getWorkspace()
		}
		res, err := c.fs.ReplaceInFiles(p)
		c.reply(env.ID, res, err)
	case protocol.MethodFSUnwatch:
		p, ok := decodeParams[protocol.UnwatchParams](c, env)
		if !ok {
			return
		}
		c.ack(env.ID, c.fs.Unwatch(p.WatchID), protocol.CodeNotFound)
	case protocol.MethodGitStatus:
		root, ok := c.gitRoot(env, "")
		if !ok {
			return
		}
		res, err := gitpkg.New().Status(root)
		c.reply(env.ID, res, err)
	case protocol.MethodGitStage, protocol.MethodGitUnstage, protocol.MethodGitDiscard:
		p, ok := decodeParams[protocol.GitPathsParams](c, env)
		if !ok {
			return
		}
		root, ok := c.gitRoot(env, p.Root)
		if !ok {
			return
		}
		paths, ok := c.allowWriteAll(env, p.Paths)
		if !ok {
			return
		}
		git := gitpkg.New()
		var err error
		switch env.Method {
		case protocol.MethodGitStage:
			err = git.Stage(root, paths)
		case protocol.MethodGitUnstage:
			err = git.Unstage(root, paths)
		default:
			err = git.Discard(root, paths)
		}
		c.ack(env.ID, err, protocol.CodeIOError)
	case protocol.MethodGitCommit:
		p, ok := decodeParams[protocol.GitCommitParams](c, env)
		if !ok {
			return
		}
		root, ok := c.gitRoot(env, p.Root)
		if !ok {
			return
		}
		res, err := gitpkg.New().Commit(root, p.Message, p.StageAll, p.Amend)
		c.reply(env.ID, res, err)
	case protocol.MethodGitPush, protocol.MethodGitPull:
		p, ok := decodeParams[protocol.GitPathsParams](c, env)
		if !ok {
			return
		}
		root, ok := c.gitRoot(env, p.Root)
		if !ok {
			return
		}
		git := gitpkg.New()
		var res *protocol.GitPushResult
		var err error
		if env.Method == protocol.MethodGitPush {
			res, err = git.Push(root)
		} else {
			res, err = git.Pull(root)
		}
		c.reply(env.ID, res, err)
	case protocol.MethodGitDiff:
		p, ok := decodeParams[protocol.GitDiffParams](c, env)
		if !ok {
			return
		}
		root, ok := c.gitRoot(env, p.Root)
		if !ok {
			return
		}
		path, ok := c.allowWriteAll(env, []string{p.Path})
		if !ok {
			return
		}
		res, err := gitpkg.New().Diff(root, path[0], p.Staged, p.AgainstHead)
		c.reply(env.ID, res, err)
	case protocol.MethodGitApplyHunk:
		p, ok := decodeParams[protocol.GitHunkParams](c, env)
		if !ok {
			return
		}
		root, ok := c.gitRoot(env, p.Root)
		if !ok {
			return
		}
		path, ok := c.allowWriteAll(env, []string{p.Path})
		if !ok {
			return
		}
		c.ack(env.ID, gitpkg.New().ApplyHunk(root, path[0], p.HunkIndex, p.Mode), protocol.CodeIOError)
	case protocol.MethodGitBranches:
		root, ok := c.gitRoot(env, "")
		if !ok {
			return
		}
		res, err := gitpkg.New().Branches(root)
		c.reply(env.ID, res, err)
	case protocol.MethodGitCheckout:
		p, ok := decodeParams[protocol.GitCheckoutParams](c, env)
		if !ok {
			return
		}
		root, ok := c.gitRoot(env, p.Root)
		if !ok {
			return
		}
		c.ack(env.ID, gitpkg.New().Checkout(root, p.Branch, p.Create), protocol.CodeIOError)
	case protocol.MethodPTYCreate:
		p, ok := decodeParams[protocol.PTYCreateParams](c, env)
		if !ok {
			return
		}
		cwd := p.Cwd
		if cwd == "" {
			cwd = c.getWorkspace()
		}
		if cwd == "" {
			cwd = mustHome()
		}
		id, err := c.pty.Create(p.Cols, p.Rows, cwd)
		if err != nil {
			c.send(protocol.NewError(env.ID, protocol.CodeInternal, err.Error()))
			return
		}
		c.send(protocol.NewResponse(env.ID, protocol.PTYCreateResult{PTYID: id}))
	case protocol.MethodPTYInput:
		p, ok := decodeParams[protocol.PTYInputParams](c, env)
		if !ok {
			return
		}
		c.ack(env.ID, c.pty.Input(p.PTYID, p.Data), protocol.CodeIOError)
	case protocol.MethodPTYResize:
		p, ok := decodeParams[protocol.PTYResizeParams](c, env)
		if !ok {
			return
		}
		c.ack(env.ID, c.pty.Resize(p.PTYID, p.Cols, p.Rows), protocol.CodeIOError)
	case protocol.MethodPTYClose:
		p, ok := decodeParams[protocol.PTYIDParams](c, env)
		if !ok {
			return
		}
		_ = c.pty.Close(p.PTYID)
		c.ack(env.ID, nil, "")
	case protocol.MethodAIList:
		found := ai.Detect()
		agents := make([]protocol.AIAgent, 0, len(found))
		for _, a := range found {
			agents = append(agents, protocol.AIAgent{ID: a.ID, Name: a.Name, Path: a.Path})
		}
		c.send(protocol.NewResponse(env.ID, protocol.AIListResult{Agents: agents}))
	default:
		c.send(protocol.NewError(env.ID, protocol.CodeNotFound, "unknown method: "+env.Method))
	}
}

// gitRoot picks the directory git runs in. Only the opened project qualifies:
// git writes, so an arbitrary client-supplied path would step around the same
// sandbox that guards fs.write.
func (c *client) gitRoot(env protocol.Envelope, requested string) (string, bool) {
	root := requested
	if root == "" {
		root = c.getWorkspace()
	}
	if root == "" {
		c.send(protocol.NewError(env.ID, protocol.CodeForbidden, "no project is open"))
		return "", false
	}
	resolved, err := c.fs.AllowWrite(root)
	if err != nil {
		c.send(protocol.NewError(env.ID, errorCode(err), err.Error()))
		return "", false
	}
	return resolved, true
}

// allowWriteAll runs every path past the sandbox before git is let near them.
func (c *client) allowWriteAll(env protocol.Envelope, paths []string) ([]string, bool) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		resolved, err := c.fs.AllowWrite(p)
		if err != nil {
			c.send(protocol.NewError(env.ID, errorCode(err), err.Error()))
			return nil, false
		}
		out = append(out, resolved)
	}
	return out, true
}

// handlePath runs fn for the `{ "path": … }` methods and answers with its result.
func (c *client) handlePath(env protocol.Envelope, fn func(string) (any, error)) {
	p, ok := decodeParams[protocol.PathParams](c, env)
	if !ok {
		return
	}
	res, err := fn(p.Path)
	c.reply(env.ID, res, err)
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return r.URL.Query().Get("token")
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
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

func mustHome() string {
	h, _ := os.UserHomeDir()
	return h
}

func mustHostname() string {
	h, _ := os.Hostname()
	return h
}
