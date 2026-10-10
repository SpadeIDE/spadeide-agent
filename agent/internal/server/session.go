package server

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/spadeide/spade-agent/internal/config"
	fsaccess "github.com/spadeide/spade-agent/internal/fs"
	gitpkg "github.com/spadeide/spade-agent/internal/git"
	"github.com/spadeide/spade-agent/internal/hostauth"
	"github.com/spadeide/spade-agent/internal/platform"
	"github.com/spadeide/spade-agent/internal/protocol"
	ptypkg "github.com/spadeide/spade-agent/internal/pty"
)

func (s *Server) attachClient(c *client) {
	c.srv = s
	if c.git == nil {
		c.git = gitpkg.New()
	}
	if config.IsSystemMode() {
		dev := c.pairedDevice()
		if dev != nil && dev.UnixUser != "" {
			if err := s.bindUnixUser(c, dev); err != nil {
				log.Printf("unix bind %q failed for %s: %v — host.login required", dev.UnixUser, dev.ID, err)
				s.unbindUnix(c)
				return
			}
			return
		}
		s.attachUnbound(c)
		return
	}
	home, _ := os.UserHomeDir()
	s.attachUserHome(c, home)
}

func (s *Server) unbindUnix(c *client) {
	dev := c.pairedDevice()
	if dev != nil && s.Store != nil {
		if err := s.Store.ClearDeviceUnixBind(dev.ID); err != nil {
			log.Printf("clear unix bind: %v", err)
		}
		c.updateDevice(func(d *config.Device) {
			d.UnixUser = ""
			d.UnixUID = 0
			d.UnixPasswdID = ""
		})
	}
	s.attachUnbound(c)
}

func (s *Server) attachUnbound(c *client) {
	c.setBoundUser("")
	home := filepath.Join(config.SystemStateDir, ".unbound")
	s.replaceFS(c, home, false, 0, 0)
	shell := platform.DefaultShell()
	s.replacePTY(c, shell, ptypkg.Identity{})
}

func (s *Server) attachUserHome(c *client, home string) {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	c.setBoundUser(platform.UserName())
	s.replaceFS(c, home, false, 0, 0)
	s.replacePTY(c, platform.DefaultShell(), ptypkg.Identity{})
}

func (s *Server) bindUnixUser(c *client, dev *config.Device) error {
	if dev == nil {
		return hostauth.ErrNoAccount
	}
	acct, err := hostauth.LookupAccount(dev.UnixUser)
	if err != nil {
		return err
	}
	if dev.UnixUID != 0 && dev.UnixUID != acct.UID {
		return hostauth.ErrAccountRecycled
	}
	id, err := hostauth.PasswdID(acct.Name)
	if err != nil {
		return err
	}
	if dev.UnixPasswdID == "" || id != dev.UnixPasswdID {
		return hostauth.ErrPasswdChanged
	}
	s.attachAccount(c, acct)
	return nil
}

func (s *Server) attachAccount(c *client, acct *hostauth.Account) {
	c.setBoundUser(acct.Name)
	privileged := os.Geteuid() == 0 && acct.UID != 0
	s.replaceFS(c, acct.Home, privileged, acct.UID, acct.GID)
	shell := acct.Shell
	if shell == "" {
		shell = platform.DefaultShell()
	}
	s.replacePTY(c, shell, ptypkg.Identity{
		UID:    acct.UID,
		GID:    acct.GID,
		Groups: acct.Groups,
		Home:   acct.Home,
		User:   acct.Name,
		Shell:  shell,
	})
}

// replaceFS swaps the session filesystem under sessMu and closes the old
// instance outside the lock so in-flight handlers keep a stable pointer.
func (s *Server) replaceFS(c *client, home string, own bool, uid, gid uint32) {
	next := fsaccess.New(home, func(env protocol.Envelope) { c.send(env) })
	next.SetJail(home)
	if own {
		next.SetWriteOwner(uid, gid)
	}
	c.sessMu.Lock()
	old := c.fs
	c.fs = next
	c.sessMu.Unlock()
	if old != nil {
		old.Close()
	}
}

func (s *Server) replacePTY(c *client, shell string, ident ptypkg.Identity) {
	next := ptypkg.NewManager(shell, func(env protocol.Envelope) { c.send(env) })
	next.SetIdentity(ident)
	c.sessMu.Lock()
	old := c.pty
	c.pty = next
	c.sessMu.Unlock()
	if old != nil {
		old.CloseAll()
	}
}

func (c *client) filesystem() *fsaccess.Service {
	c.sessMu.RLock()
	defer c.sessMu.RUnlock()
	return c.fs
}

func (c *client) terminal() *ptypkg.Manager {
	c.sessMu.RLock()
	defer c.sessMu.RUnlock()
	return c.pty
}

func (c *client) boundUser() string {
	c.sessMu.RLock()
	defer c.sessMu.RUnlock()
	return c.unixUser
}

