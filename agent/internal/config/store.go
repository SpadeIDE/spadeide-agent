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
	"sync"
	"time"
)

const (
	AppName            = "spade-agent"
	DefaultPort        = 7422
	DefaultControlPort = 7423
	// SystemStateDir holds pairings when the systemd unit runs as a privileged broker.
	SystemStateDir = "/var/lib/spade-agent"
)

// Version is set at build time via -ldflags when packaging.
var Version = "0.1.0"

type Store struct {
	Dir string

	mu       sync.Mutex
	state    *State // cached; nil until first load
	diskSize int64
	diskMod  time.Time // mtime of state.json when cache was loaded/saved
}

type Device struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	DevicePub string `json:"devicePub,omitempty"` // base64 SPKI (P-256) — session auth
	CreatedAt int64  `json:"createdAt"`
	// RvSecret is this tablet's durable 32-byte blinded-rendezvous key (base64).
	// Per-device: unpair drops only this secret; other tablets keep working.
	RvSecret string `json:"rvSecret,omitempty"`
	// UnixUser is the host account this tablet is bound to (system mode).
	UnixUser string `json:"unixUser,omitempty"`
	// UnixUID is the uid recorded at bind. Recreated accounts with the same
	// name must log in again.
	UnixUID uint32 `json:"unixUid,omitempty"`
	// UnixPasswdID is a SHA-256 of the shadow password hash (never the password).
	// A password change or lock invalidates the bind.
	UnixPasswdID string `json:"unixPasswdId,omitempty"`
}

type State struct {
	Devices      []Device `json:"devices"`
	RemoteAccess bool     `json:"remoteAccess"`           // default false — never dial rendezvous when off
	RemoteServer string   `json:"remoteServer,omitempty"` // wss://… override; empty = product default later
	// RvSecret is the legacy host-level blind key (base64). Kept for migration
	// onto per-device secrets; new pairs mint Device.RvSecret instead.
	RvSecret string `json:"rvSecret,omitempty"`
}

