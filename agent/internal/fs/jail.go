package fsaccess

import (
	"os"
	"path/filepath"
	"strings"
)

// SetJail restricts AddRoot (and therefore write roots) to this home directory.
func (s *Service) SetJail(home string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jail = filepath.Clean(home)
	if home != "" {
		s.home = home
	}
}

// SetWriteOwner chowns new/mutated paths so a privileged broker can rewrite
// root-owned files inside the jail, then leave them owned by the Unix user.
// uid 0 disables chown (never claim files as root).
func (s *Service) SetWriteOwner(uid, gid uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if uid == 0 {
		s.own = false
		return
	}
	s.own = true
	s.ownUID = int(uid)
	s.ownGID = int(gid)
}

// Home is the session jail root (the Unix user's home).
func (s *Service) Home() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.home
}

// caller must hold s.mu (RLock is enough).
func (s *Service) jailRoot() string {
	if s.jail != "" {
		return s.jail
	}
	return s.home
}

func (s *Service) pathInJail(abs string) bool {
	j := filepath.Clean(s.jailRoot())
	if j == "" {
		return true
	}
	abs = filepath.Clean(abs)
	sep := string(os.PathSeparator)
	return abs == j || strings.HasPrefix(abs, j+sep)
}

func (s *Service) chownPath(path string) {
	s.mu.RLock()
	own, uid, gid := s.own, s.ownUID, s.ownGID
	s.mu.RUnlock()
	if !own || path == "" {
		return
	}
	_ = os.Lchown(path, uid, gid)
}

func (s *Service) chownUp(path string) {
	s.mu.RLock()
	jail := filepath.Clean(s.jailRoot())
	own := s.own
	s.mu.RUnlock()
	if !own {
		return
	}
	p := filepath.Clean(path)
	for {
		s.chownPath(p)
		if p == jail || p == string(os.PathSeparator) || p == "." {
			break
		}
		next := filepath.Dir(p)
		if next == p {
			break
		}
		p = next
	}
}

// OwnTree chowns every non-symlink path under root (used after git mutations).
func (s *Service) OwnTree(root string) {
	s.mu.RLock()
	own := s.own
	s.mu.RUnlock()
	if !own || root == "" {
		return
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		s.chownPath(path)
		return nil
	})
}
