package control

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spadeide/spade-agent/internal/auth"
	"github.com/spadeide/spade-agent/internal/config"
	"github.com/spadeide/spade-agent/internal/ratelimit"
	"github.com/spadeide/spade-agent/internal/server"
)

// Status is the JSON payload for the menu bar / local UI.
type Status struct {
	Running           bool                `json:"running"`
	Host              string              `json:"host"`
	IP                string              `json:"ip"`
	Port              int                 `json:"port"`
	ControlPort       int                 `json:"controlPort"`
	Fingerprint       string              `json:"fingerprint"`
	AgentID           string              `json:"agentId,omitempty"`
	PairURL           string              `json:"pairURL"`
	PairSecret        string              `json:"pairSecret"`
	PairSecretExpires int64               `json:"pairSecretExpires"` // unix seconds
	Version           string              `json:"version"`
	ConfigDir         string              `json:"configDir"`
	RemoteAccess      bool                `json:"remoteAccess"`
	RemoteServer      string              `json:"remoteServer,omitempty"`
	RemoteRegistered  bool                `json:"remoteRegistered"`
	Devices           []DeviceInfo        `json:"devices"`
	Clients           []server.ClientInfo `json:"clients"`
}

type DeviceInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
	Online    bool   `json:"online"`
}

type Server struct {
	Port    int
	Store   *config.Store
	Auth    *auth.Manager
	Agent   *server.Server
	Remote  RemoteController // optional; notified when remote toggle changes
	token   string
	limiter *ratelimit.Limiter
	httpSrv *http.Server
}

// RemoteController applies the remote-access dialer when the tray/CLI toggles it.
type RemoteController interface {
	Apply(enabled bool, server string)
	State() (enabled bool, server string, registered bool)
}

// ErrNoControlToken means the control API refused to start without a bearer.
var ErrNoControlToken = errors.New("control API token required")

func New(port int, store *config.Store, authMgr *auth.Manager, agent *server.Server) *Server {
	if port <= 0 {
		port = config.DefaultControlPort
	}
	return &Server{
		Port:    port,
		Store:   store,
		Auth:    authMgr,
		Agent:   agent,
		limiter: ratelimit.New(ratelimit.Config{
			Rate:       5,
			Burst:      10,
			FailLimit:  8,
			LockoutMin: 2 * time.Second,
			LockoutMax: 5 * time.Minute,
		}),
	}
}

// EnsureToken loads SPADE_CONTROL_TOKEN or mints one, writes it 0600 next to
// agent.pid, and stores it for guard(). Empty token is never allowed.
func (s *Server) EnsureToken() (string, error) {
	token := strings.TrimSpace(os.Getenv("SPADE_CONTROL_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(s.Store.ReadControlToken())
	}
	if token == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		token = hex.EncodeToString(b)
	}
	if err := s.Store.WriteControlToken(token); err != nil {
		return "", err
	}
	s.token = token
	return token, nil
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	if s.token == "" {
		if _, err := s.EnsureToken(); err != nil {
			return err
		}
	}
	if s.token == "" {
		return ErrNoControlToken
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", s.guard(s.handleStatus))
	mux.HandleFunc("/v1/pair/rotate", s.guard(s.handleRotate))
	mux.HandleFunc("/v1/pair/reset", s.guard(s.handleReset))
	mux.HandleFunc("/v1/pair/devices/", s.guard(s.handleDevice))
	mux.HandleFunc("/v1/remote", s.guard(s.handleRemote))

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.Port))
	if err != nil {
		return err
	}

	s.httpSrv = &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(c)
	}()

	err = s.httpSrv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := clientKey(r)
		if !s.limiter.Allow(key) {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		if s.token == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h := r.Header.Get("Authorization")
		got := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			// Missing bearer is common while the CLI races the just-minted
			// control.token file — don't escalate that into lockout.
			if got != "" {
				s.limiter.Failure(key)
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.limiter.Success(key)
		next(w, r)
	}
}

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.snapshot())
}

func (s *Server) handleRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.Auth.RotatePairSecret()
	writeJSON(w, s.snapshot())
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.Store.ResetPairing(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Agent.DisconnectAll()
	s.Auth.RotatePairSecret()
	writeJSON(w, s.snapshot())
}

func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/pair/devices/")
	id = strings.Trim(id, "/")
	if id == "" {
		http.Error(w, "device id required", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ok, err := s.Store.RemoveDevice(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.Agent.DisconnectDevice(id)
	writeJSON(w, s.snapshot())
}

type remoteBody struct {
	Enabled bool   `json:"enabled"`
	Server  string `json:"server,omitempty"`
}

func (s *Server) handleRemote(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.snapshot())
	case http.MethodPost:
		var body remoteBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if err := s.Store.SetRemoteAccess(body.Enabled, body.Server); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if s.Remote != nil {
			on, srv, _ := s.Store.RemoteAccess()
			s.Remote.Apply(on, srv)
		}
		writeJSON(w, s.snapshot())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) snapshot() Status {
	secret, exp := s.Auth.PairSecret()
	st, _ := s.Store.LoadState()
	online := map[string]bool{}
	for _, c := range s.Agent.ClientInfos() {
		online[c.ID] = true
	}
	devices := make([]DeviceInfo, 0, len(st.Devices))
	for _, d := range st.Devices {
		devices = append(devices, DeviceInfo{
			ID: d.ID, Name: d.Name, CreatedAt: d.CreatedAt, Online: online[d.ID],
		})
	}
	agentID := ""
	if s.Agent.Identity != nil {
		agentID = s.Agent.Identity.ID
	}
	remoteRegistered := false
	if s.Remote != nil {
		_, _, remoteRegistered = s.Remote.State()
	}
	return Status{
		Running:           true,
		Host:              s.Agent.Host,
		IP:                s.Agent.DisplayHost(), // refresh — don't freeze startup IP
		Port:              s.Agent.Port,
		ControlPort:       s.Port,
		Fingerprint:       s.Auth.Fingerprint(),
		AgentID:           agentID,
		PairURL:           s.Agent.PairURL(),
		PairSecret:        secret,
		PairSecretExpires: exp.Unix(),
		Version:           config.Version,
		ConfigDir:         s.Store.Dir,
		RemoteAccess:      st.RemoteAccess,
		RemoteServer:      st.RemoteServer,
		RemoteRegistered:  remoteRegistered,
		Devices:           devices,
		Clients:           s.Agent.ClientInfos(),
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