func (c *client) setBoundUser(user string) {
	c.sessMu.Lock()
	c.unixUser = user
	c.sessMu.Unlock()
}

func (c *client) pairedDevice() *config.Device {
	c.sessMu.RLock()
	defer c.sessMu.RUnlock()
	return c.device
}

func (c *client) updateDevice(fn func(*config.Device)) {
	c.sessMu.Lock()
	defer c.sessMu.Unlock()
	if c.device != nil {
		fn(c.device)
	}
}

// closeSessionIO closes PTY/FS but leaves the pointers in place. Teardown races
// with in-flight RPC goroutines (dispatchInbound); niling would panic on
// c.filesystem().Grep etc. Both Close paths are idempotent.
func (c *client) closeSessionIO() {
	c.sessMu.RLock()
	fs, pty := c.fs, c.pty
	c.sessMu.RUnlock()
	if pty != nil {
		pty.CloseAll()
	}
	if fs != nil {
		fs.Close()
	}
}

func (c *client) needsHostLogin() bool {
	return config.IsSystemMode() && c.boundUser() == ""
}

func (c *client) requireBound(env protocol.Envelope) bool {
	if !c.needsHostLogin() {
		return true
	}
	switch env.Method {
	case protocol.MethodHello, protocol.MethodPing, protocol.MethodHostLogin:
		return true
	}
	c.send(protocol.NewError(env.ID, protocol.CodeUnauthorized, "unix login required"))
	return false
}

func (c *client) sessionHome() string {
	if fs := c.filesystem(); fs != nil {
		if h := fs.Home(); h != "" && filepath.Base(h) != ".unbound" {
			return h
		}
	}
	if !config.IsSystemMode() {
		h, _ := os.UserHomeDir()
		return h
	}
	return ""
}

func (c *client) sessionShell() string {
	if pty := c.terminal(); pty != nil {
		if sh := pty.LoginShell(); sh != "" {
			return sh
		}
	}
	return platform.DefaultShell()
}

func (c *client) ownGit(root string) {
	if fs := c.filesystem(); fs != nil && root != "" {
		fs.OwnTree(root)
	}
}

func (s *Server) hostLogin(c *client, env protocol.Envelope) {
	if !config.IsSystemMode() {
		c.send(protocol.NewError(env.ID, protocol.CodeForbidden, "unix login is only used on system installs"))
		return
	}
	if u := c.boundUser(); u != "" {
		c.send(protocol.NewResponse(env.ID, protocol.HostLoginResult{
			UnixUser: u,
			Home:     c.sessionHome(),
		}))
		return
	}
	key := "anon"
	if dev := c.pairedDevice(); dev != nil && dev.ID != "" {
		key = dev.ID
	}
	if s.loginLimit != nil && !s.loginLimit.Allow(key) {
		c.send(protocol.NewError(env.ID, protocol.CodeUnauthorized, "too many login attempts"))
		s.dropAfterReply(c)
		return
	}
	p, ok := decodeParams[protocol.HostLoginParams](c, env)
	if !ok {
		return
	}
	username := p.Username
	password := p.Password
	p.Password = ""

	acct, err := hostauth.Authenticate(username, password)
	password = ""
	if err != nil {
		if s.loginLimit != nil {
			s.loginLimit.Failure(key)
		}
		msg := "invalid username or password"
		if errors.Is(err, hostauth.ErrRefusedRoot) {
			msg = "root login is not allowed"
		}
		log.Printf("host.login failed device=%s user=%s: %v", key, username, err)
		c.send(protocol.NewError(env.ID, protocol.CodeUnauthorized, msg))
		s.dropAfterReply(c)
		return
	}
	if s.loginLimit != nil {
		s.loginLimit.Success(key)
	}
	s.attachAccount(c, acct)
	if dev := c.pairedDevice(); dev != nil && s.Store != nil {
		passwdID, err := hostauth.PasswdID(acct.Name)
		if err != nil {
			log.Printf("passwd id after login: %v", err)
			passwdID = ""
		}
		if err := s.Store.SetDeviceUnixBind(dev.ID, acct.Name, acct.UID, passwdID); err != nil {
			log.Printf("persist unixUser: %v", err)
		} else {
			c.updateDevice(func(d *config.Device) {
				d.UnixUser = acct.Name
				d.UnixUID = acct.UID
				d.UnixPasswdID = passwdID
			})
		}
	}
	log.Printf("host.login ok device=%s user=%s", key, acct.Name)
	c.send(protocol.NewResponse(env.ID, protocol.HostLoginResult{
		UnixUser: acct.Name,
		Home:     acct.Home,
	}))
}

func (s *Server) dropAfterReply(c *client) {
	if c.forceClose == nil {
		return
	}
	go func() {
		time.Sleep(80 * time.Millisecond)
		c.forceClose()
	}()
}
