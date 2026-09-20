package pty

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
	"github.com/spadeide/spade-agent/internal/platform"
	"github.com/spadeide/spade-agent/internal/protocol"
)

type Session struct {
	ID   string
	cmd  *exec.Cmd
	file *os.File
}

type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	onEvent  func(protocol.Envelope)
	shell    string
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

func (m *Manager) Create(cols, rows int, cwd string) (string, error) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	if cwd == "" {
		cwd = platform.UserHome()
	}
	if cwd == "" {
		cwd = "."
	}
	// Ensure cwd exists; fall back to home.
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		if h := platform.UserHome(); h != "" {
			cwd = h
		}
	}

	cmd := platform.ShellCommand(m.shell)
	cmd.Dir = cwd
	cmd.Env = platform.EnrichEnv(os.Environ(), cols, rows)

	ptmx, err := platform.StartPTY(cmd)
	if err != nil {
		return "", fmt.Errorf("start %s: %w", filepath.Base(m.shell), err)
	}
	// A failed initial resize is not fatal — the client resizes again on layout.
	_ = platform.SetSize(ptmx, cols, rows)

	id := uuid.NewString()
	sess := &Session{ID: id, cmd: cmd, file: ptmx}
	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()

	go m.readLoop(sess)
	go m.waitLoop(sess)
	return id, nil
}

func (m *Manager) readLoop(sess *Session) {
	buf := make([]byte, 4096)
	for {
		n, err := sess.file.Read(buf)
		if n > 0 && m.onEvent != nil {
			m.onEvent(protocol.NewEvent(protocol.EventPTYData, protocol.PTYDataParams{
				PTYID: sess.ID,
				Data:  base64.StdEncoding.EncodeToString(buf[:n]),
			}))
		}
		if err != nil {
			return // EOF or a closed PTY; the exit event comes from waitLoop
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
	if sess.cmd.Process != nil {
		_ = sess.cmd.Process.Kill()
	}
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
