package pty

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/spadeide/spade-agent/internal/platform"
	"github.com/spadeide/spade-agent/internal/protocol"
)

// C4 PTY read path: larger reads + short coalesce so bulk output is fewer
// envelopes while interactive keystrokes still flush within ptyCoalesceWait.
const (
	ptyReadSize     = 32 << 10 // 32 KiB
	ptyCoalesceWait = 5 * time.Millisecond
)

type Session struct {
	ID   string
	cmd  *exec.Cmd
	file *os.File
}

// Identity is the Unix account a PTY must run as (system-mode broker).
// UID 0 is ignored — the agent never starts a root shell.
type Identity struct {
	UID    uint32
	GID    uint32
	Groups []uint32 // existing memberships only (docker, sudo, …)
	Home   string
	User   string
	Shell  string
}

// MaxSessions caps concurrent PTYs per client manager.
const MaxSessions = 16

type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	onEvent  func(protocol.Envelope)
	shell    string
	ident    Identity
}

func NewManager(shell string, onEvent func(protocol.Envelope)) *Manager {
	if shell == "" {
		shell = platform.DefaultShell()
	}
	return &Manager{
		sessions: make(map[string]*Session),
		onEvent:  onEvent,
		shell:    shell,
	}
}

func (m *Manager) SetIdentity(id Identity) {
	if id.UID == 0 {
		id = Identity{}
	}
	m.mu.Lock()
	m.ident = id
	if id.Shell != "" {
		m.shell = id.Shell
	}
	m.mu.Unlock()
}

func (m *Manager) LoginShell() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ident.Shell != "" {
		return m.ident.Shell
	}
	return m.shell
}

func (m *Manager) Create(cols, rows int, cwd string) (string, error) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	m.mu.Lock()
	ident := m.ident
	shell := m.shell
	nSess := len(m.sessions)
	m.mu.Unlock()

	if nSess >= MaxSessions {
		return "", fmt.Errorf("too many terminals")
	}
	if ident.User != "" && ident.UID == 0 {
		return "", fmt.Errorf("refusing root pty")
	}

	home := ident.Home
	if home == "" {
		home = platform.UserHome()
	}
	if cwd == "" {
		cwd = home
	}
	if cwd == "" {
		cwd = "."
	}
	// Ensure cwd exists; fall back to home.
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		if home != "" {
			cwd = home
		}
	}

	cmd := platform.ShellCommand(shell)
	cmd.Dir = cwd
	cmd.Env = platform.EnrichEnv(os.Environ(), cols, rows)
	if ident.User != "" {
		overrides := map[string]string{
			"HOME":    ident.Home,
			"USER":    ident.User,
			"LOGNAME": ident.User,
			"SHELL":   shell,
			// Don't leak the broker's systemd / sudo context into the user shell.
			"SUDO_USER":        "",
			"SUDO_UID":         "",
			"SUDO_GID":         "",
			"SUDO_COMMAND":     "",
			"SPADE_AGENT_MODE": "",
			"SPADE_AGENT_HOME": "",
			"INVOCATION_ID":    "",
		}
		if ident.UID != 0 {
			runtimeDir := fmt.Sprintf("/run/user/%d", ident.UID)
			if st, err := os.Stat(runtimeDir); err == nil && st.IsDir() {
				overrides["XDG_RUNTIME_DIR"] = runtimeDir
			}
		}
		cmd.Env = platform.OverrideEnv(cmd.Env, overrides)
		platform.SetCredential(cmd, ident.UID, ident.GID, ident.Groups)
	}

	ptmx, err := platform.StartPTY(cmd)
	if err != nil {
		return "", fmt.Errorf("start %s: %w", filepath.Base(shell), err)
	}
	// A failed initial resize is not fatal — the client resizes again on layout.
	_ = platform.SetSize(ptmx, cols, rows)

	id := uuid.NewString()
	sess := &Session{ID: id, cmd: cmd, file: ptmx}
	m.mu.Lock()
	if len(m.sessions) >= MaxSessions {
		m.mu.Unlock()
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = platform.KillGroup(cmd)
		}
		return "", fmt.Errorf("too many terminals")
	}
	m.sessions[id] = sess
	m.mu.Unlock()

	go m.readLoop(sess)
	go m.waitLoop(sess)
	return id, nil
}

func (m *Manager) readLoop(sess *Session) {
	type readResult struct {
		b   []byte
		err error
	}
	// Dedicated reader: PTY masters on macOS don't reliably honour read deadlines.
	reads := make(chan readResult, 1)
	go func() {
		buf := make([]byte, ptyReadSize)
		for {
			n, err := sess.file.Read(buf)
			var b []byte
			if n > 0 {
				b = append([]byte(nil), buf[:n]...)
			}
			reads <- readResult{b: b, err: err}
			if err != nil {
				return
			}
		}
	}()

	pending := make([]byte, 0, ptyReadSize)
	var timer *time.Timer
	var timerC <-chan time.Time

	stopTimer := func() {
		if timer == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timerC:
			default:
			}
		}
		timer = nil
		timerC = nil
	}

	flush := func() {
		stopTimer()
		if len(pending) == 0 {
			return
		}
		chunk := pending
		pending = make([]byte, 0, ptyReadSize)
		if m.onEvent == nil {
			return
		}
		m.onEvent(protocol.NewEvent(protocol.EventPTYData, protocol.PTYDataParams{
			PTYID: sess.ID,
			Data:  base64.StdEncoding.EncodeToString(chunk),
		}))
	}

	for {
		select {
		case r := <-reads:
			if len(r.b) > 0 {
				pending = append(pending, r.b...)
				if len(pending) >= ptyReadSize {
					flush()
				} else if timer == nil {
					timer = time.NewTimer(ptyCoalesceWait)
					timerC = timer.C
				}
			}
			if r.err != nil {
				flush()
				return // EOF or closed PTY; exit event comes from waitLoop
			}
		case <-timerC:
			flush()
		}
	}
}

func (m *Manager) waitLoop(sess *Session) {
	err := sess.cmd.Wait()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = 1
		}
	}
	_ = sess.file.Close()
	m.mu.Lock()
	delete(m.sessions, sess.ID)
	m.mu.Unlock()
	if m.onEvent != nil {
		m.onEvent(protocol.NewEvent(protocol.EventPTYExit, protocol.PTYExitParams{
			PTYID: sess.ID,
			Code:  code,
		}))
	}
}

func (m *Manager) Input(id, dataB64 string) error {
	data, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return fmt.Errorf("invalid base64")
	}
	m.mu.Lock()
	sess := m.sessions[id]
	m.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("pty not found")
	}
	_, err = sess.file.Write(data)
	return err
}

func (m *Manager) Resize(id string, cols, rows int) error {
	m.mu.Lock()
	sess := m.sessions[id]
	m.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("pty not found")
	}
	return platform.SetSize(sess.file, cols, rows)
}

func (m *Manager) Close(id string) error {
	m.mu.Lock()
	sess := m.sessions[id]
	if sess != nil {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if sess == nil {
		return nil
	}
	_ = sess.file.Close()
	_ = platform.KillGroup(sess.cmd)
	return nil
}

func (m *Manager) CloseAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Close(id)
	}
}
