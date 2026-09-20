package config

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	AppName            = "spade-agent"
	DefaultPort        = 7422
	DefaultControlPort = 7423
)

// Version is set at build time via -ldflags when packaging.
var Version = "0.1.0"

type Store struct {
	Dir string
}

type Device struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token,omitempty"`     // legacy; unused (mTLS only)
	DevicePub string `json:"devicePub,omitempty"` // base64 SPKI (P-256) — session auth
	CreatedAt int64  `json:"createdAt"`
}

type State struct {
	Devices      []Device `json:"devices"`
	RemoteAccess bool     `json:"remoteAccess"`           // default false — never dial rendezvous when off
	RemoteServer string   `json:"remoteServer,omitempty"` // wss://… override; empty = product default later
	// RvSecret is a durable 32-byte key (base64) shared with paired iPads for
	// blinded rendezvous handles. Distinct from the ephemeral QR pair secret.
	RvSecret string `json:"rvSecret,omitempty"`
}

func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".spade-agent"), nil
}

func NewStore(dir string) (*Store, error) {
	if dir == "" {
		var err error
		dir, err = DefaultDir()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

func (s *Store) CertDir() string   { return filepath.Join(s.Dir, "certs") }
func (s *Store) StatePath() string { return filepath.Join(s.Dir, "state.json") }
func (s *Store) PIDPath() string   { return filepath.Join(s.Dir, "agent.pid") }
func (s *Store) LogPath() string   { return filepath.Join(s.Dir, "agent.log") }
func (s *Store) ControlTokenPath() string {
	return filepath.Join(s.Dir, "control.token")
}
func (s *Store) ControlPortPath() string {
	return filepath.Join(s.Dir, "control.port")
}

func (s *Store) WritePID(pid int) error {
	return os.WriteFile(s.PIDPath(), []byte(fmt.Sprintf("%d\n", pid)), 0o600)
}

func (s *Store) ClearPID() {
	_ = os.Remove(s.PIDPath())
}

func (s *Store) WriteControlPort(port int) error {
	return os.WriteFile(s.ControlPortPath(), []byte(fmt.Sprintf("%d\n", port)), 0o600)
}

// WriteControlToken persists the loopback control API bearer (0600).
func (s *Store) WriteControlToken(token string) error {
	return os.WriteFile(s.ControlTokenPath(), []byte(token+"\n"), 0o600)
}

// ClearControlToken removes the on-disk token (best-effort on shutdown).
func (s *Store) ClearControlToken() {
	_ = os.Remove(s.ControlTokenPath())
}

// ReadControlToken returns the trimmed token from disk, or empty if missing.
func (s *Store) ReadControlToken() string {
	data, err := os.ReadFile(s.ControlTokenPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (s *Store) LoadState() (*State, error) {
	data, err := os.ReadFile(s.StatePath())
	if errors.Is(err, os.ErrNotExist) {
		return &State{}, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) SaveState(st *State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.StatePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.StatePath())
}

func (s *Store) FindDeviceByToken(token string) (*Device, error) {
	st, err := s.LoadState()
	if err != nil {
		return nil, err
	}
	for i := range st.Devices {
		if st.Devices[i].Token == token {
			return &st.Devices[i], nil
		}
	}
	return nil, fmt.Errorf("device not found")
}

func (s *Store) UpsertDevice(d Device) error {
	st, err := s.LoadState()
	if err != nil {
		return err
	}
	for i := range st.Devices {
		if st.Devices[i].ID == d.ID {
			st.Devices[i] = d
			return s.SaveState(st)
		}
	}
	st.Devices = append(st.Devices, d)
	return s.SaveState(st)
}

func (s *Store) ResetPairing() error {
	st, err := s.LoadState()
	if err != nil {
		return err
	}
	st.Devices = nil
	return s.SaveState(st)
}

// RemoveDevice deletes one paired device by id. Returns false if not found.
func (s *Store) RemoveDevice(id string) (bool, error) {
	st, err := s.LoadState()
	if err != nil {
		return false, err
	}
	next := make([]Device, 0, len(st.Devices))
	found := false
	for _, d := range st.Devices {
		if d.ID == id {
			found = true
			continue
		}
		next = append(next, d)
	}
	if !found {
		return false, nil
	}
	st.Devices = next
	return true, s.SaveState(st)
}

func (s *Store) FindDeviceByID(id string) (*Device, error) {
	st, err := s.LoadState()
	if err != nil {
		return nil, err
	}
	for i := range st.Devices {
		if st.Devices[i].ID == id {
			return &st.Devices[i], nil
		}
	}
	return nil, fmt.Errorf("device not found")
}

// FindDeviceByPubSPKI matches a client-cert SubjectPublicKeyInfo (DER) against
// stored DevicePub (base64 SPKI from iPad pairing).
func (s *Store) FindDeviceByPubSPKI(spki []byte) (*Device, error) {
	if len(spki) == 0 {
		return nil, fmt.Errorf("device not found")
	}
	st, err := s.LoadState()
	if err != nil {
		return nil, err
	}
	for i := range st.Devices {
		pub := st.Devices[i].DevicePub
		if pub == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(pub)
		if err != nil || len(raw) != len(spki) {
			continue
		}
		if subtle.ConstantTimeCompare(raw, spki) == 1 {
			return &st.Devices[i], nil
		}
	}
	return nil, fmt.Errorf("device not found")
}

func (s *Store) SetRemoteAccess(enabled bool, server string) error {
	st, err := s.LoadState()
	if err != nil {
		return err
	}
	st.RemoteAccess = enabled
	if server != "" {
		st.RemoteServer = server
	}
	return s.SaveState(st)
}

func (s *Store) RemoteAccess() (enabled bool, server string, err error) {
	st, err := s.LoadState()
	if err != nil {
		return false, "", err
	}
	return st.RemoteAccess, st.RemoteServer, nil
}

// EnsureRvSecret mints a 32-byte rendezvous secret if missing and returns raw bytes.
func (s *Store) EnsureRvSecret() ([]byte, error) {
	st, err := s.LoadState()
	if err != nil {
		return nil, err
	}
	if st.RvSecret != "" {
		raw, err := base64.StdEncoding.DecodeString(st.RvSecret)
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("corrupt rvSecret")
		}
		return raw, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	st.RvSecret = base64.StdEncoding.EncodeToString(raw)
	if err := s.SaveState(st); err != nil {
		return nil, err
	}
	return raw, nil
}

// RvSecretBytes returns the raw secret, or nil if none yet.
func (s *Store) RvSecretBytes() ([]byte, error) {
	st, err := s.LoadState()
	if err != nil {
		return nil, err
	}
	if st.RvSecret == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(st.RvSecret)
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("corrupt rvSecret")
	}
	return raw, nil
}

// RvSecretBase64 returns the wire encoding, or empty if none.
func (s *Store) RvSecretBase64() (string, error) {
	st, err := s.LoadState()
	if err != nil {
		return "", err
	}
	return st.RvSecret, nil
}