func DefaultDir() (string, error) {
	if d := EnvHome(); d != "" {
		return d, nil
	}
	if IsSystemMode() {
		return SystemStateDir, nil
	}
	// Root talking to an already-installed system unit (QR / status).
	if os.Geteuid() == 0 {
		if st, err := os.Stat(filepath.Join(SystemStateDir, "control.token")); err == nil && !st.IsDir() {
			return SystemStateDir, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".spade-agent"), nil
}

// IsSystemMode is the privileged broker (systemd unit as root), not a user install.
func IsSystemMode() bool {
	if EnvMode() == "system" {
		return true
	}
	return os.Geteuid() == 0 && EnvUnderSystemd()
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

func cloneState(st *State) *State {
	if st == nil {
		return &State{}
	}
	out := *st
	if st.Devices != nil {
		out.Devices = append([]Device(nil), st.Devices...)
	}
	return &out
}

func (s *Store) loadFromDisk() (*State, error) {
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

// loadLocked loads from disk when the cache is cold or state.json changed
// underfoot (CLI writing while the daemon is up — cmdRemote/cmdPairReset).
func (s *Store) loadLocked() error {
	if s.state != nil && !s.diskChangedLocked() {
		return nil
	}
	st, err := s.loadFromDisk()
	if err != nil {
		return err
	}
	s.state = st
	s.rememberDiskLocked()
	return nil
}

func (s *Store) diskChangedLocked() bool {
	fi, err := os.Stat(s.StatePath())
	if errors.Is(err, os.ErrNotExist) {
		return s.diskSize != 0 || !s.diskMod.IsZero()
	}
	if err != nil {
		return true
	}
	return fi.Size() != s.diskSize || !fi.ModTime().Equal(s.diskMod)
}

func (s *Store) rememberDiskLocked() {
	fi, err := os.Stat(s.StatePath())
	if err != nil {
		s.diskSize = 0
		s.diskMod = time.Time{}
		return
	}
	s.diskSize = fi.Size()
	s.diskMod = fi.ModTime()
}

// update runs fn against the live state under the lock and persists the result.
func (s *Store) update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	if err := fn(s.state); err != nil {
		return err
	}
	return s.saveStateLocked(s.state)
}

// view runs fn against a read-only snapshot of the cached state.
func (s *Store) view(fn func(*State)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	fn(s.state)
	return nil
}

// LoadState returns a deep copy of the current state.
func (s *Store) LoadState() (*State, error) {
	var out *State
	err := s.view(func(st *State) {
		out = cloneState(st)
	})
	return out, err
}

func (s *Store) saveStateLocked(st *State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.StatePath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.StatePath()); err != nil {
		return err
	}
	// Persist the directory entry so a crash after rename cannot leave
	// state.json missing on writeback filesystems.
	if dir, err := os.Open(s.Dir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	s.rememberDiskLocked()
	return nil
}

// SaveState replaces on-disk state (and the in-memory cache) with st.
func (s *Store) SaveState(st *State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := cloneState(st)
	if err := s.saveStateLocked(cp); err != nil {
		return err
	}
	s.state = cp
	return nil
}

func (s *Store) UpsertDevice(d Device) error {
	return s.update(func(st *State) error {
		for i := range st.Devices {
			if st.Devices[i].ID == d.ID {
				st.Devices[i] = d
				return nil
			}
		}
		st.Devices = append(st.Devices, d)
		return nil
	})
}

func (s *Store) ResetPairing() error {
	return s.update(func(st *State) error {
		st.Devices = nil
		return nil
	})
}

// RemoveDevice deletes one paired device by id. Returns false if not found.
func (s *Store) RemoveDevice(id string) (bool, error) {
	var found bool
	err := s.update(func(st *State) error {
		next := make([]Device, 0, len(st.Devices))
		for _, d := range st.Devices {
			if d.ID == id {
				found = true
				continue
			}
			next = append(next, d)
		}
		if !found {
			return errSkipPersist
		}
		st.Devices = next
		return nil
	})
	if errors.Is(err, errSkipPersist) {
		return false, nil
	}
	return found, err
}

// errSkipPersist aborts update without writing (no-op mutation).
var errSkipPersist = errors.New("skip persist")

func (s *Store) SetDeviceUnixBind(id, unixUser string, uid uint32, passwdID string) error {
	return s.update(func(st *State) error {
		for i := range st.Devices {
			if st.Devices[i].ID == id {
				st.Devices[i].UnixUser = unixUser
				st.Devices[i].UnixUID = uid
				st.Devices[i].UnixPasswdID = passwdID
				return nil
			}
		}
		return fmt.Errorf("device not found")
	})
}

func (s *Store) ClearDeviceUnixBind(id string) error {
	return s.SetDeviceUnixBind(id, "", 0, "")
}

func (s *Store) FindDeviceByID(id string) (*Device, error) {
	var found *Device
	err := s.view(func(st *State) {
		for i := range st.Devices {
			if st.Devices[i].ID == id {
				d := st.Devices[i]
				found = &d
				return
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("device not found")
	}
	return found, nil
}

// FindDeviceByPubSPKI matches a client-cert SubjectPublicKeyInfo (DER) against
// stored DevicePub (base64 SPKI from iPad pairing).
func (s *Store) FindDeviceByPubSPKI(spki []byte) (*Device, error) {
	if len(spki) == 0 {
		return nil, fmt.Errorf("device not found")
	}
	var found *Device
	err := s.view(func(st *State) {
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
				d := st.Devices[i]
				found = &d
				return
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("device not found")
	}
	return found, nil
}

func (s *Store) SetRemoteAccess(enabled bool, server string) error {
	return s.update(func(st *State) error {
		st.RemoteAccess = enabled
		st.RemoteServer = strings.TrimSpace(server)
		return nil
	})
}

// UpdateRemoteAccess merges optional enabled/server fields under one lock.
func (s *Store) UpdateRemoteAccess(enabled *bool, server *string) error {
	return s.update(func(st *State) error {
		if enabled != nil {
			st.RemoteAccess = *enabled
		}
		if server != nil {
			st.RemoteServer = strings.TrimSpace(*server)
		}
		return nil
	})
}

func (s *Store) RemoteAccess() (enabled bool, server string, err error) {
	err = s.view(func(st *State) {
		enabled = st.RemoteAccess
		server = st.RemoteServer
	})
	return enabled, server, err
}

func decodeRvSecret(b64 string) ([]byte, error) {
	if b64 == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(decoded) != 32 {
		return nil, fmt.Errorf("corrupt rvSecret")
	}
	return decoded, nil
}

func mintRvSecretB64() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	return base64.StdEncoding.EncodeToString(raw), raw, nil
}

// EnsureRvSecret mints a legacy host-level secret if missing (migration only).
func (s *Store) EnsureRvSecret() ([]byte, error) {
	var raw []byte
	err := s.update(func(st *State) error {
		if st.RvSecret != "" {
			decoded, err := decodeRvSecret(st.RvSecret)
			if err != nil {
				return err
			}
			raw = decoded
			return errSkipPersist
		}
		b64, decoded, err := mintRvSecretB64()
		if err != nil {
			return err
		}
		st.RvSecret = b64
		raw = decoded
		return nil
	})
	if errors.Is(err, errSkipPersist) {
		return raw, nil
	}
	return raw, err
}

// RvSecretBytes returns the legacy host-level secret, or nil if none.
func (s *Store) RvSecretBytes() ([]byte, error) {
	var raw []byte
	var corrupt error
	err := s.view(func(st *State) {
		decoded, e := decodeRvSecret(st.RvSecret)
		if e != nil {
			corrupt = e
			return
		}
		raw = decoded
	})
	if err != nil {
		return nil, err
	}
	if corrupt != nil {
		return nil, corrupt
	}
	return raw, nil
}

// RvSecretBase64 returns the legacy host-level wire encoding, or empty if none.
func (s *Store) RvSecretBase64() (string, error) {
	var out string
	err := s.view(func(st *State) {
		out = st.RvSecret
	})
	return out, err
}

// EnsureDeviceRvSecret returns this device's rvSecret, minting one if missing.
// If the device still holds a copy of the legacy host-level secret (shared with
// every tablet after MigrateLegacyRvSecrets), mint a unique replacement so a
// later unpair cannot leave that tablet able to derive other devices' rids.
func (s *Store) EnsureDeviceRvSecret(deviceID string) (string, error) {
	var out string
	err := s.update(func(st *State) error {
		for i := range st.Devices {
			if st.Devices[i].ID != deviceID {
				continue
			}
			cur := st.Devices[i].RvSecret
			if cur != "" {
				if _, err := decodeRvSecret(cur); err != nil {
					return err
				}
				// Shared legacy copy → upgrade to unique on pair/re-pair.
				if st.RvSecret != "" && cur == st.RvSecret {
					b64, _, err := mintRvSecretB64()
					if err != nil {
						return err
					}
					st.Devices[i].RvSecret = b64
					out = b64
					return nil
				}
				out = cur
				return errSkipPersist
			}
			b64, _, err := mintRvSecretB64()
			if err != nil {
				return err
			}
			st.Devices[i].RvSecret = b64
			out = b64
			return nil
		}
		return fmt.Errorf("device not found")
	})
	if errors.Is(err, errSkipPersist) {
		return out, nil
	}
	return out, err
}

// MigrateLegacyRvSecrets copies the host-level RvSecret onto any paired device
// that still lacks its own. Safe to call on every start.
func (s *Store) MigrateLegacyRvSecrets() (int, error) {
	var n int
	err := s.update(func(st *State) error {
		if st.RvSecret == "" {
			return errSkipPersist
		}
		if _, err := decodeRvSecret(st.RvSecret); err != nil {
			return err
		}
		for i := range st.Devices {
			if st.Devices[i].RvSecret != "" {
				continue
			}
			st.Devices[i].RvSecret = st.RvSecret
			n++
		}
		if n == 0 {
			return errSkipPersist
		}
		return nil
	})
	if errors.Is(err, errSkipPersist) {
		return 0, nil
	}
	return n, err
}

// AllDeviceRvSecrets returns raw 32-byte secrets for every paired device that
// has one. Used to advertise all current rids on the rendezvous registration.
func (s *Store) AllDeviceRvSecrets() ([][]byte, error) {
	var out [][]byte
	var corrupt error
	err := s.view(func(st *State) {
		for i := range st.Devices {
			raw, e := decodeRvSecret(st.Devices[i].RvSecret)
			if e != nil {
				corrupt = e
				return
			}
			if len(raw) == 32 {
				out = append(out, raw)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if corrupt != nil {
		return nil, corrupt
	}
	return out, nil
}
